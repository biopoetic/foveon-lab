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

// The shoulder brings the frame's top under the white the correction would
// clip, leaves everything below the knee alone and keeps each pixel's colour.
func TestMaxDetailHighlights(t *testing.T) {
	const n, top = 4096, float32(0.5)
	l := &Linear{W: n, H: 1, Pix: make([]float32, n*3)}
	for i := 0; i < n; i++ {
		v := 2 * float32(i) / (n - 1)
		l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = v, v*0.8, v*0.5
	}
	MaxDetailHighlights(l, top)
	low := l.Pix[(n/20)*3] // input 0.1, under the knee
	if want := 2 * float32(n/20) / (n - 1); low != want {
		t.Errorf("below the knee: %g, want %g", low, want)
	}
	prev := float32(-1)
	for i := 0; i < n; i++ {
		p := l.Pix[i*3 : i*3+3]
		if p[0] < prev-1e-5 {
			t.Fatalf("not monotonic at %d: %g after %g", i, p[0], prev)
		}
		prev = p[0]
		if p[0] > top+1e-4 {
			t.Fatalf("%d is over the top: %g", i, p[0])
		}
		if p[0] > 0 {
			if g := p[1] / p[0]; g < 0.799 || g > 0.801 {
				t.Fatalf("colour moved at %d: green/red %g", i, g)
			}
		}
	}
	if prev < top*0.98 {
		t.Errorf("the frame's top lands at %g, want near %g", prev, top)
	}
}

// A blue-dominant highlight must not be turned into a lilac patch: the
// shoulder fades out smoothly with the blue excess, while a neutral or
// green-dominant highlight at the same level is still compressed. This is the
// SDIM0031 leaf: its blown speculars are blue, and revealing them as a dark
// violet was the visible task 27 fault.
func TestMaxDetailHighlightsBlueFade(t *testing.T) {
	const top = float32(0.5)
	mk := func(r, g, b float32) *Linear {
		l := &Linear{W: 8, H: 8, Pix: make([]float32, 8*8*3)}
		for i := 0; i < 8*8; i++ {
			l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = r, g, b
		}
		return l
	}
	neutral := mk(2, 2, 2)
	blue := mk(1.4, 1.5, 2)
	green := mk(1.2, 2, 1.3)
	MaxDetailHighlights(neutral, top)
	MaxDetailHighlights(blue, top)
	MaxDetailHighlights(green, top)
	if neutral.Pix[0] >= 1.99 {
		t.Errorf("neutral highlight was not compressed: %g", neutral.Pix[0])
	}
	if blue.Pix[2] < 1.99 {
		t.Errorf("blue highlight was compressed to %g, want the shoulder faded out", blue.Pix[2])
	}
	if green.Pix[1] >= 1.99 {
		t.Errorf("green highlight was not compressed: %g", green.Pix[1])
	}
	// The fade is smooth: a small blue excess gets a partial, not a stepped,
	// compression.
	mid := mk(1.8, 1.9, 2)
	MaxDetailHighlights(mid, top)
	if mid.Pix[2] >= 2 || mid.Pix[2] <= float32(top) {
		t.Errorf("partial blue fade landed at %g, want between the compressed and unchanged levels", mid.Pix[2])
	}
}

