// sharpfit measures how much Sigma Photo Pro sharpens its neutral export
// relative to Foveon Lab's raw path, and fits an unsharp mask that matches
// it. SPP's export is not pixel-aligned with the raw decode (lens
// corrections), so it compares detail energy per frequency band (variance
// of differences of box blurs, on sRGB-encoded luminance) over the same
// part of the scene, found by a coarse alignment search.
//
//	go run ./tools/sharpfit photo.X3F spp_export.tif [more pairs…]
package main

import (
	"fmt"
	"image"
	"image/png"
	"path/filepath"

	_ "golang.org/x/image/tiff"
	"log"
	"math"
	"os"
	"strings"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

var bandRadii = []int{0, 1, 2, 4, 8} // bands: r0-r1, r1-r2, …

func main() {
	a := os.Args[1:]
	if len(a) > 0 && a[0] == "-gain" {
		gains(a[1:])
		return
	}
	if len(a) > 0 && strings.HasPrefix(a[0], "-sigma=") {
		fmt.Sscanf(a[0], "-sigma=%g", &fixSigma)
		a = a[1:]
	}
	if len(a) > 0 && strings.HasPrefix(a[0], "-save=") {
		saveDir = strings.TrimPrefix(a[0], "-save=")
		a = a[1:]
	}
	if len(a) > 0 && a[0] == "-nofit" {
		noFit = true
		a = a[1:]
	}
	if len(a) > 0 && a[0] == "-render" {
		// Check the app's own render (SPP sharpening included): the fit
		// should then find nothing left to add.
		viaRender = true
		a = a[1:]
	}
	if len(a) > 0 && strings.HasPrefix(a[0], "-nr=") {
		fmt.Sscanf(a[0], "-nr=%g", &nrStrength)
		a = a[1:]
	}
	if len(a) < 2 || len(a)%2 != 0 {
		log.Fatal("usage: sharpfit photo.X3F spp.tif …")
	}
	type pair struct {
		ours, spp []float32
		w, h      int
	}
	var pairs []pair
	for i := 0; i+1 < len(a); i += 2 {
		ours, w, h, err := develop(a[i])
		if err != nil {
			log.Fatal(err)
		}
		spp, sw, sh, err := loadLuma(a[i+1])
		if err != nil {
			log.Fatal(err)
		}
		if sw != w || sh != h {
			fmt.Printf("%s: sizes differ (%dx%d vs %dx%d), skipped\n", a[i], w, h, sw, sh)
			continue
		}
		if saveDir != "" {
			saveCrop(filepath.Base(a[i]), ours, spp, w, h)
		}
		// Four 400×400 crops around the centre (SPP's export is aligned with
		// the raw decode on SD1 HI files: offset 0,0 measured).
		for _, c := range [][2]int{{w/2 - 450, h/2 - 450}, {w/2 + 50, h/2 - 450}, {w/2 - 450, h/2 + 50}, {w/2 + 50, h/2 + 50}} {
			pairs = append(pairs, pair{crop(ours, w, c[0], c[1], 400, 400), crop(spp, w, c[0], c[1], 400, 400), 400, 400})
		}
		o, s := pairs[len(pairs)-1].ours, pairs[len(pairs)-1].spp
		cw, ch := 400, 400
		dx, dy := 0, 0
		fmt.Printf("%s: SPP offset %+d,%+d; band energy SPP/ours:", a[i][strings.LastIndexAny(a[i], `/\`)+1:], dx, dy)
		eo, es := bands(o, cw, ch), bands(s, cw, ch)
		for b := range eo {
			fmt.Printf(" %.2f", math.Sqrt(es[b]/eo[b]))
		}
		fmt.Println()
	}
	if noFit {
		return
	}
	// Grid search: unsharp mask (Gaussian sigma, amount) on ours whose band
	// amplitudes best match SPP's (log ratio, summed over pairs and bands).
	best, bs, ba := math.Inf(1), 0.0, 0.0
	sigmas := []float64{0.5, 0.7, 0.85, 1.0, 1.2, 1.4, 1.7, 2.0}
	if fixSigma > 0 {
		sigmas = []float64{fixSigma}
	}
	for _, sig := range sigmas {
		for amt := 0.0; amt <= 4.0001; amt += 0.1 {
			var cost float64
			for _, p := range pairs {
				sh := usm(p.ours, p.w, p.h, sig, amt)
				eo, es := bands(sh, p.w, p.h), bands(p.spp, p.w, p.h)
				for b := range eo {
					d := math.Log(es[b] / eo[b])
					cost += d * d
				}
			}
			if cost < best {
				best, bs, ba = cost, sig, amt
			}
		}
	}
	fmt.Printf("best unsharp mask: sigma %.1f px, amount %.1f (cost %.4f)\n", bs, ba, best)
	for _, p := range pairs {
		sh := usm(p.ours, p.w, p.h, bs, ba)
		eo, es := bands(sh, p.w, p.h), bands(p.spp, p.w, p.h)
		fmt.Print("  after:")
		for b := range eo {
			fmt.Printf(" %.2f", math.Sqrt(es[b]/eo[b]))
		}
		fmt.Println()
	}
}

// develop: the app's raw path at full size (as tools/isocmp), as
// sRGB-encoded luminance.
var nrStrength = 1.0

// viaRender: measure the app's rendered output instead of the linear image.
var viaRender bool

// noFit: only report the band ratios.
var noFit bool

// fixSigma: fit only the amount, at this sigma.
var fixSigma float64

// saveDir: write each photo's centre crop, ours | SPP, as a PNG there.
var saveDir string

func saveCrop(name string, ours, spp []float32, w, h int) {
	const cs = 500
	x0, y0 := w/2-cs/2, h/2-cs/2
	img := image.NewGray(image.Rect(0, 0, 2*cs+8, cs))
	for y := 0; y < cs; y++ {
		for x := 0; x < cs; x++ {
			i := (y0+y)*w + x0 + x
			img.Pix[y*img.Stride+x] = uint8(math.Max(0, math.Min(1, float64(ours[i])))*255 + 0.5)
			img.Pix[y*img.Stride+cs+8+x] = uint8(math.Max(0, math.Min(1, float64(spp[i])))*255 + 0.5)
		}
	}
	f, err := os.Create(filepath.Join(saveDir, name+".png"))
	if err != nil {
		log.Fatal(err)
	}
	png.Encode(f, img)
	f.Close()
}

func develop(path string) ([]float32, int, int, error) {
	f, err := x3f.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	c, err := f.CAMF()
	if err != nil {
		return nil, 0, 0, err
	}
	raw, err := f.DecodeRaw()
	if err != nil {
		return nil, 0, 0, err
	}
	x3f.FixBadPixels(raw, c)
	wb := c.CameraWB(strings.TrimSpace(f.Props["WB_DESC"]))
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		return nil, 0, 0, err
	}
	gain, cc, err := c.WBTransform(wb)
	if err != nil {
		return nil, 0, 0, err
	}
	x3f.ClipHighlights(img, gain)
	if nrStrength > 0 {
		x3f.Denoise(img, x3f.DenoiseOptions{Strength: nrStrength, Scale: 1})
	}
	l := render.FromCamLinear(img.Pix, img.W, img.H, 0, 0, render.CamTransform{Gain: gain, CC: cc})
	fmt.Sscan(f.Props["ISO"], &l.ISO)
	if b := render.RawBaseFitted(x3f.CalibGroup(f.Props["CAMMODEL"])); b != nil {
		b.Apply(l)
	}
	if viaRender {
		img := render.Render(render.NewBase(l), preset.Params{R: 1, G: 1, B: 1, ColorMode: preset.ModeStandard}, render.Compensation{})
		l = render.FromImage(img, 0, 0)
	}
	return luma(l), l.W, l.H, nil
}

func luma(l *render.Linear) []float32 {
	y := make([]float32, l.W*l.H)
	for i := range y {
		v := 0.2126*l.Pix[i*3] + 0.7152*l.Pix[i*3+1] + 0.0722*l.Pix[i*3+2]
		y[i] = render.ToSRGB(float32(math.Max(0, math.Min(1, float64(v)))))
	}
	return y
}

func loadLuma(p string) ([]float32, int, int, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, 0, 0, err
	}
	img, _, err := render.DecodeImage(data)
	if err != nil {
		return nil, 0, 0, err
	}
	l := render.FromImage(img, 0, 0)
	return luma(l), l.W, l.H, nil
}

func crop(p []float32, w, x0, y0, cw, ch int) []float32 {
	o := make([]float32, cw*ch)
	for y := 0; y < ch; y++ {
		copy(o[y*cw:(y+1)*cw], p[(y+y0)*w+x0:])
	}
	return o
}

// align finds SPP's offset (±96 px) for ours' crop by normalised
// correlation of 4×-binned luminance, then refines to 1 px.
func align(ours, spp []float32, w, h, x0, y0, cw, ch int) (int, int) {
	bin := func(p []float32) ([]float32, int, int) {
		bw, bh := w/4, h/4
		o := make([]float32, bw*bh)
		for y := 0; y < bh*4; y++ {
			for x := 0; x < bw*4; x++ {
				o[(y/4)*bw+x/4] += p[y*w+x] / 16
			}
		}
		return o, bw, bh
	}
	corr := func(a, b []float32, aw, bw, ax, ay, bx, by, cw, ch, step int) float64 {
		var sa, sb, saa, sbb, sab, n float64
		for y := 0; y < ch; y += step {
			for x := 0; x < cw; x += step {
				u, v := float64(a[(ay+y)*aw+ax+x]), float64(b[(by+y)*bw+bx+x])
				sa, sb, saa, sbb, sab, n = sa+u, sb+v, saa+u*u, sbb+v*v, sab+u*v, n+1
			}
		}
		return (sab - sa*sb/n) / math.Sqrt(math.Max(1e-12, (saa-sa*sa/n)*(sbb-sb*sb/n)))
	}
	bo, bw, _ := bin(ours)
	bsp, _, _ := bin(spp)
	best, bx, by := -2.0, 0, 0
	for dy := -24; dy <= 24; dy++ {
		for dx := -24; dx <= 24; dx++ {
			if c := corr(bo, bsp, bw, bw, x0/4, y0/4, x0/4+dx, y0/4+dy, cw/4, ch/4, 2); c > best {
				best, bx, by = c, dx*4, dy*4
			}
		}
	}
	fx, fy := bx, by
	best = -2
	for dy := by - 4; dy <= by+4; dy++ {
		for dx := bx - 4; dx <= bx+4; dx++ {
			if c := corr(ours, spp, w, w, x0, y0, x0+dx, y0+dy, cw, ch, 4); c > best {
				best, fx, fy = c, dx, dy
			}
		}
	}
	return fx, fy
}

func boxBlur(p []float32, w, h, r int) []float32 {
	if r == 0 {
		return p
	}
	tmp := make([]float32, len(p))
	out := make([]float32, len(p))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float32
			n := 0
			for k := max(0, x-r); k <= min(w-1, x+r); k++ {
				s += p[y*w+k]
				n++
			}
			tmp[y*w+x] = s / float32(n)
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float32
			n := 0
			for k := max(0, y-r); k <= min(h-1, y+r); k++ {
				s += tmp[k*w+x]
				n++
			}
			out[y*w+x] = s / float32(n)
		}
	}
	return out
}

// bands: variance of each difference of box blurs.
func bands(p []float32, w, h int) []float64 {
	bl := make([][]float32, len(bandRadii))
	for i, r := range bandRadii {
		bl[i] = boxBlur(p, w, h, r)
	}
	out := make([]float64, len(bandRadii)-1)
	for b := range out {
		var s, ss float64
		n := float64(w * h)
		for i := range p {
			d := float64(bl[b][i] - bl[b+1][i])
			s += d
			ss += d * d
		}
		out[b] = ss/n - (s/n)*(s/n)
	}
	return out
}

// usm is an unsharp mask with a Gaussian of sigma (separable, 3 sigma).
func usm(p []float32, w, h int, sigma, amount float64) []float32 {
	if amount == 0 {
		return p
	}
	r := int(math.Ceil(3 * sigma))
	k := make([]float32, 2*r+1)
	var sum float32
	for i := range k {
		x := float64(i - r)
		k[i] = float32(math.Exp(-x * x / (2 * sigma * sigma)))
		sum += k[i]
	}
	for i := range k {
		k[i] /= sum
	}
	tmp := make([]float32, len(p))
	bl := make([]float32, len(p))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float32
			for i, kv := range k {
				s += kv * p[y*w+min(w-1, max(0, x+i-r))]
			}
			tmp[y*w+x] = s
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float32
			for i, kv := range k {
				s += kv * tmp[min(h-1, max(0, y+i-r))*w+x]
			}
			bl[y*w+x] = s
		}
	}
	out := make([]float32, len(p))
	for i := range p {
		out[i] = p[i] + float32(amount)*(p[i]-bl[i])
	}
	return out
}

// bandsOf returns each band image (difference of box blurs).
func bandsOf(p []float32, w, h int) [][]float32 {
	bl := make([][]float32, len(bandRadii))
	for i, r := range bandRadii {
		bl[i] = boxBlur(p, w, h, r)
	}
	out := make([][]float32, len(bandRadii)-1)
	for b := range out {
		out[b] = make([]float32, len(p))
		for i := range p {
			out[b][i] = bl[b][i] - bl[b+1][i]
		}
	}
	return out
}

// gains prints, per band, the regression slope of SPP's export and of
// Foveon Lab's NR output on Foveon Lab's output without NR (all aligned):
// each pipeline's transfer function on the same raw data, noise included.
func gains(a []string) {
	for i := 0; i+1 < len(a); i += 2 {
		nrStrength = 0
		x, w, h, err := develop(a[i])
		if err != nil {
			log.Fatal(err)
		}
		nrStrength = 1
		y1, _, _, err := develop(a[i])
		if err != nil {
			log.Fatal(err)
		}
		sp, _, _, err := loadLuma(a[i+1])
		if err != nil {
			log.Fatal(err)
		}
		slope := func(y []float32) []float64 {
			var out []float64
			// Four 400×400 crops around the centre.
			var cx, cy, cyy [4]float64
			_ = cyy
			sxy := make([]float64, len(bandRadii)-1)
			sxx := make([]float64, len(bandRadii)-1)
			for _, c := range [][2]int{{w/2 - 450, h/2 - 450}, {w/2 + 50, h/2 - 450}, {w/2 - 450, h/2 + 50}, {w/2 + 50, h/2 + 50}} {
				bx := bandsOf(crop(x, w, c[0], c[1], 400, 400), 400, 400)
				by := bandsOf(crop(y, w, c[0], c[1], 400, 400), 400, 400)
				for b := range bx {
					for j := range bx[b] {
						sxy[b] += float64(bx[b][j]) * float64(by[b][j])
						sxx[b] += float64(bx[b][j]) * float64(bx[b][j])
					}
				}
			}
			_, _ = cx, cy
			for b := range sxy {
				out = append(out, sxy[b]/sxx[b])
			}
			return out
		}
		name := a[i][strings.LastIndexAny(a[i], `/\`)+1:]
		fmt.Printf("%-14s gain per band (finest first) vs no NR:  SPP %.2f  | Foveon Lab NR %.2f\n", name, slope(sp), slope(y1))
	}
}
