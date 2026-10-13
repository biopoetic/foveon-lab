// noisebench measures denoising on an ISO series of one static scene: the
// base-ISO frame is the reference, a high-ISO frame is developed the same
// way (native Go pipeline), denoised by a candidate method, exposure-matched
// and compared with CIEDE2000 on a full-resolution centre crop.
//
//	go run ./tools/noisebench -ref ISO100.X3F -test ISO6400.X3F [-method none,chroma] [-save dir]
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
	"sort"
	"strings"
	"time"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

var camfWB = map[string]string{"Daylight": "Sunlight", "Sunlight": "Sunlight", "Shade": "Shade", "Overcast": "Overcast",
	"Incandescent": "Incandescent", "Fluorescent": "Fluorescent", "Flash": "Flash", "Auto": "Auto", "Custom": "Custom"}

// cam is a crop of linear camera RGB (black 0, white 1 per layer) with the
// photo's white balance + colour transform.
type cam struct {
	w, h int
	pix  []float32
	t    render.CamTransform
}

func develop(path string, cx, cy, cw, ch float64) cam {
	f, err := x3f.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	c, err := f.CAMF()
	if err != nil {
		log.Fatal(err)
	}
	raw, err := f.DecodeRaw()
	if err != nil {
		log.Fatal(err)
	}
	wb := camfWB[strings.TrimSpace(f.Props["WB_DESC"])]
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		log.Fatal(err)
	}
	var t render.CamTransform
	g, cc := c.Matrices[wb+"WBGain"], c.Matrices[wb+"CCMatrix"]
	if len(g) != 3 || len(cc) != 9 {
		log.Fatalf("%s: no %s WB in CAMF", path, wb)
	}
	copy(t.Gain[:], g)
	copy(t.CC[:], cc)
	for _, k := range []string{"SensorAdjustmentGainFact", "TempGainFact", "FNumberGainFact"} {
		if v := c.Matrices[k]; len(v) == 3 {
			for i := range t.Gain {
				t.Gain[i] *= v[i]
			}
		}
	}
	if s, ci := c.Matrices["SensorISO"], c.Matrices["CaptureISO"]; len(s) == 1 && len(ci) == 1 && s[0] > 0 {
		for i := range t.Gain {
			t.Gain[i] *= ci[0] / s[0]
		}
	}
	if os.Getenv("CLIP") != "" { // the old behaviour, for comparison
		for i, v := range img.Pix {
			img.Pix[i] = max(v, 0)
		}
	}
	w, h := int(cw*float64(img.W)), int(ch*float64(img.H))
	x0, y0 := int(cx*float64(img.W))-w/2, int(cy*float64(img.H))-h/2
	out := cam{w: w, h: h, pix: make([]float32, w*h*3), t: t}
	for y := 0; y < h; y++ {
		copy(out.pix[y*w*3:(y+1)*w*3], img.Pix[((y0+y)*img.W+x0)*3:((y0+y)*img.W+x0+w)*3])
	}
	return out
}

// toLinear applies the camera transform, giving linear sRGB.
func (c cam) toLinear(t render.CamTransform) *render.Linear {
	m := t.Matrix()
	l := &render.Linear{W: c.w, H: c.h, Pix: make([]float32, len(c.pix))}
	for i := 0; i < len(c.pix); i += 3 {
		r, g, b := float64(c.pix[i]), float64(c.pix[i+1]), float64(c.pix[i+2])
		for k := 0; k < 3; k++ {
			l.Pix[i+k] = float32(m[k*3]*r + m[k*3+1]*g + m[k*3+2]*b)
		}
	}
	return l
}

// boxBlur is a separable box blur of one float plane (radius r).
func boxBlur(p []float32, w, h, r int) []float32 {
	tmp := make([]float32, len(p))
	out := make([]float32, len(p))
	for y := 0; y < h; y++ {
		var s float32
		n := 0
		row := p[y*w : (y+1)*w]
		for x := -r; x < w; x++ {
			if x+r < w {
				s += row[x+r]
				n++
			}
			if x-r-1 >= 0 {
				s -= row[x-r-1]
				n--
			}
			if x >= 0 {
				tmp[y*w+x] = s / float32(n)
			}
		}
	}
	for x := 0; x < w; x++ {
		var s float32
		n := 0
		for y := -r; y < h; y++ {
			if y+r < h {
				s += tmp[(y+r)*w+x]
				n++
			}
			if y-r-1 >= 0 {
				s -= tmp[(y-r-1)*w+x]
				n--
			}
			if y >= 0 {
				out[y*w+x] = s / float32(n)
			}
		}
	}
	return out
}

