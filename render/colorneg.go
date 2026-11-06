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
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
)

// colorNegPush is one published push family of a colour negative: the
// characteristic curves at an exposure index above the film's ISO, with the
// longer development the maker's sheet gives them (p. 4 of E-4040, for
// example). The curves' log-exposure axis is absolute, so the family is used
// with the film rated at EI: a scene's mid-grey then lands one stop lower per
// stop of push, exactly as in the camera.
type colorNegPush struct {
	EI   float64                 `json:"ei"`
	Src  string                  `json:"src"`
	Char map[string][][2]float64 `json:"char"`
}

type colorNegData struct {
	Film    string                   `json:"film"`
	ISO     float64                  `json:"iso"`
	Src     string                   `json:"src"`
	URL     string                   `json:"url"`
	Char    map[string][][2]float64  `json:"char"`
	Push    map[string]*colorNegPush `json:"push,omitempty"` // key: stops over ISO
	Sens    map[string][][2]float64  `json:"sens"`
	Dmin    [][2]float64             `json:"dmin"`
	Neutral [][2]float64             `json:"neutral"`
	Dye     map[string][][2]float64  `json:"dye"`
	DyeSrc  string                   `json:"dyeSrc"`
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

// negFamily is the characteristic family for a push in stops: the published
// one, or the base family with the film's ISO. A stop the sheet does not
// publish falls back to the base family rather than inventing a curve; the
// editor only offers the published stops.
func negFamily(d *colorNegData, push float64) (map[string][][2]float64, float64) {
	if push != 0 && len(d.Push) > 0 {
		if p := d.Push[strconv.FormatFloat(push, 'g', -1, 64)]; p != nil {
			return p.Char, p.EI
		}
	}
	return d.Char, d.ISO
}

// ColorNegPushes lists a negative's published push families, by stops over
// its ISO, for the editor's Push / pull control.
func ColorNegPushes(key string) []FilmPush {
	d := colorNegs().negs[key]
	if d == nil || len(d.Push) == 0 {
		return nil
	}
	out := make([]FilmPush, 0, len(d.Push))
	for k, p := range d.Push {
		stops, err := strconv.ParseFloat(k, 64)
		if err != nil {
			continue
		}
		out = append(out, FilmPush{Stops: stops, EI: p.EI, Src: p.Src})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stops < out[j].Stops })
	return out
}

// ColorPapers is the library's colour printing papers (the first is the
// default).
func ColorPapers() []string { return colorNegs().order }

// PaperScan is the pseudo-paper that shows a colour negative as a scan
// instead of a print. It is a distinct value, not "": old edits and presets
// saved before the scan existed have no paper at all, and those must keep
// printing on the first colour paper (see colorFilmOnPush), while a scan is
// something the photographer chose explicitly.
const PaperScan = "scan"

// isScanPaper reports whether paper asks for a colour negative's scan.
func isScanPaper(paper string) bool { return paper == PaperScan }

// PaperInfo is a printing paper in the library, for the editor's Paper
// menu: Kind "bw" (Papers, printed at a contrast grade) or "colour"
// (ColorPapers, for colour negatives). The colour list also carries the
// scan pseudo-paper (PaperScan) as its last entry: the UI shows it first,
// because a photographer who wants no print should find it there.
type PaperInfo struct {
	Paper string `json:"paper"`
	Src   string `json:"src"`
	URL   string `json:"url"`
	Kind  string `json:"kind"`
}

// PaperList is every printing paper: black-and-white, then colour (the
// default colour paper first, the scan last).
func PaperList() []PaperInfo {
	var out []PaperInfo
	for _, p := range Papers() {
		out = append(out, PaperInfo{p.Paper, p.Src, p.URL, "bw"})
	}
	for _, name := range ColorPapers() {
		p := colorNegs().papers[name]
		out = append(out, PaperInfo{p.Paper, p.Src, p.URL, "colour"})
	}
	out = append(out, PaperInfo{PaperScan,
		"A generic scan of the colour negative: its Status M densities less its own D-min " +
			"(the orange mask), inverted with one fixed negative gamma and a black level, " +
			"grey-balanced at the film's mid-grey, and mapped by one fixed negative profile; " +
			"no paper is printed",
		"", "colour"})
	return out
}

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

