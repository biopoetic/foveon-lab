package render

// Task 74: the dye-neutral check the sheets themselves state.
//
// Every Kodak Ektachrome sheet says:
//
//	"Normalized dyes to form a visual neutral density of 1.0 for a viewing
//	 illuminant of 5000 K."
//
// The Kodachrome E-55 sheet says the same for 3200 K, and EIR's TI-2323 for
// D5000. The check below puts the sheet's own dye set c = (1,1,1) through the
// app's viewing path under that illuminant and the CIE 1931 2° observer:
//
//   - a*, b*: the app's linear-sRGB CIELAB (D65/2°), with the film's D-min
//     rendered under the sheet's illuminant as the adapted white. This is the
//     viewing model the slides are rendered in; a residual here is a residual
//     in that model, not a densitometer reading.
//   - visual density: the absolute Y-weighted density of the same set under
//     the sheet's illuminant, -log10(Y(c=1)/Y(c=0)). The sheet says this must
//     be 1.0, which is an independent check on the density axis of the trace.
//
// The illuminant is a Planck blackbody at the stated temperature. D5000 is not
// a blackbody temperature; EIR is reported for completeness and is out of
// scope, and its D5000 is taken as a 5000 K blackbody (the same grid the app
// renders with). The observer is the app's Wyman/Sloan/Shirley fit of the CIE
// 1931 2° colour-matching functions, which is the observer the render uses.
//
// Task 73's check used 5500 K for every film and did not check the level. It
// also used the film's *read path* for the D-min white, so the non-Status-A
// films had no integrated inverse at all. This check uses the integrated
// Status A inverse for every film's D-min, which is what makes the six films
// comparable with the four kept ones.

