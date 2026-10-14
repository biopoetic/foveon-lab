// foveonmodel builds the Foveon sensor's layer spectral sensitivities from
// first principles and fits them to a camera's own colour matrix.
//
// Physics: light of wavelength λ entering silicon decays as exp(-α(λ)·z),
// with α = 4πk/λ (k: silicon's extinction coefficient, M. A. Green 2008,
// si_green2008.txt). A layer collecting the carriers generated between
// depths a and b therefore sees exp(-α·a) - exp(-α·b) of the photons: blue
// is absorbed near the surface (1/α ≈ 0.4 µm at 450 nm), red deep
// (≈ 3.6 µm at 650 nm). The stack is top 0..d1, middle d1..d2, bottom
// d2..d3, behind an infrared-cut filter (logistic edge λc, width w).
//
// Fit: the camera's CAMF colour matrix (white-balanced layers → linear
// sRGB, Sunlight) is Sigma's own answer to "which mix of these layers gives
// sRGB". The depths and IR cut are chosen so that, for a large set of
// smooth reflectance spectra under daylight, that very matrix applied to the
// modelled layer responses reproduces the colours the CIE 1931 observer
// sees (Wyman-Sloan-Shirley 2013 fit of the colour matching functions).
//
//	go run ./tools/foveonmodel photo.X3F
package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/biopoetic/foveon-lab/render"
	_ "golang.org/x/image/tiff"
	_ "image/png"
	"log"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"

	"github.com/biopoetic/foveon-lab/x3f"
)

//go:embed si_green2008.txt
var siData string

const (
	lmin, lmax, lstep = 380.0, 720.0, 5.0
)

var (
	lams  []float64 // nm
	alpha []float64 // 1/µm
)

func init() {
	type row struct{ l, k float64 }
	var rows []row
	sc := bufio.NewScanner(strings.NewReader(siData))
	for sc.Scan() {
		var l, n, k float64
		if c, _ := fmt.Sscan(sc.Text(), &l, &n, &k); c == 3 {
			rows = append(rows, row{l * 1000, k})
		}
	}
	for l := lmin; l <= lmax+1e-9; l += lstep {
		i := sort.Search(len(rows), func(i int) bool { return rows[i].l >= l })
		k := rows[i].k
		if rows[i].l > l {
			t := (l - rows[i-1].l) / (rows[i].l - rows[i-1].l)
			// k varies over decades: interpolate its log.
			k = math.Exp(math.Log(rows[i-1].k)*(1-t) + math.Log(rows[i].k)*t)
		}
		lams = append(lams, l)
		alpha = append(alpha, 4*math.Pi*k/(l*1e-3)) // per µm
	}
}

// g is the piecewise Gaussian of the Wyman-Sloan-Shirley CMF fit.
func g(l, mu, s1, s2 float64) float64 {
	s := s1
	if l >= mu {
		s = s2
	}
	t := (l - mu) / s
	return math.Exp(-0.5 * t * t)
}

func cmf(l float64) [3]float64 {
	return [3]float64{
		1.056*g(l, 599.8, 37.9, 31.0) + 0.362*g(l, 442.0, 16.0, 26.7) - 0.065*g(l, 501.1, 20.4, 26.2),
		0.821*g(l, 568.8, 46.9, 40.5) + 0.286*g(l, 530.9, 16.3, 31.1),
		1.217*g(l, 437.0, 11.8, 36.0) + 0.681*g(l, 459.0, 26.0, 13.8),
	}
}

// planck is a black body's relative spectral radiance at temperature T.
func planck(l, T float64) float64 {
	lm := l * 1e-9
	return 1 / (math.Pow(lm, 5) * (math.Exp(1.4388e-2/(lm*T)) - 1))
}

// model is the sensor: the depths (µm) bounding each layer's collection
// zone (top d0..d1, middle d1..d2, bottom d2..d3; d0 = the surface's dead
// layer, whose carriers recombine) and the IR-cut edge (nm). The zones
// end between the junctions (≈0.2/0.6/2 µm deep), not at them: each diode
// gathers what drifts and diffuses to it.
type model struct{ d1, d2, d3, lc, w, d0 float64 }

