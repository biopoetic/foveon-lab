package render

import (
	"math"
	"sort"
	"sync/atomic"
)

// Shadow colour noise. A Foveon sensor's colour noise in the shadows is
// low-frequency: magenta and red blotches over dark greens and greys, the
// bottom layer's noise lifted by the colour matrix. The per-layer denoiser
// (x3f.Denoise) smooths each layer's level and misses them; Sigma Photo Pro
// and x3fuse's NLM remove them. On SDIM0432 (SD1, ISO 200, a dark forest
// bank) the share of red-dominant shadow pixels was 14.1% here, 0.5% in SPP,
// 0.0% in the camera JPEG; x3f_extract shows 14.9% without its NR, 0.4% at
// NR 3.
//
// The fix smooths colour, not brightness: each channel's ratio to the
// luminance, with an edge-aware (guided) filter steered by the encoded
// luminance, so colour stays inside its objects. It fades in only where
// the signal is low (linear luminance below shadowChromaHi, full below
// shadowChromaLo); above that nothing changes. Prototyped on x3f_extract's
// un-denoised output: red share 14.9% -> 0.0-0.2%. In the app, neutral
// render against SPP's neutral export on ten photos (SD1, SD15): SDIM0432
// 17.03 -> 16.01 (shadows 11.05 -> 9.67, red share 17.5% -> 10.7%); the
// mean of all ten 9.79 -> 9.71 (shadows 7.25 -> 7.16); no photo worse by
// more than 0.13. The 0.02-0.12 range removed more red on SDIM0432 but cost
// up to 0.32 elsewhere. What is left there is a purple cast in the deepest
// shadows; keeping the development's hue under the mapping fixed it on
// that photo but made shadows worse on eight of the ten (7.20 -> 9.18).

const (
	shadowChromaLo = 0.01 // linear luminance: full smoothing below
	shadowChromaHi = 0.06 // none above
	// shadowChromaSigma is the filter's scale at full sensor resolution, px
	// (16 until v0.4.49: it spread neighbours' colour into small dark
	// greens such as moss on rocks).
	shadowChromaSigma = 4
	shadowChromaEps   = 1e-3 // guide variance (encoded luminance) kept as edges
)

// Measured with the noise-adaptive strength and muting below, neutral
// render vs SPP, 13 SD1/SD15 photos: SDIM0432 red share 10.7% -> 3.7% (SPP
// 2.7%), mean 8.24 -> 8.14, shadows 6.17 -> 6.01, no photo worse by more
// than 0.11. Clean photos (noise below shadowNoiseLo) are no longer touched.
//
// Magenta-side muting and the tighter filter (v0.4.50), with the denoiser's
// tighter colour radius at low ISO (x3f colourRadius): SDIM0432's moss, as
// area colour against SPP's, greenness (G above the mean of R and B, 8-bit)
// 0 -> 4.9 (SPP 11.7, no NR 6.9), magenta share 0.3% -> 1.2% (SPP 2.7%);
// 13 photos 7.56 -> 7.37, none worse.
//
// Noise-adaptive (20 October 2026): SPP mutes shadow colour in proportion to
// how noisy the shadows are (SPP/our shadow chroma against ShadowChromaNoise
// over ten SD1 photos: correlation -0.63), and keeps clean dark greens
// colourful. So the strength follows the photo's measured shadow colour
// noise: none up to shadowNoiseLo, full from shadowNoiseHi, and the filter
// covers a wider area as it grows.
//
// The tonal reach used to grow with the noise too (hi = 0.06 + 0.10·amount,
// up to 0.16 linear, sRGB ~112). At high ISO, where amount saturates, that
// reaches real midtone colour: on the DP1 Merrill studio series it
// desaturated the bottles and cloth, while smoothing and muting the 0-0.06
// shadows kept their benefit. Measured with noisebench against each frame's
// own ISO 100 (colour only blurred; growing is the old reach, fixed the
// constant 0.06):
//
//	ISO   colour, growing / fixed   detail kept, growing / fixed
//	 800  2.98 / 2.93               0.99 / 0.99
//	1600  4.66 / 4.46               0.83 / 0.83
//	3200  7.02 / 6.31               0.74 / 0.73
//	6400  10.91 / 9.89              0.64 / 0.65
//
// spp-check DPM over 32 photos: 6.846 -> 6.835; all 172 photos: 7.229 ->
// 7.206 (Quattro 6.053 -> 5.945, SD10 8.826 -> 8.888). So the reach stays
// at the deep shadows at every strength; only the radius grows.
//
// 31 October 2026 (task 14): the plane that is smoothed is the channel's
// chroma relative to max(Y, shadowChromaLo), not relative to Y as before.
// The old form was unbounded next to black, where all three channels are
// camera noise: a near-black pixel's ratio could be hundreds, and the guided
// filter spread it over its window, so a 95x93 px patch on DP1 Merrill ISO
// 6400's fan edge turned red or cyan depending on the sign of a neighbour's
// noise. The floor bounds the plane by the chroma the shadow actually has;
// above shadowChromaLo it is algebraically the old filter.
const (
	shadowNoiseLo = 0.02
	shadowNoiseHi = 0.10
	// shadowMute is the share of shadow colour removed at full strength:
	// SPP keeps ~0.5 of ours on the noisiest photo (SDIM0432, 0.53).
	shadowMute = 0.5
)

