package calib

// Field measurement (task 23, part 1): is the older bodies' colour error a
// function of position in the frame? For every photo with an SPP neutral
// export we build the ratio of our developed linear image to SPP's linear
// export, heavily low-passed on a fixed normalized grid, masked to pixels
// neither clipped nor near black, and fit a low-order surface
// [1, u, v, (u²+v²)/2] per channel. FieldReport then asks, held out per
// photo, how much of the surface repeats across photos of a body, a lens, a
// focal length or an aperture, and how much of it a scene-only model (local
// luminance and chroma) explains instead.
//
// The grid is normalized to each frame's own [-1,1] box, so a landscape and a
// portrait photo share coordinates. Sensor coordinates undo the camera's
// display rotation, which is the flip test: a field fixed to the sensor keeps
// its shape there; a field that follows the scene does not.

import (
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/biopoetic/foveon-lab/render"
)

const (
	// FieldGW/FieldGH is the analysis grid: independent of the photo's
	// aspect, so every photo's field is sampled at the same coordinates.
	FieldGW = 48
	FieldGH = 36
	// fieldLo/fieldHi mask linear values near black and at clipping. A
	// block needs both images in this range in that channel.
	fieldLo = 0.004
	fieldHi = 0.94
)

// FieldSample is one photo's measured colour field.
type FieldSample struct {
	Name     string
	Group    string
	Rotation int
	Lens     string // LENSMODEL
	Focal    string // FLENGTH (mm)
	Aperture string // AP_DESC
	WB       string

	// G[c] is the smoothed log ratio our/SPP at each grid point, W[c] its
	// validity weight 0..1. Feat is [1, lnY, a/50, b/50] of SPP's scene.
	G    [3][]float64
	W    [3][]float64
	Feat [4][]float64

	// Coef is the fitted [1,u,v,r²] surface in display coordinates;
	// CoefSensor is the same fit in sensor coordinates. HiCoef is a
	// degree-4 surface (15 monomials), which can follow a smooth field the
	// 4 terms miss; HiOwnR2 is its fit to G.
	Coef, CoefSensor [3][4]float64
	HiCoef           [3][]float64
	HiCoefSensor     [3][]float64
	// OwnR2[c]: share of G's spatial variance the 4-term surface describes.
	OwnR2 [3]float64
	// HiOwnR2[c]: the same for the degree-4 surface.
	HiOwnR2 [3]float64
	// SpatialStd[c]: weighted std of G (the field's amplitude in log units).
	SpatialStd [3]float64
	// Mean[c]: weighted mean of G (the photo's global colour offset).
	Mean [3]float64
	// SG is log(CAMF SpatialGain) sampled at the grid's display coordinates
	// (nil when the file has no colour-shading table), and SGDims its shape.
	SG        [3][]float64
	SGSensor  [3][]float64 // the same table on a sensor-fixed grid
	SGDims    string
	SGPresent bool
}

// fieldUV is the normalized coordinate of grid point (i,j).
func fieldUV(i, j int) (u, v float64) {
	return -1 + 2*(float64(i)+0.5)/FieldGW, -1 + 2*(float64(j)+0.5)/FieldGH
}

// basisAt is [1, u, v, (u²+v²)/2] at a grid point in the chosen coordinates.
func basisAt(i, j, rotation int) [4]float64 {
	u, v := fieldUV(i, j)
	switch rotation {
	case 90: // display = sensor turned 90° clockwise
		u, v = v, -u
	case 180:
		u, v = -u, -v
	case 270:
		u, v = -v, u
	}
	return [4]float64{1, u, v, (u*u + v*v) / 2}
}

// basisOrderAt is basisAt for order 1, or every monomial u^i v^j with
// i+j <= order for a higher order (4 gives 15 terms).
func basisOrderAt(i, j, rotation, order int) []float64 {
	if order <= 1 {
		b := basisAt(i, j, rotation)
		return b[:]
	}
	u, v := fieldUV(i, j)
	switch rotation {
	case 90:
		u, v = v, -u
	case 180:
		u, v = -u, -v
	case 270:
		u, v = -v, u
	}
	out := make([]float64, 0, (order+1)*(order+2)/2)
	for d := 0; d <= order; d++ {
		for a := 0; a <= d; a++ {
			out = append(out, math.Pow(u, float64(a))*math.Pow(v, float64(d-a)))
		}
	}
	return out
}

