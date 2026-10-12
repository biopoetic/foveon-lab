package render

// Per-photo colour balance for previews made from the camera's JPEG (SD15,
// or SD1 with Accurate off). SPP's neutral rendering differs from the
// camera JPEG by a white-balance-like gain that changes from photo to photo
// (and works differently for shots taken with Auto white balance), so it is
// predicted from the JPEG's own average colour: log gain per channel =
// coef · [1, ln R/G, ln B/G, auto, auto·ln B/G, auto·ln R/G], fitted on SPP
// neutral exports (foveon-lab fit-raw).

import (
	_ "embed"
	"encoding/json"
	"math"
)

// JPEGBalance is one camera family's predictor.
type JPEGBalance struct {
	Family string        `json:"family"`
	Use    bool          `json:"use"` // held-out error clearly better than without
	Coef   [3][6]float64 `json:"coef"`
	// Held-out mean error (8-bit) against SPP: with and without the gains.
	HeldOut float64 `json:"heldOut"`
	Without float64 `json:"without"`
	Photos  int     `json:"photos"`
}

//go:embed jpegwb.json
var jpegWBJSON []byte

var jpegWB = map[string]*JPEGBalance{}

func init() {
	var list []JPEGBalance
	if json.Unmarshal(jpegWBJSON, &list) == nil {
		for i := range list {
			jpegWB[list[i].Family] = &list[i]
		}
	}
}

// JPEGBalanceFeatures are the predictor's inputs for an image.
func JPEGBalanceFeatures(l *Linear, autoWB bool) [6]float64 {
	var s [3]float64
	for i := 0; i < len(l.Pix); i += 3 {
		s[0] += float64(l.Pix[i])
		s[1] += float64(l.Pix[i+1])
		s[2] += float64(l.Pix[i+2])
	}
	a := 0.0
	if autoWB {
		a = 1
	}
	lr, lb := math.Log(s[0]/s[1]), math.Log(s[2]/s[1])
	return [6]float64{1, lr, lb, a, a * lb, a * lr}
}

// BalanceJPEG applies the family's predicted gains to l in place (a camera
// JPEG decode) and reports whether it did.
func BalanceJPEG(family string, l *Linear, autoWB bool) bool {
	b := jpegWB[family]
	if b == nil || !b.Use {
		return false
	}
	f := JPEGBalanceFeatures(l, autoWB)
	var g [3]float32
	for k := 0; k < 3; k++ {
		var v float64
		for i := range f {
			v += b.Coef[k][i] * f[i]
		}
		g[k] = float32(math.Exp(math.Max(-1.5, math.Min(1.5, v))))
	}
	for i := 0; i < len(l.Pix); i += 3 {
		l.Pix[i] *= g[0]
		l.Pix[i+1] *= g[1]
		l.Pix[i+2] *= g[2]
	}
	return true
}