// guided filters p guided by I (He et al.), radius r, regulariser eps.
func guided(I, p []float32, w, h, r int, eps float32) []float32 {
	mI, mp := boxBlur(I, w, h, r), boxBlur(p, w, h, r)
	Ip, II := make([]float32, len(I)), make([]float32, len(I))
	for i := range I {
		Ip[i], II[i] = I[i]*p[i], I[i]*I[i]
	}
	mIp, mII := boxBlur(Ip, w, h, r), boxBlur(II, w, h, r)
	a, b := make([]float32, len(I)), make([]float32, len(I))
	for i := range I {
		cov := mIp[i] - mI[i]*mp[i]
		vr := mII[i] - mI[i]*mI[i]
		a[i] = cov / (vr + eps)
		b[i] = mp[i] - a[i]*mI[i]
	}
	ma, mb := boxBlur(a, w, h, r), boxBlur(b, w, h, r)
	out := make([]float32, len(I))
	for i := range I {
		out[i] = ma[i]*I[i] + mb[i]
	}
	return out
}

// chroma keeps each pixel's brightness (sum of layers) and replaces its
// layer proportions by guided-filtered ones (guide: the brightness), so
// colour noise is averaged away without blurring edges.
func chroma(c cam, r int, eps float32) cam {
	n := c.w * c.h
	Y := make([]float32, n)
	for i := 0; i < n; i++ {
		Y[i] = c.pix[i*3] + c.pix[i*3+1] + c.pix[i*3+2]
	}
	out := cam{w: c.w, h: c.h, pix: make([]float32, len(c.pix)), t: c.t}
	// Normalise the guide so eps is relative to the image's brightness.
	var mean float32
	for _, v := range Y {
		mean += v
	}
	mean /= float32(n)
	G := make([]float32, n)
	for i := range Y {
		G[i] = Y[i] / mean
	}
	for k := 0; k < 3; k++ {
		p := make([]float32, n)
		for i := 0; i < n; i++ {
			p[i] = c.pix[i*3+k] / mean // layer value; filter it, then rescale
		}
		f := guided(G, p, c.w, c.h, r, eps)
		for i := 0; i < n; i++ {
			out.pix[i*3+k] = f[i] * mean
		}
	}
	// Restore each pixel's own brightness (luminance noise untouched here).
	for i := 0; i < n; i++ {
		s := out.pix[i*3] + out.pix[i*3+1] + out.pix[i*3+2]
		if s > 1e-6 {
			k := Y[i] / s
			out.pix[i*3] *= k
			out.pix[i*3+1] *= k
			out.pix[i*3+2] *= k
		}
	}
	return out
}

// match maps test to the reference per layer: a scale, plus (affine=true)
// an offset — which shows a black-level error in the test frame.
var affine = os.Getenv("AFFINE") != ""

// blurred returns layer k of c box-blurred (radius 12): noise-free enough
// to fit the exposure match without the noise biasing the slope.
func blurred(c cam, k int) []float32 {
	p := make([]float32, c.w*c.h)
	for i := range p {
		p[i] = c.pix[i*3+k]
	}
	return boxBlur(p, c.w, c.h, 12)
}

// matchFrom fits the per-layer map on from (normally the undenoised test
// frame, blurred) and applies it to test.
func match(ref, test cam) cam { return matchFrom(ref, test, test) }

