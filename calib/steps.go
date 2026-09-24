// Package calib reverse-engineers SPP's adjustments by measurement: it
// generates a preset pack where each preset moves ONE control to a known
// value, and analyses the images SPP exports with those presets against
// SPP's own neutral export.
package calib

import (
	"fmt"
	"strings"

	"github.com/biopoetic/foveon-lab/preset"
)

// Step is one calibration preset.
type Step struct {
	Code   string  `json:"code"`  // e.g. "A03" — goes into the export file name
	Param  string  `json:"param"` // which control moved ("" = neutral reference)
	Value  float64 `json:"value"`
	Label  string  `json:"label"`
	Params preset.Params
}

// Reference is the neutral step every other export is compared against.
const Reference = "A00"

// neutral is the calibration base. WhiteBalance/ColorTemp use the values
// most presets in the user's packs use; what matters is that they are the
// same in every step, so only the moved control differs.
func neutral() preset.Params {
	p := preset.Neutral()
	p.WhiteBalance, p.ColorTemp, p.ColorMode = 1, 0, preset.ModeStandard
	return p
}

func set(p *preset.Params, param string, v float64) {
	switch param {
	case "Exposure":
		p.Exposure = v
	case "Contrast":
		p.Contrast = v
	case "Shadow":
		p.Blackness = v
	case "Highlight":
		p.Highlight = v
	case "Saturation":
		p.Saturation = v
	case "Sharpness":
		p.Sharpness = v
	case "FillLight":
		p.FillLight = v
	case "ColorAdjustR":
		p.R = v
	case "ColorAdjustG":
		p.G = v
	case "ColorAdjustB":
		p.B = v
	case "ColorMode":
		p.ColorMode = int(v)
	default:
		panic("calib: unknown param " + param)
	}
}

func fmtVal(param string, v float64) string {
	switch {
	case param == "ColorMode":
		return fmt.Sprintf("%d", int(v))
	case strings.HasPrefix(param, "ColorAdjust"):
		return fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("%+.2g", v)
}

// Steps returns the full calibration plan. Tier A (one image, ~23
// exports) answers the important questions; tier B refines the extremes;
// tier C probes the undocumented ColorMode codes.
func Steps() []Step {
	type sv struct {
		param string
		v     float64
	}
	a := []sv{
		{"", 0},
		{"Exposure", 1}, {"Exposure", -1},
		{"Contrast", 0.5}, {"Contrast", 1}, {"Contrast", -0.5}, {"Contrast", -1},
		{"Highlight", -0.5}, {"Highlight", -1}, {"Highlight", 0.5},
		{"Shadow", 0.5}, {"Shadow", -0.5},
		{"Saturation", 0.5}, {"Saturation", 1}, {"Saturation", -0.5}, {"Saturation", -1},
		{"FillLight", 0.5}, {"FillLight", 1},
		{"ColorMode", preset.ModePortrait}, {"ColorMode", preset.ModeLandscape},
		{"ColorAdjustR", 1.1}, {"ColorAdjustB", 1.1},
		{"", 0}, // neutral again: proves SPP output is deterministic
	}
	b := []sv{
		{"Exposure", 2}, {"Exposure", -2},
		{"Contrast", 2}, {"Contrast", -2},
		{"Highlight", -2}, {"Highlight", 1}, {"Highlight", 2},
		{"Shadow", 1}, {"Shadow", -1}, {"Shadow", 2}, {"Shadow", -2},
		{"Saturation", 2}, {"Saturation", -2},
		{"FillLight", 0.25}, {"FillLight", 2}, {"FillLight", -1},
		{"ColorAdjustG", 1.1}, {"ColorAdjustR", 0.9}, {"ColorAdjustG", 0.9}, {"ColorAdjustB", 0.9},
		{"Sharpness", 1}, {"Sharpness", -1},
	}
	var out []Step
	add := func(code string, s sv) {
		p := neutral()
		label := "Neutral"
		if s.param != "" {
			set(&p, s.param, s.v)
			label = s.param + " " + fmtVal(s.param, s.v)
		}
		out = append(out, Step{Code: code, Param: s.param, Value: s.v, Label: label, Params: p})
	}
	for i, s := range a {
		add(fmt.Sprintf("A%02d", i), s)
	}
	out[len(out)-1].Label = "Neutral (check)"
	for i, s := range b {
		add(fmt.Sprintf("B%02d", i+1), s)
	}
	for m := 0; m <= 12; m++ {
		if m == preset.ModeStandard || m == preset.ModePortrait || m == preset.ModeLandscape {
			continue
		}
		add(fmt.Sprintf("C%02d", m), sv{"ColorMode", float64(m)})
	}
	return out
}

// PresetName is how a step appears in SPP's preset list.
func PresetName(s Step) string { return "[CAL] " + s.Code + " " + s.Label }

// PackXML renders the calibration steps as one SPP-importable XML file.
func PackXML(steps []Step) string {
	var blocks []string
	for _, s := range steps {
		blocks = append(blocks, preset.SettingXML(PresetName(s), s.Params))
	}
	return preset.FileXML(blocks...)
}

// ByCode indexes steps by code.
func ByCode(steps []Step) map[string]Step {
	m := map[string]Step{}
	for _, s := range steps {
		m[s.Code] = s
	}
	return m
}
