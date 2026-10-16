package main

// Film stocks as presets: every film the simulator models physically
// (colour slides and negatives from the makers' curves, sensitivities and
// dyes; black-and-white films with a characteristic curve or a spectral
// sensitivity) is a preset of its own, named after the film. Picking one
// in the grid IS the film; Film & Darkroom then refines it (developer,
// push, paper and grade, filtration, grain, format). Generated from the
// film library, so a new film is a new preset.

import (
	"math"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

// filmStockSource labels the film presets in the grid.
const filmStockSource = "Film stock"

func filmPresets() []preset.Preset {
	var out []preset.Preset
	for _, f := range render.Films() {
		key := f.Maker + " " + f.Name
		var group string
		switch {
		case f.Color && f.Kind == "slide":
			group = "SLIDE"
		case f.Color && f.Kind == "cn":
			group = "NEG"
		case f.BW && (f.Curve != nil || len(f.Mono) > 0):
			group = "BW"
		default:
			continue // only grain is known: not a film look of its own
		}
		p := preset.Neutral()
		p.Film = key
		p.Mono = f.BW
		if f.RMS > 0 {
			p.Grain = math.Round(f.Grain*2) / 2
		}
		section := "Color"
		if f.BW {
			section = "BW"
		}
		out = append(out, preset.Preset{
			ID:      idOf("film|" + key),
			Name:    "[" + group + "] " + key,
			Group:   group,
			Source:  filmStockSource,
			Section: section,
			Params:  p,
			Film:    key,
		})
	}
	return out
}

// cameraModePresets: each camera family's own colour modes as presets
// (Standard, Vivid, Neutral, Portrait, Landscape, and FOV Classic Blue on
// Merrill bodies), from Sigma's calibration: the mode's matrix
// (Params.CamMode) and its contrast offset.
func cameraModePresets() []preset.Preset {
	display := map[string]string{"FCBlue": "FOV Classic Blue"}
	var out []preset.Preset
	for _, fam := range []string{"SD1", "SD15"} {
		for _, m := range render.CameraModes(fam) {
			name := m.Name
			if d, ok := display[name]; ok {
				name = d
			}
			p := preset.Neutral()
			p.CamMode = m.Name
			p.Contrast = m.Contrast
			out = append(out, preset.Preset{
				ID:      idOf("cammode|" + fam + "|" + m.Name),
				Name:    "[CAMERA] " + fam + " " + name,
				Group:   "CAMERA",
				Camera:  fam,
				Source:  "Camera modes",
				Section: "Color",
				Params:  p,
			})
		}
	}
	return out
}

// wbPresets: SPP's white balance settings as presets. Each develops the
// raw with the camera's own calibrated balance for that light (SPP's
// WhiteBalance codes 2–7, cameraWBName), so they match SPP on every photo
// and every Foveon body; the same .xml means the same balance in SPP.
func wbPresets() []preset.Preset {
	var out []preset.Preset
	for code := 2; code <= 7; code++ {
		name := cameraWBName(code)
		p := preset.Neutral()
		p.WhiteBalance = code
		out = append(out, preset.Preset{
			ID:      idOf("wb|" + name),
			Name:    "[WB] " + name,
			Group:   "WB",
			Source:  "White balance",
			Section: "Color",
			Params:  p,
		})
	}
	return out
}

// irPresets: the infrared looks (render/irlooks.json), for a full-spectrum
// camera: the IR-cut dust protector removed. On a stock body there is
// almost no infrared to see. The looks are also in the Film & Darkroom
// section's Filter on the lens, beside the Wratten filters and the pad.
func irPresets() []preset.Preset {
	var out []preset.Preset
	for _, f := range []struct{ key, name string }{
		{"ir-fs-aerochrome", "False colour, no lens filter"}, {"ir-aerochrome", "Aerochrome (Wratten 12)"},
		{"ir-ir590", "Colour infrared (OG590)"}, {"ir-ir720", "Infrared B&W (RG715)"},
	} {
		p := preset.Neutral()
		p.Filter = f.key
		section := "Color"
		if f.key == "ir-ir720" {
			p.Mono, section = true, "BW"
		}
		out = append(out, preset.Preset{
			ID:      idOf("ir|" + f.key),
			Name:    "[IR] " + f.name + " – converted camera",
			Group:   "IR",
			Source:  "Infrared",
			Section: section,
			Params:  p,
		})
	}
	return out
}
