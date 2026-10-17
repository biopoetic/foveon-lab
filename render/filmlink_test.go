package render

import "testing"

func TestFilmFor(t *testing.T) {
	for name, want := range map[string]string{
		"SD1 Velvia 50":                "Fujifilm Fujichrome Velvia 50",
		"[KODAK] SD1 Gold200 Sun 3D":   "Kodak Gold 200",
		"[KODAK] SD1 Ultramax 400":     "Kodak Ultramax 400",
		"SD1 Golden Hour – Soft Glow":  "",
		"[FOVEON] SD1 PORTRAIT Volume": "",
		"SD15 Portra 400":              "Kodak Portra 400",
		"SD15 Portra 160":              "Kodak Portra 160",
		"SD15 Portra 800":              "Kodak Portra 800",
		"[KODAK] SD1 ColorPlus 200":    "Kodak ColorPlus 200",
		"[FUJI] SD15 Superia 400 V4":   "Fujifilm Fujicolor Superia X-TRA 400",
		"SD1 Provia100F Shade Clean":   "Fujifilm Fujichrome Provia 100F",
		"SD1 Ektachrome GoldenGlow":    "Kodak Ektachrome E100G",
		"Ektachrome E100GX Warm":       "Kodak Ektachrome E100GX",
		"CineStill 800T":               "CineStill 800T",
		"CineStill 400D":               "CineStill 400D",
		"CineStill 50D":                "CineStill 50D",
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
