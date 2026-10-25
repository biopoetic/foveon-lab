package x3f

// Foveon-specific noise reduction. The three layers see the same spot, but
// their noise differs a lot (the top layer is about three times cleaner than
// the bottom one at high ISO, where Merrill noise is mostly read noise). So:
//
//   - luminance is the layers' inverse-noise-variance weighted sum (not the
//     colour matrix's mix, which drags the bottom layer's noise into it),
//     optionally wavelet-denoised;
//   - colour is each layer's level, guided-filtered with that clean
//     luminance as the guide (edges stay, colour noise averages out);
//   - each pixel is then rescaled to its (denoised) luminance.
//
// Everything follows the measured noise: a per-plane noise model
// sigma(v) = sqrt(a + b·v) is fitted on the flattest 16×16 blocks at each
// brightness, the luminance wavelet threshold is that sigma at each pixel's
// level, and the colour filter's eps is a few times the relative luminance
// noise, so a clean frame keeps its colour texture.
//
// History: v0.4.0-v0.4.2 estimated the noise as the median neighbour
// difference of the whole image, which on a clean frame is texture, and
// used a fixed colour eps; it smeared fine detail even at ISO 200. The
// 2026-10-14 noisebench "detail kept" (slope of fine luminance detail on
// the ISO 100 frame; 1 = all kept, noise does not bias it) on Photography
// Blog's DP2 Merrill series, no NR / old / this: ISO 200 0.83 / 0.65 /
// 0.72, ISO 6400 0.74 / 0.36 / 0.48, at the same dE2000 (ISO 6400 vs
// ISO 100: 28.0 no NR, 6.9 old, 7.9 this). The earlier claim of beating
// SPP (18.1) rested on dE2000 alone, which rewards smoothing; Sigma Photo
// Pro keeps more texture and also sharpens, which this does not.

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"
)

// DenoiseOptions tunes Denoise. Zero values pick defaults.
type DenoiseOptions struct {
	// Strength blends the result with the original (1 = full, 0 = off).
	Strength float64
	// Scale is the image's size relative to the full sensor readout (a
	// preview downscaled 4x is 0.25; 0 = 1), so the smoothing covers the
	// same scene features at every size.
	Scale float64
	// ISO, when known, sets the colour filter's radius (colourRadius).
	ISO float64
	// Grey is a neutral grey's layer values (any scale; zero = equal
	// layers). Near black the denoised luminance goes back additively, in
	// these proportions: added equally to the layers it was not grey once
	// the white balance (DP1 Merrill gains ~2.0 / 1 / 0.6) and the colour
	// matrix were applied, a cast that grew with ISO as more pixels fell
	// near black (DP1 Merrill still life ISO 6400 vs its own ISO 100:
	// a* -8.0, green).
	Grey [3]float64
}

// colourRadius is the colour filter's radius at full sensor resolution for
// a shot's ISO. Wide (32 px) colour smoothing pulled dark greens toward
// their surroundings: on SDIM0432 (SD1, ISO 200) the denoiser shifted the
// moss's layer means by 4-8%, which the colour matrix turned from green
// (R/G 0.73, SPP 0.66) to red-brown (1.31). Up to ISO 200 the colour noise
// is small and real colour detail dominates, so the filter stays tight.
// Neutral render vs SPP, 13 SD1/SD15 photos (all ISO 100-200), 32 -> 8 px:
// mean 7.56 -> 7.40, every photo better. The SD1 ISO series (noisebench,
// colour error vs the ISO 200 frame, 32 / 16 / 8 px): ISO 200 itself 0.80 /
// 0.62 / 0.45, ISO 400 4.85 / 4.88 / 4.94, ISO 800 4.73 / 4.77 / 4.88,
// ISO 1600 5.64 / 5.74 / 5.96 - so wide from ISO 800, as before.
func colourRadius(iso float64) float64 {
	switch {
	case iso <= 0 || iso >= 800:
		return 32
	case iso <= 200:
		return 8
	default:
		return 16
	}
}

// additive forces the purely additive luminance recombination (research
// switch: FOVEON_DN_ADD=1).
var additive = os.Getenv("FOVEON_DN_ADD") != ""

var debugNoise = os.Getenv("FOVEON_DN_DEBUG") != ""

// Tuning (research switches FOVEON_DN_K, FOVEON_DN_EPSC): the luminance
// wavelet threshold in noise sigmas, and the colour filter's eps in units
// of the relative luminance noise.
var (
	dnK    = envFloat("FOVEON_DN_K", 1.0)
	dnEpsC = envFloat("FOVEON_DN_EPSC", 3.0)
)

