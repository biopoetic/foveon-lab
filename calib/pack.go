package calib

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

// Candidate is an image scored for calibration usefulness.
type Candidate struct {
	Path   string
	Camera string
	Score  float64
	Why    string
}

// Suggest scores X3F files by how well they cover the tonal range and
// colours (wide brightness spread, real shadows and highlights, saturated
// colour, little clipping), using only the embedded thumbnails.
func Suggest(paths []string) []Candidate {
	var out []Candidate
	for _, p := range paths {
		f, err := x3f.Open(p)
		if err != nil {
			continue
		}
		data, err := f.ThumbJPEG()
		if err != nil {
			continue
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}
		l := render.FromImage(img, 0, 0)
		n := l.W * l.H
		vs := make([]float64, n)
		var dark, bright, clip, chroma float64
		for i := 0; i < n; i++ {
			j := i * 3
			y := luma(l.Pix, j)
			v := float64(render.ToSRGB(y))
			vs[i] = v
			if v < 0.12 {
				dark++
			}
			if v > 0.8 {
				bright++
			}
			for c := 0; c < 3; c++ {
				if l.Pix[j+c] > 0.99 {
					clip++
					break
				}
			}
			var c2 float64
			for c := 0; c < 3; c++ {
				c2 += math.Pow(float64(render.ToSRGB(l.Pix[j+c]))-v, 2)
			}
			chroma += math.Sqrt(c2)
		}
		sort.Float64s(vs)
		spread := vs[n*98/100] - vs[n*2/100]
		fd, fb, fc := dark/float64(n), bright/float64(n), clip/float64(n)
		mc := chroma / float64(n)
		score := spread + math.Min(fd, 0.15) + math.Min(fb, 0.15) + 2*mc - 3*fc
		out = append(out, Candidate{
			Path: p, Camera: f.Camera(), Score: score,
			Why: fmt.Sprintf("range %.0f%%, shadows %.0f%%, highlights %.0f%%, colour %.2f, clipped %.1f%%", spread*100, fd*100, fb*100, mc, fc*100),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// Files and folders of a calibration pack.
const (
	PackFile         = "FoveonLab_CALIBRATION.xml"
	InstructionsFile = "INSTRUCTIONS.txt"
	ExportsDir       = "exports"
)

// WritePack creates dir with the SPP preset XML, instructions and an empty
// exports folder. It never touches files already in the exports folder.
func WritePack(dir string, candidates []Candidate) (string, error) {
	if err := os.MkdirAll(filepath.Join(dir, ExportsDir), 0o755); err != nil {
		return "", err
	}
	steps := Steps()
	xmlPath := filepath.Join(dir, PackFile)
	if err := os.WriteFile(xmlPath, []byte(PackXML(steps)), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, InstructionsFile), []byte(Instructions(steps, candidates)), 0o644); err != nil {
		return "", err
	}
	return xmlPath, nil
}

// Instructions is the how-to shipped with the pack.
func Instructions(steps []Step, cands []Candidate) string {
	var b strings.Builder
	b.WriteString(`FOVEON LAB — SPP CALIBRATION
============================

Goal: measure exactly what Sigma Photo Pro does with each slider, so Foveon
Lab can emulate SPP with curves measured on your own photos instead of
guesses. Every [CAL] preset changes ONE setting only; everything else is
identical to the neutral preset A00.

STEPS
1. In SPP, import "` + PackFile + `" (like any other preset XML).
   Import it ONCE — importing again duplicates the presets.
2. Open one photo (suggestions below). Always the SAME photo.
3. For each code in turn: apply preset "[CAL] <code> ...", then save as
   TIFF into the "` + ExportsDir + `" folder next to this file, named:

       <photo name>_<code>.tif      e.g.  SDIM0031_A00.tif, SDIM0031_A03.tif

   Save settings — the SAME for every export:
     - TIFF (8 or 16 bit, either is fine), sRGB colour space
     - full size, no cropping or resizing
   (Highest-quality JPEG also works; TIFF is better.)
4. When done, run:   foveon-lab analyze
   It writes report.txt and calibration.json into this folder.

MOST IMPORTANT: A00 (neutral) must exist — everything is measured against it.
Do not touch sliders by hand between exports; just click the preset and save.

PRIORITIES
  A00–A22  required (23 exports, one photo) — covers every main slider
  B01–B22  nice to have — extreme values, Sharpness
  C00–C12  exploration: what the undocumented ColorMode codes mean. When
           you apply a C preset, note what SPP shows in its Color Mode
           menu (e.g. "C05 = Vivid") in ` + ExportsDir + `\colormode.txt
  If you have time: repeat A00–A22 on a second photo (or an SD15 photo) —
  SPP may process SD1 and SD15 files differently.

`)
	if len(cands) > 0 {
		b.WriteString("SUGGESTED PHOTOS (wide tonal and colour range, little clipping):\n")
		per := map[string]int{}
		for _, c := range cands {
			if per[c.Camera] >= 3 {
				continue
			}
			per[c.Camera]++
			fmt.Fprintf(&b, "  %-5s %s\n        %s\n", c.Camera, c.Path, c.Why)
		}
		b.WriteString("\n")
	}
	b.WriteString("PRESET LIST\n")
	for _, s := range steps {
		fmt.Fprintf(&b, "  %s  %s\n", s.Code, s.Label)
	}
	return b.String()
}