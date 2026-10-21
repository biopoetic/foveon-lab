package render

// Raw base: for camera families where it was measured to help, the preview
// starts from the raw data (decoded by x3f_extract) mapped onto SPP's own
// neutral rendering, instead of the camera's embedded JPEG, which carries
// the in-camera colour mode (Vivid, FC Blue…). The mapping is a 3×3 matrix
// in linear light plus a tone curve per channel, fitted on pairs of SPP
// neutral exports and raw decodes of the same photos (foveon-lab fit-raw).

import (
	_ "embed"
	"encoding/json"
	"math"
)

// RawCorrection maps an x3f_extract sRGB decode to SPP's neutral look.
type RawCorrection struct {
	Family string `json:"family"` // "SD1" (Merrill) or "SD15"
	// UseRaw: the held-out test showed the raw base beats the camera JPEG.
	UseRaw bool       `json:"useRaw"`
	Matrix [9]float64 `json:"matrix"` // linear RGB, row-major
	// Curves[c][b]: output (sRGB 0..1) for input bin b of 64; -1 = identity.
	Curves [3][64]float64 `json:"curves"`
	// Held-out mean error (8-bit) vs SPP, raw base and camera JPEG.
	HeldOutRaw  float64 `json:"heldOutRaw"`
	HeldOutJPEG float64 `json:"heldOutJPEG"`
	Photos      int     `json:"photos"`
	// Taste, when set, replaces the matrix and curves: the camera's own
	// colour (already colorimetric) with only SPP's tone curve on luminance
	// (Tone, sRGB-encoded, one value per equal bin) and a saturation (Sat).
	// It has few parameters, so it carries over between bodies: TasteFrom
	// names the group it was learnt on (e.g. the Quattro uses the SD1's).
	Tone      []float64 `json:"tone,omitempty"`
	Sat       float64   `json:"sat,omitempty"`
	TasteFrom string    `json:"tasteFrom,omitempty"`
	// HueGain and HueRot (12 bins of CIELAB hue, 30° each, centred at
	// 15°+30°·k, interpolated) correct what the matrix and curves leave:
	// a chroma gain and a hue rotation (degrees), faded in over chroma 0-6
	// so near-neutrals stay put. See hueSat. fit-raw does not fit them: carry
	// them over by hand when copying a refitted rawbase.json.
	HueGain []float64 `json:"hueGain,omitempty"`
	HueRot  []float64 `json:"hueRot,omitempty"`
	// hueANeg: every hue HueGain/HueRot change lies within 90-270° (a* < 0),
	// so hueSat may skip pixels with a* >= 0 before converting them.
	hueANeg bool
}

//go:embed rawbase.json
var rawBaseJSON []byte

var rawBases map[string]*RawCorrection

func init() {
	var list []RawCorrection
	if json.Unmarshal(rawBaseJSON, &list) != nil {
		return
	}
	rawBases = map[string]*RawCorrection{}
	for i := range list {
		list[i].MakeMonotone()
		list[i].hueANeg = list[i].hueChangesOnlyANeg()
		rawBases[list[i].Family] = &list[i]
	}
}

// MakeMonotone makes each channel curve non-decreasing, working down from
// the top: a brighter input never comes out darker. The fitted curves'
// lowest bin can be polluted by matrix outputs clipped at 0 (a saturated
// colour whose channel went negative), which SPP shows bright: the SD1
// base's green came out 0.85 there instead of ~0.014, turning near-black
// pixels bright green (a night shot, v0.4.2).
func (c *RawCorrection) MakeMonotone() {
	for k := 0; k < 3; k++ {
		next := -1.0
		for b := 63; b >= 0; b-- {
			v := c.Curves[k][b]
			if v < 0 {
				continue
			}
			if next >= 0 && v > next {
				c.Curves[k][b] = next
			} else {
				next = v
			}
		}
	}
}

// RawBaseFor is the correction for a camera family, or nil when the raw
// base is not (measured to be) better than the camera JPEG there.
func RawBaseFor(family string) *RawCorrection {
	c := rawBases[family]
	if c == nil || !c.UseRaw {
		return nil
	}
	return c
}

// RawBaseFitted is the family's fitted correction even where it did not
// beat the camera JPEG at base ISO (nil if none was fitted).
func RawBaseFitted(family string) *RawCorrection {
	c := rawBases[family]
	if c == nil || (c.Photos < 3 && c.TasteFrom == "") {
		return nil
	}
	return c
}

