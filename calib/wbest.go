package calib

// Illuminant study (task 23, part 2): can a white balance estimated from the
// image itself take a useful share of what the per-photo oracle balance
// offers? Everything here is judged the same way the raw base is: each photo
// is predicted by a fit on the other photos, and every estimator sees only
// our own developed image (never SPP). WBStudy reports each estimator's
// error, so a null result is as visible as a gain.

import (
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/biopoetic/foveon-lab/render"
)

// wbGains are per-channel log gains; applying exp(g) to a developed image.
func applyGains(l *render.Linear, g [3]float64) *render.Linear {
	o := *l
	o.Pix = append([]float32(nil), l.Pix...)
	for i := 0; i < len(o.Pix); i++ {
		o.Pix[i] = float32(math.Min(1, float64(o.Pix[i])*math.Exp(g[i%3])))
	}
	return &o
}

// channelMeans is the masked per-channel mean of a linear image.
func channelMeans(l *render.Linear) ([3]float64, int) {
	var s [3]float64
	var n int
	for i := 0; i < len(l.Pix); i += 3 {
		r, g, b := l.Pix[i], l.Pix[i+1], l.Pix[i+2]
		if min(r, g, b) < 0.002 || max(r, g, b) > 0.98 {
			continue
		}
		s[0] += float64(r)
		s[1] += float64(g)
		s[2] += float64(b)
		n++
	}
	if n > 0 {
		for k := range s {
			s[k] /= float64(n)
		}
	}
	return s, n
}

// greyGains turns channel means into balanced log gains (geometric mean 1).
func greyGains(m [3]float64) [3]float64 {
	var g [3]float64
	if m[0] <= 0 || m[1] <= 0 || m[2] <= 0 {
		return g
	}
	l := [3]float64{math.Log(m[0]), math.Log(m[1]), math.Log(m[2])}
	mean := (l[0] + l[1] + l[2]) / 3
	for k := range g {
		g[k] = mean - l[k]
	}
	return g
}

// percentile returns the q quantile (0..1) of v (sorted in place).
func percentile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	i := int(q * float64(len(v)-1))
	return v[i]
}

// wbFeatures are the image statistics the estimators use. All are ratios to
// green, so a global exposure change does not enter.
func wbFeatures(l *render.Linear, jpeg *render.Linear) []float64 {
	m, _ := channelMeans(l)
	lg := func(a, b float64) float64 { return math.Log(math.Max(a, 1e-6) / math.Max(b, 1e-6)) }
	// p99.5 per channel, and the same on the brightest 0.5% of pixels
	var chanv [3][]float64
	var n, clipped int
	// every 4th pixel is plenty for a 99.5th percentile
	for i := 0; i < len(l.Pix); i += 12 {
		r, g, b := float64(l.Pix[i]), float64(l.Pix[i+1]), float64(l.Pix[i+2])
		chanv[0] = append(chanv[0], r)
		chanv[1] = append(chanv[1], g)
		chanv[2] = append(chanv[2], b)
		if max(r, g, b) > 0.98 {
			clipped++
		}
		n++
	}
	var p99 [3]float64
	for k := 0; k < 3; k++ {
		p99[k] = percentile(chanv[k], 0.995)
	}
	// near-neutral, bright pixels: the most likely to be a grey/white object
	var s [3]float64
	var sn int
	for i := 0; i < len(l.Pix); i += 3 {
		r, g, b := float64(l.Pix[i]), float64(l.Pix[i+1]), float64(l.Pix[i+2])
		hi, lo := max(r, g, b), min(r, g, b)
		y := 0.2126*r + 0.7152*g + 0.0722*b
		if hi > 0 && lo/hi > 0.80 && y > 0.10 && hi < 0.95 {
			s[0] += r
			s[1] += g
			s[2] += b
			sn++
		}
	}
	var neutral [3]float64
	if sn > 100 {
		for k := range s {
			neutral[k] = s[k] / float64(sn)
		}
	} else {
		neutral = m
	}
	f := []float64{
		1,
		lg(m[0], m[1]), lg(m[2], m[1]),
		lg(p99[0], p99[1]), lg(p99[2], p99[1]),
		lg(neutral[0], neutral[1]), lg(neutral[2], neutral[1]),
		math.Log(math.Max(0.2126*m[0]+0.7152*m[1]+0.0722*m[2], 1e-5)),
		float64(clipped) / float64(max(1, n)),
	}
	if jpeg != nil {
		jm, _ := channelMeans(jpeg)
		f = append(f, lg(jm[0], jm[1])-lg(m[0], m[1]), lg(jm[2], jm[1])-lg(m[2], m[1]))
	}
	return f
}

