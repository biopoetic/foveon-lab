package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/biopoetic/foveon-lab/render"
)

// handleLUT downloads the preset (or tuned parameters) as a .cube 3D LUT.
func (s *server) handleLUT(w http.ResponseWriter, r *http.Request) {
	p, label, err := s.params(r.URL.Query().Get("preset"), r.URL.Query().Get("p"))
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(label)+`.cube"`)
	render.WriteCube(w, label, p, 33)
}

// handleExportBatch exports several photos with one preset (or tuned
// parameters), streaming one JSON line per photo so the page can show
// progress; closing the page stops it after the current photo.
func (s *server) handleExportBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []string        `json:"ids"`
		Preset string          `json:"preset"`
		Params json.RawMessage `json:"params"`
		Comp   bool            `json:"comp"`
		Format string          `json:"format"` // "jpeg", "tiff" or "dng" (rendered)
		Denoise *int           `json:"denoise"` // raw-based exports: 0-10, nil = 10
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	if req.Format != "jpeg" && req.Format != "tiff" && req.Format != "dng" {
		httpErr(w, 400, errors.New("batch export makes JPEG, TIFF or DNG"))
		return
	}
	pj := ""
	if len(req.Params) > 0 && string(req.Params) != "null" {
		pj = string(req.Params)
	}
	p, label, err := s.params(req.Preset, pj)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	send := func(v any) {
		enc.Encode(v)
		if fl != nil {
			fl.Flush()
		}
	}
	ok, failed := 0, 0
	folders := map[string]bool{}
	for i, id := range req.IDs {
		if r.Context().Err() != nil {
			return
		}
		ph := s.lookup(id)
		line := map[string]any{"i": i + 1, "n": len(req.IDs)}
		switch {
		case ph == nil:
			line["error"] = "unknown photo"
		case ph.Unsupported != "":
			line["name"], line["error"] = ph.Name, ph.Unsupported
		default:
			line["name"] = ph.Name
			if path, err := s.exportImage(ph, p, label, req.Comp, req.Format, req.Denoise); err != nil {
				line["error"] = err.Error()
			} else {
				line["path"] = path
				folders[filepath.Dir(path)] = true
			}
		}
		if line["error"] != nil {
			failed++
		} else {
			ok++
		}
		send(line)
	}
	var dirs []string
	for d := range folders {
		dirs = append(dirs, d)
	}
	send(map[string]any{"done": true, "ok": ok, "failed": failed, "folders": dirs})
}
