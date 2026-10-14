package calib

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/render"
)

// The app applies a taste base through render.RawCorrection; it must give
// what the fit measured (calib's Taste.apply).
func TestTasteMatchesRender(t *testing.T) {
	var ta Taste
	for b := range ta.Tone {
		x := (float64(b) + 0.5) / tasteBins
		ta.Tone[b] = math.Pow(x, 0.9) // a gentle lift
	}
	ta.Sat = 1.1
	c := render.RawCorrection{Tone: ta.Tone[:], Sat: ta.Sat}
	for _, p := range [][3]float32{{0.2, 0.3, 0.1}, {0.6, 0.5, 0.4}, {0.01, 0.02, 0.015}, {0.9, 0.2, 0.3}, {0.001, 0.001, 0.001}} {
		want := ta.apply(p[:])
		r, g, b := c.Pixel(p[0], p[1], p[2])
		for k, v := range []float32{r, g, b} {
			if math.Abs(float64(v-want[k])) > 2e-3 {
				t.Errorf("%v: channel %d render %.4f, calib %.4f", p, k, v, want[k])
			}
		}
	}
}
