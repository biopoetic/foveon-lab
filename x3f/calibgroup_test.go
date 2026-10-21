package x3f

import "testing"

func TestCalibGroup(t *testing.T) {
	for model, want := range map[string]string{
		"SIGMA SD1 Merrill": "SD1",
		"SIGMA SD1":         "SD1",
		"SIGMA DP1 Merrill": "DPM",
		"SIGMA DP2 Merrill": "DPM",
		"SIGMA DP3 Merrill": "DPM",
		"SIGMA SD15":        "SD15",
		"SIGMA DP2x":        "DP",
	} {
		if got := CalibGroup(model); got != want {
			t.Errorf("CalibGroup(%q) = %q, want %q", model, got, want)
		}
	}
}
