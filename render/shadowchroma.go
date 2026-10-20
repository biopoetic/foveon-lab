package render

import (
	"math"
	"sort"
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
	// shadowChromaSigma is the filter's scale at full sensor resolution, px.
	shadowChromaSigma = 16
	shadowChromaEps   = 1e-3 // guide variance (encoded luminance) kept as edges
)

// Measured with the noise-adaptive strength and muting below, neutral
// render vs SPP, 13 SD1/SD15 photos: SDIM0432 red share 10.7% -> 3.7% (SPP
// 2.7%), mean 8.24 -> 8.14, shadows 6.17 -> 6.01, no photo worse by more
// than 0.11. Clean photos (noise below shadowNoiseLo) are no longer touched.
//
// Noise-adaptive (20 October 2026): SPP mutes shadow colour in proportion to
// how noisy the shadows are (SPP/our shadow chroma against ShadowChromaNoise
// over ten SD1 photos: correlation -0.63), and keeps clean dark greens
// colourful. So the strength follows the photo's measured shadow colour
// noise: none up to shadowNoiseLo, full from shadowNoiseHi, reaching
// brighter tones and a wider area as it grows.
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
// size); amount (0-1, ShadowChromaAmount) scales the effect, its tonal
// reach and its radius.
func SmoothShadowChroma(l *Linear, fullW int, amount float64) {
	if amount <= 0 || l.W < 8 || l.H < 8 {
		return
	}
	w, h, n := l.W, l.H, l.W*l.H
	sigma := shadowChromaSigma * (1 + 2*amount) * float64(w) / float64(max(fullW, w))
	sigma = max(sigma, 1)
	hi := float32(shadowChromaHi + 0.10*amount)
	Y := make([]float32, n)
	G := make([]float32, n) // the guide: encoded luminance
	any := false
	for i := 0; i < n; i++ {
		y := 0.2126*l.Pix[i*3] + 0.7152*l.Pix[i*3+1] + 0.0722*l.Pix[i*3+2]
		Y[i] = y
		G[i] = enc(max(0, y))
		if y < hi {
			any = true
		}
	}
	if !any {
		return
	}
	var ratio [3][]float32
	for k := 0; k < 3; k++ {
		c := make([]float32, n)
		for i := 0; i < n; i++ {
			c[i] = l.Pix[i*3+k] / (Y[i] + 1e-4)
		}
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
			// SPP also mutes noisy shadows (their colour is mostly noise):
			// down to shadowMute of the colour at full strength.
			keep := 1 - shadowMute*t
			for k := 0; k < 3; k++ {
				v := l.Pix[i*3+k]
				v = v*(1-t) + ratio[k][i]*yy*t
				l.Pix[i*3+k] = Y[i] + (v-Y[i])*keep
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
	for i := range I {
		ip[i] = I[i] * p[i]
		ii[i] = I[i] * I[i]
	}
	mI, mp := GaussBlur(I, w, h, sigma), GaussBlur(p, w, h, sigma)
	mIp, mII := GaussBlur(ip, w, h, sigma), GaussBlur(ii, w, h, sigma)
	a, b := ip, ii // reuse
	for i := 0; i < n; i++ {
		a[i] = (mIp[i] - mI[i]*mp[i]) / (mII[i] - mI[i]*mI[i] + float32(eps))
		b[i] = mp[i] - a[i]*mI[i]
	}
	ma, mb := GaussBlur(a, w, h, sigma), GaussBlur(b, w, h, sigma)
	for i := 0; i < n; i++ {
		ma[i] = ma[i]*I[i] + mb[i]
	}
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
