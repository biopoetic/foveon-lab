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
	"sync/atomic"
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
	return MaxDetailHighlightsGate(l, top, nil)
}

// MaxDetailHighlightsGate is MaxDetailHighlightsMask with a per-pixel
// eligibility gate, feathered over two pixels.
//
// Task 29: the shoulder belongs to the rebuilt highlight, not to every tone
// above the knee. On the SD1 frame SDIM0031 the display's white sits at 0.4165
// linear (the per-body curve's green reaches 0.97 there), so a 0.9 knee put
// 52% of the leaf crop above it; 62% of those pixels were compressed into the
// last 1% of the output and rendered as flat pastel plateaus. They were all
// open pixels - the raw never clipped there - so the "recovery" was a global
// tone map of unclipped light. A nil gate keeps the old behaviour; the Max
// Detail caller passes the pixels RecoverHighlights actually rebuilt, so an
// unclipped highlight keeps the normal development's value.
func MaxDetailHighlightsGate(l *Linear, top float32, gate []bool) []bool {
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
	n := l.W * l.H
	// Feather the gate: a 0/1 eligibility mask would draw its own edge at
	// the boundary between a rebuilt highlight and an open one.
	var weight []float32
	if gate != nil && len(gate) == n {
		weight = make([]float32, n)
		for i, ok := range gate {
			if ok {
				weight[i] = 1
			}
		}
		tmp := make([]float32, n)
		for pass := 0; pass < 2; pass++ {
			parallelRows(l.H, func(y0, y1 int) {
				for y := y0; y < y1; y++ {
					for x := 0; x < l.W; x++ {
						var s float32
						var cnt int
						for dy := -1; dy <= 1; dy++ {
							for dx := -1; dx <= 1; dx++ {
								xx, yy := x+dx, y+dy
								if xx < 0 || yy < 0 || xx >= l.W || yy >= l.H {
									continue
								}
								s += weight[yy*l.W+xx]
								cnt++
							}
						}
						tmp[y*l.W+x] = s / float32(cnt)
					}
				}
			})
			copy(weight, tmp)
		}
	}
	mask := make([]bool, n)
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
	var moved atomic.Bool
	parallelRows(l.H, func(y0, y1 int) {
		for i := y0 * l.W; i < y1*l.W; i++ {
			p := l.Pix[i*3 : i*3+3]
			m := max(p[0], p[1], p[2])
			if m <= k {
				continue
			}
			w := float32(1)
			if weight != nil {
				w = weight[i]
				if w == 0 {
					continue
				}
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
				w *= float32(1 - smoothstep(0.03, 0.12, blue))
			}
			g = 1 + (g-1)*w
			if g == 1 {
				continue
			}
			mask[i] = true
			moved.Store(true)
			p[0], p[1], p[2] = p[0]*g, p[1]*g, p[2]*g
		}
	})
	if !moved.Load() {
		return nil
	}
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

// MaxDetailOpenOptions are the strengths of the open-highlight recovery.
// Zero values take the defaults; a negative Amount turns the pass off.
type MaxDetailOpenOptions struct {
	Threshold float64 // a normal-development luminance at or above this is white
	Amount    float64 // fraction of exposure removed inside the region, 0..1
	Soft      float64 // region-finding blur, pixels (0 = from the width)
	Feather   float64 // edge feather, pixels (0 = from the width)
	Top       float32 // the frame's display white, in l's units (0 = no gate)
	Gate      float64 // only recover if open tones reach this multiple of Top
	ClipFrac  float64 // only recover if the white area is at most this clipped
}

// maxDetailOpenGate: a frame whose open tones stop barely above the display
// white has no hidden texture worth pulling out: the correction already shows
// what is there, and lowering those tones only draws the mask's boundary
// through a cloud. On the SD14 bridge sky the 99th percentile of open tones is
// 1.32 x white and the recovery painted grey cloud patches (task 30); on
// SDIM0031 it is 2.52 x and the leaf has real hidden texture.
const maxDetailOpenGate = 1.5

// maxDetailOpenClip: a white area that is mostly clipped has lost the data the
// recovery would show. On the Shard glass 44% of the white is clipped, and
// pulling the open part down next to it turns the glass's own tint into
// yellow streaks (task 30, the same fault task 29 removed). SDIM0031's white
// area is 0.15% clipped, the waterfall's 0.98%, dp3 Merrill's 3.55%.
const maxDetailOpenClip = 0.05

// maxDetailOpenWhite: the normal development's luminance at which a pixel is
// the display's white. The correction puts the sensor's top stop on 1.0; an
// over-exposed open tone is one that arrives there with room to spare.
const maxDetailOpenWhite = 0.95

// MaxDetailOpenAmount is how much exposure the open-highlight recovery takes
// out inside the region: 0.6 is about one and a third stops. It is one
// number, not a curve: the region has to keep its internal contrast (a
// level-dependent gain is a shoulder again), so the recovery is a uniform
// exposure change where the display has none to spare. The threshold, the
// region and both gates are measured per frame.
const MaxDetailOpenAmount = 0.6

// MaxDetailOpenHighlights pulls open, over-exposed tones down by a smooth
// exposure change before the correction, so the correction's own top end does
// not flatten them. It returns the pixels it moved.
//
// Task 30: on SDIM0031 the leaf crop is open in every layer, yet 44% of it is
// white in the normal development: the per-body correction puts raw 0.42 at
// white, and the leaf's open tones run to 1.5, so 1.1 of raw range is squeezed
// into the last few codes. The data is there; the display has no room for it.
// The gain is computed from a low-frequency region mask (the white area
// found at a coarse blur, then feathered) rather than from each pixel's own
// level, so the texture inside the region keeps its contrast instead of being
// compressed by a shoulder. Only open pixels are eligible: a fully clipped
// area has no texture and must stay white.
//
// The region is the normal development's white, measured per frame: keying
// on the raw's brightness instead caught every saturated bright green leaf
// (its green layer is the brightest), which the normal development shows as
// green, not white. norm must be the corrected (display) development at l's
// geometry.
func MaxDetailOpenHighlights(l, norm *Linear, open []bool, opt MaxDetailOpenOptions) []bool {
	if l == nil || norm == nil || l.W < 1 || l.H < 1 || norm.W != l.W || norm.H != l.H {
		return nil
	}
	n := l.W * l.H
	if len(open) != n {
		return nil
	}
	if opt.Threshold == 0 {
		opt.Threshold = maxDetailOpenWhite
	}
	if opt.Amount == 0 {
		opt.Amount = MaxDetailOpenAmount
	}
	if opt.Amount < 0 {
		return nil
	}
	opt.Amount = max(0, min(0.95, opt.Amount))
	if opt.Gate == 0 {
		opt.Gate = maxDetailOpenGate
	}
	if opt.Top > 0 && openHeadroom(l, open, opt.Top) < opt.Gate {
		return nil
	}
	// The region: open pixels the display puts at white. The white area's
	// clipped share decides whether a recovery can be coherent at all.
	field := make([]float32, n)
	white, whiteClip := 0, 0
	for i := 0; i < n; i++ {
		p := norm.Pix[i*3 : i*3+3]
		y := 0.2126*float64(p[0]) + 0.7152*float64(p[1]) + 0.0722*float64(p[2])
		if y < opt.Threshold {
			continue
		}
		white++
		if !open[i] {
			whiteClip++
			continue
		}
		field[i] = 1
	}
	if white == 0 || whiteClip == white {
		return nil
	}
	if opt.ClipFrac == 0 {
		opt.ClipFrac = maxDetailOpenClip
	}
	if float64(whiteClip)/float64(white) > opt.ClipFrac {
		return nil
	}
	// Find the white *regions*: a plain blur of the indicator, thresholded,
	// keeps a large over-exposed area whole and discards the isolated white
	// specks (a point light must stay a point). Dilating a 0/1 field instead
	// turned every speck into a disc and covered two thirds of the frame.
	if opt.Soft == 0 {
		opt.Soft = math.Max(2, float64(l.W)/160)
	}
	if opt.Feather == 0 {
		opt.Feather = math.Max(1, float64(l.W)/600)
	}
	soft := GaussBlur(field, l.W, l.H, opt.Soft)
	region := make([]float32, n)
	for i := range soft {
		u := (float64(soft[i]) - 0.25) / 0.35
		u = max(0, min(1, u))
		region[i] = float32(u * u * (3 - 2*u))
	}
	region = GaussBlur(region, l.W, l.H, opt.Feather)
	moved := make([]bool, n)
	for i := 0; i < n; i++ {
		// A clipped pixel has no data behind it and must stay exactly the
		// normal development's white, even where the region's feather
		// reaches it.
		if !open[i] {
			continue
		}
		m := float64(region[i])
		// The box-blur running sums leave float32 residue far from the
		// region; below this it is under a thousandth of a code.
		if m <= 1e-4 {
			continue
		}
		g := float32(1 - opt.Amount*m)
		if g == 1 {
			continue
		}
		p := l.Pix[i*3 : i*3+3]
		p[0], p[1], p[2] = p[0]*g, p[1]*g, p[2]*g
		moved[i] = true
	}
	return moved
}

// openHeadroom is the 99th percentile of the open pixels' brightest channel,
// in multiples of the frame's display white. It is the frame's own measure of
// how much open tone the correction has no room for.
func openHeadroom(l *Linear, open []bool, top float32) float64 {
	const bins = 1024
	var hist [bins]int
	n := 0
	for i := 0; i < l.W*l.H && i < len(open); i++ {
		if !open[i] {
			continue
		}
		p := l.Pix[i*3 : i*3+3]
		m := float64(max(p[0], max(p[1], p[2])) / top)
		hist[max(0, min(bins-1, int(m*float64(bins)/8)))]++
		n++
	}
	if n == 0 {
		return 0
	}
	want := n - n/100
	seen := 0
	for b := 0; b < bins; b++ {
		seen += hist[b]
		if seen >= want {
			return float64(b) / bins * 8
		}
	}
	return 8
}