// curve maps brightness e (sRGB 0..1) through channel k's measured curve,
// interpolating between bin centres (bins without data are identity).
func (c *RawCorrection) curve(k int, e float64) float64 {
	at := func(b int) float64 {
		if v := c.Curves[k][b]; v >= 0 {
			return v
		}
		return (float64(b) + 0.5) / 64
	}
	f := e*64 - 0.5
	if f <= 0 {
		return at(0) * e * 128 // linear down to black at 0 (bin 0's centre is 1/128)
	}
	if f >= 63 {
		return min(1, at(63)+(e-63.5/64))
	}
	b := int(f)
	t := f - float64(b)
	return at(b)*(1-t) + at(b+1)*t
}

// tone maps linear luminance y through the taste curve.
func (c *RawCorrection) tone(y float64) float64 {
	n := len(c.Tone)
	e := float64(enc(float32(max(0, min(1, y)))))
	f := e*float64(n) - 0.5
	var v float64
	switch {
	case f <= 0:
		v = c.Tone[0] * e * 2 * float64(n)
	case f >= float64(n-1):
		v = c.Tone[n-1]
	default:
		b := int(f)
		t := f - float64(b)
		v = c.Tone[b]*(1-t) + c.Tone[b+1]*t
	}
	return float64(dec(float32(max(0, min(1, v)))))
}

// tastePixel: scale to the new luminance (hue kept), then chroma by Sat.
func (c *RawCorrection) tastePixel(r, g, b float32) (float32, float32, float32) {
	y := 0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)
	if y <= 1e-6 {
		return 0, 0, 0
	}
	y2 := c.tone(y)
	k := y2 / y
	f := func(v float32) float32 { return float32(y2 + c.Sat*(float64(v)*k-y2)) }
	return f(r), f(g), f(b)
}

// Pixel maps one linear pixel.
func (c *RawCorrection) Pixel(r, g, b float32) (float32, float32, float32) {
	if len(c.Tone) > 0 {
		return c.tastePixel(r, g, b)
	}
	if len(c.HueGain) == hueBins && len(c.HueRot) == hueBins {
		return c.hueSat(c.matrixCurves(r, g, b))
	}
	return c.matrixCurves(r, g, b)
}

// matrixCurves is the matrix and per-channel curves.
func (c *RawCorrection) matrixCurves(r, g, b float32) (float32, float32, float32) {
	m := c.Matrix
	fr, fg, fb := float64(r), float64(g), float64(b)
	v := [3]float64{m[0]*fr + m[1]*fg + m[2]*fb, m[3]*fr + m[4]*fg + m[5]*fb, m[6]*fr + m[7]*fg + m[8]*fb}
	var o [3]float32
	for k := 0; k < 3; k++ {
		o[k] = dec(float32(c.curve(k, float64(enc(float32(max(0, min(1, v[k]))))))))
	}
	return o[0], o[1], o[2]
}

// Apply maps l (an x3f_extract decode, linear) in place.
func (c *RawCorrection) Apply(l *Linear) {
	parallelRows(l.H, func(y0, y1 int) {
		for i := y0 * l.W; i < y1*l.W; i++ {
			j := i * 3
			l.Pix[j], l.Pix[j+1], l.Pix[j+2] = c.Pixel(l.Pix[j], l.Pix[j+1], l.Pix[j+2])
		}
	})
}

const hueBins = 12

// sRGB (linear) <-> XYZ, D65.
var (
	rgbToXYZ = [9]float64{0.4124, 0.3576, 0.1805, 0.2126, 0.7152, 0.0722, 0.0193, 0.1192, 0.9505}
	xyzToRGB = func() [9]float64 { m, _ := invert3(rgbToXYZ); return m }()
	whiteXYZ = [3]float64{0.9505, 1.0, 1.089}
)

