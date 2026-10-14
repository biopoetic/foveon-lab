package render

import (
	"math"
	"testing"
)

// Every generation has every infrared look, finite; the 720 nm look is
// black and white (equal rows). The matrices take the camera's
// Sunlight-balanced layers, so white does not map to (1,1,1) any more: with
// the IR cut out, a white surface gives the bottom layer ~5x its signal.
func TestIRLooks(t *testing.T) {
	for _, gen := range []string{"Merrill", "TRUEII", "SD9", "Quattro"} {
		for _, look := range []string{"fs-aerochrome", "aerochrome", "ir590", "ir720"} {
			m, mono := irLook(gen, "ir-"+look)
			if m == nil {
				t.Fatalf("%s %s missing", gen, look)
			}
			for _, v := range m {
				if math.IsNaN(v) || math.Abs(v) > 20 {
					t.Errorf("%s %s coefficient %g", gen, look, v)
				}
			}
			if mono != (look == "ir720") || (mono && (m[0] != m[3] || m[1] != m[7])) {
				t.Errorf("%s %s mono %v", gen, look, mono)
			}
		}
	}
	if m, _ := irLook("Merrill", "red"); m != nil {
		t.Error("a visible lens filter taken as an infrared look")
	}
}