func (m model) sens(i int) [3]float64 {
	a := alpha[i]
	ir := 1 / (1 + math.Exp((lams[i]-m.lc)/m.w))
	e := func(z float64) float64 { return math.Exp(-a * z) }
	return [3]float64{ // bottom, middle, top: the camera's channel order
		ir * (e(m.d2) - e(m.d3)),
		ir * (e(m.d1) - e(m.d2)),
		ir * (e(m.d0) - e(m.d1)),
	}
}

var xyzToSRGB = [9]float64{3.2406, -1.5372, -0.4986, -0.9689, 1.8758, 0.0415, 0.0557, -0.2040, 1.0570}

func apply(m [9]float64, v [3]float64) [3]float64 {
	return [3]float64{m[0]*v[0] + m[1]*v[1] + m[2]*v[2], m[3]*v[0] + m[4]*v[1] + m[5]*v[2], m[6]*v[0] + m[7]*v[1] + m[8]*v[2]}
}

// scene: reflectance spectra under an illuminant, with the colours the
// observer sees (linear sRGB, white = 1,1,1).
type scene struct {
	illum  []float64
	refl   [][]float64
	target [][3]float64
}

func newScene(T float64, n int, seed int64) *scene {
	rng := rand.New(rand.NewSource(seed))
	s := &scene{}
	for _, l := range lams {
		s.illum = append(s.illum, planck(l, T))
	}
	see := func(r []float64) [3]float64 {
		var xyz [3]float64
		for i, l := range lams {
			c := cmf(l)
			for k := range xyz {
				xyz[k] += c[k] * s.illum[i] * r[i]
			}
		}
		return apply(xyzToSRGB, xyz)
	}
	white := make([]float64, len(lams))
	for i := range white {
		white[i] = 1
	}
	w := see(white)
	for j := 0; j < n; j++ {
		r := make([]float64, len(lams))
		base := 0.05 + 0.3*rng.Float64()
		for i := range r {
			r[i] = base
		}
		for b := 0; b < 1+rng.Intn(3); b++ {
			mu, sd, amp := 380+340*rng.Float64(), 15+100*rng.Float64(), 0.7*rng.Float64()
			if rng.Float64() < 0.3 {
				amp = -amp * base / 0.7
			}
			for i, l := range lams {
				r[i] += amp * math.Exp(-0.5*((l-mu)/sd)*((l-mu)/sd))
			}
		}
		for i := range r {
			r[i] = math.Max(0.01, math.Min(0.95, r[i]))
		}
		t := see(r)
		for k := range t {
			t[k] /= w[k] // von Kries to the illuminant's white
		}
		s.refl = append(s.refl, r)
		s.target = append(s.target, t)
	}
	return s
}

// layers is the white-balanced layer response of each reflectance.
func (s *scene) layers(m model) [][3]float64 {
	S := make([][3]float64, len(lams))
	var wht [3]float64
	for i := range lams {
		S[i] = m.sens(i)
		for k := 0; k < 3; k++ {
			wht[k] += S[i][k] * s.illum[i]
		}
	}
	out := make([][3]float64, len(s.refl))
	for j, r := range s.refl {
		for i := range lams {
			for k := 0; k < 3; k++ {
				out[j][k] += S[i][k] * s.illum[i] * r[i]
			}
		}
		for k := 0; k < 3; k++ {
			out[j][k] /= wht[k]
		}
	}
	return out
}

// errCC: RMS of the camera matrix applied to the modelled layers vs what
// the observer sees.
func (s *scene) errCC(m model, cc [9]float64) float64 {
	var e float64
	for j, L := range s.layers(m) {
		p := apply(cc, L)
		for k := 0; k < 3; k++ {
			d := p[k] - s.target[j][k]
			e += d * d
		}
	}
	return math.Sqrt(e / float64(3*len(s.refl)))
}

