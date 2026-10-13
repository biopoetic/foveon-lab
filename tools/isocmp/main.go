// isocmp compares noise reduction on an ISO series of one scene (tripod,
// same light), each pipeline against its OWN low-ISO frame, on a 1:1 crop
// (no downscaling, which would average the noise away):
//
//   - noise + detail: dE2000 after a global colour/tone fit of the frame to
//     its reference (a 3×3 matrix and a curve per channel), so a pipeline
//     that renders high ISO a little warmer or darker is not counted as
//     noisy; what is left is noise, smearing and lost detail;
//   - render shift: dE2000 without that fit (colour/tone moving with ISO).
//
// Pipelines: SPP's export, Foveon Lab's raw base with its noise reduction,
// and the same without noise reduction. It also writes side-by-side 1:1
// crops (SPP | Foveon Lab | no NR) per frame.
//
//	go run ./tools/isocmp -out dir -crop x,y,w,h ref.X3F ref_spp.tif frame.X3F frame_spp.tif …
package main

import (
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

func main() {
	out := flag.String("out", ".", "folder for the side-by-side crops")
	cropF := flag.String("crop", "", "x,y,w,h of the 1:1 crop (default: the centre 1200×800)")
	strength := flag.Float64("nr", 1, "Foveon Lab noise reduction strength")
	flag.Parse()
	a := flag.Args()
	if len(a) < 4 || len(a)%2 != 0 {
		log.Fatal("usage: isocmp [-out dir] [-crop x,y,w,h] ref.X3F ref_spp.tif frame.X3F frame_spp.tif …")
	}
	var crop image.Rectangle
	if *cropF != "" {
		var x, y, w, h int
		fmt.Sscanf(*cropF, "%d,%d,%d,%d", &x, &y, &w, &h)
		crop = image.Rect(x, y, x+w, y+h)
	}
	type frame struct{ spp, ours, plain *render.Linear }
	load := func(x3fPath, sppPath string) (frame, string) {
		f, err := x3f.Open(x3fPath)
		if err != nil {
			log.Fatal(err)
		}
		spp, err := loadTIFF(sppPath)
		if err != nil {
			log.Fatal(err)
		}
		if crop.Empty() {
			crop = image.Rect(spp.W/2-600, spp.H/2-400, spp.W/2+600, spp.H/2+400)
		}
		ours, err := develop(f, *strength)
		if err != nil {
			log.Fatal(err)
		}
		plain, err := develop(f, 0)
		if err != nil {
			log.Fatal(err)
		}
		return frame{cut(spp, crop), cut(ours, crop), cut(plain, crop)}, f.Props["ISO"]
	}
	ref, refISO := load(a[0], a[1])
	fmt.Printf("reference ISO %s, crop %v, noise reduction %.1f\n", refISO, crop, *strength)
	fmt.Printf("%-6s | %-29s | %-29s\n", "", "noise + detail (fitted dE)", "render shift (plain dE)")
	fmt.Printf("%-6s | %9s %9s %9s | %9s %9s %9s\n", "ISO", "SPP", "FoveonLab", "no NR", "SPP", "FoveonLab", "no NR")
	for i := 2; i+1 < len(a); i += 2 {
		fr, iso := load(a[i], a[i+1])
		row := [6]float64{}
		for k, p := range [][2]*render.Linear{{fr.spp, ref.spp}, {fr.ours, ref.ours}, {fr.plain, ref.plain}} {
			row[k] = fitted(p[0], p[1])
			row[3+k], _ = calib.DeltaEStats(p[0], p[1])
		}
		fmt.Printf("%-6s | %9.2f %9.2f %9.2f | %9.2f %9.2f %9.2f\n", iso, row[0], row[1], row[2], row[3], row[4], row[5])
		name := strings.TrimSuffix(filepath.Base(a[i]), filepath.Ext(a[i]))
		if err := sideBySide(filepath.Join(*out, fmt.Sprintf("iso%s_%s.jpg", iso, name)), fr.spp, fr.ours, fr.plain); err != nil {
			log.Fatal(err)
		}
	}
}

// fitted is the dE2000 of l against ref after the best global colour/tone
// map of l onto ref.
func fitted(l, ref *render.Linear) float64 {
	c := calib.FitRawBase("x", []calib.RawPair{{Raw: l, SPP: ref}})
	m := &render.Linear{W: l.W, H: l.H, Pix: append([]float32(nil), l.Pix...)}
	c.Apply(m)
	d, _ := calib.DeltaEStats(m, ref)
	return d
}

// develop is the app's raw path (camfread.go goLinear + rawLinear) at full
// size: native decode, highlights, Foveon noise reduction, the camera
// group's raw base.
func develop(f *x3f.File, strength float64) (*render.Linear, error) {
	c, err := f.CAMF()
	if err != nil {
		return nil, err
	}
	raw, err := f.DecodeRaw()
	if err != nil {
		return nil, err
	}
	x3f.FixBadPixels(raw, c)
	wb := c.CameraWB(strings.TrimSpace(f.Props["WB_DESC"]))
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		return nil, err
	}
	gain, cc, err := c.WBTransform(wb)
	if err != nil {
		return nil, err
	}
	x3f.ClipHighlights(img, gain)
	if strength > 0 {
		x3f.Denoise(img, x3f.DenoiseOptions{Strength: strength, Scale: 1})
	}
	l := render.FromCamLinear(img.Pix, img.W, img.H, 0, 0, render.CamTransform{Gain: gain, CC: cc})
	if b := render.RawBaseFitted(x3f.CalibGroup(f.Props["CAMMODEL"])); b != nil {
		b.Apply(l)
	}
	return l, nil
}

func loadTIFF(p string) (*render.Linear, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	img, _, err := render.DecodeImage(data) // tolerates SPP's unsorted TIFF tags
	if err != nil {
		return nil, err
	}
	return render.FromImage(img, 0, 0), nil
}

func cut(l *render.Linear, r image.Rectangle) *render.Linear {
	r = r.Intersect(image.Rect(0, 0, l.W, l.H))
	o := &render.Linear{W: r.Dx(), H: r.Dy(), Pix: make([]float32, r.Dx()*r.Dy()*3)}
	for y := 0; y < r.Dy(); y++ {
		copy(o.Pix[y*o.W*3:(y+1)*o.W*3], l.Pix[((y+r.Min.Y)*l.W+r.Min.X)*3:])
	}
	return o
}

func sideBySide(path string, ls ...*render.Linear) error {
	w, h := 0, 0
	for _, l := range ls {
		w += l.W + 8
		h = max(h, l.H)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	x0 := 0
	for _, l := range ls {
		for y := 0; y < l.H; y++ {
			for x := 0; x < l.W; x++ {
				o := img.PixOffset(x0+x, y)
				for k := 0; k < 3; k++ {
					v := float64(render.ToSRGB(float32(math.Max(0, math.Min(1, float64(l.Pix[(y*l.W+x)*3+k]))))))
					img.Pix[o+k] = uint8(math.Round(v * 255))
				}
				img.Pix[o+3] = 255
			}
		}
		x0 += l.W + 8
	}
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	return jpeg.Encode(fh, img, &jpeg.Options{Quality: 92})
}
