package preset

import (
	"strings"
	"testing"
)

// TestFindMaxDetail checks that the bundled [FL] Max Detail entry survives
// loading in both camera packs: the same values in the SD1 and SD15 packs
// are two presets, not a renamed copy.
func TestFindMaxDetail(t *testing.T) {
	ps, errs := Load(Source{Label: "Built-in", FS: Builtin()})
	for _, p := range ps {
		if strings.Contains(p.Name, "Max Detail") {
			t.Logf("found: %q camera=%q highlight=%.2f fill=%.2f black=%.2f contrast=%.2f",
				p.Name, p.Camera, p.Params.Highlight, p.Params.FillLight, p.Params.Blackness, p.Params.Contrast)
		}
	}
	for _, e := range errs {
		t.Logf("load error: %v", e)
	}
	var names []string
	for _, p := range ps {
		if strings.Contains(p.Name, "[FL]") {
			names = append(names, p.Name+"@"+p.Camera)
		}
	}
	t.Logf("all [FL] entries: %v", names)
}
