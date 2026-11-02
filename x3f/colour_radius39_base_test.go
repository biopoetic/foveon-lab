package x3f

import "testing"

// Task 39: the high-ISO radius must not reach a base-ISO frame. The filter
// is deterministic, so the strongest form of "ISO 400 and below stay
// bit-identical" is that changing the high-ISO radius leaves a base-ISO
// denoise byte for byte the same.
func TestHighRadiusDoesNotTouchBaseISO(t *testing.T) {
	mk := func() *CamImage {
		const w, h = 64, 64
		img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
		seed := uint32(99)
		rnd := func() float32 {
			seed = seed*1664525 + 1013904223
			return float32(seed>>8)/float32(1<<24)*2 - 1
		}
		for i := range img.Pix {
			img.Pix[i] = 0.12 + 0.002*rnd()
		}
		return img
	}
	for _, iso := range []float64{100, 400} {
		dnHighR = 0
		a := mk()
		Denoise(a, DenoiseOptions{Strength: 0.3, ISO: iso})
		dnHighR = 32
		b := mk()
		Denoise(b, DenoiseOptions{Strength: 0.3, ISO: iso})
		dnHighR = 0
		for i := range a.Pix {
			if a.Pix[i] != b.Pix[i] {
				t.Fatalf("ISO %g pixel %d changed with the high-ISO radius: %g -> %g", iso, i, a.Pix[i], b.Pix[i])
			}
		}
	}
}
