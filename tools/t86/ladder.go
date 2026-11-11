package main

// The decomposition ladder and the gain predictors.

import (
	"math"
	"runtime"
	"sync"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/render"
)

// Rungs is one photo's error at every step of the breakdown (8-bit mean
// error, the same measure calib.DiagnoseRaw prints).
type Rungs struct {
	// E0 is the held-out base: a matrix and curves fitted on the other
	// photos. EExp adds one exposure scalar; EGains the photo's own three
	// channel gains; EMatrix its own 3x3 with the group's curves; ECurves the
	// group's matrix with its own curves; EOwn both (its own fit).
	E0, EExp, EGains, EMatrix, ECurves, EOwn float64
	// EOwnCalib is calib.DiagnoseRaw's own fit, for comparison: its matrix is
	// a linear least-squares estimate, not an error-minimising one, and it is
	// worse than the held-out base on many photos.
	EOwnCalib float64
	// Gains are the ideal per-channel gains relative to the held-out base
	// (R,G,B); LG is [G, R-G, B-G] in stops, as DiagnoseRaw prints.
	Gains [3]float64
	LG    [3]float64
}

// Fold is the held-out base for one photo and the gains it leaves.
type Fold struct {
	Base  render.RawCorrection
	Gains [3]float64
	LG    [3]float64
	E0    float64
	E2    float64
}

// rawPair is p as calib's fitter wants it.
func rawPair(p *Photo) calib.RawPair {
	return calib.RawPair{Name: p.Name, Raw: p.Raw, SPP: p.SPP}
}

// applyCorrection applies c to a copy of l (calib's applied, which is
// unexported).
func applyCorrection(c *render.RawCorrection, l *render.Linear) *render.Linear {
	o := *l
	o.Pix = append([]float32(nil), l.Pix...)
	c.Apply(&o)
	return &o
}

// ownFit is the best global matrix + curves for one photo alone: calib's
// matrix-from-linear-LS fit, then alternating least squares that fits the
// matrix to the pre-curve values its own curves imply. The second part
// matters because calib's own fit can be worse than the group base: its
// matrix is a linear least-squares estimate, while the error is measured in
// sRGB (34 of 60 Quattro photos came out worse; see Table 1's calib column).
func ownFit(p *Photo) render.RawCorrection {
	c := calib.FitMapping([]calib.RawPair{rawPair(p)})
	best := calib.MeanError(applyCorrection(&c, p.Raw), p.SPP)
	for it := 0; it < 4; it++ {
		m := matrixForCurves(p.Raw, p.SPP, c.Curves)
		nc := render.RawCorrection{Matrix: m, Curves: fitCurves(p.Raw, p.SPP, m)}
		e := calib.MeanError(applyCorrection(&nc, p.Raw), p.SPP)
		if e >= best-1e-4 {
			break
		}
		c, best = nc, e
	}
	return c
}