// MeasureField builds a FieldSample from our linear image and SPP's, which
// must be the same size.
func MeasureField(our, spp *render.Linear) *FieldSample {
	out := &FieldSample{}
	n := FieldGW * FieldGH
	for c := 0; c < 3; c++ {
		out.G[c] = make([]float64, n)
		out.W[c] = make([]float64, n)
	}
	for k := range out.Feat {
		out.Feat[k] = make([]float64, n)
	}
	w, h := our.W, our.H
	if spp.W != w || spp.H != h {
		return out
	}
	for j := 0; j < FieldGH; j++ {
		y0 := j * h / FieldGH
		y1 := (j + 1) * h / FieldGH
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for i := 0; i < FieldGW; i++ {
			x0 := i * w / FieldGW
			x1 := (i + 1) * w / FieldGW
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sum [3][2]float64
			var cnt [3]int
			var ssum [3]float64
			var sn int
			for y := y0; y < y1; y++ {
				row := y * w * 3
				for x := x0; x < x1; x++ {
					p := row + x*3
					r, g, b := float64(our.Pix[p]), float64(our.Pix[p+1]), float64(our.Pix[p+2])
					sr, sg, sb := float64(spp.Pix[p]), float64(spp.Pix[p+1]), float64(spp.Pix[p+2])
					if r >= fieldLo && r <= fieldHi && sr >= fieldLo && sr <= fieldHi {
						sum[0][0] += r
						sum[0][1] += sr
						cnt[0]++
					}
					if g >= fieldLo && g <= fieldHi && sg >= fieldLo && sg <= fieldHi {
						sum[1][0] += g
						sum[1][1] += sg
						cnt[1]++
					}
					if b >= fieldLo && b <= fieldHi && sb >= fieldLo && sb <= fieldHi {
						sum[2][0] += b
						sum[2][1] += sb
						cnt[2]++
					}
					// Scene features: every pixel, from SPP (the scene as
					// SPP saw it), used only for the scene-only diagnosis.
					ssum[0] += sr
					ssum[1] += sg
					ssum[2] += sb
					sn++
				}
			}
			idx := j*FieldGW + i
			area := (x1 - x0) * (y1 - y0)
			for c := 0; c < 3; c++ {
				if cnt[c] < area/2 {
					continue
				}
				a := sum[c][0] / float64(cnt[c])
				b := sum[c][1] / float64(cnt[c])
				out.G[c][idx] = math.Log(a / b)
				out.W[c][idx] = float64(cnt[c]) / float64(area)
			}
			if sn > 0 {
				r, g, b := ssum[0]/float64(sn), ssum[1]/float64(sn), ssum[2]/float64(sn)
				y := 0.2126*r + 0.7152*g + 0.0722*b
				_, A, B := lab(r, g, b)
				out.Feat[0][idx] = 1
				out.Feat[1][idx] = math.Log(math.Max(y, 1e-4))
				out.Feat[2][idx] = A / 50
				out.Feat[3][idx] = B / 50
			}
		}
	}
	out.Surface()
	return out
}

func solveN(a [][]float64, b []float64) []float64 {
	n := len(b)
	for c := 0; c < n; c++ {
		p := c
		for r := c + 1; r < n; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		a[c], a[p] = a[p], a[c]
		b[c], b[p] = b[p], b[c]
		if math.Abs(a[c][c]) < 1e-12 {
			continue
		}
		for r := c + 1; r < n; r++ {
			f := a[r][c] / a[c][c]
			for k := c; k < n; k++ {
				a[r][k] -= f * a[c][k]
			}
			b[r] -= f * b[c]
		}
	}
	x := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		if math.Abs(a[r][r]) < 1e-12 {
			continue
		}
		s := b[r]
		for k := r + 1; k < n; k++ {
			s -= a[r][k] * x[k]
		}
		x[r] = s / a[r][r]
	}
	return x
}

