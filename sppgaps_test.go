package main

import (
	"fmt"
	"strings"
	"testing"
)

// The example from docs/spp-check.json: the DP1 Merrill, a body with many
// scored photos. (It used to be "two SD1 Merrill frames", but those were one
// photo and a copy of it with one property changed; the copy is excluded
// now, so the SD1 Merrill row is a single photo.)
func TestSPPGapLine(t *testing.T) {
	g, ok := sppGapFor("SIGMA DP1 Merrill")
	if !ok {
		t.Fatalf("no table row for the DP1 Merrill (source %q)", sppGapSource)
	}
	if g.Photos < 2 {
		t.Errorf("DP1 Merrill photos = %d, want several", g.Photos)
	}
	line := g.Line()
	// The mean and the count move with every regenerated table; the wording
	// must not.
	want := fmt.Sprintf("DP1 Merrill %.1f/255 from Sigma Photo Pro on %d photos", g.Mean, g.Photos)
	if line != want {
		t.Errorf("line = %q, want %q", line, want)
	}
	if _, ok := sppGapFor("SIGMA SD1 Merrill"); !ok {
		t.Errorf("no table row for the SD1 Merrill (source %q)", sppGapSource)
	}
	if _, ok := sppGapFor("SIGMA No Such Body"); ok {
		t.Error("an unknown body got a gap")
	}
	if sppGapSource == "" {
		t.Error("the table has no source")
	}
}

func TestSPPGapOnePhotoSaysPhoto(t *testing.T) {
	g := sppGap{Body: "DP1S", Mean: 18.0, Photos: 1}
	if got := g.Line(); !strings.HasSuffix(got, "on 1 photo") {
		t.Errorf("one-photo line = %q", got)
	}
}
