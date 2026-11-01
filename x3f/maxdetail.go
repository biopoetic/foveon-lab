package x3f

// Task 19/26: highlight recovery from the sensor's own layers, for the Max
// Detail development mode. When one layer reaches output white but another
// has not, the clipped layer's value is unknown; its ratio to the unclipped
// layers is a property of the light, not of the exposure, so it can be
// estimated around the clipped area and the missing value rebuilt.
//
// Task 26 found the real cause of the weak recovery: the fitted field is a
// ratio q_t/q_s, but the shipped prediction divided it by the squared source
// and used the result directly as q_t, i.e. it never multiplied by the
// source's own value. A ratio of about 1 therefore predicted q_t about 1
// whatever the source was, which is below the clipped value q_t = gain, so
// most partially clipped pixels were left untouched. The prediction is now
// ratio * q_s, which is the quantity the field was fitted for.
//
// The ratio is fitted on a small cell grid (about 1/300 of the short side)
// and empty cells take the ratio of the nearest cell that had samples, so a
// large clipped cloud takes the colour of its own unclipped rim rather than
// the average of the whole frame. Both targets are then neutralised at the
// brightest layer, which keeps the recovered luminance detail and drops the
// layer ratio that clipping made meaningless; fully clipped pixels stay
// neutral because there is nothing left to rebuild.

import (
	"math"
	"sync"
	"sync/atomic"
)

// HighlightClass is what RecoverHighlights found at a pixel.
type HighlightClass uint8

const (
	HLNone      HighlightClass = iota // no layer clipped
	HLRecovered                       // one or two layers clipped, at least one rebuilt
	HLFlat                            // one or two clipped, but nothing rebuilt
	HLLost                            // all three clipped: no signal left
)

// RecoverStats says what RecoverHighlights did, for the report and tests.
type RecoverStats struct {
	Recovered [3]int // clipped pixels whose value was rebuilt, per layer
	Lost      int    // pixels clipped in all three layers
	Partial   int    // pixels clipped in one or two layers
}

// Clipping: a layer that reaches this fraction of its own white is treated as
// saturated. The fitted ratios use only samples below highlightFitMax, so a
// pixel near the clip cannot bias the slope downwards.
const (
	highlightClip    = 0.995
	highlightFitMax  = 0.95
	maxRecoverQ      = 32.0 // safety ceiling, white-balanced units
	highlightCellMin = 4
	highlightCellDiv = 300
)

// RecoverHighlights rebuilds clipped layers from the ones still holding
// signal. gain is the as-shot white balance (CamTransform.Gain), in the same
// white-balanced units as the ratios: p*gain. Values above the clip are left
// above it: the tone curve brings them to white without inventing detail.
func RecoverHighlights(img *CamImage, gain [3]float64, clip float64) RecoverStats {
	st, _ := RecoverHighlightsClass(img, gain, clip)
	return st
}

