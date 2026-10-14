package render

import "math"

// Geometry is a photo's rotation, straighten and crop. Turn is a number of
// quarter turns clockwise (0-3) applied first, so the frame can become
// portrait; then Angle is in degrees
// (counter-clockwise positive, as a horizon tilted down to the right is
// fixed by a positive angle); the crop is a rectangle in [0,1] coordinates
// of the rotated canvas (same size as the image, rotated about its centre).
// The zero value means "uncropped, not rotated".
type Geometry struct {
	Turn  int     `json:"turn,omitempty"`
	Angle float64 `json:"angle"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
}

// IsZero reports whether g changes nothing.
func (g Geometry) IsZero() bool {
	return g.Turn%4 == 0 && math.Abs(g.Angle) < 1e-6 && (g.W == 0 || g.H == 0 || (g.X == 0 && g.Y == 0 && g.W == 1 && g.H == 1))
}

// InscribedScale is the largest s such that a centred s·w × s·h rectangle
// fits inside a w × h image rotated by angle degrees: the area a crop must
// stay inside to have no empty corners.
func InscribedScale(angle float64, w, h int) float64 {
	t := math.Abs(angle) * math.Pi / 180
	c, s := math.Cos(t), math.Sin(t)
	W, H := float64(w), float64(h)
	return math.Min(W/(W*c+H*s), H/(W*s+H*c))
}

// Clamp returns g with its crop moved/shrunk inside the valid (inscribed)
// area for its angle; an empty crop becomes the whole valid area.
func (g Geometry) Clamp(w, h int) Geometry {
	s := InscribedScale(g.Angle, w, h)
	lo, hi := (1-s)/2, (1+s)/2
	if g.W <= 0 || g.H <= 0 {
		g.X, g.Y, g.W, g.H = 0, 0, 1, 1
	}
	g.W, g.H = math.Min(g.W, s), math.Min(g.H, s)
	g.X = math.Max(lo, math.Min(g.X, hi-g.W))
	g.Y = math.Max(lo, math.Min(g.Y, hi-g.H))
	return g
}

// ApplyGeometry straightens and crops l (bilinear sampling); the result
// keeps l's Scale so sharpening still matches the view size.
func ApplyGeometry(l *Linear, g Geometry) *Linear {
	if g.IsZero() {
		return l
	}
	if t := ((g.Turn % 4) + 4) % 4; t != 0 {
		l = l.Rotate(90 * t)
		g.Turn = 0
		if g.IsZero() {
			return l
		}
	}
	g = g.Clamp(l.W, l.H)
	W, H := float64(l.W), float64(l.H)
	ow := max(1, int(math.Round(g.W*W)))
	oh := max(1, int(math.Round(g.H*H)))
	out := &Linear{W: ow, H: oh, Pix: make([]float32, ow*oh*3), Scale: l.Scale, Frame: int(math.Round(l.frame())), Raw: l.Raw, ISO: l.ISO}
	t := g.Angle * math.Pi / 180
	c, s := math.Cos(t), math.Sin(t)
	cx, cy := W/2, H/2
	x0, y0 := g.X*W, g.Y*H
	parallelRows(oh, func(r0, r1 int) {
		for v := r0; v < r1; v++ {
			for u := 0; u < ow; u++ {
				// Canvas point → source point (rotate back about the centre).
				px, py := x0+float64(u)+0.5-cx, y0+float64(v)+0.5-cy
				sx := c*px - s*py + cx - 0.5
				sy := s*px + c*py + cy - 0.5
				sx = math.Max(0, math.Min(W-1, sx))
				sy = math.Max(0, math.Min(H-1, sy))
				ix, iy := int(sx), int(sy)
				ix1, iy1 := min(ix+1, l.W-1), min(iy+1, l.H-1)
				fx, fy := float32(sx-float64(ix)), float32(sy-float64(iy))
				o := (v*ow + u) * 3
				for k := 0; k < 3; k++ {
					a := l.Pix[(iy*l.W+ix)*3+k]
					b := l.Pix[(iy*l.W+ix1)*3+k]
					cc := l.Pix[(iy1*l.W+ix)*3+k]
					d := l.Pix[(iy1*l.W+ix1)*3+k]
					top := a + (b-a)*fx
					bot := cc + (d-cc)*fx
					out.Pix[o+k] = top + (bot-top)*fy
				}
			}
		}
	})
	return out
}
