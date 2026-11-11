package main

// Task 60: a [WB] preset under a colour film. The balance belongs to the
// light, before the emulsion: the raw layers are developed with the camera's
// white balance and the film sees balanced light. These tests pin the source
// selection; the pixel side (the film branch must not re-apply the balance)
// is pinned by render.TestFilmWBGPURecipeMirrorsCPU.

import (
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// Task 60: guards which layer source a preset selects: film, film@wb, IR or normal raw.
// A failure means a [WB] preset under a colour film stopped developing the balance into the Base.
// Change only when the layer-source rules or preset set change deliberately; re-pin this table and say why.
func TestFilmWBSourceSelection(t *testing.T) {
	if !goRaw {
		t.Skip("native raw development is off (FOVEON_RAW=x3f)")
	}
	ph := &Photo{Kind: "x3f", props: map[string]string{}}
	cases := []struct {
		name string
		p    preset.Params
		want string
		ok   bool
	}{
		{"no film", preset.Neutral(), "", false},
		{"colour film, as shot", preset.Params{Film: "Kodak Portra 400"}, "film", true},
		{"colour film, Shade", preset.Params{Film: "Kodak Portra 400", WhiteBalance: 3}, "film@wb:Shade", true},
		{"colour film, Flash", preset.Params{Film: "Kodak Portra 400", WhiteBalance: 7}, "film@wb:Flash", true},
		{"colour film, as-shot code 1", preset.Params{Film: "Kodak Portra 400", WhiteBalance: 1}, "film", true},
		{"B&W film keeps the normal raw path", preset.Params{Film: "Kodak Tri-X Pan 400 (TX)", WhiteBalance: 3}, "", false},
		{"IR look is its own layer source", preset.Params{Filter: "ir-ir590", Film: "Kodak Portra 400", WhiteBalance: 3}, "ir-ir590", true},
	}
	for _, c := range cases {
		got, ok := layersSource(ph, c.p)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: layersSource = %q,%v, want %q,%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// Task 60: guards splitFilterWB and modeDevelopedWB, the film/WB spec split.
// A failure means a film@wb:... mode string no longer selects film plus a developed balance.
// Change only when the mode-string grammar changes deliberately; update the table and its callers together.
func TestFilmWBModeAndSplit(t *testing.T) {
	for _, c := range []struct {
		spec, filter, wb string
	}{
		{"film", "film", ""},
		{"film@wb:Shade", "film", "Shade"},
		{"film@wb:Fluorescent", "film", "Fluorescent"},
		{"ir-ir720", "ir-ir720", ""},
		{"wb:Overcast", "", "Overcast"},
	} {
		f, wb := splitFilterWB(c.spec)
		if f != c.filter || wb != c.wb {
			t.Errorf("splitFilterWB(%q) = %q,%q, want %q,%q", c.spec, f, wb, c.filter, c.wb)
		}
	}
	for mode, want := range map[string]bool{
		"rawlayers:film@wb:Shade": true,
		"rawwb:wb:Shade":          true,
		"rawlayers:film":          false,
		"raw":                     false,
		"rawlayers:ir-ir590":      false,
	} {
		if got := modeDevelopedWB(mode); got != want {
			t.Errorf("modeDevelopedWB(%q) = %v, want %v", mode, got, want)
		}
	}
}
