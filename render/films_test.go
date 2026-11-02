package render

import (
	"math"
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

// Every film has a grain value. Colour negatives are all on ONE estimated
// scale (their published RMS or PGI is kept but does not set G directly), so
// speed orders them and same-speed consumer films land together. Slides and
// black-and-white films keep the published/estimated RMS path.
func TestFilmGrainSources(t *testing.T) {
	for _, f := range Films() {
		if f.Grain <= 0 {
			t.Errorf("%s %s: no grain at all", f.Maker, f.Name)
		}
		switch f.GrainSource {
		case "rms", "pgi", "estimated":
		default:
			t.Errorf("%s %s: grain source %q", f.Maker, f.Name, f.GrainSource)
		}
		if f.GrainSource == "estimated" && !f.GrainEstimated {
			t.Errorf("%s %s: estimate not marked", f.Maker, f.Name)
		}
		if f.GrainSource == "rms" && f.GrainEstimated {
			t.Errorf("%s %s: published RMS marked estimated", f.Maker, f.Name)
		}
		if f.Kind == "cn" {
			if !f.GrainEstimated || f.GrainSource != "estimated" || f.GrainNote == "" {
				t.Errorf("%s %s: colour negative not on the estimated one-scale path (%q est=%v)", f.Maker, f.Name, f.GrainSource, f.GrainEstimated)
			}
			if f.CNClass != "consumer" && f.CNClass != "pro" && f.CNClass != "value" {
				t.Errorf("%s %s: colour-negative class %q", f.Maker, f.Name, f.CNClass)
			}
		}
	}
	p160, _ := FilmByKey("Kodak Portra 160")
	p400, _ := FilmByKey("Kodak Portra 400")
	p800, _ := FilmByKey("Kodak Portra 800")
	if !(p160.Grain < p400.Grain && p400.Grain < p800.Grain) {
		t.Errorf("Portra grain not monotonic in speed: %.1f %.1f %.1f", p160.Grain, p400.Grain, p800.Grain)
	}
	s100, _ := FilmByKey("Fujifilm Fujicolor Superia 100")
	s400, _ := FilmByKey("Fujifilm Fujicolor Superia X-TRA 400")
	if s400.Grain < 1.2*s100.Grain {
		t.Errorf("Superia 100 and 400 on the same scale: %.2f vs %.2f, want 400 plainly higher", s100.Grain, s400.Grain)
	}
	// Same speed and class: consumer 400s must land close together.
	u400, _ := FilmByKey("Kodak Ultramax 400")
	if math.Abs(u400.Grain-s400.Grain) > 0.02*s400.Grain {
		t.Errorf("Ultramax 400 %.2f and Superia 400 %.2f are not on one scale", u400.Grain, s400.Grain)
	}
	g200, _ := FilmByKey("Kodak Gold 200")
	s200, _ := FilmByKey("Fujifilm Fujicolor Superia 200")
	if math.Abs(g200.Grain-s200.Grain) > 0.02*s200.Grain {
		t.Errorf("Gold 200 %.2f and Superia 200 %.2f are not on one scale", g200.Grain, s200.Grain)
	}
	// Slides stay on their own published scale.
	if v, _ := FilmByKey("Fujifilm Fujichrome Velvia 50"); v.Grain != 9 || v.GrainSource != "rms" {
		t.Errorf("Velvia 50 grain %.1f (%s), want the slide's published RMS 9", v.Grain, v.GrainSource)
	}
	// The median is the middle of the nine log-spaced bands (index 4).
	s100sizes, _ := grainSizes("cn", 100, 0)
	s400sizes, _ := grainSizes("cn", 400, 0)
	if s100sizes[4] >= s400sizes[4] {
		t.Errorf("Superia 100 and 400 have the same grain size")
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

// The Film stock menu disables an option unless the library has grain, a
// characteristic curve or a colour model for it (web/index.html:
// f.rms || f.curve || f.color). The data-backed stocks must not be
// unselectable: Portra was once disabled for three releases.
func TestFilmsSelectable(t *testing.T) {
	for _, name := range []string{
		"Kodak T-Max 100",
		"Kodak T-Max 400",
		"Kodak Ektar 100",
		"Kodak Portra 160",
		"Kodak Portra 800",
		"Kodak Gold 200",
		"Kodak Ultramax 400",
		"Fujifilm Fujicolor Pro 400H",
		"Fujifilm Fujicolor Pro 160C",
		"Fujifilm Fujicolor Pro 800Z",
	} {
		f, ok := FilmByKey(name)
		if !ok {
			t.Fatalf("%s: no library entry", name)
		}
		if f.RMS == 0 && f.Curve == nil && !f.Color {
			t.Errorf("%s: no grain, no curve and no colour model: the menu disables it", name)
		}
	}
	// The T-Max curves are the point of this round: both must attach.
	for _, name := range []string{"Kodak T-Max 100", "Kodak T-Max 400"} {
		f, _ := FilmByKey(name)
		if f.Curve == nil {
			t.Errorf("%s: no characteristic curve", name)
		}
	}
}
