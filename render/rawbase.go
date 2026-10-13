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
		rawBases[list[i].Family] = &list[i]
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
	if c == nil || c.Photos < 3 {
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

// Pixel maps one linear pixel.
func (c *RawCorrection) Pixel(r, g, b float32) (float32, float32, float32) {
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
