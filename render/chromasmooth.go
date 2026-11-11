package render

import (
	"math"
)

// Colour blotches (preset.Params.ChromaSmooth): a user control over the
// low-frequency colour noise a Foveon sensor shows at high ISO, the green and
// magenta patches 30-100 px across at full size. The raw development already
// removes some of it (x3f/chromabilat.go), at a strength fixed when the raw
// is decoded; that takes 10-25 s, so it cannot follow a slider. This stage
// runs at render time on the developed image and is off at 0.
//
// What it does: the chroma (each channel minus the luminance, relative to
// the luminance) is averaged on a coarse grid whose cell is the size of the
// smallest blotch, and that coarse chroma is smoothed with a bilateral
// filter: a neighbour counts by its distance, by how close its luminance is
// and by how close its chroma is. A blotch is a small chroma difference on a
// flat tone, so it is averaged away; an object's colour edge is a large
// chroma difference, so it is kept. Only the change of the coarse chroma is
// added back to the picture, so fine colour detail and the luminance (the
// sum of the three chroma parts is zero by construction) are untouched.
//
// It is not what Sigma Photo Pro does and it is not measured against SPP:
// the default stays 0, where the render is byte-identical.

const (
	chromaCellFull = 12.0 // coarse cell at full sensor size, px
	chromaRadius   = 6    // bilateral radius, coarse cells
	chromaSigmaS   = 3.0  // spatial sigma, coarse cells
	chromaSigmaC   = 0.10 // range sigma on relative chroma
	chromaSigmaY   = 0.10 // range sigma on encoded luminance
	chromaEps      = 0.02 // keeps the relative chroma finite near black
)

// chromaDelta is the per-pixel change of the linear image at full strength
// (3 per pixel), computed once per Base.
func (b *Base) chromaDelta() []float32 {
	b.chromaOnce.Do(func() {
		b.chromaD = chromaSmoothDelta(b.Pix, b.W, b.H, b.scale())
	})
	return b.chromaD
}

// chromaSmoothDelta returns what to add to pix (linear RGB, 3 per pixel) to
// smooth its coarse chroma. scale is the image's size relative to the full
// sensor readout.
func chromaSmoothDelta(pix []float32, w, h int, scale float64) []float32 {
	if scale <= 0 {
		scale = 1
	}
	cell := max(1, int(math.Round(chromaCellFull*scale)))
	cw, ch := (w+cell-1)/cell, (h+cell-1)/cell
	// Coarse relative chroma and encoded luminance.
	cy := make([]float32, cw*ch)
	cc := make([]float32, cw*ch*3)
	parallelRows(ch, func(y0, y1 int) {
		for gy := y0; gy < y1; gy++ {
			for gx := 0; gx < cw; gx++ {
				var sy, n float64
				var sc [3]float64
				for y := gy * cell; y < min(h, (gy+1)*cell); y++ {
					for x := gx * cell; x < min(w, (gx+1)*cell); x++ {
						i := (y*w + x) * 3
						r, g, bl := float64(pix[i]), float64(pix[i+1]), float64(pix[i+2])
						l := 0.2126*r + 0.7152*g + 0.0722*bl
						d := math.Max(l, 0) + chromaEps
						sc[0] += (r - l) / d
						sc[1] += (g - l) / d
						sc[2] += (bl - l) / d
						sy += l
						n++
					}
				}
				j := gy*cw + gx
				cy[j] = enc(float32(sy / n))
				cc[j*3], cc[j*3+1], cc[j*3+2] = float32(sc[0]/n), float32(sc[1]/n), float32(sc[2]/n)
			}
		}
	})
	// Bilateral smoothing of the coarse chroma; keep only its change.
	var ws [2*chromaRadius + 1]float64
	for d := -chromaRadius; d <= chromaRadius; d++ {
		ws[d+chromaRadius] = math.Exp(-float64(d*d) / (2 * chromaSigmaS * chromaSigmaS))
	}
	dc := make([]float32, cw*ch*3)
	kc, ky := 1/(2*chromaSigmaC*chromaSigmaC), 1/(2*chromaSigmaY*chromaSigmaY)
	parallelRows(ch, func(y0, y1 int) {
		for gy := y0; gy < y1; gy++ {
			for gx := 0; gx < cw; gx++ {
				j := gy*cw + gx
				c0, c1, c2, l0 := float64(cc[j*3]), float64(cc[j*3+1]), float64(cc[j*3+2]), float64(cy[j])
				var sw float64
				var s [3]float64
				for dy := -chromaRadius; dy <= chromaRadius; dy++ {
					yy := gy + dy
					if yy < 0 || yy >= ch {
						continue
					}
					for dx := -chromaRadius; dx <= chromaRadius; dx++ {
						xx := gx + dx
						if xx < 0 || xx >= cw {
							continue
						}
						k := yy*cw + xx
						a, bb, c := float64(cc[k*3])-c0, float64(cc[k*3+1])-c1, float64(cc[k*3+2])-c2
						dl := float64(cy[k]) - l0
						wgt := ws[dy+chromaRadius] * ws[dx+chromaRadius] * math.Exp(-(a*a+bb*bb+c*c)*kc-dl*dl*ky)
						sw += wgt
						s[0] += wgt * float64(cc[k*3])
						s[1] += wgt * float64(cc[k*3+1])
						s[2] += wgt * float64(cc[k*3+2])
					}
				}
				dc[j*3] = float32(s[0]/sw - c0)
				dc[j*3+1] = float32(s[1]/sw - c1)
				dc[j*3+2] = float32(s[2]/sw - c2)
			}
		}
	})
	// Back to the picture: the coarse change, interpolated between cell
	// centres, times the pixel's own luminance.
	out := make([]float32, len(pix))
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			fy := (float64(y)+0.5)/float64(cell) - 0.5
			gy := int(math.Floor(fy))
			ty := float32(fy - float64(gy))
			ya, yb := min(ch-1, max(0, gy)), min(ch-1, max(0, gy+1))
			for x := 0; x < w; x++ {
				fx := (float64(x)+0.5)/float64(cell) - 0.5
				gx := int(math.Floor(fx))
				tx := float32(fx - float64(gx))
				xa, xb := min(cw-1, max(0, gx)), min(cw-1, max(0, gx+1))
				i := (y*w + x) * 3
				l := 0.2126*pix[i] + 0.7152*pix[i+1] + 0.0722*pix[i+2]
				d := max(l, 0) + chromaEps
				for k := 0; k < 3; k++ {
					v := (dc[(ya*cw+xa)*3+k]*(1-tx)+dc[(ya*cw+xb)*3+k]*tx)*(1-ty) +
						(dc[(yb*cw+xa)*3+k]*(1-tx)+dc[(yb*cw+xb)*3+k]*tx)*ty
					out[i+k] = v * d
				}
			}
		}
	})
	return out
}

// chromaStrength is the slider held to its range.
func chromaStrength(v float64) float32 {
	return float32(math.Max(0, math.Min(1, v)))
}
