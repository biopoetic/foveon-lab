package main

import (
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// Task 19: Max Detail is the preset name, and the mode removes the SPP
// slider bundle the raw development has already applied.
func TestMaxDetailIsAName(t *testing.T) {
	for _, n := range []string{"[FL] Max Detail", " [FL] Max Detail "} {
		if !isMaxDetail(n) {
			t.Fatalf("%q not recognised", n)
		}
	}
	for _, n := range []string{"Max Detail", "[FL] Max Detail Lite", ""} {
		if isMaxDetail(n) {
			t.Fatalf("%q wrongly recognised", n)
		}
	}
	p := preset.Params{Exposure: .08, Highlight: -1, FillLight: .8, Saturation: .12, R: 1, G: 1, B: 1, Sharpness: .3}
	q := maxDetailRenderParams(p)
	if q.Exposure != 0 || q.Highlight != 0 || q.FillLight != 0 || q.Blackness != 0 || q.Contrast != 0 {
		t.Fatalf("tone sliders not cleared: %+v", q)
	}
	if !q.MaxDetail || q.Sharpness != p.Sharpness || q.R != 1 {
		t.Fatalf("mode or post-development controls lost: %+v", q)
	}
}
