package calib

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/biopoetic/foveon-lab/render"
)

// toneParams are the sliders emulated as measured tone curves, keyed by the
// calibration step's Param; ColorMode steps become "ColorMode<code>".
var toneParams = map[string]string{"Exposure": "Exposure", "Contrast": "Contrast", "Highlight": "Highlight", "Shadow": "Blackness"}

// BuildModel turns one photo's measurements into the renderer's measured
// emulation. The neutral re-export (A22, else A00 itself) is the baseline.
func BuildModel(res *Result, image string) (*render.Model, error) {
	var ms []Measurement
	var base *Measurement
	for i, m := range res.Measurements {
		if m.Image != image {
			continue
		}
		ms = append(ms, m)
		if m.Param == "" {
			base = &res.Measurements[i]
		}
	}
	if len(ms) == 0 {
		return nil, fmt.Errorf("no measurements for %s", image)
	}
	if base == nil {
		return nil, errors.New("the neutral check export (A22) is needed as the baseline")
	}
	mod := &render.Model{Source: image, Tone: map[string][]render.ToneStep{}}
	for _, m := range ms {
		key, ok := toneParams[m.Param]
		if m.Param == "ColorMode" {
			key, ok = fmt.Sprintf("ColorMode%d", int(m.Value)), true
			// Colour modes also shift hues (e.g. 8): keep a colour mapping
			// too; the renderer uses it where it fits better.
			if m.WBFit != nil {
				if mod.Mode == nil {
					mod.Mode = map[string]*render.RawCorrection{}
				}
				mod.Mode[fmt.Sprint(int(m.Value))] = m.WBFit
			}
		}
		switch {
		case ok:
			st := render.ToneStep{Value: m.Value}
			if m.Param == "ColorMode" {
				st.Value = 1 // on/off
			}
			st.Delta, st.Chroma = knots(m, base)
			mod.Tone[key] = append(mod.Tone[key], st)
		case m.Param == "WhiteBalance" && m.WBFit != nil:
			if mod.WB == nil {
				mod.WB = map[string]*render.RawCorrection{}
			}
			key := fmt.Sprint(int(m.Value))
			if _, dup := mod.WB[key]; !dup { // ColorTemp variants are identical
				mod.WB[key] = m.WBFit
			}
		case m.Param == "FillLight":
			fs := render.FillStep{Value: m.Value}
			_, fs.Chroma = knots(m, base)
			fs.Local = fillTable(m.Local, base.Local)
			mod.Fill = append(mod.Fill, fs)
		}
	}
	return mod, nil
}

// knots samples a measurement as brightness shift and chroma ratio at
// x = i/32. Each bin's points sit at the bin's actual mean input brightness
// (the baseline's output, since baseline out = in).
func knots(m Measurement, base *Measurement) (delta, chroma [render.Knots]float64) {
	type pt struct{ x, d, c float64 }
	var pts []pt
	for b := 0; b < Bins; b++ {
		if m.Curve[b] < 0 || base.Curve[b] < 0 {
			continue
		}
		c := 1.0
		if m.Chroma[b] > 0 && base.Chroma[b] > 0 {
			c = math.Max(0, math.Min(4, m.Chroma[b]/base.Chroma[b]))
		}
		pts = append(pts, pt{base.Curve[b], m.Curve[b] - base.Curve[b], c})
	}
	if len(pts) == 0 {
		for i := range chroma {
			chroma[i] = 1
		}
		return
	}
	// Black stays black; the first chroma value is carried down.
	pts = append([]pt{{0, 0, pts[0].c}}, pts...)
	for i := 0; i < render.Knots; i++ {
		x := float64(i) / (render.Knots - 1)
		j := 0
		for j < len(pts)-1 && pts[j+1].x < x {
			j++
		}
		if j == len(pts)-1 { // above the last measured bin: hold
			delta[i], chroma[i] = pts[j].d, pts[j].c
			continue
		}
		a, b := pts[j], pts[j+1]
		t := 0.0
		if b.x > a.x {
			t = math.Max(0, math.Min(1, (x-a.x)/(b.x-a.x)))
		}
		delta[i], chroma[i] = a.d+(b.d-a.d)*t, a.c+(b.c-a.c)*t
	}
	return
}

