package render

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

// SmoothShadowChroma removes low-frequency colour noise from the shadows of
// a linear sRGB image, in place. fullW is the width of the full-resolution
// frame l was made from (the filter covers the same scene area at every
// size); amount (0-1) scales the effect.
func SmoothShadowChroma(l *Linear, fullW int, amount float64) {
	if amount <= 0 || l.W < 8 || l.H < 8 {
		return
	}
	w, h, n := l.W, l.H, l.W*l.H
	sigma := shadowChromaSigma * float64(w) / float64(max(fullW, w))
	sigma = max(sigma, 1)
	Y := make([]float32, n)
	G := make([]float32, n) // the guide: encoded luminance
	any := false
	for i := 0; i < n; i++ {
		y := 0.2126*l.Pix[i*3] + 0.7152*l.Pix[i*3+1] + 0.0722*l.Pix[i*3+2]
		Y[i] = y
		G[i] = enc(max(0, y))
		if y < shadowChromaHi {
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
			t := (shadowChromaHi - Y[i]) / (shadowChromaHi - shadowChromaLo)
			if t <= 0 {
				continue
			}
			t = min(t, 1) * a
			yy := Y[i] + 1e-4
			for k := 0; k < 3; k++ {
				v := l.Pix[i*3+k]
				l.Pix[i*3+k] = v*(1-t) + ratio[k][i]*yy*t
			}
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
