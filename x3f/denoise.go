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
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
)

// DenoiseOptions tunes Denoise. Zero values pick defaults.
type DenoiseOptions struct {
	// Strength scales the colour smoothing radius and luminance threshold
	// (1 = default, 0 = off).
	Strength float64
	// Scale is the image's size relative to the full sensor readout (a
	// preview downscaled 4x is 0.25; 0 = 1), so the smoothing covers the
	// same scene features at every size.
	Scale float64
}

// additive forces the purely additive luminance recombination (research
// switch: FOVEON_DN_ADD=1).
var additive = os.Getenv("FOVEON_DN_ADD") != ""

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
	wf := [3]float32{float32(wgt[0]), float32(wgt[1]), float32(wgt[2])}
	Y := make([]float32, n)
	for i := range Y {
		Y[i] = wf[0]*img.Pix[i*3] + wf[1]*img.Pix[i*3+1] + wf[2]*img.Pix[i*3+2]
	}
	ym := estimateNoise(Y, w, h)
	local := boxBlur(Y, w, h, 4)
	sig := make([]float32, n) // the luminance noise at each pixel's level
	for i := range sig {
		sig[i] = float32(max(ym.at(float64(local[i])), 1e-6))
	}
	Yd := atrous(Y, w, h, sig, dnK*o.Strength, 5)
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
	for i := range G {
		G[i] = Yd[i] / m
	}
	// Colour smoothing follows the noise: eps (in the guide's relative
	// units) is a few times the luminance noise at the mean level, so a clean
	// image keeps its colour texture and a noisy one is smoothed hard.
	rel := ym.at(mean) / mean
	eps := float32(math.Max(1e-6, (dnEpsC*o.Strength*rel)*(dnEpsC*o.Strength*rel)))
	scale := o.Scale
	if scale <= 0 {
		scale = 1
	}
	r := max(2, int(math.Round(32*o.Strength*scale)))
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
			out[k] = guided(G, p, w, h, r, eps)
		}(k)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		a, b, c := out[0][i]*m, out[1][i]*m, out[2][i]*m
		y := wf[0]*a + wf[1]*b + wf[2]*c
		// Put the denoised luminance back: scaling the layers keeps colour
		// best where the signal is clear of the noise; near black the scale
		// factor is noise itself (dark dots, colour halos), so there the
		// luminance difference is added instead, blending over 2-6 sigma.
		d := Yd[i] - y
		ma, mb, mc := a+d, b+d, c+d
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

// boxBlur is a separable box blur of radius r (edges average fewer pixels).
func boxBlur(p []float32, w, h, r int) []float32 {
	tmp := make([]float32, len(p))
	out := make([]float32, len(p))
	parallel(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			row := p[y*w : (y+1)*w]
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
					tmp[y*w+x] = s / float32(cnt)
				}
			}
		}
	})
	parallel(w, func(x0, x1 int) {
		for x := x0; x < x1; x++ {
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
	})
	return out
}

// guided is He et al.'s guided filter of p by guide I (radius r, eps).
func guided(I, p []float32, w, h, r int, eps float32) []float32 {
	mI, mp := boxBlur(I, w, h, r), boxBlur(p, w, h, r)
	Ip, II := make([]float32, len(I)), make([]float32, len(I))
	for i := range I {
		Ip[i], II[i] = I[i]*p[i], I[i]*I[i]
	}
	mIp, mII := boxBlur(Ip, w, h, r), boxBlur(II, w, h, r)
	a, b := make([]float32, len(I)), make([]float32, len(I))
	for i := range I {
		a[i] = (mIp[i] - mI[i]*mp[i]) / (mII[i] - mI[i]*mI[i] + eps)
		b[i] = mp[i] - a[i]*mI[i]
	}
	ma, mb := boxBlur(a, w, h, r), boxBlur(b, w, h, r)
	out := make([]float32, len(I))
	for i := range I {
		out[i] = ma[i]*I[i] + mb[i]
	}
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
				for x := 0; x < w; x++ {
					var s float32
					for i, kv := range kern {
						s += kv * cur[y*w+mirror(x+(i-2)*step, w)]
					}
					tmp[y*w+x] = s
				}
			}
		})
		parallel(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < w; x++ {
					var s float32
					for i, kv := range kern {
						s += kv * tmp[mirror(y+(i-2)*step, h)*w+x]
					}
					next[y*w+x] = s
				}
			}
		})
		lk := float32(k * lvlNoise[min(l, len(lvlNoise)-1)])
		for i := range cur {
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
		cur, next = next, cur
	}
	for i := range out {
		out[i] += cur[i]
	}
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
	var bs []blk
	res := make([]float64, 0, B*B)
	for by := 1; by+B < h-1; by += B {
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
			sort.Float64s(res)
			// Laplacian residual of white noise: variance 1.25 sigma².
			bs = append(bs, blk{sum / (B * B), res[len(res)/2] / 0.6745 / math.Sqrt(1.25)})
		}
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
