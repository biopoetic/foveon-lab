package render

// The SPP emulation measured from Sigma Photo Pro itself (calibration pack:
// one SPP export per slider setting, compared with SPP's neutral export).
// Every tone slider is a measured brightness shift and saturation change as
// a function of pixel brightness; Fill Light is a measured shift as a
// function of pixel AND surrounding brightness. Values between the measured
// settings are interpolated; beyond them the measured step is repeated.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Knots is the number of points a measured curve is sampled at (x = i/32,
// perceptual brightness 0..1).
const Knots = 33

// ToneStep is one measured setting of a tone slider.
type ToneStep struct {
	Value  float64        `json:"value"`
	Delta  [Knots]float64 `json:"delta"`  // output − input brightness (sRGB 0..1)
	Chroma [Knots]float64 `json:"chroma"` // colourfulness out / in
}

// FillStep is one measured Fill Light setting.
type FillStep struct {
	Value float64 `json:"value"`
	// Local[s][p]: brightness shift for a pixel of brightness bin p inside a
	// region of brightness bin s (8×8 bins over 0..1).
	Local  [8][8]float64  `json:"local"`
	Chroma [Knots]float64 `json:"chroma"`
}

// Model is a full measured emulation.
type Model struct {
	Source string                `json:"source"` // which photo/camera it was measured on
	Tone   map[string][]ToneStep `json:"tone"`   // "Exposure", "Contrast", "Highlight", "Blackness", "ColorMode6", …
	Fill   []FillStep            `json:"fill"`
	// WB: SPP's WhiteBalance codes that change the image (the neutral 1 and
	// 0 don't; ColorTemp has no effect at all), as a colour mapping.
	WB map[string]*RawCorrection `json:"wb,omitempty"`
}

// wbFor is the measured mapping for WhiteBalance code wb, or nil.
func wbFor(wb int) *RawCorrection {
	if model == nil {
		return nil
	}
	return model.WB[fmt.Sprint(wb)]
}

//go:embed model.json
var modelJSON []byte

// model is nil when no measurement is embedded (then the old formulas run).
var model *Model

func init() {
	var m Model
	if len(modelJSON) > 0 && json.Unmarshal(modelJSON, &m) == nil && len(m.Tone) > 0 {
		for k := range m.Tone {
			sort.Slice(m.Tone[k], func(a, b int) bool { return m.Tone[k][a].Value < m.Tone[k][b].Value })
		}
		sort.Slice(m.Fill, func(a, b int) bool { return m.Fill[a].Value < m.Fill[b].Value })
		model = &m
	}
}

// MeasuredSource says what the emulation was measured on ("" = formulas).
func MeasuredSource() string {
	if model == nil {
		return ""
	}
	return model.Source
}

func knot(c *[Knots]float64, x float64) float64 {
	f := math.Max(0, math.Min(1, x)) * (Knots - 1)
	i := min(int(f), Knots-2)
	return c[i] + (c[i+1]-c[i])*(f-float64(i))
}

// toneOp is one slider's measured effect at a given value, ready to apply:
// a weighted blend of at most two measured steps (or a repeat of one).
type toneOp struct {
	steps   []*ToneStep
	weights []float64
}

// newToneOp prepares slider param at value v (0 = no-op).
func newToneOp(param string, v float64) []toneOp {
	if model == nil || v == 0 {
		return nil
	}
	steps := model.Tone[param]
	var side []*ToneStep // measured steps on v's side, nearest first
	for i := range steps {
		if s := &steps[i]; s.Value != 0 && (s.Value > 0) == (v > 0) {
			side = append(side, s)
		}
	}
	if len(side) == 0 {
		return nil
	}
	sort.Slice(side, func(a, b int) bool { return math.Abs(side[a].Value) < math.Abs(side[b].Value) })
	a := math.Abs(v)
	far := side[len(side)-1]
	var ops []toneOp
	// Beyond the largest measured setting: apply it whole, repeatedly.
	for a > math.Abs(far.Value)+1e-9 {
		ops = append(ops, toneOp{steps: []*ToneStep{far}, weights: []float64{1}})
		a -= math.Abs(far.Value)
	}
	// Within range: interpolate between neighbours (0 = identity).
	lo, loV := (*ToneStep)(nil), 0.0
	for _, s := range side {
		if math.Abs(s.Value) >= a-1e-9 {
			t := (a - loV) / (math.Abs(s.Value) - loV)
			op := toneOp{steps: []*ToneStep{s}, weights: []float64{t}}
			if lo != nil {
				op.steps, op.weights = append(op.steps, lo), append(op.weights, 1-t)
			}
			ops = append(ops, op)
			break
		}
		lo, loV = s, math.Abs(s.Value)
	}
	return ops
}

// apply maps brightness x (sRGB 0..1) and returns the new brightness and
// the chroma factor.
func (op *toneOp) apply(x float64) (float64, float64) {
	var d, c float64
	var wsum float64
	for i, s := range op.steps {
		d += op.weights[i] * knot(&s.Delta, x)
		c += op.weights[i] * knot(&s.Chroma, x)
		wsum += op.weights[i]
	}
	c += 1 - wsum // the identity's share
	return math.Max(0, math.Min(1, x+d)), math.Max(0, c)
}

// fillOp is Fill Light at a value (measured at a few positive settings).
type fillOp struct {
	step  *FillStep
	scale float64
}

func newFillOp(v float64) *fillOp {
	if model == nil || v == 0 || len(model.Fill) == 0 {
		return nil
	}
	// The nearest measured setting at or above |v|, scaled down linearly;
	// negative values mirror the smallest step.
	a := math.Abs(v)
	best := &model.Fill[len(model.Fill)-1]
	for i := range model.Fill {
		if model.Fill[i].Value >= a {
			best = &model.Fill[i]
			break
		}
	}
	return &fillOp{step: best, scale: v / best.Value}
}

func bilerp8(t *[8][8]float64, s, p float64) float64 {
	fs := math.Max(0, math.Min(7, s*8-0.5))
	fp := math.Max(0, math.Min(7, p*8-0.5))
	s0, p0 := min(int(fs), 6), min(int(fp), 6)
	ts, tp := fs-float64(s0), fp-float64(p0)
	a := t[s0][p0] + (t[s0][p0+1]-t[s0][p0])*tp
	b := t[s0+1][p0] + (t[s0+1][p0+1]-t[s0+1][p0])*tp
	return a + (b-a)*ts
}

// apply lifts brightness x of a pixel whose surroundings have brightness m.
func (f *fillOp) apply(x, m float64) (float64, float64) {
	d := bilerp8(&f.step.Local, m, x) * f.scale
	c := 1 + (knot(&f.step.Chroma, x)-1)*math.Abs(f.scale)
	return math.Max(0, math.Min(1, x+d)), math.Max(0, c)
}
