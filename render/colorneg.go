package render

// Colour negative film printed on colour paper, from the makers' data
// (colorfilms.json "negatives" and "papers"), as a darkroom does it:
//
//  1. The scene exposes the negative's three layers (its spectral
//     sensitivities, the sensor generation's silicon model, as for slides),
//     mid-grey at 10/ISO lux·s; each layer's Status M curve gives its
//     density.
//  2. Status M reads a negative at its dyes' peaks (it is made for
//     printing): the densities above D-min give the three dye amounts. The
//     negative passes 10^-(D-min(λ) + Σ amount·ε(λ)): the orange mask and
//     the dyes. (The dyes' shapes come from a sister Kodak motion-picture
//     negative; NeutralFit reports how well they rebuild the film's own
//     published midscale-neutral spectrum.)
//  3. The enlarger: a 3200 K lamp through the negative onto the paper,
//     each paper layer exposed through its spectral sensitivity. The
//     filtration (a gain per layer, i.e. Y/M/C) is solved so the negative's
//     mid-grey prints as a neutral 18% grey, as a printer sets it.
//  4. The paper: its Status A curves give the print's densities; read at
//     one wavelength per layer, fitted to the paper's grey balance (a
//     neutral negative scale prints neutral), they give the paper's dye
//     amounts; the print reflects 10^-(Σ amount·ε(λ)) of 5500 K daylight,
//     relative to the paper's white, seen by the CIE 1931 observer.
//
// Steps 2-4 are baked into the same 3-D table as a slide's, over the
// negative's three densities.

import (
	"encoding/json"
	"math"
	"sync"
)

type colorNegData struct {
	Film    string                  `json:"film"`
	ISO     float64                 `json:"iso"`
	Src     string                  `json:"src"`
	URL     string                  `json:"url"`
	Char    map[string][][2]float64 `json:"char"`
	Sens    map[string][][2]float64 `json:"sens"`
	Dmin    [][2]float64            `json:"dmin"`
	Neutral [][2]float64            `json:"neutral"`
	Dye     map[string][][2]float64 `json:"dye"`
	DyeSrc  string                  `json:"dyeSrc"`
}

type colorPaperData struct {
	Paper string                  `json:"paper"`
	Src   string                  `json:"src"`
	URL   string                  `json:"url"`
	Char  map[string][][2]float64 `json:"char"`
	Sens  map[string][][2]float64 `json:"sens"`
	Dye   map[string][][2]float64 `json:"dye"`
}

var colorNegs = sync.OnceValue(func() (out struct {
	negs   map[string]*colorNegData
	papers map[string]*colorPaperData
	order  []string
}) {
	var d struct {
		Negatives []*colorNegData   `json:"negatives"`
		Papers    []*colorPaperData `json:"papers"`
	}
	if err := json.Unmarshal(colorFilmsJSON, &d); err != nil {
		panic("colorfilms.json: " + err.Error())
	}
	out.negs, out.papers = map[string]*colorNegData{}, map[string]*colorPaperData{}
	for _, n := range d.Negatives {
		out.negs[n.Film] = n
	}
	for _, p := range d.Papers {
		out.papers[p.Paper] = p
		out.order = append(out.order, p.Paper)
	}
	return
})

// IsColorNegative reports whether the library film named key has the
// colour-negative model.
func IsColorNegative(key string) bool { _, ok := colorNegs().negs[key]; return ok }

// ColorPapers is the library's colour printing papers (the first is the
// default).
func ColorPapers() []string { return colorNegs().order }

// NegativeFit is how a negative's borrowed dye shapes rebuild its own
// published spectra: the dye amounts of the midscale neutral and the RMS
// density error over 400-700 nm.
type NegativeFit struct {
	Amounts [3]float64 // C, M, Y
	RMS     float64
}

// negDyes returns the negative's dye shapes (C, M, Y) and D-min on the
// spectral grid.
func negDyes(d *colorNegData) ([][3]float64, []float64) {
	sp := spectral()
	eps := make([][3]float64, len(sp.lams))
	dmin := make([]float64, len(sp.lams))
	for i, l := range sp.lams {
		for j, n := range [3]string{"C", "M", "Y"} {
			eps[i][j] = math.Max(0, pinterp(d.Dye[n], l))
		}
		dmin[i] = pinterp(d.Dmin, l)
	}
	return eps, dmin
}

