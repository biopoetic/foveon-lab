// chartread reads an SPP export of a tools/synth chart: per patch, the mean
// linear output (central half of the patch) against the known raw input,
// then a least-squares 3x3 matrix raw->output over the unclipped patches
// and how much of SPP's response that matrix leaves unexplained.
//
//	go run ./tools/chartread <chart.X3F.json> <export.tif>
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strings"

	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
	_ "golang.org/x/image/tiff"
)

type patch struct {
	X0, Y0, X1, Y1 int
	Raw            [3]float64
}

func main() {
	var chart struct {
		Source  string
		Active  []int
		Patches []patch
		White   float64
	}
	js, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	if err := json.Unmarshal(js, &chart); err != nil {
		log.Fatal(err)
	}
	d, _ := os.ReadFile(os.Args[2])
	img, _, err := render.DecodeImage(d)
	if err != nil {
		log.Fatal(err)
	}
	l := render.FromImage(img, 0, 0) // full size, linear
	// What SPP really fed each patch: the raw times the source file's
	// spatial gain and (Merrill) radial WB shading at the patch centre.
	if os.Getenv("NOSG") == "" {
		f, err := x3f.Open(chart.Source)
		if err != nil {
			log.Fatal(err)
		}
		c, err := f.CAMF()
		if err != nil {
			log.Fatal(err)
		}
		wb := map[string]string{"Daylight": "Sunlight", "Shade": "Shade", "Overcast": "Overcast", "Incandescent": "Incandescent",
			"Fluorescent": "Fluorescent", "Flash": "Flash", "Auto": "Auto", "Custom": "Custom", "Sunlight": "Sunlight"}[strings.TrimSpace(f.Props["WB_DESC"])]
		sg := x3f.NewSpatialGain(c, f.Props, wb)
		shade := c.Matrices[c.Props["WhiteBalanceColorShadingFactor"][wb]]
		a := chart.Active
		aw, ah := a[2]-a[0]+1, a[3]-a[1]+1
		hw, hh := float64(a[2]-a[0])/2, float64(a[3]-a[1])/2
		for i := range chart.Patches {
			p := &chart.Patches[i]
			cx, cy := (p.X0+p.X1)/2, (p.Y0+p.Y1)/2
			for k := 0; k < 3; k++ {
				g := 1.0
				if sg != nil {
					g = sg.At(cy, cx, k, ah, aw)
				}
				if len(shade) == 4 && k != 1 {
					dx, dy := float64(cx)-hw, float64(cy)-hh
					r2 := (dx*dx + dy*dy) / (hw*hw + hh*hh)
					if k == 0 {
						g *= 1 + shade[0]*r2*r2 + shade[1]*r2
					} else {
						g *= 1 + shade[2]*r2*r2 + shade[3]*r2
					}
				}
				p.Raw[k] *= g
			}
		}
		fmt.Printf("inputs corrected for spatial gain + WB shading (%s)\n", wb)
	}
	type obs struct{ in, out [3]float64 }
	var all []obs
	for i, p := range chart.Patches {
		qx, qy := (p.X1-p.X0)/4, (p.Y1-p.Y0)/4
		var s [3]float64
		n := 0
		for y := p.Y0 + qy; y <= p.Y1-qy; y++ {
			for x := p.X0 + qx; x <= p.X1-qx; x++ {
				o := (y*l.W + x) * 3
				for k := 0; k < 3; k++ {
					s[k] += float64(l.Pix[o+k])
				}
				n++
			}
		}
		var out [3]float64
		for k := range out {
			out[k] = s[k] / float64(n)
		}
		all = append(all, obs{p.Raw, out})
		if i < 16 {
			fmt.Printf("ramp %2d raw %7.1f %7.1f %7.1f -> SPP lin %.4f %.4f %.4f\n", i, p.Raw[0], p.Raw[1], p.Raw[2], out[0], out[1], out[2])
		}
	}
	// Tone curve T from the neutral ramp (row 0): input = the ramp's top
	// layer, output = its mean channel. Invert it on every patch, so a
	// matrix fit can test "SPP = matrix, then one tone curve".
	cols := 0
	for _, p := range chart.Patches {
		if p.Y0 == 0 {
			cols++
		}
	}
	type pt struct{ x, y float64 }
	var curve []pt
	for i := 0; i < cols; i++ {
		o := all[i].out
		curve = append(curve, pt{all[i].in[2], (o[0] + o[1] + o[2]) / 3})
	}
	inverse := func(y float64) float64 { // log-log interpolation of T^-1
		if y <= curve[0].y {
			return curve[0].x * y / curve[0].y
		}
		for i := 1; i < len(curve); i++ {
			if y <= curve[i].y {
				a, b := curve[i-1], curve[i]
				t := (math.Log(y) - math.Log(a.y)) / (math.Log(b.y) - math.Log(a.y))
				return math.Exp(math.Log(a.x) + t*(math.Log(b.x)-math.Log(a.x)))
			}
		}
		return math.NaN()
	}
	if os.Getenv("LINEARIZE") != "" {
		for i := range all {
			for k := 0; k < 3; k++ {
				all[i].out[k] = inverse(all[i].out[k])
			}
		}
		fmt.Println("(outputs linearized through the ramp's tone curve)")
	}
	// Fit out = M * raw on patches where no SPP channel is near clipping.
	var X, Y []float64
	var used []obs
	for _, o := range all {
		bad := math.IsNaN(o.out[0] + o.out[1] + o.out[2])
		if os.Getenv("LINEARIZE") == "" {
			bad = bad || math.Max(o.out[0], math.Max(o.out[1], o.out[2])) > 0.9 || math.Min(o.out[0], math.Min(o.out[1], o.out[2])) < 0.002
		} else {
			bad = bad || math.Min(o.out[0], math.Min(o.out[1], o.out[2])) <= 0
		}
		if bad {
			continue
		}
		X = append(X, o.in[:]...)
		Y = append(Y, o.out[:]...)
		used = append(used, o)
	}
	n := len(used)
	// Normal equations per output channel: (X^T X) m = X^T y.
	var xtx [3][3]float64
	for _, o := range used {
		for a := 0; a < 3; a++ {
			for b := 0; b < 3; b++ {
				xtx[a][b] += o.in[a] * o.in[b]
			}
		}
	}
	inv := inv3(xtx)
	var m [3][3]float64 // m[out][in]
	for k := 0; k < 3; k++ {
		var xty [3]float64
		for _, o := range used {
			for a := 0; a < 3; a++ {
				xty[a] += o.in[a] * o.out[k]
			}
		}
		for a := 0; a < 3; a++ {
			for b := 0; b < 3; b++ {
				m[k][a] += inv[a][b] * xty[b]
			}
		}
	}
	fmt.Printf("\n%d unclipped patches; least-squares raw->SPP linear matrix (rows = R,G,B out):\n", n)
	for k := 0; k < 3; k++ {
		fmt.Printf("  %+.3e %+.3e %+.3e\n", m[k][0], m[k][1], m[k][2])
	}
	var rel, scale float64
	for _, o := range used {
		scale = math.Max(scale, o.out[0]+o.out[1]+o.out[2])
	}
	scale /= 3
	for _, o := range used {
		for k := 0; k < 3; k++ {
			pv := m[k][0]*o.in[0] + m[k][1]*o.in[1] + m[k][2]*o.in[2]
			rel += math.Abs(pv-o.out[k]) / (math.Abs(o.out[k]) + 0.01*scale)
		}
	}
	// Where the residual sits: by brightness and by how far the raw is
	// from neutral (chroma).
	type bin struct{ n, e float64 }
	var byL, byC [4]bin
	for _, o := range used {
		var e float64
		for k := 0; k < 3; k++ {
			pv := m[k][0]*o.in[0] + m[k][1]*o.in[1] + m[k][2]*o.in[2]
			e += math.Abs(pv-o.out[k]) / (math.Abs(o.out[k]) + 0.01*scale)
		}
		e /= 3
		lum := (o.out[0] + o.out[1] + o.out[2]) / 3 / scale
		li := min(3, int(math.Max(0, math.Log2(lum)+8)/2))
		// chroma: spread of log(raw/neutral) over the layers
		nu := [3]float64{0.318, 0.658, 1}
		var lg [3]float64
		for k := range lg {
			lg[k] = math.Log2(o.in[k] / nu[k])
		}
		mean := (lg[0] + lg[1] + lg[2]) / 3
		sd := math.Sqrt(((lg[0]-mean)*(lg[0]-mean) + (lg[1]-mean)*(lg[1]-mean) + (lg[2]-mean)*(lg[2]-mean)) / 3)
		ci := min(3, int(sd/0.2))
		byL[li].n++
		byL[li].e += e
		byC[ci].n++
		byC[ci].e += e
	}
	fmt.Print("residual by brightness (dark->bright):")
	for _, b := range byL {
		if b.n > 0 {
			fmt.Printf("  %.1f%% (%d)", b.e/b.n*100, int(b.n))
		}
	}
	fmt.Print("\nresidual by chroma (neutral->saturated, 0.2-stop bins):")
	for _, b := range byC {
		if b.n > 0 {
			fmt.Printf("  %.1f%% (%d)", b.e/b.n*100, int(b.n))
		}
	}
	fmt.Println()
	mi := inv3(m)
	var nu [3]float64
	for a := 0; a < 3; a++ {
		nu[a] = mi[a][0] + mi[a][1] + mi[a][2]
	}
	mx := math.Max(nu[0], math.Max(nu[1], nu[2]))
	fmt.Printf("raw neutral for SPP (matrix estimate, top = 1): %.3f,%.3f,%.3f\n", nu[0]/mx, nu[1]/mx, nu[2]/mx)
	fmt.Printf("mean relative error of a pure matrix: %.1f%% (what a tone curve / non-linear step must explain)\n", rel/float64(3*n)*100)
}

func inv3(a [3][3]float64) [3][3]float64 {
	det := a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) - a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) + a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])
	var r [3][3]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			i1, i2, j1, j2 := (j+1)%3, (j+2)%3, (i+1)%3, (i+2)%3
			r[i][j] = (a[i1][j1]*a[i2][j2] - a[i1][j2]*a[i2][j1]) / det
		}
	}
	return r
}
