package render

// Max Detail (task 19): a development mode for the raw layers, not a set of
// SPP sliders. The raw development in camfread.go has already rebuilt what
// the clipped layers imply. MaxDetailHighlights keeps the top of the frame
// out of the correction's ramp to white, and MaxDetailDevelop opens the
// shadows, gently and only down to the noise. Otherwise the picture stays as
// the normal development made it: same colour, same contrast. Earlier
// versions added saturation, an S-curve, a stronger lift and a chroma fade
// near white, and the result looked artificial: hot reds, flat highlights
// and a washed night sky.

import (
	"math"
	"sort"
)

// MaxDetailOptions are the mode's strengths. Zero values take the defaults.
type MaxDetailOptions struct {
	ISO    float64
	Shadow float64 // gain added at the shadow knee, 0.45 = ×1.45 (0 = default)
}

const (
	maxDetailShadow = 0.45
	mdToneN         = 4096
)

// MaxDetailDevelop opens the shadows of a linear image and clips it to
// [0,1]. The curve runs on each channel, as film and a tone curve do, so a
// lifted colour loses a little chroma instead of glowing, and it is gated on
// the image's own measured noise.
func MaxDetailDevelop(l *Linear, opt MaxDetailOptions) {
	if l == nil || l.W < 1 || l.H < 1 {
		return
	}
	if opt.Shadow == 0 {
		opt.Shadow = maxDetailShadow
	}
	// High ISO gets less lift: the same stops would only amplify pepper.
	isoScale := 1.0
	if opt.ISO > 200 {
		isoScale = max(0.35, 1-math.Log2(opt.ISO/200)*0.16)
	}
	lift := opt.Shadow * isoScale
	sigma := linearNoise(l)
	floor := math.Max(2.5*sigma, 0.0006)
	hi := math.Max(floor*8, 0.012)
	// One monotonic curve, baked once; the running maximum keeps a numerical
	// wrinkle from making a brighter input darker. It ends at the identity,
	// so midtones and highlights are the normal development's.
	var lut [mdToneN + 1]float32
	prev := 0.0
	for i := 0; i <= mdToneN; i++ {
		x := float64(i) / mdToneN
		w := smoothstep(floor, hi, x) * (1 - smoothstep(0.03, 0.40, x))
		y := max(prev, min(1, x*(1+lift*w)))
		prev = y
		lut[i] = float32(y)
	}
	at := func(v float32) float32 {
		f := v * mdToneN
		if f <= 0 {
			return 0
		}
		if f >= mdToneN {
			return 1
		}
		i := int(f)
		t := f - float32(i)
		return lut[i]*(1-t) + lut[i+1]*t
	}
	parallelRows(l.H, func(y0, y1 int) {
		for i := y0 * l.W * 3; i < y1*l.W*3; i++ {
			l.Pix[i] = at(l.Pix[i])
		}
	})
}

// smoothstep is the usual Hermite ramp.
func smoothstep(a, b, x float64) float64 {
	if b <= a {
		if x >= b {
			return 1
		}
		return 0
	}
	u := (x - a) / (b - a)
	u = max(0, min(1, u))
	return u * u * (3 - 2*u)
}

// linearNoise estimates the image's luminance noise from the dark tones:
// the median absolute Laplacian residual, scaled to a Gaussian sigma. The
// median ignores the few real edges in the selection; only pixels below
// 0.05 linear are considered, so the shadows' own signal does not inflate
// it.
func linearNoise(l *Linear) float64 {
	var res []float32
	step := max(1, (l.W*l.H)/40000)
	for i := l.W + 1 + step; i < l.W*l.H-l.W-1; i += step {
		x := i % l.W
		if x == 0 || x == l.W-1 {
			continue
		}
		at := func(j int) float64 {
			p := l.Pix[j*3:]
			return 0.2126*float64(p[0]) + 0.7152*float64(p[1]) + 0.0722*float64(p[2])
		}
		y := at(i)
		if y >= 0.05 {
			continue
		}
		r := y - (at(i-1)+at(i+1)+at(i-l.W)+at(i+l.W))/4
		res = append(res, float32(math.Abs(r)))
	}
	if len(res) < 64 {
		return 0.001
	}
	sort.Slice(res, func(i, j int) bool { return res[i] < res[j] })
	med := float64(res[len(res)/2])
	s := med / 0.6745 / math.Sqrt(1.25)
	return max(s, 1e-4)
}