func envFloat(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil {
		return v
	}
	return def
}

// Denoise reduces img's noise in place.
func Denoise(img *CamImage, o DenoiseOptions) {
	if o.Strength <= 0 || img.W < 16 || img.H < 16 {
		return
	}
	w, h, n := img.W, img.H, img.W*img.H
	timing := os.Getenv("FOVEON_DN_TIMING") != ""
	mark := time.Now()
	lap := func(name string) {
		if timing {
			fmt.Fprintf(os.Stderr, "    dn %-14s %s\n", name, time.Since(mark).Round(time.Millisecond))
			mark = time.Now()
		}
	}
	orig := append([]float32(nil), img.Pix...)
	plane := func(k int) []float32 {
		p := make([]float32, n)
		for i := range p {
			p[i] = img.Pix[i*3+k]
		}
		return p
	}
	// Noise per layer and of the luminance, as a function of brightness,
	// measured on the flattest parts of the image (texture is not noise).
	var models [3]noiseModel
	var wgt [3]float64
	var sum float64
	for k := 0; k < 3; k++ {
		pk := plane(k)
		models[k] = estimateNoise(pk, w, h)
		s := models[k].at(median(pk))
		wgt[k] = 1 / (s * s)
		sum += wgt[k]
	}
	for k := range wgt {
		wgt[k] /= sum
	}
	lap("noise models")
	wf := [3]float32{float32(wgt[0]), float32(wgt[1]), float32(wgt[2])}
	gw := greyWeights(o.Grey, wf)
	Y := make([]float32, n)
	parallel(n, func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			Y[i] = wf[0]*img.Pix[i*3] + wf[1]*img.Pix[i*3+1] + wf[2]*img.Pix[i*3+2]
		}
	})
	ym := estimateNoise(Y, w, h)
	local := boxBlur(Y, w, h, 4)
	sig := make([]float32, n) // the luminance noise at each pixel's level
	// sigma(v) = sqrt(a + b·v) written out rather than through noiseModel.at:
	// a method call and a math.Max per pixel was 8% of a denoise in the
	// profile (most of it in restoreChroma, which is written out too).
	na, nb := ym.a, ym.b
	parallel(n, func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			v := float64(local[i])
			if v < 0 {
				v = 0
			}
			x := na + nb*v
			if x < 1e-14 {
				x = 1e-14
			}
			s := float32(math.Sqrt(x))
			if s < 1e-6 {
				s = 1e-6
			}
			sig[i] = s
		}
	})
	Yd := atrous(Y, w, h, sig, dnK, 5)
	lap("atrous")
	var mean float64
	for _, v := range Yd {
		mean += float64(v)
	}
	mean /= float64(n)
	if mean <= 0 {
		return
	}
	m := float32(mean)
	G := make([]float32, n)
	parallel(n, func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			G[i] = Yd[i] / m
		}
	})
	// Colour smoothing follows the noise: eps (in the guide's relative
	// units) is a few times the luminance noise at the mean level, so a clean
	// image keeps its colour texture and a noisy one is smoothed hard.
	rel := ym.at(mean) / mean
	if debugNoise {
		fmt.Fprintf(os.Stderr, "denoise: relative luminance noise %.4f (sigma %.5f at mean %.4f)\n", rel, ym.at(mean), mean)
	}
	eps := float32(math.Max(1e-6, (dnEpsC*rel)*(dnEpsC*rel)))
	scale := o.Scale
	if scale <= 0 {
		scale = 1
	}
	r := max(2, int(math.Round(colourRadius(o.ISO)*scale)))
	// The guide does not change per layer: its blurs are shared.
	mI, mII := boxBlur(G, w, h, r), boxBlurSq(G, w, h, r)
	var out [3][]float32
	var wg sync.WaitGroup
	for k := 0; k < 3; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			p := plane(k)
			for i := range p {
				p[i] /= m
			}
			out[k] = guidedPre(G, p, w, h, r, eps, mI, mII)
		}(k)
	}
	wg.Wait()
	parallel(n, func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			a, b, c := out[0][i]*m, out[1][i]*m, out[2][i]*m
			y := wf[0]*a + wf[1]*b + wf[2]*c
			// Put the denoised luminance back: scaling the layers keeps colour
			// best where the signal is clear of the noise; near black the scale
			// factor is noise itself (dark dots, colour halos), so there the
			// luminance difference is added instead, blending over 2-6 sigma.
			d := Yd[i] - y
			ma, mb, mc := a+d*gw[0], b+d*gw[1], c+d*gw[2]
			sigY := sig[i]
			if t := (y - 2*sigY) / (4 * sigY); y > 1e-9 && t > 0 && !additive {
				s := Yd[i] / y
				t = min(t, 1)
				ma = (1-t)*ma + t*a*s
				mb = (1-t)*mb + t*b*s
				mc = (1-t)*mc + t*c*s
			}
			a, b, c = ma, mb, mc
			img.Pix[i*3], img.Pix[i*3+1], img.Pix[i*3+2] = a, b, c
		}
	})
	lap("guided")
	if !noRestore {
		if perLayerRestore {
			restoreDetail(img, orig, rel, o.Strength)
			lap("restore detail")
		} else {
			restoreLuma(img, orig, wf, gw, rel, o.Strength)
			lap("restore luma")
			restoreChroma(img, orig, wf, models)
			lap("restore chroma")
		}
	}
}

