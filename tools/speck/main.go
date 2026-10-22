// Command speck counts single-pixel outliers in an X3F at the stage where
// they are born: after the colour matrix, before any tone. It reports how
// many there are, how many the bad-pixel list repaired, and how many
// render.Despeckle then removes.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

func median9(n [9]float64) float64 {
	var s [9]float64
	for i := 0; i < 9; i++ {
		v := n[i]
		j := i
		for j > 0 && s[j-1] > v {
			s[j] = s[j-1]
			j--
		}
		s[j] = v
	}
	return s[4]
}

// count returns how many channel samples sit more than fraction away from
// their 3x3 median (and more than floor above it), and how many per channel.
func count(l *render.Linear, fraction, floor float64) (int, [3]int) {
	var n [9]float64
	total := 0
	var per [3]int
	for y := 1; y < l.H-1; y++ {
		for x := 1; x < l.W-1; x++ {
			i := (y*l.W + x) * 3
			for k := 0; k < 3; k++ {
				v := float64(l.Pix[i+k])
				m := 0
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						n[m] = float64(l.Pix[((y+dy)*l.W+x+dx)*3+k])
						m++
					}
				}
				med := median9(n)
				if med < 1e-9 {
					continue
				}
				d := v - med
				if d < 0 {
					d = -d
				}
				if d > fraction*med && d > floor {
					total++
					per[k]++
				}
			}
		}
	}
	return total, per
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: speck photo.X3F [fraction] [floor]\n")
		os.Exit(2)
	}
	fraction, floor := 0.6, 0.0015
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%g", &fraction)
	}
	if len(os.Args) > 3 {
		fmt.Sscanf(os.Args[3], "%g", &floor)
	}
	f, err := x3f.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	c, err := f.CAMF()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	raw, err := f.DecodeRaw()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fixed := x3f.FixBadPixels(raw, c)
	wb := strings.TrimSpace(f.Props["WB_DESC"])
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lin := &render.Linear{W: img.W, H: img.H, Pix: img.Pix}
	before, per := count(lin, fraction, floor)
	changed := render.Despeckle(lin, fraction, floor)
	after, _ := count(lin, fraction, floor)
	fmt.Printf("%s: %dx%d, bad pixels repaired %d, outlier samples %d (R %d G %d B %d) at fraction %.2f floor %g\n",
		os.Args[1], img.W, img.H, fixed, before, per[0], per[1], per[2], fraction, floor)
	fmt.Printf("   despeckle changed %d samples, outliers left %d\n", changed, after)
}
