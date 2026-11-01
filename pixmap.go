package main

// pixmap-study (task 25, part 1): how much can a global per-pixel colour
// mapping do on top of the shipped raw base?  The model is a smooth 3D
// lookup in CIELAB: a regular grid over (L*, a*, b*) whose node values are
// additive offsets in encoded sRGB, interpolated trilinearly.  The fit is
// ridge-regularised towards zero (no change) and towards a smooth field
// (discrete second differences along each axis).
//
// Everything is judged the way spp-check judges: the shipped render of every
// photo against its SPP A00 export, at the analysis size.  The mapping is
// fitted on the other photos and scored on the held-out one; series of frames
// from one shoot (capture clock, 30 min) are held out whole as well.  The
// oracle fits the same model on the photo itself, so it says how much a
// mapping of that size could do with no generalisation at all.
//
//	foveon-lab pixmap-study [-photos "..."] [-dir ...] [-groups SD14,SD9,...]
//	    [-sizes 2,3,4,5,6,7,8,10] [-alpha 0.3] [-beta 1] [-stride 2]
//	    [-cache zz-out/task25/samples] [-csv out.csv] [-json out.json]

import (
	"encoding/gob"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/render"
)

const (
	pmCacheVersion = 2
	pmSampleStride = 2
	pmBoxExpand    = 0.08 // box padding, so the fade sits outside the data
	pmFadeWidth    = 0.05 // fade to no change over this fraction of the box
	pmCGMaxIter    = 400
	pmCGTol        = 1e-6
)

// pmSample is one photo's analysis-size data, subsampled by pmSampleStride in
// each direction: CIELAB of the shipped render, its 8-bit sRGB values and
// SPP's encoded values.  Cacheable between runs of the study.
type pmSample struct {
	Version int
	Name    string
	Group   string
	W, H    int
	Time    int64
	Stride  int
	Lab     []float32 // 3 per sample, L*, a*, b*
	Base    []uint8   // 3 per sample, encoded sRGB 0..255
	Targ    []uint16  // 3 per sample, SPP encoded sRGB 0..65535
}

func (s *pmSample) n() int { return len(s.Lab) / 3 }

