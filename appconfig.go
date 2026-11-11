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
	"sync"
	"time"
)

// appConfig is what the user sets in the UI (no command line when Foveon
// Lab runs as a macOS .app or a double-clicked exe).
type appConfig struct {
	Photos  []string `json:"photos"`
	Presets string   `json:"presets"`
	// AllImages also lists ordinary JPEG/TIFF/PNG files (look preview only).
	AllImages bool `json:"allImages,omitempty"`
	// AI is the agent's provider, model and API key as set in the app.
	AI *aiSettings `json:"ai,omitempty"`
	// Accurate: raw-based previews where measured to help (nil = on).
	Accurate *bool `json:"accurate,omitempty"`
	// Favorites: the photos marked with a star (file paths).
	Favorites []string `json:"favorites,omitempty"`
	// Library: where Import from card copies originals (chosen once).
	Library string `json:"library,omitempty"`
	// PhotosApp: on macOS, also add batch exports to Apple Photos (off by default).
	PhotosApp bool `json:"photosApp,omitempty"`
	// ICloud: keep the card library in iCloud Drive (off by default).
	ICloud bool `json:"icloud,omitempty"`
	// ExportFolder: the folder Finish and batch export save into; empty means
	// next to each photo in a "foveon-lab" folder.
	ExportFolder string `json:"exportFolder,omitempty"`
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

// saveConfig replaces the config file in one step: the new text goes to a
// temporary file beside it, which is then renamed over the old one. Writing
// the file in place left it empty or half-written if the app died mid-write,
// and the next start then read no folders, favourites or API key.
//
// A caller that changes one setting uses updateConfig, not this: a bare
// load-then-save can overwrite a change another request made in between.
func saveConfig(c appConfig) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "config-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o600) // may hold an API key
	}
	if werr == nil {
		werr = os.Rename(name, p)
	}
	if werr != nil {
		os.Remove(name)
	}
	return werr
}

// cfgMu serialises read-modify-write of the config file.
var cfgMu sync.Mutex

// updateConfig changes the saved config under cfgMu: it reads the file,
// lets change edit it, and saves the result. change returns false to leave
// the file as it is. Every setting goes through here, so two requests at
// once (a star and a folder change, say) cannot overwrite each other.
//
// A config file that is there but cannot be read as JSON is kept as
// config.json.bad before a fresh one replaces it, so nothing in it is lost
// for good.
func updateConfig(change func(c *appConfig) bool) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	p := configPath()
	var c appConfig
	if data, err := os.ReadFile(p); err == nil {
		if json.Unmarshal(data, &c) != nil {
			c = appConfig{}
			if werr := os.WriteFile(p+".bad", data, 0o600); werr != nil {
				return fmt.Errorf("the settings file is damaged and could not be set aside: %w", werr)
			}
			log.Printf("config: %s is not valid JSON; kept as %s.bad and started fresh", p, p)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !change(&c) {
		return nil
	}
	return saveConfig(c)
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
	favs := map[string]bool{}
	if c, ok := loadConfig(); ok {
		for _, f := range c.Favorites {
			favs[f] = true
		}
	}
	for _, p := range ps {
		m[p.ID] = p
	}
	// The cull sidecars in each folder are the source of truth for rating and
	// rejection; the config's old favourites list is the fallback for a photo
	// that has no decision yet.
	applyCullSidecars(ps, favs)
	s.photoMu.Lock()
	s.photos, s.byID, s.photoRoots, s.allImages = ps, m, roots, all
	s.photoMu.Unlock()
	log.Printf("found %d photos in %v (%s)", len(ps), time.Since(start).Round(time.Millisecond), strings.Join(roots, "; "))
}

// handlePhotos lists the photos (under the lock: Favorite can change).
func (s *server) handlePhotos(w http.ResponseWriter, r *http.Request) {
	s.photoMu.RLock()
	defer s.photoMu.RUnlock()
	writeJSON(w, s.photos)
}

// handleFavorite stars or unstars a photo (POST {"id", "on"}). The star is
// the cull's five-star rating (cull.go), so it is stored in the photo
// folder's sidecar and mirrored into the app's config by path for older
// builds. Unstarring a photo rated below five leaves that rating alone.
func (s *server) handleFavorite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
		On bool   `json:"on"`
	}
	if r.Method != http.MethodPost {
		httpErr(w, 400, errors.New("POST {id, on}"))
		return
	}
	// Pass the decoder's own error on: httpErr turns a body over the size
	// limit into a 413 instead of a generic 400.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	ph := s.lookup(req.ID)
	if ph == nil {
		httpErr(w, 404, errors.New("unknown photo"))
		return
	}
	s.photoMu.RLock()
	rating, reject := ph.Rating, ph.Reject
	s.photoMu.RUnlock()
	if req.On {
		rating = 5
	} else if rating == 5 {
		rating = 0
	}
	if err := s.setCullState(ph, rating, reject); err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"favorite": rating == 5, "rating": rating})
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
		if err := updateConfig(func(c *appConfig) bool {
			c.Photos, c.Presets, c.AllImages = roots, presets, all
			return true
		}); err != nil {
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
	if err := updateConfig(func(c *appConfig) bool {
		c.AllImages = req.On
		if len(c.Photos) == 0 {
			c.Photos, c.Presets = roots, presets
		}
		return true
	}); err != nil {
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
