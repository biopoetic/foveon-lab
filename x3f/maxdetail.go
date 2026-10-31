package x3f

// Task 19: highlight recovery from the sensor's own layers, for the Max
// Detail development mode. When one layer reaches output white but another
// has not, the clipped layer's value is unknown; its ratio to the unclipped
// layers is a property of the light, not of the exposure, so it can be
// estimated around the clipped area and the missing value rebuilt. When all
// three layers have clipped there is nothing left: the pixel is neutralised
// at its brightest layer and left to the tone curve's white. Nothing here
// invents texture in that case.

import (
	"math"
)

// RecoverStats says what RecoverHighlights did, for the report and tests.
type RecoverStats struct {
	Recovered [3]int // clipped pixels whose value was rebuilt, per layer
	Lost      int    // pixels clipped in all three layers
	Partial   int    // pixels clipped in one or two layers
}

// RecoverHighlights rebuilds clipped layers from the ones still holding
// signal. gain is the as-shot white balance (CamTransform.Gain); a layer is
// clipped at clip of its own white, and the ratios are fitted in
// white-balanced units so a layer at 0.03 of white with gain 36 is treated
// at its real output level. Values above clip are left above it: the tone
// curve brings them to white without inventing detail.
func RecoverHighlights(img *CamImage, gain [3]float64, clip float64) RecoverStats {
	var st RecoverStats
	if img == nil || img.W < 2 || img.H < 2 {
		return st
	}
	if clip <= 0 || clip >= 1 {
		clip = 0.995
	}
	clipF := float32(clip)
	g := [3]float64{gain[0], gain[1], gain[2]}
	for k := range g {
		if g[k] <= 1e-6 {
			g[k] = 1
		}
	}
	w, h := img.W, img.H
	// Ratio fields, one per (target layer, source layer): q_t / q_s fitted
	// on unclipped pixels and smoothed so a clipped patch takes the colour
	// of the light around it, not a tile edge.
	cell := max(4, min(w, h)/96)
	gw, gh := (w+cell-1)/cell, (h+cell-1)/cell
	type field struct {
		num, den []float64
		global   float64
	}
	fields := [3][3]*field{}
	for t := 0; t < 3; t++ {
		for s := 0; s < 3; s++ {
			if s == t {
				continue
			}
			f := &field{num: make([]float64, gw*gh), den: make([]float64, gw*gh)}
			fields[t][s] = f
			var tn, td float64
			for y := 0; y < h; y++ {
				gy := y / cell
				for x := 0; x < w; x++ {
					p := img.Pix[(y*w+x)*3:]
					if p[t] >= clipF || p[s] >= clipF {
						continue
					}
					qt, qs := float64(p[t])*g[t], float64(p[s])*g[s]
					if qt <= 0.02 || qs <= 0.02 {
						continue
					}
					// Weight by the source so bright, well-exposed
					// samples decide the ratio, not dark noise.
					gi := gy*gw + x/cell
					f.num[gi] += qt * qs
					f.den[gi] += qs * qs
					tn += qt * qs
					td += qs * qs
				}
			}
			if td > 0 {
				f.global = tn / td
			}
			blurField(f.num, gw, gh)
			blurField(f.den, gw, gh)
		}
	}
	at := func(f *field, x, y int) float64 {
		fx := float64(x)/float64(cell) - 0.5
		fy := float64(y)/float64(cell) - 0.5
		fx = math.Max(0, math.Min(fx, float64(gw-1)))
		fy = math.Max(0, math.Min(fy, float64(gh-1)))
		x0, y0 := int(fx), int(fy)
		x1, y1 := min(x0+1, gw-1), min(y0+1, gh-1)
		tx, ty := fx-float64(x0), fy-float64(y0)
		sample := func(k int) float64 {
			// A cell with no unclipped samples is filled from its smoothed
			// neighbours; when those are empty too, use the frame's ratio.
			if f.den[k] > 1e-9 {
				return f.num[k] / f.den[k]
			}
			return f.global
		}
		a := sample(y0*gw+x0)*(1-tx) + sample(y0*gw+x1)*tx
		b := sample(y1*gw+x0)*(1-tx) + sample(y1*gw+x1)*tx
		return a*(1-ty) + b*ty
	}
	parallel(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				i := (y*w + x) * 3
				p := img.Pix[i : i+3]
				var q [3]float64
				var nClip int
				for k := 0; k < 3; k++ {
					q[k] = float64(p[k]) * g[k]
					if p[k] >= clipF {
						nClip++
					}
				}
				if nClip == 0 {
					continue
				}
				if nClip == 3 {
					// All three stopped counting: no detail left. Make the
					// pixel neutral at its brightest layer; the tone curve
					// takes it smoothly to white.
					m := math.Max(q[0], math.Max(q[1], q[2]))
					for k := 0; k < 3; k++ {
						p[k] = float32(m / g[k])
					}
					st.Lost++
					continue
				}
				st.Partial++
				for t := 0; t < 3; t++ {
					if p[t] < clipF {
						continue
					}
					var num, den float64
					for s := 0; s < 3; s++ {
						if s == t || p[s] >= clipF {
							continue
						}
						qs := q[s]
						if qs <= 0.02 {
							continue
						}
						r := at(fields[t][s], x, y)
						if r <= 0 {
							continue
						}
						num += r * qs * qs
						den += qs * qs
					}
					if den <= 1e-9 {
						continue
					}
					predQ := num / den
					// Do not invent more than two stops above white; a bad
					// ratio on a near-black neighbour must not become a
					// white hole.
					predQ = math.Min(predQ, 4)
					if predP := float32(predQ / g[t]); predP > p[t] {
						p[t] = predP
						st.Recovered[t]++
					}
				}
			}
		}
	})
	return st
}

// blurField box-blurs a grid in place, twice, so ratio fields cross tile
// boundaries smoothly.
func blurField(v []float64, w, h int) {
	if w < 3 || h < 3 {
		return
	}
	tmp := make([]float64, len(v))
	for pass := 0; pass < 2; pass++ {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var s float64
				var n int
				for k := max(0, x-2); k <= min(w-1, x+2); k++ {
					s += v[y*w+k]
					n++
				}
				tmp[y*w+x] = s / float64(n)
			}
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var s float64
				var n int
				for k := max(0, y-2); k <= min(h-1, y+2); k++ {
					s += tmp[k*w+x]
					n++
				}
				v[y*w+x] = s / float64(n)
			}
		}
	}
}
