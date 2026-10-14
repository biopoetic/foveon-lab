package render

import "testing"

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
