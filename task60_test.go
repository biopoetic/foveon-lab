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