import (
	"crypto/sha256"
	"math"
	"sort"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// t74Check is one sheet's dye-neutral result.
type t74Check struct {
	Film string
	Temp float64 // the sheet's viewing illuminant, K
	L    float64 // CIELAB of c = (1,1,1) in the app's viewing model
	A    float64
	B    float64
	D    float64 // absolute visual density, target 1.0
	// The same set with the illuminant itself as the white reference: the
	// colour of the dyes alone, before the film's D-min adaptation. It is the
	// diagnostic that separates "the trace is wrong" from "our viewing model
	// is not the sheet's".
	AP float64
	BP float64
	DP float64
}

// t74Temp is the sheet's stated viewing illuminant. Only E-55 (the three
// Kodachromes) is 3200 K; every Ektachrome sheet says 5000 K. EIR's TI-2323
// says D5000, reported but out of scope.
func t74Temp(key string) float64 {
	if len(key) >= 9 && key[:9] == "Kodak Kod" {
		return 3200
	}
	return 5000
}

// t74Planck is Planck's law on the app's spectral grid, the same normalisation
// the render uses (only ratios matter).
func t74Planck(l, T float64) float64 {
	lm := l * 1e-9
	return 1 / (math.Pow(lm, 5) * (math.Exp(1.4388e-2/(lm*T)) - 1))
}

// t74DyeEps samples a film's three dye curves on the app's spectral grid.
func t74DyeEps(d *colorFilmData) [][3]float64 {
	sp := spectral()
	names := [3]string{"C", "M", "Y"}
	eps := make([][3]float64, len(sp.lams))
	for i, l := range sp.lams {
		for j, n := range names {
			eps[i][j] = math.Max(0, pinterp(d.Dye[n], l))
		}
	}
	return eps
}

// t74XYZ is the illuminant-weighted tristimulus of the dye set c.
func t74XYZ(eps [][3]float64, illum []float64, c [3]float64) [3]float64 {
	sp := spectral()
	var xyz [3]float64
	for i, l := range sp.lams {
		t := math.Pow(10, -(c[0]*eps[i][0] + c[1]*eps[i][1] + c[2]*eps[i][2]))
		cm := cmf1931(l)
		for k := 0; k < 3; k++ {
			xyz[k] += cm[k] * illum[i] * t
		}
	}
	return xyz
}

// t74SlideRGB is the app's slide path: the transmitted spectrum of c through
// the sheet's illuminant, seen by the CIE 1931 2° observer, in linear sRGB.
func t74SlideRGB(eps [][3]float64, illum []float64, c [3]float64) [3]float64 {
	xyz := t74XYZ(eps, illum, c)
	return [3]float64{
		xyzToLinSRGB[0]*xyz[0] + xyzToLinSRGB[1]*xyz[1] + xyzToLinSRGB[2]*xyz[2],
		xyzToLinSRGB[3]*xyz[0] + xyzToLinSRGB[4]*xyz[1] + xyzToLinSRGB[5]*xyz[2],
		xyzToLinSRGB[6]*xyz[0] + xyzToLinSRGB[7]*xyz[1] + xyzToLinSRGB[8]*xyz[2],
	}
}

// t74LabXYZ is CIELAB from tristimulus values against an explicit white.
func t74LabXYZ(xyz, white [3]float64) (L, a, b float64) {
	f := func(t float64) float64 {
		if t > 216.0/24389 {
			return math.Cbrt(t)
		}
		return (24389.0/27*t + 16) / 116
	}
	fx, fy, fz := f(xyz[0]/white[0]), f(xyz[1]/white[1]), f(xyz[2]/white[2])
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

// t74DminAmounts solves the integrated Status A densities of the film's curve
// minima. This is the D-min white the app adapts to, for every film; task 73
// could only do it for the films already on the integrated read.
func t74DminAmounts(d *colorFilmData, dmin [3]float64) [3]float64 {
	f := &colorFilm{}
	f.curve[0], f.curve[1], f.curve[2] = d.Char["R"], d.Char["G"], d.Char["B"]
	f.readLam = [3]float64{620, 530, 440}
	var A [9]float64
	for k := 0; k < 3; k++ {
		for j, n := range [3]string{"C", "M", "Y"} {
			A[k*3+j] = pinterp(d.Dye[n], f.readLam[k])
		}
	}
	f.aInv, _ = invert3x3(A)
	lamsA := statusAGrid().lams
	f.integrated = true
	f.eps2 = make([][3]float64, len(lamsA))
	for i, l := range lamsA {
		for j, n := range [3]string{"C", "M", "Y"} {
			f.eps2[i][j] = math.Max(0, pinterp(d.Dye[n], l)) * log2of10
		}
	}
	return f.amountsStatusA(dmin)
}

// dyeNeutralCheckData is the check on one film's sheet data.
func dyeNeutralCheckData(d *colorFilmData) t74Check {
	T := t74Temp(d.Film)
	sp := spectral()
	illum := make([]float64, len(sp.lams))
	for i, l := range sp.lams {
		illum[i] = t74Planck(l, T)
	}
	eps := t74DyeEps(d)
	var dmin [3]float64
	for k, ch := range [3]string{"R", "G", "B"} {
		dmin[k] = d.Char[ch][0][1]
		for _, p := range d.Char[ch] {
			dmin[k] = math.Min(dmin[k], p[1])
		}
	}
	amt := t74DminAmounts(d, dmin)
	white := t74SlideRGB(eps, illum, amt)
	v := t74SlideRGB(eps, illum, [3]float64{1, 1, 1})
	L, a, b := labOf(v[0]/white[0], v[1]/white[1], v[2]/white[2])
	zero := t74SlideRGB(eps, illum, [3]float64{0, 0, 0})
	y := func(rgb [3]float64) float64 { return 0.2126729*rgb[0] + 0.7151522*rgb[1] + 0.0721750*rgb[2] }
	D := -math.Log10(y(v) / y(zero))
	// The colour of the dyes alone, against the illuminant.
	xv := t74XYZ(eps, illum, [3]float64{1, 1, 1})
	xw := t74XYZ(eps, illum, [3]float64{0, 0, 0})
	_, ap, bp := t74LabXYZ(xv, xw)
	dp := -math.Log10(xv[1] / xw[1])
	return t74Check{Film: d.Film, Temp: T, L: L, A: a, B: b, D: D, AP: ap, BP: bp, DP: dp}
}

// dyeNeutralCheck is the task's check by film key.
func dyeNeutralCheck(key string) (t74Check, bool) {
	d := colorFilmData_()[key]
	if d == nil {
		return t74Check{}, false
	}
	return dyeNeutralCheckData(d), true
}

// t74Want is the expected result on the committed v0.4.109 traces. A = pass
// when |a*| and |b*| are both within the task's 2-unit envelope.
type t74Want struct {
	Film string
	A, B float64
	D    float64
	Pass bool
}

var t74Wants = []t74Want{
	// Kodak Ektachrome, 5000 K.
	{"Kodak Ektachrome 100 (EPN)", -0.55, +2.12, 0.994, false},
	{"Kodak Ektachrome 100 Plus (EPP)", -0.59, +3.48, 0.997, false},
	{"Kodak Ektachrome 160T (EPT)", -0.87, +0.20, 1.005, true},
	{"Kodak Ektachrome 320T (EPJ)", +1.26, +0.31, 0.997, true},
	{"Kodak Ektachrome 400X (EPL)", +0.43, +1.61, 1.000, true},
	{"Kodak Ektachrome 64 (EPR)", +1.82, +0.68, 1.007, true},
	{"Kodak Ektachrome 64T (EPY)", +0.72, -1.40, 1.019, true},
	{"Kodak Ektachrome E100G", -0.46, +1.41, 1.018, true},
	{"Kodak Ektachrome E100GX", -0.69, +1.54, 1.018, true},
	{"Kodak Ektachrome E100VS", -0.34, +2.70, 1.015, false},
	{"Kodak Ektachrome E200", -2.40, +2.15, 0.969, false},
	// Kodak Kodachrome, 3200 K (E-55).
	{"Kodak Kodachrome 25", +0.51, -0.12, 1.011, true},
	{"Kodak Kodachrome 64", -0.54, -1.71, 1.010, true},
	{"Kodak Kodachrome 200", -2.62, +1.15, 1.003, false},
	// Kodak EIR: D5000, reported and out of scope.
	{"Kodak Ektachrome Professional Infrared (EIR)", +0.50, +0.99, 1.027, true},
	// Agfa F-PF-E4, 5000 K; the sheet does not literally say "1.0".
	{"Agfa Agfachrome RSX II 50", -0.02, +0.60, 1.018, true},
	{"Agfa Agfachrome RSX II 100", -0.44, +0.03, 1.018, true},
	{"Agfa Agfachrome RSX II 200", -4.59, +0.40, 1.027, false},
}

func t74Within2(a, b float64) bool { return math.Abs(a) <= 2 && math.Abs(b) <= 2 }

// TestDyeNeutral74 runs the corrected check on every Kodak and Agfa film and
// pins the six that fail their sheet's own claim. The six are exactly the
// films task 73 left on build A for their dye set.
func TestDyeNeutral74(t *testing.T) {
	if len(t74Wants) != 18 {
		t.Fatalf("want 18 Kodak/Agfa films, have %d", len(t74Wants))
	}
	seen := map[string]bool{}
	fails := []string{}
	for _, w := range t74Wants {
		seen[w.Film] = true
		c, ok := dyeNeutralCheck(w.Film)
		if !ok {
			t.Errorf("%s: no film data", w.Film)
			continue
		}
		if math.Abs(c.A-w.A) > 0.05 || math.Abs(c.B-w.B) > 0.05 {
			t.Errorf("%s: dye a*/b* %+.2f/%+.2f, want %+.2f/%+.2f", w.Film, c.A, c.B, w.A, w.B)
		}
		if math.Abs(c.D-w.D) > 0.01 {
			t.Errorf("%s: visual density %.3f, want %.3f", w.Film, c.D, w.D)
		}
		got := t74Within2(c.A, c.B)
		if got != w.Pass {
			t.Errorf("%s: within 2 = %v, want %v (a* %+.2f b* %+.2f)", w.Film, got, w.Pass, c.A, c.B)
		}
		if !got {
			fails = append(fails, w.Film)
		}
		t.Logf("%-44s %4.0fK  L* %5.2f  a* %+6.2f b* %+6.2f  D_Y %.3f  within2 %v  | dyes alone a* %+6.2f b* %+6.2f D %.3f",
			w.Film, c.Temp, c.L, c.A, c.B, c.D, got, c.AP, c.BP, c.DP)
	}
	// Every Kodak and Agfa film in the library must be in the table.
	for key, d := range colorFilmData_() {
		if len(key) >= 5 && (key[:5] == "Kodak" || key[:3] == "Agf") {
			if !seen[key] {
				t.Errorf("%s is a Kodak/Agfa film but is not in the check", key)
			}
			_ = d
		}
	}
	sort.Strings(fails)
	want := []string{
		"Agfa Agfachrome RSX II 200",
		"Kodak Ektachrome 100 (EPN)",
		"Kodak Ektachrome 100 Plus (EPP)",
		"Kodak Ektachrome E100VS",
		"Kodak Ektachrome E200",
		"Kodak Kodachrome 200",
	}
	if len(fails) != len(want) {
		t.Fatalf("failing films %v, want %v", fails, want)
	}
	for i := range want {
		if fails[i] != want[i] {
			t.Errorf("failing films %v, want %v", fails, want)
			break
		}
	}
}

// TestDyeNeutral74Level checks the second half of the sheet's claim: the same
// c = (1,1,1) set must have a visual density of 1.0. Every Kodak film is
// within 0.031; E200, the largest, is 3.1% low. The trace is exact (REPORT-74
// section 4), so this is the sheet's own normalisation, not a trace scale.
func TestDyeNeutral74Level(t *testing.T) {
	worst, worstFilm := 0.0, ""
	for _, w := range t74Wants {
		c, _ := dyeNeutralCheck(w.Film)
		if e := math.Abs(c.D - 1); e > worst {
			worst, worstFilm = e, w.Film
		}
		if len(w.Film) >= 5 && w.Film[:5] == "Kodak" && math.Abs(c.D-1) > 0.05 {
			t.Errorf("%s: visual density %.3f is outside 1.0 ± 0.05", w.Film, c.D)
		}
	}
	if worst > 0.05 {
		t.Errorf("worst visual density %.3f D (%s) is outside 0.05", worst, worstFilm)
	}
	t.Logf("worst |D_Y - 1| = %.3f D (%s)", worst, worstFilm)
}

// TestDyeNeutral74Model tells the two failures apart. With the illuminant
// itself as the white, four of the six old suspects are within 2 units: their
// traces are right and it is the app's D-min-adapted viewing model that moves
// them. K200 and RSX II 200 are outside under both references, so their
// sheets are not colorimetric neutrals in either model.
func TestDyeNeutral74Model(t *testing.T) {
	within := map[string]bool{}
	for _, w := range t74Wants {
		c, _ := dyeNeutralCheck(w.Film)
		within[w.Film] = t74Within2(c.AP, c.BP)
	}
	for _, key := range []string{
		"Kodak Ektachrome 100 (EPN)",
		"Kodak Ektachrome 100 Plus (EPP)",
		"Kodak Ektachrome E100VS",
		"Kodak Ektachrome E200",
	} {
		if !within[key] {
			t.Errorf("%s: dyes alone are within 2 under the sheet illuminant, want a model-only residual", key)
		}
	}
	for _, key := range []string{
		"Kodak Kodachrome 200",
		"Agfa Agfachrome RSX II 200",
	} {
		if within[key] {
			t.Errorf("%s: dyes alone are outside 2 under the sheet illuminant, want a sheet residual", key)
		}
	}
}

// TestT74GPU is the GPU/CPU check on one re-traced film that stays on A
// (Kodachrome 200), one on the integrated read (EPY), and E100G.
func TestT74GPU(t *testing.T) {
	const w, h = 32, 24
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, ISO: 100, Sensor: "Merrill"}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			u := float64(x) / float64(w-1)
			v := float64(y) / float64(h-1)
			l.Pix[i] = float32(0.002 + 0.35*u + 0.10*v)
			l.Pix[i+1] = float32(0.002 + 0.30*u + 0.12*(1-v))
			l.Pix[i+2] = float32(0.002 + 0.25*v + 0.10*u)
		}
	}
	b := NewBase(l)
	cc := [9]float64{1.5, -0.4, -0.1, -0.2, 1.4, -0.2, 0.0, -0.5, 1.5}
	comp := Compensation{Family: "SD1", Sensor: "Merrill", CC: &cc, SatFactor: 1}
	for _, key := range []string{
		"Kodak Kodachrome 200",
		"Kodak Ektachrome 64T (EPY)",
		"Kodak Ektachrome E100G",
	} {
		p := preset.Neutral()
		p.Film = key
		p.NoGrain = true
		cpu := Render(b, p, comp)
		r := NewGPURecipe(b, p, comp)
		if r.ColorFilm == nil {
			t.Fatalf("%s: no slide recipe", key)
		}
		maxd := 0
		for i := 0; i < w*h; i++ {
			c := [3]float32{l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2]}
			c = t60GPUFilm(r, [3]float32{c[0] * r.ColorFilm.Exp, c[1] * r.ColorFilm.Exp, c[2] * r.ColorFilm.Exp})
			for k := 0; k < 3; k++ {
				c[k] = c[k] * r.Gain[k] / r.ColorFilm.Exp
			}
			Y := float32(0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2])
			if Y > 1e-6 {
				d := dec(enc(Y))
				k := d / Y
				c[0], c[1], c[2] = c[0]*k, c[1]*k, c[2]*k
			}
			for k := 0; k < 3; k++ {
				want := int(float32(enc(c[k]))*255 + 0.5)
				got := int(cpu.Pix[i*4+k])
				d := got - want
				if d < 0 {
					d = -d
				}
				if d > maxd {
					maxd = d
				}
			}
		}
		if maxd > 1 {
			t.Errorf("%s: GPU differs from CPU by %d levels", key, maxd)
		}
		t.Logf("%s: GPU = CPU, max %d level", key, maxd)
	}
}