func matchFrom(ref, from, test cam) cam {
	out := cam{w: test.w, h: test.h, pix: make([]float32, len(test.pix)), t: test.t}
	for k := 0; k < 3; k++ {
		var sa, sb, sab, sbb, n float64
		rb, tb := blurred(ref, k), blurred(from, k)
		for i := range rb {
			a, b := float64(rb[i]), float64(tb[i])
			sa, sb, sab, sbb, n = sa+a, sb+b, sab+a*b, sbb+b*b, n+1
		}
		s, o := sab/sbb, 0.0
		if affine {
			s = (sab - sa*sb/n) / (sbb - sb*sb/n)
			o = (sa - s*sb) / n
			fmt.Printf("    layer %d: scale %.2f, offset %.5f (= %.1f test raw units)\n", k, s, o, -o/s*4095)
		}
		for i := k; i < len(test.pix); i += 3 {
			out.pix[i] = test.pix[i]*float32(s) + float32(o)
		}
	}
	return out
}

func save(l *render.Linear, path string) {
	var bright float32 = 1
	fmt.Sscanf(os.Getenv("BRIGHT"), "%f", &bright)
	img := image.NewRGBA(image.Rect(0, 0, l.W, l.H))
	for i := 0; i < l.W*l.H; i++ {
		for k := 0; k < 3; k++ {
			v := math.Max(0, math.Min(1, float64(render.ToSRGB(l.Pix[i*3+k]*bright))))
			img.Pix[i*4+k] = uint8(v*255 + 0.5)
		}
		img.Pix[i*4+3] = 255
	}
	o, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	jpeg.Encode(o, img, &jpeg.Options{Quality: 90})
	o.Close()
}

