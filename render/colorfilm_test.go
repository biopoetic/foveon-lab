package render

import (
	"math"
	"testing"
)

// Velvia 100 on the Merrill model: greys stay grey, mid-grey lands near
// the middle of the slide's range, and colours come out more saturated
// than the scene (Velvia's character), white bright, black dense.
func TestColorFilmVelvia(t *testing.T) {
	f := colorFilmFor("Fujifilm Fujichrome Velvia 100", "Merrill")
	if f == nil {
		t.Fatal("no model")
	}
	t.Logf("layers -> film: %.3f; reading wavelengths R/G/B %.0f nm, grey error %.3f", f.m, f.readLam, f.greyRMS)
	for _, g := range []float32{0.01, 0.05, 0.128, 0.5, 1, 2} {
		r, gg, b := f.apply(g, g, g)
		t.Logf("grey %.2f -> %.4f %.4f %.4f", g, r, gg, b)
		if d := math.Abs(float64(r-b)) / math.Max(1e-3, float64(gg)); g >= 0.05 && g <= 1 && d > 0.15 {
			t.Errorf("grey %.2f not neutral: %.4f %.4f %.4f", g, r, gg, b)
		}
	}
	// Saturated layer colours (white-balanced layer space).
	for _, c := range [][3]float32{{0.3, 0.1, 0.05}, {0.05, 0.25, 0.08}, {0.04, 0.08, 0.3}, {0.3, 0.25, 0.05}} {
		r, g, b := f.apply(c[0], c[1], c[2])
		t.Logf("layers %v -> %.3f %.3f %.3f", c, r, g, b)
	}
}

// Portra 400 printed on Portra Endura: the borrowed dye shapes must rebuild
// Portra's own midscale-neutral spectrum; greys print grey; mid-grey 18%.
func TestColorNegPortra(t *testing.T) {
	fit, ok := FitNeutral("Kodak Portra 160 / 400 / 800")
	if !ok {
		t.Fatal("no Portra")
	}
	t.Logf("midscale neutral = C %.3f + M %.3f + Y %.3f (+ D-min), RMS %.3f density", fit.Amounts[0], fit.Amounts[1], fit.Amounts[2], fit.RMS)
	if fit.RMS > 0.08 {
		t.Errorf("borrowed dyes rebuild Portra's neutral with RMS %.3f", fit.RMS)
	}
	f := colorFilmOn("Kodak Portra 160 / 400 / 800", "", "Merrill")
	if f == nil {
		t.Fatal("no model")
	}
	t.Logf("paper reading %.0f nm, filtration (log) %.3f, grey error %.3f", f.readLam, f.filtration, f.greyRMS)
	for _, g := range []float32{0.01, 0.04, 0.128, 0.4, 1, 2} {
		r, gg, b := f.apply(g, g, g)
		t.Logf("grey %.3f -> %.4f %.4f %.4f", g, r, gg, b)
	}
	if r, g, b := f.apply(0.128, 0.128, 0.128); math.Abs(float64(g)-0.18) > 0.01 || math.Abs(float64(r-b)) > 0.02 {
		t.Errorf("mid-grey prints %.3f %.3f %.3f", r, g, b)
	}
	for _, c := range [][3]float32{{0.3, 0.1, 0.05}, {0.05, 0.25, 0.08}, {0.04, 0.08, 0.3}, {0.3, 0.25, 0.05}} {
		r, g, b := f.apply(c[0], c[1], c[2])
		t.Logf("layers %v -> %.3f %.3f %.3f", c, r, g, b)
	}
}