// FitNeutral fits the negative's midscale-neutral spectrum (minus D-min)
// with its dye shapes (non-negative least squares by projected iteration).
func FitNeutral(key string) (NegativeFit, bool) {
	d := colorNegs().negs[key]
	if d == nil {
		return NegativeFit{}, false
	}
	sp := spectral()
	eps, dmin := negDyes(d)
	var ata [9]float64
	var atb [3]float64
	var rows [][4]float64
	for i, l := range sp.lams {
		if l < 400 || l > 700 {
			continue
		}
		y := pinterp(d.Neutral, l) - dmin[i]
		e := eps[i]
		rows = append(rows, [4]float64{e[0], e[1], e[2], y})
		for a := 0; a < 3; a++ {
			atb[a] += e[a] * y
			for b := 0; b < 3; b++ {
				ata[a*3+b] += e[a] * e[b]
			}
		}
	}
	inv, ok := invert3x3(ata)
	if !ok {
		return NegativeFit{}, false
	}
	var f NegativeFit
	for a := 0; a < 3; a++ {
		f.Amounts[a] = math.Max(0, inv[a*3]*atb[0]+inv[a*3+1]*atb[1]+inv[a*3+2]*atb[2])
	}
	var e2 float64
	for _, r := range rows {
		v := r[0]*f.Amounts[0] + r[1]*f.Amounts[1] + r[2]*f.Amounts[2] - r[3]
		e2 += v * v
	}
	f.RMS = math.Sqrt(e2 / float64(len(rows)))
	return f, true
}

