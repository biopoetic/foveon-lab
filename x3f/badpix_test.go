package x3f

import "testing"

// TestFixBadPixels: a listed pixel takes its good neighbours' mean; a
// listed 2-pixel cluster fills in from its edges.
func TestFixBadPixels(t *testing.T) {
	raw := &RawImage{W: 6, H: 5, Pix: make([]uint16, 6*5*3)}
	for i := range raw.Pix {
		raw.Pix[i] = 100
	}
	set := func(x, y int, v uint16) {
		for k := 0; k < 3; k++ {
			raw.Pix[(y*6+x)*3+k] = v
		}
	}
	set(2, 2, 4000) // a hot pixel
	set(4, 1, 0)    // a dead pair
	set(4, 2, 0)
	c := &CAMF{
		Matrices: map[string][]float64{"BadPixelsF20": {2, 2, 1, 1, 4, 1, 2, 4, 1}},
		Dims:     map[string][]int{"BadPixelsF20": {3, 3}},
	}
	if n := FixBadPixels(raw, c); n != 3 {
		t.Fatalf("fixed %d, want 3", n)
	}
	for _, p := range [][2]int{{2, 2}, {4, 1}, {4, 2}} {
		if v := raw.Pix[(p[1]*6+p[0])*3]; v != 100 {
			t.Errorf("pixel %v = %d, want 100", p, v)
		}
	}
}