// fitSurfaceOrder fits coef·basis of the given order to g with weights wt,
// returning the coefficients, residual rms and R² around the weighted mean.
func fitSurfaceOrder(g, wt []float64, rotation, order int) (coef []float64, rms, r2 float64) {
	nf := 4
	if order > 1 {
		nf = (order + 1) * (order + 2) / 2
	}
	ata := make([][]float64, nf)
	for r := range ata {
		ata[r] = make([]float64, nf)
	}
	atb := make([]float64, nf)
	var sw, sy, syy float64
	valid := 0
	for j := 0; j < FieldGH; j++ {
		for i := 0; i < FieldGW; i++ {
			k := j*FieldGW + i
			if wt[k] <= 0 {
				continue
			}
			valid++
			b := basisOrderAt(i, j, rotation, order)
			y, w := g[k], wt[k]
			for r := 0; r < nf; r++ {
				atb[r] += w * b[r] * y
				for q := 0; q < nf; q++ {
					ata[r][q] += w * b[r] * b[q]
				}
			}
			sw += w
			sy += w * y
			syy += w * y * y
		}
	}
	if valid < nf+4 {
		return
	}
	for r := 0; r < nf; r++ {
		ata[r][r] += 1e-9
	}
	coef = solveN(ata, atb)
	mean := sy / sw
	var ss, tot float64
	for j := 0; j < FieldGH; j++ {
		for i := 0; i < FieldGW; i++ {
			k := j*FieldGW + i
			if wt[k] <= 0 {
				continue
			}
			b := basisOrderAt(i, j, rotation, order)
			p := 0.0
			for r := 0; r < nf; r++ {
				p += coef[r] * b[r]
			}
			d := g[k] - p
			ss += wt[k] * d * d
			tot += wt[k] * (g[k] - mean) * (g[k] - mean)
		}
	}
	rms = math.Sqrt(ss / sw)
	if tot > 1e-12 {
		r2 = 1 - ss/tot
	}
	return
}

// fitSurface is fitSurfaceOrder for the task's low-order [1,u,v,r²] basis.
func fitSurface(g, wt []float64, rotation int) (coef [4]float64, rms, r2 float64) {
	x, rms, r2 := fitSurfaceOrder(g, wt, rotation, 1)
	copy(coef[:], x)
	return coef, rms, r2
}

// Surface fills Coef, CoefSensor, OwnR2, HiCoef, HiOwnR2, SpatialStd and Mean.
func (s *FieldSample) Surface() {
	for c := 0; c < 3; c++ {
		coef, _, r2 := fitSurface(s.G[c], s.W[c], 0)
		s.Coef[c] = coef
		s.OwnR2[c] = r2
		cs, _, _ := fitSurface(s.G[c], s.W[c], s.Rotation)
		s.CoefSensor[c] = cs
		hi, _, hr2 := fitSurfaceOrder(s.G[c], s.W[c], 0, 4)
		s.HiCoef[c] = hi
		s.HiOwnR2[c] = hr2
		his, _, _ := fitSurfaceOrder(s.G[c], s.W[c], s.Rotation, 4)
		s.HiCoefSensor[c] = his
		var sw, sy, syy float64
		for k := range s.G[c] {
			if s.W[c][k] <= 0 {
				continue
			}
			w := s.W[c][k]
			sw += w
			sy += w * s.G[c][k]
			syy += w * s.G[c][k] * s.G[c][k]
		}
		if sw > 0 {
			s.Mean[c] = sy / sw
			s.SpatialStd[c] = math.Sqrt(math.Max(0, syy/sw-s.Mean[c]*s.Mean[c]))
		}
	}
}

// fieldSet is one group of photos to test for a shared field.
type fieldSet struct {
	Key string
	Ids []int
}