// newColorNeg prepares negative d printed on paper pp for sensor.
func newColorNeg(d *colorNegData, pp *colorPaperData, sensor string) *colorFilm {
	S := layerSens(sensor)
	if S == nil {
		return nil
	}
	sp := spectral()
	// 1. Sensor layers -> the negative's layer exposures (as a slide).
	f := &colorFilm{hWhite: 10 / d.ISO / sensorGrey}
	if !fitLayerMatrix(f, S, d.Sens) {
		return nil
	}
	for k, n := range [3]string{"R", "G", "B"} {
		f.curve[k] = d.Char[n]
	}
	// 2. Status M reads at the dyes' peaks: D - D_min = A·c.
	eps, dmin := negDyes(d)
	var peak [3]float64
	for j, n := range [3]string{"C", "M", "Y"} {
		best := -1.0
		for _, p := range d.Dye[n] {
			if p[1] > best {
				best, peak[j] = p[1], p[0]
			}
		}
	}
	var A [9]float64
	for k := 0; k < 3; k++ {
		for j, n := range [3]string{"C", "M", "Y"} {
			A[k*3+j] = pinterp(d.Dye[n], peak[k])
		}
	}
	Ainv, ok := invert3x3(A)
	if !ok {
		return nil
	}
	var dminM [3]float64 // D-min in each channel = the curves' lowest
	for k := range dminM {
		dminM[k] = f.curve[k][0][1]
		for _, p := range f.curve[k] {
			dminM[k] = math.Min(dminM[k], p[1])
		}
	}
	negT := func(D [3]float64) []float64 {
		var c [3]float64
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				c[j] += Ainv[j*3+k] * (D[k] - dminM[k])
			}
			c[j] = math.Max(0, c[j])
		}
		t := make([]float64, len(sp.lams))
		for i := range sp.lams {
			t[i] = math.Pow(10, -(dmin[i] + c[0]*eps[i][0] + c[1]*eps[i][1] + c[2]*eps[i][2]))
		}
		return t
	}
	// 3. Enlarger: 3200 K through the negative onto the paper's layers.
	lamp := make([]float64, len(sp.lams))
	for i, l := range sp.lams {
		lm := l * 1e-9
		lamp[i] = 1 / (math.Pow(lm, 5) * (math.Exp(1.4388e-2/(lm*3200)) - 1))
	}
	ps := make([][3]float64, len(sp.lams))
	var pnorm [3]float64
	for i, l := range sp.lams {
		for k, n := range [3]string{"R", "G", "B"} {
			ps[i][k] = sensAt(pp.Sens[n], l)
			pnorm[k] += ps[i][k] * lamp[i]
		}
	}
	paperExp := func(t []float64) [3]float64 { // relative to the open lamp
		var e [3]float64
		for i := range sp.lams {
			for k := 0; k < 3; k++ {
				e[k] += ps[i][k] * lamp[i] * t[i]
			}
		}
		for k := range e {
			e[k] /= pnorm[k]
		}
		return e
	}
	pcurve := [3][][2]float64{pp.Char["R"], pp.Char["G"], pp.Char["B"]}
	var pdmin [3]float64
	for k := range pdmin {
		pdmin[k] = pcurve[k][0][1]
		for _, p := range pcurve[k] {
			pdmin[k] = math.Min(pdmin[k], p[1])
		}
	}
	// 4. Paper dyes in reflection, viewed in daylight.
	peps := make([][3]float64, len(sp.lams))
	for i, l := range sp.lams {
		for j, n := range [3]string{"C", "M", "Y"} {
			peps[i][j] = math.Max(0, pinterp(pp.Dye[n], l))
		}
	}
	cmfE := make([][3]float64, len(sp.lams))
	for i, l := range sp.lams {
		c := cmf1931(l)
		for k := range c {
			cmfE[i][k] = c[k] * sp.illum[i]
		}
	}
	toRGB := func(xyz [3]float64) [3]float64 {
		return [3]float64{
			xyzToLinSRGB[0]*xyz[0] + xyzToLinSRGB[1]*xyz[1] + xyzToLinSRGB[2]*xyz[2],
			xyzToLinSRGB[3]*xyz[0] + xyzToLinSRGB[4]*xyz[1] + xyzToLinSRGB[5]*xyz[2],
			xyzToLinSRGB[6]*xyz[0] + xyzToLinSRGB[7]*xyz[1] + xyzToLinSRGB[8]*xyz[2],
		}
	}
	var wxyz [3]float64
	for i := range sp.lams {
		for k := 0; k < 3; k++ {
			wxyz[k] += cmfE[i][k]
		}
	}
	white := toRGB(wxyz)
	readAt := func(lam [3]float64) ([9]float64, bool) {
		var P [9]float64
		for k := 0; k < 3; k++ {
			for j, n := range [3]string{"C", "M", "Y"} {
				P[k*3+j] = pinterp(pp.Dye[n], lam[k])
			}
		}
		return invert3x3(P)
	}
	// printRGB is the print's linear sRGB for negative densities D, with the
	// filtration (log10 gains) lg and the paper's reading inverse Pinv.
	printRGB := func(D [3]float64, lg [3]float64, Pinv [9]float64) [3]float64 {
		e := paperExp(negT(D))
		var Dp [3]float64
		for k := 0; k < 3; k++ {
			Dp[k] = pinterp(pcurve[k], math.Log10(math.Max(e[k], 1e-12))+lg[k]) - pdmin[k]
		}
		var a [3]float64
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				a[j] += Pinv[j*3+k] * Dp[k]
			}
			a[j] = math.Max(0, a[j])
		}
		var xyz [3]float64
		for i := range sp.lams {
			r := math.Pow(10, -(a[0]*peps[i][0] + a[1]*peps[i][1] + a[2]*peps[i][2]))
			for k := 0; k < 3; k++ {
				xyz[k] += cmfE[i][k] * r
			}
		}
		rgb := toRGB(xyz)
		for k := range rgb {
			rgb[k] /= white[k]
		}
		return rgb
	}
	negAt := func(lgH float64) [3]float64 {
		return [3]float64{f.density(0, lgH), f.density(1, lgH), f.density(2, lgH)}
	}
	lgGrey := math.Log10(sensorGrey * f.hWhite)
	// filtration solves print(mid-grey) = 0.18 grey (Newton on log gains).
	filtration := func(Pinv [9]float64) ([3]float64, bool) {
		lg := [3]float64{0, 0, 0}
		D := negAt(lgGrey)
		// Start where each layer's exposure reaches the paper's mid density.
		e := paperExp(negT(D))
		for k := 0; k < 3; k++ {
			lg[k] = pcurve[k][len(pcurve[k])/2][0] - math.Log10(math.Max(e[k], 1e-12))
		}
		for it := 0; it < 30; it++ {
			v := printRGB(D, lg, Pinv)
			var r [3]float64
			small := true
			for k := range r {
				r[k] = math.Log10(math.Max(v[k], 1e-9)) - math.Log10(0.18)
				if math.Abs(r[k]) > 1e-4 {
					small = false
				}
			}
			if small {
				return lg, true
			}
			var J [9]float64
			for j := 0; j < 3; j++ {
				l2 := lg
				l2[j] += 1e-3
				v2 := printRGB(D, l2, Pinv)
				for k := 0; k < 3; k++ {
					J[k*3+j] = (math.Log10(math.Max(v2[k], 1e-9)) - math.Log10(math.Max(v[k], 1e-9))) / 1e-3
				}
			}
			Ji, ok := invert3x3(J)
			if !ok {
				return lg, false
			}
			for j := 0; j < 3; j++ {
				lg[j] -= Ji[j*3]*r[0] + Ji[j*3+1]*r[1] + Ji[j*3+2]*r[2]
			}
		}
		return lg, false
	}
	greyErr := func(lg [3]float64, Pinv [9]float64) float64 {
		var e float64
		for s := -3.0; s <= 3.01; s += 1 {
			v := printRGB(negAt(lgGrey+s*math.Log10(2)), lg, Pinv)
			if v[0] <= 0 || v[1] <= 0 || v[2] <= 0 {
				return math.Inf(1)
			}
			rg, bg := math.Log(v[0]/v[1]), math.Log(v[2]/v[1])
			e += rg*rg + bg*bg
		}
		return e
	}
	best := math.Inf(1)
	var bestLam, bestLg [3]float64
	var bestPinv [9]float64
	for lr := 600.0; lr <= 680; lr += 10 {
		for lgw := 520.0; lgw <= 570; lgw += 10 {
			for lb := 430.0; lb <= 470; lb += 10 {
				lam := [3]float64{lr, lgw, lb}
				Pinv, ok := readAt(lam)
				if !ok {
					continue
				}
				lg, ok := filtration(Pinv)
				if !ok {
					continue
				}
				if e := greyErr(lg, Pinv); e < best {
					best, bestLam, bestLg, bestPinv = e, lam, lg, Pinv
				}
			}
		}
	}
	if math.IsInf(best, 1) {
		return nil
	}
	f.readLam, f.greyRMS, f.filtration = bestLam, math.Sqrt(best/7), bestLg
	f.lut = make([]float32, cfLutN*cfLutN*cfLutN*3)
	step := cfLutDMax / (cfLutN - 1)
	parallelRows(cfLutN, func(a0, a1 int) {
		for a := a0; a < a1; a++ {
			for b := 0; b < cfLutN; b++ {
				for c := 0; c < cfLutN; c++ {
					rgb := printRGB([3]float64{float64(a) * step, float64(b) * step, float64(c) * step}, bestLg, bestPinv)
					i := ((a*cfLutN+b)*cfLutN + c) * 3
					for k := 0; k < 3; k++ {
						f.lut[i+k] = float32(-math.Log10(math.Max(1e-7, rgb[k])))
					}
				}
			}
		}
	})
	return f
}

