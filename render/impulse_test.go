package render

import (
	"image"
	"testing"
)

// A lone bright dot on a flat patch is removed; a star whose PSF lights its
// neighbours, and the sloping edge of a real object, are kept.
func TestImpulseCleanKeepsStars(t *testing.T) {
	const w, h = 32, 32
	base := func() *image.RGBA {
		m := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < w*h; i++ {
			// A flat grey with a little deterministic noise, so the
			// neighbour span is what a denoised flat area really has.
			n := (i*1103515245 + 12345) & 0x7fffffff
			v := uint8(89 + n%3)
			m.Pix[i*4], m.Pix[i*4+1], m.Pix[i*4+2], m.Pix[i*4+3] = v, v, v, 255
		}
		return m
	}
	at := func(m *image.RGBA, x, y int) uint8 { return m.Pix[(y*w+x)*4] }

	m := base()
	m.Pix[(16*w+16)*4], m.Pix[(16*w+16)*4+1], m.Pix[(16*w+16)*4+2] = 240, 240, 240
	if n := impulseClean8(m, 80, 12); n != 1 {
		t.Fatalf("dot: cleaned %d px, want 1", n)
	}
	if v := at(m, 16, 16); v < 88 || v > 92 {
		t.Fatalf("dot: %d, want the flat neighbours' ~90", v)
	}

	// A star: the centre is bright and the wings light all eight neighbours,
	// so their span is far above the flat threshold.
	m = base()
	for i, v := range []uint8{120, 180, 130, 190, 250, 140, 200, 135, 150} {
		p := (16+i/3-1)*w + 16 + i%3 - 1
		m.Pix[p*4], m.Pix[p*4+1], m.Pix[p*4+2] = v, v, v
	}
	before := at(m, 16, 16)
	if n := impulseClean8(m, 80, 12); n != 0 {
		t.Fatalf("star: cleaned %d px, want 0", n)
	}
	if at(m, 16, 16) != before {
		t.Fatalf("star centre changed: %d -> %d", before, at(m, 16, 16))
	}

	// A one-pixel step of a real edge: the pixel beside it has the same
	// value on one side, so its neighbours span the step and it is kept.
	m = base()
	for y := 0; y < h; y++ {
		for x := 20; x < w; x++ {
			p := (y*w + x) * 4
			m.Pix[p], m.Pix[p+1], m.Pix[p+2] = 200, 200, 200
		}
	}
	if n := impulseClean8(m, 80, 12); n != 0 {
		t.Fatalf("edge: cleaned %d px, want 0", n)
	}
}
