// Single-pixel impulse dots in a denoised render. A hot or stuck sensor
// sample that the CAMF bad-pixel list does not cover is one sample, so no
// neighbourhood filter removes it: the denoiser smooths it a little, and
// once the grain around it is gone it is the sharpest thing in the frame.
// At DP1 Merrill ISO 6400 the DN80 metric counted 198 dots/MP in the flat
// areas against SPP's 0.2, and a dot is white in a dark surround because
// the sRGB curve near black is steep.
//
// The camera-linear despeckle (in the decode path) removes the large
// outliers, but its absolute floor (0.01) is tens of noise sigmas at ISO
// 6400: it cannot see a sample that is only 0.0005 linear above a 0.0001
// neighbourhood and still becomes a 100-level dot after the colour matrix,
// the per-photo raw correction and the tone curve. This pass works on the
// finished 8-bit sRGB frame, where the dot is what the user sees, and uses
// the same test as the impulse metric: a pixel more than dev levels from
// the median of its eight neighbours, which themselves span less than span
// levels. Both are noise-derived in that domain (80 levels is 8-10 sigma
// at ISO 6400, 20 at ISO 800), so the test follows what is visible at every
// ISO, and it runs after sharpening so a dot's ring does not survive it.
// A star or a specular point has a PSF that lights its neighbours, so their
// span is large and the pixel is left alone.
package render

import (
	"image"
	"os"
	"sync/atomic"
)

// noImpulse is the research switch that keeps the dots (FOVEON_DN_NOIMP=1).
var noImpulse = os.Getenv("FOVEON_DN_NOIMP") != ""

// impulseClean8 removes isolated single-pixel luma impulses from an 8-bit
// sRGB image, replacing each layer of a flagged pixel with that layer's 3x3
// median. dev is the minimum luma deviation and span the largest neighbour
// luma span that still counts as flat, both in 8-bit levels. It returns how
// many pixels it changed.
func impulseClean8(img *image.RGBA, dev, span int) int {
	if img == nil || dev <= 0 || span <= 0 {
		return 0
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 3 || h < 3 {
		return 0
	}
	E := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		p := img.Pix[i*4:]
		E[i] = 0.2126*float32(p[0]) + 0.7152*float32(p[1]) + 0.0722*float32(p[2])
	}
	src := append([]uint8(nil), img.Pix...)
	thr := float32(dev)
	flat := float32(span)
	var changed int64
	parallelRows(h, func(y0, y1 int) {
		var nb, ch [8]float32
		y0, y1 = max(y0, 1), min(y1, h-1)
		for y := y0; y < y1; y++ {
			for x := 1; x < w-1; x++ {
				i := y*w + x
				j := 0
				lo, hi := E[i-w-1], E[i-w-1]
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						v := E[i+dy*w+dx]
						nb[j] = v
						j++
						lo, hi = min(lo, v), max(hi, v)
					}
				}
				if hi-lo >= flat {
					continue
				}
				d := E[i] - median8f(nb)
				if d < 0 {
					d = -d
				}
				if d <= thr {
					continue
				}
				for c := 0; c < 3; c++ {
					j := 0
					for dy := -1; dy <= 1; dy++ {
						for dx := -1; dx <= 1; dx++ {
							if dx == 0 && dy == 0 {
								continue
							}
							ch[j] = float32(src[((i+dy*w+dx)*4)+c])
							j++
						}
					}
					img.Pix[i*4+c] = uint8(median8f(ch) + 0.5)
				}
				atomic.AddInt64(&changed, 1)
			}
		}
	})
	return int(changed)
}

// impulseClean16 is impulseClean8 on a 16-bit sRGB image (TIFF export);
// dev and span are in 8-bit levels and scaled to 16 bits by the caller.
func impulseClean16(img *image.RGBA64, dev, span int) int {
	if img == nil || dev <= 0 || span <= 0 {
		return 0
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 3 || h < 3 {
		return 0
	}
	E := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		p := img.Pix[i*8:]
		E[i] = 0.2126*float32(uint16(p[0])<<8|uint16(p[1])) +
			0.7152*float32(uint16(p[2])<<8|uint16(p[3])) +
			0.0722*float32(uint16(p[4])<<8|uint16(p[5]))
	}
	src := append([]uint8(nil), img.Pix...)
	thr := float32(dev)
	flat := float32(span)
	var changed int64
	parallelRows(h, func(y0, y1 int) {
		var nb, ch [8]float32
		y0, y1 = max(y0, 1), min(y1, h-1)
		for y := y0; y < y1; y++ {
			for x := 1; x < w-1; x++ {
				i := y*w + x
				j := 0
				lo, hi := E[i-w-1], E[i-w-1]
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						v := E[i+dy*w+dx]
						nb[j] = v
						j++
						lo, hi = min(lo, v), max(hi, v)
					}
				}
				if hi-lo >= flat {
					continue
				}
				d := E[i] - median8f(nb)
				if d < 0 {
					d = -d
				}
				if d <= thr {
					continue
				}
				for c := 0; c < 3; c++ {
					j := 0
					for dy := -1; dy <= 1; dy++ {
						for dx := -1; dx <= 1; dx++ {
							if dx == 0 && dy == 0 {
								continue
							}
							o := (i + dy*w + dx) * 8
							ch[j] = float32(uint16(src[o+2*c])<<8 | uint16(src[o+2*c+1]))
							j++
						}
					}
					u := uint16(max(0, min(65535, median8f(ch)+0.5)))
					img.Pix[i*8+2*c], img.Pix[i*8+2*c+1] = uint8(u>>8), uint8(u)
				}
				atomic.AddInt64(&changed, 1)
			}
		}
	})
	return int(changed)
}

// median8f is the mean of the two middle values of eight samples (n is a
// copy, so the caller's slice is untouched).
func median8f(n [8]float32) float32 {
	s := n
	for i := 1; i < 8; i++ {
		v := s[i]
		j := i
		for j > 0 && s[j-1] > v {
			s[j] = s[j-1]
			j--
		}
		s[j] = v
	}
	return (s[3] + s[4]) / 2
}
