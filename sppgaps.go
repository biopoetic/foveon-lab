package main

// The per-body gap against Sigma Photo Pro, shown in the photo info. The
// table itself is generated from a real spp-check run (tools/sppgap,
// docs/spp-gap.md); this file is only the lookup and the one-line wording.
// A body with no scored reference prints nothing rather than a guess.

import "fmt"

type sppGap struct {
	Body   string  // "SD1 Merrill"
	Model  string  // the camera's own CAMMODEL, e.g. "SIGMA SD1 Merrill"
	Mean   float64 // mean absolute 8-bit error over all pixels
	Area   float64 // the same after both images are reduced 4x
	Photos int     // how many scored photos the mean rests on
}

// Line is the photo-info sentence: "SD1 Merrill 4.0/255 from Sigma Photo Pro
// on 2 photos".
func (g sppGap) Line() string {
	p := "photos"
	if g.Photos == 1 {
		p = "photo"
	}
	return fmt.Sprintf("%s %.1f/255 from Sigma Photo Pro on %d %s", g.Body, g.Mean, g.Photos, p)
}

// sppGapFor finds the table row for a camera model. The table is keyed by
// the model string spp-check recorded (CAMMODEL); an exact match is required
// so one body is never reported with another's error.
func sppGapFor(model string) (sppGap, bool) {
	for _, g := range sppGaps {
		if g.Model == model {
			return g, true
		}
	}
	return sppGap{}, false
}
