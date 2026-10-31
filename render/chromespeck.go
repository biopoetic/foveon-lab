package render

// Task 19: isolated chroma specks at high ISO. On the SD14 frame that
// started it (rytterfalk_SDIM3378, ISO 1600) a clipped-layer ratio that
// clipping made meaningless, amplified by a white balance gain of 36,
// left single pixels far off their neighbourhood's colour: magenta and
// purple sparkles in the water. The q-space clip removes most; this pass
// removes what survives: a pixel whose colour is far outside a 5x5
// neighbourhood's median, or a bright magenta point on a much darker
// neighbourhood (a specular the matrix tinted). Real coloured areas keep
// their colour.

import (
	"sort"
)

// ChromaDespeckle replaces isolated chroma outliers in place and returns
// how many pixels it changed. It is meant for high-ISO frames.
func ChromaDespeckle(l *Linear, iso float64) int {
	if l == nil || l.W < 5 || l.H < 5 || iso < 800 {
		return 0
	}
	// The higher the ISO, the lower the chroma that counts as a speck.
	lim := 0.10
	switch {
	case iso >= 6400:
		lim = 0.06
	case iso >= 3200:
		lim = 0.075
	case iso >= 1600:
		lim = 0.09
	}
	src := append([]float32(nil), l.Pix...)
	n := 0
	parallelRows(l.H, func(y0, y1 int) {
		var cr, cg, cb, yy [24]float32
		for y := max(2, y0); y < min(l.H-2, y1); y++ {
			for x := 2; x < l.W-2; x++ {
				i := (y*l.W + x) * 3
				Y := 0.2126*src[i] + 0.7152*src[i+1] + 0.0722*src[i+2]
				if Y < 0.015 || Y > 1.5 {
					continue
				}
				pr, pg, pb := src[i]-Y, src[i+1]-Y, src[i+2]-Y
				pm := max(absf(pr), max(absf(pg), absf(pb)))
				// Most pixels are far below the speck threshold; skip the
				// median until one is colourful enough to be a candidate.
				if pm < float32(lim)*0.8 {
					continue
				}
				k := 0
				var msum float64
				for dy := -2; dy <= 2; dy++ {
					for dx := -2; dx <= 2; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						j := i + (dy*l.W+dx)*3
						ny := 0.2126*src[j] + 0.7152*src[j+1] + 0.0722*src[j+2]
						msum += float64(ny)
						cr[k], cg[k], cb[k], yy[k] = src[j]-ny, src[j+1]-ny, src[j+2]-ny, ny
						k++
					}
				}
				sort.Slice(cr[:], func(a, b int) bool { return cr[a] < cr[b] })
				sort.Slice(cg[:], func(a, b int) bool { return cg[a] < cg[b] })
				sort.Slice(cb[:], func(a, b int) bool { return cb[a] < cb[b] })
				sort.Slice(yy[:], func(a, b int) bool { return yy[a] < yy[b] })
				mr, mg, mb := (cr[11]+cr[12])/2, (cg[11]+cg[12])/2, (cb[11]+cb[12])/2
				my := (yy[11] + yy[12]) / 2
				mm := max(absf(mr), max(absf(mg), absf(mb)))
				d := max(absf(pr-mr), max(absf(pg-mg), absf(pb-mb)))
				outlier := d > float32(lim) && pm > mm+float32(lim)*0.6
				// A bright magenta point on a dark neighbourhood is a
				// specular the layer ratio tinted; make it the white point
				// it is, not its wrong colour.
				magenta := pr+pb-2*pg > float32(lim)*1.2 && Y > my+float32(lim)*1.5 && Y > my*1.15
				if outlier || magenta {
					ry, gy, by := float32(my)+mr, float32(my)+mg, float32(my)+mb
					if magenta {
						ry, gy, by = Y, Y, Y
					}
					l.Pix[i], l.Pix[i+1], l.Pix[i+2] = float32(clamp01f(float64(ry))), float32(clamp01f(float64(gy))), float32(clamp01f(float64(by)))
					n++
				}
			}
		}
	})
	return n
}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
