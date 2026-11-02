package render

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func flat(w, h int, v float32) *Linear {
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	for i := range l.Pix {
		l.Pix[i] = v
	}
	return l
}

// A smaller aperture sees more density noise, but not the full Selwyn
// 1/√a: once a clump is larger than the pixel its contrast no longer grows
// as the pixel shrinks. The old per-pixel model was the 1/√a limit; the new
// one is flatter, which is the point of the clump tail.
func TestGrainAperture(t *testing.T) {
	p := preset.Params{Grain: 17}
	a := NewBase(flat(4704, 64, 0.2)).newFilm(p, false)
	b := NewBase(flat(2352, 64, 0.2)).newFilm(p, false)
	r := float64(a.sigma / b.sigma)
	if r < 1.25 || r > 2.05 {
		t.Errorf("sigma ratio %.3f, want between 1.25 and the Selwyn 2", r)
	}
	if a.sigma <= 0 || b.sigma <= 0 {
		t.Fatalf("no grain: %g %g", a.sigma, b.sigma)
	}
}

// The model is normalized so a 48 µm aperture measures the requested G.
func TestGrainCalibration48(t *testing.T) {
	const want = 0.017
	f := NewBase(flat(750, 500, 0.214)).newFilm(preset.Params{Grain: 17}, true)
	var s, s2 float64
	n := 750 * 500
	for i := 0; i < n; i++ {
		r, _, _ := f.grain(i, 0.214, 0.214, 0.214)
		d := -math.Log10(float64(r) / 0.214)
		s += d
		s2 += d * d
	}
	sd := math.Sqrt(s2/float64(n) - (s/float64(n))*(s/float64(n)))
	if math.Abs(sd-want) > 0.05*want {
		t.Errorf("density sd at 48 µm %.4f, want %.4f", sd, want)
	}
}

// The measured density noise of a mid-grey matches sigma; black stays black.
func TestGrainStats(t *testing.T) {
	f := NewBase(flat(256, 256, 0.2)).newFilm(preset.Params{Grain: 17}, true)
	var s, s2 float64
	n := 256 * 256
	for i := 0; i < n; i++ {
		r, _, _ := f.grain(i, 0.2, 0.2, 0.2)
		d := -math.Log10(float64(r) / 0.2)
		s, s2 = s+d, s2+d*d
	}
	sd := math.Sqrt(s2/float64(n) - (s/float64(n))*(s/float64(n)))
	u := float64(enc(0.2))
	want := float64(f.sigma) * 2 * math.Sqrt(u*(1-u))
	if math.Abs(sd-want) > 0.05*want {
		t.Errorf("density sd %.4f, want %.4f", sd, want)
	}
	if r, _, _ := f.grain(5, 0, 0, 0); r != 0 {
		t.Errorf("black got grain: %g", r)
	}
}

// A fast film's grain is larger and stronger than a slow film's, at the
// same view size; a pushed film gets more grain still.
func TestGrainSpeedAndPush(t *testing.T) {
	b := NewBase(flat(1200, 800, 0.2))
	slow, _ := FilmByKey("Ilford Pan F Plus")
	fast, _ := FilmByKey("Ilford Delta 3200")
	a := b.newFilm(preset.Params{Film: "Ilford Pan F Plus", Grain: slow.Grain}, true)
	z := b.newFilm(preset.Params{Film: "Ilford Delta 3200", Grain: fast.Grain}, true)
	if z.sigma < 2*a.sigma {
		t.Errorf("ISO 3200 sigma %.4f is not well above ISO 50 %.4f", z.sigma, a.sigma)
	}
	pushed := b.newFilm(preset.Params{Film: "Ilford Delta 3200", Grain: fast.Grain, Push: 2}, true)
	if pushed.sigma <= z.sigma {
		t.Errorf("push did not add grain: %.4f -> %.4f", z.sigma, pushed.sigma)
	}
}

// Choosing a film turns its grain on even when the stored Grain is 0 (a
// preset that names a film but predates the film's grain), and the slider
// still scales it linearly.
func TestFilmGrainOnByDefault(t *testing.T) {
	b := NewBase(flat(1200, 800, 0.2))
	f, _ := FilmByKey("Ilford Pan F Plus")
	a := b.newFilm(preset.Params{Film: "Ilford Pan F Plus"}, true)
	if a == nil || a.sigma == 0 {
		t.Fatal("selecting a film did not turn its grain on")
	}
	d := b.newFilm(preset.Params{Film: "Ilford Pan F Plus", Grain: 2 * f.Grain}, true)
	if d.sigma < 1.9*a.sigma || d.sigma > 2.1*a.sigma {
		t.Errorf("doubling the slider gave %.3f x the grain, want 2", d.sigma/a.sigma)
	}
}

// Halation: light spreads into red (a little green), never blue.
func TestHalationRed(t *testing.T) {
	l := flat(101, 101, 0)
	c := (50*101 + 50) * 3
	l.Pix[c], l.Pix[c+1], l.Pix[c+2] = 50, 50, 50
	l.Scale, l.Frame = 1, 4704 // a 101 px window of a full SD1 frame: 7.65 µm pixels
	b := NewBase(l)
	f := b.newFilm(preset.Params{Halation: 1}, false)
	r, g, bl := f.addHalo(50*101+60, 0, 0, 0)
	if r <= 0 || g >= r || bl != 0 {
		t.Errorf("halo at 10 px: %g %g %g", r, g, bl)
	}
}
