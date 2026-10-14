package render

// Infrared looks for a full-spectrum Foveon (the SD bodies' IR-cut "dust
// protector" removed, an IR-passing filter on the lens). tools/foveonmodel
// extends each generation's silicon model to 1100 nm without the IR cut and
// fits, over reflectance spectra of real surfaces (foliage with its red
// edge, sky, skin, water, soil), a 3×3 from the white-balanced layers to the
// bands each look shows: aerochrome (Wratten 12; infrared -> red, red ->
// green, green -> blue, as Kodak Aerochrome EIR), ir590 (590 nm long-pass;
// infrared -> red and green, visible red -> blue) and ir720 (720 nm
// long-pass, black and white). The camera's colour matrix assumes the IR
// cut, so these bypass it: they map the layers straight to the output.

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed irlooks.json
var irLooksJSON []byte

var irLooks map[string]map[string][9]float64

func init() {
	json.Unmarshal(irLooksJSON, &irLooks)
}

// irLook is the named look's matrix (white-balanced layers -> output) for a
// sensor generation, and whether it is black and white; nil if the filter
// is not an infrared look.
func irLook(sensor, filter string) (*[9]float64, bool) {
	name, ok := strings.CutPrefix(filter, "ir-")
	if !ok {
		return nil, false
	}
	m, ok := irLooks[sensor][name]
	if !ok {
		return nil, false
	}
	return &m, name == "ir720"
}
