package calib

import (
	"math"

	"github.com/biopoetic/foveon-lab/render"
)

// fitFillCoef fits the two-scale Fill Light model on one export pair: per
// brightness bin, least squares of the brightness shift on [1, surround at
// scale a − pixel, surround at scale b − pixel], with the renderer's own
// Surround so the coefficients mean the same thing there.
func fitFillCoef(ref, out *render.Linear) [render.FillBins][3]float64 {
	sa := render.NewSurround(ref, render.SurroundFracs[0])
	sb := render.NewSurround(ref, render.SurroundFracs[1])
	var ata [render.FillBins][3][3]float64
	var atb [render.FillBins][3]float64
	for y := 0; y < ref.H; y++ {
		for x := 0; x < ref.W; x++ {
			i := (y*ref.W + x) * 3
			v := float64(render.ToSRGB(luma(ref.Pix, i)))
			o := float64(render.ToSRGB(luma(out.Pix, i)))
			b := min(render.FillBins-1, int(v*render.FillBins))
			f := [3]float64{1, float64(sa.At(x, y)) - v, float64(sb.At(x, y)) - v}
			for r := 0; r < 3; r++ {
				atb[b][r] += f[r] * (o - v)
				for c := 0; c < 3; c++ {
					ata[b][r][c] += f[r] * f[c]
				}
			}
		}
	}
	var coef [render.FillBins][3]float64
	var last [3]float64
	for b := 0; b < render.FillBins; b++ {
		if ata[b][0][0] < 50 { // too few pixels: carry the neighbour's shape
			coef[b] = last
			continue
		}
		a := ata[b]
		for k := 0; k < 3; k++ {
			a[k][k] += 1e-4 * ata[b][0][0] // a little ridge keeps rare bins sane
		}
		if inv, ok := inverse3(a); ok {
			for r := 0; r < 3; r++ {
				for c := 0; c < 3; c++ {
					coef[b][r] += inv[r][c] * atb[b][c]
				}
			}
		}
		last = coef[b]
	}
	// Bins below the first measured one take its values.
	for b := render.FillBins - 1; b > 0; b-- {
		if coef[b-1] == ([3]float64{}) && coef[b] != ([3]float64{}) {
			coef[b-1] = coef[b]
		}
	}
	for b := range coef {
		for k := range coef[b] {
			if math.IsNaN(coef[b][k]) {
				coef[b][k] = 0
			}
		}
	}
	return coef
}
