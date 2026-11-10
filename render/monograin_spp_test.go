package render

// Task 82: the 1:1 monochrome grain was refit against SPP's 1:1 exports.
// Task 83 kept the pin and changed the fixture's sensor from "Merrill" to
// "DPM": the exports are DP3 Merrills, which take the DPM sharpening row
// (amount 1.67, sigma .7), not the SD1's "Merrill" row. The numbers below are
// still SPP's flat-block std and width.
//
// The fixture is a flat mid-grey frame. That is the hardest case for the
// 8-bit unsharp-mask clamp (there is no scene texture to widen the 3x3
// local range), so its absolute std runs 7-13% below the real flat-block
// exports; the report's real-photo table is the 5% accuracy evidence. The
// pin here is 13% on std and 15% on clump width, so a regression in the
// .1/.5/1 clump or the roughness share fails without pretending the flat
// fixture is a photograph.

import (
	"image"
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

type sppFitPoint struct {
	size, rough float64
	std         float64 // SPP flat-block std, 8-bit levels
	width       float64 // SPP integral ACF width, px
}

// 01 (sigma_dp3_merrill_01.x3f) at the seven settings SPP exported, measured
// by the task-82 run on the 1400x1400 crop at (2100,700).
var sppFit = []sppFitPoint{
	{.1, .1, 1.68, 2.15},
	{.1, .5, 2.68, 1.66},
	{.5, .1, 8.51, 2.50},
	{.5, .5, 11.69, 1.95},
	{.5, 1.0, 14.21, 1.69},
	{1.0, .5, 30.12, 2.27},
	{1.0, 1.0, 34.90, 2.00},
}

func sppFitBase(w, h int) *Base {
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1,
		Frame: w, NativeFrame: w, Raw: true, ISO: 100, Sensor: "DPM"}
	for i := range l.Pix {
		l.Pix[i] = 0.2158 // sRGB 128
	}
	return NewBase(l)
}

func sppFitDiff(a, b *image.RGBA) []float32 {
	out := make([]float32, a.Rect.Dx()*a.Rect.Dy())
	for i := range out {
		out[i] = float32(a.Pix[i*4]) - float32(b.Pix[i*4])
	}
	return out
}

func sppFitStats(v []float32, w, h int) (sd, width float64) {
	mean, s2 := 0.0, 0.0
	for _, x := range v {
		mean += float64(x)
		s2 += float64(x) * float64(x)
	}
	mean /= float64(len(v))
	sd = math.Sqrt(math.Max(0, s2/float64(len(v))-mean*mean))
	one := func(axis int) float64 {
		s := 0.0
		for lag := 1; lag < w/2 && lag < h/2; lag++ {
			var num, den float64
			for y := 0; y < h-lag; y++ {
				for x := 0; x < w-lag; x++ {
					i := y*w + x
					j := i + lag
					if axis == 1 {
						j = i + lag*w
					}
					a := float64(v[i]) - mean
					bb := float64(v[j]) - mean
					num += a * bb
					den += a * a
				}
			}
			if den == 0 {
				break
			}
			r := num / den
			if r < 0.02 {
				break
			}
			s += r
		}
		return 1 + 2*s
	}
	return sd, (one(0) + one(1)) / 2
}

func TestMonoGrainSPP1to1Fit(t *testing.T) {
	const W = 512
	comp := Compensation{Family: "SD1", Sensor: "Merrill", SatFactor: 1}
	b := sppFitBase(W, W)
	for _, q := range sppFit {
		p := preset.Neutral()
		p.Mono, p.MonoMixSet = true, true
		p.MonoR, p.MonoG, p.MonoB = 1.0/3, 1.0/3, 1.0/3
		p.MonoGrainSize, p.MonoGrainRough = q.size, q.rough
		p0 := p
		p0.MonoGrainSize = 0
		g := Render(b, p, comp)
		z := Render(b, p0, comp)
		sd, width := sppFitStats(sppFitDiff(g, z), W, W)
		if r := sd / q.std; math.Abs(r-1) > 0.13 {
			t.Errorf("size %.1f rough %.1f: std %.2f, SPP %.2f (ratio %.3f), want within 13%%",
				q.size, q.rough, sd, q.std, r)
		}
		if r := width / q.width; math.Abs(r-1) > 0.15 {
			t.Errorf("size %.1f rough %.1f: width %.3f, SPP %.3f (ratio %.3f), want within 15%%",
				q.size, q.rough, width, q.width, r)
		}
		t.Logf("size %.1f rough %.1f: std %.2f/%.2f (%.3f) width %.3f/%.3f (%.3f)",
			q.size, q.rough, sd, q.std, sd/q.std, width, q.width, width/q.width)
	}
}