// nelderMead minimises f over n parameters from x0 with initial steps dx.
func nelderMead(f func([]float64) float64, x0, dx []float64, iters int) ([]float64, float64) {
	n := len(x0)
	pts := make([][]float64, n+1)
	vals := make([]float64, n+1)
	for i := range pts {
		pts[i] = append([]float64(nil), x0...)
		if i > 0 {
			pts[i][i-1] += dx[i-1]
		}
		vals[i] = f(pts[i])
	}
	for it := 0; it < iters; it++ {
		idx := make([]int, n+1)
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool { return vals[idx[a]] < vals[idx[b]] })
		np, nv := make([][]float64, n+1), make([]float64, n+1)
		for i, j := range idx {
			np[i], nv[i] = pts[j], vals[j]
		}
		pts, vals = np, nv
		c := make([]float64, n)
		for i := 0; i < n; i++ {
			for k := 0; k < n; k++ {
				c[k] += pts[i][k] / float64(n)
			}
		}
		at := func(t float64) []float64 {
			p := make([]float64, n)
			for k := range p {
				p[k] = c[k] + t*(pts[n][k]-c[k])
			}
			return p
		}
		r := at(-1)
		fr := f(r)
		switch {
		case fr < vals[0]:
			e := at(-2)
			if fe := f(e); fe < fr {
				pts[n], vals[n] = e, fe
			} else {
				pts[n], vals[n] = r, fr
			}
		case fr < vals[n-1]:
			pts[n], vals[n] = r, fr
		default:
			cc := at(0.5)
			if fc := f(cc); fc < vals[n] {
				pts[n], vals[n] = cc, fc
			} else {
				for i := 1; i <= n; i++ {
					for k := range pts[i] {
						pts[i][k] = pts[0][k] + 0.5*(pts[i][k]-pts[0][k])
					}
					vals[i] = f(pts[i])
				}
			}
		}
	}
	best := 0
	for i := range vals {
		if vals[i] < vals[best] {
			best = i
		}
	}
	return pts[best], vals[best]
}

// bestMatrix is the least-squares 3×3 from the modelled layers to what the
// observer sees, and its RMS: how well these layers can give colour at all.
func (s *scene) bestMatrix(m model) ([9]float64, float64) {
	L := s.layers(m)
	var ata, atb [3][3]float64
	for j := range L {
		for r := 0; r < 3; r++ {
			for q := 0; q < 3; q++ {
				ata[r][q] += L[j][r] * L[j][q]
				atb[r][q] += s.target[j][r] * L[j][q]
			}
		}
	}
	inv := inv3(ata)
	var M [9]float64
	for r := 0; r < 3; r++ {
		for q := 0; q < 3; q++ {
			for k := 0; k < 3; k++ {
				M[r*3+q] += atb[r][k] * inv[k][q]
			}
		}
	}
	return M, s.errCC(m, M)
}

func inv3(a [3][3]float64) [3][3]float64 {
	d := a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) - a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) + a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])
	var o [3][3]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			i1, i2, j1, j2 := (j+1)%3, (j+2)%3, (i+1)%3, (i+2)%3
			o[i][j] = (a[i1][j1]*a[i2][j2] - a[i1][j2]*a[i2][j1]) / d
		}
	}
	return o
}

// Filters: transmission spectra. Long-pass glass (Wratten yellow, orange,
// red) is modelled by its documented 50% edge with a logistic slope;
// conversion filters (81A warming, 82A cooling) are, by definition, the
// ratio of two black-body spectra (±18 mired from 5500 K).
func longpass(l, edge float64) float64 { return 1 / (1 + math.Exp(-(l-edge)/10)) }

var filters = map[string]func(l float64) float64{
	"yellow":      func(l float64) float64 { return longpass(l, 495) },                              // Wratten 8 (K2)
	"deepyellow":  func(l float64) float64 { return longpass(l, 530) },                              // Wratten 15 (G)
	"orange":      func(l float64) float64 { return longpass(l, 555) },                              // Wratten 21
	"red":         func(l float64) float64 { return longpass(l, 595) },                              // Wratten 25 (A)
	"deepred":     func(l float64) float64 { return longpass(l, 615) },                              // Wratten 29
	"yellowgreen": func(l float64) float64 { return longpass(l, 480) * (1 - 0.6*longpass(l, 590)) }, // Wratten 11 (X1)
	"green":       func(l float64) float64 { return longpass(l, 490) * (1 - longpass(l, 585)) },     // Wratten 58
	"blue":        func(l float64) float64 { return 1 - longpass(l, 505) },                          // Wratten 47
	"warm":        mired(18),                                                                        // 81A
	"cool":        mired(-18),                                                                       // 82A
}

