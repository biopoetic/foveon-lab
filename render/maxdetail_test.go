package render

import "testing"

// Task 19: the Max Detail tone map must keep black, lift shadows above the
// noise, stay monotonic and end at white.
func TestMaxDetailDevelopRange(t *testing.T) {
	const w, h = 256, 8
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(2 * float64(x) / float64(w-1))
			i := (y*w + x) * 3
			l.Pix[i], l.Pix[i+1], l.Pix[i+2] = v, v, v
		}
	}
	MaxDetailDevelop(l, MaxDetailOptions{ISO: 100})
	prev := -1.0
	for x := 0; x < w; x++ {
		v := float64(l.Pix[(h/2*w+x)*3])
		if v < prev-1e-5 {
			t.Fatalf("not monotonic at x=%d: %g after %g", x, v, prev)
		}
		prev = v
	}
	if l.Pix[0] != 0 {
		t.Fatalf("black is %g, want 0", l.Pix[0])
	}
	if v := float64(l.Pix[((h/2)*w+w-1)*3]); v < 0.99 {
		t.Fatalf("white ends at %g, want >= 0.99", v)
	}
	// A shadow well above the noise floor is lifted.
	dark := 12
	in := 2 * float64(dark) / float64(w-1)
	if v := float64(l.Pix[(h/2*w+dark)*3]); v <= in {
		t.Fatalf("shadow %g was not lifted: %g (pix %v)", in, v, l.Pix[(h/2*w+dark)*3:(h/2*w+dark)*3+3])
	}
}

// A high ISO frame gets less lift than a clean one, and its noise floor is
// not lifted into pepper.
func TestMaxDetailHighISO(t *testing.T) {
	mk := func() *Linear {
		l := &Linear{W: 64, H: 8, Pix: make([]float32, 64*8*3)}
		for i := 0; i < 64*8; i++ {
			v := float32(0.01)
			l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = v, v, v
		}
		return l
	}
	lo, hi := mk(), mk()
	MaxDetailDevelop(lo, MaxDetailOptions{ISO: 100})
	MaxDetailDevelop(hi, MaxDetailOptions{ISO: 6400})
	if hi.Pix[0] >= lo.Pix[0] {
		t.Fatalf("ISO 6400 lifted a flat shadow as much as ISO 100 (%g vs %g)", hi.Pix[0], lo.Pix[0])
	}
}
