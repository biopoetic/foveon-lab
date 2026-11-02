package x3f

// Task 15: the dark-tone threshold boost and the patch-scale chroma restore
// are measured on the ladders; these tests pin the behaviour so a later
// change cannot silently switch either off.

import (
	"math"
	"testing"
)

func TestDarkBoost(t *testing.T) {
	local := []float32{0, 0.004, 0.005, 0.02}
	b := darkBoost(local, 2, 0.004, 0.006)
	if b == nil {
		t.Fatal("expected a boost plane")
	}
	if b[0] != 2 || b[1] != 2 {
		t.Errorf("at and below darkLo: %v, want 2", b[:2])
	}
	if b[3] != 1 {
		t.Errorf("above darkHi: %v, want 1", b[3])
	}
	if b[2] <= 1 || b[2] >= 2 {
		t.Errorf("between: %v, want strictly between 1 and 2", b[2])
	}
	if darkBoost(local, 1, 0.004, 0.006) != nil {
		t.Error("a multiplier of 1 must not allocate or boost")
	}
}

// TestChromaPatchKept: a saturated patch inside a neutral field keeps its
// chroma better on the Quattro path than off it. The wide colour filter's
// window is deliberately larger than the patch, which is the dp2 Quattro
// chart at ISO 6400.
func TestChromaPatchKept(t *testing.T) {
	const w, h = 192, 192
	mk := func() *CamImage {
		img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
		seed := uint32(12345)
		rnd := func() float32 {
			seed = seed*1664525 + 1013904223
			return float32(seed>>8)/float32(1<<24)*2 - 1
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := (y*w + x) * 3
				// neutral surround and a 48x48 red patch of the same
				// luminance: the wide filter's luminance guide cannot see the
				// colour edge, which is the chart's worst case
				img.Pix[i], img.Pix[i+1], img.Pix[i+2] = 0.060, 0.060, 0.060
				if x >= 72 && x < 120 && y >= 72 && y < 120 {
					img.Pix[i], img.Pix[i+1], img.Pix[i+2] = 0.120, 0.030, 0.030
				}
				for k := 0; k < 3; k++ {
					img.Pix[i+k] += 0.0015 * rnd()
				}
			}
		}
		return img
	}
	patchChroma := func(img *CamImage) float64 {
		var r, g, b float64
		var n int
		for y := 80; y < 112; y++ {
			for x := 80; x < 112; x++ {
				i := (y*w + x) * 3
				r += float64(img.Pix[i])
				g += float64(img.Pix[i+1])
				b += float64(img.Pix[i+2])
				n++
			}
		}
		r, g, b = r/float64(n), g/float64(n), b/float64(n)
		// max-min in layer units: a step in layer chroma
		return math.Max(r, math.Max(g, b)) - math.Min(r, math.Min(g, b))
	}
	plain := mk()
	oldCB := dnCB
	dnCB = 0 // this test pins the old wide filter's patch restore, not task 43
	Denoise(plain, DenoiseOptions{Strength: 0.3, ISO: 6400})
	withFix := mk()
	Denoise(withFix, DenoiseOptions{Strength: 0.3, ISO: 6400, Quattro: true})
	dnCB = oldCB
	cp, cf := patchChroma(plain), patchChroma(withFix)
	if cf < cp*1.05 {
		t.Errorf("Quattro patch chroma %.4f, without %.4f: the patch-scale restore did not run", cf, cp)
	}
}