// ShadowChromaAmount is the smoothing strength (0-1) for a measured shadow
// colour noise (ShadowChromaNoise).
func ShadowChromaAmount(noise float64) float64 {
	return math.Max(0, math.Min(1, (noise-shadowNoiseLo)/(shadowNoiseHi-shadowNoiseLo)))
}

// SmoothShadowChroma removes low-frequency colour noise from the shadows of
// a linear sRGB image, in place. fullW is the width of the full-resolution
// frame l was made from (the filter covers the same scene area at every
// size); amount (0-1, ShadowChromaAmount) scales the effect and its radius.
func SmoothShadowChroma(l *Linear, fullW int, amount float64) {
	if amount <= 0 || l.W < 8 || l.H < 8 {
		return
	}
	w, h, n := l.W, l.H, l.W*l.H
	sigma := shadowChromaSigma * (1 + 2*amount) * float64(w) / float64(max(fullW, w))
	sigma = max(sigma, 1)
	hi := float32(shadowChromaHi)
	Y := make([]float32, n)
	G := make([]float32, n) // the guide: encoded luminance
	var any atomic.Bool
	parallelRows(n, func(i0, i1 int) {
		found := false
		for i := i0; i < i1; i++ {
			y := 0.2126*l.Pix[i*3] + 0.7152*l.Pix[i*3+1] + 0.0722*l.Pix[i*3+2]
			Y[i] = y
			G[i] = enc(max(0, y))
			found = found || y < hi
		}
		if found {
			any.Store(true)
		}
	})
	if !any.Load() {
		return
	}
	// The plane that is smoothed is each channel's chroma relative to the
	// luminance, but relative to max(Y, shadowChromaLo), not to Y itself.
	// Dividing by Y made the plane explode next to black: at DP1 Merrill ISO
	// 6400 a near-black pixel (linear Y ~1e-4, all three channels camera
	// noise) has ratios of hundreds, and the guided filter spread that ratio
	// over its ~10 px window, painting a saturated red/cyan patch on a
	// neighbouring pixel at Y = 0.026 (the fan's edge). Whether that pixel's
	// noise was positive or negative decided the patch's sign, so a handful
	// of despeckled samples elsewhere flipped it from red to cyan. With the
	// floor, the plane is bounded by the chroma the shadow actually has and a
	// black neighbour contributes almost nothing; above the floor this is
	// exactly the old Y-relative chroma.
	// The floor is for the high-ISO regime the patch came from. Below ISO
	// 800 the old ratio (and its exact arithmetic) stays, so base ISO output
	// is bit-identical; the two paths only differ where a window reaches a
	// near-black pixel, which at low ISO is a few pixels of sensor noise.
	legacy := l.ISO < 800
	floor := float32(shadowChromaLo)
	var ratio [3][]float32
	for k := 0; k < 3; k++ {
		c := make([]float32, n)
		parallelRows(n, func(i0, i1 int) {
			for i := i0; i < i1; i++ {
				if legacy {
					c[i] = l.Pix[i*3+k] / (Y[i] + 1e-4)
				} else {
					c[i] = (l.Pix[i*3+k] - Y[i]) / max(Y[i], floor)
				}
			}
		})
		ratio[k] = guidedGauss(G, c, w, h, sigma, shadowChromaEps)
	}
	a := float32(amount)
	parallelRows(h, func(y0, y1 int) {
		for i := y0 * w; i < y1*w; i++ {
			t := (hi - Y[i]) / (hi - shadowChromaLo)
			if t <= 0 {
				continue
			}
			t = min(t, 1) * a
			yy := Y[i] + 1e-4
			if !legacy {
				yy = max(Y[i], floor)
			}
			var v [3]float32
			for k := 0; k < 3; k++ {
				if legacy {
					v[k] = l.Pix[i*3+k]*(1-t) + ratio[k][i]*yy*t
				} else {
					v[k] = l.Pix[i*3+k]*(1-t) + (Y[i]+ratio[k][i]*yy)*t
				}
			}
			// SPP also mutes noisy shadows, but only their magenta side
			// (green below the luminance): dark greens stay green.
			keep := float32(1)
			if v[1] < Y[i] {
				keep = 1 - shadowMute*t
			}
			for k := 0; k < 3; k++ {
				l.Pix[i*3+k] = Y[i] + (v[k]-Y[i])*keep
			}
		}
	})
}