// fitLayerMatrix fits f.m: the sensor's white-balanced layers (sensitivities
// S) -> the film's layer exposures (log sensitivities sens R, G, B), least
// squares over the reflectance set in daylight, both normalised by white.
func fitLayerMatrix(f *colorFilm, S [][3]float64, sens map[string][][2]float64) bool {
	sp := spectral()
	var fs [][3]float64
	for _, l := range sp.lams {
		fs = append(fs, [3]float64{sensAt(sens["R"], l), sensAt(sens["G"], l), sensAt(sens["B"], l)})
	}
	var ws, wf [3]float64
	for i := range sp.lams {
		for k := 0; k < 3; k++ {
			ws[k] += S[i][k] * sp.illum[i]
			wf[k] += fs[i][k] * sp.illum[i]
		}
	}
	var ata, atb [3][3]float64
	for _, r := range sp.refl {
		var p, h [3]float64
		for i := range sp.lams {
			e := sp.illum[i] * r[i]
			for k := 0; k < 3; k++ {
				p[k] += S[i][k] * e
				h[k] += fs[i][k] * e
			}
		}
		for k := 0; k < 3; k++ {
			p[k] /= ws[k]
			h[k] /= wf[k]
		}
		for a := 0; a < 3; a++ {
			for b := 0; b < 3; b++ {
				ata[a][b] += p[a] * p[b]
				atb[a][b] += h[a] * p[b]
			}
		}
	}
	inv, ok := invert3x3([9]float64{ata[0][0], ata[0][1], ata[0][2], ata[1][0], ata[1][1], ata[1][2], ata[2][0], ata[2][1], ata[2][2]})
	if !ok {
		return false
	}
	for a := 0; a < 3; a++ {
		for b := 0; b < 3; b++ {
			var v float64
			for k := 0; k < 3; k++ {
				v += atb[a][k] * inv[k*3+b]
			}
			f.m[a*3+b] = float32(v)
		}
	}
	return true
}
