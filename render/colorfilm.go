package render

// Colour slide film, from its maker's data (colorfilms.json), through the
// physics of a reversal film:
//
//  1. The scene exposes the film's three layers. A 3×3 matrix takes the
//     sensor's white-balanced layers to the film's layer exposures, fitted
//     (least squares, 400 reflectance spectra in 5500 K daylight) from the
//     film's spectral sensitivities and the sensor generation's own silicon
//     model (spectralfilter.go) — the filter pad's machinery.
//  2. Mid-grey gets 10/ISO lux·s (the B&W films' convention; on the slide
//     curves' absolute axis it lands at a density of about 1). The sensor's
//     mid-grey is 10/78 of its saturation: a digital ISO is rated by
//     saturation (ISO 12232, H_sat = 78/S) where a meter puts mid-grey at
//     10/S — so a layer value of 0.128 (1 = saturation) is the film's
//     mid-grey exposure.
//  3. Each layer's characteristic curve (Status A density, daylight)
//     gives its density.
//  4. Each channel's density is read at one wavelength (narrow-band, as
//     Status A's filters), fitted to the film's grey balance (a neutral
//     exposure makes a neutral slide); that gives the dye amounts; the slide
//     passes 10^-(Σ amount·ε(λ)) of a 5500 K light, seen by the CIE 1931
//     observer (Wyman's fit), the eye adapted to the clearest part of the
//     slide (D-min = white, as on a light box).
//
// Step 4 is baked into a 3-D table over the three densities (log output,
// interpolated trilinearly).

import (
	_ "embed"
	"encoding/json"
	"math"
	"sort"
	"sync"
)

//go:embed colorfilms.json
var colorFilmsJSON []byte

type colorFilmData struct {
	Film string                  `json:"film"`
	ISO  float64                 `json:"iso"`
	Src  string                  `json:"src"`
	URL  string                  `json:"url"`
	Char map[string][][2]float64 `json:"char"` // R, G, B: log H -> Status A density
	Sens map[string][][2]float64 `json:"sens"` // R, G, B: nm -> log relative sensitivity
	Dye  map[string][][2]float64 `json:"dye"`  // C, M, Y: nm -> normalised spectral density
}

var colorFilmData_ = sync.OnceValue(func() map[string]*colorFilmData {
	var d struct {
		Films []*colorFilmData `json:"films"`
	}
	if err := json.Unmarshal(colorFilmsJSON, &d); err != nil {
		panic("colorfilms.json: " + err.Error())
	}
	m := map[string]*colorFilmData{}
	for _, f := range d.Films {
		for _, c := range []map[string][][2]float64{f.Char, f.Sens, f.Dye} {
			for _, pts := range c {
				sort.Slice(pts, func(i, j int) bool { return pts[i][0] < pts[j][0] })
			}
		}
		m[f.Film] = f
	}
	return m
})

// IsColorFilm reports whether the library film named key has the colour
// model (its data sheet's curves).
func IsColorFilm(key string) bool {
	_, ok := colorFilmData_()[key]
	return ok || IsColorNegative(key)
}

// pinterp interpolates sorted points linearly, flat past the ends.
func pinterp(pts [][2]float64, x float64) float64 {
	n := len(pts)
	if x <= pts[0][0] {
		return pts[0][1]
	}
	if x >= pts[n-1][0] {
		return pts[n-1][1]
	}
	i := sort.Search(n, func(i int) bool { return pts[i][0] >= x })
	t := (x - pts[i-1][0]) / (pts[i][0] - pts[i-1][0])
	return pts[i-1][1] + t*(pts[i][1]-pts[i-1][1])
}

// sensAt is a layer's linear relative sensitivity at λ: the drawn curve,
// and past its ends (drawn down to ~-0.7) falling a decade per 15 nm.
func sensAt(pts [][2]float64, l float64) float64 {
	n := len(pts)
	switch {
	case l < pts[0][0]:
		return math.Pow(10, pts[0][1]-(pts[0][0]-l)/15)
	case l > pts[n-1][0]:
		return math.Pow(10, pts[n-1][1]-(l-pts[n-1][0])/15)
	}
	return math.Pow(10, pinterp(pts, l))
}

// cmf1931 is Wyman, Sloan & Shirley's multi-lobe fit of the CIE 1931 2°
// observer.
func cmf1931(l float64) [3]float64 {
	g := func(mu, s1, s2 float64) float64 {
		s := s1
		if l >= mu {
			s = s2
		}
		t := (l - mu) / s
		return math.Exp(-0.5 * t * t)
	}
	return [3]float64{
		1.056*g(599.8, 37.9, 31.0) + 0.362*g(442.0, 16.0, 26.7) - 0.065*g(501.1, 20.4, 26.2),
		0.821*g(568.8, 46.9, 40.5) + 0.286*g(530.9, 16.3, 31.1),
		1.217*g(437.0, 11.8, 36.0) + 0.681*g(459.0, 26.0, 13.8),
	}
}

var xyzToLinSRGB = [9]float64{3.2406, -1.5372, -0.4986, -0.9689, 1.8758, 0.0415, 0.0557, -0.2040, 1.0570}

