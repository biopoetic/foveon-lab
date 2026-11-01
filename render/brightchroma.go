package render

// Bright near-neutral chroma denoise: SPP's chroma noise reduction removes a
// low-frequency colour mottling in bright skies and clouds that the per-pixel
// Foveon denoiser leaves. On the SD14 bridge sky the 8-48 px a*/b* spread is
// 0.67/0.84 for our render against SPP's 0.63/0.51; the mottling is already in
// the denoised raw data (removing the denoiser changes it by 0.01) and the
// per-channel curves are not its source (a luminance-only tone curve leaves
// 0.77/1.05). This pass low-passes only chroma, only on bright, near-neutral,
// locally flat pixels, and keeps each pixel's luminance: it removes the
// patches without desaturating the local colour (the Max Detail grey pull did
// the latter, which treated the symptom).

import "math"

// brightChromaRadius: fraction of the image width covered by the chroma
// low-pass (0.021 = 25 px at 1200, the radius whose 8-48 px spread matches
// SPP's on the SD14 bridge sky).
const brightChromaRadius = 0.021

// BrightChromaDenoiseFor is BrightChromaDenoise for the camera groups whose
// SPP neutral export has visibly less bright near-neutral chroma mottling
// than our denoiser leaves: the TRUEII bodies and SD9/SD10. On the Merrill
// and Quattro groups the fitted denoiser already matches SPP there, and the
// pass cost them on spp-check (DPM ISO 1600-6400 +0.01..0.07, task 21).
func BrightChromaDenoiseFor(l *Linear, group string) {
	switch group {
	case "SD14", "SD15", "DP", "SD9", "SD10":
		BrightChromaDenoise(l)
	}
}

// BrightChromaDenoise smooths low-frequency chroma mottling in place. It is
// meant for a developed linear image (preview or export) before the preset.
func BrightChromaDenoise(l *Linear) {
	if l == nil || l.W < 64 || l.H < 64 {
		return
	}
	w, h, n := l.W, l.H, l.W*l.H
	r := int(math.Round(brightChromaRadius * float64(w)))
	r = max(4, min(r, 64))
	y := make([]float32, n)
	wt := make([]float32, n)
	var sel int
	for i := 0; i < n; i++ {
		rr, gg, bb := float64(l.Pix[i*3]), float64(l.Pix[i*3+1]), float64(l.Pix[i*3+2])
		yy := 0.2126*rr + 0.7152*gg + 0.0722*bb
		y[i] = float32(yy)
		// Bright, not clipped, low CIELAB chroma.
		if yy < 0.40 || yy > 0.999 {
			continue
		}
		x := 0.4124*rr + 0.3576*gg + 0.1805*bb
		z := 0.0193*rr + 0.1192*gg + 0.9505*bb
		f := func(v float64) float64 {
			if v > 216.0/24389 {
				return math.Cbrt(v)
			}
			return (24389.0/27*v + 16) / 116
		}
		fx, fy, fz := f(x/0.9505), f(yy/1.0), f(z/1.089)
		a, b := 500*(fx-fy), 200*(fy-fz)
		c := math.Hypot(a, b)
		gw := smoothstep(0.40, 0.48, yy) * (1 - smoothstep(0.96, 1.0, yy))
		cw := 1 - smoothstep(8, 14, c)
		if gw*cw <= 0 {
			continue
		}
		wt[i] = float32(gw * cw)
		sel++
	}
	if sel < n/200 {
		return
	}
	// Flatness gate: a local luminance step (a wall against sky) must keep
	// its colour. The gradient is measured on a small blur so cloud texture
	// does not close the gate.
	lo := bcBox(y, w, h, 3)
	wf := make([]float32, n)
	for i := 0; i < n; i++ {
		if wt[i] == 0 {
			continue
		}
		g := math.Abs(float64(y[i]) - float64(lo[i]))
		flat := 1 - smoothstep(0.010, 0.035, g)
		wt[i] *= float32(flat)
		wf[i] = wt[i]
	}
	// Local mean of the selected pixels, per channel, at radius r.
	blur := [3][]float32{}
	for k := 0; k < 3; k++ {
		p := make([]float32, n)
		for i := 0; i < n; i++ {
			p[i] = l.Pix[i*3+k] * wt[i]
		}
		blur[k] = bcBox(p, w, h, r)
	}
	bw := bcBox(wf, w, h, r)
	for i := 0; i < n; i++ {
		if wt[i] <= 0 || bw[i] <= 1e-6 {
			continue
		}
		var avg [3]float64
		for k := 0; k < 3; k++ {
			avg[k] = float64(blur[k][i] / bw[i])
		}
		yb := 0.2126*avg[0] + 0.7152*avg[1] + 0.0722*avg[2]
		if yb <= 1e-7 {
			continue
		}
		// Keep this pixel's luminance; take the neighbourhood's chroma.
		sc := float64(y[i]) / yb
		alpha := float64(wt[i])
		for k := 0; k < 3; k++ {
			target := avg[k] * sc
			v := float64(l.Pix[i*3+k])
			l.Pix[i*3+k] = float32(v + alpha*(target-v))
		}
	}
}

// bcBox is a centred box blur with a running sum, edges with a smaller
// window (same convention as boxBlur but O(n) instead of O(n*r)).
func bcBox(src []float32, w, h, r int) []float32 {
	tmp := make([]float32, len(src))
	for yy := 0; yy < h; yy++ {
		row := src[yy*w : (yy+1)*w]
		o := tmp[yy*w : (yy+1)*w]
		var s float32
		// Window [0, min(r,w-1)] at x=0.
		for k := 0; k <= min(r, w-1); k++ {
			s += row[k]
		}
		for x := 0; x < w; x++ {
			if x > 0 {
				if x+r < w {
					s += row[x+r]
				}
				if x-r-1 >= 0 {
					s -= row[x-r-1]
				}
			}
			lo, hi := max(0, x-r), min(w-1, x+r)
			o[x] = s / float32(hi-lo+1)
		}
	}
	out := make([]float32, len(src))
	for x := 0; x < w; x++ {
		var s float32
		for k := 0; k <= min(r, h-1); k++ {
			s += tmp[k*w+x]
		}
		for y := 0; y < h; y++ {
			if y > 0 {
				if y+r < h {
					s += tmp[(y+r)*w+x]
				}
				if y-r-1 >= 0 {
					s -= tmp[(y-r-1)*w+x]
				}
			}
			lo, hi := max(0, y-r), min(h-1, y+r)
			out[y*w+x] = s / float32(hi-lo+1)
		}
	}
	return out
}
