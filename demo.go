package main

// The built-in demo: a way for someone with no X3F at hand to try the app,
// without putting a third-party sample in the repository or a release.
//
// The mechanism is deliberately dormant. It looks for a demo .X3F supplied by
// the maintainer (next to the program in demo/demo.X3F, or FOVEON_DEMO); when
// none is present the welcome screen has no demo link and /api/demo reports
// available:false. Choosing the photograph is the owner's call, so no photo
// ships with this code. The demo is copied into the user's cache and listed
// for this run only; nothing is written next to the program and nothing enters
// the saved folder list. A separate demo download therefore adds 0 bytes to
// the main archive.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// findDemo returns the supplied demo X3F, or "" when none is installed.
func findDemo() string {
	if p := strings.TrimSpace(os.Getenv("FOVEON_DEMO")); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	for _, d := range dirs {
		for _, p := range []string{
			filepath.Join(d, "demo", "demo.X3F"),
			filepath.Join(d, "demo.X3F"),
		} {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

// demoInfo is what the welcome screen needs to decide whether to offer the
// link. No path is sent to the browser.
func (s *server) demoInfo() (name string, bytes int64, ok bool) {
	if s.demo == "" {
		return "", 0, false
	}
	st, err := os.Stat(s.demo)
	if err != nil || st.IsDir() {
		return "", 0, false
	}
	return filepath.Base(s.demo), st.Size(), true
}

// handleDemo: GET tells whether a demo is installed; POST {action:"open"}
// copies it into the user's cache and adds that copy to this run's list.
func (s *server) handleDemo(w http.ResponseWriter, r *http.Request) {
	name, size, ok := s.demoInfo()
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"available": ok, "name": name, "bytes": size})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !ok {
		httpErr(w, http.StatusNotFound, errors.New("No demo photo is installed with this copy. Nothing was changed."))
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	if req.Action != "open" {
		httpErr(w, 400, errors.New(`POST {"action":"open"}`))
		return
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		httpErr(w, 500, errors.New("There is no writable cache folder to put the demo photo in."))
		return
	}
	dir := filepath.Join(cache, "foveon-lab", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httpErr(w, 500, plainFileError(err, dir))
		return
	}
	dst := filepath.Join(dir, "FoveonLab-Demo"+strings.ToLower(filepath.Ext(s.demo)))
	if err := copyFile(s.demo, dst); err != nil {
		httpErr(w, 500, plainFileError(err, dst))
		return
	}
	// Scan only the demo folder and add its photos to this run; the saved
	// folder list and the user's own library are untouched.
	found := scanPhotos([]string{dir}, false)
	s.photoMu.Lock()
	for _, p := range found {
		if _, exists := s.byID[p.ID]; exists {
			continue
		}
		s.photos = append(s.photos, p)
		s.byID[p.ID] = p
	}
	s.photoMu.Unlock()
	if len(found) == 0 {
		httpErr(w, 422, errors.New("The demo file could not be read as an X3F. Nothing was changed."))
		return
	}
	first := found[0]
	writeJSON(w, map[string]any{"ok": true, "photo": first.ID, "name": first.Name, "bytes": size})
}

// copyFile writes src to dst in one atomic step, so a failed copy never leaves
// a half demo that looks installed.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".demo-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}