// Task 29: the shoulder must follow the rebuilt-pixel gate. An unclipped
// highlight (gate false) keeps its value bit for bit; a gated one is
// compressed under the top; the boundary is feathered, not stepped; and a
// wrong-sized gate falls back to the old global shoulder.
func TestMaxDetailHighlightsGate(t *testing.T) {
	const n, top = 512, float32(0.5)
	mk := func() *Linear {
		l := &Linear{W: n, H: 1, Pix: make([]float32, n*3)}
		for i := 0; i < n; i++ {
			v := 2 * float32(i) / (n - 1)
			l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = v, v*0.8, v*0.5
		}
		return l
	}
	gate := make([]bool, n)
	for i := 0; i < n/2; i++ {
		gate[i] = true
	}
	l := mk()
	before := append([]float32(nil), l.Pix...)
	MaxDetailHighlightsGate(l, top, gate)
	// Far from the boundary on the gated side: compressed under the top,
	// hue kept.
	i := n / 4
	if p := l.Pix[i*3 : i*3+3]; p[0] > top+1e-4 || p[0] >= before[i*3] {
		t.Errorf("gated pixel %d not compressed: %g (was %g), top %g", i, p[0], before[i*3], top)
	} else if g := p[1] / p[0]; g < 0.799 || g > 0.801 {
		t.Errorf("gated pixel %d colour moved: green/red %g", i, g)
	}
	// Far from the boundary on the open side: untouched.
	i = n - n/8
	for k := 0; k < 3; k++ {
		if got := l.Pix[i*3+k]; got != before[i*3+k] {
			t.Errorf("ungated pixel %d channel %d moved: %g -> %g", i, k, before[i*3+k], got)
		}
	}
	// The boundary is a two-pixel feather, so the gated mask ends after it
	// and no pixel jumps from fully compressed to untouched.
	// A gate with no true entries is a no-op and reports no mask.
	none := make([]bool, n)
	l2 := mk()
	if m := MaxDetailHighlightsGate(l2, top, none); m != nil {
		t.Errorf("empty gate returned a mask")
	}
	for k := range l2.Pix {
		if l2.Pix[k] != before[k] {
			t.Fatalf("empty gate changed pixel %d", k)
		}
	}
	// A wrong-sized gate is ignored: the global shoulder runs.
	l3 := mk()
	if m := MaxDetailHighlightsGate(l3, top, []bool{true}); m == nil {
		t.Errorf("wrong-sized gate did not fall back to the global shoulder")
	}
	if l3.Pix[(n-1)*3] > top+1e-4 {
		t.Errorf("global fallback left %g over the top", l3.Pix[(n-1)*3])
	}
}

// Everything under the knee is exactly the normal development's value.
func TestMaxDetailHighlightsKnee(t *testing.T) {
	const top = float32(0.5)
	l := &Linear{W: 16, H: 1, Pix: make([]float32, 16*3)}
	for i := 0; i < 16; i++ {
		v := float32(i) / 15 * 0.5
		l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = v, v*0.8, v*0.5
	}
	before := append([]float32(nil), l.Pix...)
	MaxDetailHighlights(l, top)
	for i := 0; i < 16; i++ {
		p := before[i*3 : i*3+3]
		if max(p[0], max(p[1], p[2])) > 0.9*top+1e-6 {
			continue // above the knee: the whole pixel may be scaled
		}
		for k := 0; k < 3; k++ {
			if got := l.Pix[i*3+k]; got != before[i*3+k] {
				t.Fatalf("pixel %d under the knee moved: %g -> %g", i, before[i*3+k], got)
			}
		}
	}
}

// t30Block makes a white block on a dark ground: l is the pre-correction
// development, norm the normal display, open the never-clipped gate.
func t30Block(w, h, bx, by, bw, bh int, level float32, clipped bool) (*Linear, *Linear, []bool) {
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	norm := &Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	open := make([]bool, w*h)
	for i := range open {
		open[i] = true
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			in := x >= bx && x < bx+bw && y >= by && y < by+bh
			if !in {
				l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = 0.1, 0.08, 0.06
				norm.Pix[i*3], norm.Pix[i*3+1], norm.Pix[i*3+2] = 0.1, 0.08, 0.06
				continue
			}
			// A small repeated texture, so a level-dependent gain would
			// compress it and a uniform gain would not.
			tx := float32(1)
			if (x+y)%2 == 0 {
				tx = 1.02
			}
			l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2] = level*tx, level*0.9*tx, level*0.8*tx
			norm.Pix[i*3], norm.Pix[i*3+1], norm.Pix[i*3+2] = 1, 1, 1
			if clipped {
				open[i] = false
			}
		}
	}
	return l, norm, open
}

