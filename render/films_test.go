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

// A film's formats are the sizes its maker sold it in, and each is one the
// grain model can size. A film without verified formats is not an error — it
// is offered as 35 mm only — but a format entry must be one of the known
// sizes and must name the document it came from.
func TestFilmFormats(t *testing.T) {
	known := map[string]bool{"135": true, "120": true, "220": true, "4x5": true, "8x10": true}
	withFormats := 0
	for _, f := range Films() {
		if len(f.Formats) == 0 {
			continue
		}
		withFormats++
		if f.FormatsSrc == "" {
			t.Errorf("%s %s: formats with no source", f.Maker, f.Name)
		}
		for _, s := range f.Formats {
			if !known[s] {
				t.Errorf("%s %s: unknown format %q", f.Maker, f.Name, s)
			}
		}
		// 35 mm is the reference the grain model is built on, so a film with
		// verified sizes normally includes it. The exception is a stock that
		// never came in 135: an early roll-film-only stock (Verichrome Pan)
		// or a sheet-film-only one (Ektapan 100).
		found := false
		for _, s := range f.Formats {
			if s == "135" {
				found = true
			}
		}
		if !found && f.Name != "Verichrome Pan" && f.Name != "Ektapan 100" {
			t.Errorf("%s %s: no 135 among %v", f.Maker, f.Name, f.Formats)
		}
	}
	if withFormats == 0 {
		t.Error("no film has verified formats")
	}
}
