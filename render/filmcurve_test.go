package render

import (
	"math"
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
		if len(c.LgH) != len(c.D) || len(c.LgH) < 5 {
			t.Fatalf("%s: curve has %d/%d points", key, len(c.LgH), len(c.D))
		}
		for i := 1; i < len(c.D); i++ {
			if c.LgH[i] <= c.LgH[i-1] || c.D[i] < c.D[i-1] {
				t.Errorf("%s: curve not increasing at %d", key, i)
			}
		}
		g := c.GammaRef()
		// ISO speed: the density is fog + 0.1 at H = 0.8/ISO lux·s.
		if d := interp(c.LgH, c.D, math.Log10(0.8/float64(f.ISO))) - c.Fog(); math.Abs(d-0.1) > 0.08 {
			t.Errorf("%s: density %.2f above fog at the ISO speed point, want 0.1", key, d)
		}
		for _, d := range c.Developers {
			if len(d.T) != len(d.Gamma) {
				t.Fatalf("%s %s: %d times, %d gammas", key, d.Name, len(d.T), len(d.Gamma))
			}
			// A developer's standard time develops to the curve's gamma.
			if gs := interp(d.T, d.Gamma, d.Time); math.Abs(gs-g) > 0.04 {
				t.Errorf("%s %s: gamma %.3f at its standard %.1f min, curve's %.3f", key, d.Name, gs, d.Time, g)
			}
		}
		t.Logf("%s: gamma_ref %.3f, fog %.2f, %d developers", key, g, c.Fog(), len(c.Developers))
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
	// No curve, no tone.
	if newFilmTone(preset.Params{Film: "Ilford HP5 Plus"}) != nil || newFilmTone(preset.Params{}) != nil {
		t.Error("tone without a curve")
	}
}