// sensorGrey: mid-grey as a fraction of the sensor's saturation, 10/78
// (ISO 12232 saturation-based speed vs an exposure meter's 10/S).
const sensorGrey = 10.0 / 78

const (
	cfLutN    = 33  // samples per density axis
	cfLutDMax = 4.0 // density range of the table
)

// colorFilm is one film prepared for one sensor generation.
type colorFilm struct {
	// rp: sensor layers -> film layer exposures (R, G, B), white = 1, a
	// root-polynomial (fitLayerMap): rows 0-8 times (r, g, b), rows 9-17
	// times (√rg, √gb, √rb).
	rp     [18]float32
	hWhite float64    // lux·s at the sensor's saturation (layer value 1)
	curve  [3][][2]float64
	lut    []float32 // cfLutN^3 × 3: -log10 of linear sRGB, over (D_R, D_G, D_B)
	// readLam: the channels' reading wavelengths (R, G, B) the grey balance
	// gives; greyRMS its remaining error (natural log of R/G and B/G).
	readLam [3]float64
	greyRMS float64
	// filtration: a printed negative's enlarger filtration (log10 gain per
	// paper layer, R G B).
	filtration [3]float64
}

var (
	colorFilmMu    sync.Mutex
	colorFilmCache = map[string]*colorFilm{}
)

// colorFilmFor is film key prepared for sensor, nil if either has no model.
func colorFilmFor(key, sensor string) *colorFilm { return colorFilmOn(key, "", sensor) }

// colorFilmOn is film key (a slide, or a negative printed on paper; ""
// = the first colour paper) prepared for sensor, nil if any has no model.
func colorFilmOn(key, paper, sensor string) *colorFilm {
	if n := colorNegs().negs[key]; n != nil {
		if paper == "" || colorNegs().papers[paper] == nil {
			if o := colorNegs().order; len(o) > 0 {
				paper = o[0]
			}
		}
		pp := colorNegs().papers[paper]
		if pp == nil {
			return nil
		}
		colorFilmMu.Lock()
		defer colorFilmMu.Unlock()
		k := key + "|" + paper + "|" + sensor
		if f, ok := colorFilmCache[k]; ok {
			return f
		}
		f := newColorNeg(n, pp, sensor)
		colorFilmCache[k] = f
		return f
	}
	d := colorFilmData_()[key]
	if d == nil {
		return nil
	}
	colorFilmMu.Lock()
	defer colorFilmMu.Unlock()
	if f, ok := colorFilmCache[key+"|"+sensor]; ok {
		return f
	}
	f := newColorFilm(d, sensor)
	colorFilmCache[key+"|"+sensor] = f
	return f
}

