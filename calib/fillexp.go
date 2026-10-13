package calib

// Fill Light research: which surround scales describe SPP's Fill Light
// best? Each candidate is fitted per photo (the same per-brightness-bin
// least squares as fitFillCoef, with any number of scales), averaged over
// the OTHER photos of the camera family, and scored on the held-out photo:
// mean |brightness error| (0-255) against SPP's Fill Light export.
//
// The CAMF of every X3F names SPP's Fill Light parameters (FIR_*,
// LocalMean*): local means at proportions 0.0011 / 0.0019 / 0.019, an
// edge reference and a sigmoid — a Retinex-style operator. These
// experiments test those scales against the ones the app uses.

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/biopoetic/foveon-lab/render"
)

// FillCandidate is one surround configuration.
type FillCandidate struct {
	Name  string
	Fracs []float64 // scales as fractions of the image width
	Blur  string    // "box" (the app's Surround) or "gauss"
	// Square adds d·|d| per scale (a non-linear response to the surround).
	Square bool
}

type fillPhoto struct {
	name, family string
	ref          *render.Linear
	outs         map[float64]*render.Linear // Fill Light value → SPP export
}

// FillResearch runs every candidate over the Fill Light exports in dir.
// family maps an export's photo name to its camera family.
func FillResearch(dir string, family func(name string) string, cands []FillCandidate, w io.Writer) error {
	codes := map[string]float64{}
	for _, s := range Steps() {
		if s.Param == "FillLight" {
			codes[s.Code] = s.Value
		}
	}
	refs, _ := filepath.Glob(filepath.Join(dir, ExportsDir, "*_"+Reference+".tif"))
	var photos []*fillPhoto
	for _, r := range refs {
		name := strings.TrimSuffix(filepath.Base(r), "_"+Reference+".tif")
		fam := family(name)
		if fam == "" {
			continue
		}
		p := &fillPhoto{name: name, family: fam, outs: map[float64]*render.Linear{}}
		for code, v := range codes {
			path := filepath.Join(dir, ExportsDir, name+"_"+code+".tif")
			out, err := load(path)
			if err != nil {
				continue
			}
			p.outs[v] = out
		}
		if len(p.outs) == 0 {
			continue
		}
		if p.ref, _ = load(r); p.ref == nil {
			continue
		}
		photos = append(photos, p)
	}
	sort.Slice(photos, func(i, j int) bool { return photos[i].name < photos[j].name })
	fmt.Fprintf(w, "%d photos with Fill Light exports\n", len(photos))

	for _, fam := range []string{"SD1", "SD15"} {
		var ps []*fillPhoto
		for _, p := range photos {
			if p.family == fam {
				ps = append(ps, p)
			}
		}
		if len(ps) < 3 {
			continue
		}
		vals := map[float64]int{}
		for _, p := range ps {
			for v := range p.outs {
				vals[v]++
			}
		}
		var vs []float64
		for v, n := range vals {
			if n >= 3 {
				vs = append(vs, v)
			}
		}
		sort.Float64s(vs)
		fmt.Fprintf(w, "\n%s: %d photos. Held-out brightness error (0-255) per Fill Light value:\n", fam, len(ps))
		fmt.Fprintf(w, "%-34s", "candidate")
		for _, v := range vs {
			fmt.Fprintf(w, " %7s", fmt.Sprintf("%+.2g", v))
		}
		fmt.Fprintf(w, " %7s\n", "mean")
		for _, c := range cands {
			errs := fillHeldOut(ps, vs, c)
			fmt.Fprintf(w, "%-34s", c.Name)
			var sum float64
			for _, e := range errs {
				fmt.Fprintf(w, " %7.2f", e)
				sum += e
			}
			fmt.Fprintf(w, " %7.2f\n", sum/float64(len(errs)))
		}
	}
	return nil
}

