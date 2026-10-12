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

// mergeModelsCommand: foveon-lab merge-models -o out.json a.json b.json …
func mergeModelsCommand() {
	fsFlags := flag.NewFlagSet("merge-models", flag.ExitOnError)
	out := fsFlags.String("o", "", "output model file")
	fsFlags.Parse(os.Args[2:])
	var ms []*render.Model
	for _, p := range fsFlags.Args() {
		b, err := os.ReadFile(p)
		if err != nil {
			log.Fatal(err)
		}
		var m render.Model
		if err := json.Unmarshal(b, &m); err != nil {
			log.Fatalf("%s: %v", p, err)
		}
		ms = append(ms, &m)
	}
	if *out == "" || len(ms) == 0 {
		log.Fatal("usage: foveon-lab merge-models -o out.json model_a.json model_b.json …")
	}
	js, _ := json.MarshalIndent(calib.MergeModels(ms), "", " ")
	if err := os.WriteFile(*out, js, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("merged %d models into %s\n", len(ms), *out)
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
	jgroups := map[string][]calib.JPEGPair{}
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
		// Skip scenes at the edge of sRGB (e.g. a saturated red flower): SPP
		// works on wider data there, so they teach clipping, not colour.
		var edge int
		for i := 0; i < len(spp.Pix); i += 3 {
			lo := min(spp.Pix[i], spp.Pix[i+1], spp.Pix[i+2])
			hi := max(spp.Pix[i], spp.Pix[i+1], spp.Pix[i+2])
			if lo < 0.0005 && hi > 0.2 || hi > 0.99 {
				edge++
			}
		}
		if frac := float64(edge) / float64(len(spp.Pix)/3); frac > 0.3 {
			fmt.Printf("  %s: %.0f%% of it at the sRGB edge or clipped, skipped\n", name, frac*100)
			continue
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
				props := f.Props
				neutral := func(l *render.Linear) *render.Linear {
					return rgbaToLinear(render.Render(render.NewBase(l), preset.Neutral(), render.CompensationFor(props)))
				}
				jpegErr = calib.MeanError(neutral(cam), spp)
				jgroups[family] = append(jgroups[family], calib.JPEGPair{Name: name, JPEG: cam, SPP: spp, AutoWB: props["WB_DESC"] == "Auto", Neutral: neutral})
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
	var bal []render.JPEGBalance
	for _, f := range fams {
		b := calib.FitJPEGBalance(f, jgroups[f])
		bal = append(bal, b)
		verdict := "off (not clearly better)"
		if b.Use {
			verdict = "ON"
		}
		fmt.Printf("%s camera-JPEG balance: %d photos, held-out %.1f vs %.1f without -> %s\n", f, b.Photos, b.HeldOut, b.Without, verdict)
	}
	jb, _ := json.MarshalIndent(bal, "", " ")
	pj := filepath.Join(*dir, "jpegwb.json")
	if err := os.WriteFile(pj, jb, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Saved %s and %s: copy them to render/ and rebuild to use them.\n", p, pj)
}
