package render

import "math"

// Surround is an image's perceptual brightness averaged over square blocks
// of a given fraction of its width, read back with bilinear interpolation
// between block centres: "how bright is the area around this pixel" at one
// scale. X3F Fill Light is modelled from two such scales (SurroundFracs);
// the calibration measures it with the very same function.
type Surround struct {
	cell   int
	gw, gh int
	v      []float32
}

// SurroundFracs are the two scales (fractions of the image width) the Fill
// Light model uses; chosen by held-out error on SPP exports of four photos.
var SurroundFracs = [2]float64{1.0 / 80, 1.0 / 20}

// NewSurround measures l at block size frac × width.
func NewSurround(l *Linear, frac float64) *Surround {
	cell := max(1, int(float64(l.W)*frac))
	s := &Surround{cell: cell, gw: (l.W + cell - 1) / cell, gh: (l.H + cell - 1) / cell}
	s.v = make([]float32, s.gw*s.gh)
	n := make([]float32, s.gw*s.gh)
	for y := 0; y < l.H; y++ {
		for x := 0; x < l.W; x++ {
			i := (y*l.W + x) * 3
			k := (y/cell)*s.gw + x/cell
			s.v[k] += enc(0.2126*l.Pix[i] + 0.7152*l.Pix[i+1] + 0.0722*l.Pix[i+2])
			n[k]++
		}
	}
	for i := range s.v {
		s.v[i] /= n[i]
	}
	return s
}

// At is the surround brightness at pixel (x, y).
func (s *Surround) At(x, y int) float32 {
	fx := float32(math.Max(0, math.Min(float64(s.gw-1), (float64(x)+0.5)/float64(s.cell)-0.5)))
	fy := float32(math.Max(0, math.Min(float64(s.gh-1), (float64(y)+0.5)/float64(s.cell)-0.5)))
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, s.gw-1), min(y0+1, s.gh-1)
	tx, ty := fx-float32(x0), fy-float32(y0)
	a := s.v[y0*s.gw+x0] + (s.v[y0*s.gw+x1]-s.v[y0*s.gw+x0])*tx
	b := s.v[y1*s.gw+x0] + (s.v[y1*s.gw+x1]-s.v[y1*s.gw+x0])*tx
	return a + (b-a)*ty
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
