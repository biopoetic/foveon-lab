package render

// Presets named after a film (an SPP-slider imitation, e.g. "SD1 Velvia 50
// Sun Pop") are linked to that film in the library, so the editor can
// offer the physical emulation of the real film. Explicit rules, most
// specific first; "Golden Hour" is not Kodak Gold, "PORTRAIT" not Portra.

import (
	"regexp"
	"sync"
)

var filmLinks = []struct {
	re   *regexp.Regexp
	film string // the library film's maker + " " + name
}{
	{regexp.MustCompile(`(?i)velvia\s*100\s*f`), "Fujifilm Fujichrome Velvia 100F"},
	{regexp.MustCompile(`(?i)velvia\s*100`), "Fujifilm Fujichrome Velvia 100"},
	{regexp.MustCompile(`(?i)velvia`), "Fujifilm Fujichrome Velvia 50"},
	{regexp.MustCompile(`(?i)provia\s*400\s*x`), "Fujifilm Fujichrome Provia 400X"},
	{regexp.MustCompile(`(?i)provia\s*400`), "Fujifilm Fujichrome Provia 400F"},
	{regexp.MustCompile(`(?i)provia`), "Fujifilm Fujichrome Provia 100F"},
	{regexp.MustCompile(`(?i)astia`), "Fujifilm Fujichrome Astia 100F"},
	{regexp.MustCompile(`(?i)sensia\s*200`), "Fujifilm Fujichrome Sensia 200"},
	{regexp.MustCompile(`(?i)sensia\s*400`), "Fujifilm Fujichrome Sensia 400"},
	{regexp.MustCompile(`(?i)sensia`), "Fujifilm Fujichrome Sensia 100"},
	{regexp.MustCompile(`(?i)reala`), "Fujifilm Fujicolor Superia Reala 100"},
	{regexp.MustCompile(`(?i)superia\s*(x-?tra\s*)?800`), "Fujifilm Fujicolor Superia X-TRA 800"},
	{regexp.MustCompile(`(?i)superia\s*200`), "Fujifilm Fujicolor Superia 200"},
	{regexp.MustCompile(`(?i)superia\s*100`), "Fujifilm Fujicolor Superia 100"},
	{regexp.MustCompile(`(?i)superia`), "Fujifilm Fujicolor Superia X-TRA 400"},
	{regexp.MustCompile(`(?i)pro\s*400\s*h|\b400h\b`), "Fujifilm Fujicolor Pro 400H"},
	{regexp.MustCompile(`(?i)acros`), "Fujifilm Neopan Acros 100"},
	{regexp.MustCompile(`(?i)ektachrome\s*e?100\s*vs`), "Kodak Ektachrome E100VS"},
	{regexp.MustCompile(`(?i)ektachrome\s*e?100\s*gx`), "Kodak Ektachrome E100GX"},
	{regexp.MustCompile(`(?i)ektachrome`), "Kodak Ektachrome E100G"},
	{regexp.MustCompile(`(?i)kodachrome\s*25\b`), "Kodak Kodachrome 25"},
	{regexp.MustCompile(`(?i)kodachrome\s*200`), "Kodak Kodachrome 200"},
	{regexp.MustCompile(`(?i)kodachrome`), "Kodak Kodachrome 64"},
	{regexp.MustCompile(`(?i)tri-?x`), "Kodak Tri-X Pan 400 (TX)"},
	{regexp.MustCompile(`(?i)p\s*3200`), "Kodak T-Max P3200"},
	{regexp.MustCompile(`(?i)t-?max\s*100`), "Kodak T-Max 100"},
	{regexp.MustCompile(`(?i)t-?max`), "Kodak T-Max 400"},
	{regexp.MustCompile(`(?i)plus-?x`), "Kodak Plus-X Pan 125 (PX)"},
	{regexp.MustCompile(`(?i)apx\s*25\b`), "Agfa Agfapan APX 25"},
	{regexp.MustCompile(`(?i)apx\s*400`), "Agfa Agfapan APX 400"},
	{regexp.MustCompile(`(?i)apx`), "Agfa Agfapan APX 100"},
	{regexp.MustCompile(`(?i)scala`), "Agfa Agfa Scala 200x"},
	{regexp.MustCompile(`(?i)fomapan\s*400`), "Foma Fomapan 400 Action"},
	{regexp.MustCompile(`(?i)fomapan\s*200`), "Foma Fomapan 200 Creative"},
	{regexp.MustCompile(`(?i)fomapan`), "Foma Fomapan 100 Classic"},
	{regexp.MustCompile(`(?i)\bportra\s*160`), "Kodak Portra 160"},
	{regexp.MustCompile(`(?i)\bportra\s*800`), "Kodak Portra 800"},
	{regexp.MustCompile(`(?i)\bportra(\s*\d|\b)`), "Kodak Portra 400"},
	{regexp.MustCompile(`(?i)ultra\s*max|\bmax\s*400`), "Kodak Ultramax 400"},
	{regexp.MustCompile(`(?i)\bgold\s*\d|\bgold\s+(soft|punch)`), "Kodak Gold 200"},
	{regexp.MustCompile(`(?i)\bhp\s*5`), "Ilford HP5 Plus"},
	{regexp.MustCompile(`(?i)\bfp\s*4`), "Ilford FP4 Plus"},
	{regexp.MustCompile(`(?i)pan\s*f\b`), "Ilford Pan F Plus"},
	{regexp.MustCompile(`(?i)delta\s*3200`), "Ilford Delta 3200"},
	{regexp.MustCompile(`(?i)delta\s*400`), "Ilford Delta 400"},
}

var filmKeys = sync.OnceValue(func() map[string]bool {
	m := map[string]bool{}
	for _, f := range Films() {
		m[f.Maker+" "+f.Name] = true
	}
	return m
})

// FilmFor is the library film a preset's name imitates ("" if none).
func FilmFor(presetName string) string {
	for _, l := range filmLinks {
		if l.re.MatchString(presetName) && filmKeys()[l.film] {
			return l.film
		}
	}
	return ""
}