// fieldSets builds groups of at least three photos: by body, then within each
// body by lens, focal length and aperture.
func fieldSets(ss []*FieldSample) []fieldSet {
	by := map[string][]int{}
	add := func(key string, i int) {
		if key == "" {
			return
		}
		by[key] = append(by[key], i)
	}
	for i, s := range ss {
		add("body "+s.Group, i)
		add("body "+s.Group+" lens "+s.Lens, i)
		add("body "+s.Group+" focal "+s.Focal, i)
		add("body "+s.Group+" ap "+s.Aperture, i)
	}
	var out []fieldSet
	for k, ids := range by {
		if len(ids) >= 3 {
			out = append(out, fieldSet{k, ids})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Key < out[b].Key })
	return out
}

// sharedShapeR2 is the held-out-per-photo share of the field's shape that the
// other photos' mean surface explains, in display or sensor coordinates. The
// target is the measured G itself, so the number is a share of the field, not
// only of its low-order part. order 1 is the task's [1,u,v,r²]; order 4 is a
// smooth 15-term surface.
func sharedShapeR2(ss []*FieldSample, ids []int, c int, sensor bool, order int) (r2, amp float64) {
	var ssres, sstot, sw float64
	rot := 0
	if sensor {
		rot = 1
	}
	for _, i := range ids {
		s := ss[i]
		var pred []float64
		var n int
		for _, q := range ids {
			if q == i {
				continue
			}
			cq := ss[q].Coef[c][:]
			if order > 1 {
				cq = ss[q].HiCoef[c]
				if sensor {
					cq = ss[q].HiCoefSensor[c]
				}
			} else if sensor {
				cq = ss[q].CoefSensor[c][:]
			}
			if pred == nil {
				pred = make([]float64, len(cq))
			}
			for t := range cq {
				pred[t] += cq[t]
			}
			n++
		}
		if n == 0 {
			continue
		}
		for t := range pred {
			pred[t] /= float64(n)
		}
		rotDeg := 0
		if rot == 1 {
			rotDeg = s.Rotation
		}
		// Weighted means of the predicted surface and of the target.
		var pm, tm, pw float64
		for j := 0; j < FieldGH; j++ {
			for ii := 0; ii < FieldGW; ii++ {
				k := j*FieldGW + ii
				w := s.W[c][k]
				if w <= 0 {
					continue
				}
				b := basisOrderAt(ii, j, rotDeg, order)
				v := 0.0
				for t := range pred {
					v += pred[t] * b[t]
				}
				pm += w * v
				tm += w * s.G[c][k]
				pw += w
			}
		}
		if pw <= 0 {
			continue
		}
		pm /= pw
		tm /= pw
		for j := 0; j < FieldGH; j++ {
			for ii := 0; ii < FieldGW; ii++ {
				k := j*FieldGW + ii
				w := s.W[c][k]
				if w <= 0 {
					continue
				}
				b := basisOrderAt(ii, j, rotDeg, order)
				v := 0.0
				for t := range pred {
					v += pred[t] * b[t]
				}
				d := (s.G[c][k] - tm) - (v - pm)
				ssres += w * d * d
				sstot += w * (s.G[c][k] - tm) * (s.G[c][k] - tm)
				sw += w
			}
		}
	}
	if sstot > 1e-15 {
		r2 = 1 - ssres/sstot
	}
	if sw > 0 {
		amp = math.Sqrt(sstot / sw)
	}
	return
}

// meanSurfaceAmp is the RMS amplitude (log units) of the group's mean fitted
// surface around its own spatial mean: how big the shared field would be.
func meanSurfaceAmp(ss []*FieldSample, ids []int, c int, order int) float64 {
	nf := 4
	if order > 1 {
		nf = (order + 1) * (order + 2) / 2
	}
	mean := make([]float64, nf)
	for _, i := range ids {
		co := ss[i].Coef[c][:]
		if order > 1 {
			co = ss[i].HiCoef[c]
		}
		for t := 0; t < nf; t++ {
			mean[t] += co[t]
		}
	}
	for t := range mean {
		mean[t] /= float64(len(ids))
	}
	var sy, syy, sw float64
	for j := 0; j < FieldGH; j++ {
		for i := 0; i < FieldGW; i++ {
			w := 1.0
			b := basisOrderAt(i, j, 0, order)
			v := 0.0
			for t := 0; t < nf; t++ {
				v += mean[t] * b[t]
			}
			sy += w * v
			syy += w * v * v
			sw += w
		}
	}
	m := sy / sw
	return math.Sqrt(math.Max(0, syy/sw-m*m))
}

