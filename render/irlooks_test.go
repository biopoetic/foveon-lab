package render

import (
	"math"
	"testing"
)

// Every generation has every infrared look, finite; the 720 nm look is
// black and white (equal rows). Each row's noise gain (its norm) stays at
// the model's bound; the deep 930/1000 nm looks used to run at 20-30x.
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
			for r := 0; r < 3; r++ {
				n := math.Sqrt(m[r*3]*m[r*3] + m[r*3+1]*m[r*3+1] + m[r*3+2]*m[r*3+2])
				if n > 6.01 {
					t.Errorf("%s %s row %d: noise gain %.2f", gen, look, r, n)
				}
			}
			if w := IRWhite(gen, "ir-"+look); w == nil || w[0] <= 0 || w[1] <= 0 || w[2] <= 0 {
				t.Errorf("%s %s: white %v", gen, look, w)
			}
		}
	}
	if m, _ := irLook("Merrill", "red"); m != nil {
		t.Error("a visible lens filter taken as an infrared look")
	}
}

// Only the Merrill generation's infrared is measured; the others are model
// predictions and the UI must label them (Photo.IRApprox).
func TestIRCalibrationStatus(t *testing.T) {
	if !IRCalibrated("Merrill") || IRApproximate("Merrill") {
		t.Error("Merrill should be calibrated")
	}
	for _, gen := range []string{"TRUEII", "SD9", "Quattro"} {
		if IRCalibrated(gen) || !IRApproximate(gen) {
			t.Errorf("%s should be approximate", gen)
		}
	}
	if IRApproximate("") {
		t.Error("an unknown sensor has no IR look to label")
	}
}