// pmLAB converts linear sRGB (D65) to CIELAB; calib's own lab is unexported.
func pmLAB(r, g, b float64) (L, A, B float64) {
	x := 0.4124564*r + 0.3575761*g + 0.1804375*b
	y := 0.2126729*r + 0.7151522*g + 0.0721750*b
	z := 0.0193339*r + 0.1191920*g + 0.9503041*b
	f := func(t float64) float64 {
		if t > 216.0/24389 {
			return math.Cbrt(t)
		}
		return (24389.0/27*t + 16) / 116
	}
	fx, fy, fz := f(x/0.95047), f(y), f(z/1.08883)
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

func pmClamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func pmEnc(v float32) float64 { return float64(render.ToSRGB(float32(pmClamp01(float64(v))))) }

// pmLoad reads (or builds) the study samples for the wanted groups.
func pmLoad(dir string, photos string, want map[string]bool, stride int, cache string) map[string][]*pmSample {
	exports := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(dir, "*", "*_A00.tif"))
	for _, m := range matches {
		name := strings.ToUpper(strings.TrimSuffix(filepath.Base(m), "_A00.tif"))
		if _, ok := exports[name]; !ok || filepath.Base(filepath.Dir(m)) == "exports" {
			exports[name] = m
		}
	}
	byName := map[string]*Photo{}
	for _, p := range scanPhotos(strings.Split(photos, ";"), false) {
		if p.Kind != "x3f" || p.Unsupported != "" {
			continue
		}
		key := strings.ToUpper(strings.TrimSuffix(p.Name, filepath.Ext(p.Name)))
		if _, ok := byName[key]; !ok {
			byName[key] = p
		}
	}
	var names []string
	for n := range exports {
		names = append(names, n)
	}
	sort.Strings(names)

	out := map[string][]*pmSample{}
	for _, name := range names {
		ph := byName[name]
		if ph == nil {
			continue
		}
		group := calibGroup(ph)
		if !want[group] {
			continue
		}
		if _, bad := calib.ExcludedReference(name, group); bad {
			continue
		}
		cacheFile := ""
		if cache != "" {
			cacheFile = filepath.Join(cache, group, name+".gob")
		}
		if cacheFile != "" {
			if f, err := os.Open(cacheFile); err == nil {
				var s pmSample
				err := gob.NewDecoder(f).Decode(&s)
				f.Close()
				if err == nil && s.Version == pmCacheVersion && s.Stride == stride && s.Name == name && s.Group == group && len(s.Lab) > 0 {
					out[group] = append(out[group], &s)
					continue
				}
			}
		}
		v, spp, err := checkRender(ph, exports[name])
		if err != nil {
			fmt.Printf("  %s: %v\n", name, err)
			continue
		}
		if v.W != spp.W || v.H != spp.H {
			fmt.Printf("  %s: ours %dx%d, SPP %dx%d, skipped\n", name, v.W, v.H, spp.W, spp.H)
			continue
		}
		s := &pmSample{Version: pmCacheVersion, Name: name, Group: group, W: v.W, H: v.H, Stride: stride}
		s.Time, _ = strconv.ParseInt(strings.TrimSpace(ph.props["TIME"]), 10, 64)
		np := (v.W + stride - 1) / stride
		nq := (v.H + stride - 1) / stride
		n := np * nq
		s.Lab = make([]float32, 3*n)
		s.Base = make([]uint8, 3*n)
		s.Targ = make([]uint16, 3*n)
		k := 0
		for y := 0; y < v.H; y += stride {
			for x := 0; x < v.W; x += stride {
				i := (y*v.W + x) * 3
				cr := pmClamp01(float64(v.Pix[i]))
				cg := pmClamp01(float64(v.Pix[i+1]))
				cb := pmClamp01(float64(v.Pix[i+2]))
				L, A, B := pmLAB(cr, cg, cb)
				s.Lab[k*3], s.Lab[k*3+1], s.Lab[k*3+2] = float32(L), float32(A), float32(B)
				for c := 0; c < 3; c++ {
					s.Base[k*3+c] = uint8(pmEnc(v.Pix[i+c])*255 + 0.5)
					s.Targ[k*3+c] = uint16(pmEnc(spp.Pix[i+c])*65535 + 0.5)
				}
				k++
			}
		}
		s.Lab, s.Base, s.Targ = s.Lab[:k*3], s.Base[:k*3], s.Targ[:k*3]
		out[group] = append(out[group], s)
		fmt.Printf("  %-12s %-34s %dx%d %d samples\n", group, name, s.W, s.H, s.n())
		if cacheFile != "" {
			os.MkdirAll(filepath.Dir(cacheFile), 0o755)
			if f, err := os.Create(cacheFile); err == nil {
				gob.NewEncoder(f).Encode(s)
				f.Close()
			}
		}
	}
	for g := range out {
		sort.Slice(out[g], func(i, j int) bool { return out[g][i].Name < out[g][j].Name })
	}
	return out
}

// pmGrid is the model's coordinate box and resolution.
type pmGrid struct {
	N                      int
	L0, L1, A0, A1, B0, B1 float64
	M, ncells              int
}

func pmNewGrid(n int, L0, L1, A0, A1, B0, B1 float64) *pmGrid {
	return &pmGrid{N: n, L0: L0, L1: L1, A0: A0, A1: A1, B0: B0, B1: B1,
		M: n * n * n, ncells: (n - 1) * (n - 1) * (n - 1)}
}

// pmBox gives the coordinate box: percentiles 0.5/99.5 of the body's sampled
// colours, expanded by pmBoxExpand of each range.
func pmBox(ss []*pmSample) (L0, L1, A0, A1, B0, B1 float64) {
	var ls, as, bs []float64
	for _, s := range ss {
		for i := 0; i < s.n(); i += 5 {
			ls = append(ls, float64(s.Lab[i*3]))
			as = append(as, float64(s.Lab[i*3+1]))
			bs = append(bs, float64(s.Lab[i*3+2]))
		}
	}
	pct := func(v []float64, p float64) float64 {
		if len(v) == 0 {
			return 0
		}
		sort.Float64s(v)
		x := p * float64(len(v)-1)
		i := int(x)
		if i >= len(v)-1 {
			return v[len(v)-1]
		}
		t := x - float64(i)
		return v[i]*(1-t) + v[i+1]*t
	}
	L0, L1 = pct(ls, 0.005), pct(ls, 0.995)
	A0, A1 = pct(as, 0.005), pct(as, 0.995)
	B0, B1 = pct(bs, 0.005), pct(bs, 0.995)
	expand := func(lo, hi, lim float64) (float64, float64) {
		d := hi - lo
		if d < 1e-3 {
			d = 1
		}
		lo, hi = lo-pmBoxExpand*d, hi+pmBoxExpand*d
		return math.Max(-lim, lo), math.Min(lim, hi)
	}
	L0, L1 = expand(L0, L1, 105)
	A0, A1 = expand(A0, A1, 128)
	B0, B1 = expand(B0, B1, 128)
	return
}

