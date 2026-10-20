package render

import (
	"math"
	"testing"
)

// Fill Light's frame mean is the whole frame's: a crop, a zoom window or a
// rotation of the image keeps it, so cropping does not change the fill.
func TestFillMeanFollowsTheFrame(t *testing.T) {
	const w, h = 300, 200
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(0.02)
			if x > w/2 {
				v = 0.6 // bright right half
			}
			for k := 0; k < 3; k++ {
				l.Pix[(y*w+x)*3+k] = v
			}
		}
	}
	full := FrameMean(l)
	if full <= 0 {
		t.Fatal("no mean")
	}
	crop := ApplyGeometry(l, Geometry{X: 0, Y: 0, W: 0.3, H: 1}) // the dark third only
	win := l.Window(0, 0, 50, 50)
	rot := l.Rotate(90)
	for name, m := range map[string]float32{"crop": crop.frameMean(), "window": win.frameMean(), "rotation": rot.frameMean(), "base": NewBase(crop).fillMean} {
		if math.Abs(float64(m-full)) > 1e-6 {
			t.Errorf("%s: frame mean %.4f, want the full frame's %.4f", name, m, full)
		}
	}
	if own := FrameMean(crop); math.Abs(float64(own-full)) < 0.05 {
		t.Errorf("the crop alone should be darker than the frame (%.3f vs %.3f) - the test is not testing", own, full)
	}
}
