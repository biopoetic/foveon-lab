package render

import (
	"math"
	"math/rand"
	"testing"
)

// Bright pixels are untouched; blotchy colour noise in a dark, flat area
// shrinks towards the area's own colour.
func TestSmoothShadowChroma(t *testing.T) {
	const w, h = 64, 64
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	rng := rand.New(rand.NewSource(1))
	// Low-frequency colour blotches: one random tint per 8x8 block.
	tint := make([][3]float32, (w/8)*(h/8))
	for i := range tint {
		tint[i] = [3]float32{1 + 0.6*(rng.Float32()-0.5), 1, 1 + 0.6*(rng.Float32()-0.5)}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			v := float32(0.01) // dark grey on the left
			if x >= w/2 {
				v = 0.5 // bright grey on the right
			}
			tn := tint[(y/8)*(w/8)+x/8]
			for k := 0; k < 3; k++ {
				l.Pix[i+k] = v * tn[k]
			}
		}
	}
	before := append([]float32(nil), l.Pix...)
	spread := func(p []float32) float64 { // mean |R/G - 1| over the dark half
		var s float64
		n := 0
		for y := 0; y < h; y++ {
			for x := 4; x < w/2-4; x++ {
				i := (y*w + x) * 3
				s += math.Abs(float64(p[i]/p[i+1]) - 1)
				n++
			}
		}
		return s / float64(n)
	}
	SmoothShadowChroma(l, w, 1)
	if a, b := spread(l.Pix), spread(before); a > 0.5*b {
		t.Errorf("dark colour noise %.3f -> %.3f, want at least halved", b, a)
	}
	for y := 0; y < h; y++ {
		for x := w/2 + 2; x < w; x++ {
			i := (y*w + x) * 3
			for k := 0; k < 3; k++ {
				if l.Pix[i+k] != before[i+k] {
					t.Fatalf("bright pixel (%d,%d) changed", x, y)
				}
			}
		}
	}
}

// Through a colour matrix (a colour film's layers), the fix maps back to
// the layers: a clean image comes back unchanged.
func TestShadowChromaFixLayersRoundTrip(t *testing.T) {
	l := &Linear{W: 64, H: 64, Pix: make([]float32, 64*64*3)}
	for i := 0; i < 64*64; i++ {
		v := 0.02 + 0.3*float32(i%64)/64
		l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = v, v*0.9, v*1.1
	}
	want := append([]float32(nil), l.Pix...)
	cc := [9]float64{3.5, -3.4, 0.9, -2.1, 4.5, -1.4, 1.1, -4.2, 4.1}
	ShadowChromaFix(l, 64, &cc)
	for i := range want {
		if d := l.Pix[i] - want[i]; d > 1e-4 || d < -1e-4 {
			t.Fatalf("pixel %d: %v, want %v", i/3, l.Pix[i], want[i])
		}
	}
}

// Noisy-shadow muting removes the magenta side only: a dark green keeps
// its colour, a dark magenta is muted (SDIM0432's moss went grey).
func TestShadowChromaKeepsGreen(t *testing.T) {
	for _, c := range []struct {
		name    string
		r, g, b float32
		muted   bool
	}{{"green", 0.008, 0.02, 0.008, false}, {"magenta", 0.03, 0.008, 0.03, true}} {
		l := &Linear{W: 32, H: 32, Pix: make([]float32, 32*32*3)}
		for i := 0; i < 32*32; i++ {
			l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = c.r, c.g, c.b
		}
		SmoothShadowChroma(l, 32, 1)
		y := 0.2126*c.r + 0.7152*c.g + 0.0722*c.b
		before, after := c.g-y, l.Pix[16*32*3+1]-y
		if muted := after/before < 0.9; muted != c.muted {
			t.Errorf("%s: colour %.5f -> %.5f, muted %v want %v", c.name, before, after, muted, c.muted)
		}
	}
}
