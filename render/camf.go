package render

import (
	"os"
	"strconv"
	"strings"
)

// camfMode is one in-camera colour mode as the camera's own calibration
// (the CAMF block of every X3F) defines it: the colour matrix the camera
// applies for that mode (CMCM_<mode>) and its contrast offset
// (CMCC_<mode>). The values are constant per camera model (Camsettings
// revision) — checked identical across all 40 calibration files — so they
// are tabled here rather than parsed from each file.
type camfMode struct {
	M        [9]float64
	Contrast float64
}

var camfModes = map[string]map[string]camfMode{
	// Camsettings-F20-SD1Merrill-Rev3.0
	"SD1": {
		"Standard":  {M: identity3, Contrast: 0},
		"Vivid":     {M: [9]float64{1.09679, -0.0732721, -0.0235136, -0.0813805, 1.15301, -0.0716295, -0.0322602, -0.0708569, 1.10312}, Contrast: 0.3},
		"Neutral":   {M: [9]float64{0.908578, 0.0350542, 0.056368, 0.0469052, 0.931717, 0.0213774, 0.0462284, 0.0255636, 0.928208}, Contrast: -0.3},
		"Portrait":  {M: [9]float64{1.04191, -0.0216111, -0.0202983, 0.0217424, 1.0055, -0.0272433, 0.0796864, -0.0849876, 1.0053}, Contrast: -0.25},
		"Landscape": {M: [9]float64{1.0975, -0.0688049, -0.0286943, -0.0534907, 1.14064, -0.0871517, -0.0302884, -0.0834959, 1.11378}, Contrast: 0.25},
		"FCBlue":    {M: [9]float64{1.60778, 0.291276, -0.899056, 0.383872, 1.36512, -0.748988, 0.148312, 0.0812079, 0.770481}, Contrast: 0.3},
	},
	// Camsettings-DP13-SD15-MP
	"SD15": {
		"Standard":  {M: identity3, Contrast: 0},
		"Vivid":     {M: [9]float64{1.35156, -0.292969, -0.0585938, -0.148438, 1.20703, -0.0585938, -0.148438, -0.292969, 1.44141}, Contrast: 0.3},
		"Neutral":   {M: identity3, Contrast: -0.1},
		"Portrait":  {M: [9]float64{1.17, -0.105, -0.065, 0.074, 0.933, -0.007, 0.13, -0.2, 1.09}, Contrast: 0.1},
		"Landscape": {M: [9]float64{1.01429, -0.00698137, -0.00730739, 0.000302745, 0.935506, 0.0641916, 0.12402, -0.417113, 1.29476}, Contrast: 0.1},
	},
}

var identity3 = [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}

// camModeFamily is the camera table a calibration family's colour modes
// come from (Merrill bodies share the SD1's, TRUE II bodies the SD15's).
func camModeFamily(family string) string {
	switch family {
	case "SD1", "DPM":
		return "SD1"
	case "SD15", "SD14", "DP1", "DP2", "TRUEII":
		return "SD15"
	}
	return ""
}

// camModeMatrix is the camera's matrix for colour mode name (camera
// space), nil for Standard, an unknown mode or a camera without a table.
func camModeMatrix(family, name string) *[9]float64 {
	if name == "" || name == "Standard" {
		return nil
	}
	cm, ok := camfModes[camModeFamily(family)][name]
	if !ok {
		return nil
	}
	m := cm.M
	return &m
}

// CameraMode is one of a camera family's own colour modes: its name and
// its contrast offset (SPP contrast units).
type CameraMode struct {
	Name     string
	Contrast float64
}

// CameraModes is the colour modes the camera family ("SD1" or "SD15")
// carries in its calibration, Standard first.
func CameraModes(family string) []CameraMode {
	order := []string{"Standard", "Vivid", "Neutral", "Portrait", "Landscape", "FCBlue"}
	var out []CameraMode
	for _, n := range order {
		if cm, ok := camfModes[family][n]; ok {
			out = append(out, CameraMode{n, cm.Contrast})
		}
	}
	return out
}

