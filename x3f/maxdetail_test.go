package x3f

import (
	"math"
	"testing"
)

// Task 19: a clipped layer is rebuilt from the ones still holding signal.
func TestRecoverHighlightsRebuilds(t *testing.T) {
	const w, h = 32, 32
	img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	for i := 0; i < w*h; i++ {
		img.Pix[i*3] = 0.55   // bottom
		img.Pix[i*3+1] = 0.50 // middle
		img.Pix[i*3+2] = 0.65 // top: ratio to middle 1.3
	}
	// A clipped top: the local ratio says 1.3 x 0.9 = 1.17, above white.
	c := (16*w + 16) * 3
	img.Pix[c+1] = 0.90
	img.Pix[c+2] = 1.00
	// All three clipped: no detail left, neutralise at the brightest.
	a := (4*w + 4) * 3
	img.Pix[a], img.Pix[a+1], img.Pix[a+2] = 1.0, 1.1, 1.2
	st := RecoverHighlights(img, [3]float64{1, 1, 1}, 0.995)
	if st.Recovered[2] == 0 {
		t.Fatalf("no top-layer recovery: %+v", st)
	}
	if img.Pix[c+2] <= 1.0 {
		t.Fatalf("top stayed at %g, want > 1", img.Pix[c+2])
	}
	if st.Lost == 0 {
		t.Fatalf("all-clipped pixel not counted: %+v", st)
	}
	// The lost pixel is neutral in white-balanced units.
	q0, q1, q2 := img.Pix[a], img.Pix[a+1], img.Pix[a+2]
	if math.Abs(float64(q0-q1)) > 1e-4 || math.Abs(float64(q1-q2)) > 1e-4 {
		t.Fatalf("all-clipped pixel not neutralised: %g %g %g", q0, q1, q2)
	}
}

// The clipped-white test: with a high white balance gain the raw threshold
// is far below output white, so the clip must be judged in balanced units.
func TestClipHighlightsHighGain(t *testing.T) {
	img := &CamImage{W: 1, H: 1, Pix: []float32{0.030, 0.020, 0.030}}
	gain := [3]float64{36, 29, 30}
	ClipHighlights(img, gain)
	var q [3]float64
	for k := 0; k < 3; k++ {
		q[k] = float64(img.Pix[k]) * gain[k]
	}
	mx := math.Max(q[0], math.Max(q[1], q[2]))
	for k := 0; k < 3; k++ {
		if q[k] < 0.95*mx {
			t.Fatalf("layer %d still unbalanced: q=%g max=%g", k, q[k], mx)
		}
	}
}

// A normal gain keeps the task-18 result bit-for-bit (the fix is scoped to
// the frames whose rendered white is many stops below the raw white).
func TestClipHighlightsNormalGainUnchanged(t *testing.T) {
	a := &CamImage{W: 1, H: 1, Pix: []float32{0.95, 0.80, 0.85}}
	b := &CamImage{W: 1, H: 1, Pix: []float32{0.95, 0.80, 0.85}}
	ClipHighlights(a, [3]float64{2.7, 1.25, 0.7})
	// The task-18 expression, on the same input.
	p := b.Pix
	const lo, hi = 0.9, 1.0
	m := max(p[0], max(p[1], p[2]))
	h := min(1, (m-lo)/(hi-lo))
	h = h * h * (3 - 2*h)
	g := [3]float32{2.7, 1.25, 0.7}
	l := max(p[0]*g[0], max(p[1]*g[1], p[2]*g[2]))
	for k := 0; k < 3; k++ {
		p[k] += h * (l/g[k] - p[k])
	}
	for k := 0; k < 3; k++ {
		if a.Pix[k] != b.Pix[k] {
			t.Fatalf("normal-gain clip changed at %d: %g vs %g", k, a.Pix[k], b.Pix[k])
		}
	}
}
