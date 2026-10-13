package main

// Per-photo straighten + crop, kept in geometry.json next to config.json
// (keyed by the photo's path) and applied to previews and exports.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/biopoetic/foveon-lab/render"
)

var geo = struct {
	sync.Mutex
	m map[string]render.Geometry
}{m: map[string]render.Geometry{}}

func geometryPath() string { return filepath.Join(filepath.Dir(configPath()), "geometry.json") }

func loadGeometry() {
	data, err := os.ReadFile(geometryPath())
	if err != nil {
		return
	}
	geo.Lock()
	defer geo.Unlock()
	json.Unmarshal(data, &geo.m)
}

func saveGeometryLocked() error {
	data, err := json.MarshalIndent(geo.m, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(geometryPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(geometryPath(), data, 0o644)
}

// geometryOf is ph's straighten + crop (zero = none).
func geometryOf(ph *Photo) render.Geometry {
	geo.Lock()
	defer geo.Unlock()
	return geo.m[ph.path]
}

// geoKey is a cache-key fragment that changes with the geometry.
func geoKey(g render.Geometry) string {
	if g.IsZero() {
		return ""
	}
	return fmt.Sprintf("|g%.3f,%.4f,%.4f,%.4f,%.4f", g.Angle, g.X, g.Y, g.W, g.H)
}

// handleGeometry: GET ?id= → the photo's geometry; POST {"id", "angle",
// "x", "y", "w", "h"} sets it (all zero = reset).
func (s *server) handleGeometry(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		ph := s.lookup(r.URL.Query().Get("id"))
		if ph == nil {
			httpErr(w, 404, fmt.Errorf("unknown photo"))
			return
		}
		writeJSON(w, geometryOf(ph))
		return
	}
	var req struct {
		ID string `json:"id"`
		render.Geometry
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	ph := s.lookup(req.ID)
	if ph == nil {
		httpErr(w, 404, fmt.Errorf("unknown photo"))
		return
	}
	g := req.Geometry
	if g.Angle < -45 || g.Angle > 45 || g.X < 0 || g.Y < 0 || g.W < 0 || g.H < 0 || g.X+g.W > 1.0001 || g.Y+g.H > 1.0001 {
		httpErr(w, 400, fmt.Errorf("geometry out of range"))
		return
	}
	geo.Lock()
	if g.IsZero() {
		delete(geo.m, ph.path)
	} else {
		geo.m[ph.path] = g
	}
	err := saveGeometryLocked()
	geo.Unlock()
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, g)
}
