// devcmp compares x3f.CameraRGB with x3f_extract -color none (linear camera
// RGB after x3fuse's preprocessing) on a coarse grid: per-channel ratio
// and spread, and the levels used.
package main

import (
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/biopoetic/foveon-lab/x3f"
	"golang.org/x/image/tiff"
)

func main() {
	tool := os.Args[1]
	tmp, _ := os.MkdirTemp("", "devcmp")
	defer os.RemoveAll(tmp)
	for _, p := range os.Args[2:] {
		f, _ := x3f.Open(p)
		c, _ := f.CAMF()
		raw, _ := f.DecodeRaw()
		g, err := x3f.CameraRGB(raw, c, sgFor(f, c), wbOf(f))
		if err != nil {
			fmt.Println(err)
			continue
		}
		exec.Command(tool, "-q", "-tiff", "-color", "none", "-no-denoise", "-o", tmp, p).Run()
		tp := filepath.Join(tmp, filepath.Base(p)+".tif")
		fh, _ := os.Open(tp)
		img, err := tiff.Decode(fh)
		fh.Close()
		os.Remove(tp)
		if err != nil {
			fmt.Println(err)
			continue
		}
		e := img.(*image.RGBA64)
		fmt.Printf("%s: go %v extract %v  ImageDepth %v RawSat %v Sat %v\n", filepath.Base(p), g.Bounds().Size(), e.Bounds().Size(), c.Matrices["ImageDepth"], c.Matrices["RawSaturationLevel"], c.Matrices["SaturationLevel"])
		var sg, se [3]float64
		var mx [2][3]float64
		for y := 0.05; y < 0.95; y += 0.01 {
			for x := 0.05; x < 0.95; x += 0.01 {
				a := g.RGBA64At(int(x*float64(g.Bounds().Dx())), int(y*float64(g.Bounds().Dy())))
				b := e.RGBA64At(int(x*float64(e.Bounds().Dx())), int(y*float64(e.Bounds().Dy())))
				av := [3]float64{float64(a.R), float64(a.G), float64(a.B)}
				bv := [3]float64{float64(b.R), float64(b.G), float64(b.B)}
				for k := 0; k < 3; k++ {
					sg[k] += av[k]
					se[k] += bv[k]
					mx[0][k] = max(mx[0][k], av[k])
					mx[1][k] = max(mx[1][k], bv[k])
				}
			}
		}
		// Shading check: go/extract ratio in each corner relative to the centre.
		// Slope of extract against go per channel (least squares, so a
		// black-level offset does not count) in a region.
		region := func(fx, fy float64) [3]float64 {
			var sx, sy, sxx, sxy, n [3]float64
			for y := fy - 0.05; y < fy+0.05; y += 0.002 {
				for x := fx - 0.05; x < fx+0.05; x += 0.002 {
					p := g.RGBA64At(int(x*float64(g.Bounds().Dx())), int(y*float64(g.Bounds().Dy())))
					q := e.RGBA64At(int(x*float64(e.Bounds().Dx())), int(y*float64(e.Bounds().Dy())))
					a := [3]float64{float64(p.R), float64(p.G), float64(p.B)}
					b := [3]float64{float64(q.R), float64(q.G), float64(q.B)}
					for k := 0; k < 3; k++ {
						sx[k] += a[k]
						sy[k] += b[k]
						sxx[k] += a[k] * a[k]
						sxy[k] += a[k] * b[k]
						n[k]++
					}
				}
			}
			var out [3]float64
			for k := 0; k < 3; k++ {
				out[k] = (sxy[k] - sx[k]*sy[k]/n[k]) / (sxx[k] - sx[k]*sx[k]/n[k])
			}
			return out
		}
		mid := region(0.5, 0.5)
		worst := 0.0
		for _, pt := range [][2]float64{{0.05, 0.05}, {0.95, 0.05}, {0.05, 0.95}, {0.95, 0.95}, {0.5, 0.05}, {0.05, 0.5}} {
			r := region(pt[0], pt[1])
			for k := 0; k < 3; k++ {
				worst = math.Max(worst, math.Abs(r[k]/mid[k]-1))
			}
		}
		fmt.Printf("   shading mismatch (edge vs centre, worst channel): %.1f%%\n", worst*100)
		fmt.Printf("   mean go %.0f %.0f %.0f | extract %.0f %.0f %.0f | ratio %.3f %.3f %.3f | max go %v extract %v\n", sg[0], sg[1], sg[2], se[0], se[1], se[2], sg[0]/se[0], sg[1]/se[1], sg[2]/se[2], mx[0], mx[1])
	}
}

func sgFor(f *x3f.File, c *x3f.CAMF) *x3f.SpatialGain {
	if os.Getenv("NOSG") != "" {
		return nil
	}
	return x3f.NewSpatialGain(c, f.Props, "Sunlight")
}

// wbOf maps the photo's WB_DESC to CAMF's name, as camfread.go does.
func wbOf(f *x3f.File) string {
	m := map[string]string{"Daylight": "Sunlight", "Sunlight": "Sunlight", "Shade": "Shade", "Overcast": "Overcast",
		"Incandescent": "Incandescent", "Fluorescent": "Fluorescent", "Flash": "Flash", "Auto": "Auto", "Custom": "Custom"}
	return m[strings.TrimSpace(f.Props["WB_DESC"])]
}