// loadImg reads a rendered image (e.g. an SPP export) as linear RGB with an
// identity transform, cropped like develop.
func loadImg(path string, cx, cy, cw, ch float64) cam {
	d, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	img, _, err := render.DecodeImage(d)
	if err != nil {
		log.Fatal(err)
	}
	l := render.FromImage(img, 0, 0)
	w, h := int(cw*float64(l.W)), int(ch*float64(l.H))
	x0, y0 := int(cx*float64(l.W))-w/2, int(cy*float64(l.H))-h/2
	out := cam{w: w, h: h, pix: make([]float32, w*h*3)}
	out.t.Gain = [3]float64{1, 1, 1}
	out.t.CC = [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	for y := 0; y < h; y++ {
		copy(out.pix[y*w*3:(y+1)*w*3], l.Pix[((y0+y)*l.W+x0)*3:((y0+y)*l.W+x0+w)*3])
	}
	return out
}

func main() {
	refImg := flag.String("refimg", "", "instead of -ref: a rendered base-ISO image (e.g. SPP's export)")
	testImg := flag.String("testimg", "", "instead of -test: the rendered high-ISO image")
	refPath := flag.String("ref", "", "base-ISO X3F (the truth)")
	testPath := flag.String("test", "", "high-ISO X3F of the same scene")
	methods := flag.String("method", "none,chroma", "comma-separated: none, chroma")
	saveDir := flag.String("save", "", "write sRGB JPEG crops here")
	cx := flag.Float64("cx", 0.5, "crop centre x (fraction)")
	cy := flag.Float64("cy", 0.5, "crop centre y (fraction)")
	cs := flag.Float64("crop", 0.3, "crop size (fraction of width/height)")
	flag.Parse()
	var ref, test cam
	if *refImg != "" {
		ref = loadImg(*refImg, *cx, *cy, *cs, *cs)
		test = loadImg(*testImg, *cx, *cy, *cs, *cs)
	} else {
		ref = develop(*refPath, *cx, *cy, *cs, *cs)
		test = develop(*testPath, *cx, *cy, *cs, *cs)
	}
	if os.Getenv("DIAG") != "" {
		diagnose(ref, test)
	}
	refL := ref.toLinear(ref.t)
	// Scale the reference to a comfortable mid-grey exposure for ΔE.
	var mx float32
	for _, v := range refL.Pix {
		mx = max(mx, v)
	}
	norm := 0.9 / mx
	for i := range refL.Pix {
		refL.Pix[i] *= norm
	}
	if *saveDir != "" {
		save(refL, filepath.Join(*saveDir, "ref.jpg"))
	}
	for _, m := range strings.Split(*methods, ",") {
		t0 := time.Now()
		var d cam
		switch {
		case m == "none":
			d = test
		case strings.HasPrefix(m, "chroma"):
			r, eps := 8, float32(0.01)
			fmt.Sscanf(m, "chroma%d_%f", &r, &eps)
			d = chroma(test, r, eps)
		case strings.HasPrefix(m, "pkg"): // the x3f package's Denoise (what the app uses)
			st := 1.0
			fmt.Sscanf(m, "pkg%f", &st)
			d = test
			d.pix = append([]float32(nil), test.pix...)
			x3f.Denoise(&x3f.CamImage{W: d.w, H: d.h, Pix: d.pix}, x3f.DenoiseOptions{Strength: st})
		case strings.HasPrefix(m, "fov"):
			r, eps, kl := 16, float32(0.01), 1.0
			fmt.Sscanf(m, "fov%d_%f_%f", &r, &eps, &kl)
			d = foveonDenoise(test, r, eps, kl)
		default:
			log.Fatalf("unknown method %q", m)
		}
		el := time.Since(t0)
		mt := matchFrom(ref, test, d) // the same exposure map for every method
		l := mt.toLinear(ref.t)       // same colour transform as the reference
		for i := range l.Pix {
			l.Pix[i] *= norm
		}
		mean, p95 := calib.DeltaEStats(l, refL)
		bm, _ := calib.DeltaEStats(blurLin(l, 6), blurLin(refL, 6))
		fmt.Printf("%-16s dE2000 mean %5.2f p95 %5.2f | colour only (blurred) %5.2f   (%v)\n", m, mean, p95, bm, el.Round(time.Millisecond))
		if *saveDir != "" {
			save(l, filepath.Join(*saveDir, m+".jpg"))
		}
	}
}

func blurLin(l *render.Linear, r int) *render.Linear {
	out := &render.Linear{W: l.W, H: l.H, Pix: make([]float32, len(l.Pix))}
	for k := 0; k < 3; k++ {
		p := make([]float32, l.W*l.H)
		for i := range p {
			p[i] = l.Pix[i*3+k]
		}
		b := boxBlur(p, l.W, l.H, r)
		for i := range b {
			out.Pix[i*3+k] = max(b[i], 0)
		}
	}
	return out
}

// diagnose prints where the exposure-matched test frame's layers deviate
// from the reference (both blurred, radius 6): spread over columns and rows
// (fixed-pattern / banding) and the mean deviation by reference level (a
// non-linearity at low signal), each relative to the layer's mean level.
func diagnose(ref, test cam) {
	mt := matchFrom(ref, test, test)
	for k := 0; k < 3; k++ {
		rb, tb := blurred(ref, k), blurred(mt, k)
		var mean float64
		for _, v := range rb {
			mean += float64(v)
		}
		mean /= float64(len(rb))
		col := make([]float64, ref.w)
		row := make([]float64, ref.h)
		for y := 0; y < ref.h; y++ {
			for x := 0; x < ref.w; x++ {
				d := float64(tb[y*ref.w+x] - rb[y*ref.w+x])
				col[x] += d / float64(ref.h)
				row[y] += d / float64(ref.w)
			}
		}
		sd := func(v []float64) float64 {
			var m, s float64
			for _, x := range v {
				m += x
			}
			m /= float64(len(v))
			for _, x := range v {
				s += (x - m) * (x - m)
			}
			return math.Sqrt(s/float64(len(v))) / mean * 100
		}
		// Deviation by reference level (quartiles of the reference).
		var bins [5][2]float64
		for i, v := range rb {
			b := min(4, int(float64(v)/mean*1.5))
			bins[b][0] += float64(tb[i] - v)
			bins[b][1]++
		}
		fmt.Printf("  layer %d: column spread %.1f%%, row spread %.1f%%; mean deviation by level:", k, sd(col), sd(row))
		for _, b := range bins {
			if b[1] > 100 {
				fmt.Printf(" %+.1f%%", b[0]/b[1]/mean*100)
			}
		}
		fmt.Println()
	}
}

// noiseSigma estimates a plane's noise from the finest detail (MAD of
// horizontal differences / (0.6745*sqrt2)).
func noiseSigma(p []float32, w, h int) float64 {
	var d []float64
	for y := 0; y < h; y += 2 {
		for x := 0; x+1 < w; x += 2 {
			d = append(d, math.Abs(float64(p[y*w+x+1]-p[y*w+x])))
		}
	}
	sort.Float64s(d)
	return d[len(d)/2] / 0.6745 / math.Sqrt2
}

// atrous denoises a plane with an à-trous B3-spline wavelet: each detail
// level is soft-thresholded at k * its noise (noise falls by the known
// B3 factors per level).
func atrous(p []float32, w, h int, sigma, k float64, levels int) []float32 {
	cur := append([]float32(nil), p...)
	out := make([]float32, len(p))
	kern := [5]float32{1.0 / 16, 4.0 / 16, 6.0 / 16, 4.0 / 16, 1.0 / 16}
	// noise std of each detail level for unit white noise (B3 à trous)
	lvlNoise := []float64{0.8908, 0.2007, 0.0856, 0.0413, 0.0205, 0.0103}
	for l := 0; l < levels; l++ {
		step := 1 << l
		tmp := make([]float32, len(p))
		next := make([]float32, len(p))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var s float32
				for i, kv := range kern {
					xx := x + (i-2)*step
					if xx < 0 {
						xx = -xx
					}
					if xx >= w {
						xx = 2*w - 2 - xx
					}
					s += kv * cur[y*w+xx]
				}
				tmp[y*w+x] = s
			}
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var s float32
				for i, kv := range kern {
					yy := y + (i-2)*step
					if yy < 0 {
						yy = -yy
					}
					if yy >= h {
						yy = 2*h - 2 - yy
					}
					s += kv * tmp[yy*w+x]
				}
				next[y*w+x] = s
			}
		}
		t := float32(k * sigma * lvlNoise[min(l, len(lvlNoise)-1)])
		for i := range cur {
			d := cur[i] - next[i]
			switch {
			case d > t:
				d -= t
			case d < -t:
				d += t
			default:
				d = 0
			}
			out[i] += d
		}
		cur = next
	}
	for i := range out {
		out[i] += cur[i]
	}
	return out
}

