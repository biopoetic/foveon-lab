package render

import (
	"strings"
	"testing"
)

func TestFilms(t *testing.T) {
	fs := Films()
	if len(fs) < 80 {
		t.Fatalf("%d films", len(fs))
	}
	for _, f := range fs {
		if f.Maker == "" || f.Name == "" || f.Src == "" {
			t.Errorf("incomplete: %+v", f)
		}
		if f.RMS > 0 && f.URL == "" {
			t.Errorf("%s %s: a value with no document", f.Maker, f.Name)
		}
		if f.Name == "Tri-X Pan 400 (TX)" && (f.Grain < 39 || f.Grain > 40) {
			t.Errorf("Tri-X grain on the print %.1f, want 17 x 1.4/0.6", f.Grain)
		}
		if f.Name == "Fujichrome Velvia 50" && f.Grain != 9 {
			t.Errorf("a slide's grain is its own RMS: %.1f", f.Grain)
		}
	}
}

// One entry per film stock: an entry grouping several films ("Portra 160 /
// 400 / 800") cannot be right now that grain comes from the selected film —
// the films differ in speed, in grain and, for the Portras, in the curve the
// colour model was built from. A grouped name is a library error, not
// shorthand. PGI is recorded but never mixed with RMS: Kodak's data sheets
// state the two scales cannot be compared.
func TestNoGroupedFilms(t *testing.T) {
	for _, f := range Films() {
		if strings.Contains(f.Name, " / ") {
			t.Errorf("%s %q groups several films; split it", f.Maker, f.Name)
		}
		if f.PGI > 0 && f.RMS > 0 {
			t.Errorf("%s %q has both PGI and RMS", f.Maker, f.Name)
		}
		if f.PGI > 0 && f.URL == "" {
			t.Errorf("%s %q: a PGI with no document", f.Maker, f.Name)
		}
	}
	// Every colour model names a film that exists in the library.
	for key := range colorFilmData_() {
		if _, ok := FilmByKey(key); !ok {
			t.Errorf("colour model %q has no library entry", key)
		}
	}
	for key := range colorNegs().negs {
		if _, ok := FilmByKey(key); !ok {
			t.Errorf("colour negative model %q has no library entry", key)
		}
	}
}
