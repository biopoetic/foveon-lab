package render

import (
	"math"
	"testing"
)

func testLab(r, g, b float64) (a, bb, c float64) {
	x := 0.4124*r + 0.3576*g + 0.1805*b
	y := 0.2126*r + 0.7152*g + 0.0722*b
	z := 0.0193*r + 0.1192*g + 0.9505*b
	f := func(v float64) float64 {
		if v > 216.0/24389 {
			return math.Cbrt(v)
		}
		return (24389.0/27*v + 16) / 116
	}
	fx, fy, fz := f(x/0.9505), f(y/1.0), f(z/1.089)
	a, bb = 500*(fx-fy), 200*(fy-fz)
	return a, bb, math.Hypot(a, bb)
}

// The bright chroma denoise must even out a low-frequency colour mottling in
// a flat bright area, keep each pixel's luminance, and leave dark or
// saturated pixels exactly alone.
func TestBrightChromaDenoise(t *testing.T) {
	const w, h = 512, 256
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			if y >= 200 {
				// A dark saturated band: out of the gate, untouched.
				l.Pix[i], l.Pix[i+1], l.Pix[i+2] = 0.10, 0.04, 0.04
				continue
			}
			// 16 px checkerboard of opposite small tints: low-frequency
			// colour mottling at the scale of the filter's radius.
			tint := 0.02
			if (x/16+y/16)%2 == 1 {
				tint = -0.02
			}
			l.Pix[i], l.Pix[i+1], l.Pix[i+2] = float32(0.60+tint), 0.60, float32(0.60-tint)
		}
	}
	spread := func() float64 {
		var s, s2 float64
		n := 0
		for y := 8; y < 190; y++ {
			for x := 8; x < w-8; x++ {
				i := (y*w + x) * 3
				a, _, _ := testLab(float64(l.Pix[i]), float64(l.Pix[i+1]), float64(l.Pix[i+2]))
				s += a
				s2 += a * a
				n++
			}
		}
		m := s / float64(n)
		return math.Sqrt(math.Max(0, s2/float64(n)-m*m))
	}
	yBefore := make([]float64, w*h)
	for i := 0; i < w*h; i++ {
		yBefore[i] = 0.2126*float64(l.Pix[i*3]) + 0.7152*float64(l.Pix[i*3+1]) + 0.0722*float64(l.Pix[i*3+2])
	}
	s0 := spread()
	BrightChromaDenoise(l)
	s1 := spread()
	if s1 > 0.7*s0 {
		t.Fatalf("chroma mottling not reduced: a* spread %.3f -> %.3f", s0, s1)
	}
	for i := 0; i < w*h; i++ {
		y := 0.2126*float64(l.Pix[i*3]) + 0.7152*float64(l.Pix[i*3+1]) + 0.0722*float64(l.Pix[i*3+2])
		if math.Abs(y-yBefore[i]) > 2e-3 {
			t.Fatalf("luminance moved at %d: %.5f -> %.5f", i, yBefore[i], y)
		}
	}
	for i := 200 * w * 3; i < len(l.Pix); i++ {
		want := float32([]float64{0.10, 0.04, 0.04}[i%3])
		if l.Pix[i] != want {
			t.Fatalf("dark saturated band changed at %d: %v want %v", i, l.Pix[i], want)
		}
	}
}

// Small images must not panic or be touched.
func TestBrightChromaDenoiseSmall(t *testing.T) {
	l := &Linear{W: 16, H: 16, Pix: make([]float32, 16*16*3)}
	for i := range l.Pix {
		l.Pix[i] = 0.5
	}
	BrightChromaDenoise(l)
	for i := range l.Pix {
		if l.Pix[i] != 0.5 {
			t.Fatalf("small image changed: %v", l.Pix[i])
		}
	}
}
