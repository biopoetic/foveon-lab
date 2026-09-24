package calib

import (
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	_ "golang.org/x/image/tiff"

	"foveon-lab/render"
)

// AnalysisW is the working width: exports are box-downscaled to this, which
// also averages away noise before measuring.
const AnalysisW = 1200

// Bins is the resolution of measured tone curves (reference brightness,
// perceptual/sRGB scale).
const Bins = 32

// Measurement is what one SPP export reveals about its moved control.
type Measurement struct {
	Code  string  `json:"code"`
	Label string  `json:"label"`
	Param string  `json:"param"`
	Value float64 `json:"value"`
	Image string  `json:"image"`

	// Curve[i] = mean output brightness (sRGB 0..1) of pixels whose
	// reference brightness falls in bin i; -1 where the bin is empty.
	Curve [Bins]float64 `json:"curve"`
	// Spread[i] = std-dev of output brightness inside bin i. Near zero means
	// a global tone curve; large means a local operator (Fill Light).
	Spread [Bins]float64 `json:"spread"`
	// Chroma[i] = Σ|chroma out| / Σ|chroma ref| per bin (saturation gain).
	Chroma [Bins]float64 `json:"chroma"`
	// Matrix is the least-squares 3x3 linear-RGB fit out = M·ref over
	// unclipped pixels, row-major; MatrixErr its mean abs error in 8-bit.
	Matrix    [9]float64 `json:"matrix"`
	MatrixErr float64    `json:"matrixErr"`
	// Local[l][b] = mean output-minus-reference brightness for pixels of
	// brightness bin b (8 bins) inside regions of local-mean bin l (8 bins).
	Local [8][8]float64 `json:"local"`

	Change   float64 `json:"change"`   // mean |SPP out − SPP ref|, 8-bit
	EmuError float64 `json:"emuError"` // mean |our emulation − SPP out|, 8-bit
}

