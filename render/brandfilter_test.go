package render

import (
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
		if f.Kind != "colour" && f.Kind != "ir" {
			t.Errorf("%s: kind %q is neither colour nor ir", f.ID, f.Kind)
		}
		switch {
		case f.Glass != "":
			if !SchottGlass(f.Glass) {
				t.Errorf("%s: glass %q is not in schottglass.json", f.ID, f.Glass)
			}
		case f.Wratten != "":
			if _, ok := wrattenEdges[f.Wratten]; !ok {
				t.Errorf("%s: Wratten %q has no edge", f.ID, f.Wratten)
			}
		case f.Edge > 0:
			if f.Edge < 380 || f.Edge > 1100 {
				t.Errorf("%s: cutoff %g nm outside the spectrum", f.ID, f.Edge)
			}
		default:
			t.Errorf("%s: no basis (no glass, Wratten equivalent or edge)", f.ID)
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