// newColorNeg prepares negative d printed on paper pp for sensor, its
// curve family at push stops over ISO.
func newColorNeg(d *colorNegData, pp *colorPaperData, sensor string, push float64) *colorFilm {
	S := layerSens(sensor)
	if S == nil {
		return nil
	}
	sp := spectral()
	// 1. Sensor layers -> the negative's layer exposures (as a slide).
	char, ei := negFamily(d, push)
	grey := sourceGrey(sensor)
	f := &colorFilm{hWhite: 10 / ei / grey, ei: ei}
	if !fitLayerMap(f, S, d.Sens) {
		return nil
	}
	for k, n := range [3]string{"R", "G", "B"} {
		f.curve[k] = char[n]
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
	lgGrey := math.Log10(grey * f.hWhite)
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
	// The print table for a filtration: the solved one, or the darkroom's
	// own (ColorPrint) on top of it.
	f.printLUT = func(lg [3]float64) []float32 {
		lut := make([]float32, cfLutN*cfLutN*cfLutN*3)
		step := cfLutDMax / (cfLutN - 1)
		parallelRows(cfLutN, func(a0, a1 int) {
			for a := a0; a < a1; a++ {
				for b := 0; b < cfLutN; b++ {
					for c := 0; c < cfLutN; c++ {
						rgb := printRGB([3]float64{float64(a) * step, float64(b) * step, float64(c) * step}, lg, bestPinv)
						i := ((a*cfLutN+b)*cfLutN + c) * 3
						for k := 0; k < 3; k++ {
							lut[i+k] = float32(-math.Log10(math.Max(1e-7, rgb[k])))
						}
					}
				}
			}
		})
		return lut
	}
	f.lut = f.printLUT(bestLg)
	return f
}

// A colour negative shown as a scan, not printed. The scan is a real lab
// scanner's job, not the film's inverse: a Frontier, a Noritsu or a camera
// scan with a standard inversion does not know the film's spectral
// sensitivities or its characteristic curves. It reads the negative's
// densities with its own three fixed channels, subtracts the orange base,
// grey-balances the frame, and maps the result to RGB with one fixed
// conversion. Each film's curves, dye set, layer crosstalk and sensitivities
// therefore show in the scan, which is the point: Portra scans warm and soft,
// Ektar punchy, Fuji negatives have their greens.
//
// The scan is the same kind of 3-D table over the negative's Status M
// densities as the print, so the GPU needs no new shader path: only the table
// differs. The model's densities are read as the scanner's three channel
// readings: Status M is what a negative is measured in, and a scanner's
// channels are close enough (this is stated in REPORT-63).
//
//  1. Read D (Status M, as the print does). Subtract the film's D-min per
//     channel: a scanner measures the rebate, so this is legitimate film
//     knowledge (the orange mask).
//  2. Invert with ONE fixed curve for every film, the film's characteristic
//     slope γ: scene light = 10^(e/γ_s) - B, with e = D - Dmin (the negative's
//     base-relative transmission is 10^-e; its reciprocal is the light).
//     Colour negatives are published with a mid-scale slope of about 0.6 (the
//     library's own sheets: 0.55-0.64), so γ_s = 0.6, one round value. B is
//     one black level, 0.8, just below the base's own light (see scanCurve).
//     This is not the film's own curve inverted: every film goes through the
//     same slope and black level, so a film whose toe, shoulder or layer
//     balance differs keeps that difference.
//  3. One grey balance at the film's mid-grey: a scanner's auto balance makes
//     a neutral grey neutral, so the three channel gains put the film's 18%
//     grey at the same linear level. That is three gains, not a per-layer
//     curve; the film's own layer balance above and below mid-grey survives.
//     The same gains carry the one exposure gain for 18%, so mid-grey lands
//     at linear 0.18.
//  4. ONE fixed 3x3 to the output RGB: the scanner's negative profile. It is
//     derived once, from the average over the library's ten colour negatives
//     of each negative's own 3x3 from its linearised channel readings to
//     linear sRGB (the same reflectance spectra as the layer map, through
//     that negative's curves and base). Averaging is what makes it generic:
//     no one film's dyes are the profile, so a film whose sensitivities or
//     dye crosstalk differ from the average shows its own colour. It is not a
//     per-film inverse.
//  5. Tone: one gentle scanner curve on luminance, the same for every film: a
//     mild contrast of 1.06 around mid-grey in log light (fixed at 0.18, so it
//     cannot move the grey balance) and a soft shoulder above 0.95. It scales
//     all three channels together, so it cannot rotate hue or desaturate. It
//     is this model's own choice, not a Frontier or Noritsu default: no
//     manufacturer's curve was copied.
//  6. The neutral density trajectory is made neutral in the table itself
//     (a 0.055-density tube around it), so the trilinear interpolation between
//     nodes cannot put a cast into a neutral wedge at the toe or the top. A
//     near-neutral guard (C* below 8) is the safety net the tube's fade needs.
const (
	scanNegGamma     = 0.6   // the colour negative's published mid-scale slope
	scanBlackPoint   = 0.8   // scanner black level, below the film base
	scanMidGrey      = 0.18  // linear, the print model's mid-grey
	scanToneContrast = 1.06  // gentle S around mid-grey, fixed at 0.18
	scanShoulder     = 0.95  // soft shoulder above this
	scanLinMax       = 1e6   // stop a blank sky or a specular from overflowing
	scanNeutralTube  = 0.055 // density radius of the neutral tube
	scanNeutralCore  = 0.045
	scanLutN         = 65 // samples per density axis
)

// scanCurve is the scanner's one fixed inversion for the density above the
// film base: scene linear = 10^(e/γ_s) - B, with e = D - Dmin. The negative's
// base-relative transmission is 10^-e; raising it to -1/γ_s recovers the
// light that exposed the film (the negative's published slope is γ_s), and B
// is the scanner's black level. B sits a little below the film base's own
// light (0.8, not 1.0): a black level exactly at the base clips the film's
// toe, where a real negative's density rises slowly, and sent the darkest 5%
// of a normal frame to pure black; leaving a little of the base in the black
// keeps the toe's tones. It is one constant for every film.
func scanCurve(e float64) float64 {
	if e <= 0 {
		return 1 - scanBlackPoint
	}
	return math.Pow(10, e/scanNegGamma) - scanBlackPoint
}

// scanTone is the scanner's gentle tone curve on luminance Y: a mild contrast
// fixed at mid-grey, then a soft shoulder. Monotone, so it cannot invert the
// image; it acts on luminance only.
func scanTone(Y float64) float64 {
	if Y <= 0 {
		return 0
	}
	y := scanMidGrey * math.Pow(Y/scanMidGrey, scanToneContrast)
	if y > scanShoulder {
		y = scanShoulder + (1-scanShoulder)*(1-math.Exp(-(y-scanShoulder)/(1-scanShoulder)))
	}
	return y
}

// scanMatrixFor is one negative's own 3x3 from its linearised channel
// readings to linear sRGB. The readings are exactly the ones newColorScan
// makes: the scene's linear sRGB through the same sRGB->layers map the render
// uses, that negative's characteristic curves, D-min removed, the fixed γ_s
// inverted, and the film's mid-grey balanced to 0.18. It is fitted on the
// same reflectance spectra the layer map is fitted on.
func scanMatrixFor(d *colorNegData) ([9]float64, bool) {
	sp := spectral()
	rf := &colorFilm{hWhite: 10 / d.ISO / scanMidGrey}
	if !fitLayerMap(rf, layerSens("sRGB"), d.Sens) {
		return [9]float64{}, false
	}
	var white [3]float64
	for i, l := range sp.lams {
		c := cmf1931(l)
		for k := 0; k < 3; k++ {
			white[k] += c[k] * sp.illum[i]
		}
	}
	var wrgb [3]float64
	for k := 0; k < 3; k++ {
		wrgb[k] = xyzToLinSRGB[k*3]*white[0] + xyzToLinSRGB[k*3+1]*white[1] + xyzToLinSRGB[k*3+2]*white[2]
	}
	var dmin [3]float64
	for k, n := range [3]string{"R", "G", "B"} {
		dmin[k] = d.Char[n][0][1]
		for _, p := range d.Char[n] {
			dmin[k] = math.Min(dmin[k], p[1])
		}
	}
	hw := 10 / d.ISO / scanMidGrey
	var Lg [3]float64
	for k, n := range [3]string{"R", "G", "B"} {
		Dg := pinterp(d.Char[n], math.Log10(scanMidGrey*hw))
		Lg[k] = scanCurve(Dg - dmin[k])
	}
	train := append(append([][]float64{}, fitSpectra()...), make([]float64, len(sp.lams)))
	for i := range train[len(train)-1] {
		train[len(train)-1][i] = 1
	}
	var ata, atb [9]float64
	for _, r := range train {
		var xyz [3]float64
		for i := range sp.lams {
			e := sp.illum[i] * r[i]
			c := cmf1931(sp.lams[i])
			for k := 0; k < 3; k++ {
				xyz[k] += c[k] * e
			}
		}
		var rgb [3]float64
		for k := 0; k < 3; k++ {
			rgb[k] = (xyzToLinSRGB[k*3]*xyz[0] + xyzToLinSRGB[k*3+1]*xyz[1] + xyzToLinSRGB[k*3+2]*xyz[2]) / wrgb[k]
		}
		x := rpFeatures(float32(rgb[0]), float32(rgb[1]), float32(rgb[2]))
		var h [3]float64
		for k := 0; k < 3; k++ {
			for j := 0; j < 3; j++ {
				h[k] += float64(rf.rp[k*3+j])*float64(x[j]) + float64(rf.rp[9+k*3+j])*float64(x[3+j])
			}
		}
		var L [3]float64
		for k, n := range [3]string{"R", "G", "B"} {
			D := pinterp(d.Char[n], math.Log10(math.Max(h[k]*hw, 1e-6)))
			L[k] = scanMidGrey * scanCurve(D-dmin[k]) / math.Max(Lg[k], 1e-9)
		}
		for a := 0; a < 3; a++ {
			for b := 0; b < 3; b++ {
				ata[a*3+b] += L[a] * L[b]
			}
		}
		for k := 0; k < 3; k++ {
			for j := 0; j < 3; j++ {
				atb[k*3+j] += rgb[k] * L[j]
			}
		}
	}
	Ainv, ok := invert3x3(ata)
	if !ok {
		return [9]float64{}, false
	}
	var M [9]float64
	for k := 0; k < 3; k++ {
		for i := 0; i < 3; i++ {
			M[k*3+i] = atb[k*3]*Ainv[i] + atb[k*3+1]*Ainv[3+i] + atb[k*3+2]*Ainv[6+i]
		}
	}
	for k := 0; k < 3; k++ {
		s := M[k*3] + M[k*3+1] + M[k*3+2]
		if math.Abs(s) < 1e-9 {
			return [9]float64{}, false
		}
		for i := 0; i < 3; i++ {
			M[k*3+i] /= s
		}
	}
	return M, true
}

// scanProfile is the scanner's one fixed negative profile: the average of the
// library's ten negatives, each fitted by scanMatrixFor. It was fitted once on
// the task-63 library and is pinned here rather than recomputed from the
// current data. A scanner's negative profile is a property of the scanner (its
// channels and the one inversion), not of whichever film sheet was corrected
// last: recomputing it after the task-64 curve corrections would move every
// other film's scan by a few 1e-3, although no other film changed. The numbers
// are that task-63 average (REPORT-63 §2); after the task-64 corrections the
// ten-film average would differ by at most 0.0068.
var scanProfile = func() [9]float64 {
	return [9]float64{
		1.3534365036171707, -0.27231520041960572, -0.081121303197565039,
		-0.044727459328964034, 1.1300816603296684, -0.085354201000704441,
		-0.054605683087417527, -0.025311947254529784, 1.0799176303419473,
	}
}

// scanAB is the CIELAB a*, b* of a linear-sRGB value, for the scan's
// near-neutral guard.
func scanAB(r, g, b float64) (a, bb float64) {
	x := 0.4124*r + 0.3576*g + 0.1805*b
	y := 0.2126*r + 0.7152*g + 0.0722*b
	z := 0.0193*r + 0.1192*g + 0.9505*b
	f := func(v float64) float64 {
		if v > 216.0/24389 {
			return math.Cbrt(v)
		}
		return (24389.0/27*v + 16) / 116
	}
	fx, fy, fz := f(x/0.9505), f(y), f(z/1.089)
	return 500 * (fx - fy), 200 * (fy - fz)
}

// newColorScan prepares negative d as a scan for sensor, at push stops.
func newColorScan(d *colorNegData, sensor string, push float64) *colorFilm {
	S := layerSens(sensor)
	if S == nil {
		return nil
	}
	char, ei := negFamily(d, push)
	grey := sourceGrey(sensor)
	f := &colorFilm{hWhite: 10 / ei / grey, ei: ei}
	if !fitLayerMap(f, S, d.Sens) {
		return nil
	}
	for k, n := range [3]string{"R", "G", "B"} {
		f.curve[k] = char[n]
	}
	var dmin [3]float64
	for k := range dmin {
		dmin[k] = f.curve[k][0][1]
		for _, p := range f.curve[k] {
			dmin[k] = math.Min(dmin[k], p[1])
		}
	}
	// 2 + 3. The fixed inversion, and the film's grey balance at mid-grey,
	// folded together into the one exposure gain for 18%.
	lgGrey := math.Log10(grey * f.hWhite)
	var gain [3]float64
	for k := 0; k < 3; k++ {
		Dg := f.density(k, lgGrey)
		Lg := scanCurve(Dg - dmin[k])
		gain[k] = scanMidGrey / math.Max(Lg, 1e-9)
	}
	prof := scanProfile()
	// core: the negative's densities -> the scan's linear sRGB (steps 2-5).
	core := func(D [3]float64) [3]float64 {
		var L [3]float64
		for k := 0; k < 3; k++ {
			L[k] = scanCurve(D[k]-dmin[k]) * gain[k]
			if L[k] > scanLinMax {
				L[k] = scanLinMax
			}
		}
		var rgb [3]float64
		for k := 0; k < 3; k++ {
			rgb[k] = prof[k*3]*L[0] + prof[k*3+1]*L[1] + prof[k*3+2]*L[2]
		}
		Y := 0.2126*rgb[0] + 0.7152*rgb[1] + 0.0722*rgb[2]
		if Y > 0 {
			Y2 := scanTone(Y)
			for k := range rgb {
				rgb[k] *= Y2 / Y
			}
		}
		for k := range rgb {
			if rgb[k] < 0 {
				rgb[k] = 0
			}
		}
		return rgb
	}
	// 6. The neutral density trajectory, and its luminance through steps 2-5:
	// the tube pulls the table's cells around it onto this neutral value, so
	// trilinear interpolation cannot put a cast into a neutral wedge.
	lo, hi := f.curve[0][0][0], f.curve[0][len(f.curve[0])-1][0]
	for k := 1; k < 3; k++ {
		if x := f.curve[k][0][0]; x < lo {
			lo = x
		}
		if x := f.curve[k][len(f.curve[k])-1][0]; x > hi {
			hi = x
		}
	}
	const scanNeutralN = 256
	nD := make([][3]float64, scanNeutralN)
	nh := make([]float64, scanNeutralN)
	for i := 0; i < scanNeutralN; i++ {
		lg := lo + (hi-lo)*float64(i)/(scanNeutralN-1)
		for k := 0; k < 3; k++ {
			nD[i][k] = f.density(k, lg)
		}
		rgb := core(nD[i])
		nh[i] = 0.2126*rgb[0] + 0.7152*rgb[1] + 0.0722*rgb[2]
	}
	f.lutN = scanLutN
	f.lut = make([]float32, scanLutN*scanLutN*scanLutN*3)
	step := cfLutDMax / (scanLutN - 1)
	parallelRows(scanLutN, func(a0, a1 int) {
		for a := a0; a < a1; a++ {
			for b := 0; b < scanLutN; b++ {
				for c := 0; c < scanLutN; c++ {
					i := ((a*scanLutN+b)*scanLutN + c) * 3
					D := [3]float64{float64(a) * step, float64(b) * step, float64(c) * step}
					rgb := core(D)
					best, bestI := math.Inf(1), 0
					for j := range nD {
						d := 0.0
						for k := 0; k < 3; k++ {
							if x := math.Abs(D[k] - nD[j][k]); x > d {
								d = x
							}
						}
						if d < best {
							best, bestI = d, j
						}
					}
					if best < scanNeutralTube {
						w := 1.0
						if best > scanNeutralCore {
							u := (best - scanNeutralCore) / (scanNeutralTube - scanNeutralCore)
							w = 1 - u*u*(3-2*u)
						}
						for k := range rgb {
							rgb[k] = rgb[k]*(1-w) + nh[bestI]*w
						}
					}
					// Safety net for the toe and the top, where the curve's
					// shoulder compresses density: a cell within C* 4 of
					// neutral is a neutral tone, not a colour (skin is around
					// 15-20), so pull it onto its luminance; 4-8 fades out.
					// This is on perceptible chroma, so a faintly tinted scene
					// colour survives, and it cannot touch a saturated one.
					if a, b := scanAB(rgb[0], rgb[1], rgb[2]); math.Hypot(a, b) < 8 {
						C := math.Hypot(a, b)
						w := 1.0
						if C > 4 {
							u := (C - 4) / 4
							w = 1 - u*u*(3-2*u)
						}
						Y := 0.2126*rgb[0] + 0.7152*rgb[1] + 0.0722*rgb[2]
						for k := range rgb {
							rgb[k] = rgb[k]*(1-w) + Y*w
						}
					}
					for k := range rgb {
						f.lut[i+k] = float32(-math.Log10(math.Max(1e-7, rgb[k])))
					}
				}
			}
		}
	})
	return f
}

// ColorPrint is the enlarger's colour head on top of the filtration that
// prints mid-grey neutral: Y, M and C filters in CC units (density × 100;
// a dichroic head's filters each hold back one paper layer's light: Y the
// blue-sensitive, M the green-, C the red-sensitive) and the print
// exposure in stops. As in the darkroom, adding a colour's filter takes
// that colour out of the print (Y holds back blue light: the blue-
// sensitive layer forms less yellow dye), and more exposure prints darker.
type ColorPrint struct{ Y, M, C, Exp float64 }

// key identifies cp in caches.
func (cp ColorPrint) key() string { return fmt.Sprintf("cp:%g,%g,%g,%g", cp.Y, cp.M, cp.C, cp.Exp) }

// offsets is the log10 gain per paper layer (R, G, B) of the print.
func (cp ColorPrint) offsets() [3]float64 {
	e := cp.Exp * math.Log10(2)
	return [3]float64{e - cp.C/100, e - cp.M/100, e - cp.Y/100}
}