// matrixForCurves is the 3x3 that, followed by the given tone curves, best
// matches spp: the tone curves are inverted to their input values, and the
// matrix is the linear least-squares map from raw to those values.
func matrixForCurves(raw, spp *render.Linear, curves [3][64]float64) [9]float64 {
	var inv [3][256]float64
	for k := 0; k < 3; k++ {
		for i := 0; i < 256; i++ {
			inv[k][i] = invCurve(curves[k], float64(i)/255)
		}
	}
	var ata, atb [3][3]float64
	for i := 0; i < len(spp.Pix); i += 3 {
		if max(spp.Pix[i], spp.Pix[i+1], spp.Pix[i+2]) > 0.95 {
			continue
		}
		var x, y [3]float64
		for k := 0; k < 3; k++ {
			x[k] = float64(raw.Pix[i+k])
			lv := min(255, max(0, int(math.Round(float64(render.ToSRGB(spp.Pix[i+k]))*255))))
			y[k] = float64(render.ToLinear(float32(inv[k][lv])))
		}
		for r := 0; r < 3; r++ {
			for q := 0; q < 3; q++ {
				ata[r][q] += x[r] * x[q]
				atb[r][q] += y[r] * x[q]
			}
		}
	}
	invA, ok := inverse3(ata)
	if !ok {
		return [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	}
	var m [9]float64
	for r := 0; r < 3; r++ {
		for q := 0; q < 3; q++ {
			for k := 0; k < 3; k++ {
				m[r*3+q] += atb[r][k] * invA[k][q]
			}
		}
	}
	return m
}

// invCurve maps an sRGB output level back through a fitted tone curve to the
// input sRGB level; outside the fitted range it clamps to black/white.
func invCurve(curve [64]float64, v float64) float64 {
	type pt struct{ x, y float64 }
	var ps []pt
	for b := 0; b < 64; b++ {
		if curve[b] < 0 {
			continue
		}
		ps = append(ps, pt{(float64(b) + 0.5) / 64, curve[b]})
	}
	if len(ps) == 0 {
		return v
	}
	if v <= ps[0].y {
		return ps[0].x
	}
	if v >= ps[len(ps)-1].y {
		return ps[len(ps)-1].x
	}
	for i := 0; i+1 < len(ps); i++ {
		a, b := ps[i], ps[i+1]
		if v >= a.y && v <= b.y {
			if b.y == a.y {
				return a.x
			}
			return a.x + (v-a.y)/(b.y-a.y)*(b.x-a.x)
		}
	}
	return v
}

func inverse3(m [3][3]float64) ([3][3]float64, bool) {
	det := m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
	if det == 0 {
		return m, false
	}
	var r [3][3]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			i1, i2, j1, j2 := (j+1)%3, (j+2)%3, (i+1)%3, (i+2)%3
			r[i][j] = (m[i1][j1]*m[i2][j2] - m[i1][j2]*m[i2][j1]) / det
		}
	}
	return r, true
}

// fitCurves fits the per-channel tone curves for this photo given matrix m,
// exactly as calib's fitRaw does after its matrix.
func fitCurves(raw, spp *render.Linear, m [9]float64) [3][64]float64 {
	var sum, cnt [3][64]float64
	for i := 0; i < len(spp.Pix); i += 3 {
		r, g, b := float64(raw.Pix[i]), float64(raw.Pix[i+1]), float64(raw.Pix[i+2])
		v := [3]float64{m[0]*r + m[1]*g + m[2]*b, m[3]*r + m[4]*g + m[5]*b, m[6]*r + m[7]*g + m[8]*b}
		for k := 0; k < 3; k++ {
			if v[k] <= 0 {
				continue
			}
			e := float64(render.ToSRGB(float32(math.Max(0, math.Min(1, v[k])))))
			bin := min(63, int(e*64))
			sum[k][bin] += float64(render.ToSRGB(spp.Pix[i+k]))
			cnt[k][bin]++
		}
	}
	var c render.RawCorrection
	c.Matrix = m
	for k := 0; k < 3; k++ {
		for b := 0; b < 64; b++ {
			c.Curves[k][b] = -1
			if cnt[k][b] > 50 {
				c.Curves[k][b] = sum[k][b] / cnt[k][b]
			}
		}
	}
	c.MakeMonotone()
	return c.Curves
}

// unclippedMask is the pixels the gains are fitted on: the same set
// DiagnoseRaw uses (no SPP channel above 0.95).
func channelGains(pred, spp *render.Linear) [3]float64 {
	var a, b [3]float64
	for i := 0; i < len(pred.Pix); i += 3 {
		if max(spp.Pix[i], spp.Pix[i+1], spp.Pix[i+2]) > 0.95 {
			continue
		}
		for k := 0; k < 3; k++ {
			a[k] += float64(pred.Pix[i+k])
			b[k] += float64(spp.Pix[i+k])
		}
	}
	var g [3]float64
	for k := 0; k < 3; k++ {
		if a[k] > 0 {
			g[k] = b[k] / a[k]
		} else {
			g[k] = 1
		}
		if math.IsNaN(g[k]) || math.IsInf(g[k], 0) || g[k] <= 0 {
			g[k] = 1
		}
	}
	return g
}

