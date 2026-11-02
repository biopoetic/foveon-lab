package main

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

// Every generated film-stock preset carries its film's grain as a settled
// value, so the slider can take it to 0.
func TestFilmPresetsStoreGrain(t *testing.T) {
	for _, p := range filmPresets() {
		if _, ok := render.FilmByKey(p.Params.Film); !ok {
			continue
		}
		if p.Params.Grain <= 0 || !p.Params.GrainSet {
			t.Errorf("%s: grain %.2f set=%v", p.Name, p.Params.Grain, p.Params.GrainSet)
		}
	}
}

// An old preset that names a film but predates the grain model is filled
// once, at load; the render path itself never fills it.
func TestOldPresetGrainFilledAtLoad(t *testing.T) {
	dir := t.TempDir()
	old := preset.Neutral()
	// A preset saved before the grain model: it names its film in the FL
	// sidecar but stores grain 0.
	old.Film = "Kodak Portra 800"
	xml := filepath.Join(dir, "FoveonLab_ALL.xml")
	if err := preset.Append(xml, "Portra 800 evening", old); err != nil {
		t.Fatal(err)
	}
	if err := saveFLParams(dir, filepath.Base(xml), "Portra 800 evening", old); err != nil {
		t.Fatal(err)
	}
	s := &server{presetsDir: dir}
	s.loadPresets()
	for _, p := range s.presets {
		if p.Name != "Portra 800 evening" {
			continue
		}
		f, ok := render.FilmByKey(p.Params.Film)
		if !ok || p.Params.Film != "Kodak Portra 800" {
			t.Fatalf("film %q (want Kodak Portra 800)", p.Params.Film)
		}
		if p.Params.Grain <= 0 || !p.Params.GrainSet || p.Params.Grain != math.Round(f.Grain*2)/2 {
			t.Errorf("old preset grain %.2f set=%v, want the film's %.2f", p.Params.Grain, p.Params.GrainSet, f.Grain)
		}
		return
	}
	t.Fatal("the old preset was not loaded")
}