// mired is a conversion filter shifting 5500 K light by m mired.
func mired(m float64) func(float64) float64 {
	t2 := 1 / (1/5500.0 + m*1e-6)
	mx := 0.0
	for l := lmin; l <= lmax; l += lstep {
		mx = math.Max(mx, planck(l, t2)/planck(l, 5500))
	}
	return func(l float64) float64 { return planck(l, t2) / planck(l, 5500) / mx }
}

// filterMatrix is the 3×3 in white-balanced layer space that maps the
// layers without the filter to the layers with it (least squares over the
// scene's colours; the filter's light loss is kept), and its RMS.
func (s *scene) filterMatrix(m model, T func(float64) float64) ([9]float64, float64) {
	plain := s.layers(m)
	f := &scene{refl: s.refl, target: s.target, illum: make([]float64, len(s.illum))}
	for i, l := range lams {
		f.illum[i] = s.illum[i] * T(l)
	}
	// Normalise the filtered layers by the UNfiltered white.
	var wht, whtF [3]float64
	for i := range lams {
		S := m.sens(i)
		for k := 0; k < 3; k++ {
			wht[k] += S[k] * s.illum[i]
			whtF[k] += S[k] * f.illum[i]
		}
	}
	filt := f.layers(m) // normalised by its own white: undo that
	for j := range filt {
		for k := 0; k < 3; k++ {
			filt[j][k] *= whtF[k] / wht[k]
		}
	}
	var ata, atb [3][3]float64
	for j := range plain {
		for r := 0; r < 3; r++ {
			for q := 0; q < 3; q++ {
				ata[r][q] += plain[j][r] * plain[j][q]
				atb[r][q] += filt[j][r] * plain[j][q]
			}
		}
	}
	inv := inv3(ata)
	var F [9]float64
	for r := 0; r < 3; r++ {
		for q := 0; q < 3; q++ {
			for k := 0; k < 3; k++ {
				F[r*3+q] += atb[r][k] * inv[k][q]
			}
		}
	}
	var e, n float64
	for j := range plain {
		p := apply(F, plain[j])
		for k := 0; k < 3; k++ {
			d := p[k] - filt[j][k]
			e, n = e+d*d, n+1
		}
	}
	return F, math.Sqrt(e / n)
}

// wbReport: which black-body temperature the camera's white balance gains
// for wb correspond to, on the modelled layers (relative to its Sunlight
// gains, taken as 5500 K), and the per-layer residual: what the camera's
// estimate differs from a true black body (a tint off the Planck locus).
// camTemp is the black-body temperature (K) the camera's gains for wb
// correspond to, and the off-locus residual of its bottom/top layers (%).
func camTemp(m model, c *x3f.CAMF, wb string) (float64, float64, float64, bool) {
	gs, _, err1 := c.WBTransform("Sunlight")
	gw, _, err2 := c.WBTransform(wb)
	if err1 != nil || err2 != nil {
		return 0, 0, 0, false
	}
	white := func(T float64) [3]float64 {
		var r [3]float64
		for i, l := range lams {
			S := m.sens(i)
			e := planck(l, T)
			for k := 0; k < 3; k++ {
				r[k] += S[k] * e
			}
		}
		return r
	}
	rs := white(5500)
	norm := func(v [3]float64) [3]float64 { return [3]float64{v[0] / v[1], 1, v[2] / v[1]} }
	want := norm([3]float64{gs[0] / gw[0], gs[1] / gw[1], gs[2] / gw[2]})
	bestT, bestE := 0.0, math.Inf(1)
	var bestR [3]float64
	for T := 2000.0; T <= 12000; T += 10 {
		r := white(T)
		pr := norm([3]float64{r[0] / rs[0], r[1] / rs[1], r[2] / rs[2]})
		e := math.Pow(math.Log(pr[0]/want[0]), 2) + math.Pow(math.Log(pr[2]/want[2]), 2)
		if e < bestE {
			bestT, bestE, bestR = T, e, pr
		}
	}
	return bestT, 100 * (want[0]/bestR[0] - 1), 100 * (want[2]/bestR[2] - 1), true
}