// Max Detail's highlight shoulder: above this fraction of the top, the
// brightest channel is compressed so the frame's top lands at the top. Colour
// is untouched because all three channels share one gain, and everything
// under the knee is left alone. The knee is high on purpose: 0.60 (before
// task 28) reached into tones the normal development never clips and
// stretched the faint cloud contrast into grey patches with hard edges on the
// SD14 bridge sky. Only the last tenth, where the correction is already about
// to run to white, may move.
const maxDetailKnee = 0.90

// MaxDetailHighlights brings what the frame holds above white back into
// range. Run it before the per-body correction: that correction ends in a
// ramp that turns the sensor's top stop into flat white.
func MaxDetailHighlights(l *Linear, top float32) {
	MaxDetailHighlightsMask(l, top)
}

// MaxDetailHighlightsMask is MaxDetailHighlights and reports which pixels it
// moved. The caller can keep the normal development's corrected value for
// the rest: the shoulder is per-pixel, but the bright-chroma denoise that
// runs later is spatial, and a moved highlight would otherwise pull the
// chroma of its unclipped neighbours (task 28 measured 87,004 below-knee
// pixels changed, up to 0.25 linear). With the mask, every pixel the shoulder
// did not touch is bit-identical to the normal development.
func MaxDetailHighlightsMask(l *Linear, top float32) []bool {
	if l == nil || l.W < 1 || l.H < 1 {
		return nil
	}
	ceil := highlightCeiling(l)
	// The ceiling must stay inside what the correction can display. On a
	// frame with a large fully clipped area, the lost pixels are neutralised
	// at their brightest layer, well above white, and they are 3% or more of
	// the frame: the 99.95th percentile then sits at the lost plateau (2.5 on
	// the SD14 bridge) instead of at the top of the open tones (about 1.9).
	// Using the plateau as the ceiling compresses every open near-white tone
	// into the first part of the range and turns a faint cloud into a grey
	// patch with edges. Above the base's white a value cannot be shown as
	// anything but white anyway, so it must not buy room from the values that
	// can.
	if ceil > rawBaseWhite {
		ceil = rawBaseWhite
	}
	if ceil <= top {
		return nil
	}
	mask := make([]bool, l.W*l.H)
	k, o := maxDetailKnee*top, top
	// Exponential approach to the top with slope 1 at the knee, so there is
	// no visible seam: solve its rate for this frame's ceiling.
	span := float64(ceil - k)
	room := float64(o - k)
	lo, hi := 1e-3, 100.0
	for range 50 {
		a := (lo + hi) / 2
		if room*a/(1-math.Exp(-a*span)) > 1 {
			hi = a
		} else {
			lo = a
		}
	}
	a := (lo + hi) / 2
	scale := room / (1 - math.Exp(-a*span))
	parallelRows(l.H, func(y0, y1 int) {
		for i := y0 * l.W; i < y1*l.W; i++ {
			p := l.Pix[i*3 : i*3+3]
			m := max(p[0], p[1], p[2])
			if m <= k {
				continue
			}
			t := min(float64(m-k), span)
			g := (k + float32(scale*(1-math.Exp(-a*t)))) / m
			// A blue- or violet-dominant highlight is a reflection whose
			// colour above white reads as a lilac fleck (SDIM0031's leaf)
			// once the shoulder pulls it below the correction's clip. Fade
			// the shoulder out there, smoothly with the blue excess, so the
			// pixel stays the normal development's white instead of a new
			// colour. Neutral, green and red (skin) highlights keep it.
			blue := float64(p[2]-max(p[0], p[1])) / float64(m)
			if blue > 0 {
				w := 1 - smoothstep(0.03, 0.12, blue)
				g = 1 + (g-1)*float32(w)
			}
			if g == 1 {
				continue
			}
			mask[i] = true
			p[0], p[1], p[2] = p[0]*g, p[1]*g, p[2]*g
		}
	})
	return mask
}

// highlightCeiling is the frame's top level, ignoring the few brightest
// pixels so one specular doesn't darken the rest.
func highlightCeiling(l *Linear) float32 {
	var hist [2048]int
	n := l.W * l.H
	for i := 0; i < n; i++ {
		p := l.Pix[i*3 : i*3+3]
		m := max(p[0], p[1], p[2])
		hist[max(0, min(len(hist)-1, int(m*512)))]++
	}
	left := n / 2000
	for b := len(hist) - 1; b >= 0; b-- {
		left -= hist[b]
		if left <= 0 {
			return float32(b+1) / 512
		}
	}
	return 0
}
