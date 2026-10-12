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

// Apply maps l (an x3f_extract decode, linear) in place.
func (c *RawCorrection) Apply(l *Linear) {
	m := c.Matrix
	parallelRows(l.H, func(y0, y1 int) {
		for i := y0 * l.W; i < y1*l.W; i++ {
			j := i * 3
			r, g, b := float64(l.Pix[j]), float64(l.Pix[j+1]), float64(l.Pix[j+2])
			v := [3]float64{m[0]*r + m[1]*g + m[2]*b, m[3]*r + m[4]*g + m[5]*b, m[6]*r + m[7]*g + m[8]*b}
			for k := 0; k < 3; k++ {
				l.Pix[j+k] = dec(float32(c.curve(k, float64(enc(float32(max(0, min(1, v[k]))))))))
			}
		}
	})
}