// pmFade is 1 inside the box and falls smoothly to 0 at its edge: outside
// the trained range the mapping is the identity.
func pmFade(u float64) float64 {
	if u <= 0 || u >= 1 {
		return 0
	}
	s := func(t float64) float64 {
		if t < 0 {
			t = 0
		}
		if t > 1 {
			t = 1
		}
		return t * t * (3 - 2*t)
	}
	return s(u/pmFadeWidth) * s((1-u)/pmFadeWidth)
}

// pmCoords maps a sample's Lab to the unit cube.
func (g *pmGrid) pmCoords(s *pmSample, i int) (u, v, w float64) {
	u = (float64(s.Lab[i*3]) - g.L0) / (g.L1 - g.L0)
	v = (float64(s.Lab[i*3+1]) - g.A0) / (g.A1 - g.A0)
	w = (float64(s.Lab[i*3+2]) - g.B0) / (g.B1 - g.B0)
	return
}

// pmNodeWeights returns the eight node offsets (relative to the cell's base
// node) and trilinear weights for (u,v,w) inside the cube.
func (g *pmGrid) pmNodeWeights(u, v, w float64) (off [8]int, wt [8]float64) {
	f := func(x float64) (int, float64) {
		p := x * float64(g.N-1)
		i := int(math.Floor(p))
		if i < 0 {
			i = 0
		}
		if i > g.N-2 {
			i = g.N - 2
		}
		t := p - float64(i)
		if t < 0 {
			t = 0
		}
		if t > 1 {
			t = 1
		}
		return i, t
	}
	_, tx := f(u)
	_, ty := f(v)
	_, tz := f(w)
	q := 0
	for di := 0; di < 2; di++ {
		for dj := 0; dj < 2; dj++ {
			for dk := 0; dk < 2; dk++ {
				off[q] = (di*g.N+dj)*g.N + dk
				a := tx
				if di == 0 {
					a = 1 - tx
				}
				b := ty
				if dj == 0 {
					b = 1 - ty
				}
				c := tz
				if dk == 0 {
					c = 1 - tz
				}
				wt[q] = a * b * c
				q++
			}
		}
	}
	return
}

// pmCells is one photo's (or one series') binned normal equations: an 8x8
// matrix per grid cell and an 8-vector per channel.
type pmCells struct {
	N int
	a []float64 // ncells*64
	b []float64 // ncells*24
	n int       // samples used
}

func pmNewCells(n int) *pmCells {
	nc := (n - 1) * (n - 1) * (n - 1)
	return &pmCells{N: n, a: make([]float64, nc*64), b: make([]float64, nc*24)}
}

func (c *pmCells) add(o *pmCells) {
	for i := range c.a {
		c.a[i] += o.a[i]
	}
	for i := range c.b {
		c.b[i] += o.b[i]
	}
	c.n += o.n
}

// pmAccumulate bins one photo's core (unfaded) samples.  w, when not nil, is
// a per-sample weight (the IRLS step that makes the fit behave like an L1
// fit, matching the 8-bit mean absolute error it is judged by).
func (g *pmGrid) pmAccumulate(c *pmCells, s *pmSample, w []float32) {
	for i := 0; i < s.n(); i++ {
		u, v, ww := g.pmCoords(s, i)
		if u < pmFadeWidth || u > 1-pmFadeWidth || v < pmFadeWidth || v > 1-pmFadeWidth || ww < pmFadeWidth || ww > 1-pmFadeWidth {
			continue
		}
		wi := 1.0
		if w != nil {
			wi = float64(w[i])
			if wi <= 0 {
				continue
			}
		}
		_, wt := g.pmNodeWeights(u, v, ww)
		p := int(u * float64(g.N-1))
		q := int(v * float64(g.N-1))
		r := int(ww * float64(g.N-1))
		if p > g.N-2 {
			p = g.N - 2
		}
		if q > g.N-2 {
			q = g.N - 2
		}
		if r > g.N-2 {
			r = g.N - 2
		}
		ci := (p*(g.N-1) + q) * (g.N - 1)
		ci += r
		for x := 0; x < 8; x++ {
			for y := 0; y < 8; y++ {
				c.a[ci*64+x*8+y] += wi * wt[x] * wt[y]
			}
			t0 := float64(s.Targ[i*3])/65535 - float64(s.Base[i*3])/255
			t1 := float64(s.Targ[i*3+1])/65535 - float64(s.Base[i*3+1])/255
			t2 := float64(s.Targ[i*3+2])/65535 - float64(s.Base[i*3+2])/255
			c.b[ci*24+x] += wi * wt[x] * t0
			c.b[ci*24+8+x] += wi * wt[x] * t1
			c.b[ci*24+16+x] += wi * wt[x] * t2
		}
		c.n++
	}
}

