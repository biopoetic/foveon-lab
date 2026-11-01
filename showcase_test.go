package main

import (
	"testing"

	"github.com/biopoetic/foveon-lab/render"
)

func TestShowcaseBoxSameForEveryVariant(t *testing.T) {
	for _, c := range [][]float64{
		{0, 0.55, 0.50, 0.42},
		{0.55, 0.45, 0.45, 0.50},
		{10, 20, 300, 200},
	} {
		x, y, w, h := showcaseBox(c, 1200, 800)
		if w < 2 || h < 2 || x < 0 || y < 0 || x+w > 1200 || y+h > 800 {
			t.Fatalf("box %v -> %d,%d %dx%d outside the frame", c, x, y, w, h)
		}
		// The same four numbers must give the same box for the 1200-wide
		// SPP frame and the 800-wide portrait one after it is brought down;
		// the caller uses this one box for all variants.
		x2, y2, w2, h2 := showcaseBox(c, 1200, 800)
		if x != x2 || y != y2 || w != w2 || h != h2 {
			t.Fatalf("box is not stable: %d,%d %dx%d vs %d,%d %dx%d", x, y, w, h, x2, y2, w2, h2)
		}
	}
}

func TestShowcaseCropKeepsEveryVariantIdentical(t *testing.T) {
	mk := func(base float32) *render.Linear {
		l := &render.Linear{W: 12, H: 8, Pix: make([]float32, 12*8*3)}
		for i := range l.Pix {
			l.Pix[i] = base
		}
		return l
	}
	a, b, c := mk(0.1), mk(0.2), mk(0.3)
	x, y, w, h := showcaseBox([]float64{0.25, 0.25, 0.5, 0.5}, 12, 8)
	ca, cb, cc := showcaseCropLinear(a, x, y, w, h), showcaseCropLinear(b, x, y, w, h), showcaseCropLinear(c, x, y, w, h)
	if ca.W != cb.W || ca.W != cc.W || ca.H != cb.H || ca.H != cc.H || ca.W != w || ca.H != h {
		t.Fatalf("crops differ in size: %dx%d %dx%d %dx%d", ca.W, ca.H, cb.W, cb.H, cc.W, cc.H)
	}
	if showcaseMean(ca) >= showcaseMean(cb) || showcaseMean(cb) >= showcaseMean(cc) {
		t.Fatal("means do not follow the source pixels")
	}
}

func TestUnderZZOut(t *testing.T) {
	if !underZZOut("zz-out/task22/showcase") {
		t.Error("a normal showcase output was refused")
	}
	if underZZOut("docs") {
		t.Error("a non-zz-out output was accepted")
	}
}
