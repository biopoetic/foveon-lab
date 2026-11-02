package main

// Task 37: the base-ISO denoise strength is a property of the photo, not of
// the view or the caller. SPP keeps more finest-scale texture on DPM and
// Quattro base-ISO frames than our default 0.3; 0.15 recovers it. Every
// other body, high ISO, an unknown ISO and an explicit -dn/-nodenoise keep
// the strength they were given (including -dn3, which equals the old
// default numerically).

import "testing"

func TestBaseNoiseStrength(t *testing.T) {
	ph := func(model, iso string) *Photo {
		return &Photo{props: map[string]string{"CAMMODEL": model, "ISO": iso}}
	}
	cases := []struct {
		name     string
		model    string
		iso      string
		strength float64
		explicit bool
		want     float64
	}{
		{"DPM base", "SIGMA DP1 Merrill", "100", defaultNR, false, baseNR},
		{"DPM 400", "SIGMA DP2 Merrill", "400", defaultNR, false, baseNR},
		{"DPM 800", "SIGMA DP1 Merrill", "800", defaultNR, false, defaultNR},
		{"DPM 6400", "SIGMA DP3 Merrill", "6400", defaultNR, false, defaultNR},
		{"Quattro base", "SIGMA dp2 Quattro", "200", defaultNR, false, baseNR},
		{"Quattro H 400", "sd Quattro H", "400", defaultNR, false, baseNR},
		{"Quattro 1600", "SIGMA dp0 Quattro", "1600", defaultNR, false, defaultNR},
		{"DP2 base untouched", "SIGMA DP2", "100", defaultNR, false, defaultNR},
		{"SD14 base untouched", "SIGMA SD14", "100", defaultNR, false, defaultNR},
		{"SD1 base untouched", "SIGMA SD1 Merrill", "100", defaultNR, false, defaultNR},
		{"unknown ISO", "SIGMA DP1 Merrill", "", defaultNR, false, defaultNR},
		{"explicit medium equals old default", "SIGMA DP1 Merrill", "100", defaultNR, true, defaultNR},
		{"explicit low", "SIGMA DP1 Merrill", "100", 0.2, true, 0.2},
		{"explicit full", "SIGMA dp2 Quattro", "100", 1.0, true, 1.0},
		{"off", "SIGMA DP1 Merrill", "100", 0, true, 0},
	}
	for _, c := range cases {
		if got := denoiseStrengthFor(ph(c.model, c.iso), c.strength, c.explicit); got != c.want {
			t.Errorf("%s: got %g, want %g", c.name, got, c.want)
		}
	}
}
