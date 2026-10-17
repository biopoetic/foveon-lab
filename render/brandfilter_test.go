package render

import (
	"math"
	"testing"
)

// Every catalogue entry must rest on something published or measured: a
// SCHOTT glass in the measured catalogue, a documented Wratten equivalent,
// or a published cutoff. This is the project's rule for the simulator — no
// invented curves — applied to the filter list itself.
func TestBrandFiltersHaveBasis(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range BrandFilters() {
		if f.ID == "" || f.Brand == "" || f.Model == "" {
			t.Errorf("entry without id/brand/model: %+v", f)
		}
		if seen[f.ID] {
			t.Errorf("duplicate id %q", f.ID)
		}
		seen[f.ID] = true
		if f.Kind != "colour" && f.Kind != "ir" && f.Kind != "nd" && f.Kind != "gnd" {
			t.Errorf("%s: kind %q is not colour, ir, nd or gnd", f.ID, f.Kind)
		}
		switch f.Kind {
		case "nd", "gnd":
			// A density filter's specification IS its effect: the published
			// density (stops) and, for a grad, the maker's soft/hard naming.
			if f.Stops <= 0 {
				t.Errorf("%s: density filter without stops", f.ID)
			}
			if f.Glass != "" || f.Wratten != "" || f.Edge != 0 {
				t.Errorf("%s: a density filter must not claim a spectrum", f.ID)
			}
			if f.Kind == "gnd" && f.Hardness != "soft" && f.Hardness != "hard" {
				t.Errorf("%s: graduated without soft/hard", f.ID)
			}
		default:
			switch {
		case f.Glass != "":
			if !SchottGlass(f.Glass) {
				t.Errorf("%s: glass %q is not in schottglass.json", f.ID, f.Glass)
			}
		case f.Wratten != "":
			if _, ok := wrattenT[f.Wratten]; !ok {
				t.Errorf("%s: Wratten %q has no spectrum", f.ID, f.Wratten)
			}
			case f.Edge > 0:
				if f.Edge < 380 || f.Edge > 1100 {
					t.Errorf("%s: cutoff %g nm outside the spectrum", f.ID, f.Edge)
				}
			default:
				t.Errorf("%s: no basis (no glass, Wratten equivalent or edge)", f.ID)
			}
		}
		if f.Src == "" {
			t.Errorf("%s: no source line", f.ID)
		}
	}
}

// Every colour entry resolves to a layer-space matrix on every generation;
// every infrared entry has a fitted look in irlooks.json.
func TestBrandFiltersResolve(t *testing.T) {
	gens := []string{"Merrill", "TRUEII", "SD9", "Quattro"}
	for _, f := range BrandFilters() {
		switch f.Kind {
		case "colour":
			for _, gen := range gens {
				if m := brandFilterMatrix(gen, f.ID); m == nil {
					t.Errorf("%s: no matrix on %s", f.ID, gen)
				}
			}
		case "ir":
			for _, gen := range gens {
				if m, _ := irLook(gen, "ir-"+f.ID); m == nil {
					t.Errorf("%s: no infrared look on %s", f.ID, gen)
				}
			}
		}
	}
}

// A brand colour filter reaches the render through the same path a preset's
// filter does.
func TestBrandFilterInLensPath(t *testing.T) {
	for _, f := range BrandFilters() {
		if f.Kind != "colour" {
			continue
		}
		if m := lensFilter("Merrill", "br-"+f.ID); m == nil {
			t.Errorf("lensFilter(br-%s) = nil", f.ID)
		}
	}
	// An unknown brand id is nil, not a silent clear glass.
	if lensFilter("Merrill", "br-nonexistent") != nil {
		t.Error("unknown brand filter resolved to a matrix")
	}
}

// TestNeutralDensityIsAchromatic records why neutral density is a class of
// its own rather than a spectral filter. A neutral filter attenuates every
// wavelength alike, so its layer matrix is density x the identity - no
// colour change at all. That is what makes it useful (it costs exposure, not
// colour), and why it cannot be modelled in the spectral filter path: there
// the render scales a lens filter so white keeps its brightness (the
// photographer's filter factor), which would divide the density straight back
// out and show nothing. ND is therefore rendered as an exposure change
// (render/density.go), with the product's density as its specification.
func TestNeutralDensityIsAchromatic(t *testing.T) {
	for _, density := range []float64{0.5, 0.1, 0.01} {
		m := filterMatrixFor("Merrill", func(float64) float64 { return density })
		if m == nil {
			t.Fatal("no matrix")
		}
		for i, v := range m {
			want := 0.0
			if i%4 == 0 {
				want = density
			}
			if math.Abs(v-want) > 1e-6 {
				t.Errorf("density %.2f: matrix %v is not density x identity", density, *m)
				break
			}
		}
		// Normalising by the light loss (what the render does) is the identity.
		for i, v := range m {
			want := 0.0
			if i%4 == 0 {
				want = 1
			}
			if math.Abs(v/density-want) > 1e-6 {
				t.Errorf("density %.2f: normalised matrix %v is not the identity", density, *m)
				break
			}
		}
	}
}