// hueSat applies HueGain/HueRot to a linear sRGB pixel in CIELAB.
//
// The SD1 base's greens were weak (12 SD1/SD15 photos: green chroma 0.72x
// SPP's on average, 0.67-0.72 on foliage, hue 4-8° towards cyan); a 3×3
// matrix with per-channel curves cannot add chroma to one hue only.
// Fitted on 16 SD1 photos against SPP's neutral exports (area colour,
// blurred, so detail does not count), leave-one-out: chroma ×1.40/×1.27
// and -2°/-7° for hues 120-180°, area colour error 2.38 -> 2.25, foliage
// 0097 2.08 -> 1.46, 0075 1.33 -> 1.03, 0031 3.33 -> 2.68, the worst
// photo +0.06. Correcting every hue, or hue × lightness, did not carry
// over to unseen photos (2.33, 2.37).
func (c *RawCorrection) hueSat(r, g, b float32) (float32, float32, float32) {
	lr, lg, lb := float64(r), float64(g), float64(b)
	m := &rgbToXYZ
	// When only hues with a* < 0 change (hueANeg), a pixel with a* >= 0,
	// i.e. X/Xn >= Y/Yn, returns before the cube roots.
	if c.hueANeg && (m[0]*lr+m[1]*lg+m[2]*lb)/whiteXYZ[0] >= (m[3]*lr+m[4]*lg+m[5]*lb)/whiteXYZ[1] {
		return r, g, b
	}
	var f [3]float64
	for i := 0; i < 3; i++ {
		v := (m[i*3]*lr + m[i*3+1]*lg + m[i*3+2]*lb) / whiteXYZ[i]
		if v > 216.0/24389 {
			f[i] = math.Cbrt(v)
		} else {
			f[i] = (24389.0/27*v + 16) / 116
		}
	}
	L, A, B := 116*f[1]-16, 500*(f[0]-f[1]), 200*(f[1]-f[2])
	ch := math.Hypot(A, B)
	if ch < 1e-6 {
		return r, g, b
	}
	h := math.Atan2(B, A)
	deg := math.Mod(h*180/math.Pi+360, 360)
	pos := deg/(360.0/hueBins) - 0.5
	i0 := int(math.Floor(pos))
	t := pos - float64(i0)
	i0 = ((i0 % hueBins) + hueBins) % hueBins
	i1 := (i0 + 1) % hueBins
	gain := c.HueGain[i0]*(1-t) + c.HueGain[i1]*t
	rot := c.HueRot[i0]*(1-t) + c.HueRot[i1]*t
	if gain == 1 && rot == 0 {
		return r, g, b
	}
	w := math.Min(1, ch/6)
	gain = 1 + (gain-1)*w
	rot = rot * w * math.Pi / 180
	ch *= gain
	h += rot
	A, B = ch*math.Cos(h), ch*math.Sin(h)
	fy := (L + 16) / 116
	fv := [3]float64{fy + A/500, fy, fy - B/200}
	var xyz [3]float64
	for i, v := range fv {
		if v*v*v > 216.0/24389 {
			xyz[i] = v * v * v
		} else {
			xyz[i] = (116*v - 16) / (24389.0 / 27)
		}
		xyz[i] *= whiteXYZ[i]
	}
	n := &xyzToRGB
	return float32(n[0]*xyz[0] + n[1]*xyz[1] + n[2]*xyz[2]),
		float32(n[3]*xyz[0] + n[4]*xyz[1] + n[5]*xyz[2]),
		float32(n[6]*xyz[0] + n[7]*xyz[1] + n[8]*xyz[2])
}

// hueChangesOnlyANeg reports whether every hue that HueGain/HueRot change
// (a bin that is not identity, interpolated out to its neighbours' centres)
// lies strictly within 90-270°, where a* < 0.
func (c *RawCorrection) hueChangesOnlyANeg() bool {
	if len(c.HueGain) != hueBins || len(c.HueRot) != hueBins {
		return false
	}
	w := 360.0 / hueBins
	for k := 0; k < hueBins; k++ {
		if c.HueGain[k] == 1 && c.HueRot[k] == 0 {
			continue
		}
		lo, hi := w*(float64(k)-0.5), w*(float64(k)+1.5) // neighbours' centres
		if lo <= 90 || hi >= 270 {
			return false
		}
	}
	return true
}

// SetHue sets HueGain/HueRot (nil clears them), keeping hueSat's shortcut
// consistent.
func (c *RawCorrection) SetHue(gain, rot []float64) {
	c.HueGain, c.HueRot = gain, rot
	c.hueANeg = c.hueChangesOnlyANeg()
}

// ShippedRawBaseJSON is the raw-base file built into the app
// (render/rawbase.json), for fit-raw to merge a refit into.
func ShippedRawBaseJSON() []byte { return rawBaseJSON }