// t74SlideHashes pins every slide's rendered bytes. All are the v0.4.109
// hashes except Agfa RSX II 200, the only dye array task 74 changed: its
// re-trace is on the sheet's printed grid, and the report's proof overlay
// shows the old trace off the ink. The v0.4.109 hash was d9193c670e319162c245105f.
var t74SlideHashes = map[string]string{
	"Agfa Agfachrome RSX II 100":                   "47e7e978a9f697ef394280fb",
	"Agfa Agfachrome RSX II 200":                   "8688d883855d3a58b65d8daa",
	"Agfa Agfachrome RSX II 50":                    "ec4e3794c6d3c785f34a40de",
	"Fujifilm Fujichrome 64T Type II":              "4f59c424cc86ac6fb429575b",
	"Fujifilm Fujichrome Astia 100F":               "767a00d08af7d9339b78908d",
	"Fujifilm Fujichrome Provia 100F":              "f3a78a3f7f38e6ca9f5aa55a",
	"Fujifilm Fujichrome Provia 400F":              "449a50b236039ef717cd0ef2",
	"Fujifilm Fujichrome Provia 400X":              "62d9a9870e72343a3ed25727",
	"Fujifilm Fujichrome Sensia 100":               "d78369d234b3f7238543a1e1",
	"Fujifilm Fujichrome Sensia 200":               "18a9465272d1945577d85a4e",
	"Fujifilm Fujichrome Sensia 400":               "c8255d130907001c950d8e3b",
	"Fujifilm Fujichrome T64":                      "39046253c72529196c2d495c",
	"Fujifilm Fujichrome Velvia 100":               "18579bf8104d8543cffbe972",
	"Fujifilm Fujichrome Velvia 100F":              "2667ec61def716ff81cadab3",
	"Fujifilm Fujichrome Velvia 50":                "5226b337ae148ef36875844b",
	"Kodak Ektachrome 100 (EPN)":                   "6ead5b548c883d19c227ecda",
	"Kodak Ektachrome 100 Plus (EPP)":              "31b506a5faa0399511ae1753",
	"Kodak Ektachrome 160T (EPT)":                  "04105ffa71c2fb3f22789478",
	"Kodak Ektachrome 320T (EPJ)":                  "554498d25eda871d2983004e",
	"Kodak Ektachrome 400X (EPL)":                  "cd90811d82e4df2b10ae0c78",
	"Kodak Ektachrome 64 (EPR)":                    "ef2d6e5168bffee01c09aa90",
	"Kodak Ektachrome 64T (EPY)":                   "9bc12888935146ab06fa6aac",
	"Kodak Ektachrome E100G":                       "f2ce7756054c8a0b86749653",
	"Kodak Ektachrome E100GX":                      "8c5ad057abc54736429b2853",
	"Kodak Ektachrome E100VS":                      "7b2ad5d5071ae0cf8eb0d059",
	"Kodak Ektachrome E200":                        "ffeb10d46be8833301a6c6e6",
	"Kodak Ektachrome Professional Infrared (EIR)": "6f9408238b3fb0b47f4841f5",
	"Kodak Kodachrome 200":                         "214c5c47961935bf45c9cc1c",
	"Kodak Kodachrome 25":                          "87ceff6b6aaf398800f553a7",
	"Kodak Kodachrome 64":                          "7304e62d748abc6b4a0ddac3",
}