// ShadowChromaFix measures l's shadow colour noise and removes it
// (SmoothShadowChroma at ShadowChromaAmount), returning the noise. With
// colour set, l holds the sensor's layers and colour is the photo's
// colour matrix: the fix runs on the colour image it would make and maps
// back, so a colour film (which develops the layers) gets exactly the
// neutral render's fix — without it, SDIM0432's forest shadows came out
// magenta again under every colour film.
func ShadowChromaFix(l *Linear, fullW int, colour *[9]float64) float64 {
	var inv [9]float64
	if colour != nil {
		var ok bool
		if inv, ok = invert3(*colour); !ok {
			return 0
		}
		mulPixels(l, *colour)
	}
	noise := ShadowChromaNoise(l)
	SmoothShadowChroma(l, fullW, ShadowChromaAmount(noise))
	if colour != nil {
		mulPixels(l, inv)
	}
	return noise
}

// mulPixels applies the 3×3 matrix m to every pixel of l.
func mulPixels(l *Linear, m [9]float64) {
	var f [9]float32
	for i, v := range m {
		f[i] = float32(v)
	}
	parallelRows(l.H, func(y0, y1 int) {
		for i := y0 * l.W; i < y1*l.W; i++ {
			r, g, b := l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2]
			l.Pix[i*3] = f[0]*r + f[1]*g + f[2]*b
			l.Pix[i*3+1] = f[3]*r + f[4]*g + f[5]*b
			l.Pix[i*3+2] = f[6]*r + f[7]*g + f[8]*b
		}
	})
}

// guidedGauss is a guided filter (He et al.) with Gaussian windows: p
// smoothed where the guide I is flat, kept where I has edges.
func guidedGauss(I, p []float32, w, h int, sigma, eps float64) []float32 {
	n := len(I)
	ip := make([]float32, n)
	ii := make([]float32, n)
	parallelRows(n, func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			ip[i] = I[i] * p[i]
			ii[i] = I[i] * I[i]
		}
	})
	mI, mII := GaussBlur(I, w, h, sigma), GaussBlur(ii, w, h, sigma)
	return guidedGaussPre(I, p, w, h, sigma, eps, mI, mII, ip)
}

// guidedGaussPre is guidedGauss with the guide's statistics and I*p already
// computed: the guide's blurs are shared instead of rebuilt.
func guidedGaussPre(I, p []float32, w, h int, sigma, eps float64, mI, mII, ip []float32) []float32 {
	n := len(I)
	mp := GaussBlur(p, w, h, sigma)
	mIp := GaussBlur(ip, w, h, sigma)
	a, b := ip, make([]float32, n)
	parallelRows(n, func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			// He et al.'s 0 <= a <= 1 is what makes this an edge-preserving
			// filter. A colour-ratio plane does vary with brightness, so a
			// wider bound can be argued, but it must be a bound: left free, a
			// near-clipped highlight beside deep shadow produced coefficients
			// of tens and painted a saturated red/cyan patch at the tonal
			// boundary (DP1 Merrill ISO 6400 beside the clipped lamp; also on
			// dp2 Quattro). Bound it to [0,1].
			mi, mpf := float64(mI[i]), float64(mp[i])
			varI := float64(mII[i]) - mi*mi
			ai := 0.0
			if varI > 0.5*float64(eps) {
				ai = (float64(mIp[i]) - mi*mpf) / (varI + float64(eps))
				if ai > 1 {
					ai = 1
				}
				if ai < 0 {
					ai = 0
				}
			}
			a[i] = float32(ai)
			b[i] = mp[i] - a[i]*mI[i]
		}
	})
	ma, mb := GaussBlur(a, w, h, sigma), GaussBlur(b, w, h, sigma)
	parallelRows(n, func(i0, i1 int) {
		for i := i0; i < i1; i++ {
			ma[i] = ma[i]*I[i] + mb[i]
		}
	})
	return ma
}

// ShadowChromaNoise measures l's colour noise in the shadows: the median,
// over pixels of linear luminance 0.005-0.04, of the fine-scale part (box
// radius ~3 px at this size) of each pixel's chroma relative to its
// luminance. Clean dark greens read ~0.02-0.03, a noisy ISO 200 forest
// shadow ~0.18 (render scale, SDIM0432).
func ShadowChromaNoise(l *Linear) float64 {
	w, h, n := l.W, l.H, l.W*l.H
	var rel [3][]float32
	Y := make([]float32, n)
	for i := 0; i < n; i++ {
		Y[i] = 0.2126*l.Pix[i*3] + 0.7152*l.Pix[i*3+1] + 0.0722*l.Pix[i*3+2]
	}
	for k := 0; k < 3; k++ {
		rel[k] = make([]float32, n)
		for i := 0; i < n; i++ {
			rel[k][i] = (l.Pix[i*3+k] - Y[i]) / (Y[i] + 1e-3)
		}
	}
	var blur [3][]float32
	for k := 0; k < 3; k++ {
		blur[k] = GaussBlur(rel[k], w, h, 3)
	}
	var v []float64
	for i := 0; i < n; i += 3 {
		if Y[i] < 0.005 || Y[i] > 0.04 {
			continue
		}
		var s float64
		for k := 0; k < 3; k++ {
			d := float64(rel[k][i] - blur[k][i])
			s += d * d
		}
		v = append(v, math.Sqrt(s))
	}
	if len(v) < 500 {
		return 0
	}
	sort.Float64s(v)
	return v[len(v)/2]
}