// pmMat is the assembled sparse normal matrix (full, symmetric in value).
type pmMat struct {
	N        int
	rows     []int32
	cols     []int32
	vals     []float64
	diag     []float64
	diagMean float64
}

func (m *pmMat) mul(y, x []float64) {
	for i := range y {
		y[i] = 0
	}
	for r := 0; r < m.N; r++ {
		var s float64
		for k := m.rows[r]; k < m.rows[r+1]; k++ {
			s += m.vals[k] * x[m.cols[k]]
		}
		y[r] = s
	}
}

type pmKey struct {
	i, j int32
	v    float64
}

func pmAssemble(cs []*pmCells, n int) *pmMat {
	M := n * n * n
	nc := (n - 1) * (n - 1) * (n - 1)
	m := map[uint64]float64{}
	for _, c := range cs {
		for ci := 0; ci < nc; ci++ {
			p := ci / ((n - 1) * (n - 1))
			rem := ci % ((n - 1) * (n - 1))
			q := rem / (n - 1)
			base := (p*n + q) * n
			for x := 0; x < 8; x++ {
				di := (x >> 2) & 1
				dj := (x >> 1) & 1
				dk := x & 1
				ni := base + (di*n+dj)*n + dk
				for y := 0; y < 8; y++ {
					ei := (y >> 2) & 1
					ej := (y >> 1) & 1
					ek := y & 1
					nj := base + (ei*n+ej)*n + ek
					m[uint64(ni)*uint64(M)+uint64(nj)] += c.a[ci*64+x*8+y]
				}
			}
		}
	}
	keys := make([]pmKey, 0, len(m))
	for k, v := range m {
		keys = append(keys, pmKey{int32(k / uint64(M)), int32(k % uint64(M)), v})
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].i != keys[j].i {
			return keys[i].i < keys[j].i
		}
		return keys[i].j < keys[j].j
	})
	out := &pmMat{N: M, rows: make([]int32, M+1), diag: make([]float64, M)}
	for _, k := range keys {
		out.rows[k.i+1]++
		if k.i == k.j {
			out.diag[k.i] = k.v
		}
	}
	for i := 0; i < M; i++ {
		out.rows[i+1] += out.rows[i]
	}
	out.cols = make([]int32, len(keys))
	out.vals = make([]float64, len(keys))
	for i, k := range keys {
		out.cols[i], out.vals[i] = k.j, k.v
	}
	var sum float64
	var occ int
	for _, d := range out.diag {
		if d > 0 {
			sum += d
			occ++
		}
	}
	if occ > 0 {
		out.diagMean = sum / float64(occ)
	} else {
		out.diagMean = 1
	}
	return out
}

// pmRHS sums the cells' right-hand sides into 3*M.
func pmRHS(cs []*pmCells, n int) []float64 {
	M := n * n * n
	nc := (n - 1) * (n - 1) * (n - 1)
	out := make([]float64, 3*M)
	for _, c := range cs {
		for ci := 0; ci < nc; ci++ {
			p := ci / ((n - 1) * (n - 1))
			rem := ci % ((n - 1) * (n - 1))
			q := rem / (n - 1)
			base := (p*n + q) * n
			for x := 0; x < 8; x++ {
				di := (x >> 2) & 1
				dj := (x >> 1) & 1
				dk := x & 1
				ni := base + (di*n+dj)*n + dk
				for ch := 0; ch < 3; ch++ {
					out[ch*M+ni] += c.b[ci*24+ch*8+x]
				}
			}
		}
	}
	return out
}

