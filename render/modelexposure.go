package render

import (
	"math"
	"strings"
)

// modelExposureEV is how much brighter (in stops) Sigma Photo Pro develops a
// camera model's raw than the rest of its calibration group: a group's raw
// base fits the group's mean brightness, but within a group the bodies
// differ. Measured with spp-check (mean linear luminance vs SPP's neutral
// export, October 2026); listed only where the other photos' offset,
// applied to a held-out photo, moved it clearly closer to SPP (mean |EV|):
//
//	sd Quattro H  13 photos  -0.35  0.35 -> 0.08
//	dp1 Quattro    6         +0.22  0.22 -> 0.05
//	dp2 Quattro   13         +0.09  0.085 -> 0.013
//	dp0 Quattro    6         +0.06  0.06 -> 0.02
//	sd Quattro     5         -0.09  0.09 -> 0.04
//	DP2            9         -0.49  0.49 -> 0.17
//
// Every other model was within a few hundredths of a stop with no
// held-out gain, so it gets none. Keys are the X3F model names, upper case.
var modelExposureEV = map[string]float64{
	"SIGMA SD QUATTRO H": 0.35,
	"SIGMA DP1 QUATTRO":  -0.22,
	"SIGMA DP2 QUATTRO":  -0.09,
	"SIGMA DP0 QUATTRO":  -0.06,
	"SIGMA SD QUATTRO":   0.09,
	"SIGMA DP2":          0.49,
}

// ModelExposure is the linear gain that brings a model's raw to SPP's
// brightness (1 for models without a measured offset).
func ModelExposure(model string) float32 {
	m := strings.ToUpper(strings.TrimSpace(model))
	if !strings.HasPrefix(m, "SIGMA ") {
		m = "SIGMA " + m
	}
	if ev, ok := modelExposureEV[m]; ok {
		return float32(math.Exp2(ev))
	}
	return 1
}