// exposureGain is the one scalar that puts pred's mean luminance on SPP's.
func exposureGain(pred, spp *render.Linear) float64 {
	var a, b float64
	for i := 0; i < len(pred.Pix); i += 3 {
		if max(spp.Pix[i], spp.Pix[i+1], spp.Pix[i+2]) > 0.95 {
			continue
		}
		a += float64(0.2126*pred.Pix[i] + 0.7152*pred.Pix[i+1] + 0.0722*pred.Pix[i+2])
		b += float64(0.2126*spp.Pix[i] + 0.7152*spp.Pix[i+1] + 0.0722*spp.Pix[i+2])
	}
	if a <= 0 {
		return 1
	}
	return b / a
}

// scaleImage multiplies each channel by g (clamped to 1, as DiagnoseRaw's
// gain step does).
func scaleImage(l *render.Linear, g [3]float64) *render.Linear {
	o := &render.Linear{W: l.W, H: l.H, Pix: make([]float32, len(l.Pix)), Scale: l.Scale, Frame: l.Frame, ISO: l.ISO, Sensor: l.Sensor}
	for i := 0; i < len(l.Pix); i += 3 {
		for k := 0; k < 3; k++ {
			o.Pix[i+k] = float32(math.Min(1, float64(l.Pix[i+k])*g[k]))
		}
	}
	return o
}

func lgains(g [3]float64) [3]float64 {
	l := func(v float64) float64 { return math.Log2(v) }
	return [3]float64{l(g[1]), l(g[0]) - l(g[1]), l(g[2]) - l(g[1])}
}

// buildFolds fits each photo's held-out base and measures what its ideal
// gains leave. This is the expensive pass: one fit per photo over the other
// 59. The fits are independent and read the pairs read-only, so they run in
// parallel (one goroutine per core).
func buildFolds(photos []*Photo, logf func(string, ...any)) []Fold {
	folds := make([]Fold, len(photos))
	sem := make(chan struct{}, max(1, runtime.GOMAXPROCS(0)))
	var wg sync.WaitGroup
	done := make(chan int, len(photos))
	for h := range photos {
		wg.Add(1)
		go func(h int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			train := make([]calib.RawPair, 0, len(photos)-1)
			for i := range photos {
				if i != h {
					train = append(train, rawPair(photos[i]))
				}
			}
			folds[h].Base = calib.FitMapping(train)
			pred := applyCorrection(&folds[h].Base, photos[h].Raw)
			folds[h].E0 = calib.MeanError(pred, photos[h].SPP)
			g := channelGains(pred, photos[h].SPP)
			folds[h].Gains = g
			folds[h].LG = lgains(g)
			folds[h].E2 = calib.MeanError(scaleImage(pred, g), photos[h].SPP)
			done <- h
		}(h)
	}
	go func() { wg.Wait(); close(done) }()
	n := 0
	for range done {
		n++
		if n%10 == 0 || n == len(photos) {
			logf("  folds %d/%d", n, len(photos))
		}
	}
	return folds
}

// ladder computes every rung for one photo against its held-out base. own
// may be nil (then it is fitted here).
func ladder(p *Photo, f Fold, own *render.RawCorrection) Rungs {
	var r Rungs
	pred := applyCorrection(&f.Base, p.Raw)
	r.E0 = calib.MeanError(pred, p.SPP)
	eg := exposureGain(pred, p.SPP)
	r.EExp = calib.MeanError(scaleImage(pred, [3]float64{eg, eg, eg}), p.SPP)
	g := channelGains(pred, p.SPP)
	r.Gains = g
	r.LG = lgains(g)
	r.EGains = calib.MeanError(scaleImage(pred, g), p.SPP)
	var o render.RawCorrection
	if own != nil {
		o = *own
	} else {
		o = ownFit(p)
	}
	mat := render.RawCorrection{Matrix: matrixForCurves(p.Raw, p.SPP, f.Base.Curves), Curves: f.Base.Curves}
	r.EMatrix = calib.MeanError(applyCorrection(&mat, p.Raw), p.SPP)
	cur := render.RawCorrection{Matrix: f.Base.Matrix, Curves: fitCurves(p.Raw, p.SPP, f.Base.Matrix)}
	r.ECurves = calib.MeanError(applyCorrection(&cur, p.Raw), p.SPP)
	r.EOwn = calib.MeanError(applyCorrection(&o, p.Raw), p.SPP)
	co := calib.FitMapping([]calib.RawPair{rawPair(p)})
	r.EOwnCalib = calib.MeanError(applyCorrection(&co, p.Raw), p.SPP)
	return r
}