// pmCellsMul adds sign * A_cells * x to y.
func pmCellsMul(y, x []float64, c *pmCells, sign float64) {
	n := c.N
	nc := (n - 1) * (n - 1) * (n - 1)
	for ci := 0; ci < nc; ci++ {
		p := ci / ((n - 1) * (n - 1))
		rem := ci % ((n - 1) * (n - 1))
		q := rem / (n - 1)
		base := (p*n + q) * n
		var node [8]int
		for x := 0; x < 8; x++ {
			node[x] = base + (((x>>2)&1)*n+((x>>1)&1))*n + (x & 1)
		}
		for a := 0; a < 8; a++ {
			var s float64
			for b := 0; b < 8; b++ {
				s += c.a[ci*64+a*8+b] * x[node[b]]
			}
			y[node[a]] += sign * s
		}
	}
}

// pmRegMul adds lambda * D^T D x, D the second difference along each axis.
func pmRegMul(y, x []float64, n int, lambda float64) {
	if lambda == 0 {
		return
	}
	// L axis
	for i := 1; i < n-1; i++ {
		for j := 0; j < n; j++ {
			for k := 0; k < n; k++ {
				idx := (i*n+j)*n + k
				d := x[idx-n*n] - 2*x[idx] + x[idx+n*n]
				y[idx-n*n] += lambda * d
				y[idx] -= 2 * lambda * d
				y[idx+n*n] += lambda * d
			}
		}
	}
	// a axis
	for i := 0; i < n; i++ {
		for j := 1; j < n-1; j++ {
			for k := 0; k < n; k++ {
				idx := (i*n+j)*n + k
				d := x[idx-n] - 2*x[idx] + x[idx+n]
				y[idx-n] += lambda * d
				y[idx] -= 2 * lambda * d
				y[idx+n] += lambda * d
			}
		}
	}
	// b axis
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			for k := 1; k < n-1; k++ {
				idx := (i*n+j)*n + k
				d := x[idx-1] - 2*x[idx] + x[idx+1]
				y[idx-1] += lambda * d
				y[idx] -= 2 * lambda * d
				y[idx+1] += lambda * d
			}
		}
	}
}

// pmRegDiag is the diagonal of D^T D.
func pmRegDiag(node, n int) float64 {
	i := node / (n * n)
	rem := node % (n * n)
	j := rem / n
	k := rem % n
	var d float64
	if i > 0 && i < n-1 {
		d += 6
	}
	if j > 0 && j < n-1 {
		d += 6
	}
	if k > 0 && k < n-1 {
		d += 6
	}
	return d
}

// pmSolve runs block conjugate gradient for the three channels.
func pmSolve(mat *pmMat, sub *pmCells, subSign float64, n int, l1, l2 float64, rhs, init []float64) ([]float64, int) {
	M := n * n * n
	x := make([]float64, 3*M)
	if init != nil {
		copy(x, init)
	}
	// Preconditioner: diagonal of the data term plus the two penalties.
	pc := make([]float64, M)
	if mat != nil {
		copy(pc, mat.diag)
	}
	if sub != nil {
		nc := (n - 1) * (n - 1) * (n - 1)
		for ci := 0; ci < nc; ci++ {
			p := ci / ((n - 1) * (n - 1))
			rem := ci % ((n - 1) * (n - 1))
			q := rem / (n - 1)
			base := (p*n + q) * n
			for a := 0; a < 8; a++ {
				di := (a >> 2) & 1
				dj := (a >> 1) & 1
				dk := a & 1
				pc[base+(di*n+dj)*n+dk] += subSign * sub.a[ci*64+a*8+a]
			}
		}
	}
	for i := range pc {
		pc[i] += l1 + l2*pmRegDiag(i, n)
		if pc[i] < 1e-12 {
			pc[i] = 1e-12
		}
	}
	r := make([]float64, 3*M)
	p := make([]float64, 3*M)
	z := make([]float64, 3*M)
	ap := make([]float64, M)
	apply := func(dst, src []float64) {
		if mat != nil {
			mat.mul(dst, src)
		} else {
			for i := range dst {
				dst[i] = 0
			}
		}
		if sub != nil {
			pmCellsMul(dst, src, sub, subSign)
		}
		for i := range dst {
			dst[i] += l1 * src[i]
		}
		pmRegMul(dst, src, n, l2)
	}
	bnorm := [3]float64{}
	it := 0
	for ch := 0; ch < 3; ch++ {
		off := ch * M
		b := rhs[off : off+M]

		// r = b - A x
		apply(ap, x[off:off+M])
		var bs float64
		for i := 0; i < M; i++ {
			r[off+i] = b[i] - ap[i]
			bs += b[i] * b[i]
			z[off+i] = r[off+i] / pc[i]
			p[off+i] = z[off+i]
		}
		bnorm[ch] = bs
	}
	rho := [3]float64{}
	for ch := 0; ch < 3; ch++ {
		for i := 0; i < M; i++ {
			rho[ch] += r[ch*M+i] * z[ch*M+i]
		}
	}
	for it = 0; it < pmCGMaxIter; it++ {
		conv := true
		for ch := 0; ch < 3; ch++ {
			off := ch * M
			apply(ap, p[off:off+M])
			var pap float64
			for i := 0; i < M; i++ {
				pap += p[off+i] * ap[i]
			}
			if pap == 0 || rho[ch] == 0 {
				continue
			}
			alpha := rho[ch] / pap
			for i := 0; i < M; i++ {
				x[off+i] += alpha * p[off+i]
			}
			// Recompute the residual from the recurrence, then its norm.
			var rs float64
			for i := 0; i < M; i++ {
				r[off+i] -= alpha * ap[i]
				rs += r[off+i] * r[off+i]
			}
			if bnorm[ch] == 0 {
				if rs > 1e-18 {
					conv = false
				}
			} else if rs/bnorm[ch] > pmCGTol*pmCGTol {
				conv = false
			}
		}
		if conv {
			it++
			break
		}
		for ch := 0; ch < 3; ch++ {
			off := ch * M
			var rn float64
			for i := 0; i < M; i++ {
				z[off+i] = r[off+i] / pc[i]
				rn += r[off+i] * z[off+i]
			}
			beta := 0.0
			if rho[ch] != 0 {
				beta = rn / rho[ch]
			}
			for i := 0; i < M; i++ {
				p[off+i] = z[off+i] + beta*p[off+i]
			}
			rho[ch] = rn
		}
	}
	return x, it
}

