package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

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

// handleExportBatch exports several photos with one preset (or one preset
// per photo, when the user asked to keep their saved edits), streaming one
// JSON line per photo so the page can show progress; closing the page stops
// it after the current photo. Exported files are complete before a line is
// sent, and an existing file is never replaced.
//
// With Photos set on macOS, the finished files are handed to Apple Photos in
// one step at the end; iCloud Photos then syncs them by itself.
func (s *server) handleExportBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs        []string                   `json:"ids"`
		Preset     string                     `json:"preset"`
		Params     json.RawMessage            `json:"params"`
		ParamsByID map[string]json.RawMessage `json:"paramsById"`
		Comp       bool                       `json:"comp"`
		Format     string                     `json:"format"`  // "jpeg", "tiff" or "dng" (rendered)
		Denoise    *int                       `json:"denoise"` // raw-based exports: 0-10, nil = 10
		Space      string                     `json:"space"`   // export colour space, "" = sRGB
		// Look: the plain name of the look for the file names (see
		// exportLookName); empty falls back to the preset label.
		Look string `json:"look"`
		// Destination: a chosen folder; empty means next to each photo in a
		// "foveon-lab" folder.
		Destination string `json:"destination"`
		// Photos: add the finished files to Apple Photos (macOS only).
		Photos bool   `json:"photos"`
		Album  string `json:"album"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	if req.Format != "jpeg" && req.Format != "tiff" && req.Format != "dng" {
		httpErr(w, 400, errors.New("batch export makes JPEG, TIFF or DNG"))
		return
	}
	if _, ok := render.ColourSpaceByID(req.Space); !ok {
		httpErr(w, 400, errors.New("unknown colour space "+req.Space))
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
	if strings.TrimSpace(req.Look) != "" {
		label = req.Look
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
	var exported []string
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
			useP, useLabel := p, label
			if raw, ok := req.ParamsByID[id]; ok && len(raw) > 0 && string(raw) != "null" {
				if err := json.Unmarshal(raw, &useP); err != nil {
					line["error"] = "this photo's saved edits could not be read: " + err.Error()
					break
				}
				useLabel = label + " Edited"
			}
			if path, err := s.exportImage(ph, useP, useLabel, req.Comp, req.Format, req.Denoise, req.Space, req.Destination); err != nil {
				line["error"] = err.Error()
			} else {
				line["path"] = path
				exported = append(exported, path)
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
	done := map[string]any{"done": true, "ok": ok, "failed": failed, "folders": dirs}
	if req.Photos {
		album := req.Album
		if album == "" {
			album = photosAlbumName(time.Now())
		}
		switch {
		case !photosSupported:
			done["photosError"] = "adding photos to Apple Photos is only available on macOS"
		case len(exported) == 0:
			done["photosError"] = "nothing was exported, so nothing was added to Photos"
		default:
			if err := photosImport(exported, album); err != nil {
				done["photosError"] = err.Error()
			} else {
				done["photos"] = len(exported)
				done["photosAlbum"] = album
			}
		}
	}
	send(done)
}
