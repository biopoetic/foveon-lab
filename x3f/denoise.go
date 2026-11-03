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
// sigma(v) = sqrt(a + bĂ„â€šĂ˘â‚¬ĹˇÄ‚â€šĂ‚Â·v) is fitted on the flattest 16Ä‚â€žĂ˘â‚¬ĹˇÄ‚ËĂ˘â€šÂ¬Ă˘â‚¬ĹĄ16 blocks at each
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
	// NoiseAware enables the noise-aware restore fitted on the DPM and
	// Quattro sensor families (restoreRamp). Other generations keep the
	// previous restore until their own ladders are measured: on the SD14
	// and DP2x high-ISO photos it cost up to 0.47 in spp-check.
	NoiseAware bool
	// Quattro marks the sensor generation: its two low layers are quarter
	// resolution, so the same pixel radius covers twice the scene area in
	// the chroma planes, and its dark-tone pepper needs its own threshold.
	Quattro bool
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
//
// Task 39 split that high-ISO figure by scale and found the 32 px window
// pulled a small colour patch towards its surround: on the DP1 Merrill
// studio ladder at ISO 1600 the patch-mean colour error was 14.6 at 32 px
// and 11.9 at 24 px, the patch saturation 0.916 -> 0.937 (SPP 0.962), and
// the old 8-32 px blotch 4.18 -> 3.95. On a strict flat mask (chroma edges
// dilated away) the 16-32 px noise band is 0.45 against 0.32 at 32 px and
// the 32-64 band 0.84 against 0.81, so 24 trades a little flat chroma noise
// for much less colour bleeding. 24 is the one value used at every ISO
// >= 800; it is a radius, not a table per body.
func colourRadius(iso float64) float64 {
	switch {
	case iso <= 0:
		return 32
	case iso >= 800:
		// Research switch for measuring the high-ISO radius apart from the
		// base-ISO ones (the SD1 study above used -R, which also changed the
		// reference frame).
		if dnHighR > 0 {
			return dnHighR
		}
		return 24
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
	// dnRadius overrides the colour filter's radius (research switch
	// FOVEON_DN_R), so the radius can be measured apart from ISO.
	dnRadius = envFloat("FOVEON_DN_R", 0)
	// dnHighR overrides only the ISO >= 800 radius (FOVEON_DN_HIGHR), which
	// leaves the ISO 100 reference on its base-ISO radius.
	dnHighR = envFloat("FOVEON_DN_HIGHR", 0)
	// chromaSmooth puts the patch-scale chroma the wide filter removed back
	// into saturated areas (default on for Quattro; FOVEON_DN_NOCS=1
	// renders without it). csA2/csB2 are the absolute layer chroma where it
	// fades in; csResid is the radius of the residual low-pass.
	chromaSmooth = os.Getenv("FOVEON_DN_NOCS") == ""
	csMode       = int(envFloat("FOVEON_DN_CS", 2))
	csAbs        = float32(envFloat("FOVEON_DN_CSA", 0.0015))
	csA2         = float32(envFloat("FOVEON_DN_CSA2", 0.0010))
	csB2         = float32(envFloat("FOVEON_DN_CSB2", 0.0060))
	csResid      = int(envFloat("FOVEON_DN_CSR", 24))
	csLo         = float32(envFloat("FOVEON_DN_CSLO", 0.08))
	csHi         = float32(envFloat("FOVEON_DN_CSHI", 0.30))
	// darkBoost raises the wavelet threshold in the darkest tones
	// (FOVEON_DN_DARK, 1 = off) between local luminances darkLo and darkHi,
	// where the eye sees the coarse "pepper" and the read noise is largest.
	dnDark   = envFloat("FOVEON_DN_DARK", 2)
	dnDarkLo = envFloat("FOVEON_DN_DARKLO", 0.004)
	dnDarkHi = envFloat("FOVEON_DN_DARKHI", 0.006)
	// darkLev: the coarsest wavelet level (0 = 1 px) the boost applies to.
	// The pepper is the fine grain; boosting the coarse levels as well eats
	// the low-contrast lettering.
	darkLev = int(envFloat("FOVEON_DN_DARKLV", 3))
	// levFin scales the boost on the finest level only: the 1 px band is
	// where the books' texture lives, the pepper is coarser.
	levFin = envFloat("FOVEON_DN_LEVFIN", 0.25)
	// dnGarrote replaces the soft threshold with the non-negative garrote
	// (dĂ„â€šĂ˘â‚¬ĹˇÄ‚â€šĂ‚Â·(1-(t/d)Ă„â€šĂ˘â‚¬ĹˇÄ‚â€ąĂ˘â‚¬Ĺź) above the threshold): a strong edge coefficient is kept
	// almost unchanged instead of being biased down by t, while a
	// just-above-threshold coefficient (usually noise) is suppressed harder.
	// Measured but not enabled (task 21, 21 frames, fixed basis): slope
	// +0.5-4% came with residual +0.3-2.6% and noise +1-6%, i.e. no gain in
	// detail per unit of noise. FOVEON_DN_GARROTE=1 for the study.
	dnGarrote = os.Getenv("FOVEON_DN_GARROTE") != ""
	// noFimp keeps the denoiser's isolated luma impulses, for the before
	// crops of task 51 (FOVEON_DN_NOFIMP=1).
	noFimp = os.Getenv("FOVEON_DN_NOFIMP") != ""
)

// rejectLumaOut removes isolated luma impulses the denoiser would otherwise
// leave: a pixel whose luma is far outside the eight neighbours' luma, while
// those neighbours are themselves flat, is replaced by the per-layer 3x3
// median. It is not a median filter over the picture: a real edge, a thin
// line or a star lights its neighbours, so the neighbour span closes the
// gate. sig is the input luminance noise at the pixel, so the test follows
// the ISO. A dead or hot sensor sample in the layer that dominates the
// inverse-variance luma makes the luma collapse there; without this the
// multiplicative put-back spreads it into all three layers as a black or
// white speck.
const (
	fimpK    = 2.5 // threshold in the local luminance noise
	fimpSpan = 6.0 // the neighbours must span less than this
)

func rejectLumaOut(img *CamImage, sig []float32, w, h int) int {
	src := append([]float32(nil), img.Pix...)
	n := w * h
	l := make([]float32, n)
	parallel(n, func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			l[i] = 0.2126*src[i*3] + 0.7152*src[i*3+1] + 0.0722*src[i*3+2]
		}
	})
	var changed int
	parallel(h, func(y0, y1 int) {
		var a, ch [8]float32
		y0, y1 = max(y0, 1), min(y1, h-1)
		for y := y0; y < y1; y++ {
			for x := 1; x < w-1; x++ {
				i := y*w + x
				v := l[i]
				lo, hi := float32(math.Inf(1)), float32(math.Inf(-1))
				m := 0
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						if dx == 0 && dy == 0 {
							continue
						}
						q := l[i+dy*w+dx]
						a[m] = q
						m++
						lo, hi = min(lo, q), max(hi, q)
					}
				}
				s := sig[i]
				if s < 1e-6 {
					s = 1e-6
				}
				if hi-lo >= fimpSpan*s {
					continue
				}
				// An isolated dot is a local extreme: a pixel inside the
				// neighbours' range is structure, not a speck, and need not
				// be sorted.
				if v >= lo && v <= hi {
					continue
				}
				med := median8f32(&a)
				if d := v - med; d <= fimpK*s && d >= -fimpK*s {
					continue
				}
				for c := 0; c < 3; c++ {
					q := 0
					for dy := -1; dy <= 1; dy++ {
						for dx := -1; dx <= 1; dx++ {
							if dx == 0 && dy == 0 {
								continue
							}
							ch[q] = src[(i+dy*w+dx)*3+c]
							q++
						}
					}
					img.Pix[i*3+c] = median8f32(&ch)
				}
				changed++
			}
		}
	})
	return changed
}

