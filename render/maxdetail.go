package render

// Max Detail (task 19): a development mode for the raw layers, not a set of
// SPP sliders. The raw development in camfread.go has already rebuilt what
// the clipped layers imply. Here the mode opens the shadows, gently and only
// down to the noise, and otherwise leaves the picture as the normal
// development made it: same colour, same contrast, same white. Earlier
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
