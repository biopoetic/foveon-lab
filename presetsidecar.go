package main

// Foveon Lab's own preset parameters (the film, its push, the paper, the
// colour head, the lens filter) have no place in SPP's XML: SPP would not
// understand them and the file must stay importable into SPP. They are kept
// in a small sidecar next to the XML, keyed by file and preset name, and
// merged back into the preset after the XML is parsed and BEFORE the preset
// load de-duplicates (two looks that differ only in push have identical SPP
// fields). Without this, saving a Portra 800 Push 1 preset and re-opening it
// silently returned box speed.

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/biopoetic/foveon-lab/preset"
)

const flSidecarFile = "FoveonLab_FL.json"

type flSidecarEntry struct {
	File   string        `json:"file"` // base name of the SPP XML it belongs to
	Name   string        `json:"name"`
	Params preset.Params `json:"params"`
}

type flSidecar struct {
	Presets []flSidecarEntry `json:"presets"`
}

var flSidecarMu sync.Mutex

func flSidecarPath(dir string) string { return filepath.Join(dir, flSidecarFile) }

func readFLSidecar(dir string) flSidecar {
	var sc flSidecar
	b, err := os.ReadFile(flSidecarPath(dir))
	if err == nil {
		_ = json.Unmarshal(b, &sc) // a damaged sidecar only loses the FL fields
	}
	return sc
}

// apply overlays the fields SPP's XML cannot carry. The core exposure,
// contrast, colour and white-balance values stay whatever the XML says, so a
// preset edited in SPP is not overridden.
func (e flSidecarEntry) apply(p *preset.Params) {
	p.Film = e.Params.Film
	p.Developer = e.Params.Developer
	p.DevTime = e.Params.DevTime
	p.Push = e.Params.Push
	p.Paper = e.Params.Paper
	p.Grade = e.Params.Grade
	p.CamMode = e.Params.CamMode
	p.CCY, p.CCM, p.CCC = e.Params.CCY, e.Params.CCM, e.Params.CCC
	p.PrintExp = e.Params.PrintExp
	p.ND, p.NDMetered = e.Params.ND, e.Params.NDMetered
	p.GND, p.GNDAngle, p.GNDPos, p.GNDSoft = e.Params.GND, e.Params.GNDAngle, e.Params.GNDPos, e.Params.GNDSoft
	p.Filter = e.Params.Filter
	p.LayerBottom, p.LayerMiddle, p.LayerTop = e.Params.LayerBottom, e.Params.LayerMiddle, e.Params.LayerTop
	p.Grain, p.FilmWidth, p.Halation = e.Params.Grain, e.Params.FilmWidth, e.Params.Halation
}

// flMerge is the preset.Source hook: a preset whose XML was written together
// with a sidecar entry gets its Foveon Lab fields back.
func flMerge(dir string) func(*preset.Preset) {
	m := map[string]flSidecarEntry{}
	for _, e := range readFLSidecar(dir).Presets {
		m[e.File+"\x00"+e.Name] = e
	}
	if len(m) == 0 {
		return nil
	}
	return func(p *preset.Preset) {
		if e, ok := m[path.Base(p.Source)+"\x00"+p.Name]; ok {
			e.apply(&p.Params)
		}
	}
}

// saveFLParams records p's Foveon Lab fields for the setting `name` written
// to the XML file `file` (a base name inside dir).
func saveFLParams(dir, file, name string, p preset.Params) error {
	flSidecarMu.Lock()
	defer flSidecarMu.Unlock()
	sc := readFLSidecar(dir)
	kept := sc.Presets[:0]
	for _, e := range sc.Presets {
		if e.File != file || e.Name != name {
			kept = append(kept, e)
		}
	}
	sc.Presets = append(kept, flSidecarEntry{File: file, Name: name, Params: p})
	b, err := json.MarshalIndent(sc, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(flSidecarPath(dir), append(b, '\n'), 0o644)
}