// camfSunlight is a SunlightCCMatrix (WB-gained camera RGB to linear output
// RGB; rows sum to 1) of one SD1 and one SD15 body. SPP applies the mode
// matrix in camera space (combined = WB matrix × mode matrix), so in output
// RGB a mode is W·M·W⁻¹. W differs per camera body (each is calibrated at
// the factory: another SD15 has 1.73 -0.91 0.17 …), so the app uses each
// photo's own as-shot matrix (camfUndo's cc) and these only as a fallback.
var camfSunlight = map[string][9]float64{
	"SD1":  {3.13281, -3.54688, 1.41406, -1.70312, 3.86719, -1.16406, 1.10938, -4.85938, 4.75},
	"SD15": {3.46875, -3.35938, 0.890625, -1.98438, 4.40625, -1.42188, 1.0625, -4, 3.9375},
}

func mul3(a, b [9]float64) [9]float64 {
	var c [9]float64
	for r := 0; r < 3; r++ {
		for k := 0; k < 3; k++ {
			c[r*3+k] = a[r*3]*b[k] + a[r*3+1]*b[3+k] + a[r*3+2]*b[6+k]
		}
	}
	return c
}

// camfUndo returns the inverse of the camera's matrix for a colour mode,
// and its contrast offset; ok is false when the camera or mode is unknown.
// cc, when given, is the photo's own as-shot white-balance colour matrix
// read from its CAMF: it differs between camera bodies (each is calibrated
// at the factory), so the table below is only a fallback.
func camfUndo(family, mode, how string, cc *[9]float64) (inv [9]float64, contrast float64, ok bool) {
	cm, found := camfModes[family][strings.TrimSpace(mode)]
	if !found {
		return inv, 0, false
	}
	inv, ok = invert3(cm.M)
	if ok && how == "camf-cam" {
		w := camfSunlight[family]
		if cc != nil {
			w = *cc
		}
		wi, ok2 := invert3(w)
		if !ok2 {
			return inv, 0, false
		}
		inv = mul3(mul3(w, inv), wi)
	}
	return inv, cm.Contrast, ok
}

func invert3(m [9]float64) ([9]float64, bool) {
	a, b, c, d, e, f, g, h, i := m[0], m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8]
	det := a*(e*i-f*h) - b*(d*i-f*g) + c*(d*h-e*g)
	if det == 0 {
		return [9]float64{}, false
	}
	return [9]float64{
		(e*i - f*h) / det, (c*h - b*i) / det, (b*f - c*e) / det,
		(f*g - d*i) / det, (a*i - c*g) / det, (c*d - a*f) / det,
		(d*h - e*g) / det, (b*g - a*h) / det, (a*e - b*d) / det,
	}, true
}

// compModes is how each camera's colour mode is undone, chosen by held-out
// error of the camera-JPEG starting image against SPP (fit-raw, Oct 2026):
//   - SD1: "camf-cam", the CAMF matrix undone in camera space as SPP applies
//     it (W·M⁻¹·W⁻¹) plus its CMCC contrast: 12.8 → 11.4. In linear sRGB
//     (13.6) or gamma (16.3) it is worse than the old guess.
//   - SD15: "old" (hand-tuned chroma/contrast, 9.8); every CAMF variant is
//     worse (10.5-11.3) — the SD15's in-camera JPEG evidently is not SPP's
//     Vivid matrix.
//
// FOVEON_COMP overrides it for calibration ("old", "camf", "camf-gamma",
// "camf-cam").
var compModes = map[string]string{"SD1": "camf-cam", "SD15": "old"}

func compModeFor(family string) string {
	if v := os.Getenv("FOVEON_COMP"); v != "" {
		return v
	}
	if m, ok := compModes[family]; ok {
		return m
	}
	return "old"
}

// compContrast scales CMCC into this app's contrast units
// (FOVEON_COMP_CT, default 1).
var compContrast = func() float64 {
	var v float64 = 1
	if s := os.Getenv("FOVEON_COMP_CT"); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			v = f
		}
	}
	return v
}()
