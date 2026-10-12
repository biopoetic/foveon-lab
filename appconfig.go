package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// appConfig is what the user sets in the UI (no command line when Foveon
// Lab runs as a macOS .app or a double-clicked exe).
type appConfig struct {
	Photos  []string `json:"photos"`
	Presets string   `json:"presets"`
	// AllImages also lists ordinary JPEG/TIFF/PNG files (look preview only).
	AllImages bool `json:"allImages,omitempty"`
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "foveon-lab", "config.json")
}

func loadConfig() (appConfig, bool) {
	var c appConfig
	data, err := os.ReadFile(configPath())
	if err != nil {
		return c, false
	}
	return c, json.Unmarshal(data, &c) == nil
}

func saveConfig(c appConfig) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, data, 0o644)
}

// defaultPhotoRoots: the Desktop plus the Pictures folder when present.
func defaultPhotoRoots(desk string) []string {
	roots := []string{desk}
	if home, err := os.UserHomeDir(); err == nil {
		if st, err := os.Stat(filepath.Join(home, "Pictures")); err == nil && st.IsDir() {
			roots = append(roots, filepath.Join(home, "Pictures"))
		}
	}
	return roots
}

// setPhotos rescans the photo folders and swaps the catalogue in.
func (s *server) setPhotos(roots []string, all bool) {
	start := time.Now()
	ps := scanPhotos(roots, all)
	if ps == nil {
		ps = []*Photo{} // JSON [] rather than null
	}
	m := make(map[string]*Photo, len(ps))
	for _, p := range ps {
		m[p.ID] = p
	}
	s.photoMu.Lock()
	s.photos, s.byID, s.photoRoots, s.allImages = ps, m, roots, all
	s.photoMu.Unlock()
	log.Printf("found %d photos in %v (%s)", len(ps), time.Since(start).Round(time.Millisecond), strings.Join(roots, "; "))
}

func (s *server) lookup(id string) *Photo {
	s.photoMu.RLock()
	defer s.photoMu.RUnlock()
	return s.byID[id]
}

func (s *server) photoList() []*Photo {
	s.photoMu.RLock()
	defer s.photoMu.RUnlock()
	return s.photos
}

// handleFolders reads (GET) or changes and remembers (POST) where photos and
// presets are loaded from.
func (s *server) handleFolders(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req appConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpErr(w, 400, err)
			return
		}
		var roots []string
		for _, p := range req.Photos {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if st, err := os.Stat(p); err != nil || !st.IsDir() {
				httpErr(w, 400, fmt.Errorf("not a folder: %s", p))
				return
			}
			roots = append(roots, p)
		}
		if len(roots) == 0 {
			httpErr(w, 400, errors.New("give at least one photo folder"))
			return
		}
		presets := strings.TrimSpace(req.Presets)
		if presets != "" {
			if st, err := os.Stat(presets); err != nil || !st.IsDir() {
				httpErr(w, 400, fmt.Errorf("not a folder: %s", presets))
				return
			}
		}
		s.photoMu.RLock()
		all := s.allImages
		s.photoMu.RUnlock()
		if err := saveConfig(appConfig{Photos: roots, Presets: presets, AllImages: all}); err != nil {
			httpErr(w, 500, err)
			return
		}
		s.setPhotos(roots, all)
		s.pmu.Lock()
		s.presetsDir = presets
		s.pmu.Unlock()
		s.loadPresets()
	}
	s.photoMu.RLock()
	roots := s.photoRoots
	n := len(s.photos)
	all := s.allImages
	s.photoMu.RUnlock()
	s.pmu.RLock()
	presets, np := s.presetsDir, len(s.presets)
	s.pmu.RUnlock()
	writeJSON(w, map[string]any{"photos": roots, "presets": presets, "photoCount": n, "presetCount": np, "allImages": all})
}

// handleAllImages switches ordinary JPEG/TIFF/PNG files on or off (POST
// {"on": true}) and rescans; the choice is remembered.
func (s *server) handleAllImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	s.photoMu.RLock()
	roots := s.photoRoots
	s.photoMu.RUnlock()
	s.pmu.RLock()
	presets := s.presetsDir
	s.pmu.RUnlock()
	cfg, _ := loadConfig()
	cfg.AllImages = req.On
	if len(cfg.Photos) == 0 {
		cfg.Photos, cfg.Presets = roots, presets
	}
	if err := saveConfig(cfg); err != nil {
		httpErr(w, 500, err)
		return
	}
	s.setPhotos(roots, req.On)
	s.photoMu.RLock()
	n := len(s.photos)
	s.photoMu.RUnlock()
	writeJSON(w, map[string]any{"allImages": req.On, "photoCount": n})
}

// handleQuit stops the app (it may have no window to close, e.g. as a .app).
func handleQuit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
	log.Printf("quit requested from the UI")
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	}()
}

// alreadyRunning reports the address of a Foveon Lab already serving on
// port, so a second launch (double-click, Dock) just reopens the browser.
func alreadyRunning(port int) string {
	addr := fmt.Sprintf("http://127.0.0.1:%d/", port)
	c := http.Client{Timeout: 700 * time.Millisecond}
	resp, err := c.Get(addr + "api/caps")
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if resp.Header.Get("X-App") != "foveon-lab" {
		return ""
	}
	return addr
}
