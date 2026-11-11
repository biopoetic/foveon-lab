package render

// Task 71: EIR's IR estimate must be continuous. The old estimator used
// `lit = max(Y, 0.30) when Y > 0.04`, a step of 0.26 at Y = 0.04; every
// noisy dark-hair or shadowed-skin pixel crossing it flipped its IR by a
// factor of seven and the portrait posterised into hard red/black patches.
// The ramp is now `lit = Y + (max(Y,0.30)-Y)*smoothstep(0.02,0.10,Y)`.
//
// The tests below sample irEstimate along fine ramps through the old
// threshold and through the vegetation and sky transitions, and pin GPU = CPU
// for EIR and Sensia 200.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// maxStep samples f over [lo,hi] in n steps and returns the largest
// difference between neighbouring samples and where it occurs.
func maxStep(f func(float64) float64, lo, hi float64, n int) (float64, float64) {
	step := (hi - lo) / float64(n)
	prev := f(lo)
	worst, at := 0.0, lo
	for i := 1; i <= n; i++ {
		x := lo + step*float64(i)
		v := f(x)
		if d := math.Abs(v - prev); d > worst {
			worst, at = d, x
		}
		prev = v
	}
	return worst, at
}

// Task 71: guards that irEstimate is continuous through the old 0.04 threshold and its transitions.
// A failure means the estimator has a hard step again, which posterises dark hair or shadowed skin.
// Change only when the IR estimator is deliberately changed; re-run the fine ramps and REPORT-71.
func TestEIRContinuous(t *testing.T) {
	// The old threshold: a neutral grey ramp through Y = 0.04. With a
	// 1e-6 input step the old code jumped 0.26; the ramp's steepest
	// legitimate slope is about 5, so a step of a few 1e-6 is expected.
	const step = 1e-6
	d, at := maxStep(func(y float64) float64 { return irEstimate(y, y, y) }, 0, 0.20, 200000)
	if d > 1e-5 {
		t.Errorf("irEstimate neutral ramp: largest step %.3g at Y %.6f, want <= 1e-5 (input step %g)", d, at, step)
	}
	t.Logf("neutral ramp 0..0.20, input step %g: largest IR step %.3g at Y %.6f", step, d, at)

	// The two samples straddling the old threshold specifically.
	a := irEstimate(0.04-step, 0.04-step, 0.04-step)
	b := irEstimate(0.04+step, 0.04+step, 0.04+step)
	if math.Abs(a-b) > 1e-5 {
		t.Errorf("irEstimate jumps at the old threshold: %.6f vs %.6f", a, b)
	}

	// Vegetation: Vary G so the green-magenta index crosses its 0..0.06
	// smoothstep and B/G crosses 0.80..1.02.
	d, at = maxStep(func(g float64) float64 { return irEstimate(0.10, g, 0.08) }, 0.05, 0.25, 200000)
	if d > 1e-4 {
		t.Errorf("irEstimate vegetation transition: largest step %.3g at G %.6f", d, at)
	}
	t.Logf("vegetation G 0.05..0.25: largest IR step %.3g at G %.6f", d, at)

	// Sky: Vary B so (B-R)/(R+G+B) crosses its 0.06..0.22 smoothstep.
	d, at = maxStep(func(bb float64) float64 { return irEstimate(0.15, 0.20, bb) }, 0.05, 0.35, 300000)
	if d > 1e-4 {
		t.Errorf("irEstimate sky transition: largest step %.3g at B %.6f", d, at)
	}
	t.Logf("sky B 0.05..0.35: largest IR step %.3g at B %.6f", d, at)

	// No hard edge anywhere on a coarse sweep of the neutral axis either:
	// the estimate never falls as luminance rises.
	prev := -1.0
	for i := 0; i <= 6000; i++ {
		y := float64(i) / 10000
		v := irEstimate(y, y, y)
		if v < prev-1e-9 {
			t.Fatalf("irEstimate is not monotone in luminance at Y %.4f: %.6f < %.6f", y, v, prev)
		}
		prev = v
	}
}

// Task 71: guards GPU = CPU for EIR and Sensia 200 after its re-digitisation.
// A failure means the shader's IR branch or Sensia 200's table drifted.
// Change only when web/gpu.js's slide/IR pass changes; update the Go mirror with it.
func TestSlideGPUEIR(t *testing.T) {
	for _, key := range []string{
		"Kodak Ektachrome Professional Infrared (EIR)",
		"Fujifilm Fujichrome Sensia 200",
	} {
		const w, h = 32, 24
		l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, ISO: 100, Sensor: "Merrill"}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := (y*w + x) * 3
				u := float64(x) / float64(w-1)
				v := float64(y) / float64(h-1)
				switch {
				case u < 0.5: // dark neutral ramp: crosses 0.02..0.10
					c := 0.003 + 0.30*u
					l.Pix[i] = float32(c)
					l.Pix[i+1] = float32(c * 1.04)
					l.Pix[i+2] = float32(c * 0.97)
				case v < 0.5: // vegetation
					l.Pix[i] = 0.04
					l.Pix[i+1] = 0.20
					l.Pix[i+2] = 0.06
				case v < 0.75: // blue sky
					l.Pix[i] = 0.15
					l.Pix[i+1] = 0.25
					l.Pix[i+2] = 0.70
				default: // skin-like
					l.Pix[i] = 0.45
					l.Pix[i+1] = 0.25
					l.Pix[i+2] = 0.18
				}
			}
		}
		b := NewBase(l)
		cc := [9]float64{1.5, -0.4, -0.1, -0.2, 1.4, -0.2, 0.0, -0.5, 1.5}
		comp := Compensation{Family: "SD1", Sensor: "Merrill", CC: &cc, SatFactor: 1}
		p := preset.Neutral()
		p.Film = key
		p.NoGrain = true
		cpu := Render(b, p, comp)
		r := NewGPURecipe(b, p, comp)
		if r.ColorFilm == nil || r.ColorFilm.Black != float32(slideBlack) {
			t.Fatalf("%s: slide recipe black %v", key, r.ColorFilm)
		}
		maxd := 0
		for i := 0; i < w*h; i++ {
			c := [3]float32{l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2]}
			c = gpuFilmPass(r, [3]float32{c[0] * r.ColorFilm.Exp, c[1] * r.ColorFilm.Exp, c[2] * r.ColorFilm.Exp})
			for k := 0; k < 3; k++ {
				c[k] = c[k] * r.Gain[k] / r.ColorFilm.Exp
			}
			Y := float32(0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2])
			if Y > 1e-6 {
				d := dec(enc(Y))
				k := d / Y
				c[0], c[1], c[2] = c[0]*k, c[1]*k, c[2]*k
			}
			for k := 0; k < 3; k++ {
				want := int(float32(enc(c[k]))*255 + 0.5)
				got := int(cpu.Pix[i*4+k])
				d := got - want
				if d < 0 {
					d = -d
				}
				if d > maxd {
					maxd = d
				}
			}
		}
		if maxd > 1 {
			t.Fatalf("%s: GPU/CPU differ by %d levels", key, maxd)
		}
		t.Logf("%s: GPU = CPU, max %d 8-bit level", key, maxd)
	}
}