// Detail given back after the filters, per frequency band (box radii
// 0-1, 1-2, 2-4, 4-8 px; finest first): the fraction of each band of
// (original - denoised) that returns, by the frame's relative luminance
// noise (log-interpolated between the rows). Fitted so that the result
// keeps each band about as Sigma Photo Pro's neutral export does
// (tools/sharpfit -gain: regression of the output's band on the
// no-NR band, same raw data, SD1 ISO 100-6400, 2026-10-14). SPP, finest
// band: 0.8 at ISO 100-200, 0.5 at 400, 0.27 at 800, 0.15-0.25 from 1600;
// the filters alone kept 0.03-0.2.
var (
	keepNoise = []float64{0.011, 0.017, 0.026, 0.042, 0.072, 0.109}
	keepBand  = [][4]float64{
		{0.76, 1, 1, 1},
		{0.45, 0.82, 1, 1},
		{0.20, 0.36, 0.77, 1},
		{0.12, 0.14, 0.29, 1},
		{0.17, 0.20, 0.36, 1},
		{0.21, 0.17, 0.21, 0.46},
	}
	noRestore = os.Getenv("FOVEON_DN_NORESTORE") != ""
	// perLayerRestore: give back each layer's own difference, as up to
	// v0.4.8 (research switch FOVEON_DN_PERLAYER=1), which gave back each
	// layer's noise with it. The default gives back the luminance detail
	// (restoreLuma) and the colour detail that stands clear of the noise per
	// band (restoreChroma). Photography Blog's DP2 Merrill ISO series
	// (noisebench vs the ISO 100 frame, 2026-10-14), NR Low, per layer ->
	// now: dE2000 ISO 200 6.11 -> 4.06, ISO 1600 16.78 -> 4.51, ISO 6400
	// 26.32 -> 7.88; detail kept 0.81 -> 0.77, 0.79 -> 0.77, 0.67 -> 0.65.
	perLayerRestore = os.Getenv("FOVEON_DN_PERLAYER") != ""
)

func keepFor(rel float64) [4]float64 {
	if rel <= keepNoise[0] {
		return keepBand[0]
	}
	last := len(keepNoise) - 1
	if rel >= keepNoise[last] {
		return keepBand[last]
	}
	i := sort.SearchFloat64s(keepNoise, rel)
	t := (math.Log(rel) - math.Log(keepNoise[i-1])) / (math.Log(keepNoise[i]) - math.Log(keepNoise[i-1]))
	var k [4]float64
	for b := range k {
		k[b] = keepBand[i-1][b]*(1-t) + keepBand[i][b]*t
	}
	return k
}

// restoreDetail adds back, per layer and band, keepFor(rel) of what the
// filters removed. Strength below 1 (NR Medium, Low) blends towards the
// original in every band, the coarse rest included: the filters always
// run at full strength, since a weaker colour filter (smaller radius and
// eps) left blotches about 20 px wide in near-black areas, which no fine
// band restores (a night shot at NR Low).
func restoreDetail(img *CamImage, orig []float32, rel, strength float64) {
	keep := keepFor(rel)
	var wb [4]float32
	for b := range keep {
		wb[b] = float32(1 - math.Min(1, strength)*(1-keep[b]))
	}
	rest := float32(1 - math.Min(1, strength))
	w, h, n := img.W, img.H, img.W*img.H
	d := make([]float32, n)
	for k := 0; k < 3; k++ {
		for i := range d {
			d[i] = orig[i*3+k] - img.Pix[i*3+k]
		}
		bands := boxBlurRads(d, w, h, []int{1, 2, 4, 8})
		b1, b2, b4, b8 := bands[0], bands[1], bands[2], bands[3]
		for i := range d {
			img.Pix[i*3+k] += wb[0]*(d[i]-b1[i]) + wb[1]*(b1[i]-b2[i]) + wb[2]*(b2[i]-b4[i]) + wb[3]*(b4[i]-b8[i]) + rest*b8[i]
		}
	}
}

