package render

import (
	"math"
	"testing"
)

// Area resampling keeps the mean and keeps features in place: a centred
// bright bar stays centred at any ratio (the integer-edge box drifted by a
// fraction of a pixel per step).
func TestAreaResample(t *testing.T) {
	sw, sh := 2352, 7
	src := make([]float32, sw*sh*3)
	for y := 0; y < sh; y++ {
		for x := sw/2 - 8; x < sw/2+8; x++ {
			for k := 0; k < 3; k++ {
				src[(y*sw+x)*3+k] = 1
			}
		}
	}
	for _, dw := range []int{1600, 1200, 1176, 803} {
		out := areaResample(src, sw, sh, dw, sh)
		var sum, cx float64
		for x := 0; x < dw; x++ {
			v := float64(out[(3*dw+x)*3])
			sum += v
			cx += v * (float64(x) + 0.5)
		}
		if want := 16 * float64(dw) / float64(sw); math.Abs(sum-want) > 1e-3 {
			t.Errorf("width %d: sum %.4f, want %.4f", dw, sum, want)
		}
		if c := cx / sum; math.Abs(c-float64(dw)/2) > 1e-3 {
			t.Errorf("width %d: bar centred at %.4f, want %.4f", dw, c, float64(dw)/2)
		}
	}
}