// errorMap is per-pixel mean abs 8-bit sRGB difference, the same measure as
// calib.MeanError summed per pixel.
func errorMap(a, b *render.Linear) []float64 {
	n := min(len(a.Pix), len(b.Pix)) / 3
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		var e float64
		for k := 0; k < 3; k++ {
			e += math.Abs(float64(render.ToSRGB(a.Pix[i*3+k]) - render.ToSRGB(b.Pix[i*3+k])))
		}
		out[i] = e / 3 * 255
	}
	return out
}

// ToneBins is the error split by SPP luminance.
type ToneBins struct {
	Share [3]float64 // shadow, mid, highlight
	Err   [3]float64
}

func toneBins(own, spp *render.Linear) ToneBins {
	var t ToneBins
	var n [3]int
	for i := 0; i < len(spp.Pix); i += 3 {
		y := 0.2126*spp.Pix[i] + 0.7152*spp.Pix[i+1] + 0.0722*spp.Pix[i+2]
		b := 1
		if y < 0.06 {
			b = 0
		} else if y > 0.6 {
			b = 2
		}
		var e float64
		for k := 0; k < 3; k++ {
			e += math.Abs(float64(render.ToSRGB(own.Pix[i+k]) - render.ToSRGB(spp.Pix[i+k])))
		}
		t.Err[b] += e / 3 * 255
		n[b]++
	}
	tot := float64(n[0] + n[1] + n[2])
	for b := 0; b < 3; b++ {
		if n[b] > 0 {
			t.Err[b] /= float64(n[b])
			t.Share[b] = float64(n[b]) / tot
		}
	}
	return t
}

// boxDown averages f×f blocks (f divides W and H in this tool's frames).
func boxDown(l *render.Linear, f int) *render.Linear {
	w, h := l.W/f, l.H/f
	if w < 1 || h < 1 {
		return l
	}
	o := &render.Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s [3]float32
			for yy := y * f; yy < y*f+f; yy++ {
				for xx := x * f; xx < x*f+f; xx++ {
					i := (yy*l.W + xx) * 3
					for k := 0; k < 3; k++ {
						s[k] += l.Pix[i+k]
					}
				}
			}
			j := (y*w + x) * 3
			for k := 0; k < 3; k++ {
				o.Pix[j+k] = s[k] / float32(f*f)
			}
		}
	}
	return o
}

// Grid is a 4×4 mean error map of the frame.
type Grid [4][4]float64

func gridOf(em []float64, w, h int) Grid {
	var g Grid
	var n [4][4]int
	for y := 0; y < h; y++ {
		gy := min(3, y*4/h)
		for x := 0; x < w; x++ {
			gx := min(3, x*4/w)
			g[gy][gx] += em[y*w+x]
			n[gy][gx]++
		}
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if n[y][x] > 0 {
				g[y][x] /= float64(n[y][x])
			}
		}
	}
	return g
}

// ---------- predictors ----------

// LinModel is a linear model of one photo's [G, R-G, B-G] in stops from a
// design vector.
type LinModel struct{ Coef [3][]float64 }

