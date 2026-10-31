package render

// Max Detail (task 19): a development mode for the raw layers, not a set of
// SPP sliders. It runs on the linear output of the sensor's own layers,
// keeps the highlight range the layers still hold, lifts shadow signal and
// stops at the noise, and leaves clipped white white. The SPP neutral base
// is deliberately not applied here: its channel curves clamp at 1, which
// would throw away exactly the signal above white this mode exists to keep.

import (
	"math"
	"sort"
)

// MaxDetailOptions are the mode's strengths. Zero values take the defaults.
type MaxDetailOptions struct {
	ISO        float64
	Shadow     float64 // shadow lift in stops at the shadow knee (0 = default)
	Highlight  float64 // highlight compression (0 = default)
	Saturation float64 // chroma gain after tone mapping (0 = default)
}

// maxDetail defaults: enough to show the range, gentle enough not to look
// like an HDR composite.
const (
	maxDetailShadow       = 1.20
	maxDetailHighlight    = 0.18
	maxDetailSaturation   = 1.06
	maxDetailShoulderKnee = 0.84
	maxDetailContrast     = 0.10
	mdToneN               = 4096
)

// MaxDetailDevelop maps a linear image with sensor headroom (values may
// exceed 1) into display-linear [0,1]. It changes luminance only (so hue is
// kept), fades chroma into white so recovered highlights have no coloured
// rim, and gates the shadow lift on the image's own measured noise.
func MaxDetailDevelop(l *Linear, opt MaxDetailOptions) {
	if l == nil || l.W < 1 || l.H < 1 {
		return
	}
	if opt.Shadow == 0 {
		opt.Shadow = maxDetailShadow
	}
	if opt.Highlight == 0 {
		opt.Highlight = maxDetailHighlight
	}
	if opt.Saturation == 0 {
		opt.Saturation = maxDetailSaturation
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
	// The tonal part is one monotonic curve, baked once: the shadow gain,
	// the S-curve and the shoulder in that order, with the running maximum
	// taken so no numerical wrinkle can make a brighter input darker. Above
	// the top sample the shoulder is white.
	const lutMax = 4.0
	var lut [mdToneN + 1]float32
	prev := 0.0
	for i := 0; i <= mdToneN; i++ {
		x := lutMax * float64(i) / mdToneN
		w := smoothstep(floor, hi, x) * (1 - smoothstep(0.04, 0.28, x))
		y2 := x * (1 + lift*w)
		if maxDetailContrast > 0 && y2 < 1 {
			y2 += maxDetailContrast * (smoothstep(0, 1, y2) - y2)
		}
		y3 := shoulder(y2, maxDetailShoulderKnee, opt.Highlight)
		if y3 < prev {
			y3 = prev
		}
		prev = y3
		lut[i] = float32(y3)
	}
	at := func(y float64) float64 {
		f := y * mdToneN / lutMax
		if f <= 0 {
			return 0
		}
		if f >= mdToneN {
			return 1
		}
		i := int(f)
		t := f - float64(i)
		return float64(lut[i])*(1-t) + float64(lut[i+1])*t
	}
	parallelRows(l.H, func(y0, y1 int) {
		for i := y0 * l.W; i < y1*l.W; i++ {
			p := l.Pix[i*3 : i*3+3]
			r, g, b := float64(p[0]), float64(p[1]), float64(p[2])
			y := 0.2126*r + 0.7152*g + 0.0722*b
			if y <= 1e-7 {
				p[0], p[1], p[2] = 0, 0, 0
				continue
			}
			y3 := at(y)
			k := y3 / y
			r, g, b = r*k, g*k, b*k
			// Chroma fades as the brightest channel approaches white: a
			// recovered highlight keeps its brightness, not a magenta rim.
			mx := math.Max(r, math.Max(g, b))
			if mx > 0.9 {
				d := smoothstep(0.9, 1.25, mx) * 0.85
				r = r + (y3-r)*d
				g = g + (y3-g)*d
				b = b + (y3-b)*d
			}
			// Keep the Foveon colour, gently, in the tones that are not white.
			if opt.Saturation != 1 && mx < 1.2 {
				r = y3 + (r-y3)*opt.Saturation
				g = y3 + (g-y3)*opt.Saturation
				b = y3 + (b-y3)*opt.Saturation
			}
			p[0], p[1], p[2] = float32(clamp01f(r)), float32(clamp01f(g)), float32(clamp01f(b))
		}
	})
}

// shoulder maps x >= 0 into [0,1): identity below knee, then an exponential
// shoulder whose steepness is set by strength (larger = softer).
func shoulder(x, knee, strength float64) float64 {
	if x <= knee {
		return x
	}
	if strength <= 0 {
		strength = maxDetailHighlight
	}
	s := (1 - knee) / (1 + 2*strength)
	return knee + (1-knee)*(1-math.Exp(-(x-knee)/s))
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

func clamp01f(v float64) float64 { return max(0, min(1, v)) }

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