// fillTable subtracts the baseline and fills cells without data (-9) from
// their nearest measured neighbour in the same row, else the same column.
func fillTable(t, base [8][8]float64) [8][8]float64 {
	var out [8][8]float64
	has := func(v float64) bool { return v > -8 }
	var ok [8][8]bool
	for s := 0; s < 8; s++ {
		for p := 0; p < 8; p++ {
			if has(t[s][p]) {
				out[s][p], ok[s][p] = t[s][p], true
				if has(base[s][p]) {
					out[s][p] -= base[s][p]
				}
			}
		}
	}
	nearest := func(s, p int) (float64, bool) {
		for d := 1; d < 8; d++ {
			for _, q := range []int{p - d, p + d} {
				if q >= 0 && q < 8 && ok[s][q] {
					return out[s][q], true
				}
			}
		}
		for d := 1; d < 8; d++ {
			for _, r := range []int{s - d, s + d} {
				if r >= 0 && r < 8 && ok[r][p] {
					return out[r][p], true
				}
			}
		}
		return 0, false
	}
	var filled [8][8]float64
	for s := 0; s < 8; s++ {
		for p := 0; p < 8; p++ {
			filled[s][p] = out[s][p]
			if !ok[s][p] {
				filled[s][p], _ = nearest(s, p)
			}
		}
	}
	return filled
}

// MergeModels averages models measured on different photos, so the
// emulation describes the camera rather than one scene: tone and Fill Light
// steps with the same setting are averaged point by point; white-balance and
// colour-mode mappings are taken from the first model that has them.
func MergeModels(ms []*render.Model) *render.Model {
	out := &render.Model{Tone: map[string][]render.ToneStep{}}
	type acc struct {
		st render.ToneStep
		n  float64
	}
	tone := map[string]map[float64]*acc{}
	fills := map[float64]*struct {
		fs render.FillStep
		n  float64
	}{}
	var srcs []string
	for _, m := range ms {
		srcs = append(srcs, m.Source)
		for k, steps := range m.Tone {
			if tone[k] == nil {
				tone[k] = map[float64]*acc{}
			}
			for _, s := range steps {
				a := tone[k][s.Value]
				if a == nil {
					a = &acc{st: render.ToneStep{Value: s.Value}}
					tone[k][s.Value] = a
				}
				for i := range s.Delta {
					a.st.Delta[i] += s.Delta[i]
					a.st.Chroma[i] += s.Chroma[i]
				}
				a.n++
			}
		}
		for _, f := range m.Fill {
			a := fills[f.Value]
			if a == nil {
				a = &struct {
					fs render.FillStep
					n  float64
				}{fs: render.FillStep{Value: f.Value}}
				fills[f.Value] = a
			}
			for i := range f.Chroma {
				a.fs.Chroma[i] += f.Chroma[i]
			}
			for s := range f.Local {
				for p := range f.Local[s] {
					a.fs.Local[s][p] += f.Local[s][p]
				}
			}
			a.n++
		}
		for k, v := range m.WB {
			if out.WB == nil {
				out.WB = map[string]*render.RawCorrection{}
			}
			if out.WB[k] == nil {
				out.WB[k] = v
			}
		}
		for k, v := range m.Mode {
			if out.Mode == nil {
				out.Mode = map[string]*render.RawCorrection{}
			}
			if out.Mode[k] == nil {
				out.Mode[k] = v
			}
		}
	}
	for k, byV := range tone {
		for _, a := range byV {
			for i := range a.st.Delta {
				a.st.Delta[i] /= a.n
				a.st.Chroma[i] /= a.n
			}
			out.Tone[k] = append(out.Tone[k], a.st)
		}
	}
	for _, a := range fills {
		for i := range a.fs.Chroma {
			a.fs.Chroma[i] /= a.n
		}
		for s := range a.fs.Local {
			for p := range a.fs.Local[s] {
				a.fs.Local[s][p] /= a.n
			}
		}
		out.Fill = append(out.Fill, a.fs)
	}
	out.Source = strings.Join(srcs, "+")
	return out
}