// WBStudy prints the held-out error of each image-only balance estimator.
func WBStudy(name string, pairs []RawPair, w io.Writer) {
	type row struct {
		name                                                  string
		base, oracle, oracleWB, gw, wp, sog, ge, neutral, reg float64
	}
	var rows []row
	real := 0
	for _, p := range pairs {
		if !p.TrainOnly {
			real++
		}
	}
	if real < 4 {
		fmt.Fprintf(w, "\n%s: %d photos, too few for a held-out balance study\n", name, real)
		return
	}
	// features of the camera JPEG are only there when every training photo
	// has one; fitWBOracle uses 9 columns otherwise, so keep all photos.
	for h := range pairs {
		if pairs[h].TrainOnly {
			continue
		}
		var train []RawPair
		for i := range pairs {
			if i != h {
				train = append(train, pairs[i])
			}
		}
		fc := fitRaw(train)
		pred := applied(&fc, pairs[h].Raw)
		var a, b [3]float64
		for i := 0; i < len(pred.Pix); i += 3 {
			if max(pairs[h].SPP.Pix[i], pairs[h].SPP.Pix[i+1], pairs[h].SPP.Pix[i+2]) > 0.95 {
				continue
			}
			for k := 0; k < 3; k++ {
				a[k] += float64(pred.Pix[i+k])
				b[k] += float64(pairs[h].SPP.Pix[i+k])
			}
		}
		var og [3]float64
		for k := 0; k < 3; k++ {
			og[k] = math.Log(b[k] / a[k])
		}
		// WB-only oracle: green fixed, red/blue relative to it.
		var ogw [3]float64
		ogw[0] = og[0] - og[1]
		ogw[1] = 0
		ogw[2] = og[2] - og[1]
		row := row{name: pairs[h].Name}
		row.base = MeanError(pred, pairs[h].SPP)
		row.oracle = MeanError(applyGains(pred, og), pairs[h].SPP)
		row.oracleWB = MeanError(applyGains(pred, ogw), pairs[h].SPP)
		m, n := channelMeans(pred)
		row.gw = MeanError(applyGains(pred, greyGains(m)), pairs[h].SPP)
		// white patch from p99.5
		var chanv [3][]float64
		for i := 0; i < len(pred.Pix); i += 3 {
			for k := 0; k < 3; k++ {
				chanv[k] = append(chanv[k], float64(pred.Pix[i+k]))
			}
		}
		var wp [3]float64
		for k := 0; k < 3; k++ {
			wp[k] = percentile(chanv[k], 0.995)
		}
		if n > 0 && wp[0] > 0 && wp[1] > 0 && wp[2] > 0 {
			row.wp = MeanError(applyGains(pred, greyGains(wp)), pairs[h].SPP)
		} else {
			row.wp = row.gw
		}
		// shades-of-grey p=6
		var p6 [3]float64
		var cnt int
		for i := 0; i < len(pred.Pix); i += 3 {
			for k := 0; k < 3; k++ {
				p6[k] += math.Pow(float64(pred.Pix[i+k]), 6)
			}
			cnt++
		}
		for k := 0; k < 3; k++ {
			p6[k] = math.Pow(p6[k]/float64(max(1, cnt)), 1.0/6)
		}
		row.sog = MeanError(applyGains(pred, greyGains(p6)), pairs[h].SPP)
		// grey-edge: mean absolute derivative per channel
		var ge [3]float64
		for y := 0; y < pred.H; y++ {
			for x := 0; x < pred.W-1; x++ {
				i := (y*pred.W + x) * 3
				for k := 0; k < 3; k++ {
					ge[k] += math.Abs(float64(pred.Pix[i+3+k] - pred.Pix[i+k]))
				}
			}
		}
		row.ge = MeanError(applyGains(pred, greyGains(ge)), pairs[h].SPP)
		// the near-neutral bright pixels alone
		var neutral [3]float64
		var nn int
		for i := 0; i < len(pred.Pix); i += 3 {
			r, g, bb := float64(pred.Pix[i]), float64(pred.Pix[i+1]), float64(pred.Pix[i+2])
			hi, lo := max(r, g, bb), min(r, g, bb)
			y := 0.2126*r + 0.7152*g + 0.0722*bb
			if hi > 0 && lo/hi > 0.80 && y > 0.10 && hi < 0.95 {
				neutral[0] += r
				neutral[1] += g
				neutral[2] += bb
				nn++
			}
		}
		if nn > 100 {
			for k := range neutral {
				neutral[k] /= float64(nn)
			}
			row.neutral = MeanError(applyGains(pred, greyGains(neutral)), pairs[h].SPP)
		} else {
			row.neutral = row.gw
		}
		// regression on all the features, learned on the other photos
		var targets [][3]float64
		for _, tp := range train {
			tpPred := applied(&fc, tp.Raw)
			var ta, tb [3]float64
			for i := 0; i < len(tpPred.Pix); i += 3 {
				if max(tp.SPP.Pix[i], tp.SPP.Pix[i+1], tp.SPP.Pix[i+2]) > 0.95 {
					continue
				}
				for k := 0; k < 3; k++ {
					ta[k] += float64(tpPred.Pix[i+k])
					tb[k] += float64(tp.SPP.Pix[i+k])
				}
			}
			var tg [3]float64
			for k := 0; k < 3; k++ {
				tg[k] = math.Log(tb[k] / ta[k])
			}
			targets = append(targets, tg)
		}
		var reg [3]float64
		if len(targets) >= 6 {
			// regression on all the features, learned on the other photos
			f := wbFeatures(pred, pairs[h].JPEG)
			nf := len(f)
			var coef [11][3]float64
			var ata [11][11]float64
			var atb [3][11]float64
			for i := range train {
				tpPred := applied(&fc, train[i].Raw)
				tf := wbFeatures(tpPred, train[i].JPEG)
				for r := 0; r < len(tf); r++ {
					for q := 0; q < len(tf); q++ {
						ata[r][q] += tf[r] * tf[q]
					}
					for k := 0; k < 3; k++ {
						atb[k][r] += tf[r] * targets[i][k]
					}
				}
			}
			for r := 0; r < nf; r++ {
				ata[r][r] += 0.05
			}
			a2 := make([][]float64, nf)
			for r := range a2 {
				a2[r] = append([]float64(nil), ata[r][:nf]...)
			}
			for k := 0; k < 3; k++ {
				x := solveN(a2, append([]float64(nil), atb[k][:nf]...))
				for r := 0; r < nf; r++ {
					coef[r][k] = x[r]
				}
			}
			for k := 0; k < 3; k++ {
				for r := 0; r < nf; r++ {
					reg[k] += coef[r][k] * f[r]
				}
				reg[k] = math.Max(-1.5, math.Min(1.5, reg[k]))
			}
			row.reg = MeanError(applyGains(pred, reg), pairs[h].SPP)
		} else {
			row.reg = row.base
		}
		if math.IsNaN(row.wp) {
			row.wp = row.base
		}
		rows = append(rows, row)
		fmt.Fprintf(w, "%-12s %-9s base %5.1f  oracle %5.1f  wb-only %5.1f  gw %5.1f  wp %5.1f  sog6 %5.1f  edge %5.1f  neut %5.1f  reg %5.1f\n",
			row.name, pairs[h].WB, row.base, row.oracle, row.oracleWB, row.gw, row.wp, row.sog, row.ge, row.neutral, row.reg)
	}
	if len(rows) == 0 {
		return
	}
	var m row
	for _, r := range rows {
		m.base += r.base
		m.oracle += r.oracle
		m.oracleWB += r.oracleWB
		m.gw += r.gw
		m.wp += r.wp
		m.sog += r.sog
		m.ge += r.ge
		m.neutral += r.neutral
		m.reg += r.reg
	}
	n := float64(len(rows))
	fmt.Fprintf(w, "%s: %d photos held out per photo, mean 8-bit error\n", name, len(rows))
	fmt.Fprintf(w, "  base %.2f  oracle %.2f (wb-only %.2f)  gw %.2f  wp %.2f  sog6 %.2f  edge %.2f  neut %.2f  reg %.2f\n",
		m.base/n, m.oracle/n, m.oracleWB/n, m.gw/n, m.wp/n, m.sog/n, m.ge/n, m.neutral/n, m.reg/n)
}
