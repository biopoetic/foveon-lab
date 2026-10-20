package render

// The SPP emulation measured from Sigma Photo Pro itself (calibration pack:
// one SPP export per slider setting, compared with SPP's neutral export).
// Every tone slider is a measured brightness shift and saturation change as
// a function of pixel brightness; Fill Light is a measured shift as a
// function of pixel AND surrounding brightness. Values between the measured
// settings are interpolated; beyond them the measured step is repeated.

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
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
	// Coef (when Multi): per brightness bin, shift = c0 + c1·(surround at
	// SurroundFracs[0] − pixel) + c2·(surround at [1] − pixel). Replaces Local.
	Multi bool                 `json:"multi,omitempty"`
	Coef  [FillBins][3]float64 `json:"coef,omitempty"`
	// CoefMean (optional, per brightness bin): + c·(frame mean − pixel), the
	// whole frame's mean brightness. SPP's local fill strength follows it;
	// held out (tools/fillstudy, fillstudy10) it took SD1 Fill +0.5 / +1
	// from 5.01 / 9.18 to 3.17 / 6.09. Empty = no such term (the SD15, where
	// it did not help on five photos).
	CoefMean []float64 `json:"coefMean,omitempty"`
}

// Model is a full measured emulation.
type Model struct {
	Source string                `json:"source"` // which photo/camera it was measured on
	Tone   map[string][]ToneStep `json:"tone"`   // "Exposure", "Contrast", "Highlight", "Blackness", "ColorMode6", …
	Fill   []FillStep            `json:"fill"`
	// WB: SPP's WhiteBalance codes that change the image (the neutral 1 and
	// 0 don't; ColorTemp has no effect at all), as a colour mapping.
	WB map[string]*RawCorrection `json:"wb,omitempty"`
	// Mode: colour-mode mappings (matrix + curves) for modes whose effect
	// includes hue shifts the brightness/colourfulness steps can't express.
	Mode map[string]*RawCorrection `json:"mode,omitempty"`
}

// UseModeMapping lists the colour modes rendered with their colour mapping
// instead of tone steps, chosen by measured error on SPP exports: mode 8
// 7.9 -> 1.8, Landscape (7) 1.3 -> 0.6 and its real preset 3.5 -> 2.2. Not
// Portrait (6): alone it's a tie, inside real presets the mapping was worse
// (6.0 -> 8.9). Modes 0-3 and 9-12 all render like one default mode.
var UseModeMapping = map[int]bool{0: true, 1: true, 2: true, 3: true, 5: true, 7: true, 8: true, 9: true, 10: true, 11: true, 12: true}

func (md *Model) modeFor(mode int) *RawCorrection {
	if md == nil || !UseModeMapping[mode] {
		return nil
	}
	return md.Mode[fmt.Sprint(mode)]
}

// wbFor is the measured mapping for WhiteBalance code wb, or nil.
func (md *Model) wbFor(wb int) *RawCorrection {
	if md == nil {
		return nil
	}
	return md.WB[fmt.Sprint(wb)]
}

//go:embed model*.json
var modelFiles embed.FS

// models are the measured emulations by camera family ("" = the default,
// model.json; model_SD15.json etc. override it for that family).
var models = map[string]*Model{}

// model is the default one, nil when none is embedded (then the old
// formulas run).
var model *Model

func init() {
	ents, _ := modelFiles.ReadDir(".")
	for _, e := range ents {
		b, _ := modelFiles.ReadFile(e.Name())
		var m Model
		if json.Unmarshal(b, &m) != nil || len(m.Tone) == 0 {
			continue
		}
		for k := range m.Tone {
			sort.Slice(m.Tone[k], func(a, b int) bool { return m.Tone[k][a].Value < m.Tone[k][b].Value })
		}
		sort.Slice(m.Fill, func(a, b int) bool { return m.Fill[a].Value < m.Fill[b].Value })
		fam := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(e.Name(), "model"), "_"), ".json")
		models[fam] = &m
	}
	model = models[""]
}

