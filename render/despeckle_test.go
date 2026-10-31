package render

import "testing"

// A hot sample alone on a flat patch is repaired; a bright point whose
// neighbours are lit too (a star's PSF, a specular highlight) is kept.
func TestDespeckleKeepsStars(t *testing.T) {
	const w, h = 16, 16
	base := func() *Linear {
		l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
		for i := 0; i < w*h; i++ {
			l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = 0.20, 0.20, 0.20
		}
		return l
	}
	at := func(l *Linear, x, y int) float32 { return l.Pix[(y*w+x)*3] }

	l := base()
	l.Pix[(8*w+8)*3] = 0.80
	if n := Despeckle(l, 0.5, 0.01, 0.05); n != 1 {
		t.Fatalf("hot sample: fixed %d, want 1", n)
	}
	if v := at(l, 8, 8); v < 0.19 || v > 0.21 {
		t.Fatalf("hot sample: %v, want the flat neighbours' 0.20", v)
	}

	// A star: the centre is bright and the eight neighbours carry the sloping
	// wings of the PSF, so their own span is too large for a defect repair.
	l = base()
	for i, v := range []float32{0.22, 0.30, 0.24, 0.34, 0.90, 0.26, 0.40, 0.28, 0.32} {
		l.Pix[((8+i/3-1)*w+8+i%3-1)*3] = v
	}
	before := at(l, 8, 8)
	if n := Despeckle(l, 0.5, 0.01, 0.05); n != 0 {
		t.Fatalf("star: fixed %d samples, want 0", n)
	}
	if at(l, 8, 8) != before {
		t.Fatalf("star centre changed: %v -> %v", before, at(l, 8, 8))
	}

	// Hot samples spread over the rows exercise the parallel bands: a
	// worker-local median buffer must not leak between them.
	l = base()
	want := 0
	for y := 2; y < h-2; y += 3 {
		l.Pix[(y*w+4)*3] = 0.80
		want++
	}
	if n := Despeckle(l, 0.5, 0.01, 0.05); n != want {
		t.Fatalf("row spread: fixed %d, want %d", n, want)
	}
	for y := 2; y < h-2; y += 3 {
		if v := at(l, 4, y); v < 0.19 || v > 0.21 {
			t.Fatalf("row %d: %v, want 0.20", y, v)
		}
	}
}
