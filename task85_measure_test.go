package main

// Task 85 scratch measurement: the three-way comparison of the SPP export,
// our raw render and the camera's embedded JPEG. Manual only:
//
//	T85=1          TestT85Measure (six rows and the deliberate-darkening gate)
//	T85_SPREAD=1   TestT85JPEGSpread (camera JPEG vs SPP over every scored photo)
//	T85_KRONDIAG=1 TestT85KronDiag (the LOW raw with and without the DPM base)
//
// T85_SAMPLES, T85_EXPORTS and T85_EXTRA_X3F override the paths.

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const t85Out = "zz-out/t85"

type t85PhotoCase struct {
	name, path, export string
}

type t85Numbers struct {
	Name      string  `json:"name"`
	Group     string  `json:"group"`
	W         int     `json:"w"`
	H         int     `json:"h"`
	YSpp      float64 `json:"ySpp"`
	YRaw      float64 `json:"yRaw"`
	YJpg      float64 `json:"yJpg"`
	EVRaw     float64 `json:"evRaw"`
	EVJpg     float64 `json:"evJpg"`
	DESppRaw  float64 `json:"deSppRaw"`
	DESppJpg  float64 `json:"deSppJpg"`
	DERawJpg  float64 `json:"deRawJpg"`
	CSppRaw   float64 `json:"chromaSppRaw"`
	CSppJpg   float64 `json:"chromaSppJpg"`
	RGBSpp    [3]float64
	RGBRaw    [3]float64
	RGBJpg    [3]float64
	ChromaSpp [2]float64
	ChromaRaw [2]float64
	ChromaJpg [2]float64
	Verdict   string `json:"verdict"`
}

func t85LoadPhoto(t *testing.T, path string) *Photo {
	t.Helper()
	ph := &Photo{ID: idOf(path), Name: filepath.Base(path), Folder: filepath.Base(filepath.Dir(path)), path: path, Info: map[string]string{}}
	ph.Kind = "x3f"
	if !loadMeta(ph) {
		t.Fatalf("loadMeta %s", path)
	}
	ph.Accurate = ph.RawDecoded || rawCorrectionFor(ph) != nil
	return ph
}

// t85JPEG is the camera's own embedded preview in linear light, at the
// export's geometry and size.
func t85JPEG(ph *Photo, spp *render.Linear) (*render.Linear, error) {
	f, err := x3f.Open(ph.path)
	if err != nil {
		return nil, err
	}
	img, _, err := f.Preview()
	if err != nil {
		return nil, err
	}
	l := render.FromImage(img, sourceW, ph.Rotation)
	l = fitAspect(l.Downscale(spp.W), spp.W, spp.H)
	return l, nil
}

func t85Mean(l *render.Linear) (r, g, b float64) {
	for i := 0; i+2 < len(l.Pix); i += 3 {
		r += float64(l.Pix[i])
		g += float64(l.Pix[i+1])
		b += float64(l.Pix[i+2])
	}
	n := float64(l.W * l.H)
	return r / n, g / n, b / n
}

func t85Y(r, g, b float64) float64 { return 0.2126*r + 0.7152*g + 0.0722*b }

func t85Lab(r, g, b float64) (L, A, B float64) {
	// The means are already linear; go through XYZ directly.
	x := 0.4124*r + 0.3576*g + 0.1805*b
	y := 0.2126*r + 0.7152*g + 0.0722*b
	z := 0.0193*r + 0.1192*g + 0.9505*b
	xn, yn, zn := 0.9505, 1.0, 1.089
	f := func(t float64) float64 {
		if t > 216.0/24389 {
			return math.Cbrt(t)
		}
		return (24389.0/27*t + 16) / 116
	}
	fx, fy, fz := f(x/xn), f(y/yn), f(z/zn)
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

func t85MeanLab(l *render.Linear) (L, A, B float64) {
	r, g, b := t85Mean(l)
	return t85Lab(r, g, b)
}

func t85DE(a, b *render.Linear) float64 {
	L1, A1, B1 := t85MeanLab(a)
	L2, A2, B2 := t85MeanLab(b)
	return math.Sqrt((L1-L2)*(L1-L2) + (A1-A2)*(A1-A2) + (B1-B2)*(B1-B2))
}

// t85ChromaDE is the colour-only distance of the mean colours: L is already
// measured as EV, so this does not count a brightness difference twice.
func t85ChromaDE(a, b *render.Linear) float64 {
	_, A1, B1 := t85MeanLab(a)
	_, A2, B2 := t85MeanLab(b)
	return math.Hypot(A1-A2, B1-B2)
}

func t85Chroma(r, g, b float64) [2]float64 {
	s := r + g + b
	if s <= 0 {
		return [2]float64{}
	}
	return [2]float64{r / s, g / s}
}

// t85Panel draws one linear image at w×h; gain is applied to every panel so
// a dark frame can be inspected without changing the relative exposure.
func t85Panel(l *render.Linear, w, h int, gain float64) *image.RGBA {
	s := stretchTo(l, w, h)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		for k := 0; k < 3; k++ {
			v := clamp01(s.Pix[i*3+k]) * float32(gain)
			img.Pix[i*4+k] = uint8(math.Round(float64(render.ToSRGB(clamp01(v))) * 255))
		}
		img.Pix[i*4+3] = 255
	}
	return img
}