// FitModel is ridge least squares per target.
func FitModel(x [][]float64, y [][3]float64, ridge float64) LinModel {
	if len(x) == 0 {
		return LinModel{}
	}
	k := len(x[0])
	var ata [][]float64
	var atb [3][]float64
	for r := 0; r < k; r++ {
		ata = append(ata, make([]float64, k))
	}
	for t := 0; t < 3; t++ {
		atb[t] = make([]float64, k)
	}
	for i := range x {
		for r := 0; r < k; r++ {
			xr := num(x[i][r])
			for c := 0; c < k; c++ {
				ata[r][c] += xr * num(x[i][c])
			}
			for t := 0; t < 3; t++ {
				atb[t][r] += xr * num(y[i][t])
			}
		}
	}
	for r := 1; r < k; r++ {
		ata[r][r] += ridge
	}
	var m LinModel
	for t := 0; t < 3; t++ {
		m.Coef[t] = solveDense(ata, atb[t])
	}
	return m
}

// num maps NaN/Inf to 0, so one bad feature cannot poison a whole model.
func num(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// solveDense is Gaussian elimination with partial pivoting.
func solveDense(a [][]float64, b []float64) []float64 {
	n := len(b)
	m := make([][]float64, n)
	for i := range m {
		m[i] = append([]float64(nil), a[i]...)
	}
	x := append([]float64(nil), b...)
	for c := 0; c < n; c++ {
		p := c
		for r := c + 1; r < n; r++ {
			if math.Abs(m[r][c]) > math.Abs(m[p][c]) {
				p = r
			}
		}
		m[c], m[p] = m[p], m[c]
		x[c], x[p] = x[p], x[c]
		if math.Abs(m[c][c]) < 1e-12 {
			continue
		}
		for r := c + 1; r < n; r++ {
			f := m[r][c] / m[c][c]
			for k := c; k < n; k++ {
				m[r][k] -= f * m[c][k]
			}
			x[r] -= f * x[c]
		}
	}
	out := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		if math.Abs(m[r][r]) < 1e-12 {
			continue
		}
		s := x[r]
		for k := r + 1; k < n; k++ {
			s -= m[r][k] * out[k]
		}
		out[r] = s / m[r][r]
	}
	return out
}

// Predict returns the predicted [G, R-G, B-G] in stops.
func (m LinModel) Predict(x []float64) [3]float64 {
	var o [3]float64
	for t := 0; t < 3; t++ {
		var s float64
		for k := range x {
			if k < len(m.Coef[t]) {
				s += m.Coef[t][k] * x[k]
			}
		}
		o[t] = s
	}
	return o
}

// applyLGains applies predicted [G, R-G, B-G] stops to pred.
func applyLGains(pred *render.Linear, lg [3]float64) *render.Linear {
	clamp := func(v float64) float64 { return math.Max(-2, math.Min(2, num(v))) }
	g := [3]float64{
		math.Exp2(clamp(lg[0] + clamp(lg[1]))),
		math.Exp2(clamp(lg[0])),
		math.Exp2(clamp(lg[0] + clamp(lg[2]))),
	}
	return scaleImage(pred, g)
}

