package render

import "math"

// The raw base's hue correction (HueGain/HueRot, applied in CIELAB on
// linear sRGB) as a DNG ProfileHueSatMap, so a raw editor developing our
// camera DNG gets it too. The DNG reader applies the map in HSV of linear
// ProPhoto RGB, right after the forward matrix (DNG SDK, dng_reference.cpp
// RefBaselineHueSatMap): hue 0-6, interpolated over hue (wrapping) and
// saturation, hue shifts in degrees. Each cell's colour is sent through
// hueSat itself and the change measured in that space.
//
// dngcheck (the DNG developed as the SDK's reference pipeline does) against
// SPP's neutral exports, 11 SD1 photos, without -> with the map: dE2000
// 3.44 -> 3.35, foliage SDIM0098 3.81 -> 2.80, 0097 2.56 -> 2.15, photos
// without greens unchanged, worst +0.14; the DNG vs the app's own preview
// 2.22 -> 2.15.

const (
	hsmHueDivs = 90 // 4° steps, as Adobe's own profiles
	hsmSatDivs = 30
	// hsmValue is the HSV value each cell is evaluated at: hueSat fades in
	// over CIELAB chroma 0-6, which depends on brightness; this is a mid
	// tone (CIELAB L ~ 50 for a grey). 0.08 and 0.6 developed the same to
	// 0.01 dE2000 (dngcheck, 11 SD1 photos).
	hsmValue = 0.25
)

// ProPhoto (linear, D50) -> XYZ D50.
var proToXYZD50 = [9]float64{0.7976749, 0.1351917, 0.0313534, 0.2880402, 0.7118741, 0.0000857, 0, 0, 0.8252100}

// hueSatMap returns the ProfileHueSatMapDims (hue, sat, value divisions)
// and ProfileHueSatMapData1 (hue shift °, saturation scale, value scale per
// cell; value outermost, then hue, saturation innermost) of c's hue
// correction, or nil data when it has none.
func (c *RawCorrection) hueSatMap() ([3]uint32, []float32) {
	dims := [3]uint32{hsmHueDivs, hsmSatDivs, 1}
	if len(c.HueGain) != hueBins || len(c.HueRot) != hueBins {
		return dims, nil
	}
	var d50 [9]float64
	copy(d50[:], sRGBToXYZD50)
	srgbFromD50, _ := invert3(d50)
	proToSRGB := mul33(srgbFromD50, proToXYZD50)
	srgbToPro, _ := invert3(proToSRGB)
	data := make([]float32, 0, hsmHueDivs*hsmSatDivs*3)
	for hi := 0; hi < hsmHueDivs; hi++ {
		h := float64(hi) * 6 / hsmHueDivs
		for si := 0; si < hsmSatDivs; si++ {
			s := float64(si) / (hsmSatDivs - 1)
			shift, sat, val := 0.0, 1.0, 1.0
			if s > 0 {
				p := hsvToRGB(h, s, hsmValue)
				q := mulVec(proToSRGB, p)
				r, g, b := c.hueSat(float32(q[0]), float32(q[1]), float32(q[2]))
				p2 := mulVec(srgbToPro, [3]float64{float64(r), float64(g), float64(b)})
				h2, s2, v2 := rgbToHSV(p2)
				if s2 > 0 {
					dh := math.Mod(h2-h+9, 6) - 3 // wrapped to -3..3
					shift = dh * 60
					sat = s2 / s
					val = v2 / hsmValue
				}
			}
			data = append(data, float32(shift), float32(sat), float32(val))
		}
	}
	return dims, data
}

func mulVec(m [9]float64, v [3]float64) [3]float64 {
	return [3]float64{m[0]*v[0] + m[1]*v[1] + m[2]*v[2], m[3]*v[0] + m[4]*v[1] + m[5]*v[2], m[6]*v[0] + m[7]*v[1] + m[8]*v[2]}
}

// rgbToHSV is the DNG SDK's DNG_RGBtoHSV: hue 0-6, s = gap/max, v = max.
func rgbToHSV(p [3]float64) (h, s, v float64) {
	r, g, b := p[0], p[1], p[2]
	v = math.Max(r, math.Max(g, b))
	gap := v - math.Min(r, math.Min(g, b))
	if gap <= 0 || v <= 0 {
		return 0, 0, v
	}
	switch v {
	case r:
		h = (g - b) / gap
		if h < 0 {
			h += 6
		}
	case g:
		h = 2 + (b-r)/gap
	default:
		h = 4 + (r-g)/gap
	}
	return h, gap / v, v
}

// hsvToRGB is the DNG SDK's DNG_HSVtoRGB (hue 0-6).
func hsvToRGB(h, s, v float64) [3]float64 {
	if s <= 0 {
		return [3]float64{v, v, v}
	}
	h = math.Mod(h, 6)
	if h < 0 {
		h += 6
	}
	i := int(h)
	f := h - float64(i)
	p, q, t := v*(1-s), v*(1-s*f), v*(1-s*(1-f))
	switch i {
	case 0:
		return [3]float64{v, t, p}
	case 1:
		return [3]float64{q, v, p}
	case 2:
		return [3]float64{p, v, t}
	case 3:
		return [3]float64{p, q, v}
	case 4:
		return [3]float64{t, p, v}
	default:
		return [3]float64{v, p, q}
	}
}