// modelShapeR2 fits a pooled per-channel model on all photos but the held-out
// one and predicts that photo's field shape. Columns are the position basis
// (u, v, r²) and/or the scene features (lnY, a/50, b/50), every column
// mean-removed per photo.
func modelShapeR2(ss []*FieldSample, ids []int, c int, usePos, useScene bool) (r2 float64) {
	nf := 0
	if usePos {
		nf += 3
	}
	if useScene {
		nf += 3
	}
	if nf == 0 || len(ids) < 2 {
		return 0
	}
	np := FieldGW * FieldGH
	type photo struct {
		target []float64
		feat   [][]float64
		w      []float64
	}
	ps := make([]photo, len(ids))
	for a := range ids {
		s := ss[ids[a]]
		p := &ps[a]
		p.w = s.W[c]
		p.target = make([]float64, np)
		// centre the target
		var sw, sy float64
		for k := 0; k < np; k++ {
			if p.w[k] <= 0 {
				continue
			}
			sw += p.w[k]
			sy += p.w[k] * s.G[c][k]
		}
		m := sy / sw
		for k := 0; k < np; k++ {
			p.target[k] = s.G[c][k] - m
		}
		// raw feature columns
		var cols [][]float64
		if usePos {
			for f := 1; f < 4; f++ {
				v := make([]float64, np)
				for j := 0; j < FieldGH; j++ {
					for i2 := 0; i2 < FieldGW; i2++ {
						v[j*FieldGW+i2] = basisAt(i2, j, 0)[f]
					}
				}
				cols = append(cols, v)
			}
		}
		if useScene {
			for f := 1; f < 4; f++ {
				cols = append(cols, s.Feat[f])
			}
		}
		// centre each column with the same weights
		for f := range cols {
			var sc float64
			for k := 0; k < np; k++ {
				if p.w[k] > 0 {
					sc += p.w[k] * cols[f][k]
				}
			}
			sc /= sw
			for k := 0; k < np; k++ {
				cols[f][k] -= sc
			}
		}
		p.feat = cols
	}
	var ssres, sstot float64
	for h := range ids {
		ata := make([][]float64, nf)
		for r := range ata {
			ata[r] = make([]float64, nf)
		}
		atb := make([]float64, nf)
		for a := range ps {
			if a == h {
				continue
			}
			for k := 0; k < np; k++ {
				w := ps[a].w[k]
				if w <= 0 {
					continue
				}
				for r := 0; r < nf; r++ {
					atb[r] += w * ps[a].feat[r][k] * ps[a].target[k]
					for q := 0; q < nf; q++ {
						ata[r][q] += w * ps[a].feat[r][k] * ps[a].feat[q][k]
					}
				}
			}
		}
		for r := 0; r < nf; r++ {
			ata[r][r] += 1e-9
		}
		x := solveN(ata, atb)
		// predict the held-out photo and remove the prediction's own mean
		pred := make([]float64, np)
		var sw, sp float64
		for k := 0; k < np; k++ {
			if ps[h].w[k] <= 0 {
				continue
			}
			for r := 0; r < nf; r++ {
				pred[k] += x[r] * ps[h].feat[r][k]
			}
			sw += ps[h].w[k]
			sp += ps[h].w[k] * pred[k]
		}
		if sw <= 0 {
			continue
		}
		pm := sp / sw
		for k := 0; k < np; k++ {
			w := ps[h].w[k]
			if w <= 0 {
				continue
			}
			d := ps[h].target[k] - (pred[k] - pm)
			ssres += w * d * d
			sstot += w * ps[h].target[k] * ps[h].target[k]
		}
	}
	if sstot > 1e-15 {
		r2 = 1 - ssres/sstot
	}
	return
}

