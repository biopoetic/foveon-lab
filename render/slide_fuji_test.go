package render

// Task 69: the last eight slide films; task 70 corrected two axes, returned
// P1600 to the generic look, and replaced EIR's brightness-scaled IR proxy
// with a scale-invariant vegetation estimate.
//
// Velvia 50, Provia 100F, Sensia 100 and Fujichrome 64T Type II are digitised
// from the embedded raster figures of their makers' sheets; Sensia 200/400
// join the vector fragments and borrow the shared Fuji dye set. EIR is the
// false-colour film, modelled with a visible-light estimate of its infrared
// layer because no X3F carries an infrared channel; it is flagged approximate
// in /api/films. P1600 has no model (see REPORT-70): its published push family
// cannot be registered to the neutral wedge, so it keeps the generic look.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

var fujiSlideFilms = []string{
	"Fujifilm Fujichrome Velvia 50",
	"Fujifilm Fujichrome Provia 100F",
	"Fujifilm Fujichrome Sensia 100",
	"Fujifilm Fujichrome 64T Type II",
	"Fujifilm Fujichrome Sensia 200",
	"Fujifilm Fujichrome Sensia 400",
	"Kodak Ektachrome Professional Infrared (EIR)",
}

// Task 69: guards the last slide films' three curve sets, coverage and sampling gaps.
// A failure means a Fuji/EIR film's digitised data is incomplete or its curves have a gap over 0.1 decade.
// Change only when those sheets are re-digitised; update the data and REPORT-69.
func TestSlideFujiData(t *testing.T) {
	for _, key := range fujiSlideFilms {
		d := colorFilmData_()[key]
		if d == nil {
			t.Errorf("%s: no data entry", key)
			continue
		}
		for _, m := range []map[string][][2]float64{d.Char, d.Sens, d.Dye} {
			if len(m) != 3 {
				t.Errorf("%s: %d curve sets, want 3", key, len(m))
			}
		}
		if f := colorFilmFor(key, "Merrill"); f == nil {
			t.Fatalf("%s: no model", key)
		}
		lg := math.Log10(10 / d.ISO)
		for _, n := range []string{"R", "G", "B"} {
			ch := d.Char[n]
			lo, hi := ch[0][0], ch[0][0]
			for _, p := range ch {
				lo = math.Min(lo, p[0])
				hi = math.Max(hi, p[0])
			}
			if lo > lg-4*0.30103 || hi < lg+2*0.30103 {
				t.Errorf("%s %s: curves span %.2f..%.2f, mid-grey %.2f needs -4..+2 stops", key, n, lo, hi, lg)
			}
			// no gaps above 0.1 decade
			for i := 1; i < len(ch); i++ {
				if ch[i][0]-ch[i-1][0] > 0.1+1e-9 {
					t.Errorf("%s %s: gap %.2f at %.2f", key, n, ch[i][0]-ch[i-1][0], ch[i][0])
					break
				}
			}
		}
	}
}

// Task 69: guards the last slides' wedge drift and Status A read wavelengths (EIR exempt).
// A failure means a film drifts in colour, a read wavelength left its band, or the EIR exemption moved.
// Change only when the slide model or a film's data is deliberately changed; re-check all seven.
func TestSlideFujiWedge(t *testing.T) {
	for _, key := range fujiSlideFilms {
		f := colorFilmOn(key, "", "Merrill", ColorPrint{})
		if f == nil {
			t.Fatalf("%s: no model", key)
		}
		var pts [][2]float64
		var ys []float64
		for s := -4.0; s <= 2.01; s += 1 {
			v := float32(midGreyLayer * math.Exp2(s))
			r, g, b := f.apply(v, v, v)
			_, a, bb := slideLab(float64(r), float64(g), float64(b))
			pts = append(pts, [2]float64{a, bb})
			ys = append(ys, 0.2126*float64(r)+0.7152*float64(g)+0.0722*float64(b))
		}
		var ma, mb float64
		for _, p := range pts {
			ma += p[0]
			mb += p[1]
		}
		ma /= float64(len(pts))
		mb /= float64(len(pts))
		worst := 0.0
		for _, p := range pts {
			worst = math.Max(worst, math.Max(math.Abs(p[0]-ma), math.Abs(p[1]-mb)))
		}
		limit := 3.0
		if key == "Kodak Ektachrome Professional Infrared (EIR)" {
			t.Logf("%-46s false colour: balance a* %+.2f b* %+.2f, spread %.2f (exempt)", key, ma, mb, worst)
			continue
		}
		if worst > limit {
			t.Errorf("%s: wedge drift %.2f > %.1f", key, worst, limit)
		}
		for i := 1; i < len(ys); i++ {
			if ys[i] < ys[i-1]-1e-9 {
				t.Errorf("%s: wedge is not monotone at stop %d", key, i-4)
			}
		}
		// Status A bands: R 600-700, G 500-600, B 400-500 nm.
		for k, band := range [][2]float64{{600, 700}, {500, 600}, {400, 500}} {
			if f.readLam[k] < band[0] || f.readLam[k] > band[1] {
				t.Errorf("%s: read wavelength %.0f outside Status A band %.0f-%.0f", key, f.readLam[k], band[0], band[1])
			}
		}
		t.Logf("%-46s balance a* %+.2f b* %+.2f, worst drift %.2f, lam %.0f/%.0f/%.0f, rms %.3f",
			key, ma, mb, worst, f.readLam[0], f.readLam[1], f.readLam[2], f.greyRMS)
	}
}