func wbReport(m model, c *x3f.CAMF, wb string) {
	gs, _, err1 := c.WBTransform("Sunlight")
	gw, _, err2 := c.WBTransform(wb)
	if err1 != nil || err2 != nil {
		fmt.Println("no gains for", wb)
		return
	}
	white := func(T float64) [3]float64 {
		var r [3]float64
		for i, l := range lams {
			S := m.sens(i)
			e := planck(l, T)
			for k := 0; k < 3; k++ {
				r[k] += S[k] * e
			}
		}
		return r
	}
	rs := white(5500)
	// Camera: gains ∝ 1/response, so response_wb/response_sun = gs/gw.
	var want [3]float64
	for k := range want {
		want[k] = gs[k] / gw[k]
	}
	norm := func(v [3]float64) [3]float64 { return [3]float64{v[0] / v[1], 1, v[2] / v[1]} }
	want = norm(want)
	bestT, bestE := 0.0, math.Inf(1)
	var bestR [3]float64
	for T := 2000.0; T <= 10000; T += 10 {
		r := white(T)
		pr := norm([3]float64{r[0] / rs[0], r[1] / rs[1], r[2] / rs[2]})
		e := math.Pow(math.Log(pr[0]/want[0]), 2) + math.Pow(math.Log(pr[2]/want[2]), 2)
		if e < bestE {
			bestT, bestE, bestR = T, e, pr
		}
	}
	fmt.Printf("%s white balance: camera's layer response relative to Sunlight (bottom/middle/top, middle = 1): %.3f / 1 / %.3f\n", wb, want[0], want[2])
	fmt.Printf("  nearest black body: %.0f K, which gives %.3f / 1 / %.3f; camera minus black body: bottom %+.1f%%, top %+.1f%%\n",
		bestT, bestR[0], bestR[2], 100*(want[0]/bestR[0]-1), 100*(want[2]/bestR[2]-1))
}

func main() {
	if out := os.Getenv("IR"); out != "" {
		irMain(out)
		return
	}
	if len(os.Args) < 2 {
		log.Fatal("usage: foveonmodel photo.X3F")
	}
	f, err := x3f.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	c, err := f.CAMF()
	if err != nil {
		log.Fatal(err)
	}
	_, cc, err := c.WBTransform("Sunlight")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s: Sunlight colour matrix %.3f\n", f.Props["CAMMODEL"], cc)
	sc := newScene(5500, 400, 1)
	if os.Getenv("SPPWB") != "" {
		sppWB(c, f, os.Getenv("SPPWB"))
		return
	}
	if wb := os.Getenv("WB"); wb != "" {
		var fm model
		gen := os.Getenv("GEN")
		if gen == "" {
			gen = "Merrill"
		}
		var lf map[string]struct{ Model []float64 }
		data, _ := os.ReadFile("render/layerfilters.json")
		json.Unmarshal(data, &lf)
		v := lf[gen].Model
		fm = model{d0: v[0], d1: v[1], d2: v[2], d3: v[3], lc: v[4], w: v[5]}
		for _, w := range strings.Split(wb, ",") {
			wbReport(fm, c, w)
		}
		return
	}
	if os.Getenv("LIT") != "" {
		for _, m := range []model{{0.2, 0.6, 2.0, 660, 10, 0}, {0.2, 0.6, 3.0, 660, 10, 0}, {0.3, 0.9, 3.0, 660, 10, 0}, {0.15, 0.5, 2.0, 650, 10, 0}} {
			M, e := sc.bestMatrix(m)
			fmt.Printf("depths %.2f/%.2f/%.2f IR %.0f: camera matrix RMS %.4f | best matrix RMS %.4f %.2f\n", m.d1, m.d2, m.d3, m.lc, sc.errCC(m, cc), e, M)
		}
		return
	}
	// Parameters: log thicknesses (dead, top, middle, bottom), IR edge, log width.
	clamp := func(p []float64) model {
		d0 := math.Exp(p[0])
		d1 := d0 + math.Exp(p[1])
		d2 := d1 + math.Exp(p[2])
		d3 := d2 + math.Exp(p[3])
		return model{d1: d1, d2: d2, d3: d3, lc: p[4], w: math.Exp(p[5]), d0: d0}
	}
	obj := func(p []float64) float64 {
		m := clamp(p)
		if m.d3 > 20 || m.lc < 600 || m.lc > 760 || m.w > 60 {
			return 1e9
		}
		return sc.errCC(m, cc)
	}
	best, bestV := []float64(nil), math.Inf(1)
	rng := rand.New(rand.NewSource(7))
	for st := 0; st < 40; st++ {
		x0 := []float64{math.Log(0.01 + 0.15*rng.Float64()), math.Log(0.1 + 0.6*rng.Float64()), math.Log(0.2 + 1.5*rng.Float64()),
			math.Log(0.5 + 5*rng.Float64()), 640 + 60*rng.Float64(), math.Log(5 + 20*rng.Float64())}
		p, v := nelderMead(obj, x0, []float64{0.5, 0.4, 0.4, 0.4, 15, 0.4}, 800)
		if v < bestV {
			best, bestV = p, v
		}
	}
	m := clamp(best)
	fmt.Printf("fit: dead %.3f µm, top 0-%.2f µm, middle %.2f-%.2f µm, bottom %.2f-%.2f µm; IR cut %.0f nm (width %.1f)\n", m.d0, m.d1, m.d1, m.d2, m.d2, m.d3, m.lc, m.w)
	fmt.Printf("RMS error of the camera's own matrix on the modelled layers: %.4f (white = 1)\n", bestV)
	// Held out: a different set of colours and a warmer light.
	for _, T := range []float64{5500, 3200} {
		h := newScene(T, 400, 99)
		fmt.Printf("  held-out colours, %4.0f K: RMS %.4f\n", T, h.errCC(m, cc))
	}
	if out := os.Getenv("FILTERS"); out != "" {
		names := make([]string, 0, len(filters))
		for n := range filters {
			names = append(names, n)
		}
		sort.Strings(names)
		var b strings.Builder
		fmt.Fprintf(&b, "{\"model\": [%.4f, %.4f, %.4f, %.4f, %.1f, %.2f], \"filters\": {", m.d0, m.d1, m.d2, m.d3, m.lc, m.w)
		for i, n := range names {
			F, e := sc.filterMatrix(m, filters[n])
			fmt.Printf("  filter %-11s matrix fit RMS %.4f\n", n, e)
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "\"%s\": [", n)
			for k, v := range F {
				if k > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%.5f", v)
			}
			b.WriteString("]")
		}
		b.WriteString("}}")
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Println("layer sensitivities (relative, bottom / middle / top):")
	for i, l := range lams {
		if int(l)%20 == 0 {
			s := m.sens(i)
			fmt.Printf("  %3.0f nm  %.3f  %.3f  %.3f\n", l, s[0], s[1], s[2])
		}
	}
}