// fillFeatures returns, per pixel, the perceptual brightness v and the
// differences (surround − v) for each scale.
func fillFeatures(l *render.Linear, c FillCandidate) (v []float32, d [][]float32) {
	n := l.W * l.H
	v = make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = render.ToSRGB(luma(l.Pix, i*3))
	}
	for _, f := range c.Fracs {
		s := make([]float32, n)
		switch {
		case strings.HasPrefix(c.Blur, "guided"):
			var eps float64
			fmt.Sscanf(c.Blur, "guided%g", &eps)
			s = guidedFilter(v, l.W, l.H, f*float64(l.W), eps)
		case c.Blur == "gauss":
			s = gaussBlur(v, l.W, l.H, f*float64(l.W))
		default:
			sr := render.NewSurround(l, f)
			for y := 0; y < l.H; y++ {
				for x := 0; x < l.W; x++ {
					s[y*l.W+x] = sr.At(x, y)
				}
			}
		}
		for i := range s {
			s[i] -= v[i]
		}
		d = append(d, s)
	}
	if c.Square {
		for _, s := range d[:len(c.Fracs)] {
			q := make([]float32, len(s))
			for i, x := range s {
				q[i] = x * float32(math.Abs(float64(x)))
			}
			d = append(d, q)
		}
	}
	return v, d
}

// gaussBlur approximates a Gaussian of the given sigma (pixels) with three
// box blurs per axis.
func gaussBlur(src []float32, w, h int, sigma float64) []float32 {
	r := int(math.Max(1, math.Round(sigma*math.Sqrt(12.0/3+1)/2-0.5)))
	a := append([]float32(nil), src...)
	b := make([]float32, len(src))
	for pass := 0; pass < 3; pass++ {
		boxH(a, b, w, h, r)
		boxV(b, a, w, h, r)
	}
	return a
}

// guidedFilter is an edge-aware local mean (He et al.): within a window of
// Gaussian sigma, the output follows v where it varies more than sqrt(eps)
// (an edge) and the local mean where it is flat.
func guidedFilter(v []float32, w, h int, sigma, eps float64) []float32 {
	vv := make([]float32, len(v))
	for i, x := range v {
		vv[i] = x * x
	}
	m := gaussBlur(v, w, h, sigma)
	m2 := gaussBlur(vv, w, h, sigma)
	a := make([]float32, len(v))
	b := make([]float32, len(v))
	for i := range v {
		vr := m2[i] - m[i]*m[i]
		a[i] = vr / (vr + float32(eps))
		b[i] = m[i] - a[i]*m[i]
	}
	ma := gaussBlur(a, w, h, sigma)
	mb := gaussBlur(b, w, h, sigma)
	out := make([]float32, len(v))
	for i := range v {
		out[i] = ma[i]*v[i] + mb[i]
	}
	return out
}

func boxH(src, dst []float32, w, h, r int) {
	for y := 0; y < h; y++ {
		row := src[y*w : (y+1)*w]
		var sum float32
		cnt := 0
		for x := 0; x <= min(r, w-1); x++ {
			sum += row[x]
			cnt++
		}
		for x := 0; x < w; x++ {
			dst[y*w+x] = sum / float32(cnt)
			if x+r+1 < w {
				sum += row[x+r+1]
				cnt++
			}
			if x-r >= 0 {
				sum -= row[x-r]
				cnt--
			}
		}
	}
}

func boxV(src, dst []float32, w, h, r int) {
	for x := 0; x < w; x++ {
		var sum float32
		cnt := 0
		for y := 0; y <= min(r, h-1); y++ {
			sum += src[y*w+x]
			cnt++
		}
		for y := 0; y < h; y++ {
			dst[y*w+x] = sum / float32(cnt)
			if y+r+1 < h {
				sum += src[(y+r+1)*w+x]
				cnt++
			}
			if y-r >= 0 {
				sum -= src[(y-r)*w+x]
				cnt--
			}
		}
	}
}

