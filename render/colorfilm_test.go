package render

import (
	"math"
	"sort"
	"testing"
)

// Every slide film on the Merrill model: greys stay grey, mid-grey lands
// near the middle of the slide's range, white bright, black dense.
func TestColorFilmSlides(t *testing.T) {
	keys := []string{}
	for k := range colorFilmData_() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) < 2 {
		t.Errorf("slides %v", keys)
	}
	for _, key := range keys {
		f := colorFilmFor(key, "Merrill")
		if f == nil {
			t.Fatalf("no model for %s", key)
		}
		t.Logf("%s: layers -> film %.3f; reading wavelengths R/G/B %.0f nm, grey error %.3f", key, f.rp, f.readLam, f.greyRMS)
		for _, g := range []float32{0.01, 0.05, 0.128, 0.5, 1, 2} {
			r, gg, b := f.apply(g, g, g)
			t.Logf("  grey %.2f -> %.4f %.4f %.4f", g, r, gg, b)
			if d := math.Abs(float64(r-b)) / math.Max(1e-3, float64(gg)); g >= 0.05 && g <= 1 && d > 0.15 {
				t.Errorf("%s: grey %.2f not neutral: %.4f %.4f %.4f", key, g, r, gg, b)
			}
		}
		// Saturated layer colours (white-balanced layer space).
		for _, c := range [][3]float32{{0.3, 0.1, 0.05}, {0.05, 0.25, 0.08}, {0.04, 0.08, 0.3}, {0.3, 0.25, 0.05}} {
			r, g, b := f.apply(c[0], c[1], c[2])
			t.Logf("  layers %v -> %.3f %.3f %.3f", c, r, g, b)
		}
	}
}

// Every colour negative on every colour paper: the borrowed dye shapes
// must rebuild the film's own midscale-neutral spectrum; greys print grey;
// mid-grey prints 18%.
func TestColorNegatives(t *testing.T) {
	if len(ColorPapers()) < 2 {
		t.Errorf("colour papers %v", ColorPapers())
	}
	keys := []string{}
	for k := range colorNegs().negs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) < 2 {
		t.Errorf("negatives %v", keys)
	}
	for _, key := range keys {
		fit, ok := FitNeutral(key)
		if !ok {
			t.Fatalf("no fit for %s", key)
		}
		t.Logf("%s: midscale neutral = C %.3f + M %.3f + Y %.3f (+ D-min), RMS %.3f density", key, fit.Amounts[0], fit.Amounts[1], fit.Amounts[2], fit.RMS)
		// Kodak's Vision3 dyes rebuild Portra's neutral to 0.058 and Fuji
		// Superia's to 0.098 (Fuji's yellow and magenta peak ~12 nm further
		// red). Moving the dyes to fit (tried) brought the neutral to 0.02-0.05
		// but printed the grey scale less neutral, so the dyes stay as
		// published.
		if fit.RMS > 0.10 {
			t.Errorf("%s: borrowed dyes rebuild the neutral with RMS %.3f", key, fit.RMS)
		}
		for _, paper := range ColorPapers() {
			f := colorFilmOn(key, paper, "Merrill")
			if f == nil {
				t.Fatalf("no model for %s on %s", key, paper)
			}
			t.Logf("  on %s: paper reading %.0f nm, filtration (log) %.3f, grey error %.3f", paper, f.readLam, f.filtration, f.greyRMS)
			for _, g := range []float32{0.01, 0.04, 0.128, 0.4, 1, 2} {
				r, gg, b := f.apply(g, g, g)
				t.Logf("    grey %.3f -> %.4f %.4f %.4f", g, r, gg, b)
			}
			if r, g, b := f.apply(0.128, 0.128, 0.128); math.Abs(float64(g)-0.18) > 0.01 || math.Abs(float64(r-b)) > 0.02 {
				t.Errorf("%s on %s: mid-grey prints %.3f %.3f %.3f", key, paper, r, g, b)
			}
			for _, c := range [][3]float32{{0.3, 0.1, 0.05}, {0.05, 0.25, 0.08}, {0.04, 0.08, 0.3}, {0.3, 0.25, 0.05}} {
				r, g, b := f.apply(c[0], c[1], c[2])
				t.Logf("    layers %v -> %.3f %.3f %.3f", c, r, g, b)
			}
		}
	}
}