// median8f32 sorts eight samples in place and returns their median (the mean
// of the middle two, as the impulse metric counts it).
func median8f32(a *[8]float32) float32 {
	s := a
	for i := 1; i < 8; i++ {
		v := s[i]
		j := i
		for j > 0 && s[j-1] > v {
			s[j] = s[j-1]
			j--
		}
		s[j] = v
	}
	return (s[3] + s[4]) / 2
}

// darkBoost is the per-pixel factor multiplying the luminance wavelet
// threshold: 1 above darkHi, dnDark at and below darkLo, smooth between.
// nil when no boost is asked for.
func darkBoost(local []float32, mult, lo, hi float64) []float32 {
	if mult <= 1 || hi <= lo {
		return nil
	}
	out := make([]float32, len(local))
	for i, v := range local {
		f := float64(v)
		switch {
		case f <= lo:
			out[i] = float32(mult)
		case f >= hi:
			out[i] = 1
		default:
			u := (f - lo) / (hi - lo)
			out[i] = float32(1 + (mult-1)*(1-u*u*(3-2*u)))
		}
	}
	return out
}

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
	// plane copies layer k into pooled scratch: six of these a call (three for
	// the noise models, one per layer for the guided filter) come to 53 MB of
	// garbage at 2.2 MP.
	plane := func(k int) []float32 {
		p := getPlane(n)
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
		putPlane(pk)
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
	// sigma(v) = sqrt(a + bĂ„â€šĂ˘â‚¬ĹˇÄ‚â€šĂ‚Â·v) written out rather than through noiseModel.at:
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
	var boost []float32
	if o.Quattro && o.ISO >= 800 {
		boost = darkBoost(local, dnDark, dnDarkLo, dnDarkHi)
	}
	Yd := atrous(Y, w, h, sig, dnK, 5, boost)
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
	if dnRadius > 0 {
		r = max(2, int(math.Round(dnRadius*scale)))
	}
	if debugNoise {
		fmt.Fprintf(os.Stderr, "denoise: wf %.9g %.9g %.9g mean %.9g eps %.9g r %d\n",
			wf[0], wf[1], wf[2], mean, eps, r)
	}
	// Task 43: one continuous joint-bilateral chroma filter at ISO >= 800,
	// with a wide support and weights from a heavily smoothed chroma estimate
	// (chromabilat.go). It replaces the wide guided filter and its task 41
	// gate; below ISO 800 the old path runs untouched.
	useCB := dnCB > 0 && o.ISO >= 800 && r > 2
	// A wide chroma filter averages each layer over a window larger than a
	// colour chart's patch, so a patch smaller than the window is pulled
	// towards its surroundings and pales (the dp2 Quattro chart at ISO 6400
	// lost a third of its chroma at radius 32). The wide filter's residual,
	// low-passed at a patch-scale radius, is added back where the pixel
	// holds real colour (measured on the layer chroma, so neutral grain is
	// not restored). That keeps the patch and leaves the wide filter's
	// mottle removal; FOVEON_DN_NOCS=1 renders without it.
	rFine := min(r, 8)
	csFix := chromaSmooth && csMode > 0 && o.Quattro && o.ISO >= 800 && r > rFine && !useCB
	rResid := max(2, int(math.Round(float64(csResid)*scale)))
	var outF [3][]float32
	var out [3][]float32
	if useCB {
		var pn [3][]float32
		var wg sync.WaitGroup
		for k := 0; k < 3; k++ {
			wg.Add(1)
			go func(k int) {
				defer wg.Done()
				p := plane(k)
				for i := range p {
					p[i] /= m
				}
				pn[k] = p
			}(k)
		}
		wg.Wait()
		// A clean chroma estimate for the wide filter's range weights: the
		// same luma-guided filter as before, whose output is smooth and
		// edge-aware. It is the guide only (dnCBGU=2), or also the base the
		// wide filter averages (dnCBGU=1).
		mI, mII := boxBlur(G, w, h, r), boxBlurSq(G, w, h, r)
		var gb [3][]float32
		var wgGuide sync.WaitGroup
		for k := 0; k < 3; k++ {
			wgGuide.Add(1)
			go func(k int) {
				defer wgGuide.Done()
				gb[k] = guidedPre(G, pn[k], w, h, r, eps, mI, mII)
			}(k)
		}
		wgGuide.Wait()
		putPlane(mI)
		putPlane(mII)
		base := gb
		baseOwned := false
		if rBase := int(math.Round(dnCBBaseR * scale)); rBase >= 2 && rBase != r {
			// A narrower guided filter is the base: less of its own bleed in
			// the neutral patches, and the wide bilateral over it still has
			// the clean r-radius estimate as its guide.
			mIB, mIIB := boxBlur(G, w, h, rBase), boxBlurSq(G, w, h, rBase)
			for k := 0; k < 3; k++ {
				base[k] = guidedPre(G, pn[k], w, h, rBase, eps, mIB, mIIB)
			}
			putPlane(mIB)
			putPlane(mIIB)
			baseOwned = true
		} else if dnCBGU != 1 {
			base = pn
		}
		out = chromaBilateral(base, gb, G, w, h, scale, rel)
		if baseOwned {
			for k := 0; k < 3; k++ {
				putPlane(base[k])
			}
		}
		if dnCBFineAmt > 0 {
			// A controlled amount of the original 0-4 px chroma detail, so
			// the wide average does not leave the thread and paper surfaces
			// waxy. It is the fine band only: the coarse clouds stay filtered.
			rf := max(1, int(math.Round(dnCBFine*scale)))
			for k := 0; k < 3; k++ {
				lo := boxBlur(pn[k], w, h, rf)
				for i := range out[k] {
					out[k][i] += float32(dnCBFineAmt) * (pn[k][i] - lo[i])
				}
				putPlane(lo)
			}
		}
		for k := 0; k < 3; k++ {
			putPlane(pn[k])
			putPlane(gb[k])
		}
	} else {
		// The guide does not change per layer: its blurs are shared.
		mI, mII := boxBlur(G, w, h, r), boxBlurSq(G, w, h, r)
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
				if csFix {
					if csMode == 2 {
						// The wide filter's residual, low-passed at a patch
						// radius: the colour it removed at the chart's scale,
						// with the 8-32 px grain largely smoothed out.
						res := getPlane(n)
						for i := range res {
							res[i] = p[i] - out[k][i]
						}
						outF[k] = boxBlur(res, w, h, rResid)
						putPlane(res)
					} else {
						outF[k] = guidedPre(G, p, w, h, rFine, eps, mI, mII)
					}
				}
				putPlane(p)
			}(k)
		}
		wg.Wait()
		putPlane(mI)
		putPlane(mII)
	}

	parallel(n, func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			a, b, c := out[0][i]*m, out[1][i]*m, out[2][i]*m
			if csFix && csMode == 2 {
				// The 16 px chroma residual is added back in proportion to
				// how saturated the pixel is, so a patch keeps its level and
				// near-neutral noise keeps the wide filter's smooth plane.
				hR, lR := max(out[0][i]+outF[0][i], max(out[1][i]+outF[1][i], out[2][i]+outF[2][i])),
					min(out[0][i]+outF[0][i], min(out[1][i]+outF[1][i], out[2][i]+outF[2][i]))
				t := (hR - lR) * m
				var alpha float32
				switch {
				case t >= csB2:
					alpha = 1
				case t > csA2:
					u := (t - csA2) / (csB2 - csA2)
					alpha = u * u * (3 - 2*u)
				}
				a += alpha * outF[0][i] * m
				b += alpha * outF[1][i] * m
				c += alpha * outF[2][i] * m
			} else if csFix {
				// Absolute layer chroma after the fine and the wide pass. A
				// patch keeps its level in the fine one; a large flat colour
				// keeps it in both. Where the wide pass removed a real step
				// (a patch smaller than its window) the fine pass takes over;
				// where the two agree the wide one's cleaner plane stays. The
				// gate keeps the fine plane out of near-neutral chroma, which
				// is noise.
				hF, lF := max(outF[0][i], max(outF[1][i], outF[2][i])), min(outF[0][i], min(outF[1][i], outF[2][i]))
				hW, lW := max(out[0][i], max(out[1][i], out[2][i])), min(out[0][i], min(out[1][i], out[2][i]))
				tF, tW := (hF-lF)*m, (hW-lW)*m
				var ww float32 = 1
				if tF >= csAbs {
					loss := (tF - tW) / tF
					switch {
					case loss >= csHi:
						ww = 0
					case loss > csLo:
						u := (loss - csLo) / (csHi - csLo)
						ww = 1 - u*u*(3-2*u)
					}
				}
				a = ww*a + (1-ww)*outF[0][i]*m
				b = ww*b + (1-ww)*outF[1][i]*m
				c = ww*c + (1-ww)*outF[2][i]*m
			}
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
	ramp := 0.0
	if o.NoiseAware {
		ramp = restoreRamp(rel, o.ISO)
	}
	if !noRestore {
		if perLayerRestore {
			restoreDetail(img, orig, rel, o.Strength)
			lap("restore detail")
		} else {
			if !noLumaRestore {
				restoreLuma(img, orig, wf, gw, rel, o.Strength, sig, ramp)
				lap("restore luma")
			}
			if !noChromaRestore {
				restoreChroma(img, orig, wf, models, ramp)
				lap("restore chroma")
			}
		}
	}
	// The last step: a narrow impulse reject on the finished layers. The
	// wavelet keeps a one-pixel luma outlier (it is indistinguishable from a
	// real point detail), and the luma put-back then paints it into all
	// three layers. At high ISO those specks are what the eye sees on a flat
	// wall or a grey card. Low ISO is untouched (the gate keeps the base
	// frame bit-identical), and so is anything whose neighbours show real
	// structure.
	if !noFimp && o.ISO >= 800 {
		if n := rejectLumaOut(img, sig, w, h); debugNoise {
			fmt.Fprintf(os.Stderr, "denoise: luma impulse reject %d pixels\n", n)
		}
	}
	lap("impulse reject")
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
	// FOVEON_DN_NOLUMA / FOVEON_DN_NOCHROMA split the restore, so the luma
	// and chroma halves can be measured on their own. Measured on the fixed
	// basis (DP1 Merrill ladder, task 14): the four variants agree on 0-1 px
	// detail to 0.0003 at every ISO, and the luma half adds no detail; but
	// spp-check needs both halves (183 photos, ALL 7.064 both, 7.165 luma
	// only, 7.207 chroma only; 1/16/32 photos worse by >0.30). So the luma
	// restore stays: it buys no detail, but removing it is not free either.
	noLumaRestore   = os.Getenv("FOVEON_DN_NOLUMA") != ""
	noChromaRestore = os.Getenv("FOVEON_DN_NOCHROMA") != ""
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
		b1, b2, b4, b8 := boxBlur(d, w, h, 1), boxBlur(d, w, h, 2), boxBlur(d, w, h, 4), boxBlur(d, w, h, 8)
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
//
// restoreRamp is how far into the noisy-restore regime a frame is: 0 at ISO
// 400 and below (the restore behaves as it always did, so low ISO is
// untouched) and 1 by ISO 800 on a frame whose relative luminance noise is
// high enough. The relative-noise thresholds are measured on the DP1
// Merrill studio ladder: 0.019 at ISO 200 and 0.028 at ISO 400, 0.044 at
// ISO 800 and 0.067 at ISO 1600. Both gates are smooth so a body's ISO
// steps do not jump, and the ISO gate keeps a synthetic or dark low-ISO
// frame (whose measured relative noise can be high) on the old path.
func restoreRamp(rel, iso float64) float64 {
	ir := math.Max(0, math.Min(1, (iso-400)/400))
	nr := (rel - 0.030) / (0.045 - 0.030)
	nr = math.Max(0, math.Min(1, nr))
	return ir * nr * nr * (3 - 2*nr)
}

// dnLT (research switch FOVEON_DN_LT) makes the restore noise-aware: each
// band is soft-thresholded at dnLT times its own noise before it is given
// back, so a real edge returns and the part of the residual that is only
// noise does not. Without it the restore gives back (1-strength) of the
// whole residual, and at NR Low (strength 0.3) that put most of the noise
// back (DP1 Merrill studio ISO 6400 vs SPP: luma 15.1 against 11.1).
var dnLT = envFloat("FOVEON_DN_LT", 2.0)

// dnGrain is the fraction of the un-denoised 0-1 px luminance residual that
// is given back as a linear (unthresholded) blend on the noise-aware high-ISO
// path. The noise-aware restore soft-thresholds each band, which keeps real
// edges but removes the body of the fine noise and leaves only its >2 sigma
// tail: a smooth field with isolated dark/light residuals. A linear blend
// scales the tail with the body, so what remains is even, fine grain. Zero
// keeps the old path (research switch FOVEON_DN_GRAIN).
var dnGrain = envFloat("FOVEON_DN_GRAIN", 0.7)

func restoreLuma(img *CamImage, orig []float32, wf, gw [3]float32, rel, strength float64, sig []float32, ramp float64) {
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
	b1, b2, b4, b8 := boxBlur(d, w, h, 1), boxBlur(d, w, h, 2), boxBlur(d, w, h, 4), boxBlur(d, w, h, 8)
	// Each band's noise relative to the pixel noise: the finer box's.
	fr := [5]float32{1, 1.0 / 3, 1.0 / 5, 1.0 / 9, 1.0 / 17}
	lt := float32(dnLT)
	r := float32(ramp)
	// The linear blend follows the NR strength the way the old per-band
	// restore did: at full strength there is no blend and the old
	// soft-thresholded band runs; at the default (0.3) the blend is 0.7 of
	// dnGrain.
	g := float32(dnGrain) * rest
	parallel(n, func(a, b int) {
		for i := a; i < b; i++ {
			fine := d[i] - b1[i]
			vOld := wb[0]*fine + wb[1]*(b1[i]-b2[i]) + wb[2]*(b2[i]-b4[i]) + wb[3]*(b4[i]-b8[i]) + rest*b8[i]
			v := vOld
			if lt > 0 && r > 0 {
				t := lt * sig[i]
				// The finest band is the fine grain. A linear blend keeps its
				// body and scales its tail; the thresholded form would keep
				// only the tail and leave the field around it flat.
				vFine := wb[0] * softThreshold(fine, t*fr[0])
				if g > 0 {
					vFine = g * fine
				}
				vNew := vFine + wb[1]*softThreshold(b1[i]-b2[i], t*fr[1]) +
					wb[2]*softThreshold(b2[i]-b4[i], t*fr[2]) + wb[3]*softThreshold(b4[i]-b8[i], t*fr[3]) +
					rest*softThreshold(b8[i], t*fr[4])
				v = vOld*(1-r) + vNew*r
			}
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
// the colour noise back (DP2 Merrill ISO 6400, dE2000 8.3 -> 18.1). It does
// follow the frame's noise through restoreRamp: 3 on a clean frame (as
// before) and dnCT on a noisy one, where a coherent colour error of 10-30 px
// is only 1-2 sigma at any one pixel but is exactly what the restore was
// putting back. DP1 Merrill studio, NR Low: ISO 1600 chroma 2.25 -> 0.95
// dE, ISO 6400 4.99 -> 2.96, while retained luminance detail is unchanged.
var dnCT = envFloat("FOVEON_DN_CT", 6.0)

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
func restoreChroma(img *CamImage, orig []float32, wf [3]float32, models [3]noiseModel, ramp float64) {
	w, h, n := img.W, img.H, img.W*img.H
	r := float32(ramp)
	// On a clean frame keep the old threshold (3 sigma); on a noisy one
	// raise it to dnCT, because a blotch is coherent over 10-30 px and only
	// 1-2 sigma at each pixel.
	t := 3*(1-r) + float32(dnCT)*r
	if debugNoise && r > 0 {
		fmt.Fprintf(os.Stderr, "denoise: chroma restore threshold %.2f sigma (ramp %.2f)\n", t, r)
	}
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
		// The five blurs read c and p only: run them together (on a
		// preview each is short, and one after another they idled cores).
		var lvl []float32
		var b1, b2, b4, b8 []float32
		var bw sync.WaitGroup
		bw.Add(5)
		go func() { defer bw.Done(); lvl = boxBlur(p, w, h, 2) }()
		go func() { defer bw.Done(); b1 = boxBlur(c, w, h, 1) }()
		go func() { defer bw.Done(); b2 = boxBlur(c, w, h, 2) }()
		go func() { defer bw.Done(); b4 = boxBlur(c, w, h, 4) }()
		go func() { defer bw.Done(); b8 = boxBlur(c, w, h, 8) }()
		bw.Wait()
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

// atrous soft-thresholds an Ä‚â€žĂ˘â‚¬ĹˇÄ‚â€šĂ‚Â -trous B3-spline wavelet decomposition at k
// times each level's noise (sigma: the plane's noise at each pixel).
func atrous(p []float32, w, h int, sigma []float32, k float64, levels int, boost []float32) []float32 {
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
				if boost != nil && l < darkLev {
					b := boost[i]
					if l == 0 {
						b = 1 + (b-1)*float32(levFin)
					}
					t *= b
				}
				d := cur[i] - next[i]
				if dnGarrote {
					if ad := d; ad < 0 {
						ad = -ad
						if ad > t {
							d = -ad * (1 - (t/ad)*(t/ad))
						} else {
							d = 0
						}
					} else if ad > t {
						d = ad * (1 - (t/ad)*(t/ad))
					} else {
						d = 0
					}
				} else {
					switch {
					case d > t:
						d -= t
					case d < -t:
						d += t
					default:
						d = 0
					}
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
// sigma(v) = sqrt(a + bĂ„â€šĂ˘â‚¬ĹˇÄ‚â€šĂ‚Â·v) (read noise plus shot noise).
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

// estimateNoise fits the noise model on 16Ä‚â€žĂ˘â‚¬ĹˇÄ‚ËĂ˘â€šÂ¬Ă˘â‚¬ĹĄ16 blocks: each block's noise is
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
				// Laplacian residual of white noise: variance 1.25 sigmaĂ„â€šĂ˘â‚¬ĹˇÄ‚â€ąĂ˘â‚¬Ĺź.
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
	// Least squares var = a + bĂ„â€šĂ˘â‚¬ĹˇÄ‚â€šĂ‚Â·v, b >= 0.
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

// greyWeights is grey (DenoiseOptions.Grey) scaled so that wfĂ„â€šĂ˘â‚¬ĹˇÄ‚â€šĂ‚Â·gw = 1: adding
// dĂ„â€šĂ˘â‚¬ĹˇÄ‚â€šĂ‚Â·gw changes the weighted luminance by exactly d. A zero grey gives equal
// layers (wf sums to 1).
func greyWeights(grey [3]float64, wf [3]float32) [3]float32 {
	y := float64(wf[0])*grey[0] + float64(wf[1])*grey[1] + float64(wf[2])*grey[2]
	if grey[0] <= 0 || grey[1] <= 0 || grey[2] <= 0 || y <= 0 {
		return [3]float32{1, 1, 1}
	}
	return [3]float32{float32(grey[0] / y), float32(grey[1] / y), float32(grey[2] / y)}
}