// pmDelta is theta's additive encoded-sRGB delta at sample i (already faded).
func (g *pmGrid) pmDelta(theta []float64, s *pmSample, i int) (d [3]float64, fade float64) {
	u, v, w := g.pmCoords(s, i)
	fade = pmFade(u) * pmFade(v) * pmFade(w)
	if fade <= 0 {
		return
	}
	M := g.M
	off, wt := g.pmNodeWeights(u, v, w)
	p := int(u * float64(g.N-1))
	q := int(v * float64(g.N-1))
	if p > g.N-2 {
		p = g.N - 2
	}
	if q > g.N-2 {
		q = g.N - 2
	}
	base := (p*g.N + q) * g.N
	for x := 0; x < 8; x++ {
		n := base + off[x]
		d[0] += wt[x] * theta[n]
		d[1] += wt[x] * theta[M+n]
		d[2] += wt[x] * theta[2*M+n]
	}
	for c := 0; c < 3; c++ {
		d[c] *= fade
	}
	return
}

// pmScore is the mean absolute 8-bit error of theta applied to s.
func pmScore(theta []float64, g *pmGrid, s *pmSample) float64 {
	var e float64
	for i := 0; i < s.n(); i++ {
		d, _ := g.pmDelta(theta, s, i)
		for c := 0; c < 3; c++ {
			e0 := float64(s.Base[i*3+c]) / 255
			e1 := float64(s.Targ[i*3+c]) / 65535
			e += math.Abs(pmClamp01(e0+d[c]) - e1)
		}
	}
	if s.n() == 0 {
		return 0
	}
	return e / float64(3*s.n()) * 255
}

// pmWeights is one IRLS step towards an L1 fit: samples with a small residual
// under theta count more, large outliers less, so the next least-squares fit
// follows the median error rather than the squared one.
func pmWeights(theta []float64, g *pmGrid, s *pmSample) []float32 {
	w := make([]float32, s.n())
	for i := 0; i < s.n(); i++ {
		d, _ := g.pmDelta(theta, s, i)
		var r float64
		for c := 0; c < 3; c++ {
			e0 := float64(s.Base[i*3+c]) / 255
			e1 := float64(s.Targ[i*3+c]) / 65535
			r = math.Max(r, math.Abs(pmClamp01(e0+d[c])-e1))
		}
		q := 1 / math.Max(r, 0.008)
		q = math.Max(0.1, math.Min(10, q))
		w[i] = float32(q)
	}
	return w
}