// fillFit fits, per brightness bin, shift = c0 + Σ ci·d_i.
func fillFit(v, o []float32, d [][]float32) [][]float64 {
	k := len(d) + 1
	coef := make([][]float64, render.FillBins)
	ata := make([][]float64, render.FillBins)
	atb := make([][]float64, render.FillBins)
	for b := range ata {
		ata[b] = make([]float64, k*k)
		atb[b] = make([]float64, k)
	}
	f := make([]float64, k)
	for i := range v {
		b := min(render.FillBins-1, int(v[i]*render.FillBins))
		f[0] = 1
		for j := range d {
			f[j+1] = float64(d[j][i])
		}
		y := float64(o[i] - v[i])
		for r := 0; r < k; r++ {
			atb[b][r] += f[r] * y
			for c := 0; c < k; c++ {
				ata[b][r*k+c] += f[r] * f[c]
			}
		}
	}
	var last []float64
	for b := 0; b < render.FillBins; b++ {
		if ata[b][0] < 50 {
			coef[b] = last
			continue
		}
		a := append([]float64(nil), ata[b]...)
		for j := 0; j < k; j++ {
			a[j*k+j] += 1e-4 * ata[b][0]
		}
		coef[b] = solve(a, append([]float64(nil), atb[b]...), k)
		last = coef[b]
	}
	for b := render.FillBins - 1; b >= 0; b-- { // leading empty bins
		if coef[b] == nil && b+1 < render.FillBins {
			coef[b] = coef[b+1]
		}
	}
	return coef
}

func solve(a, b []float64, n int) []float64 {
	for c := 0; c < n; c++ {
		p := c
		for r := c + 1; r < n; r++ {
			if math.Abs(a[r*n+c]) > math.Abs(a[p*n+c]) {
				p = r
			}
		}
		for j := 0; j < n; j++ {
			a[c*n+j], a[p*n+j] = a[p*n+j], a[c*n+j]
		}
		b[c], b[p] = b[p], b[c]
		for r := c + 1; r < n; r++ {
			m := a[r*n+c] / a[c*n+c]
			for j := c; j < n; j++ {
				a[r*n+j] -= m * a[c*n+j]
			}
			b[r] -= m * b[c]
		}
	}
	x := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		s := b[r]
		for j := r + 1; j < n; j++ {
			s -= a[r*n+j] * x[j]
		}
		x[r] = s / a[r*n+r]
	}
	return x
}

// FillInSample scores each photo with its OWN fit (the model-shape
// ceiling) instead of the others' average.
var FillInSample = os.Getenv("FILL_INSAMPLE") != ""

// fillHeldOut is the mean held-out error per Fill Light value.
func fillHeldOut(ps []*fillPhoto, vs []float64, c FillCandidate) []float64 {
	type feat struct {
		v []float32
		d [][]float32
	}
	feats := make([]feat, len(ps))
	parallelEach(len(ps), func(i int) {
		feats[i].v, feats[i].d = fillFeatures(ps[i].ref, c)
	})
	var res []float64
	for _, val := range vs {
		coefs := make([][][]float64, len(ps))
		outs := make([][]float32, len(ps))
		parallelEach(len(ps), func(i int) {
			out := ps[i].outs[val]
			if out == nil || out.W != ps[i].ref.W || out.H != ps[i].ref.H {
				return
			}
			o := make([]float32, len(feats[i].v))
			for j := range o {
				o[j] = render.ToSRGB(luma(out.Pix, j*3))
			}
			outs[i] = o
			coefs[i] = fillFit(feats[i].v, o, feats[i].d)
		})
		var sum float64
		var n int
		for i := range ps {
			if outs[i] == nil {
				continue
			}
			// Average the others' coefficients.
			k := len(feats[i].d) + 1
			avg := make([][]float64, render.FillBins)
			for b := range avg {
				avg[b] = make([]float64, k)
			}
			m := 0
			for j := range ps {
				if (j == i) != FillInSample || coefs[j] == nil {
					continue
				}
				for b := range avg {
					for r := 0; r < k; r++ {
						avg[b][r] += coefs[j][b][r]
					}
				}
				m++
			}
			if m == 0 {
				continue
			}
			var e float64
			v, d := feats[i].v, feats[i].d
			for p := range v {
				b := min(render.FillBins-1, int(v[p]*render.FillBins))
				s := avg[b][0]
				for j := range d {
					s += avg[b][j+1] * float64(d[j][p])
				}
				e += math.Abs(float64(v[p]) + s/float64(m) - float64(outs[i][p]))
			}
			sum += e / float64(len(v)) * 255
			n++
		}
		res = append(res, sum/float64(max(1, n)))
	}
	return res
}

