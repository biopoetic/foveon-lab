package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
)

// handleExportBatch exports several photos with one preset (or tuned
// parameters), streaming one JSON line per photo so the page can show
// progress; closing the page stops it after the current photo.
func (s *server) handleExportBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []string        `json:"ids"`
		Preset string          `json:"preset"`
		Params json.RawMessage `json:"params"`
		Comp   bool            `json:"comp"`
		Format string          `json:"format"` // "jpeg" or "tiff"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	if req.Format != "jpeg" && req.Format != "tiff" {
		httpErr(w, 400, errors.New("batch export makes JPEG or TIFF"))
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
			if path, err := s.exportImage(ph, p, label, req.Comp, req.Format); err != nil {
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
