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
	"fmt"
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

func main() {
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