// FieldReport prints the measurement and the held-out sharedness tables.
func FieldReport(ss []*FieldSample, w io.Writer) {
	fmt.Fprintf(w, "\ncolour field: ours/SPP in linear, %dx%d block means, log units\n", FieldGW, FieldGH)
	fmt.Fprintf(w, "%-8s %-26s %4s %-6s %-5s %-4s  %8s %8s %8s  %7s %7s %7s\n",
		"group", "photo", "rot", "lens", "focal", "ap", "Rmean", "Gmean", "Bmean", "Rstd", "Gstd", "Bstd")
	byGroup := map[string][]*FieldSample{}
	var groups []string
	for _, s := range ss {
		byGroup[s.Group] = append(byGroup[s.Group], s)
	}
	for g := range byGroup {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		for _, s := range byGroup[g] {
			fmt.Fprintf(w, "%-8s %-26s %4d %-6s %-5s %-4s  %+8.4f %+8.4f %+8.4f  %7.4f %7.4f %7.4f\n",
				s.Group, s.Name, s.Rotation, s.Lens, s.Focal, s.Aperture,
				s.Mean[0], s.Mean[1], s.Mean[2], s.SpatialStd[0], s.SpatialStd[1], s.SpatialStd[2])
		}
	}
	fmt.Fprintf(w, "\nheld out per photo: share of the *measured field* the mean surface of the other photos explains\n")
	fmt.Fprintf(w, "%-34s %4s  %6s %6s %6s  %6s %6s %6s   %6s\n", "set", "n",
		"p4 r", "p4 g", "p4 b", "p15 r", "p15 g", "p15 b", "null")
	for _, set := range fieldSets(ss) {
		var r4, r15 [3]float64
		for c := 0; c < 3; c++ {
			r4[c], _ = sharedShapeR2(ss, set.Ids, c, false, 1)
			r15[c], _ = sharedShapeR2(ss, set.Ids, c, false, 4)
		}
		fmt.Fprintf(w, "%-34s %4d  %6.3f %6.3f %6.3f  %6.3f %6.3f %6.3f   %6.3f\n",
			set.Key, len(set.Ids), r4[0], r4[1], r4[2], r15[0], r15[1], r15[2], -1/float64(len(set.Ids)-1))
	}
	fmt.Fprintf(w, "\nby body: display vs sensor coordinates (the flip test), mean-surface amplitude, own fit\n")
	fmt.Fprintf(w, "%-8s %4s  %6s %6s %6s  %6s %6s %6s  %6s %6s %6s  %6s %6s %6s  %6s %6s\n", "body", "n",
		"p4 r", "p4 g", "p4 b", "p15 r", "p15 g", "p15 b", "sen r", "sen g", "sen b", "amp r", "amp g", "amp b", "own4", "own15")
	for _, g := range groups {
		ids := bodyIds(ss, g)
		var p4, p15, sen, amp [3]float64
		var own4, own15 float64
		for c := 0; c < 3; c++ {
			p4[c], _ = sharedShapeR2(ss, ids, c, false, 1)
			p15[c], _ = sharedShapeR2(ss, ids, c, false, 4)
			sen[c], _ = sharedShapeR2(ss, ids, c, true, 4)
			amp[c] = meanSurfaceAmp(ss, ids, c, 1)
			for _, i := range ids {
				own4 += ss[i].OwnR2[c] / float64(3*len(ids))
				own15 += ss[i].HiOwnR2[c] / float64(3*len(ids))
			}
		}
		fmt.Fprintf(w, "%-8s %4d  %6.3f %6.3f %6.3f  %6.3f %6.3f %6.3f  %6.3f %6.3f %6.3f  %6.3f %6.3f %6.3f  %6.3f %6.3f\n",
			g, len(ids), p4[0], p4[1], p4[2], p15[0], p15[1], p15[2], sen[0], sen[1], sen[2], amp[0], amp[1], amp[2], own4, own15)
	}
	fmt.Fprintf(w, "\nby body: scene-only (local luminance and chroma) vs position+scene, held out per photo\n")
	fmt.Fprintf(w, "%-8s %4s  %6s %6s %6s  %6s %6s %6s  %9s\n", "body", "n", "scn r", "scn g", "scn b",
		"cms r", "cms g", "cms b", "fieldAmp")
	for _, g := range groups {
		ids := bodyIds(ss, g)
		var sc, cm [3]float64
		for c := 0; c < 3; c++ {
			sc[c] = modelShapeR2(ss, ids, c, false, true)
			cm[c] = modelShapeR2(ss, ids, c, true, true)
		}
		_, amp := sharedShapeR2(ss, ids, 1, false, 1)
		fmt.Fprintf(w, "%-8s %4d  %6.3f %6.3f %6.3f  %6.3f %6.3f %6.3f  %9.4f\n",
			g, len(ids), sc[0], sc[1], sc[2], cm[0], cm[1], cm[2], amp)
	}
	fmt.Fprintf(w, "\nby body: consistent linear tilt of the shared surface (milli-log: 1000·mean ± sem, t)\n")
	fmt.Fprintf(w, "%-8s %-2s  %16s %16s\n", "body", "ch", "u (left-right)", "v (top-bottom)")
	for _, g := range groups {
		ids := bodyIds(ss, g)
		for c, ch := range []string{"r", "g", "b"} {
			var mu, su, mv, sv float64
			for _, i := range ids {
				mu += ss[i].Coef[c][1]
				mv += ss[i].Coef[c][2]
			}
			n := float64(len(ids))
			mu /= n
			mv /= n
			for _, i := range ids {
				du, dv := ss[i].Coef[c][1]-mu, ss[i].Coef[c][2]-mv
				su += du * du
				sv += dv * dv
			}
			su = math.Sqrt(su / math.Max(1, n-1) / n)
			sv = math.Sqrt(sv / math.Max(1, n-1) / n)
			fmt.Fprintf(w, "%-8s %-2s  %+7.1f±%4.1f t%+5.1f  %+7.1f±%4.1f t%+5.1f\n", g, ch,
				mu*1000, su*1000, mu/math.Max(su, 1e-9), mv*1000, sv*1000, mv/math.Max(sv, 1e-9))
		}
	}
	fmt.Fprintf(w, "\np4/p15: 4-term and degree-4 shared surface; sen: degree-4 in sensor coordinates; null: -1/(n-1)\n")
	fmt.Fprintf(w, "own4/own15: mean share of a photo's smoothed field the same surface fits on its own\n")
	fmt.Fprintf(w, "amp: RMS of the group's mean 4-term surface (log units; 0.01 = 1%%)\n")
	SpatialGainReport(ss, w)
}

