package render

// TASK-78: the new black-and-white curves must pin the speed point and the
// contrast the maker's sheet states, and a film without a curve must stay
// generic (no tone).

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// xAt is the log H where density d is reached on the curve.
func t78xAt(c *FilmCurve, d float64) float64 {
	lo, hi := c.LgH[0], c.LgH[len(c.LgH)-1]
	for i := 0; i < 60; i++ {
		m := (lo + hi) / 2
		if c.Density(c.GammaRef())(m) < d {
			lo = m
		} else {
			hi = m
		}
	}
	return (lo + hi) / 2
}

// t78CI is Kodak's contrast index (and Fuji's G): 0.9 density over the log H
// span from 0.1 to 1.0 above base + fog.
func t78CI(c *FilmCurve) float64 {
	dens := c.Density(c.GammaRef())
	fog := dens(c.LgH[0])
	return 0.9 / (t78xAtDens(c, fog+1.0, dens) - t78xAtDens(c, fog+0.1, dens))
}

func t78xAtDens(c *FilmCurve, d float64, dens func(float64) float64) float64 {
	lo, hi := c.LgH[0], c.LgH[len(c.LgH)-1]
	for i := 0; i < 60; i++ {
		m := (lo + hi) / 2
		if dens(m) < d {
			lo = m
		} else {
			hi = m
		}
	}
	return (lo + hi) / 2
}

func TestT78NewFilmCurves(t *testing.T) {
	type want struct {
		film string
		ei   float64 // the speed the sheet states for its developer
		ci   float64 // 0 when the sheet states no contrast
		// contrast measure: "CI" or "gamma"
		measure string
		end     float64 // the family must reach this lgH (the longest printed curve)
	}
	cases := []want{
		{"Kodak Tri-X Pan Professional 320 (TXP)", 320, 0.56, "CI", -0.06},
		{"Kodak Plus-X Pan 125 (PX)", 125, 0.62, "CI", 0.80},
		{"Kodak T-Max P3200", 800, 0, "gamma", -0.30},
		{"Kodak Technical Pan", 25, 0.64, "CI", 0.86},
		{"Fujifilm Neopan Acros 100", 100, 0.65, "CI", 0.46},
	}
	for _, w := range cases {
		c, ok := FilmCurveFor(w.film)
		if !ok {
			t.Errorf("%s: no curve", w.film)
			continue
		}
		// Base + fog is the film's, so it is the lowest left end of the
		// family: a longer development starts higher on its toe at the
		// printed frame, which is not base + fog. The curve is monotone.
		fog := c.Curves[0].D[0]
		for i := range c.Curves {
			if c.Curves[i].D[0] < fog {
				fog = c.Curves[i].D[0]
			}
			for j := 1; j < len(c.LgH); j++ {
				if c.LgH[j] <= c.LgH[j-1] || c.Curves[i].D[j] < c.Curves[i].D[j-1] {
					t.Errorf("%s: not monotone at %d", w.film, j)
					break
				}
			}
		}
		if fog < 0.05 || fog > 0.35 {
			t.Errorf("%s: base+fog %.3f out of 0.05..0.35", w.film, fog)
		}
		// Every printed curve reaches its own printed end: the family's last
		// point must not stop at the shortest curve's end (Plus-X lost its
		// shoulders that way).
		if c.LgH[len(c.LgH)-1] < w.end {
			t.Errorf("%s: family ends at %.3f, want at least %.3f", w.film, c.LgH[len(c.LgH)-1], w.end)
		}
		// Speed: the ISO 6 point 0.10 above base+fog, within 1/3 stop of the
		// sheet's speed for this developer. A relative axis (Acros, the Fuji
		// trap) is placed by its speed point, so only the shape is checked.
		off := c.SpeedPoint() - math.Log10(0.8/w.ei)
		if !c.Relative && math.Abs(off) > 0.10 {
			t.Errorf("%s: speed point %.2f stops from EI %.0f (%.3f dex)", w.film, off/math.Log10(2), w.ei, off)
		}
		// Contrast: CI or gamma, within 0.05 of the sheet.
		var got float64
		if w.measure == "CI" {
			got = t78CI(c)
		} else {
			got = c.GammaRef()
		}
		if w.ci > 0 && math.Abs(got-w.ci) > 0.05 {
			t.Errorf("%s: %s %.3f, sheet %.3f", w.film, w.measure, got, w.ci)
		}
		t.Logf("%s: speed %.2f dex (EI %.0f, %+.2f stops), %s %.3f (sheet %.3f), fog %.3f, ends %.3f",
			w.film, c.SpeedPoint(), w.ei, off/math.Log10(2), w.measure, got, w.ci, fog, c.LgH[len(c.LgH)-1])
	}
}

func TestT78NoCurveStaysGeneric(t *testing.T) {
	for _, film := range []string{"Foma Fomapan 100 Classic", "Rollei RPX 100"} {
		if _, ok := FilmCurveFor(film); ok {
			t.Errorf("%s: has a curve, want generic", film)
		}
		if newFilmTone(preset.Params{Film: film}) != nil {
			t.Errorf("%s: has a film tone, want generic", film)
		}
		f, ok := FilmByKey(film)
		if !ok {
			t.Fatalf("%s: not in the library", film)
		}
		if f.Curve != nil {
			t.Errorf("%s: Films() carries a curve, want nil", film)
		}
	}
	// A film with a curve is picked up by name with no code change.
	for _, film := range []string{
		"Kodak Tri-X Pan Professional 320 (TXP)",
		"Kodak Plus-X Pan 125 (PX)",
		"Kodak T-Max P3200",
		"Kodak Technical Pan",
		"Fujifilm Neopan Acros 100",
	} {
		if _, ok := FilmCurveFor(film); !ok {
			t.Errorf("%s: not picked up by name", film)
		}
		if newFilmTone(preset.Params{Film: film}) == nil {
			t.Errorf("%s: no film tone", film)
		}
	}
}