func t85DrawLabel(img *image.RGBA, x, y int, s string) {
	d := &font.Drawer{Dst: img, Src: image.White, Face: basicfont.Face7x13, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

func t85Three(name string, gain float64, spp, raw, jpg *render.Linear, labels [3]string) {
	const w, h, gap, bar = 360, 240, 6, 18
	canvas := image.NewRGBA(image.Rect(0, 0, 3*w+4*gap, h+bar+gap))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.RGBA{20, 20, 20, 255}}, image.Point{}, draw.Src)
	lift := ""
	if gain > 1.01 {
		lift = fmt.Sprintf("  (all panels x%.1f)", gain)
	}
	for i, l := range []*render.Linear{spp, raw, jpg} {
		p := t85Panel(l, w, h, gain)
		x := gap + i*(w+gap)
		draw.Draw(canvas, image.Rect(x, bar, x+w, bar+h), p, image.Point{}, draw.Src)
		t85DrawLabel(canvas, x+4, 14, labels[i])
	}
	t85DrawLabel(canvas, gap, h+bar+gap-3, lift)
	f, err := os.Create(filepath.Join(t85Out, name+"_three.png"))
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, canvas); err != nil {
		panic(err)
	}
}

func t85MeasureOne(t *testing.T, c t85PhotoCase) t85Numbers {
	ph := t85LoadPhoto(t, c.path)
	spp, err := calib.LoadLinear(c.export)
	if err != nil {
		t.Fatalf("%s export: %v", c.name, err)
	}
	raw, err := linearForCheck(ph, spp)
	if err != nil {
		t.Fatalf("%s raw: %v", c.name, err)
	}
	jpg, err := t85JPEG(ph, spp)
	if err != nil {
		t.Fatalf("%s jpeg: %v", c.name, err)
	}
	if jpg.W != spp.W || jpg.H != spp.H || raw.W != spp.W || raw.H != spp.H {
		t.Fatalf("%s frames differ: spp %dx%d raw %dx%d jpg %dx%d", c.name, spp.W, spp.H, raw.W, raw.H, jpg.W, jpg.H)
	}
	n := t85Numbers{Name: c.name, Group: calibGroup(ph), W: spp.W, H: spp.H}
	rgb := func(l *render.Linear) [3]float64 {
		r, g, b := t85Mean(l)
		return [3]float64{r, g, b}
	}
	n.RGBSpp, n.RGBRaw, n.RGBJpg = rgb(spp), rgb(raw), rgb(jpg)
	n.YSpp, n.YRaw, n.YJpg = t85Y(n.RGBSpp[0], n.RGBSpp[1], n.RGBSpp[2]), t85Y(n.RGBRaw[0], n.RGBRaw[1], n.RGBRaw[2]), t85Y(n.RGBJpg[0], n.RGBJpg[1], n.RGBJpg[2])
	n.EVRaw = math.Log2(n.YRaw / n.YSpp)
	n.EVJpg = math.Log2(n.YJpg / n.YSpp)
	n.DESppRaw = t85DE(raw, spp)
	n.DESppJpg = t85DE(jpg, spp)
	n.DERawJpg = t85DE(raw, jpg)
	n.CSppRaw = t85ChromaDE(raw, spp)
	n.CSppJpg = t85ChromaDE(jpg, spp)
	n.ChromaSpp = t85Chroma(n.RGBSpp[0], n.RGBSpp[1], n.RGBSpp[2])
	n.ChromaRaw = t85Chroma(n.RGBRaw[0], n.RGBRaw[1], n.RGBRaw[2])
	n.ChromaJpg = t85Chroma(n.RGBJpg[0], n.RGBJpg[1], n.RGBJpg[2])
	n.Verdict = t85Verdict(n)
	gain := 1.0
	if n.YSpp < 0.05 {
		gain = math.Min(64, 0.20/n.YSpp)
	}
	t85Three(c.name, gain, spp, raw, jpg, [3]string{"SPP export", "our raw render", "camera JPEG"})
	if gain > 1.01 {
		t85Three(c.name+"_nolift", 1, spp, raw, jpg, [3]string{"SPP export", "our raw render", "camera JPEG"})
	}
	return n
}