// restoreLuma is restoreDetail on the luminance only: the bands of what
// the filters removed from the noise-weighted luminance (whose noise is
// mostly the clean top layer's) go back into every layer alike. Giving
// back each layer's own difference also gives back each layer's noise, the
// bottom layer's above all: at ISO 6400 that was most of the noise left.
func restoreLuma(img *CamImage, orig []float32, wf, gw [3]float32, rel, strength float64) {
	keep := keepFor(rel)
	var wb [4]float32
	for b := range keep {
		wb[b] = float32(1 - math.Min(1, strength)*(1-keep[b]))
	}
	rest := float32(1 - math.Min(1, strength))
	w, h, n := img.W, img.H, img.W*img.H
	d := make([]float32, n)
	parallel(n, func(a, b int) {
		for i := a; i < b; i++ {
			for k := 0; k < 3; k++ {
				d[i] += wf[k] * (orig[i*3+k] - img.Pix[i*3+k])
			}
		}
	})
	bands := boxBlurRads(d, w, h, []int{1, 2, 4, 8})
	b1, b2, b4, b8 := bands[0], bands[1], bands[2], bands[3]
	parallel(n, func(a, b int) {
		for i := a; i < b; i++ {
			v := wb[0]*(d[i]-b1[i]) + wb[1]*(b1[i]-b2[i]) + wb[2]*(b2[i]-b4[i]) + wb[3]*(b4[i]-b8[i]) + rest*b8[i]
			// Scale the layers, keeping their ratios (the colour): the same
			// amount added to each layer is not grey once the colour matrix
			// (which amplifies layer differences ~10x) is applied, and washed
			// out saturated colours (an orange spatula at ISO 200). Near black
			// the ratios are noise, so there it is added.
			p := img.Pix[i*3 : i*3+3]
			y := wf[0]*p[0] + wf[1]*p[1] + wf[2]*p[2]
			if y > 1e-6 && y+v > 0 {
				s := (y + v) / y
				p[0], p[1], p[2] = p[0]*s, p[1]*s, p[2]*s
			} else {
				p[0], p[1], p[2] = p[0]+v*gw[0], p[1]+v*gw[1], p[2]+v*gw[2]
			}
		}
	})
}

// Chroma restore threshold, in noise sigmas (research switch FOVEON_DN_CT).
// It does not follow the NR strength: scaling it down for NR Low brought
// the colour noise back (DP2 Merrill ISO 6400, dE2000 8.3 -> 18.1).
var dnCT = envFloat("FOVEON_DN_CT", 3.0)

// restoreChroma gives back the colour detail the filters removed where it
// stands clear of the noise: each layer's difference minus the luminance
// difference, split into bands (box radii 0-1, 1-2, 2-4, 4-8, 8+) and
// soft-thresholded per band at a few times that layer's noise in the band
// (a box mean over (2r+1)^2 pixels cuts the noise (2r+1)-fold). Foveon's
// colour signal is small in camera space (the colour matrix amplifies layer
// differences ~10x), so at the pixel scale real colour detail and colour
// noise are alike; a coloured surface or edge is large at the coarse scales,
// where the noise is not. Without it a clean ISO 200 frame lost its colour
// detail (print on an orange spatula) and its saturated edges bled.
func restoreChroma(img *CamImage, orig []float32, wf [3]float32, models [3]noiseModel) {
	w, h, n := img.W, img.H, img.W*img.H
	t := float32(dnCT)
	if debugNoise {
		for k := 0; k < 3; k++ {
			fmt.Fprintf(os.Stderr, "denoise: layer %d noise a=%.3g b=%.3g, sigma at 0.05/0.2/0.5: %.5f %.5f %.5f\n", k, models[k].a, models[k].b, models[k].at(0.05), models[k].at(0.2), models[k].at(0.5))
		}
	}
	// Each band's noise relative to the pixel noise: the finer box's.
	fr := [5]float32{1, 1.0 / 3, 1.0 / 5, 1.0 / 9, 1.0 / 17}
	c := make([]float32, n)
	p := make([]float32, n)
	for k := 0; k < 3; k++ {
		parallel(n, func(a, b int) {
			for i := a; i < b; i++ {
				var dy float32
				for j := 0; j < 3; j++ {
					dy += wf[j] * (orig[i*3+j] - img.Pix[i*3+j])
				}
				c[i] = orig[i*3+k] - img.Pix[i*3+k] - dy
				p[i] = orig[i*3+k]
			}
		})
		// The four bands of c share one read of c; the brightness plane p is
		// blurred alongside them.
		var lvl []float32
		var bands [][]float32
		var bw sync.WaitGroup
		bw.Add(2)
		go func() { defer bw.Done(); lvl = boxBlur(p, w, h, 2) }()
		go func() { defer bw.Done(); bands = boxBlurRads(c, w, h, []int{1, 2, 4, 8}) }()
		bw.Wait()
		b1, b2, b4, b8 := bands[0], bands[1], bands[2], bands[3]
		// The threshold loop is the hot one: the noise model, the five
		// thresholds and the five soft cuts are written out here rather than
		// called, because a call per pixel (and an indirect one through the
		// old soft closure) was a third of this stage.
		na, nb := models[k].a, models[k].b
		parallel(n, func(a, b int) {
			for i := a; i < b; i++ {
				v := float64(lvl[i])
				if v < 0 {
					v = 0
				}
				x := na + nb*v
				if x < 1e-14 {
					x = 1e-14
				}
				sg := t * float32(math.Sqrt(x))
				img.Pix[i*3+k] += softThreshold(c[i]-b1[i], sg*fr[0]) + softThreshold(b1[i]-b2[i], sg*fr[1]) +
					softThreshold(b2[i]-b4[i], sg*fr[2]) + softThreshold(b4[i]-b8[i], sg*fr[3]) + softThreshold(b8[i], sg*fr[4])
			}
		})
	}
}

