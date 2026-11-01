package x3f

// Task 26: the synthetic exposure ramp with a known answer. One layer is
// twice as sensitive as the next and four times the last, so the first layer
// clips at E=1, the second at E=2 and the third at E=4: two full stops in
// which a clipped layer still has a live sibling. The test pins the error of
// the rebuilt value; the old code used the fitted ratio itself as the value
// and missed the source's level, which this ramp catches immediately.

import (
	"math"
	"testing"
)

func TestRecoverHighlightsKnownRamp(t *testing.T) {
	const w, h = 64, 256
	sens := [3]float64{1.0, 0.5, 0.25}
	img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	exposure := func(y int) float64 { return 0.2 + 4.8*float64(y)/float64(h-1) }
	for y := 0; y < h; y++ {
		E := exposure(y)
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			for k := 0; k < 3; k++ {
				p := E * sens[k]
				if p > 0.995 {
					p = 0.995
				}
				img.Pix[i+k] = float32(p)
			}
		}
	}
	st, class := RecoverHighlightsClass(img, [3]float64{1, 1, 1}, 0.995)
	if st.Partial == 0 || st.Lost == 0 {
		t.Fatalf("ramp did not exercise both partial and lost: %+v", st)
	}
	var maxErr, sumErr float64
	var n int
	var recoveredRows int
	for y := 0; y < h; y++ {
		E := exposure(y)
		nClip := 0
		for k := 0; k < 3; k++ {
			if E*sens[k] >= highlightClip {
				nClip++
			}
		}
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			c := class[y*w+x]
			if nClip == 3 {
				if c != HLLost {
					t.Fatalf("E=%.2f all-clipped class %d, want lost", E, c)
				}
				// Nothing is invented: the pixel stays neutral.
				q0, q1, q2 := float64(img.Pix[i]), float64(img.Pix[i+1]), float64(img.Pix[i+2])
				if math.Abs(q0-q1) > 1e-4 || math.Abs(q1-q2) > 1e-4 {
					t.Fatalf("E=%.2f lost pixel not neutral: %g %g %g", E, q0, q1, q2)
				}
				continue
			}
			if nClip == 0 {
				continue
			}
			if c != HLRecovered {
				t.Fatalf("E=%.2f partial class %d, want recovered", E, c)
			}
			recoveredRows++
			for k := 0; k < 3; k++ {
				if E*sens[k] < highlightClip {
					continue
				}
				got := float64(img.Pix[i+k])
				want := E * sens[k]
				e := math.Abs(math.Log2(got / want))
				sumErr += e
				n++
				if e > maxErr {
					maxErr = e
				}
			}
		}
	}
	if n == 0 || recoveredRows == 0 {
		t.Fatalf("no rebuilt samples: n=%d rows=%d", n, recoveredRows)
	}
	// The ratio is exactly constant on this ramp, so the rebuild is exact up
	// to float32: 0.01 stop is a loose pin that still fails a missing q_s.
	if maxErr > 0.01 {
		t.Fatalf("rebuilt value error %.4f stops (mean %.4f), want <= 0.01", maxErr, sumErr/float64(n))
	}
}

// A clipped layer with no live sibling must not be invented: the pixel stays
// neutral at the brightest layer, and the class says it was lost.
func TestRecoverHighlightsNoSibling(t *testing.T) {
	const w, h = 16, 16
	img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	for i := 0; i < w*h; i++ {
		img.Pix[i*3] = 0.999
		img.Pix[i*3+1] = 0.999
		img.Pix[i*3+2] = 0.999
	}
	st, class := RecoverHighlightsClass(img, [3]float64{1, 1, 1}, 0.995)
	if st.Recovered != [3]int{} || st.Lost != w*h {
		t.Fatalf("invented signal with no sibling: %+v", st)
	}
	for i, c := range class {
		if c != HLLost {
			t.Fatalf("pixel %d class %d, want lost", i, c)
		}
	}
}

// The safety cap: a wildly dark sibling must not turn a clipped pixel into a
// white hole much above the clip. The cap is in white-balanced units.
func TestRecoverHighlightsCap(t *testing.T) {
	const w, h = 16, 16
	img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	for i := 0; i < w*h; i++ {
		img.Pix[i*3] = 0.995 // clipped
		img.Pix[i*3+1] = 0.02
		img.Pix[i*3+2] = 0.02
	}
	RecoverHighlights(img, [3]float64{1, 1, 1}, 0.995)
	for i := 0; i < w*h; i++ {
		if v := float64(img.Pix[i*3]); v > maxRecoverQ+1e-4 || math.IsNaN(v) {
			t.Fatalf("recovered value %g above cap %g", v, maxRecoverQ)
		}
	}
}
