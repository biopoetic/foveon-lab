// Despeckle removes single-pixel outliers from a camera-linear image: a
// sample that sits far from its 3x3 neighbourhood's median, in one channel,
// is replaced by that median. Sigma's CAMF badness table lists the bad
// pixels a body left the factory with, but a stuck or hot sample that is not
// on it survives the colour matrix and shows up as a bright coloured dot in
// dark areas (an SD1 Merrill frame at ISO 100 showed them in black fur, in
// both the Vivid and the Neutral presets). Neighbourhood smoothing cannot
// remove them - they are single pixels, not noise - and a median filter over
// the whole image would soften detail, so only the deviating channel of a
// pixel that is far off is touched.
package render

import (
	"math"
	"sync/atomic"
)

// Despeckle replaces a channel sample that is more than fraction away from
// its 3x3 median (and above floor in absolute terms) with that median, but
// only when the eight neighbours are themselves flat (their span is below
// flatSpan). A hot or dead pixel sits alone on a flat patch; a star or a
// specular point has a PSF that lights its neighbours too, so its span is
// large and it is left alone. It returns how many samples it changed.
func Despeckle(l *Linear, fraction, floor, flatSpan float64) int {
	if l == nil || l.W < 3 || l.H < 3 {
		return 0
	}
	src := l.Pix
	dst := make([]float32, len(src))
	copy(dst, src)
	var changed int64
	parallelRows(l.H, func(y0, y1 int) {
		var n [9]float64
		y0 = max(y0, 1)
		y1 = min(y1, l.H-1)
		for y := y0; y < y1; y++ {
			row := y * l.W
			for x := 1; x < l.W-1; x++ {
				i := (row + x) * 3
				for k := 0; k < 3; k++ {
					v := float64(src[i+k])
					m := 0
					lo, hi := math.MaxFloat64, -math.MaxFloat64
					for dy := -1; dy <= 1; dy++ {
						for dx := -1; dx <= 1; dx++ {
							s := float64(src[((y+dy)*l.W+x+dx)*3+k])
							n[m] = s
							m++
							if dx != 0 || dy != 0 {
								lo, hi = math.Min(lo, s), math.Max(hi, s)
							}
						}
					}
					med := median9(n)
					if med < 1e-6 || hi-lo > flatSpan {
						continue
					}
					d := v - med
					if d < 0 {
						d = -d
					}
					// Both relative and absolute: a shadow sample can be a
					// large fraction off while being a couple of DN, which is
					// noise.
					if d > fraction*med && d > floor {
						dst[i+k] = float32(med)
						atomic.AddInt64(&changed, 1)
					}
				}
			}
		}
	})
	l.Pix = dst
	return int(changed)
}

// median9 is the median of nine samples, sorted by insertion (n is small and
// this runs once per channel per pixel).
func median9(n [9]float64) float64 {
	var s [9]float64
	for i := 0; i < 9; i++ {
		v := n[i]
		j := i
		for j > 0 && s[j-1] > v {
			s[j] = s[j-1]
			j--
		}
		s[j] = v
	}
	return s[4]
}