// softThreshold leaves the part of c beyond th, keeping its sign.
func softThreshold(c, th float32) float32 {
	switch {
	case c > th:
		return c - th
	case c < -th:
		return c + th
	}
	return 0
}

// noiseSigma estimates a plane's noise from the median absolute horizontal
// difference of neighbours (robust to edges).
func noiseSigma(p []float32, w, h int) float64 {
	d := make([]float64, 0, w*h/4)
	for y := 0; y < h; y += 2 {
		for x := 0; x+1 < w; x += 2 {
			d = append(d, math.Abs(float64(p[y*w+x+1]-p[y*w+x])))
		}
	}
	if len(d) == 0 {
		return 0
	}
	sort.Float64s(d)
	return d[len(d)/2] / 0.6745 / math.Sqrt2
}

// parallel runs fn over [0, n) in row bands.
func parallel(n int, fn func(a, b int)) {
	workers := runtime.GOMAXPROCS(0)
	step := max(1, (n+workers-1)/workers)
	var wg sync.WaitGroup
	for a := 0; a < n; a += step {
		wg.Add(1)
		go func(a, b int) { defer wg.Done(); fn(a, b) }(a, min(a+step, n))
	}
	wg.Wait()
}

// A pool of float scratch planes: a 15 MP frame needs ~20 of them per
// denoise and each is fully written before it is read, so reusing them saves
// the allocation and the GC pressure (339 MB per frame measured).
var planePool sync.Pool

func getPlane(n int) []float32 {
	if v := planePool.Get(); v != nil {
		if b := v.([]float32); cap(b) >= n {
			return b[:n]
		}
	}
	return make([]float32, n)
}

func putPlane(b []float32) {
	if cap(b) > 0 {
		planePool.Put(b[:0])
	}
}

// boxBlur is a separable box blur of radius r (edges average fewer pixels).
func boxBlur(p []float32, w, h, r int) []float32 {
	tmp := getPlane(len(p))
	blurRowsH(p, tmp, w, h, r)
	out := boxBlurCols(tmp, w, h, r)
	putPlane(tmp)
	return out
}

// boxBlurRads blurs one plane at several radii at once. The horizontal passes
// share a single read of p: each row is copied once into scratch, and every
// radius walks the copy while it is in cache. Each radius keeps its own
// running sum in the same order as boxBlur, so the results are bit-identical.
func boxBlurRads(p []float32, w, h int, radii []int) [][]float32 {
	tmp := make([][]float32, len(radii))
	for i := range tmp {
		tmp[i] = getPlane(len(p))
	}
	parallel(h, func(y0, y1 int) {
		buf := make([]float32, w)
		for y := y0; y < y1; y++ {
			row := p[y*w : (y+1)*w]
			copy(buf, row)
			for i, r := range radii {
				blurRowH(buf, tmp[i][y*w:(y+1)*w], w, r)
			}
		}
	})
	out := make([][]float32, len(radii))
	for i, r := range radii {
		out[i] = boxBlurCols(tmp[i], w, h, r)
		putPlane(tmp[i])
	}
	return out
}

