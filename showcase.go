package main

// showcase: given frames and crop boxes, write the same pixels from the
// default render, Max Detail and Sigma Photo Pro's neutral export, at one
// scale, with the measured EV beside them.
//
// It exists because task 20 compared crops that were not the same pixels:
// a portrait frame's raw development was capped at 1200 px before the turn
// (800 px wide after it), while the SPP export was reduced to 1200 px after
// the turn (1200 px wide). The SPP crop was therefore 1.5x larger and the EV
// numbers were read off a different part of the picture. Here every variant
// is brought to the exact same upright frame size first; the crop box is then
// applied to that one frame, so all three crops are the same pixels by
// construction.
//
//	foveon-lab showcase -manifest zz-out/task22/showcase.json
//	go run ./tools/showcase -manifest zz-out/task22/showcase.json
//
// manifest:
//
//	{"out":"zz-out/task22/showcase","items":[
//	  {"name":"violin","frame":"sigma-photo_Violin.X3F",
//	   "spp":"…\\sigma-photo_Violin_A00.tif","crop":[0,0.55,0.5,0.42]}]}
//
// A crop's four numbers are fractions of the upright frame when w and h are
// <= 1, and pixels of that frame when either is > 1.

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

type showcaseCrop struct {
	Name  string    `json:"name"`
	Frame string    `json:"frame"`
	SPP   string    `json:"spp,omitempty"`
	Crop  []float64 `json:"crop"`
}

type showcaseManifest struct {
	Out     string         `json:"out"`
	Width   int            `json:"width,omitempty"`
	Items   []showcaseCrop `json:"items"`
	Exports string         `json:"exports,omitempty"`
}

type showcaseRow struct {
	Item, Frame, Body, Variant string
	W, H                       int
	Mean, EVBefore, EVSPP      float64
}