// modelFor is the measured model for a camera: the camera's own model when
// there is one ("SD14", measured on that body), else its preset family
// ("SD1", "SD15"), else the default.
func modelFor(family, camera string) *Model {
	if camera != "" {
		if m := models[camera]; m != nil {
			return m
		}
	}
	if m := models[family]; m != nil {
		return m
	}
	return model
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
func (md *Model) newToneOp(param string, v float64) []toneOp {
	if md == nil || v == 0 {
		return nil
	}
	steps := md.Tone[param]
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

// fillBin interpolates a per-bin coefficient between bin centres, as
// fillCoef does.
func fillBin(c []float64, x float64) float64 {
	f := math.Max(0, math.Min(FillBins-1, x*FillBins-0.5))
	b0 := min(int(f), FillBins-2)
	t := f - float64(b0)
	return c[b0]*(1-t) + c[b0+1]*t
}

// usesMean reports whether any step has a frame-mean term.
func (f *fillOp) usesMean() bool {
	for _, s := range f.steps {
		if len(s.CoefMean) > 0 {
			return true
		}
	}
	return false
}

// fillOp is Fill Light at a value: a weighted blend of measured settings.
type fillOp struct {
	steps   []*FillStep
	weights []float64
}

func (md *Model) newFillOp(v float64) *fillOp {
	if md == nil || v == 0 || len(md.Fill) == 0 {
		return nil
	}
	var side []*FillStep // measured settings with v's sign, by size
	for i := range md.Fill {
		if s := &md.Fill[i]; s.Value != 0 && (s.Value > 0) == (v > 0) {
			side = append(side, s)
		}
	}
	if len(side) == 0 { // nothing measured on this side: mirror the smallest
		s := &md.Fill[0]
		for i := range md.Fill {
			if math.Abs(md.Fill[i].Value) < math.Abs(s.Value) {
				s = &md.Fill[i]
			}
		}
		return &fillOp{steps: []*FillStep{s}, weights: []float64{v / s.Value}}
	}
	sort.Slice(side, func(a, b int) bool { return math.Abs(side[a].Value) < math.Abs(side[b].Value) })
	a := math.Abs(v)
	if a <= math.Abs(side[0].Value) || len(side) == 1 {
		s := side[0]
		if a > math.Abs(s.Value) {
			s = side[len(side)-1]
		}
		return &fillOp{steps: []*FillStep{s}, weights: []float64{v / s.Value}}
	}
	for k := 1; k < len(side); k++ {
		lo, hi := side[k-1], side[k]
		if a <= math.Abs(hi.Value) {
			t := (a - math.Abs(lo.Value)) / (math.Abs(hi.Value) - math.Abs(lo.Value))
			return &fillOp{steps: []*FillStep{lo, hi}, weights: []float64{1 - t, t}}
		}
	}
	far := side[len(side)-1] // beyond the largest: scale it
	return &fillOp{steps: []*FillStep{far}, weights: []float64{v / far.Value}}
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
// multi reports whether the steps use the two-scale model.
func (f *fillOp) multi() bool { return len(f.steps) > 0 && f.steps[0].Multi }

// fillLUT: the two-scale Fill Light at one setting is, for each
// brightness x, linear in the two surroundings: d = A(x) + B(x)·da +
// C(x)·db, with a chroma factor of x alone. Baked once per render.
type fillLUT struct{ a, b, c, k [toneLUTN + 1]float32 }

func (f *fillOp) bake(mean float64) *fillLUT {
	if !f.multi() {
		return nil
	}
	t := &fillLUT{}
	for i := 0; i <= toneLUTN; i++ {
		x := float64(i) / toneLUTN
		var a, b, c float64
		k := 1.0
		for j, s := range f.steps {
			w := f.weights[j]
			a0 := fillCoef(&s.Coef, x, 0, 0)
			a += a0 * w
			if len(s.CoefMean) == FillBins && mean > 0 {
				a += fillBin(s.CoefMean, x) * (mean - x) * w
			}
			b += (fillCoef(&s.Coef, x, 1, 0) - a0) * w
			c += (fillCoef(&s.Coef, x, 0, 1) - a0) * w
			k += (knot(&s.Chroma, x) - 1) * math.Abs(w)
		}
		t.a[i], t.b[i], t.c[i], t.k[i] = float32(a), float32(b), float32(c), float32(k)
	}
	return t
}

func (t *fillLUT) apply(x, da, db float64) (float64, float64) {
	f := math.Max(0, math.Min(1, x)) * toneLUTN
	i := min(int(f), toneLUTN-1)
	fr := float32(f - float64(i))
	l := func(v *[toneLUTN + 1]float32) float64 { return float64(v[i] + fr*(v[i+1]-v[i])) }
	return math.Max(0, math.Min(1, x+l(&t.a)+l(&t.b)*da+l(&t.c)*db)), math.Max(0, l(&t.k))
}

func (f *fillOp) apply(x, m, da, db float64) (float64, float64) {
	var d float64
	c := 1.0
	for i, s := range f.steps {
		w := f.weights[i]
		if s.Multi {
			d += fillCoef(&s.Coef, x, da, db) * w
		} else {
			d += bilerp8(&s.Local, m, x) * w
		}
		c += (knot(&s.Chroma, x) - 1) * math.Abs(w)
	}
	return math.Max(0, math.Min(1, x+d)), math.Max(0, c)
}