// blurRowsH is boxBlur's horizontal pass, p into dst.
func blurRowsH(p, dst []float32, w, h, r int) {
	parallel(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			blurRowH(p[y*w:(y+1)*w], dst[y*w:(y+1)*w], w, r)
		}
	})
}

// blurRowH is one row of the horizontal pass.
func blurRowH(row, o []float32, w, r int) {
	if 2*r+1 >= w {
		// Narrow image: the general form.
		var s float32
		cnt := 0
		for x := -r; x < w; x++ {
			if x+r < w {
				s += row[x+r]
				cnt++
			}
			if x-r-1 >= 0 {
				s -= row[x-r-1]
				cnt--
			}
			if x >= 0 {
				o[x] = s / float32(cnt)
			}
		}
		return
	}
	// Left edge, middle (full window, no branches), right edge.
	var s float32
	for x := 0; x < r; x++ {
		s += row[x]
	}
	for x := 0; x <= r; x++ {
		s += row[x+r]
		o[x] = s / float32(x+r+1)
	}
	inv := 1 / float32(2*r+1)
	for x := r + 1; x < w-r; x++ {
		s += row[x+r] - row[x-r-1]
		o[x] = s * inv
	}
	for x := max(r+1, w-r); x < w; x++ {
		s -= row[x-r-1]
		o[x] = s / float32(w-(x-r))
	}
}

// boxBlurCols is boxBlur's vertical pass: tmp into a new plane.
func boxBlurCols(tmp []float32, w, h, r int) []float32 {
	out := getPlane(len(tmp))
	// Vertical pass row by row over bands of columns, a running sum per
	// column: walking each column top to bottom strides a whole row per
	// step and missed the cache, 46% of a full-size development's time.
	const band = 64
	nb := (w + band - 1) / band
	parallel(nb, func(b0, b1 int) {
		sum := make([]float32, band)
		for bi := b0; bi < b1; bi++ {
			x0 := bi * band
			bw := min(band, w-x0)
			s := sum[:bw]
			clear(s)
			cnt := 0
			for y := -r; y < h; y++ {
				if y+r < h {
					add := tmp[(y+r)*w+x0 : (y+r)*w+x0+bw]
					for i, v := range add {
						s[i] += v
					}
					cnt++
				}
				if y-r-1 >= 0 {
					sub := tmp[(y-r-1)*w+x0 : (y-r-1)*w+x0+bw]
					for i, v := range sub {
						s[i] -= v
					}
					cnt--
				}
				if y >= 0 {
					o := out[y*w+x0 : y*w+x0+bw]
					inv := 1 / float32(cnt)
					for i, v := range s {
						o[i] = v * inv
					}
				}
			}
		}
	})
	return out
}

// boxBlurRef is the column-by-column vertical pass, kept as the reference
// for the test.
func boxBlurRef(p []float32, w, h, r int) []float32 {
	tmp := make([]float32, len(p))
	out := make([]float32, len(p))
	for y := 0; y < h; y++ {
		var s float32
		cnt := 0
		for x := -r; x < w; x++ {
			if x+r < w {
				s += p[y*w+x+r]
				cnt++
			}
			if x-r-1 >= 0 {
				s -= p[y*w+x-r-1]
				cnt--
			}
			if x >= 0 {
				tmp[y*w+x] = s / float32(cnt)
			}
		}
	}
	for x := 0; x < w; x++ {
		var s float32
		cnt := 0
		for y := -r; y < h; y++ {
			if y+r < h {
				s += tmp[(y+r)*w+x]
				cnt++
			}
			if y-r-1 >= 0 {
				s -= tmp[(y-r-1)*w+x]
				cnt--
			}
			if y >= 0 {
				out[y*w+x] = s / float32(cnt)
			}
		}
	}
	return out
}

// guided is He et al.'s guided filter of p by guide I (radius r, eps).
func guided(I, p []float32, w, h, r int, eps float32) []float32 {
	mI, mII := boxBlur(I, w, h, r), boxBlurSq(I, w, h, r)
	return guidedPre(I, p, w, h, r, eps, mI, mII)
}

// boxBlurSq blurs I*I without materialising the square.
func boxBlurSq(I []float32, w, h, r int) []float32 {
	II := make([]float32, len(I))
	parallel(len(I), func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			II[i] = I[i] * I[i]
		}
	})
	return boxBlur(II, w, h, r)
}

