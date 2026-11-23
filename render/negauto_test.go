package render

import (
	"bytes"
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// A frame at the reference key is not corrected; a darker frame is
// brightened by the stops it is short, a brighter one darkened; the
// correction is held to its range and its step.
func TestNegAutoEV(t *testing.T) {
	if ev := NegAutoEV(negKeyRef); ev != 0 {
		t.Errorf("ordinary frame corrected by %.3f", ev)
	}
	if ev := NegAutoEV(negKeyRef / 2); math.Abs(ev-1) > 1e-9 {
		t.Errorf("a frame one stop dark: %.3f, want +1", ev)
	}
	if ev := NegAutoEV(negKeyRef * 2); math.Abs(ev+1) > 1e-9 {
		t.Errorf("a frame one stop bright: %.3f, want -1", ev)
	}
	if ev := NegAutoEV(negKeyRef / 100); ev != negAutoMax {
		t.Errorf("a black frame: %.3f, want the limit %.1f", ev, negAutoMax)
	}
	if ev := NegAutoEV(0); ev != 0 {
		t.Errorf("unknown key corrected by %.3f", ev)
	}
	ev := NegAutoEV(negKeyRef / 1.3)
	if r := ev / negAutoStep; math.Abs(r-math.Round(r)) > 1e-9 {
		t.Errorf("correction %.4f is not on the %.4f step", ev, negAutoStep)
	}
}

// The key reads the frame, not the view: a frame and its reduced copy have
// (nearly) the same key, so a preview and the export are printed alike.
func TestFrameLogKeySameAtEverySize(t *testing.T) {
	const w, h = 1200, 800
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(0.02 + 0.5*math.Pow(float64(x)/w, 2)*(0.5+0.5*math.Sin(float64(y)/37)))
			i := (y*w + x) * 3
			l.Pix[i], l.Pix[i+1], l.Pix[i+2] = v, v, v
		}
	}
	full, small := FrameLogKey(l), FrameLogKey(l.Downscale(400))
	if d := math.Abs(math.Log2(full / small)); d > negAutoStep/2 {
		t.Errorf("key %.4f full, %.4f reduced: %.3f stops apart", full, small, d)
	}
}

// negAutoFrame is a dark raw Merrill frame: most of it deep shadow, as a
// portrait against a dark background.
func negAutoFrame() *Base {
	const w, h = 64, 48
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, Frame: w, NativeFrame: w, Raw: true, ISO: 100, Sensor: "Merrill"}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(0.012 + 0.02*float64(x)/w)
			if x > w/3 && x < 2*w/3 && y > h/4 && y < 3*h/4 {
				v = 0.08 // the face
			}
			i := (y*w + x) * 3
			l.Pix[i], l.Pix[i+1], l.Pix[i+2] = v*1.05, v, v*0.95
		}
	}
	return NewBase(l)
}

func meanG(pix []uint8) float64 {
	var s float64
	for i := 1; i < len(pix); i += 4 {
		s += float64(pix[i])
	}
	return s / float64(len(pix)/4)
}

// The correction brightens a dark frame on a print and on a scan; it leaves
// a slide, a B&W film and the picture without a film byte for byte alone;
// and NegEV 0 is the render as it was.
func TestNegAutoBrightensADarkNegativeOnly(t *testing.T) {
	b := negAutoFrame()
	cc := [9]float64{1.5, -0.4, -0.1, -0.2, 1.4, -0.2, 0.0, -0.5, 1.5}
	comp := Compensation{Family: "SD1", Sensor: "Merrill", CC: &cc, SatFactor: 1}
	render := func(film, paper string, ev float64) []uint8 {
		p := preset.Neutral()
		p.Film, p.Paper, p.NoGrain = film, paper, true
		c := comp
		c.NegEV = ev
		return RenderOpt(b, p, c, false).Pix
	}
	for _, paper := range []string{"", PaperScan} {
		off, on := render("Kodak Portra 400", paper, 0), render("Kodak Portra 400", paper, 1.5)
		if mOff, mOn := meanG(off), meanG(on); mOn <= mOff+3 {
			t.Errorf("paper %q: a dark negative did not brighten (green mean %.1f -> %.1f)", paper, mOff, mOn)
		}
	}
	for _, film := range []string{"Fujifilm Fujichrome Velvia 100", "Kodak Tri-X Pan 400 (TX)", ""} {
		if !bytes.Equal(render(film, "", 0), render(film, "", 1.5)) {
			t.Errorf("%q changed with the negative's correction", film)
		}
	}
	// The GPU recipe builds the same film table key as the CPU.
	p := preset.Neutral()
	p.Film = "Kodak Portra 400"
	c := comp
	c.NegEV = 1.5
	if k0, k1 := NewGPURecipe(b, p, comp).ColorFilm.Key, NewGPURecipe(b, p, c).ColorFilm.Key; k0 == k1 {
		t.Errorf("the GPU film key ignores the correction: %s", k1)
	}
}
