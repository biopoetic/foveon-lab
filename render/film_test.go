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

// Selwyn's law: a pixel covering twice the film width has half the noise.
func TestGrainSelwyn(t *testing.T) {
	p := preset.Params{Grain: 17}
	a := NewBase(flat(4704, 64, 0.2)).newFilm(p, false)
	b := NewBase(flat(2352, 64, 0.2)).newFilm(p, false)
	if r := a.sigma / b.sigma; math.Abs(float64(r)-2) > 0.01 {
		t.Errorf("sigma ratio %.3f, want 2", r)
	}
	// 35 mm, 4704 px: 7.65 µm pixels, G 17 -> 0.017·√(π·24²)/7.65 ≈ 0.094.
	if math.Abs(float64(a.sigma)-0.0944) > 0.002 {
		t.Errorf("sigma %.4f", a.sigma)
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