var reCode = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])([ABC]\d{2})($|[^0-9])`)

type export struct {
	path, image, code string
}

// findExports groups exported files by source image. The step code may be
// anywhere in the file name (SDIM0031_A03.tif, A03.tif, A03 SDIM0031.jpg).
func findExports(dir string) (map[string][]export, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	groups := map[string][]export{}
	for _, e := range ents {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || (ext != ".tif" && ext != ".tiff" && ext != ".jpg" && ext != ".jpeg") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		m := reCode.FindStringSubmatchIndex(stem)
		if m == nil {
			continue
		}
		code := strings.ToUpper(stem[m[4]:m[5]])
		img := strings.Trim(stem[:m[4]]+stem[m[5]:], " _-.")
		if img == "" {
			img = "slika"
		}
		groups[img] = append(groups[img], export{path: filepath.Join(dir, e.Name()), image: img, code: code})
	}
	return groups, nil
}

func load(path string) (*render.Linear, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return render.FromImage(img, AnalysisW, 0), nil
}

func luma(p []float32, i int) float32 { return 0.2126*p[i] + 0.7152*p[i+1] + 0.0722*p[i+2] }

// localMean returns per-pixel blurred brightness (region brightness).
func localMean(l *render.Linear) []float32 {
	cell := max(1, l.W/40)
	gw, gh := (l.W+cell-1)/cell, (l.H+cell-1)/cell
	g := make([]float32, gw*gh)
	n := make([]float32, gw*gh)
	for y := 0; y < l.H; y++ {
		for x := 0; x < l.W; x++ {
			k := (y/cell)*gw + x/cell
			g[k] += render.ToSRGB(luma(l.Pix, (y*l.W+x)*3))
			n[k]++
		}
	}
	for i := range g {
		g[i] /= n[i]
	}
	out := make([]float32, l.W*l.H)
	for y := 0; y < l.H; y++ {
		for x := 0; x < l.W; x++ {
			out[y*l.W+x] = g[(y/cell)*gw+x/cell]
		}
	}
	return out
}

// measure compares one export against the neutral reference.
func measure(ref, out *render.Linear, refLocal []float32) (Measurement, error) {
	var m Measurement
	if ref.W != out.W || ref.H != out.H {
		return m, fmt.Errorf("druga veličina (%dx%d vs referenca %dx%d) — izvozi moraju biti iste rezolucije i bez rezanja", out.W, out.H, ref.W, ref.H)
	}
	var sum, sum2, cnt, cOut, cRef [Bins]float64
	var loc, locN [8][8]float64
	var ata, atb [3][3]float64 // normal equations for the matrix fit
	var change float64
	for i := 0; i < ref.W*ref.H; i++ {
		j := i * 3
		vr := float64(render.ToSRGB(luma(ref.Pix, j)))
		vo := float64(render.ToSRGB(luma(out.Pix, j)))
		b := min(Bins-1, int(vr*Bins))
		sum[b] += vo
		sum2[b] += vo * vo
		cnt[b]++
		for c := 0; c < 3; c++ {
			change += math.Abs(float64(render.ToSRGB(out.Pix[j+c])-render.ToSRGB(ref.Pix[j+c]))) * 255
		}

		yr, yo := float64(luma(ref.Pix, j)), float64(luma(out.Pix, j))
		var cr, co float64
		for c := 0; c < 3; c++ {
			cr += math.Pow(float64(ref.Pix[j+c])-yr, 2)
			co += math.Pow(float64(out.Pix[j+c])-yo, 2)
		}
		if yr > 0.01 && math.Sqrt(cr) > 0.03*yr {
			cRef[b] += math.Sqrt(cr)
			cOut[b] += math.Sqrt(co)
		}

		lb := min(7, int(float64(refLocal[i])*8))
		pb := min(7, int(vr*8))
		loc[lb][pb] += vo - vr
		locN[lb][pb]++

		clipped := false
		for c := 0; c < 3; c++ {
			if ref.Pix[j+c] > 0.95 || out.Pix[j+c] > 0.95 {
				clipped = true
			}
		}
		if !clipped {
			for r := 0; r < 3; r++ {
				for c := 0; c < 3; c++ {
					ata[r][c] += float64(ref.Pix[j+r]) * float64(ref.Pix[j+c])
					atb[r][c] += float64(out.Pix[j+r]) * float64(ref.Pix[j+c])
				}
			}
		}
	}
	for b := 0; b < Bins; b++ {
		m.Curve[b], m.Chroma[b] = -1, -1
		if cnt[b] > 20 {
			mean := sum[b] / cnt[b]
			m.Curve[b] = mean
			m.Spread[b] = math.Sqrt(math.Max(0, sum2[b]/cnt[b]-mean*mean))
		}
		if cRef[b] > 0 {
			m.Chroma[b] = cOut[b] / cRef[b]
		}
	}
	for l := 0; l < 8; l++ {
		for b := 0; b < 8; b++ {
			if locN[l][b] > 20 {
				m.Local[l][b] = loc[l][b] / locN[l][b]
			} else {
				m.Local[l][b] = math.NaN()
			}
		}
	}
	for l := range m.Local {
		for b := range m.Local[l] {
			if math.IsNaN(m.Local[l][b]) {
				m.Local[l][b] = -9 // JSON has no NaN; -9 = no data
			}
		}
	}
	m.Change = change / float64(ref.W*ref.H*3)

	// M = (Σ out·refᵀ)(Σ ref·refᵀ)⁻¹
	if inv, ok := inverse3(ata); ok {
		for r := 0; r < 3; r++ {
			for c := 0; c < 3; c++ {
				for k := 0; k < 3; k++ {
					m.Matrix[r*3+c] += atb[r][k] * inv[k][c]
				}
			}
		}
		var e float64
		var n int
		for i := 0; i < ref.W*ref.H; i += 7 {
			j := i * 3
			for r := 0; r < 3; r++ {
				var v float64
				for c := 0; c < 3; c++ {
					v += m.Matrix[r*3+c] * float64(ref.Pix[j+c])
				}
				e += math.Abs(float64(render.ToSRGB(float32(v))-render.ToSRGB(out.Pix[j+r]))) * 255
				n++
			}
		}
		m.MatrixErr = e / float64(n)
	}
	return m, nil
}

func inverse3(a [3][3]float64) ([3][3]float64, bool) {
	var r [3][3]float64
	det := a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) -
		a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) +
		a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])
	if math.Abs(det) < 1e-12 {
		return r, false
	}
	r[0][0] = (a[1][1]*a[2][2] - a[1][2]*a[2][1]) / det
	r[0][1] = (a[0][2]*a[2][1] - a[0][1]*a[2][2]) / det
	r[0][2] = (a[0][1]*a[1][2] - a[0][2]*a[1][1]) / det
	r[1][0] = (a[1][2]*a[2][0] - a[1][0]*a[2][2]) / det
	r[1][1] = (a[0][0]*a[2][2] - a[0][2]*a[2][0]) / det
	r[1][2] = (a[0][2]*a[1][0] - a[0][0]*a[1][2]) / det
	r[2][0] = (a[1][0]*a[2][1] - a[1][1]*a[2][0]) / det
	r[2][1] = (a[0][1]*a[2][0] - a[0][0]*a[2][1]) / det
	r[2][2] = (a[0][0]*a[1][1] - a[0][1]*a[1][0]) / det
	return r, true
}

// emuError renders our emulation of step s on SPP's neutral export and
// returns its mean abs difference (8-bit) from SPP's actual export.
func emuError(ref, out *render.Linear, s Step) float64 {
	em := render.Render(render.NewBase(ref), s.Params, render.Compensation{SatFactor: 1})
	var e float64
	for i := 0; i < ref.W*ref.H; i++ {
		for c := 0; c < 3; c++ {
			o := render.ToSRGB(out.Pix[i*3+c]) * 255
			e += math.Abs(float64(em.Pix[i*4+c]) - float64(o))
		}
	}
	return e / float64(ref.W*ref.H*3)
}

// Result is a full analysis run.
type Result struct {
	Dir          string        `json:"dir"`
	Measurements []Measurement `json:"measurements"`
	Warnings     []string      `json:"warnings"`
}

// Analyze measures every export in dir against the A00 export of the same
// image.
func Analyze(dir string, progress io.Writer) (*Result, error) {
	groups, err := findExports(dir)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, errors.New("nema izvoza s kodom (npr. SDIM0031_A03.tif) u " + dir)
	}
	steps := ByCode(Steps())
	res := &Result{Dir: dir}
	var names []string
	for k := range groups {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		exps := groups[name]
		sort.Slice(exps, func(i, j int) bool { return exps[i].code < exps[j].code })
		var refPath string
		for _, e := range exps {
			if e.code == Reference {
				refPath = e.path
			}
		}
		if refPath == "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: nema %s (neutralne reference) — preskačem %d izvoza", name, Reference, len(exps)))
			continue
		}
		ref, err := load(refPath)
		if err != nil {
			res.Warnings = append(res.Warnings, err.Error())
			continue
		}
		refLocal := localMean(ref)
		for _, e := range exps {
			if e.code == Reference {
				continue
			}
			s, ok := steps[e.code]
			if !ok {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: nepoznat kod %s", filepath.Base(e.path), e.code))
				continue
			}
			fmt.Fprintf(progress, "  %s %s %s\n", name, s.Code, s.Label)
			out, err := load(e.path)
			if err != nil {
				res.Warnings = append(res.Warnings, err.Error())
				continue
			}
			m, err := measure(ref, out, refLocal)
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v", filepath.Base(e.path), err))
				continue
			}
			m.Code, m.Label, m.Param, m.Value, m.Image = s.Code, s.Label, s.Param, s.Value, name
			m.EmuError = emuError(ref, out, s)
			res.Measurements = append(res.Measurements, m)
		}
	}
	return res, nil
}

// curveAt samples a curve at brightness v (linear interpolation across
// non-empty bins).
func curveAt(c [Bins]float64, v float64) float64 {
	b := int(v * Bins)
	for d := 0; d < Bins; d++ {
		for _, k := range []int{b - d, b + d} {
			if k >= 0 && k < Bins && c[k] >= 0 {
				return c[k]
			}
		}
	}
	return -1
}

// Report renders a human-readable summary.
func (r *Result) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Foveon Lab — kalibracijski izvještaj\n%s\n\n", r.Dir)
	b.WriteString("Promjena = koliko je SPP promijenio sliku u odnosu na A00 (0-255, prosjek po kanalu).\n")
	b.WriteString("Greška emulacije = koliko se naša emulacija razlikuje od SPP-a za isti klizač (manje = bolje).\n")
	b.WriteString("Krivulja = izlazna svjetlina (0-255) za ulaz 25 / 64 / 128 / 191 / 230. Neutralno bi bilo 25 64 128 191 230.\n")
	b.WriteString("Lokalno = raspršenost izlaza za isti ulaz; neutralno ~2-3; bitno veće znači lokalni operator, ne globalna krivulja.\n\n")
	fmt.Fprintf(&b, "%-10s %-4s %-24s %8s %8s  %-24s %6s %7s %8s\n", "slika", "kod", "postavka", "promjena", "emu.gr.", "krivulja", "sat×", "lokalno", "matr.gr.")
	for _, m := range r.Measurements {
		var pts []string
		for _, v := range []float64{25, 64, 128, 191, 230} {
			c := curveAt(m.Curve, v/255)
			pts = append(pts, fmt.Sprintf("%3.0f", c*255))
		}
		var sp float64
		var n int
		for i := 4; i < Bins-4; i++ {
			if m.Curve[i] >= 0 {
				sp += m.Spread[i]
				n++
			}
		}
		if n > 0 {
			sp = sp / float64(n) * 255
		}
		sat := curveAt(m.Chroma, 0.5)
		fmt.Fprintf(&b, "%-10s %-4s %-24s %8.1f %8.1f  %-24s %6.2f %7.1f %8.1f\n",
			trunc(m.Image, 10), m.Code, trunc(m.Label, 24), m.Change, m.EmuError, strings.Join(pts, " "), sat, sp, m.MatrixErr)
	}
	for _, m := range r.Measurements {
		if m.Param != "FillLight" {
			continue
		}
		fmt.Fprintf(&b, "\n%s %s — pomak svjetline (0-255) po svjetlini piksela (stupci) i svjetlini okoline (retci):\n", m.Code, m.Label)
		for l := 0; l < 8; l++ {
			fmt.Fprintf(&b, "  okolina %3d-%3d:", l*32, l*32+31)
			for p := 0; p < 8; p++ {
				if v := m.Local[l][p]; v > -9 {
					fmt.Fprintf(&b, " %+5.1f", v*255)
				} else {
					b.WriteString("     ·")
				}
			}
			b.WriteString("\n")
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\nUpozorenja:\n")
		for _, w := range r.Warnings {
			b.WriteString("  - " + w + "\n")
		}
	}
	return b.String()
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