// features builds the design vectors for every named predictor. cat holds
// the category lists (WB, body) so one-hot columns are stable.
func features(photos []*Photo) map[string][][]float64 {
	wbs, bodies := []string{}, []string{}
	seenWB, seenB := map[string]bool{}, map[string]bool{}
	for _, p := range photos {
		if !seenWB[p.WB] {
			seenWB[p.WB] = true
			wbs = append(wbs, p.WB)
		}
		if !seenB[p.Body] {
			seenB[p.Body] = true
			bodies = append(bodies, p.Body)
		}
	}
	// deterministic order
	sortStrings(wbs)
	sortStrings(bodies)

	oneHot := func(v string, cats []string) []float64 {
		out := make([]float64, len(cats)-1)
		for i := 1; i < len(cats); i++ {
			if cats[i] == v {
				out[i-1] = 1
			}
		}
		return out
	}
	meanCol := func(p *Photo) (float64, float64) {
		var s [3]float64
		for i := 0; i < len(p.Raw.Pix); i += 3 {
			for k := 0; k < 3; k++ {
				s[k] += float64(p.Raw.Pix[i+k])
			}
		}
		if s[1] <= 0 || s[0] <= 0 || s[2] <= 0 {
			return 0, 0
		}
		return math.Log(s[0] / s[1]), math.Log(s[2] / s[1])
	}
	brightCol := func(p *Photo) (float64, float64) {
		n := len(p.Raw.Pix) / 3
		// The brightest 1% by luminance: find a threshold with a 256-bin
		// histogram, then the mean colour above it.
		var hist [256]int
		for i := 0; i < len(p.Raw.Pix); i += 3 {
			y := 0.2126*p.Raw.Pix[i] + 0.7152*p.Raw.Pix[i+1] + 0.0722*p.Raw.Pix[i+2]
			hist[min(255, int(float64(render.ToSRGB(float32(math.Min(1, float64(y)))))*256))]++
		}
		want := n / 100
		acc, thr := 0, 255
		for b := 255; b >= 0; b-- {
			acc += hist[b]
			if acc >= want {
				thr = b
				break
			}
		}
		var s [3]float64
		var c int
		for i := 0; i < len(p.Raw.Pix); i += 3 {
			y := 0.2126*p.Raw.Pix[i] + 0.7152*p.Raw.Pix[i+1] + 0.0722*p.Raw.Pix[i+2]
			b := min(255, int(float64(render.ToSRGB(float32(math.Min(1, float64(y)))))*256))
			if b < thr {
				continue
			}
			for k := 0; k < 3; k++ {
				s[k] += float64(p.Raw.Pix[i+k])
			}
			c++
		}
		if c == 0 || s[0] <= 0 || s[1] <= 0 || s[2] <= 0 {
			return 0, 0
		}
		return math.Log(s[0] / s[1]), math.Log(s[2] / s[1])
	}
	out := map[string][][]float64{}
	for i, p := range photos {
		mr, mb := meanCol(p)
		br, bb := brightCol(p)
		ar, ab := 0.0, 0.0
		if p.AutoGain[0] > 0 && p.AutoGain[1] > 0 && p.AutoGain[2] > 0 {
			ar = math.Log2(p.AutoGain[0] / p.AutoGain[1])
			ab = math.Log2(p.AutoGain[2] / p.AutoGain[1])
		}
		rr, rb := 0.0, 0.0
		if p.WBManual && p.ManualGain[0] > 0 && p.AutoGain[0] > 0 {
			rr = math.Log2(p.ManualGain[0] / p.AutoGain[0])
			rb = math.Log2(p.ManualGain[2] / p.AutoGain[2])
		}
		expn := 0.0
		if p.HasExpNet {
			expn = p.ExpNet
		}
		iso := math.Log2(math.Max(1, p.ISO))
		base := []float64{1}
		add := func(f ...float64) []float64 { return append(append([]float64(nil), base...), f...) }
		put := func(name string, f []float64) {
			if out[name] == nil {
				out[name] = make([][]float64, len(photos))
			}
			out[name][i] = f
		}
		put("mean gains (no input)", []float64{1})
		put("body", append([]float64{1}, oneHot(p.Body, bodies)...))
		put("WB setting", append([]float64{1}, oneHot(p.WB, wbs)...))
		put("ISO", add(iso))
		put("EXPCOMP", add(p.ExpComp))
		put("EXPNET", add(expn))
		put("EXPCOMP+EXPNET", add(p.ExpComp, expn))
		put("CAMF Auto gains", add(ar, ab))
		put("CAMF recorded vs Auto", add(rr, rb))
		put("mean colour", add(mr, mb))
		put("grey world", []float64{1, -mr, -mb})
		put("brightest 1%", add(br, bb))
		put("ISO+EXPCOMP+EXPNET", add(iso, p.ExpComp, expn))
		put("colour+body", append([]float64{1, mr, mb}, oneHot(p.Body, bodies)...))
		all := append([]float64{1, iso, p.ExpComp, expn, ar, ab, mr, mb, br, bb}, oneHot(p.Body, bodies)...)
		put("all", all)
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
