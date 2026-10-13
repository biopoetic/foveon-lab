// rescmp prints, per X3F, its resolution, how well x3f_extract's sRGB decode
// lines up with the camera JPEG (luma correlation and best shift on a
// normalised grid) and the per-channel colour ratio raw/JPEG in mid-tones.
package main

import (
	"fmt"
	_ "image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
	_ "golang.org/x/image/tiff"
)

func main() {
	tool := os.Args[1]
	tmp, _ := os.MkdirTemp("", "rescmp")
	defer os.RemoveAll(tmp)
	for _, p := range os.Args[2:] {
		f, err := x3f.Open(p)
		if err != nil {
			fmt.Println(p, err)
			continue
		}
		jb, _, err := f.PreviewJPEG()
		if err != nil {
			fmt.Println(p, err)
			continue
		}
		pv, _, err := render.DecodeImage(jb)
		if err != nil {
			fmt.Println(p, err)
			continue
		}
		ref := "JPEG"
		if dir := os.Getenv("REF_DIR"); dir != "" {
			name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
			if e := calib.ExportFile(dir, name, "A00"); e != "" {
				if d, err := os.ReadFile(e); err == nil {
					if im, _, err := render.DecodeImage(d); err == nil {
						pv, ref = im, "SPP "+fmt.Sprint(im.Bounds().Dx())+"x"+fmt.Sprint(im.Bounds().Dy())
					}
				}
			}
		}
		res := f.Props["RESOLUTION"]
		if out, err := exec.Command(tool, "-q", "-tiff", "-color", "sRGB", "-no-denoise", "-o", tmp, p).CombinedOutput(); err != nil {
			fmt.Println(p, err, string(out))
			continue
		}
		tp := filepath.Join(tmp, filepath.Base(p)+".tif")
		d, _ := os.ReadFile(tp)
		os.Remove(tp)
		ri, _, err := render.DecodeImage(d)
		if err != nil {
			fmt.Println(p, err)
			continue
		}
		raw := render.FromImage(ri, 300, 0)
		jp := render.FromImage(pv, 300, 0)
		at := func(l *render.Linear, x, y float64) int {
			return ((int(y*float64(l.H)))*l.W + int(x*float64(l.W))) * 3
		}
		// Structure: luma correlation over the central 80%, searching a
		// small shift of the raw grid (a crop would show up as a shift).
		corr := func(dx, dy float64) float64 {
			var sa, sb, saa, sbb, sab, m float64
			for y := 0.1; y < 0.9; y += 0.01 {
				for x := 0.1; x < 0.9; x += 0.0067 {
					ia, ib := at(raw, x+dx, y+dy), at(jp, x, y)
					a := float64(raw.Pix[ia] + raw.Pix[ia+1] + raw.Pix[ia+2])
					b := float64(jp.Pix[ib] + jp.Pix[ib+1] + jp.Pix[ib+2])
					sa, sb, saa, sbb, sab, m = sa+a, sb+b, saa+a*a, sbb+b*b, sab+a*b, m+1
				}
			}
			return (sab/m - sa/m*sb/m) / math.Sqrt((saa/m-sa*sa/m/m)*(sbb/m-sb*sb/m/m))
		}
		best, bx, by := -2.0, 0.0, 0.0
		for dy := -0.05; dy <= 0.051; dy += 0.01 {
			for dx := -0.05; dx <= 0.051; dx += 0.01 {
				if c := corr(dx, dy); c > best {
					best, bx, by = c, dx, dy
				}
			}
		}
		var s [2][3]float64
		for y := 0.0; y < 1; y += 0.01 {
			for x := 0.0; x < 1; x += 0.0067 {
				ia, ib := at(raw, x, y), at(jp, x, y)
				if l := jp.Pix[ib] + jp.Pix[ib+1] + jp.Pix[ib+2]; l < 0.1 || l > 2 {
					continue
				}
				for k := 0; k < 3; k++ {
					s[0][k] += float64(raw.Pix[ia+k])
					s[1][k] += float64(jp.Pix[ib+k])
				}
			}
		}
		g := s[0][1] / s[1][1]
		fmt.Printf("%s vs %s: %s %-4s %dx%d  corr %.3f shift (%.2f,%.2f)  raw/jpeg R %.2f B %.2f (G=1)\n",
			ref, "", filepath.Base(p), res, ri.Bounds().Dx(), ri.Bounds().Dy(), best, bx, by, s[0][0]/s[1][0]/g, s[0][2]/s[1][2]/g)
	}
}