func parallelEach(n int, f func(i int)) {
	done := make(chan struct{}, n)
	sem := make(chan struct{}, 8)
	for i := 0; i < n; i++ {
		go func(i int) {
			sem <- struct{}{}
			f(i)
			<-sem
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
}

// FillGainStats prints, per photo, the gain that best scales the others'
// average Fill Light shift to this photo's (1 = typical), next to simple
// image statistics, to see whether SPP's per-photo strength is predictable.
func FillGainStats(dir string, family func(string) string, c FillCandidate, val float64, w io.Writer) {
	codes := map[string]float64{}
	for _, s := range Steps() {
		if s.Param == "FillLight" {
			codes[s.Code] = s.Value
		}
	}
	refs, _ := filepath.Glob(filepath.Join(dir, ExportsDir, "*_"+Reference+".tif"))
	var ps []*fillPhoto
	for _, r := range refs {
		name := strings.TrimSuffix(filepath.Base(r), "_"+Reference+".tif")
		p := &fillPhoto{name: name, family: family(name), outs: map[float64]*render.Linear{}}
		for code, v := range codes {
			if v != val {
				continue
			}
			if out, err := load(filepath.Join(dir, ExportsDir, name+"_"+code+".tif")); err == nil {
				p.outs[v] = out
			}
		}
		if len(p.outs) == 0 || p.family == "" {
			continue
		}
		p.ref, _ = load(r)
		ps = append(ps, p)
	}
	for _, fam := range []string{"SD1", "SD15"} {
		var fp []*fillPhoto
		for _, p := range ps {
			if p.family == fam {
				fp = append(fp, p)
			}
		}
		n := len(fp)
		if n < 3 {
			continue
		}
		vs := make([][]float32, n)
		ds := make([][][]float32, n)
		os_ := make([][]float32, n)
		cf := make([][][]float64, n)
		parallelEach(n, func(i int) {
			vs[i], ds[i] = fillFeatures(fp[i].ref, c)
			out := fp[i].outs[val]
			o := make([]float32, len(vs[i]))
			for j := range o {
				o[j] = render.ToSRGB(luma(out.Pix, j*3))
			}
			os_[i] = o
			cf[i] = fillFit(vs[i], o, ds[i])
		})
		fmt.Fprintf(w, "\n%s Fill %+.2g: gain = best scale of the others' shift; stats of the neutral image (0-1 brightness)\n", fam, val)
		fmt.Fprintf(w, "%-10s %6s %6s %6s %6s %6s %6s\n", "photo", "gain", "mean", "median", "p10", "p90", "dark<.2")
		for i := 0; i < n; i++ {
			k := len(ds[i]) + 1
			var num, den float64
			for p := range vs[i] {
				b := min(render.FillBins-1, int(vs[i][p]*render.FillBins))
				var s float64
				m := 0
				for j := 0; j < n; j++ {
					if j == i {
						continue
					}
					c := cf[j][b]
					s += c[0]
					for r := 1; r < k; r++ {
						s += c[r] * float64(ds[i][r-1][p])
					}
					m++
				}
				s /= float64(m)
				num += s * float64(os_[i][p]-vs[i][p])
				den += s * s
			}
			sorted := append([]float32(nil), vs[i]...)
			sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
			var mean float64
			dark := 0
			for _, x := range vs[i] {
				mean += float64(x)
				if x < 0.2 {
					dark++
				}
			}
			L := len(sorted)
			fmt.Fprintf(w, "%-10s %6.2f %6.3f %6.3f %6.3f %6.3f %6.3f\n", fp[i].name, num/den, mean/float64(L), sorted[L/2], sorted[L/10], sorted[L*9/10], float64(dark)/float64(L))
		}
	}
}