// TestMaxDetailOpenHighlights pins the scope: an open, display-white area
// with real headroom is pulled down by a uniform exposure, its texture keeps
// its relative contrast, and every gate that has nothing to recover returns
// nil with the image untouched.
func TestMaxDetailOpenHighlights(t *testing.T) {
	const w, h = 200, 200
	const bx, by, bw, bh = 75, 75, 50, 50
	top := float32(1)
	// The recovering case: open, white, level 2 (2 x white).
	l, norm, open := t30Block(w, h, bx, by, bw, bh, 2, false)
	before := append([]float32(nil), l.Pix...)
	m := MaxDetailOpenHighlights(l, norm, open, MaxDetailOpenOptions{Top: top})
	if m == nil {
		t.Fatal("no mask for an open white block with headroom")
	}
	at := func(x, y int) int { return (y*w + x) * 3 }
	c := at(bx+bw/2, by+bh/2)
	if got, want := l.Pix[c], float32(2*1.02*(1-MaxDetailOpenAmount)); got < want-1e-4 || got > want+1e-4 {
		t.Errorf("centre %g, want %g", got, want)
	}
	if !m[(by+bh/2)*w+bx+bw/2] {
		t.Error("centre not reported as moved")
	}
	// Same gain on the bright and dim texture samples: the relative
	// contrast is kept.
	bright, dim := l.Pix[c], l.Pix[c+3]
	if r := bright / dim; r < 1.019 || r > 1.021 {
		t.Errorf("texture compressed: bright/dim %g, want 1.02", r)
	}
	// The dark ground is untouched and not reported.
	for _, p := range [][2]int{{0, 0}, {w - 1, h - 1}, {10, 100}} {
		i := p[1]*w + p[0]
		if m[i] {
			t.Errorf("ground pixel %v reported moved", p)
		}
		for k := 0; k < 3; k++ {
			if l.Pix[i*3+k] != before[i*3+k] {
				t.Errorf("ground pixel %v moved: %g -> %g", p, before[i*3+k], l.Pix[i*3+k])
			}
		}
	}

	// No white at all: nil and bit-identical.
	l2, norm2, open2 := t30Block(w, h, 0, 0, 0, 0, 0, false)
	before2 := append([]float32(nil), l2.Pix...)
	if m := MaxDetailOpenHighlights(l2, norm2, open2, MaxDetailOpenOptions{Top: top}); m != nil {
		t.Error("mask without a white area")
	}
	for i := range before2 {
		if l2.Pix[i] != before2[i] {
			t.Fatal("image changed without a white area")
		}
	}

	// White, open, but nearly no headroom over white: nothing to recover.
	l3, norm3, open3 := t30Block(w, h, bx, by, bw, bh, 1.2, false)
	before3 := append([]float32(nil), l3.Pix...)
	if m := MaxDetailOpenHighlights(l3, norm3, open3, MaxDetailOpenOptions{Top: top}); m != nil {
		t.Error("mask for a white area with no headroom")
	}
	for i := range before3 {
		if l3.Pix[i] != before3[i] {
			t.Fatal("image changed without headroom")
		}
	}

	// White and deep, but clipped: the data is lost, it must stay white.
	l4, norm4, open4 := t30Block(w, h, bx, by, bw, bh, 2, true)
	before4 := append([]float32(nil), l4.Pix...)
	if m := MaxDetailOpenHighlights(l4, norm4, open4, MaxDetailOpenOptions{Top: top}); m != nil {
		t.Error("mask for a clipped white area")
	}
	for i := range before4 {
		if l4.Pix[i] != before4[i] {
			t.Fatal("image changed without open data")
		}
	}

	// A wrong-sized gate is not trusted.
	l5, norm5, _ := t30Block(w, h, bx, by, bw, bh, 2, false)
	if m := MaxDetailOpenHighlights(l5, norm5, []bool{true}, MaxDetailOpenOptions{Top: top}); m != nil {
		t.Error("mask from a wrong-sized gate")
	}
}
