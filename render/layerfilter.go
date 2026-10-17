package render

// Physical lens filters on a Foveon sensor. tools/foveonmodel models each
// generation's layer sensitivities from silicon's absorption (Green 2008)
// and collection depths fitted to the camera's own CAMF colour matrix
// (which then reproduces the CIE observer's colours within 2%), and turns
// each filter's transmission spectrum (Wratten long-pass edges; 81A/82A as
// a ±18 mired black-body ratio) into a 3×3 matrix on white-balanced layers
// (least squares over 400 reflectance spectra in daylight; fit RMS
// 0.0002-0.006). The filter's light loss is kept in the matrix; the render
// compensates exposure like a filter factor.

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed layerfilters.json
var layerFiltersJSON []byte

var layerFilters map[string]struct {
	Model   []float64             `json:"model"`
	Filters map[string][9]float64 `json:"filters"`
}

func init() {
	json.Unmarshal(layerFiltersJSON, &layerFilters)
}

// lensFilter is the named filter's layer-space matrix for a sensor
// generation, or nil.
func lensFilter(sensor, name string) *[9]float64 {
	if name == "" {
		return nil
	}
	if strings.HasPrefix(name, "pad:") {
		return padFilter(sensor, name)
	}
	if strings.HasPrefix(name, "br-") {
		return brandFilterMatrix(sensor, strings.TrimPrefix(name, "br-"))
	}
	g, ok := layerFilters[sensor]
	if !ok {
		return nil
	}
	f, ok := g.Filters[name]
	if !ok {
		return nil
	}
	return &f
}

// SensorGeneration maps a calibration group (x3f.CalibGroup) to the Foveon
// generation the lens filters were modelled for.
func SensorGeneration(group string) string {
	switch group {
	case "SD1", "DPM":
		return "Merrill"
	case "SD15":
		return "TRUEII"
	case "SD9", "SD10":
		return "SD9"
	case "Quattro":
		return "Quattro"
	}
	return ""
}
