package render

// The integrated Status A read: forward density through the standard's
// spectral products, and its Newton inverse. Both run at build time only
// (newColorFilm); the 3-D table and the shader are unchanged.

import "math"

// statusAIntegrals is the forward integral at dye amounts c: the unnormalised
// Status A response of each channel, and the same integral weighted by each
// dye's spectral density (the Jacobian's numerator).
func (f *colorFilm) statusAIntegrals(c [3]float64) (qraw [3]float64, jr [9]float64) {
	g := statusAGrid()
	s := g.s
	for i := range f.eps2 {
		e := &f.eps2[i]
		// T = 10^-(c·ε) as exp2(-c·ε·log2 10); eps2 already carries log2 10.
		t := math.Exp2(-(c[0]*e[0] + c[1]*e[1] + c[2]*e[2]))
		if t == 0 {
			continue
		}
		for k := 0; k < 3; k++ {
			w := t * s[k][i]
			qraw[k] += w
			jr[k*3+0] += w * e[0]
			jr[k*3+1] += w * e[1]
			jr[k*3+2] += w * e[2]
		}
	}
	return qraw, jr
}

// statusADensity is D_k(c) for the three Status A channels.
func (f *colorFilm) statusADensity(c [3]float64) [3]float64 {
	qraw, _ := f.statusAIntegrals(c)
	den := statusAGrid().denom
	var D [3]float64
	for k := 0; k < 3; k++ {
		q := qraw[k] / den[k]
		if q < 1e-300 {
			q = 1e-300
		}
		D[k] = -math.Log10(q)
	}
	return D
}

// invertStatusA solves D = D(c) for c ≥ 0 by Newton, started from the old
// narrow-band solution at the Status A peaks. It returns the best iterate,
// the number of iterations, and the largest remaining density error (D).
//
// Far outside the film's own gamut (a corner of the LUT cube, say) the
// non-negativity constraint can leave no exact solution; the best iterate is
// then kept, and newColorFilm counts it. The film's real densities all solve.
func (f *colorFilm) invertStatusA(D [3]float64) (c [3]float64, iters int, res float64) {
	den := statusAGrid().denom
	for j := 0; j < 3; j++ {
		var v float64
		for k := 0; k < 3; k++ {
			v += f.aInv[j*3+k] * D[k]
		}
		c[j] = math.Max(0, v)
	}
	best, bestRes := c, math.Inf(1)
	for it := 0; it < 24; it++ {
		qraw, jr := f.statusAIntegrals(c)
		var r [3]float64
		maxr := 0.0
		for k := 0; k < 3; k++ {
			q := qraw[k] / den[k]
			if q < 1e-300 {
				q = 1e-300
			}
			r[k] = D[k] + math.Log10(q)
			if a := math.Abs(r[k]); a > maxr {
				maxr = a
			}
		}
		if maxr < bestRes {
			best, bestRes = c, maxr
		}
		if maxr < 1e-8 {
			return best, it + 1, bestRes
		}
		var J [9]float64
		for k := 0; k < 3; k++ {
			if qraw[k] <= 0 {
				return best, it + 1, bestRes
			}
			for j := 0; j < 3; j++ {
				J[k*3+j] = jr[k*3+j] / (qraw[k] * log2of10)
			}
		}
		Ji, ok := invert3x3(J)
		if !ok {
			return best, it + 1, bestRes
		}
		var delta [3]float64
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				delta[j] += Ji[j*3+k] * r[k]
			}
		}
		// Keep c ≥ 0: shrink the step to the largest one the constraint allows,
		// then backtrack while the residual does not fall.
		alpha := 1.0
		for j := 0; j < 3; j++ {
			if delta[j] < 0 {
				if a := -c[j] / delta[j]; a < alpha {
					alpha = a
				}
			}
		}
		if alpha <= 0 {
			return best, it + 1, bestRes
		}
		moved := false
		for try := 0; try < 12; try++ {
			var trial [3]float64
			for j := 0; j < 3; j++ {
				trial[j] = math.Max(0, c[j]+alpha*delta[j])
			}
			td := f.statusADensity(trial)
			tr := 0.0
			for k := 0; k < 3; k++ {
				if a := math.Abs(D[k] - td[k]); a > tr {
					tr = a
				}
			}
			if tr < maxr {
				c, moved = trial, true
				break
			}
			alpha *= 0.5
		}
		if !moved {
			return best, it + 1, bestRes
		}
	}
	return best, 24, bestRes
}

// amountsStatusA is the inverse used by the LUT and the wedge.
func (f *colorFilm) amountsStatusA(D [3]float64) [3]float64 {
	c, _, _ := f.invertStatusA(D)
	return c
}
