package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/biopoetic/foveon-lab/preset"
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

	// Prepare every job first: opening the photo and reading its saved edits
	// is cheap and must not happen inside the workers (which are busy with
	// pixel work).
	type batchJob struct {
		ph    *Photo
		p     preset.Params
		label string
		err   string
		est   int64
	}
	jobs := make([]batchJob, 0, len(req.IDs))
	var maxEst int64
	for _, id := range req.IDs {
		ph := s.lookup(id)
		j := batchJob{ph: ph, p: p, label: label}
		switch {
		case ph == nil:
			j.err = "unknown photo"
		case ph.Unsupported != "":
			j.err = ph.Unsupported
		default:
			j.est = exportMemEstimate(ph)
			if j.est > maxEst {
				maxEst = j.est
			}
			if raw, ok := req.ParamsByID[id]; ok && len(raw) > 0 && string(raw) != "null" {
				if err := json.Unmarshal(raw, &j.p); err != nil {
					j.err = "this photo's saved edits could not be read: " + err.Error()
				} else {
					j.label = label + " Edited"
				}
			}
		}
		jobs = append(jobs, j)
	}

	// Run the exports on a few workers at once, admitted by the machine's
	// memory budget. The batch leaves one render slot free, so a click on a
	// photo during a batch never waits for the whole queue.
	ctx := r.Context()
	n := len(jobs)
	workers := s.batchWorkers()
	budget := exportMemoryBudget()
	if maxEst > budget {
		budget = maxEst // one photo this large must still be allowed to finish
	}
	gate := newMemGate(budget)
	work := make(chan batchJob)
	results := make(chan map[string]any, n)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range work {
				line := map[string]any{"n": n}
				if j.err != "" {
					if j.ph != nil {
						line["name"] = j.ph.Name
					}
					line["error"] = j.err
					results <- line
					continue
				}
				line["name"] = j.ph.Name
				if !gate.acquire(ctx, j.est) {
					results <- map[string]any{"n": n, "name": j.ph.Name, "cancelled": true}
					continue
				}
				path, err := s.exportImageCtx(ctx, j.ph, j.p, j.label, req.Comp, req.Format, req.Denoise, req.Space, req.Destination)
				gate.release(j.est)
				if err != nil {
					if ctx.Err() != nil {
						line["cancelled"] = true
					} else {
						line["error"] = err.Error()
					}
				} else {
					line["path"] = path
				}
				results <- line
			}
		}()
	}
	go func() {
		defer close(work)
		for _, j := range jobs {
			select {
			case work <- j:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	// One writer: lines go out as each photo finishes, numbered by how many
	// have finished, not by how many were started. After a cancel the workers
	// are drained so none keeps writing behind the closed page.
	completed := 0
	cancelled := false
	for line := range results {
		if ctx.Err() != nil {
			cancelled = true
		}
		if cancelled {
			continue
		}
		completed++
		line["i"] = completed
		if line["error"] != nil {
			failed++
		} else if path, isPath := line["path"].(string); isPath {
			ok++
			exported = append(exported, path)
			folders[filepath.Dir(path)] = true
		}
		send(line)
	}
	if cancelled || ctx.Err() != nil {
		return
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