func loadModel(gen string) model {
	var lf map[string]struct{ Model []float64 }
	data, _ := os.ReadFile("render/layerfilters.json")
	json.Unmarshal(data, &lf)
	v := lf[gen].Model
	return model{d0: v[0], d1: v[1], d2: v[2], d3: v[3], lc: v[4], w: v[5]}
}

// sppWB prints, for one photo and its SPP neutral export, the camera's
// light temperature (from its WB gains on the modelled layers) and how
// SPP's colour balance differs from the camera's own WB + matrix: the
// median, over mid-tone pixels, of log(SPP R/G) - log(camera R/G) and the
// same for B/G, as gains (camera = 1).
func sppWB(c *x3f.CAMF, f *x3f.File, export string) {
	gen := render.SensorGeneration(x3f.CalibGroup(f.Props["CAMMODEL"]))
	m := loadModel(gen)
	wb := c.CameraWB(strings.TrimSpace(f.Props["WB_DESC"]))
	if u := os.Getenv("USEWB"); u != "" {
		wb = u
	}
	T, tb, tt, ok := camTemp(m, c, wb)
	if !ok {
		fmt.Println("no WB gains")
		return
	}
	raw, err := f.DecodeRaw()
	if err != nil {
		fmt.Println(err)
		return
	}
	x3f.FixBadPixels(raw, c)
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		fmt.Println(err)
		return
	}
	g, cc, _ := c.WBTransform(wb)
	data, _ := os.ReadFile(export)
	si, _, err := render.DecodeImage(data)
	if err != nil {
		fmt.Println(err)
		return
	}
	spp := render.FromImage(si, 600, 0)
	cam := render.FromCamLinear(img.Pix, img.W, img.H, 600, 0, render.CamTransform{Gain: g, CC: cc})
	if cam.W != spp.W || cam.H != spp.H {
		fmt.Printf("%s: sizes differ\n", export)
		return
	}
	var rg, bg []float64
	for i := 0; i < len(cam.Pix); i += 3 {
		a, b := cam.Pix[i:i+3], spp.Pix[i:i+3]
		ya := 0.2126*a[0] + 0.7152*a[1] + 0.0722*a[2]
		if ya < 0.03 || ya > 0.4 || min(a[0], a[1], a[2], b[0], b[1], b[2]) <= 0.002 || max(b[0], b[1], b[2]) > 0.95 {
			continue
		}
		rg = append(rg, math.Log(float64(b[0]/b[1]))-math.Log(float64(a[0]/a[1])))
		bg = append(bg, math.Log(float64(b[2]/b[1]))-math.Log(float64(a[2]/a[1])))
	}
	if len(rg) < 100 {
		fmt.Println("too few mid-tone pixels")
		return
	}
	sort.Float64s(rg)
	sort.Float64s(bg)
	robs, bobs := math.Exp(rg[len(rg)/2]), math.Exp(bg[len(bg)/2])
	if box := os.Getenv("BOX"); box != "" {
		var x, y, w, h float64
		fmt.Sscanf(box, "%g,%g,%g,%g", &x, &y, &w, &h)
		mean := func(l *render.Linear) [3]float64 {
			var m [3]float64
			n := 0.0
			for yy := int(y * float64(l.H)); yy < int((y+h)*float64(l.H)); yy++ {
				for xx := int(x * float64(l.W)); xx < int((x+w)*float64(l.W)); xx++ {
					for k := 0; k < 3; k++ {
						m[k] += float64(l.Pix[(yy*l.W+xx)*3+k])
					}
					n++
				}
			}
			return [3]float64{m[0] / n, m[1] / n, m[2] / n}
		}
		a, b := mean(cam), mean(spp)
		fmt.Printf("  white object: camera R/G %.3f B/G %.3f | SPP R/G %.3f B/G %.3f\n", a[0]/a[1], a[2]/a[1], b[0]/b[1], b[2]/b[1])
	}
	// Which white-balance temperature would SPP's balance correspond to?
	// SPP's gains relative to the camera's are r(Tcam)/r(Teff) per layer;
	// simulate that on scene colours lit at Tcam through the camera matrix.
	sc := newScene(T, 200, 3)
	L := sc.layers(m)
	resp := func(Tx float64) [3]float64 {
		var r [3]float64
		for i, l := range lams {
			S := m.sens(i)
			e := planck(l, Tx)
			for k := 0; k < 3; k++ {
				r[k] += S[k] * e
			}
		}
		return r
	}
	rc := resp(T)
	bestTe, bestE, bestP := 0.0, math.Inf(1), [2]float64{}
	for Te := 1500.0; Te <= 20000; Te *= 1.01 {
		re := resp(Te)
		var lr, lb []float64
		for _, v := range L {
			base := apply(cc, v)
			d := [3]float64{v[0] * rc[0] / re[0] * re[1] / rc[1], v[1], v[2] * rc[2] / re[2] * re[1] / rc[1]}
			pr := apply(cc, d)
			if base[0] <= 0 || base[1] <= 0 || base[2] <= 0 || pr[0] <= 0 || pr[1] <= 0 || pr[2] <= 0 {
				continue
			}
			lr = append(lr, math.Log(pr[0]/pr[1])-math.Log(base[0]/base[1]))
			lb = append(lb, math.Log(pr[2]/pr[1])-math.Log(base[2]/base[1]))
		}
		if len(lr) < 20 {
			continue
		}
		sort.Float64s(lr)
		sort.Float64s(lb)
		p := [2]float64{math.Exp(lr[len(lr)/2]), math.Exp(lb[len(lb)/2])}
		e := math.Pow(math.Log(p[0]/robs), 2) + math.Pow(math.Log(p[1]/bobs), 2)
		if e < bestE {
			bestTe, bestE, bestP = Te, e, p
		}
	}
	name := f.Path[strings.LastIndexAny(f.Path, `/\`)+1:]
	fmt.Printf("%-14s %-7s WB %-12s %5.0f K (off-locus %+.1f%%/%+.1f%%)  SPP vs camera: R %.3f B %.3f -> as a WB of %5.0f K (gives R %.3f B %.3f)\n",
		name, gen, wb, T, tb, tt, robs, bobs, bestTe, bestP[0], bestP[1])
}