func newColorFilm(d *colorFilmData, sensor string) *colorFilm {
	S := layerSens(sensor)
	if S == nil {
		return nil
	}
	sp := spectral()
	names := [3]string{"R", "G", "B"}
	// 1. Sensor layers -> film exposures (fitLayerMap).
	f := &colorFilm{hWhite: 10 / d.ISO / sensorGrey}
	if !fitLayerMap(f, S, d.Sens) {
		return nil
	}
	for k, n := range names {
		f.curve[k] = d.Char[n]
	}
	// 4. Dyes. Channel k (R, G, B) reads the dyes' density at one
	// wavelength λ_k (a narrow-band reading, as Status A's filters): D =
	// A(λ)·c, so the dye amounts are c = A⁻¹·D. The λ_k are fitted to the
	// film's grey balance — a neutral daylight exposure, which the curves
	// are, makes a visually neutral slide — over the whole scale.
	dyes := [3]string{"C", "M", "Y"}
	eps := make([][3]float64, len(sp.lams))
	for i, l := range sp.lams {
		for j, n := range dyes {
			eps[i][j] = math.Max(0, pinterp(d.Dye[n], l))
		}
	}
	cmfE := make([][3]float64, len(sp.lams))
	for i, l := range sp.lams {
		c := cmf1931(l)
		for k := range c {
			cmfE[i][k] = c[k] * sp.illum[i]
		}
	}
	slide := func(c [3]float64) [3]float64 {
		var xyz [3]float64
		for i := range sp.lams {
			t := math.Pow(10, -(c[0]*eps[i][0] + c[1]*eps[i][1] + c[2]*eps[i][2]))
			for k := 0; k < 3; k++ {
				xyz[k] += cmfE[i][k] * t
			}
		}
		return [3]float64{
			xyzToLinSRGB[0]*xyz[0] + xyzToLinSRGB[1]*xyz[1] + xyzToLinSRGB[2]*xyz[2],
			xyzToLinSRGB[3]*xyz[0] + xyzToLinSRGB[4]*xyz[1] + xyzToLinSRGB[5]*xyz[2],
			xyzToLinSRGB[6]*xyz[0] + xyzToLinSRGB[7]*xyz[1] + xyzToLinSRGB[8]*xyz[2],
		}
	}
	readAt := func(lam [3]float64) ([9]float64, bool) {
		var A [9]float64 // A[k][j]: dye j's density at channel k's wavelength
		for k := 0; k < 3; k++ {
			for j, n := range dyes {
				A[k*3+j] = pinterp(d.Dye[n], lam[k])
			}
		}
		return invert3x3(A)
	}
	amountsWith := func(Ainv [9]float64, D [3]float64) [3]float64 {
		var c [3]float64
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				c[j] += Ainv[j*3+k] * D[k]
			}
			c[j] = math.Max(0, c[j])
		}
		return c
	}
	var dmin [3]float64
	for k := range dmin {
		dmin[k] = f.curve[k][0][1]
		for _, p := range f.curve[k] {
			dmin[k] = math.Min(dmin[k], p[1])
		}
	}
	// Grey balance: neutral exposures from 1/16 to 2× mid-grey's.
	greyErr := func(lam [3]float64) float64 {
		Ainv, ok := readAt(lam)
		if !ok {
			return math.Inf(1)
		}
		w := slide(amountsWith(Ainv, dmin))
		var e float64
		for s := -4.0; s <= 1.01; s += 0.5 {
			lg := math.Log10(sensorGrey*f.hWhite) + s*math.Log10(2)
			D := [3]float64{f.density(0, lg), f.density(1, lg), f.density(2, lg)}
			v := slide(amountsWith(Ainv, D))
			if v[0] <= 0 || v[1] <= 0 || v[2] <= 0 {
				return math.Inf(1)
			}
			rg := math.Log(v[0]/w[0]) - math.Log(v[1]/w[1])
			bg := math.Log(v[2]/w[2]) - math.Log(v[1]/w[1])
			e += rg*rg + bg*bg
		}
		return e
	}
	best, lam := math.Inf(1), [3]float64{}
	for lr := 590.0; lr <= 690; lr += 5 {
		for lg := 515.0; lg <= 580; lg += 5 {
			for lb := 420.0; lb <= 475; lb += 5 {
				if e := greyErr([3]float64{lr, lg, lb}); e < best {
					best, lam = e, [3]float64{lr, lg, lb}
				}
			}
		}
	}
	f.readLam, f.greyRMS = lam, math.Sqrt(best/11)
	Ainv, ok := readAt(lam)
	if !ok {
		return nil
	}
	rgbOf := func(D [3]float64) [3]float64 { return slide(amountsWith(Ainv, D)) }
	// The eye adapts to the slide's clearest part: D-min is white.
	white := rgbOf(dmin)
	f.lut = make([]float32, cfLutN*cfLutN*cfLutN*3)
	step := cfLutDMax / (cfLutN - 1)
	parallelRows(cfLutN, func(a0, a1 int) {
		for a := a0; a < a1; a++ {
			for b := 0; b < cfLutN; b++ {
				for c := 0; c < cfLutN; c++ {
					rgb := rgbOf([3]float64{float64(a) * step, float64(b) * step, float64(c) * step})
					i := ((a*cfLutN+b)*cfLutN + c) * 3
					for k := 0; k < 3; k++ {
						f.lut[i+k] = float32(-math.Log10(math.Max(1e-7, rgb[k]/white[k])))
					}
				}
			}
		}
	})
	return f
}

// density is layer k's density at log exposure lg.
func (f *colorFilm) density(k int, lg float64) float64 { return pinterp(f.curve[k], lg) }

// apply takes one pixel of white-balanced sensor layers (relative to the
// metered exposure: 1 = a white of reflectance 1) to the slide's linear
// sRGB.
func (f *colorFilm) apply(r, g, b float32) (float32, float32, float32) {
	x := rpFeatures(r, g, b)
	var h [3]float64
	for k := 0; k < 3; k++ {
		for j := 0; j < 3; j++ {
			h[k] += float64(f.rp[k*3+j]*x[j] + f.rp[9+k*3+j]*x[3+j])
		}
	}
	return f.fromExposure(h)
}

// fromExposure is the film's linear sRGB for its layer exposures h (white
// = 1): each layer's curve, then the table.
func (f *colorFilm) fromExposure(h [3]float64) (float32, float32, float32) {
	var D [3]float64
	for k := 0; k < 3; k++ {
		D[k] = f.density(k, math.Log10(math.Max(h[k]*f.hWhite, 1e-6)))
	}
	// Trilinear in the table.
	var idx [3]int
	var fr [3]float64
	for k := 0; k < 3; k++ {
		x := math.Max(0, math.Min(cfLutN-1-1e-9, D[k]/cfLutDMax*(cfLutN-1)))
		idx[k] = int(x)
		fr[k] = x - float64(idx[k])
	}
	var out [3]float64
	for c := 0; c < 8; c++ {
		w := 1.0
		var j [3]int
		for k := 0; k < 3; k++ {
			if c>>k&1 == 1 {
				j[k], w = idx[k]+1, w*fr[k]
			} else {
				j[k], w = idx[k], w*(1-fr[k])
			}
		}
		i := ((j[0]*cfLutN+j[1])*cfLutN + j[2]) * 3
		for k := 0; k < 3; k++ {
			out[k] += w * float64(f.lut[i+k])
		}
	}
	return float32(math.Pow(10, -out[0])), float32(math.Pow(10, -out[1])), float32(math.Pow(10, -out[2]))
}