// t85Verdict: SPP and the camera JPEG agreeing while ours differs means ours
// is off; ours and the JPEG agreeing while SPP differs means the reference is
// suspect. When ours is 0.25 stop from both (a tie on brightness), the
// colour-only distance breaks the tie: the witness whose mean colour is
// closer to SPP's is the one that "agrees" with it.
func t85Verdict(n t85Numbers) string {
	const evTol = 0.25
	rawNearJpg := math.Abs(n.EVRaw-n.EVJpg) <= evTol
	rawNearSpp := math.Abs(n.EVRaw) <= evTol
	jpgNearSpp := math.Abs(n.EVJpg) <= evTol
	switch {
	case rawNearSpp && jpgNearSpp:
		return "all three agree"
	case rawNearJpg && jpgNearSpp:
		if n.CSppJpg <= n.CSppRaw {
			return "ours is off"
		}
		return "reference is not neutral"
	case rawNearJpg && !rawNearSpp:
		return "reference is not neutral"
	case jpgNearSpp && !rawNearSpp:
		return "ours is off"
	default:
		return "inconclusive (all three differ)"
	}
}

func TestT85Measure(t *testing.T) {
	if os.Getenv("T85") == "" {
		t.Skip("set T85=1 to run the task 85 measurement")
	}
	if err := os.MkdirAll(t85Out, 0o755); err != nil {
		t.Fatal(err)
	}
	desk := os.Getenv("T85_SAMPLES")
	if desk == "" {
		desk = `C:\Users\Korisnik\Desktop\foveon-samples`
	}
	exp := os.Getenv("T85_EXPORTS")
	if exp == "" {
		exp = `C:\Users\Korisnik\Desktop\foveon-calibration\exports`
	}
	cases := []t85PhotoCase{
		{"KRON_PROCESSING_SDIM7824", filepath.Join(desk, `archive\kron_processing_SDIM7824.X3F`), filepath.Join(exp, `kron_processing_SDIM7824_A00.tif`)},
		{"RYTTERFALK_SDIM0656", filepath.Join(desk, `rytterfalk\rytterfalk_SDIM0656.X3F`), filepath.Join(exp, `rytterfalk_SDIM0656_A00.tif`)},
		{"SIGMA-PHOTO_VW_BEETLE", filepath.Join(desk, `archive\sigma-photo_VW_Beetle.X3F`), filepath.Join(exp, `sigma-photo_VW_Beetle_A00.tif`)},
		{"SIGMA-PHOTO_IMG00046_A", filepath.Join(desk, `archive\sigma-photo_IMG00046_a.X3F`), filepath.Join(exp, `sigma-photo_IMG00046_a_A00.tif`)},
		{"KRON_DP2CARDX3FS_SDIM0686", filepath.Join(desk, `archive\kron_DP2CardX3Fs_SDIM0686.X3F`), filepath.Join(exp, `kron_DP2CardX3Fs_SDIM0686_A00.tif`)},
		{"RAW_SIGMA_DP1S", filepath.Join(desk, `DP1s\RAW_SIGMA_DP1S.X3F`), filepath.Join(exp, `RAW_SIGMA_DP1S_A00.tif`)},
	}
	var out []t85Numbers
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := t85MeasureOne(t, c)
			out = append(out, n)
			fmt.Printf("%-28s %-4s %4dx%-4d  Yspp %.6f Yraw %.6f Yjpg %.6f  EVraw %+6.3f EVjpg %+6.3f  dE raw %5.2f jpg %5.2f rawjpg %5.2f  chroma raw %5.2f jpg %5.2f  %s\n",
				n.Name, n.Group, n.W, n.H, n.YSpp, n.YRaw, n.YJpg, n.EVRaw, n.EVJpg, n.DESppRaw, n.DESppJpg, n.DERawJpg, n.CSppRaw, n.CSppJpg, n.Verdict)
		})
	}
	b, _ := json.MarshalIndent(out, "", " ")
	if err := os.WriteFile(filepath.Join(t85Out, "measure.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	// The check that must fail: a healthy photo whose render is darkened one
	// stop in the measuring code only. The same comparison must call it ours.
	gate := t85PhotoCase{"DP1MHSLI0100", filepath.Join(desk, `DP1-Merrill\DP1MhSLI0100.X3F`), filepath.Join(exp, `DP1MHSLI0100_A00.tif`)}
	ph := t85LoadPhoto(t, gate.path)
	spp, err := calib.LoadLinear(gate.export)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := linearForCheck(ph, spp)
	if err != nil {
		t.Fatal(err)
	}
	jpg, err := t85JPEG(ph, spp)
	if err != nil {
		t.Fatal(err)
	}
	healthy := t85Numbers{Name: gate.name, Group: calibGroup(ph), W: spp.W, H: spp.H}
	{
		r, g, b := t85Mean(spp)
		healthy.RGBSpp, healthy.YSpp = [3]float64{r, g, b}, t85Y(r, g, b)
		r, g, b = t85Mean(raw)
		healthy.RGBRaw, healthy.YRaw = [3]float64{r, g, b}, t85Y(r, g, b)
		r, g, b = t85Mean(jpg)
		healthy.RGBJpg, healthy.YJpg = [3]float64{r, g, b}, t85Y(r, g, b)
		healthy.EVRaw = math.Log2(healthy.YRaw / healthy.YSpp)
		healthy.EVJpg = math.Log2(healthy.YJpg / healthy.YSpp)
		healthy.DESppRaw = t85DE(raw, spp)
		healthy.DESppJpg = t85DE(jpg, spp)
		healthy.CSppRaw = t85ChromaDE(raw, spp)
		healthy.CSppJpg = t85ChromaDE(jpg, spp)
		healthy.Verdict = t85Verdict(healthy)
	}
	dark := *raw
	dark.Pix = append([]float32(nil), raw.Pix...)
	for i := range dark.Pix {
		dark.Pix[i] *= 0.5
	}
	bad := healthy
	{
		r, g, b := t85Mean(&dark)
		bad.RGBRaw, bad.YRaw = [3]float64{r, g, b}, t85Y(r, g, b)
		bad.EVRaw = math.Log2(bad.YRaw / bad.YSpp)
		bad.DESppRaw = t85DE(&dark, spp)
		bad.Verdict = t85Verdict(bad)
	}
	fmt.Printf("GATE healthy %-18s EVraw %+6.3f EVjpg %+6.3f -> %s\n", healthy.Name, healthy.EVRaw, healthy.EVJpg, healthy.Verdict)
	fmt.Printf("GATE darkened by 1 stop          EVraw %+6.3f EVjpg %+6.3f -> %s\n", bad.EVRaw, bad.EVJpg, bad.Verdict)
	if bad.Verdict != "ours is off" {
		t.Fatalf("the check did not catch a deliberate one-stop darkening: %s", bad.Verdict)
	}
	if healthy.Verdict != "all three agree" && healthy.Verdict != "inconclusive (all three differ)" && healthy.Verdict != "reference is not neutral" && healthy.Verdict != "ours is off" {
		t.Fatalf("unexpected healthy verdict %q", healthy.Verdict)
	}
	t85Three("GATE_"+gate.name, 1, spp, &dark, jpg, [3]string{"SPP export", "our render -1 EV (deliberate)", "camera JPEG"})
	b, _ = json.MarshalIndent(map[string]any{"healthy": healthy, "deliberateDark": bad}, "", " ")
	os.WriteFile(filepath.Join(t85Out, "gate.json"), b, 0o644)
}

// TestT85JPEGSpread compares every scored camera JPEG with its SPP export,
// with no raw decode: it says whether the camera-JPEG offset from SPP on the
// five verdict rows is typical for that body. Run with T85_SPREAD=1.
func TestT85JPEGSpread(t *testing.T) {
	if os.Getenv("T85_SPREAD") == "" {
		t.Skip("set T85_SPREAD=1 to run the camera-JPEG spread")
	}
	b, err := os.ReadFile("docs/spp-check.json")
	if err != nil {
		t.Fatal(err)
	}
	var scores map[string]sppScore
	if err := json.Unmarshal(b, &scores); err != nil {
		t.Fatal(err)
	}
	desk := os.Getenv("T85_SAMPLES")
	if desk == "" {
		desk = `C:\Users\Korisnik\Desktop\foveon-samples`
	}
	exp := os.Getenv("T85_EXPORTS")
	if exp == "" {
		exp = `C:\Users\Korisnik\Desktop\foveon-calibration\exports`
	}
	exports := map[string]string{}
	tifs, _ := filepath.Glob(filepath.Join(exp, "*_A00.tif"))
	for _, p := range tifs {
		exports[strings.ToUpper(strings.TrimSuffix(filepath.Base(p), "_A00.tif"))] = p
	}
	paths := map[string]string{}
	extra := os.Getenv("T85_EXTRA_X3F")
	for _, root := range []string{desk, extra} {
		if root == "" {
			continue
		}
		filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.EqualFold(filepath.Ext(p), ".x3f") {
				return nil
			}
			name := strings.ToUpper(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
			if _, ok := paths[name]; !ok {
				paths[name] = p
			}
			return nil
		})
	}
	var names []string
	for n := range scores {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Println("name,group,model,iso,wb,evJpg,deJpg")
	for _, name := range names {
		p, ok := paths[name]
		e, ok2 := exports[name]
		if !ok || !ok2 {
			continue
		}
		f, err := x3f.Open(p)
		if err != nil {
			continue
		}
		ph := &Photo{Kind: "x3f", path: p, props: f.Props, Rotation: f.Rotation, Camera: f.Camera()}
		spp, err := calib.LoadLinear(e)
		if err != nil {
			continue
		}
		jpg, err := t85JPEG(ph, spp)
		if err != nil {
			continue
		}
		y := func(l *render.Linear) float64 {
			r, g, bb := t85Mean(l)
			return t85Y(r, g, bb)
		}
		fmt.Printf("%s,%s,%s,%s,%s,%.4f,%.3f\n", name, scores[name].Group, scores[name].Model, f.Props["ISO"], f.Props["WB_DESC"], math.Log2(y(jpg)/y(spp)), t85DE(jpg, spp))
	}
}

// TestT85KronDiag answers "is the LOW raw really 1.4 stops dark?" by forcing
// the DPM raw base that rawDecodable refuses onto the same LOW decode. It is
// a diagnosis only; the app still shows the camera JPEG, and spp-check now
// scores that. Run with T85_KRONDIAG=1.
func TestT85KronDiag(t *testing.T) {
	if os.Getenv("T85_KRONDIAG") == "" {
		t.Skip("set T85_KRONDIAG=1 to run the KRON LOW raw diagnosis")
	}
	desk := os.Getenv("T85_SAMPLES")
	if desk == "" {
		desk = `C:\Users\Korisnik\Desktop\foveon-samples`
	}
	exp := os.Getenv("T85_EXPORTS")
	if exp == "" {
		exp = `C:\Users\Korisnik\Desktop\foveon-calibration\exports`
	}
	ph := t85LoadPhoto(t, filepath.Join(desk, `archive\kron_processing_SDIM7824.X3F`))
	spp, err := calib.LoadLinear(filepath.Join(exp, `kron_processing_SDIM7824_A00.tif`))
	if err != nil {
		t.Fatal(err)
	}
	bare, err := linearForCheck(ph, spp) // no base: rawCorrectionFor is nil for LOW
	if err != nil {
		t.Fatal(err)
	}
	show := func(label string, l *render.Linear) {
		v := rgbaToLinear(render.Render(render.NewBase(l), preset.Neutral(), rawCompensation(ph)))
		s := sppCheckScore(v, spp)
		r, g, b := t85Mean(spp)
		ys := t85Y(r, g, b)
		r, g, b = t85Mean(v)
		fmt.Printf("%-18s Y %.6f (SPP %.6f, EV %+6.3f)  all %6.2f  area %5.2f  area@ev0 %5.2f\n", label, t85Y(r, g, b), ys, s.EV, s.All, s.Area, s.AreaEV0)
	}
	show("bare raw", bare)
	forced := *bare
	forced.Pix = append([]float32(nil), bare.Pix...)
	c := render.RawBaseFor(calibGroup(ph), wbDesc(ph))
	if c == nil {
		t.Fatal("no DPM raw base")
	}
	c.Apply(&forced)
	rawJPEGBalance(ph, &forced)
	render.BrightChromaDenoiseFor(&forced, calibGroup(ph))
	show("with DPM raw base", &forced)
}