// foveonDenoise: luminance = inverse-noise-variance weighted sum of the
// layers (wavelet-denoised with strength kl), colour = each layer's ratio
// to it, guided-filtered (radius r, eps) with the clean luminance as guide.
func foveonDenoise(c cam, r int, eps float32, kl float64) cam {
	n := c.w * c.h
	var wgt [3]float64
	var sum float64
	for k := 0; k < 3; k++ {
		p := make([]float32, n)
		for i := range p {
			p[i] = c.pix[i*3+k]
		}
		s := noiseSigma(p, c.w, c.h)
		wgt[k] = 1 / (s * s)
		sum += wgt[k]
	}
	for k := range wgt {
		wgt[k] /= sum
	}
	Y := make([]float32, n)
	for i := 0; i < n; i++ {
		Y[i] = float32(wgt[0])*c.pix[i*3] + float32(wgt[1])*c.pix[i*3+1] + float32(wgt[2])*c.pix[i*3+2]
	}
	Yd := Y
	if kl > 0 {
		Yd = atrous(Y, c.w, c.h, noiseSigma(Y, c.w, c.h), kl, 5)
	}
	var mean float32
	for _, v := range Yd {
		mean += v
	}
	mean /= float32(n)
	G := make([]float32, n)
	for i := range G {
		G[i] = Yd[i] / mean
	}
	out := cam{w: c.w, h: c.h, pix: make([]float32, len(c.pix)), t: c.t}
	for k := 0; k < 3; k++ {
		p := make([]float32, n)
		for i := 0; i < n; i++ {
			p[i] = c.pix[i*3+k] / mean
		}
		f := guided(G, p, c.w, c.h, r, eps)
		for i := 0; i < n; i++ {
			out.pix[i*3+k] = f[i] * mean
		}
	}
	// Rescale each pixel so its weighted luminance is the denoised one.
	for i := 0; i < n; i++ {
		y := float32(wgt[0])*out.pix[i*3] + float32(wgt[1])*out.pix[i*3+1] + float32(wgt[2])*out.pix[i*3+2]
		if y > 1e-7 {
			s := Yd[i] / y
			out.pix[i*3] *= s
			out.pix[i*3+1] *= s
			out.pix[i*3+2] *= s
		}
	}
	return out
}