func showcaseCommand() bool {
	fs := flag.NewFlagSet("showcase", flag.ExitOnError)
	manifest := fs.String("manifest", "", "JSON file with frames and crop boxes")
	out := fs.String("out", "", "output folder (default: the manifest's out; must be under zz-out)")
	width := fs.Int("width", 0, "working width (default: the manifest's width, else 1200)")
	fs.Parse(os.Args[2:])
	if *manifest == "" {
		fmt.Fprintln(os.Stderr, "showcase: -manifest is required")
		os.Exit(2)
	}
	var m showcaseManifest
	b, err := os.ReadFile(*manifest)
	if err != nil {
		fatalShowcase(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		fatalShowcase(fmt.Errorf("%s: %w", *manifest, err))
	}
	if *out != "" {
		m.Out = *out
	}
	if m.Out == "" {
		m.Out = filepath.Join("zz-out", "showcase")
	}
	if !underZZOut(m.Out) {
		fatalShowcase(fmt.Errorf("output %q is not under zz-out: crops stay out of the repository", m.Out))
	}
	if *width != 0 {
		m.Width = *width
	}
	if m.Width <= 0 {
		m.Width = 1200
	}
	if len(m.Items) == 0 {
		fatalShowcase(errors.New("the manifest has no items"))
	}
	if err := os.MkdirAll(m.Out, 0o755); err != nil {
		fatalShowcase(err)
	}
	// The samples live on the Desktop, read-only. A frame may be a base name
	// or a path; a base name is looked up under the samples folder.
	photos := map[string]*Photo{}
	roots := []string{`C:\Users\Korisnik\Desktop\foveon-samples`, `C:\Users\Korisnik\Desktop`}
	if v := os.Getenv("FOVEON_SAMPLES"); v != "" {
		roots = []string{v}
	}
	for _, root := range roots {
		for _, ph := range scanPhotos([]string{root}, false) {
			if _, ok := photos[ph.Name]; !ok {
				photos[ph.Name] = ph
			}
		}
	}
	exports := m.Exports
	if exports == "" {
		exports = filepath.Join(os.Getenv("USERPROFILE"), "Desktop", "foveon-calibration", "exports")
	}
	var rows []showcaseRow
	for _, it := range m.Items {
		if it.Name == "" || len(it.Crop) != 4 {
			fatalShowcase(fmt.Errorf("item %q needs a name and crop [x,y,w,h]", it.Name))
		}
		ph := photos[filepath.Base(it.Frame)]
		if ph == nil {
			// An absolute path outside the roots is read directly.
			if st, err := os.Stat(it.Frame); err == nil && !st.IsDir() {
				info := &Photo{ID: idOf(it.Frame), Name: filepath.Base(it.Frame), path: it.Frame, Info: map[string]string{}, Kind: "x3f"}
				if loadMeta(info) {
					ph = info
				}
			}
		}
		if ph == nil {
			fatalShowcase(fmt.Errorf("frame %q not found under %v", it.Frame, roots))
		}
		sppPath := it.SPP
		if sppPath == "" {
			sppPath = filepath.Join(exports, strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name))+"_A00.tif")
		}
		sppFull, err := calib.LoadLinear(sppPath)
		if err != nil {
			fatalShowcase(fmt.Errorf("%s: %w", it.Name, err))
		}
		before, err := rawLinearFor(ph, m.Width, "sRGB", "")
		if err != nil {
			fatalShowcase(fmt.Errorf("%s: default render: %w", it.Name, err))
		}
		md, err := rawMaxDetail(ph, m.Width, defaultNR)
		if err != nil {
			fatalShowcase(fmt.Errorf("%s: Max Detail: %w", it.Name, err))
		}
		// rawLinearFor turns the frame upright; rawMaxDetail keeps the
		// sensor's own orientation, so turn it the same way before the two
		// are compared. Without this a portrait frame's Max Detail came out
		// landscape and stretchTo squeezed it onto the wrong pixels.
		if ph.Rotation != 0 {
			md = md.Rotate(ph.Rotation)
		}
		// The SPP export is already upright. Bring it to our post-rotation
		// frame, never upscale: the common width is the smaller of the two.
		targetW := min(before.W, sppFull.W)
		if targetW < 2 {
			fatalShowcase(fmt.Errorf("%s: bad frame size", it.Name))
		}
		if before.W != targetW {
			before = before.Downscale(targetW)
		}
		if md.W != targetW {
			md = md.Downscale(targetW)
		}
		targetH := before.H
		spp := sppFull.Downscale(targetW)
		if spp.H != targetH {
			// One pixel of rounding: stretchTo keeps the pixels aligned
			// without touching the colour values.
			spp = stretchTo(spp, targetW, targetH)
		}
		before = stretchTo(before, targetW, targetH)
		md = stretchTo(md, targetW, targetH)
		vars := []struct {
			name string
			l    *render.Linear
		}{
			{"before", before},
			{"maxdetail", md},
			{"spp_a00", spp},
		}
		// Render before/maxdetail first: the crop must be measured on the
		// final 8-bit picture, as the app shows it.
		vars[0].l = rgbaToLinear(render.Render(render.NewBase(before), preset.Neutral(), rawCompensation(ph)))
		vars[1].l = rgbaToLinear(render.Render(render.NewBase(md), maxDetailRenderParams(preset.Neutral()), rawCompensation(ph)))
		cx, cy, cw, ch := showcaseBox(it.Crop, targetW, targetH)
		var means [3]float64
		for i, v := range vars {
			cl := showcaseCropLinear(v.l, cx, cy, cw, ch)
			means[i] = showcaseMean(cl)
			if err := showcaseSavePNG(filepath.Join(m.Out, safeName(it.Name)+"_"+v.name+".png"), cl); err != nil {
				fatalShowcase(err)
			}
			rows = append(rows, showcaseRow{Item: it.Name, Frame: ph.Name, Body: modelOf(ph), Variant: v.name, W: cw, H: ch, Mean: means[i]})
		}
		for i := len(rows) - 3; i < len(rows); i++ {
			ev := 0.0
			if means[0] > 1e-12 {
				ev = math.Log2(rows[i].Mean / means[0])
			}
			evS := 0.0
			if means[2] > 1e-12 {
				evS = math.Log2(rows[i].Mean / means[2])
			}
			rows[i].EVBefore, rows[i].EVSPP = ev, evS
		}
		fmt.Printf("%-14s %-10s %4dx%-4d before %.5f  maxdetail %.5f  spp %.5f  EV(md-before) %+0.3f  EV(md-spp) %+0.3f\n",
			it.Name, modelOf(ph), cw, ch, means[0], means[1], means[2], rows[len(rows)-2].EVBefore, rows[len(rows)-2].EVSPP)
	}
	if err := showcaseWriteCSV(filepath.Join(m.Out, "showcase.csv"), rows); err != nil {
		fatalShowcase(err)
	}
	if err := showcaseWriteTXT(filepath.Join(m.Out, "showcase.txt"), rows); err != nil {
		fatalShowcase(err)
	}
	fmt.Printf("wrote %d crops to %s (same frame, same box, same pixels)\n", len(rows), m.Out)
	return true
}

