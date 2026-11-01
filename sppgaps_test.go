package main

import (
	"strings"
	"testing"
)

// The example from the task, from docs/spp-check.json: two SD1 Merrill frames.
func TestSPPGapLine(t *testing.T) {
	g, ok := sppGapFor("SIGMA SD1 Merrill")
	if !ok {
		t.Fatalf("no table row for the SD1 Merrill (source %q)", sppGapSource)
	}
	if g.Photos != 2 {
		t.Errorf("SD1 Merrill photos = %d, want 2", g.Photos)
	}
	line := g.Line()
	if line != "SD1 Merrill 4.0/255 from Sigma Photo Pro on 2 photos" {
		t.Errorf("line = %q", line)
	}
	if !strings.Contains(line, "Sigma Photo Pro") || !strings.Contains(line, "2 photos") {
		t.Errorf("the line must name the source and the photo count: %q", line)
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
