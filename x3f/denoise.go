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
// Measured on Photography Blog's DP1/DP2/DP3 Merrill ISO series (same scene
// at ISO 100 ... 6400; tools/noisebench): ISO 6400 dE2000 against ISO 100
// fell 3-5x in all 18 crops; DP2 Merrill 28.0 -> 6.6 (Sigma Photo Pro with
// its [CAL] A00 settings: 18.1).

import (
	"math"
	"os"
	"runtime"
	"sort"
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
	var wgt [3]float64
	var sum float64
	for k := 0; k < 3; k++ {
		s := noiseSigma(plane(k), w, h)
		if s <= 0 {
			s = 1e-6
		}
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
	sigY32 := noiseSigma(Y, w, h)
	Yd := atrous(Y, w, h, sigY32, 2*o.Strength, 5)
	sigY := float32(max(sigY32, 1e-6))
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
			out[k] = guided(G, p, w, h, r, 0.01)
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
// times each level's noise (sigma = the plane's noise).
func atrous(p []float32, w, h int, sigma, k float64, levels int) []float32 {
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
		cur, next = next, cur
	}
	for i := range out {
		out[i] += cur[i]
	}
	return out
}
