package main

// foveon-lab fit-raw: learn, per camera family, how to turn an x3f_extract
// raw decode into SPP's neutral rendering, from photos that have a neutral
// SPP export (<photo>_A00.tif in the calibration exports folder). Writes
// rawbase.json (copy it to render/rawbase.json and rebuild to ship it).

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

// stretchTo resamples l to w×h (nearest neighbour): reduced-resolution raw
// decodes come out at another aspect than the full-size render.
func stretchTo(l *render.Linear, w, h int) *render.Linear {
	if l.W == w && l.H == h {
		return l
	}
	o := &render.Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := ((y*l.H/h)*l.W + x*l.W/w) * 3
			copy(o.Pix[(y*w+x)*3:], l.Pix[s:s+3])
		}
	}
	return o
}

func rgbaToLinear(img *image.RGBA) *render.Linear {
	b := img.Bounds()
	l := &render.Linear{W: b.Dx(), H: b.Dy(), Pix: make([]float32, b.Dx()*b.Dy()*3)}
	for i := 0; i < l.W*l.H; i++ {
		for k := 0; k < 3; k++ {
			l.Pix[i*3+k] = render.ToLinear(float32(img.Pix[i*4+k]) / 255)
		}
	}
	return l
}

func fitRawCommand(desk string) {
	fsFlags := flag.NewFlagSet("fit-raw", flag.ExitOnError)
	dir := fsFlags.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder")
	photos := fsFlags.String("photos", desk, "folders with the X3F files (separated by ;)")
	fsFlags.Parse(os.Args[2:])
	if rawTool == "" {
		log.Fatal("fit-raw needs x3f_extract")
	}
	exports, _ := filepath.Glob(filepath.Join(*dir, calib.ExportsDir, "*_A00.tif"))
	byName := map[string]*Photo{}
	for _, p := range scanPhotos(strings.Split(*photos, ";"), false) {
		if p.Kind == "x3f" {
			byName[strings.ToUpper(strings.TrimSuffix(p.Name, filepath.Ext(p.Name)))] = p
		}
	}
	groups := map[string][]calib.RawPair{}
	for _, e := range exports {
		name := strings.TrimSuffix(filepath.Base(e), "_A00.tif")
		ph := byName[strings.ToUpper(name)]
		if ph == nil {
			fmt.Printf("  %s: X3F not found under -photos, skipped\n", name)
			continue
		}
		family := x3f.PresetFamily(ph.Camera)
		if family == "" {
			fmt.Printf("  %s: %s has no family, skipped\n", name, ph.Camera)
			continue
		}
		spp, err := calib.LoadLinear(e)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %s (%s): decoding raw…\n", name, ph.Camera)
		rimg, err := decodeRaw(ph)
		if err != nil {
			fmt.Printf("    raw decode failed: %v\n", err)
			continue
		}
		raw := stretchTo(render.FromImage(rimg, calib.AnalysisW, 0), spp.W, spp.H)
		var jpegErr float64 = -1
		if f, err := x3f.Open(ph.path); err == nil {
			if img, _, err := f.Preview(); err == nil {
				cam := stretchTo(render.FromImage(img, calib.AnalysisW, 0), spp.W, spp.H)
				em := render.Render(render.NewBase(cam), preset.Neutral(), render.CompensationFor(f.Props))
				jpegErr = calib.MeanError(rgbaToLinear(em), spp)
			}
		}
		if jpegErr < 0 {
			fmt.Printf("    its camera JPEG can't be read; used for fitting only as raw\n")
			jpegErr = calib.MeanError(raw, spp) // no better baseline exists
		}
		groups[family] = append(groups[family], calib.RawPair{Name: name, Raw: raw, SPP: spp, JPEGErr: jpegErr})
	}
	var fams []string
	for f := range groups {
		fams = append(fams, f)
	}
	sort.Strings(fams)
	var out []render.RawCorrection
	for _, f := range fams {
		c := calib.FitRawBase(f, groups[f])
		out = append(out, c)
		verdict := "camera JPEG stays (raw base not clearly better)"
		if c.UseRaw {
			verdict = "RAW BASE ON"
		}
		fmt.Printf("%s: %d photos, held-out error raw %.1f vs camera JPEG %.1f → %s\n", f, c.Photos, c.HeldOutRaw, c.HeldOutJPEG, verdict)
	}
	js, _ := json.MarshalIndent(out, "", " ")
	p := filepath.Join(*dir, "rawbase.json")
	if err := os.WriteFile(p, js, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Saved %s — copy it to render/rawbase.json and rebuild to use it.\n", p)
}