// guidedPre is guided with the guide's own statistics already computed: the
// guide is the same for all three layers, so mI and mII are shared instead of
// being rebuilt per layer (a third of the box blurs in a render).
func guidedPre(I, p []float32, w, h, r int, eps float32, mI, mII []float32) []float32 {
	mp := boxBlur(p, w, h, r)
	Ip := getPlane(len(I))
	parallel(len(I), func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			Ip[i] = I[i] * p[i]
		}
	})
	mIp := boxBlur(Ip, w, h, r)
	a, b := make([]float32, len(I)), make([]float32, len(I))
	parallel(len(I), func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			// He et al.'s 0 <= a <= 1 is what makes this an edge-preserving
			// filter, but a chroma plane varies more than the luminance guide:
			// here a may legitimately exceed 1, so it is only bounded against a
			// runaway (clamping it to [0,1] cost the calibration 2-6 dE on 85
			// photos). The subtraction is done in float64 so eps still means
			// what it says at low signal: where the neighbourhood is flatter
			// than eps the estimate is noise, in float32 the two near-equal
			// subtractions lose their significant digits and a runs away, which
			// paints spikes along dark edges (SD1, ISO 100, black fur - the
			// coloured dots of 2026-10-22). There the local mean is the honest
			// answer (a = 0).
			mi, mpf := float64(mI[i]), float64(mp[i])
			varI := float64(mII[i]) - mi*mi
			ai := 0.0
			if varI > 0.5*float64(eps) {
				ai = (float64(mIp[i]) - mi*mpf) / (varI + float64(eps))
				if ai > 8 {
					ai = 8
				}
				if ai < -8 {
					ai = -8
				}
			}
			a[i] = float32(ai)
			b[i] = mp[i] - a[i]*mI[i]
		}
	})
	ma, mb := boxBlur(a, w, h, r), boxBlur(b, w, h, r)
	out := make([]float32, len(I))
	parallel(len(I), func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			out[i] = ma[i]*I[i] + mb[i]
		}
	})
	return out
}

// atrous soft-thresholds an à-trous B3-spline wavelet decomposition at k
// times each level's noise (sigma: the plane's noise at each pixel).
func atrous(p []float32, w, h int, sigma []float32, k float64, levels int) []float32 {
	cur := append([]float32(nil), p...)
	out := make([]float32, len(p))
	kern := [5]float32{1.0 / 16, 4.0 / 16, 6.0 / 16, 4.0 / 16, 1.0 / 16}
	lvlNoise := []float64{0.8908, 0.2007, 0.0856, 0.0413, 0.0205}
	tmp := make([]float32, len(p))
	next := make([]float32, len(p))
	mirror := func(i, n int) int {
		if i < 0 {
			i = -i
		}
		if i >= n {
			i = 2*n - 2 - i
		}
		return max(0, min(n-1, i))
	}
	for l := 0; l < levels; l++ {
		step := 1 << l
		parallel(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row, o := cur[y*w:(y+1)*w], tmp[y*w:(y+1)*w]
				for x := 0; x < w; x++ {
					if x >= 2*step && x < w-2*step {
						// Interior: no mirroring (same sum, same order).
						var s float32
						s += kern[0] * row[x-2*step]
						s += kern[1] * row[x-step]
						s += kern[2] * row[x]
						s += kern[3] * row[x+step]
						s += kern[4] * row[x+2*step]
						o[x] = s
						continue
					}
					var s float32
					for i, kv := range kern {
						s += kv * row[mirror(x+(i-2)*step, w)]
					}
					o[x] = s
				}
			}
		})
		parallel(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				var rows [5][]float32
				for i := range rows {
					r := mirror(y+(i-2)*step, h)
					rows[i] = tmp[r*w : (r+1)*w]
				}
				o := next[y*w : (y+1)*w]
				for x := 0; x < w; x++ {
					var s float32
					s += kern[0] * rows[0][x]
					s += kern[1] * rows[1][x]
					s += kern[2] * rows[2][x]
					s += kern[3] * rows[3][x]
					s += kern[4] * rows[4][x]
					o[x] = s
				}
			}
		})
		lk := float32(k * lvlNoise[min(l, len(lvlNoise)-1)])
		parallel(len(cur), func(a, b int) {
			for i := a; i < b; i++ {
				t := lk * sigma[i]
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
		})
		cur, next = next, cur
	}
	parallel(len(out), func(a, b int) {
		for i := a; i < b; i++ {
			out[i] += cur[i]
		}
	})
	return out
}

// noiseModel is a plane's noise as a function of brightness:
// sigma(v) = sqrt(a + b·v) (read noise plus shot noise).
type noiseModel struct{ a, b float64 }

func (m noiseModel) at(v float64) float64 {
	return math.Sqrt(math.Max(1e-14, m.a+m.b*math.Max(0, v)))
}

