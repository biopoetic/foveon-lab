package render

import (
	"math"
	"slices"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// The curves must agree with their own data sheets.
func TestFilmCurves(t *testing.T) {
	for key, c := range filmCurves() {
		f, ok := FilmByKey(key)
		if !ok || !f.BW {
			t.Fatalf("%s: not a black-and-white library film", key)
		}
		for _, k := range c.Curves {
			if len(k.D) != len(c.LgH) {
				t.Fatalf("%s: curve has %d points, lgh %d", key, len(k.D), len(c.LgH))
			}
			for i := 1; i < len(k.D); i++ {
				if c.LgH[i] <= c.LgH[i-1] || k.D[i] < k.D[i-1] {
					t.Errorf("%s: curve %v not increasing at %d", key, k.Time, i)
				}
			}
		}
		g := c.GammaRef()
		// ISO speed: the curve's fog + 0.1 point near 0.8/ISO lux·s (the
		// axis read right); the tone puts it there exactly.
		off := c.SpeedPoint() - math.Log10(0.8/float64(f.ISO))
		if !c.Relative && math.Abs(off) > 0.35 {
			t.Errorf("%s: speed point %.2f stops from the box speed", key, off/math.Log10(2))
		}
		// The sheet's standard times all aim at one contrast.
		var cs []float64
		for _, d := range c.Developers {
			if len(d.T) != len(d.C) {
				t.Fatalf("%s %s: %d times, %d contrasts", key, d.Name, len(d.T), len(d.C))
			}
			// A borrowed curve shape is on its own sheet's scale (only its
			// ratios are used).
			if d.Src == "" {
				cs = append(cs, interp(d.T, d.C, d.Time))
			}
			if d.PushTime(2) < d.Time {
				t.Errorf("%s %s: push time %.2f below the standard %.2f", key, d.Name, d.PushTime(2), d.Time)
			}
		}
		lo, hi := 0.0, 0.0
		if len(cs) > 0 {
			lo, hi = slices.Min(cs), slices.Max(cs)
		}
		if hi-lo > 0.04 {
			t.Errorf("%s: standard times develop to %.3f..%.3f", key, lo, hi)
		}
		t.Logf("%s: gamma_ref %.3f, speed point %+.2f stops off the box (curve faster if -), standard contrast %.3f..%.3f, %d developers", key, g, off/math.Log10(2), lo, hi, len(c.Developers))
	}
}

func TestFilmTone(t *testing.T) {
	p := preset.Params{Film: "Agfa Agfapan APX 400"}
	std := newFilmTone(p)
	if std == nil {
		t.Fatal("no tone for APX 400")
	}
	if g := std.at(0.18); math.Abs(float64(g)-0.18) > 0.002 {
		t.Errorf("mid-grey prints %.4f", g)
	}
	prev := float32(0)
	for y := float32(0.0005); y < 40; y *= 1.1 {
		v := std.at(y)
		if v < prev {
			t.Fatalf("not monotonic at %g", y)
		}
		prev = v
	}
	// Pushed two stops, standard time: the shadows sink into the toe.
	p.Push = 2
	push := newFilmTone(p)
	// Pushed and developed longer: more contrast.
	p.DevTime = 6.8
	pushDev := newFilmTone(p)
	for _, y := range []float32{0.01, 0.03, 0.6} {
		t.Logf("Y %.2f: standard %.4f, push 2 %.4f, push 2 + 6.8 min %.4f", y, std.at(y), push.at(y), pushDev.at(y))
	}
	if !(push.at(0.02) < std.at(0.02)) {
		t.Errorf("push: shadows %.4f, standard %.4f", push.at(0.02), std.at(0.02))
	}
	if !(pushDev.at(0.6) > push.at(0.6)) {
		t.Errorf("longer development: highlights %.4f, not above %.4f", pushDev.at(0.6), push.at(0.6))
	}
	// Tri-X, D-76 pushed two stops: Kodak's 9 1/2 minutes, more contrast.
	tx, _ := FilmCurveFor("Kodak Tri-X Pan 400 (TX)")
	pd := tx.DevelopmentFor(preset.Params{Film: "Kodak Tri-X Pan 400 (TX)", Developer: "D-76", Push: 2})
	if pd.Time != 9.5 || pd.EI != 1600 || pd.Scale < 1.1 {
		t.Errorf("Tri-X D-76 +2: %.2f min, EI %.0f, contrast x%.2f", pd.Time, pd.EI, pd.Scale)
	}
	t.Logf("Tri-X D-76 +2: %.2f min, EI %.0f, gamma %.3f (x%.2f)", pd.Time, pd.EI, pd.Gamma, pd.Scale)
	for _, k := range tx.Curves {
		t.Logf("Tri-X curve %v min: gamma %.3f", k.Time, k.gamma)
	}
	// No curve, no tone.
	if newFilmTone(preset.Params{Film: "Foma Fomapan 100 Classic"}) != nil || newFilmTone(preset.Params{}) != nil {
		t.Error("tone without a curve")
	}
}

// A red filter in front of Tri-X: blue sky darker, red lighter, white the
// same grey (the photographer opens up by the filter factor).
func TestFilmWithLensFilter(t *testing.T) {
	cc := camfSunlight["SD1"]
	c := Compensation{CC: &cc, Sensor: "Merrill"}
	grey := func(p preset.Params, r, g, b float32) float32 {
		op := newPixOp(p, c)
		if op.layerMono == nil {
			t.Fatalf("%+v: no film grey", p)
		}
		v := op.layerMono
		return v[0]*r + v[1]*g + v[2]*b
	}
	film := preset.Params{Film: "Kodak Tri-X Pan 400 (TX)"}
	red := film
	red.Filter = "red"
	if w0, w1 := grey(film, 1, 1, 1), grey(red, 1, 1, 1); math.Abs(float64(w1-w0)) > 0.001 {
		t.Errorf("white: %.3f without the filter, %.3f with", w0, w1)
	}
	sky0, sky1 := grey(film, 0.05, 0.1, 0.6), grey(red, 0.05, 0.1, 0.6)
	rose0, rose1 := grey(film, 0.6, 0.08, 0.05), grey(red, 0.6, 0.08, 0.05)
	t.Logf("blue sky %.3f -> %.3f, red %.3f -> %.3f", sky0, sky1, rose0, rose1)
	if !(sky1 < sky0*0.6 && rose1 > rose0*1.3) {
		t.Errorf("red filter: sky %.3f -> %.3f, red %.3f -> %.3f", sky0, sky1, rose0, rose1)
	}
}

// HP5 printed on Multigrade: mid-grey prints 18% grey on every grade; a
// harder grade darkens the shadows and lifts the highlights; the paper's
// D-max (2.05) is the black, ~1%.
func TestPaperGrades(t *testing.T) {
	tone := func(g string) *filmTone {
		return newFilmTone(preset.Params{Film: "Ilford HP5 Plus", Paper: "Ilford Multigrade IV RC", Grade: g})
	}
	soft, normal, hard := tone("00"), tone("2"), tone("5")
	for _, tt := range []*filmTone{soft, normal, hard} {
		if g := tt.at(0.18); math.Abs(float64(g)-0.18) > 0.01 {
			t.Errorf("mid-grey prints %.4f", g)
		}
	}
	for _, y := range []float32{0.005, 0.03, 0.18, 0.6, 2} {
		t.Logf("scene %.3f: grade 00 %.4f, grade 2 %.4f, grade 5 %.4f", y, soft.at(y), normal.at(y), hard.at(y))
	}
	if !(hard.at(0.03) < normal.at(0.03) && normal.at(0.03) < soft.at(0.03)) {
		t.Error("shadows don't darken with grade")
	}
	if !(hard.at(0.6) > normal.at(0.6) && normal.at(0.6) > soft.at(0.6)) {
		t.Error("highlights don't lift with grade")
	}
	if b := normal.at(0.0005); b < 0.005 || b > 0.02 {
		t.Errorf("black %.4f, want ~0.009 (D-max 2.05)", b)
	}
}