func fatalShowcase(err error) {
	fmt.Fprintln(os.Stderr, "showcase:", err)
	os.Exit(1)
}

func underZZOut(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	zz, err := filepath.Abs("zz-out")
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(zz, abs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// showcaseBox turns a fractional or pixel crop into an integer box clamped to
// the common frame. The same box is used for every variant.
func showcaseBox(c []float64, w, h int) (x, y, cw, ch int) {
	if c[2] <= 1 && c[3] <= 1 {
		x, y = int(math.Round(c[0]*float64(w))), int(math.Round(c[1]*float64(h)))
		cw, ch = int(math.Round(c[2]*float64(w))), int(math.Round(c[3]*float64(h)))
	} else {
		x, y, cw, ch = int(math.Round(c[0])), int(math.Round(c[1])), int(math.Round(c[2])), int(math.Round(c[3]))
	}
	x, y = max(0, min(w-2, x)), max(0, min(h-2, y))
	cw, ch = max(2, min(w-x, cw)), max(2, min(h-y, ch))
	return
}

func showcaseCropLinear(l *render.Linear, x, y, w, h int) *render.Linear {
	o := &render.Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: l.Scale, Raw: l.Raw, ISO: l.ISO, Sensor: l.Sensor, Frame: l.Frame}
	for yy := 0; yy < h; yy++ {
		copy(o.Pix[yy*w*3:(yy+1)*w*3], l.Pix[((yy+y)*l.W+x)*3:])
	}
	return o
}

func showcaseMean(l *render.Linear) float64 {
	var s float64
	for i := 0; i < l.W*l.H; i++ {
		s += 0.2126*float64(l.Pix[i*3]) + 0.7152*float64(l.Pix[i*3+1]) + 0.0722*float64(l.Pix[i*3+2])
	}
	return s / float64(l.W*l.H)
}

func showcaseSavePNG(path string, l *render.Linear) error {
	return showcaseWritePNG(path, linearToSRGB16(l))
}

func showcaseWritePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func showcaseWriteCSV(path string, rows []showcaseRow) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	w.Write([]string{"item", "frame", "body", "variant", "width", "height", "mean_lin", "ev_vs_before", "ev_vs_spp"})
	for _, r := range rows {
		w.Write([]string{r.Item, r.Frame, r.Body, r.Variant,
			fmt.Sprint(r.W), fmt.Sprint(r.H), fmt.Sprintf("%.6f", r.Mean),
			fmt.Sprintf("%+.4f", r.EVBefore), fmt.Sprintf("%+.4f", r.EVSPP)})
	}
	return w.Error()
}

func showcaseWriteTXT(path string, rows []showcaseRow) error {
	var b strings.Builder
	b.WriteString("Foveon Lab showcase pairs: same frame, same crop box, same pixels\n")
	b.WriteString("EV is log2(mean linear luminance in the crop / the reference's).\n\n")
	byItem := map[string][]showcaseRow{}
	var items []string
	for _, r := range rows {
		if _, ok := byItem[r.Item]; !ok {
			items = append(items, r.Item)
		}
		byItem[r.Item] = append(byItem[r.Item], r)
	}
	sort.Strings(items)
	for _, it := range items {
		rs := byItem[it]
		fmt.Fprintf(&b, "%s  (%s, %s)\n", it, rs[0].Frame, rs[0].Body)
		for _, r := range rs {
			fmt.Fprintf(&b, "  %-10s %4dx%-4d mean %.6f  EV vs before %+0.3f  EV vs SPP %+0.3f\n",
				r.Variant, r.W, r.H, r.Mean, r.EVBefore, r.EVSPP)
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
