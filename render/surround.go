package render

import "math"

// Surround is "how bright is the area around this pixel" at one scale:
// an edge-aware local mean (a guided filter) of perceptual brightness,
// read back with bilinear interpolation. X3F Fill Light is modelled from
// two such scales (SurroundFracs); the calibration measures it with the
// very same function.
//
// SPP's own Fill Light is Retinex-style (CAMF names FIR_* local means with
// an edge reference). A guided filter beat the plain block average held
// out on 25 photos (fill-research, Oct 2026): mean brightness error at
// Fill Light +0.5/+1, SD1 7.80 -> 6.82, SD15 8.34 -> 7.58; a plain Gaussian
// got 7.12 / 7.80. CAMF's FIR proportions (0.0011/0.0019/0.019 of the
// width) did not help (7.79 / 8.35).
type Surround struct {
	gw, gh int
	sx, sy float64 // grid cells per image pixel
	v      []float32
}

// SurroundFracs are the two scales (Gaussian sigma as a fraction of the
// image width) the Fill Light model uses.
var SurroundFracs = [2]float64{1.0 / 80, 1.0 / 20}

// SurroundEps is the guided filter's edge threshold (brightness variance):
// detail above it is kept, flat areas are averaged.
const SurroundEps = 0.08

// surroundMaxW is the grid width the surround is computed at (the
// calibration's analysis width), so previews, exports and calibration see
// the same filter; larger images are averaged down first.
const surroundMaxW = 1200

// NewSurround measures l at scale frac × width.
func NewSurround(l *Linear, frac float64) *Surround {
	gw := min(l.W, surroundMaxW)
	gh := max(1, int(math.Round(float64(l.H)*float64(gw)/float64(l.W))))
	s := &Surround{gw: gw, gh: gh, sx: float64(gw) / float64(l.W), sy: float64(gh) / float64(l.H)}
	v := make([]float32, gw*gh)
	n := make([]float32, gw*gh)
	for y := 0; y < l.H; y++ {
		gy := min(gh-1, int(float64(y)*s.sy))
		for x := 0; x < l.W; x++ {
			i := (y*l.W + x) * 3
			k := gy*gw + min(gw-1, int(float64(x)*s.sx))
			v[k] += enc(0.2126*l.Pix[i] + 0.7152*l.Pix[i+1] + 0.0722*l.Pix[i+2])
			n[k]++
		}
	}
	for i := range v {
		if n[i] > 0 {
			v[i] /= n[i]
		}
	}
	s.v = GuidedFilter(v, gw, gh, frac*float64(gw), SurroundEps)
	return s
}

// At is the surround brightness at pixel (x, y).
func (s *Surround) At(x, y int) float32 {
	fx := float32(math.Max(0, math.Min(float64(s.gw-1), (float64(x)+0.5)*s.sx-0.5)))
	fy := float32(math.Max(0, math.Min(float64(s.gh-1), (float64(y)+0.5)*s.sy-0.5)))
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, s.gw-1), min(y0+1, s.gh-1)
	tx, ty := fx-float32(x0), fy-float32(y0)
	a := s.v[y0*s.gw+x0] + (s.v[y0*s.gw+x1]-s.v[y0*s.gw+x0])*tx
	b := s.v[y1*s.gw+x0] + (s.v[y1*s.gw+x1]-s.v[y1*s.gw+x0])*tx
	return a + (b-a)*ty
}

// GuidedFilter is an edge-aware local mean (He et al.) of v (w×h): within
// a window of Gaussian sigma (pixels) the output follows v where it varies
// more than sqrt(eps) and the local mean where it is flat.
func GuidedFilter(v []float32, w, h int, sigma, eps float64) []float32 {
	vv := make([]float32, len(v))
	for i, x := range v {
		vv[i] = x * x
	}
	m := GaussBlur(v, w, h, sigma)
	m2 := GaussBlur(vv, w, h, sigma)
	a, b := vv, make([]float32, len(v)) // vv is no longer needed
	for i := range v {
		vr := max(0, m2[i]-m[i]*m[i])
		a[i] = vr / (vr + float32(eps))
		b[i] = m[i] - a[i]*m[i]
	}
	ma := GaussBlur(a, w, h, sigma)
	mb := GaussBlur(b, w, h, sigma)
	for i := range v {
		ma[i] = ma[i]*v[i] + mb[i]
	}
	return ma
}

// GaussBlur approximates a Gaussian of the given sigma (pixels) with three
// box blurs per axis.
func GaussBlur(src []float32, w, h int, sigma float64) []float32 {
	r := int(math.Max(1, math.Round(sigma*math.Sqrt(12.0/3+1)/2-0.5)))
	a := append([]float32(nil), src...)
	b := make([]float32, len(src))
	for pass := 0; pass < 3; pass++ {
		boxH(a, b, w, h, r)
		boxV(b, a, w, h, r)
	}
	return a
}

func boxH(src, dst []float32, w, h, r int) {
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			row := src[y*w : (y+1)*w]
			var sum float32
			cnt := 0
			for x := 0; x <= min(r, w-1); x++ {
				sum += row[x]
				cnt++
			}
			for x := 0; x < w; x++ {
				dst[y*w+x] = sum / float32(cnt)
				if x+r+1 < w {
					sum += row[x+r+1]
					cnt++
				}
				if x-r >= 0 {
					sum -= row[x-r]
					cnt--
				}
			}
		}
	})
}

func boxV(src, dst []float32, w, h, r int) {
	parallelRows(w, func(x0, x1 int) {
		for x := x0; x < x1; x++ {
			var sum float32
			cnt := 0
			for y := 0; y <= min(r, h-1); y++ {
				sum += src[y*w+x]
				cnt++
			}
			for y := 0; y < h; y++ {
				dst[y*w+x] = sum / float32(cnt)
				if y+r+1 < h {
					sum += src[(y+r+1)*w+x]
					cnt++
				}
				if y-r >= 0 {
					sum -= src[(y-r)*w+x]
					cnt--
				}
			}
		}
	})
}

// FillBins is the number of brightness bins of the Fill Light model.
const FillBins = 16

// fillCoef evaluates a multi-scale Fill Light step: the brightness shift of
// a pixel of brightness x whose surroundings are da and db brighter than
// it, interpolated between bin centres.
func fillCoef(c *[FillBins][3]float64, x, da, db float64) float64 {
	f := math.Max(0, math.Min(FillBins-1, x*FillBins-0.5))
	b0 := min(int(f), FillBins-2)
	t := f - float64(b0)
	at := func(b int) float64 { return c[b][0] + c[b][1]*da + c[b][2]*db }
	return at(b0)*(1-t) + at(b0+1)*t
}

// FillShift evaluates a two-scale Fill Light step (for calibration tools).
func FillShift(c *[FillBins][3]float64, x, da, db float64) float64 { return fillCoef(c, x, da, db) }
