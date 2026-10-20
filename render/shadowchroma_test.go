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