func median(p []float32) float64 {
	step := max(1, len(p)/20000)
	s := make([]float64, 0, len(p)/step+1)
	for i := 0; i < len(p); i += step {
		s = append(s, float64(p[i]))
	}
	sort.Float64s(s)
	return s[len(s)/2]
}

// estimateNoise fits the noise model on 16×16 blocks: each block's noise is
// the robust spread of its pixels' difference from their four neighbours'
// mean; per brightness band only the flattest blocks (15th percentile)
// count, so texture and edges are not mistaken for noise.
func estimateNoise(p []float32, w, h int) noiseModel {
	const B = 16
	type blk struct{ m, s float64 }
	// Rows of blocks in parallel, each block's median by selection (it was
	// a full sort per block, serially: a tenth of a development's time).
	nby := 0
	for by := 1; by+B < h-1; by += B {
		nby++
	}
	rows := make([][]blk, nby)
	parallel(nby, func(r0, r1 int) {
		res := make([]float64, 0, B*B)
		for ri := r0; ri < r1; ri++ {
			by := 1 + ri*B
			for bx := 1; bx+B < w-1; bx += B {
				res = res[:0]
				var sum float64
				for y := by; y < by+B; y++ {
					for x := bx; x < bx+B; x++ {
						v := p[y*w+x]
						sum += float64(v)
						r := v - (p[y*w+x-1]+p[y*w+x+1]+p[(y-1)*w+x]+p[(y+1)*w+x])/4
						res = append(res, math.Abs(float64(r)))
					}
				}
				// Laplacian residual of white noise: variance 1.25 sigma².
				rows[ri] = append(rows[ri], blk{sum / (B * B), selectK(res, len(res)/2) / 0.6745 / math.Sqrt(1.25)})
			}
		}
	})
	var bs []blk
	for _, r := range rows {
		bs = append(bs, r...)
	}
	if len(bs) < 32 {
		s := noiseSigma(p, w, h)
		return noiseModel{a: s * s}
	}
	sort.Slice(bs, func(i, j int) bool { return bs[i].m < bs[j].m })
	const bands = 16
	var xs, ys []float64
	for b := 0; b < bands; b++ {
		band := bs[b*len(bs)/bands : (b+1)*len(bs)/bands]
		if len(band) < 4 {
			continue
		}
		ss := make([]float64, len(band))
		var mm float64
		for i, x := range band {
			ss[i] = x.s
			mm += x.m
		}
		sort.Float64s(ss)
		s := ss[len(ss)*15/100]
		xs, ys = append(xs, mm/float64(len(band))), append(ys, s*s)
	}
	// Least squares var = a + b·v, b >= 0.
	var sx, sy, sxx, sxy float64
	for i := range xs {
		sx, sy, sxx, sxy = sx+xs[i], sy+ys[i], sxx+xs[i]*xs[i], sxy+xs[i]*ys[i]
	}
	nf := float64(len(xs))
	b := 0.0
	if d := nf*sxx - sx*sx; d > 0 {
		b = math.Max(0, (nf*sxy-sx*sy)/d)
	}
	a := math.Max(1e-14, (sy-b*sx)/nf)
	return noiseModel{a: a, b: b}
}

// selectK returns the k-th smallest value of v (reordering v), as
// sort.Float64s(v); v[k] would, in linear time (Hoare's selection).
func selectK(v []float64, k int) float64 {
	lo, hi := 0, len(v)-1
	for lo < hi {
		pv := v[(lo+hi)/2]
		i, j := lo, hi
		for i <= j {
			for v[i] < pv {
				i++
			}
			for v[j] > pv {
				j--
			}
			if i <= j {
				v[i], v[j] = v[j], v[i]
				i++
				j--
			}
		}
		switch {
		case k <= j:
			hi = j
		case k >= i:
			lo = i
		default:
			return v[k]
		}
	}
	return v[k]
}

// greyWeights is grey (DenoiseOptions.Grey) scaled so that wf·gw = 1: adding
// d·gw changes the weighted luminance by exactly d. A zero grey gives equal
// layers (wf sums to 1).
func greyWeights(grey [3]float64, wf [3]float32) [3]float32 {
	y := float64(wf[0])*grey[0] + float64(wf[1])*grey[1] + float64(wf[2])*grey[2]
	if grey[0] <= 0 || grey[1] <= 0 || grey[2] <= 0 || y <= 0 {
		return [3]float32{1, 1, 1}
	}
	return [3]float32{float32(grey[0] / y), float32(grey[1] / y), float32(grey[2] / y)}
}
