package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// A saved preset must keep the fields SPP's XML cannot carry: film, push,
// paper and the darkroom. Two looks that differ only in push must both
// survive the XML's identical core settings.
func TestFLPresetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	xml := filepath.Join(dir, "FoveonLab_ALL.xml")
	box := preset.Neutral()
	box.Film, box.Push, box.Paper = "Kodak Portra 800", 0, "Kodak Portra Endura"
	if err := preset.Append(xml, "Portra 800 box", box); err != nil {
		t.Fatal(err)
	}
	if err := saveFLParams(dir, filepath.Base(xml), "Portra 800 box", box); err != nil {
		t.Fatal(err)
	}
	push1 := box
	push1.Push = 1
	if err := preset.Append(xml, "Portra 800 Push 1", push1); err != nil {
		t.Fatal(err)
	}
	if err := saveFLParams(dir, filepath.Base(xml), "Portra 800 Push 1", push1); err != nil {
		t.Fatal(err)
	}
	ps, errs := preset.Load(preset.Source{FS: os.DirFS(dir), Merge: flMerge(dir)})
	if len(errs) != 0 {
		t.Fatalf("load: %v", errs)
	}
	if len(ps) != 2 {
		t.Fatalf("got %d presets, want 2 (box and Push 1 must not be de-duplicated)", len(ps))
	}
	byName := map[string]preset.Preset{}
	for _, p := range ps {
		byName[p.Name] = p
	}
	for _, tc := range []struct {
		name string
		push float64
	}{{"Portra 800 box", 0}, {"Portra 800 Push 1", 1}} {
		p, ok := byName[tc.name]
		if !ok {
			t.Errorf("%q missing", tc.name)
			continue
		}
		if p.Params.Film != "Kodak Portra 800" || p.Params.Push != tc.push || p.Params.Paper != "Kodak Portra Endura" {
			t.Errorf("%q: film %q push %v paper %q", tc.name, p.Params.Film, p.Params.Push, p.Params.Paper)
		}
	}
}

// The XML remains the source of the SPP values: editing exposure in the XML
// must not be undone by an old sidecar.
func TestFLPresetXMLWins(t *testing.T) {
	dir := t.TempDir()
	xml := filepath.Join(dir, "FoveonLab_ALL.xml")
	p := preset.Neutral()
	p.Exposure, p.Film, p.Push = 0.5, "Kodak Portra 800", 2
	if err := preset.Append(xml, "x", p); err != nil {
		t.Fatal(err)
	}
	old := p
	old.Exposure = -1
	if err := saveFLParams(dir, filepath.Base(xml), "x", old); err != nil {
		t.Fatal(err)
	}
	ps, errs := preset.Load(preset.Source{FS: os.DirFS(dir), Merge: flMerge(dir)})
	if len(errs) != 0 || len(ps) != 1 {
		t.Fatalf("load: %d presets, %v", len(ps), errs)
	}
	if ps[0].Params.Exposure != 0.5 || ps[0].Params.Push != 2 {
		t.Errorf("exposure %.2f push %v, want 0.5 and 2", ps[0].Params.Exposure, ps[0].Params.Push)
	}
}

func TestFLSidecarBadJSONIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(flSidecarPath(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m := flMerge(dir); m != nil {
		t.Error("a damaged sidecar should merge nothing")
	}
}
