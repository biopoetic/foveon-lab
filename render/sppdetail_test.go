package render

import (
	"bytes"
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// detailFrame is a raw Merrill frame with fine texture for the sharpening
// to act on: a soft gradient with a pixel-scale pattern.
func detailFrame() *Base {
	const w, h = 96, 64
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, Frame: w, NativeFrame: w, Raw: true, ISO: 100, Sensor: "Merrill"}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 0.18 + 0.10*float64(x)/w + 0.03*math.Sin(float64(x)*1.3)*math.Cos(float64(y)*1.1)
			i := (y*w + x) * 3
			l.Pix[i], l.Pix[i+1], l.Pix[i+2] = float32(v), float32(v*0.95), float32(v*0.9)
		}
	}
	return NewBase(l)
}

// fineEnergy is the mean squared difference between neighbouring pixels of
// the green channel: what a sharpening mask raises.
func fineEnergy(pix []uint8, w, h int) float64 {
	var s float64
	for y := 0; y < h; y++ {
		for x := 1; x < w; x++ {
			d := float64(pix[(y*w+x)*4+1]) - float64(pix[(y*w+x-1)*4+1])
			s += d * d
		}
	}
	return s / float64(h*(w-1))
}

// Detail scales SPP's own sharpening and nothing else: 0 is the render as
// it always was, -1 is the development without the mask, +1 is stronger.
func TestSPPDetailScalesSPPSharpening(t *testing.T) {
	b := detailFrame()
	comp := Compensation{Family: "SD1", Sensor: "Merrill", SatFactor: 1}
	render := func(detail float64, sharp bool) []uint8 {
		p := preset.Neutral()
		p.SPPDetail = detail
		return RenderOpt(b, p, comp, sharp).Pix
	}
	def := render(0, true)
	if a, _ := sppView(1, 100, "Merrill"); a == 0 {
		t.Fatal("the fixture's sensor has no SPP sharpening, so the test would prove nothing")
	}
	none := render(-1, true)
	// The check that must fail if Detail did nothing: no mask is not the default.
	if bytes.Equal(none, def) {
		t.Fatal("Detail -1 rendered the same bytes as the default: the control has no effect")
	}
	// -1 is exactly the development with the sharpening stage skipped.
	if unsharp := render(0, false); !bytes.Equal(none, unsharp) {
		t.Error("Detail -1 is not the unsharpened development")
	}
	eNone, eHalf, eDef, eMore := fineEnergy(none, b.W, b.H), fineEnergy(render(-0.5, true), b.W, b.H), fineEnergy(def, b.W, b.H), fineEnergy(render(1, true), b.W, b.H)
	if !(eNone < eHalf && eHalf < eDef && eDef < eMore) {
		t.Errorf("fine detail should rise with Detail: -1 %.3f, -0.5 %.3f, 0 %.3f, +1 %.3f", eNone, eHalf, eDef, eMore)
	}
	// Out-of-range values are held at the ends.
	if !bytes.Equal(render(-3, true), none) || !bytes.Equal(render(5, true), render(1, true)) {
		t.Error("Detail outside -1..+1 is not clamped")
	}
}

// The GPU recipe carries the same scaled amount as the CPU render uses, and
// the grain gain that models the sharpening follows it.
func TestSPPDetailInGPURecipeAndGrainGain(t *testing.T) {
	b := detailFrame()
	comp := Compensation{Family: "SD1", Sensor: "Merrill", SatFactor: 1}
	amount := func(detail float64) float32 {
		p := preset.Neutral()
		p.SPPDetail = detail
		return NewGPURecipe(b, p, comp).SppSharpen
	}
	a0 := amount(0)
	if a0 <= 0 {
		t.Fatalf("default recipe amount %.3f", a0)
	}
	if got := amount(-1); got != 0 {
		t.Errorf("recipe amount at Detail -1 = %.3f, want 0", got)
	}
	if got := amount(-0.5); math.Abs(float64(got/a0)-0.5) > 1e-6 {
		t.Errorf("recipe amount at Detail -0.5 = %.3f of the default, want 0.5", got/a0)
	}
	if g := sppGrainGain(0.5, 100, "Merrill", -1); g != 1 {
		t.Errorf("grain gain with no sharpening = %.3f, want 1", g)
	}
	if g0, g1 := sppGrainGain(0.5, 100, "Merrill", 0), sppGrainGain(0.5, 100, "Merrill", 1); !(g1 > g0 && g0 > 1) {
		t.Errorf("grain gain should rise with Detail: 0 -> %.3f, +1 -> %.3f", g0, g1)
	}
}
