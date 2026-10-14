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
		// ISO speed: the density is fog + 0.1 at H = 0.8/ISO lux·s.
		dens := c.Density(g)
		if d := dens(math.Log10(0.8/float64(f.ISO))) - dens(c.LgH[0]); math.Abs(d-0.1) > 0.08 {
			t.Errorf("%s: density %.2f above fog at the ISO speed point, want 0.1", key, d)
		}
		// The sheet's standard times all aim at one contrast.
		var cs []float64
		for _, d := range c.Developers {
			if len(d.T) != len(d.C) {
				t.Fatalf("%s %s: %d times, %d contrasts", key, d.Name, len(d.T), len(d.C))
			}
			cs = append(cs, interp(d.T, d.C, d.Time))
			if d.PushTime(2) < d.Time {
				t.Errorf("%s %s: push time %.2f below the standard %.2f", key, d.Name, d.PushTime(2), d.Time)
			}
		}
		lo, hi := slices.Min(cs), slices.Max(cs)
		if hi-lo > 0.04 {
			t.Errorf("%s: standard times develop to %.3f..%.3f", key, lo, hi)
		}
		t.Logf("%s: gamma_ref %.3f, standard contrast %.3f..%.3f, %d developers", key, g, lo, hi, len(c.Developers))
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
	if newFilmTone(preset.Params{Film: "Ilford HP5 Plus"}) != nil || newFilmTone(preset.Params{}) != nil {
		t.Error("tone without a curve")
	}
}
