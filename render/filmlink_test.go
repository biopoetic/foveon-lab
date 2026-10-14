package render

import "testing"

func TestFilmFor(t *testing.T) {
	for name, want := range map[string]string{
		"SD1 Velvia 50":                "Fujifilm Fujichrome Velvia 50",
		"[KODAK] SD1 Gold200 Sun 3D":   "Kodak Gold / Ultramax / ColorPlus",
		"[KODAK] SD1 Ultramax 400":     "Kodak Gold / Ultramax / ColorPlus",
		"SD1 Golden Hour – Soft Glow":  "",
		"[FOVEON] SD1 PORTRAIT Volume": "",
		"SD15 Portra 400":              "Kodak Portra 160 / 400 / 800",
		"[FUJI] SD15 Superia 400 V4":   "Fujifilm Fujicolor Superia X-TRA 400",
		"SD1 Provia100F Shade Clean":   "Fujifilm Fujichrome Provia 100F",
		"SD1 Ektachrome GoldenGlow":    "Kodak Ektachrome E100G / E100GX",
		"[BW] Tri-X Push":              "Kodak Tri-X Pan 400 (TX)",
	} {
		if got := FilmFor(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
	// Every rule names a film that exists in the library.
	for _, l := range filmLinks {
		if !filmKeys()[l.film] {
			t.Errorf("rule for %q: no such film", l.film)
		}
	}
}
