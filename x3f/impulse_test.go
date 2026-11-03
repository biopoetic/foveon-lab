package x3f

// Task 51: the denoiser's narrow luma impulse reject. An isolated hot or
// dead sample is repaired; a thin bright line and a star whose PSF lights
// its neighbours keep every pixel.

import (
	"math"
	"testing"
)

func TestRejectLumaOutKeepsLinesAndStars(t *testing.T) {
	const w, h = 48, 40
	img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	for i := 0; i < w*h; i++ {
		img.Pix[i*3], img.Pix[i*3+1], img.Pix[i*3+2] = 0.10, 0.11, 0.12
	}
	set := func(x, y int, v [3]float32) {
		i := (y*w + x) * 3
		img.Pix[i], img.Pix[i+1], img.Pix[i+2] = v[0], v[1], v[2]
	}
	// Isolated hot and dead samples.
	set(10, 10, [3]float32{0.31, 0.32, 0.33})
	set(20, 20, [3]float32{-0.11, -0.10, -0.09})
	// A one-pixel bright line: its neighbours along the line are bright.
	for x := 5; x < 30; x++ {
		set(x, 5, [3]float32{0.35, 0.36, 0.37})
	}
	// A star: the PSF lights its four closest neighbours well above noise.
	set(30, 20, [3]float32{0.55, 0.56, 0.57})
	for _, d := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
		set(30+d[0], 20+d[1], [3]float32{0.25, 0.26, 0.27})
	}
	sig := make([]float32, w*h)
	for i := range sig {
		sig[i] = 0.01
	}
	n := rejectLumaOut(img, sig, w, h)
	if n != 2 {
		i := (10*w + 10) * 3
		t.Logf("hot now %v at %d", img.Pix[i:i+3], i)
		i = (20*w + 20) * 3
		t.Logf("dead now %v at %d", img.Pix[i:i+3], i)
		t.Fatalf("repaired %d pixels, want 2", n)
	}
	at := func(x, y int) [3]float32 {
		i := (y*w + x) * 3
		return [3]float32{img.Pix[i], img.Pix[i+1], img.Pix[i+2]}
	}
	for _, q := range [][2]int{{10, 10}, {20, 20}} {
		v := at(q[0], q[1])
		if math.Abs(float64(v[0]-0.10)) > 1e-4 || math.Abs(float64(v[1]-0.11)) > 1e-4 {
			t.Fatalf("impulse at %v left as %v", q, v)
		}
	}
	if got := at(10, 5); math.Abs(float64(got[0]-0.35)) > 1e-6 {
		t.Fatalf("line pixel changed to %v", got)
	}
	if got := at(30, 20); math.Abs(float64(got[0]-0.55)) > 1e-6 {
		t.Fatalf("star centre changed to %v", got)
	}
	if got := at(29, 20); math.Abs(float64(got[0]-0.25)) > 1e-6 {
		t.Fatalf("star PSF changed to %v", got)
	}
}

func TestRejectLumaOutQuietOnNoise(t *testing.T) {
	const w, h = 64, 64
	img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	// Deterministic +-1 sigma noise: no sample is 2.5 sigma from its
	// neighbours, so nothing is touched.
	for i := 0; i < w*h; i++ {
		v := float32(0.1)
		if (i*7)%11 == 0 {
			v += 0.01
		} else if (i*5)%13 == 0 {
			v -= 0.01
		}
		img.Pix[i*3], img.Pix[i*3+1], img.Pix[i*3+2] = v, v+0.002, v+0.004
	}
	sig := make([]float32, w*h)
	for i := range sig {
		sig[i] = 0.01
	}
	if n := rejectLumaOut(img, sig, w, h); n != 0 {
		t.Fatalf("changed %d pixels on quiet noise, want 0", n)
	}
}
