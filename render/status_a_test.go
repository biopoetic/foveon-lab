package render

// Task 72: the reported densities are read through the published ISO 5-3
// Status A spectral products, not at one wavelength per channel. These tests
// pin the table to the printed values and the integrated inverse to the
// forward integral; the measured wedge and the 14-photo tables are in
// REPORT-72.md. Task 73 keeps the integrated read only on the films whose
// mid-grey registration and measured grey balance support it, so the inverse
// test runs on that kept list (slide_registration_test.go).

import (
	"math"
	"testing"
)

// Task 72: guards the ISO 5-3 Status A table against the printed values and edge slopes.
// A failure means the table was re-digitised wrong or its normalization/peaks moved.
// Change only when the published standard's table is re-read; update every printed point and cite it.
func TestStatusATable(t *testing.T) {
	d := statusA_()
	if d.Normalized != 5 {
		t.Errorf("normalized %.1f, want 5", d.Normalized)
	}
	if d.URL == "" || d.Source == "" || d.SHA256 == "" {
		t.Errorf("citation incomplete: %+v", d)
	}
	// The printed values either side of each peak, plus the edge slopes.
	checks := []struct {
		ch   string
		lam  float64
		want float64
	}{
		{"B", 420, 3.602}, {"B", 440, 5.000}, {"B", 500, 0.165},
		{"G", 500, 1.650}, {"G", 530, 5.000}, {"G", 590, 1.579},
		{"R", 600, 2.568}, {"R", 620, 5.000}, {"R", 750, 0.341},
	}
	for _, c := range checks {
		if got := d.Channels[c.ch].logAt(c.lam); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("Status A %s at %.0f nm = %.4f, printed %.4f", c.ch, c.lam, got, c.want)
		}
	}
	for _, c := range []struct {
		ch      string
		below   float64
		above   float64
		last    float64
		first   float64
		minPeak float64
	}{
		{"B", 0.380, -0.140, 500, 420, 440},
		{"G", 0.220, -0.170, 590, 500, 530},
		{"R", 0.270, -0.040, 750, 600, 620},
	} {
		ch := d.Channels[c.ch]
		if ch.Below[1] != c.below || ch.Above[1] != c.above {
			t.Errorf("Status A %s edge slopes %v/%v, printed %.3f/%.3f", c.ch, ch.Below, ch.Above, c.below, c.above)
		}
		if ch.Points[0][0] != c.first || ch.Points[len(ch.Points)-1][0] != c.last {
			t.Errorf("Status A %s spans %v..%v, printed %.0f..%.0f", c.ch, ch.Points[0][0], ch.Points[len(ch.Points)-1][0], c.first, c.last)
		}
		g := statusAGrid()
		peak, at := 0.0, 0.0
		for i, l := range g.lams {
			if v := g.s[map[string]int{"R": 0, "G": 1, "B": 2}[c.ch]][i]; v > peak {
				peak, at = v, l
			}
		}
		if math.Abs(at-c.minPeak) > 5 || math.Abs(peak-1) > 1e-9 {
			t.Errorf("Status A %s peak %.4f at %.0f nm, want 1 at %.0f", c.ch, peak, at, c.minPeak)
		}
	}
}

// Task 72: guards the integrated Status A inverse on the kept films (round trip <= 0.005 D).
// A failure means the forward/inverse products no longer agree or a dye amount went negative.
// Change only when the Status A read or the kept-film list changes; re-run on that list.
func TestStatusAIntegrated(t *testing.T) {
	for _, key := range statusASlideFilms {
		f := colorFilmFor(key, "Merrill")
		if f == nil {
			t.Fatalf("%s: no model", key)
		}
		if !f.integrated {
			t.Errorf("%s: slide is not read through Status A", key)
			continue
		}
		d := colorFilmData_()[key]
		lg := math.Log10(10 / d.ISO)
		for s := -4.0; s <= 2.001; s += 0.5 {
			l := lg + s*math.Log10(2)
			D := [3]float64{f.density(0, l), f.density(1, l), f.density(2, l)}
			c, iters, res := f.invertStatusA(D)
			if res > 5e-3 {
				t.Errorf("%s stop %+.1f: inverse residual %.3g D after %d iterations", key, s, res, iters)
			}
			D2 := f.statusADensity(c)
			for k := 0; k < 3; k++ {
				if math.Abs(D2[k]-D[k]) > 5e-3 {
					t.Errorf("%s stop %+.1f channel %d: %.4f -> %.4f (%.2g D)", key, s, k, D[k], D2[k], math.Abs(D2[k]-D[k]))
				}
				if c[k] < 0 {
					t.Errorf("%s stop %+.1f: negative dye amount %.3f", key, s, c[k])
				}
			}
		}
		// A known dye amount: forward through the standard's products, then
		// back; the round trip must stay inside 0.005 D.
		c := [3]float64{1.2, 0.8, 0.6}
		D := f.statusADensity(c)
		got, _, res := f.invertStatusA(D)
		D2 := f.statusADensity(got)
		for k := 0; k < 3; k++ {
			if math.Abs(D2[k]-D[k]) > 5e-3 {
				t.Errorf("%s known dye amount %v: %.4f -> %.4f", key, c, D[k], D2[k])
			}
		}
		if res > 5e-3 {
			t.Errorf("%s known dye amount inverse residual %.3g", key, res)
		}
	}
}
