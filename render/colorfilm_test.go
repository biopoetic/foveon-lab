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
			if g == 0.128 {
				// The scene's mid-grey must print as mid-grey: the slide's
				// viewing exposure (the eye adapted to the light box).
				y := 0.2126*float64(r) + 0.7152*float64(gg) + 0.0722*float64(b)
				if y < 0.15 || y > 0.21 {
					t.Errorf("%s: mid-grey %.3f, want ~0.18", key, y)
				}
			}
		}
		// Saturated layer colours (white-balanced layer space).
		for _, c := range [][3]float32{{0.3, 0.1, 0.05}, {0.05, 0.25, 0.08}, {0.04, 0.08, 0.3}, {0.3, 0.25, 0.05}} {
			r, g, b := f.apply(c[0], c[1], c[2])
			t.Logf("  layers %v -> %.3f %.3f %.3f", c, r, g, b)
		}
	}
}

// Portra 800 publishes EI 1600 (push 1) and EI 3200 (push 2) characteristic
// families on E-4040 p. 4. Each is a selectable push of the same library
// film, not a duplicate entry; no other negative in the library publishes a
// push family.
func TestColorNegativePush(t *testing.T) {
	pushes := ColorNegPushes("Kodak Portra 800")
	if len(pushes) != 2 || pushes[0].EI != 1600 || pushes[1].EI != 3200 {
		t.Fatalf("Portra 800 pushes %v", pushes)
	}
	// The published D-min and midscale-neutral spectra are one set for all
	// three families; the drawn part of the neutral exposure below covers the
	// pushed one too, since push moves the exposure, not the neutral colour.
	if fit, ok := FitNeutral("Kodak Portra 800"); !ok || fit.RMS > 0.10 {
		t.Errorf("Portra 800 neutral fit %v, ok %v", fit, ok)
	} else {
		t.Logf("neutral rebuilt: C %.3f + M %.3f + Y %.3f, RMS %.3f D",
			fit.Amounts[0], fit.Amounts[1], fit.Amounts[2], fit.RMS)
	}
	if pushes[0].Stops != 1 || pushes[1].Stops != 2 {
		t.Errorf("Portra 800 push stops %v", pushes)
	}
	for k := range colorNegs().negs {
		if k != "Kodak Portra 800" && len(ColorNegPushes(k)) > 0 {
			t.Errorf("%s publishes push families that are not wired in", k)
		}
	}
	if f := colorFilmOn("Kodak Portra 800", "", "Merrill", ColorPrint{}); f == nil || f.ei != 800 {
		t.Errorf("box speed Portra 800: %v", f)
	}
	for _, p := range pushes {
		f := colorFilmOnPush("Kodak Portra 800", "", "Merrill", p.Stops, ColorPrint{})
		if f == nil {
			t.Fatalf("no model at push %g", p.Stops)
		}
		if f.ei != p.EI {
			t.Errorf("push %g: EI %.0f, want %.0f", p.Stops, f.ei, p.EI)
		}
		t.Logf("push %g (EI %.0f): grey error %.3f, reading R/G/B %.0f/%.0f/%.0f nm",
			p.Stops, f.ei, f.greyRMS, f.readLam[0], f.readLam[1], f.readLam[2])
		for _, g := range []float32{0.04, 0.128, 0.4, 1} {
			r, gg, b := f.apply(g, g, g)
			rg := math.Log10(math.Max(1e-9, float64(r/gg)))
			bg := math.Log10(math.Max(1e-9, float64(b/gg)))
			t.Logf("  grey %.3f -> %.4f %.4f %.4f (R-G %.4f, B-G %.4f log10)", g, r, gg, b, rg, bg)
			if g == 0.128 {
				if math.Abs(float64(gg)-0.18) > 0.01 || math.Abs(float64(r-b)) > 0.02 {
					t.Errorf("push %g: mid-grey prints %.4f %.4f %.4f", p.Stops, r, gg, b)
				}
			}
			if math.Abs(rg) > 0.05 || math.Abs(bg) > 0.05 {
				t.Errorf("push %g: grey %.2f has a cast: R-G %.4f, B-G %.4f log10", p.Stops, g, rg, bg)
			}
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
			f := colorFilmOn(key, paper, "Merrill", ColorPrint{})
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

// The colour darkroom works like the real one: adding a colour's filter
// takes that colour out of the print, more exposure prints darker; no
// filtration is the neutral print.
func TestColorPrintFiltration(t *testing.T) {
	const neg = "Kodak Portra 400"
	g := float32(0.128)
	grey := func(cp ColorPrint) [3]float64 {
		f := colorFilmOn(neg, "", "Merrill", cp)
		if f == nil {
			t.Fatal("no model")
		}
		r, gg, b := f.apply(g, g, g)
		return [3]float64{float64(r), float64(gg), float64(b)}
	}
	base := grey(ColorPrint{})
	t.Logf("neutral %.4f %.4f %.4f", base[0], base[1], base[2])
	for _, c := range []struct {
		name string
		cp   ColorPrint
		ok   func(v [3]float64) bool
	}{
		{"+20Y: less yellow (bluer)", ColorPrint{Y: 20}, func(v [3]float64) bool { return v[2]/v[1] > base[2]/base[1]*1.1 }},
		{"+20M: less magenta (greener)", ColorPrint{M: 20}, func(v [3]float64) bool { return v[1]/v[0] > base[1]/base[0]*1.1 }},
		{"+20C: less cyan (redder)", ColorPrint{C: 20}, func(v [3]float64) bool { return v[0]/v[1] > base[0]/base[1]*1.1 }},
		{"+1 stop: darker", ColorPrint{Exp: 1}, func(v [3]float64) bool { return v[1] < base[1]*0.7 }},
		{"-1 stop: lighter", ColorPrint{Exp: -1}, func(v [3]float64) bool { return v[1] > base[1]*1.3 }},
	} {
		v := grey(c.cp)
		t.Logf("%-28s %.4f %.4f %.4f", c.name, v[0], v[1], v[2])
		if !c.ok(v) {
			t.Errorf("%s: got %.4f %.4f %.4f", c.name, v[0], v[1], v[2])
		}
	}
	if v := grey(ColorPrint{}); v != base {
		t.Errorf("neutral print changed after filtering: %v vs %v", v, base)
	}
}
