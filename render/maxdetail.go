package render

// Max Detail (task 19): a development mode for the raw layers, not a set of
// SPP sliders. The raw development in camfread.go has already rebuilt what
// the clipped layers imply, put the SPP neutral colour on it and taken its
// white ramp to white (task 20: the old base stopped at 0.93 linear, which
// made the mode's whites grey). Here the mode lifts shadow signal and stops
// at the noise, keeps the Foveon colour, and maps the top to a hard white:
// an 8-bit display cannot show more than white, and the earlier extra
// shoulder only moved the mode's white down to 0.959 (task 20, part 1).

import (
	"math"
	"sort"
)

// MaxDetailOptions are the mode's strengths. Zero values take the defaults.
type MaxDetailOptions struct {
	ISO        float64
	Shadow     float64 // shadow lift in stops at the shadow knee (0 = default)
	Saturation float64 // chroma gain after tone mapping (0 = default)
}

// maxDetail defaults: enough to show the range, gentle enough not to look
// like an HDR composite.
const (
	maxDetailShadow     = 1.20
	maxDetailSaturation = 1.06
	maxDetailContrast   = 0.10
	mdToneN             = 4096
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
		// White stays white. An earlier shoulder compressed everything above
		// 0.84 and left the mode's white at 0.959 linear (250 in 8-bit)
		// while the default render reached 255: the mode's skies were
		// visibly grey and its top stop lost the local contrast the raw
		// data holds (task 20, part 1). The recovered headroom still has to
		// land somewhere, and 8-bit output white is the limit, so values at
		// and above it are white; the win is the base's smooth 0.93-1.0
		// ramp, which the default render now has too, plus the layers
		// rebuilt from unclipped siblings below it.
		y3 := min(1, y2)
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
			if mx > 1.0 {
				d := smoothstep(1.0, 1.3, mx) * 0.85
				r = r + (y3-r)*d
				g = g + (y3-g)*d
				b = b + (y3-b)*d
			}
			// Keep the Foveon colour, gently, in the tones that are not
			// white. The gain fades out as the pixel approaches white, so a
			// bright, slightly coloured cloud is not pushed over 1 by the
			// saturation itself (which would read as a clipped channel).
			if opt.Saturation != 1 && mx < 1.2 {
				sat := 1 + (opt.Saturation-1)*(1-smoothstep(0.85, 1.0, y3))
				r = y3 + (r-y3)*sat
				g = y3 + (g-y3)*sat
				b = y3 + (b-y3)*sat
			}
			// A saturated highlight can gain a channel over white through
			// that saturation (or the tone curve). Pull back only as much
			// chroma as needed for the brightest channel to reach 1, so the
			// extra does not turn a bright colour into a flat clipped patch.
			mx = math.Max(r, math.Max(g, b))
			if mx > 1 && mx > y3 {
				t := (mx - 1) / (mx - y3)
				r += (y3 - r) * t
				g += (y3 - g) * t
				b += (y3 - b) * t
			}
			// Bright near-neutral areas (sky, cloud, white walls) keep the
			// cast the neutral base gives them: on the SD14 bridge sky that
			// is about +1.5 a* against SPP's own neutral export, which shows
			// as pink/cyan pastel blotches along the cloud edges. Pull only
			// the low-chroma bright pixels towards neutral; saturated skin
			// and water (C above ~10) are left alone so the mode does not
			// cost colour where SPP keeps it.
			if y3 > 0.55 {
				c := chromaOf(r, g, b)
				if c < 10 {
					w := smoothstep(0.55, 0.8, y3) * (1 - smoothstep(4, 10, c)) * 0.6
					r += (y3 - r) * w
					g += (y3 - g) * w
					b += (y3 - b) * w
				}
			}
			p[0], p[1], p[2] = float32(clamp01f(r)), float32(clamp01f(g)), float32(clamp01f(b))
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

func clamp01f(v float64) float64 { return max(0, min(1, v)) }

// chromaOf is the CIELAB chroma of a linear sRGB pixel (D65), for the
// near-neutral gate on the highlight cast clean-up.
func chromaOf(r, g, b float64) float64 {
	x := 0.4124*r + 0.3576*g + 0.1805*b
	y := 0.2126*r + 0.7152*g + 0.0722*b
	z := 0.0193*r + 0.1192*g + 0.9505*b
	f := func(v float64) float64 {
		if v > 216.0/24389 {
			return math.Cbrt(v)
		}
		return (24389.0/27*v + 16) / 116
	}
	fx, fy, fz := f(x/0.9505), f(y/1.0), f(z/1.089)
	a := 500 * (fx - fy)
	bb := 200 * (fy - fz)
	return math.Hypot(a, bb)
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