// RecoverHighlightsClass is RecoverHighlights with the per-pixel class, so a
// caller (Max Detail's display shoulder) can tell a rebuilt pixel from a
// merely clipped one.
func RecoverHighlightsClass(img *CamImage, gain [3]float64, clip float64) (RecoverStats, []HighlightClass) {
	var st RecoverStats
	var mu sync.Mutex
	merge := func(rec [3]int, lost, partial int) {
		mu.Lock()
		for k := range rec {
			st.Recovered[k] += rec[k]
		}
		st.Lost += lost
		st.Partial += partial
		mu.Unlock()
	}
	if img == nil || img.W < 2 || img.H < 2 {
		return st, nil
	}
	if clip <= 0 || clip >= 1 {
		clip = highlightClip
	}
	clipF := float32(clip)
	g := [3]float64{gain[0], gain[1], gain[2]}
	for k := range g {
		if g[k] <= 1e-6 {
			g[k] = 1
		}
	}
	w, h := img.W, img.H
	// Most frames clip nothing, and the fits below cost a second or more.
	class := make([]HighlightClass, w*h)
	var anyClip atomic.Bool
	parallel(h, func(y0, y1 int) {
		for _, v := range img.Pix[y0*w*3 : y1*w*3] {
			if v >= clipF {
				anyClip.Store(true)
				return
			}
		}
	})
	if !anyClip.Load() {
		return st, class
	}
	cell := max(highlightCellMin, min(w, h)/highlightCellDiv)
	gw, gh := (w+cell-1)/cell, (h+cell-1)/cell

	// Ratio fields, one per (target layer, source layer): q_t/q_s fitted on
	// unclipped pixels and filled outwards from the nearest fitted cell so a
	// clipped patch takes the colour of the light around it, not a tile edge.
	type field struct {
		ratio []float64
	}
	fields := [3][3]*field{}
	var fits sync.WaitGroup
	for t := 0; t < 3; t++ {
		for s := 0; s < 3; s++ {
			if s == t {
				continue
			}
			f := &field{ratio: make([]float64, gw*gh)}
			fields[t][s] = f
			fits.Add(1)
			go func(t, s int) {
				defer fits.Done()
				wsum := make([]float64, gw*gh)
				for y := 0; y < h; y++ {
					gy := y / cell
					for x := 0; x < w; x++ {
						p := img.Pix[(y*w+x)*3:]
						// Both sides below the fit band: a sample at the clip
						// would bias the slope down.
						if float64(p[t]) >= highlightFitMax || float64(p[s]) >= highlightFitMax {
							continue
						}
						qt, qs := float64(p[t])*g[t], float64(p[s])*g[s]
						if qt <= 0.02 || qs <= 0.02 {
							continue
						}
						gi := gy*gw + x/cell
						f.ratio[gi] += qt * qs
						wsum[gi] += qs * qs
					}
				}
				queue := make([]int, 0, gw*gh)
				for i := range f.ratio {
					if wsum[i] > 1e-9 {
						f.ratio[i] /= wsum[i]
						queue = append(queue, i)
					} else {
						f.ratio[i] = math.NaN()
					}
				}
				// Nearest fitted cell (8-neighbour BFS). A diffusion would mix
				// every rim of a clipped region; the nearest rim is the material
				// next to the pixel.
				for head := 0; head < len(queue); head++ {
					i := queue[head]
					x, y := i%gw, i/gw
					for _, d := range [8][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
						xx, yy := x+d[0], y+d[1]
						if xx < 0 || yy < 0 || xx >= gw || yy >= gh {
							continue
						}
						j := yy*gw + xx
						if !math.IsNaN(f.ratio[j]) {
							continue
						}
						f.ratio[j] = f.ratio[i]
						queue = append(queue, j)
					}
				}
				for i := range f.ratio {
					if math.IsNaN(f.ratio[i]) {
						f.ratio[i] = 1
					}
				}
			}(t, s)
		}
	}
	fits.Wait()
	at := func(f *field, x, y int) float64 {
		fx := float64(x)/float64(cell) - 0.5
		fy := float64(y)/float64(cell) - 0.5
		fx = math.Max(0, math.Min(fx, float64(gw-1)))
		fy = math.Max(0, math.Min(fy, float64(gh-1)))
		x0, y0 := int(fx), int(fy)
		x1, y1 := min(x0+1, gw-1), min(y0+1, gh-1)
		tx, ty := fx-float64(x0), fy-float64(y0)
		a := f.ratio[y0*gw+x0]*(1-tx) + f.ratio[y0*gw+x1]*tx
		b := f.ratio[y1*gw+x0]*(1-tx) + f.ratio[y1*gw+x1]*tx
		return a*(1-ty) + b*ty
	}

	// A coarse, read-only map of the brightest layer, for the specular test.
	// It is built before the parallel pass so the test never reads a pixel
	// another goroutine is rebuilding.
	scell := max(2, min(w, h)/400)
	sgw, sgh := (w+scell-1)/scell, (h+scell-1)/scell
	qsum := make([]float64, sgw*sgh)
	qcnt := make([]int, sgw*sgh)
	// Row by row of the coarse map, so no two workers share a cell.
	parallel(sgh, func(g0, g1 int) {
		for y := g0 * scell; y < min(h, g1*scell); y++ {
			gy := y / scell
			for x := 0; x < w; x++ {
				p := img.Pix[(y*w+x)*3:]
				v := math.Max(float64(p[0])*g[0], math.Max(float64(p[1])*g[1], float64(p[2])*g[2]))
				gi := gy*sgw + x/scell
				qsum[gi] += v
				qcnt[gi]++
			}
		}
	})
	for i := range qsum {
		if qcnt[i] > 0 {
			qsum[i] /= float64(qcnt[i])
		}
	}

	parallel(h, func(y0, y1 int) {
		var rec [3]int
		var lost, partial int
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
				// A clipped, isolated, near-neutral bright point is a
				// specular. Rebuilding it can only make it a grey disc and a
				// partially clipped one would be shown below white; push it
				// to a little above white instead, so the point light stays a
				// point light. A coloured LED is not near-neutral and is left
				// to the normal path.
				qmax, qmin := math.Max(q[0], math.Max(q[1], q[2])), math.Min(q[0], math.Min(q[1], q[2]))
				if qmax > 0.5 {
					gx, gy := x/scell, y/scell
					var sum float64
					var n int
					for yy := max(0, gy-1); yy <= min(sgh-1, gy+1); yy++ {
						for xx := max(0, gx-1); xx <= min(sgw-1, gx+1); xx++ {
							sum += qsum[yy*sgw+xx]
							n++
						}
					}
					if n > 0 && qmax > 2.2*(sum/float64(n)) && qmax-qmin < 0.2*qmax {
						target := math.Max(qmax, 1.2)
						for k := 0; k < 3; k++ {
							p[k] = float32(target / g[k])
						}
						if nClip == 3 {
							lost++
							class[y*w+x] = HLLost
						} else {
							partial++
							class[y*w+x] = HLFlat
						}
						continue
					}
				}
				if nClip == 3 {
					// All three stopped counting: no detail left. Make the
					// pixel neutral at its brightest layer; the tone curve
					// takes it smoothly to white.
					m := math.Max(q[0], math.Max(q[1], q[2]))
					for k := 0; k < 3; k++ {
						p[k] = float32(m / g[k])
					}
					lost++
					class[y*w+x] = HLLost
					continue
				}
				partial++
				recovered := false
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
						// The field is q_t/q_s; the missing value is r*q_s.
						wgt := qs * qs
						num += r * qs * wgt
						den += wgt
					}
					if den <= 1e-9 {
						continue
					}
					predQ := math.Min(num/den, maxRecoverQ)
					if predP := float32(predQ / g[t]); predP > p[t] {
						p[t] = predP
						rec[t]++
						recovered = true
					}
				}
				// The caller (Max Detail) runs ClipHighlights next, which
				// neutralises these pixels at the brightest layer: that keeps
				// the recovered luminance detail and drops the layer ratio
				// that clipping made meaningless, so no magenta/cyan rim can
				// appear. This function leaves the per-layer values intact so
				// the synthetic ramp can measure them.
				if recovered {
					class[y*w+x] = HLRecovered
				} else {
					class[y*w+x] = HLFlat
				}
			}
		}
		merge(rec, lost, partial)
	})
	return st, class
}