// Task 69: guards the last slides' viewing black and deep-shadow neutrality (EIR cast exempt).
// A failure means a film lost the slide black level or its shadow gained a cast.
// Change only when the slide model is deliberately changed; re-check the task-67 criterion.
func TestSlideFujiBlack(t *testing.T) {
	for _, key := range fujiSlideFilms {
		f := colorFilmOn(key, "", "Merrill", ColorPrint{})
		if f == nil {
			t.Fatalf("%s: no model", key)
		}
		if math.Abs(f.black-slideBlack) > 1e-12 {
			t.Errorf("%s: viewing black %.5f, want %.5f", key, f.black, slideBlack)
		}
		vr, vg, vb := f.apply(2e-4, 2e-4, 2e-4)
		L, a, bb := slideLab(float64(vr), float64(vg), float64(vb))
		if L < 3.0 || L > 12.0 {
			t.Errorf("%s: deep shadow L* %.2f", key, L)
		}
		if key != "Kodak Ektachrome Professional Infrared (EIR)" && (math.Abs(a) > 3 || math.Abs(bb) > 3) {
			t.Errorf("%s: deep shadow a* %+.2f b* %+.2f", key, a, bb)
		}
	}
}

// Task 69: guards GPU = CPU for Velvia 50, Provia 100F and EIR's estimated IR layer.
// A failure means the shader's IR branch or a film's table drifted from the CPU.
// Change only when web/gpu.js's slide/IR pass changes; update the Go mirror with it.
func TestSlideFujiGPU(t *testing.T) {
	for _, key := range []string{"Fujifilm Fujichrome Velvia 50", "Fujifilm Fujichrome Provia 100F", "Kodak Ektachrome Professional Infrared (EIR)"} {
		const w, h = 32, 24
		l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, ISO: 100, Sensor: "Merrill"}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := (y*w + x) * 3
				u := float64(x) / float64(w-1)
				v := float64(y) / float64(h-1)
				l.Pix[i] = float32(0.002 + 0.35*u + 0.10*v)
				l.Pix[i+1] = float32(0.002 + 0.30*u + 0.12*(1-v))
				l.Pix[i+2] = float32(0.002 + 0.25*v + 0.10*u)
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

// Task 69: guards EIR's approximate flag, its IR estimate and the vegetation/sky mapping.
// A failure means EIR stopped being flagged approximate or its estimated IR layer lost its documented behaviour.
// Change only when a real infrared channel or new EIR data lands; then revise the estimate and report.
func TestEIRApprox(t *testing.T) {
	const key = "Kodak Ektachrome Professional Infrared (EIR)"
	d := colorFilmData_()[key]
	if d == nil || !d.IRApprox {
		t.Fatalf("EIR is not flagged approximate")
	}
	f := colorFilmFor(key, "Merrill")
	if f == nil || !f.irApprox {
		t.Fatalf("EIR model is not approximate")
	}
	t.Logf("EIR viewing gain %.3f, black %.4f", f.gain, f.black)
	fl, ok := FilmByKey(key)
	if !ok || !fl.Approximate || !fl.Color {
		t.Fatalf("EIR library entry: ok %v approximate %v color %v", ok, fl.Approximate, fl.Color)
	}
	fr, fg, fb := f.apply(0.04, 0.20, 0.06) // vegetation
	sr, sg, sb := f.apply(0.15, 0.25, 0.70) // blue sky
	if !(fr > 3*fg || fr > 3*fb) {
		t.Errorf("vegetation is not on the red/magenta side: %.3f %.3f %.3f", fr, fg, fb)
	}
	if !(sb > 3*sr) {
		t.Errorf("sky is not on the blue side: %.3f %.3f %.3f", sr, sg, sb)
	}
	// The vegetation index is a chromaticity ratio: the IR estimate of a leaf
	// does not change when the leaf is darker, so a conifer at 6% has the
	// same NIR as a sunlit one.
	if a, b := irEstimate(0.04, 0.20, 0.06), irEstimate(0.02, 0.10, 0.03); math.Abs(a-b) > 1e-6 {
		t.Errorf("IR estimate is not scale-invariant on vegetation: %.4f vs %.4f", a, b)
	}
	if v := irEstimate(0.04, 0.20, 0.06); math.Abs(v-0.45) > 0.02 {
		t.Errorf("vegetation IR %.3f, want a plant-like 0.45", v)
	}
	if v := irEstimate(0.15, 0.25, 0.70); v > 0.02 {
		t.Errorf("blue sky IR %.3f, want near zero", v)
	}
	if v := irEstimate(0.45, 0.25, 0.18); v < 0.28 {
		t.Errorf("skin-like IR %.3f, want moderately high", v)
	}
	// The IR layer is not a linear proxy any more: its root-polynomial row is
	// zero and apply computes it from the estimate.
	if f.rp[0] != 0 || f.rp[1] != 0 || f.rp[2] != 0 {
		t.Errorf("IR layer still has a linear row: %v", f.rp[0:3])
	}
}
