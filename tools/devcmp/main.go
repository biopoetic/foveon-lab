// devcmp compares x3f.CameraRGB with x3f_extract -color none (linear camera
// RGB after x3fuse's preprocessing) on a coarse grid: per-channel ratio
// and spread, and the levels used.
package main

import (
	"fmt"
	"image"
	imgcolor "image/color"
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
		wb := c.CameraWB(strings.TrimSpace(f.Props["WB_DESC"]))
		g, err := x3f.CameraRGB(raw, c, sgFor(f, c), wb)
		color := "none"
		if os.Getenv("SRGB") != "" {
			// Compare linear sRGB: ours through the CAMF white balance and
			// matrix, x3f_extract's -color sRGB decoded from its gamma.
			color = "sRGB"
			g, err = linearSRGB(raw, c, sgFor(f, c), wb)
		}
		if err != nil {
			fmt.Println(filepath.Base(p), "go:", err)
			continue
		}
		if out, err := exec.Command(tool, append([]string{"-q", "-tiff", "-color", color, "-no-denoise", "-o", tmp}, append(strings.Fields(os.Getenv("XARGS")), p)...)...).CombinedOutput(); err != nil {
			fmt.Println(filepath.Base(p), "extract:", err, string(out))
		}
		tp := filepath.Join(tmp, filepath.Base(p)+".tif")
		fh, _ := os.Open(tp)
		img, err := tiff.Decode(fh)
		fh.Close()
		os.Remove(tp)
		if err != nil {
			fmt.Println(filepath.Base(p), "extract tiff:", err)
			continue
		}
		e := img.(*image.RGBA64)
		if color == "sRGB" {
			for i := 0; i < len(e.Pix); i += 2 {
				v := float64(uint16(e.Pix[i])<<8|uint16(e.Pix[i+1])) / 65535
				if v <= 0.04045 {
					v /= 12.92
				} else {
					v = math.Pow((v+0.055)/1.055, 2.4)
				}
				u := uint16(math.Round(v * 65535))
				e.Pix[i], e.Pix[i+1] = byte(u>>8), byte(u)
			}
		}
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
		if os.Getenv("PAIRS") != "" {
			W, H := g.Bounds().Dx(), g.Bounds().Dy()
			for _, f := range [][2]float64{{0.9, 0.1}, {0.5, 0.5}, {0.9, 0.9}} {
				fmt.Printf("   at %.1f,%.1f go/extract top:", f[0], f[1])
				for i := 0; i < 8; i++ {
					x, y := int(f[0]*float64(W))+i*37, int(f[1]*float64(H))+i*11
					fmt.Printf(" %d/%d", g.RGBA64At(x, y).R, e.RGBA64At(x, y).R)
				}
				fmt.Println()
			}
		}
		if os.Getenv("FIT") != "" {
			// extract = a*go + b per channel over unclipped pixels; the
			// residual is what an offset and a gain cannot explain.
			W, H := g.Bounds().Dx(), g.Bounds().Dy()
			for k := 0; k < 3; k++ {
				var sx, sy, sxx, sxy, syy, n float64
				ch := func(c imgcolor.RGBA64) float64 { return float64([3]uint16{c.R, c.G, c.B}[k]) }
				for y := 0; y < H; y += 4 {
					for x := 0; x < W; x += 4 {
						a, b := ch(g.RGBA64At(x, y)), ch(e.RGBA64At(x, y))
						if a > 50000 {
							continue
						}
						sx, sy, sxx, sxy, syy, n = sx+a, sy+b, sxx+a*a, sxy+a*b, syy+b*b, n+1
					}
				}
				sl := (sxy - sx*sy/n) / (sxx - sx*sx/n)
				off := (sy - sl*sx) / n
				res := (syy - sy*sy/n) - sl*(sxy-sx*sy/n)
				shown := 0
				var rows [20]int
				var tot [20]int
				for y := 0; y < H; y += 4 {
					for x := 0; x < W; x += 4 {
						a, b := ch(g.RGBA64At(x, y)), ch(e.RGBA64At(x, y))
						if a > 50000 {
							continue
						}
						tot[y*20/H]++
						if math.Abs(sl*a+off-b) > 0.3*(sy/n) {
							rows[y*20/H]++
						}
					}
				}
				fmt.Print("     outlier share by row band:")
				for i := range rows {
					fmt.Printf(" %.0f%%", 100*float64(rows[i])/float64(max(1, tot[i])))
				}
				fmt.Println()
				for y := 0; y < H && shown < 6; y += 4 {
					for x := 0; x < W && shown < 6; x += 4 {
						a, b := ch(g.RGBA64At(x, y)), ch(e.RGBA64At(x, y))
						if a <= 50000 && math.Abs(sl*a+off-b) > 0.5*(sy/n) {
							fmt.Printf("     outlier at %d,%d: go %.0f extract %.0f (fit %.0f) go RGB %v\n", x, y, a, b, sl*a+off, g.RGBA64At(x, y))
							shown++
						}
					}
				}
				fmt.Printf("   ch%d: extract = %.4f*go %+.1f, residual %.2f%% of the extract's mean\n", k, sl, off, 100*math.Sqrt(math.Max(0, res)/n)/(sy/n))
			}
		}
		if os.Getenv("SHIFT") != "" {
			// Best integer shift of extract against go, top layer, central crop.
			k := sg[2] / se[2]
			W, H := g.Bounds().Dx(), g.Bounds().Dy()
			best, bx, by := math.Inf(1), 0, 0
			for dy := -6; dy <= 6; dy++ {
				for dx := -6; dx <= 6; dx++ {
					d := 0.0
					for y := H / 3; y < 2*H/3; y += 3 {
						for x := W / 3; x < 2*W/3; x += 3 {
							a := float64(g.RGBA64At(x, y).B) / k
							b := float64(e.RGBA64At(x+dx, y+dy).B)
							d += math.Abs(a - b)
						}
					}
					if d < best {
						best, bx, by = d, dx, dy
					}
				}
			}
			fmt.Printf("   best shift extract vs go: dx %d dy %d\n", bx, by)
		}
		if os.Getenv("GRID") != "" {
			for _, fy := range []float64{0.1, 0.3, 0.5, 0.7, 0.9} {
				fmt.Print("   ")
				for _, fx := range []float64{0.1, 0.3, 0.5, 0.7, 0.9} {
					// Mean ratio in the region, relative to the whole image's.
					var a, b [3]float64
					for y := fy - 0.05; y < fy+0.05; y += 0.002 {
						for x := fx - 0.05; x < fx+0.05; x += 0.002 {
							p := g.RGBA64At(int(x*float64(g.Bounds().Dx())), int(y*float64(g.Bounds().Dy())))
							q := e.RGBA64At(int(x*float64(e.Bounds().Dx())), int(y*float64(e.Bounds().Dy())))
							if max(p.R, p.G, p.B) > 50000 {
								continue // clipped highlights: compare the rest
							}
							a[0], a[1], a[2] = a[0]+float64(p.R), a[1]+float64(p.G), a[2]+float64(p.B)
							b[0], b[1], b[2] = b[0]+float64(q.R), b[1]+float64(q.G), b[2]+float64(q.B)
						}
					}
					fmt.Printf(" %.3f/%.3f/%.3f", a[0]/b[0]/(sg[0]/se[0]), a[1]/b[1]/(sg[1]/se[1]), a[2]/b[2]/(sg[2]/se[2]))
				}
				fmt.Println()
			}
		}
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

// linearSRGB develops raw to linear sRGB (clipped to 0..1) with the photo's
// CAMF white balance and colour matrix.
func linearSRGB(raw *x3f.RawImage, c *x3f.CAMF, sg *x3f.SpatialGain, wb string) (*image.RGBA64, error) {
	l, err := x3f.CameraLinear(raw, c, sg, wb)
	if err != nil {
		return nil, err
	}
	gain, cc, err := c.WBTransform(wb)
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA64(image.Rect(0, 0, l.W, l.H))
	for i := 0; i < l.W*l.H; i++ {
		var v [3]float64
		for k := 0; k < 3; k++ {
			v[k] = float64(l.Pix[i*3+k]) * gain[k]
		}
		d := out.Pix[(i/l.W)*out.Stride+(i%l.W)*8:]
		for r := 0; r < 3; r++ {
			o := cc[r*3]*v[0] + cc[r*3+1]*v[1] + cc[r*3+2]*v[2]
			u := uint16(math.Round(math.Max(0, math.Min(1, o)) * 65535))
			d[r*2], d[r*2+1] = byte(u>>8), byte(u)
		}
		d[6], d[7] = 0xff, 0xff
	}
	return out, nil
}