// SpatialGainReport compares the measured field with the X3F's own CAMF
// colour-shading table: its amplitude, whether it repeats across photos, and
// whether G follows it (a +1 slope would mean we failed to apply the table
// SPP applies).
func SpatialGainReport(ss []*FieldSample, w io.Writer) {
	type agg struct {
		n           int
		dims        map[string]int
		std         [3]float64
		corr, slope [3]float64
		rep         [3]float64
		nrep        [3]int
	}
	by := map[string]*agg{}
	var groups []string
	for _, s := range ss {
		if !s.SGPresent {
			continue
		}
		a := by[s.Group]
		if a == nil {
			a = &agg{dims: map[string]int{}}
			by[s.Group] = a
		}
		a.n++
		a.dims[s.SGDims]++
		for c := 0; c < 3; c++ {
			// amplitude of the table's log field on the sensor grid
			var sw, sy, syy float64
			for _, v := range s.SGSensor[c] {
				sw++
				sy += v
				syy += v * v
			}
			if sw > 0 {
				m := sy / sw
				a.std[c] += math.Sqrt(math.Max(0, syy/sw-m*m))
			}
			// does G follow the table? (display-sampled SG, so a rotated
			// photo's table is in the same frame as G)
			var sa, sb, saa, sbb, sab, ww float64
			for k := range s.G[c] {
				wt := s.W[c][k]
				if wt <= 0 || s.SG[c] == nil {
					continue
				}
				sa += wt * s.SG[c][k]
				sb += wt * s.G[c][k]
				ww += wt
			}
			if ww > 0 {
				ma, mb := sa/ww, sb/ww
				for k := range s.G[c] {
					wt := s.W[c][k]
					if wt <= 0 || s.SG[c] == nil {
						continue
					}
					da, db := s.SG[c][k]-ma, s.G[c][k]-mb
					saa += wt * da * da
					sbb += wt * db * db
					sab += wt * da * db
				}
				if saa > 1e-12 && sbb > 1e-12 {
					a.corr[c] += sab / math.Sqrt(saa*sbb)
					a.slope[c] += sab / saa
				}
			}
		}
	}
	for g := range by {
		groups = append(groups, g)
	}
	// table repeat: pairwise correlation of the sensor-grid log tables within
	// each body (1 = the same table in every file, 0 = unrelated).
	for _, g := range groups {
		var ids []int
		for i, s := range ss {
			if s.Group == g && s.SGPresent {
				ids = append(ids, i)
			}
		}
		a := by[g]
		for x := 0; x < len(ids); x++ {
			for y := x + 1; y < len(ids); y++ {
				for c := 0; c < 3; c++ {
					var sa, sbb, saa, sab, ww float64
					pa, pb := ss[ids[x]].SGSensor[c], ss[ids[y]].SGSensor[c]
					for k := range pa {
						sa += pa[k]
						sbb += pb[k]
						ww++
					}
					if ww == 0 {
						continue
					}
					ma, mb := sa/ww, sbb/ww
					for k := range pa {
						da, db := pa[k]-ma, pb[k]-mb
						saa += da * da
						sab += da * db
					}
					if saa > 1e-12 {
						a.rep[c] += sab / saa
						a.nrep[c]++
					}
				}
			}
		}
	}
	sort.Strings(groups)
	fmt.Fprintf(w, "\nCAMF SpatialGain (the X3F's own colour-shading table): amplitude and relation to G\n")
	fmt.Fprintf(w, "%-8s %4s %-12s  %6s %6s %6s  %6s %6s %6s  %6s %6s %6s  %6s %6s %6s\n", "body", "n", "dims",
		"std r", "std g", "std b", "cor r", "cor g", "cor b", "slp r", "slp g", "slp b", "rep r", "rep g", "rep b")
	for _, g := range groups {
		a := by[g]
		var dims []string
		for d := range a.dims {
			dims = append(dims, d)
		}
		sort.Strings(dims)
		ds := ""
		for i, d := range dims {
			if i > 0 {
				ds += ","
			}
			ds += d
		}
		var rep [3]float64
		for c := 0; c < 3; c++ {
			if a.nrep[c] > 0 {
				rep[c] = a.rep[c] / float64(a.nrep[c])
			}
			a.std[c] /= float64(a.n)
			a.corr[c] /= float64(a.n)
			a.slope[c] /= float64(a.n)
		}
		fmt.Fprintf(w, "%-8s %4d %-12s  %6.4f %6.4f %6.4f  %6.3f %6.3f %6.3f  %6.2f %6.2f %6.2f  %6.3f %6.3f %6.3f\n",
			g, a.n, ds, a.std[0], a.std[1], a.std[2], a.corr[0], a.corr[1], a.corr[2], a.slope[0], a.slope[1], a.slope[2], rep[0], rep[1], rep[2])
	}
	fmt.Fprintf(w, "cor/slp: G against log(SpatialGain), per photo then averaged (a +1 slope = the table is not applied)\n")
	fmt.Fprintf(w, "rep: mean pairwise correlation of the files' tables on a sensor-fixed grid (1 = identical)\n")
}

// bodyIds is the indices of one body's samples.
func bodyIds(ss []*FieldSample, g string) []int {
	var ids []int
	for i, s := range ss {
		if s.Group == g {
			ids = append(ids, i)
		}
	}
	return ids
}
