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
	// Tone: after the balance, a brightness curve per photo, predicted per
	// bin from the image's brightness statistics (ToneFeatures).
	ToneUse     bool                 `json:"toneUse"`
	Tone        [ToneBins][6]float64 `json:"tone"`
	HeldOutTone float64              `json:"heldOutTone"`
}

// ToneBins is the resolution of the per-photo tone curve.
const ToneBins = 16

// ToneFeatures: [1, mean, 5th, 50th, 95th percentile brightness, auto WB].
func ToneFeatures(l *Linear, autoWB bool) [6]float64 {
	n := l.W * l.H
	var hist [256]int
	var mean float64
	for i := 0; i < n; i++ {
		j := i * 3
		v := enc(0.2126*l.Pix[j] + 0.7152*l.Pix[j+1] + 0.0722*l.Pix[j+2])
		mean += float64(v)
		hist[min(255, int(v*255))]++
	}
	pct := func(p float64) float64 {
		want, acc := int(p*float64(n)), 0
		for b, c := range hist {
			acc += c
			if acc > want {
				return float64(b) / 255
			}
		}
		return 1
	}
	a := 0.0
	if autoWB {
		a = 1
	}
	return [6]float64{1, mean / float64(n), pct(0.05), pct(0.5), pct(0.95), a}
}

// ApplyTone shifts l's brightness by the curve delta (sRGB, per bin
// centre), keeping colour ratios.
func ApplyTone(l *Linear, delta [ToneBins]float64) {
	parallelRows(l.H, func(y0, y1 int) {
		for i := y0 * l.W * 3; i < y1*l.W*3; i += 3 {
			y := 0.2126*l.Pix[i] + 0.7152*l.Pix[i+1] + 0.0722*l.Pix[i+2]
			if y <= 1e-6 {
				continue
			}
			x := float64(enc(y))
			f := math.Max(0, math.Min(ToneBins-1, x*ToneBins-0.5))
			b0 := min(int(f), ToneBins-2)
			t := f - float64(b0)
			nx := math.Max(0, math.Min(1, x+delta[b0]*(1-t)+delta[b0+1]*t))
			k := dec(float32(nx)) / y
			l.Pix[i], l.Pix[i+1], l.Pix[i+2] = l.Pix[i]*k, l.Pix[i+1]*k, l.Pix[i+2]*k
		}
	})
}

// PredictTone is the curve the coefficients give for features f.
func PredictTone(c *[ToneBins][6]float64, f [6]float64) [ToneBins]float64 {
	var d [ToneBins]float64
	for b := range d {
		for i := range f {
			d[b] += c[b][i] * f[i]
		}
		d[b] = math.Max(-0.25, math.Min(0.25, d[b]))
	}
	return d
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
	if b.ToneUse {
		ApplyTone(l, PredictTone(&b.Tone, ToneFeatures(l, autoWB)))
	}
	return true
}
