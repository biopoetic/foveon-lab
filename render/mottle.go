// Large-radius chroma mottle removal for the Quattro bodies. Their per-layer
// denoiser removes the fine chroma noise but leaves 8-32 px blotches: SPP's
// 8-32 px a*/b* band is 1.5-2.5x lower at ISO 3200-6400. The existing 32 px
// guided colour filter works at the pixel scale; the blotches are coherent
// over tens of pixels, so they need a much wider window. A wide chroma blur
// on its own pulls colour in from saturated neighbours (measured: chroma
// noise rose 0.8 -> 2.2 at dp2 Quattro 800), so this pass is edge-aware: the
// guide is the encoded luminance, the filter is the same He et al. one the
// shadow pass uses, and it only acts on low-chroma pixels (a saturated
// object is left alone). Luminance is carried through unchanged.
package render

import (
	"math"
)

// MottleChroma smooths low-chroma mottle with a guided filter of radius
// sigma (px at this size). lowLo and lowHi bracket the chroma magnitude
// (max-min of the linear channels) over which the effect fades in: fully at
// or below lowLo, none at or above lowHi.
func MottleChroma(l *Linear, sigma, lowLo, lowHi float64) {
	if l == nil || l.W < 8 || l.H < 8 || sigma < 1 || lowHi <= lowLo {
		return
	}
	w, h, n := l.W, l.H, l.W*l.H
	Y := make([]float32, n)
	lo := float32(lowLo)
	hi := float32(lowHi)
	amp := make([]float32, n)
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				i := (y*w + x) * 3
				y0 := 0.2126*l.Pix[i] + 0.7152*l.Pix[i+1] + 0.0722*l.Pix[i+2]
				if y0 < 0 {
					y0 = 0
				}
				Y[y*w+x] = y0
				mx := max(l.Pix[i], max(l.Pix[i+1], l.Pix[i+2]))
				mn := min(l.Pix[i], min(l.Pix[i+1], l.Pix[i+2]))
				c := mx - mn
				t := (hi - c) / (hi - lo)
				amp[y*w+x] = max(0, min(1, t))
			}
		}
	})
	// Normalised convolution by the low-chroma weight: only low-chroma
	// pixels contribute to the average, so a saturated neighbour cannot
	// bleed its colour in (the plain blur did: chroma noise 0.8 -> 2.2).
	sw := GaussBlur(amp, w, h, sigma)
	// The chroma planes are channel - luminance; smoothing them and adding
	// them back leaves luminance exactly where it was, so the pass cannot
	// change brightness or detail.
	for k := 0; k < 3; k++ {
		c := make([]float32, n)
		wc := make([]float32, n)
		parallelRows(n, func(i0, i1 int) {
			for i := i0; i < i1; i++ {
				c[i] = l.Pix[i*3+k] - Y[i]
				wc[i] = amp[i] * c[i]
			}
		})
		bw := GaussBlur(wc, w, h, sigma)
		parallelRows(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < w; x++ {
					i := y*w + x
					t := amp[i]
					if t <= 0 || sw[i] < 1e-6 {
						continue
					}
					v := Y[i] + c[i]*(1-t) + (bw[i]/sw[i])*t
					l.Pix[i*3+k] = max(0, v)
				}
			}
		})
	}
}

// MottleSigma is the filter radius at full resolution: 64 px covers the
// 8-32 px blotches (a Gaussian of sigma 64 has most of its mass over 3
// sigma), scaled to the render size so a preview smooths the same scene
// area.
func MottleSigma(fullW, w int) float64 {
	if fullW <= 0 || w <= 0 {
		return 64
	}
	return math.Max(2, 64*float64(w)/float64(fullW))
}