// TestT74SlideBytes renders a fixed 32x24 frame through every slide and
// compares the SHA-256 of the 8-bit frame with the pinned hash. 29 of the 30
// are v0.4.109 byte for byte; the Agfa RSX II 200 is the one re-traced film.
func TestT74SlideBytes(t *testing.T) {
	const w, h = 32, 24
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, ISO: 100, Sensor: "Merrill"}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			u := float64(x) / float64(w-1)
			v := float64(y) / float64(h-1)
			l.Pix[i] = float32(0.002 + 0.35*u + 0.10*v)
			l.Pix[i+1] = float32(0.002 + 0.30*u + 0.12*(1-v))
			l.Pix[i+2] = float32(0.002 + 0.25*v + 0.10*u)
		}
	}
	b := NewBase(l)
	cc := [9]float64{1.5, -0.4, -0.1, -0.2, 1.4, -0.2, 0.0, -0.5, 1.5}
	comp := Compensation{Family: "SD1", Sensor: "Merrill", CC: &cc, SatFactor: 1}
	if len(t74SlideHashes) != len(colorFilmData_()) {
		t.Fatalf("hash table has %d films, library has %d", len(t74SlideHashes), len(colorFilmData_()))
	}
	for key := range colorFilmData_() {
		p := preset.Neutral()
		p.Film = key
		p.NoGrain = true
		out := Render(b, p, comp)
		sum := sha256.Sum256(out.Pix)
		got := hex12(sum[:12])
		want, ok := t74SlideHashes[key]
		if !ok {
			t.Errorf("%s: no pinned hash", key)
			continue
		}
		if got != want {
			t.Errorf("%s: hash %s, want %s", key, got, want)
		}
	}
}

func hex12(b []byte) string {
	const hx = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hx[v>>4]
		out[i*2+1] = hx[v&15]
	}
	return string(out)
}
