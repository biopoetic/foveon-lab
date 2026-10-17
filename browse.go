package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A folder browser for the "Folders…" dialog: lists the subfolders of one
// directory so the editor can pick a photo or preset folder by navigating
// instead of typing a path. Read-only: it never creates or changes anything.

type browseDir struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type browseResp struct {
	Path   string      `json:"path"`
	Parent string      `json:"parent,omitempty"`
	Roots  []browseDir `json:"roots"`
	Dirs   []browseDir `json:"dirs"`
	X3F    int         `json:"x3f"`    // .X3F files directly in this folder
	Images int         `json:"images"` // any photo the app can show, directly here
	// Note explains that the folder asked for was not available and which
	// one is shown instead (an unplugged drive, a renamed folder).
	Note string `json:"note,omitempty"`
}

// handleBrowse lists one directory: its subfolders and the roots (drives)
// for the "up" navigation.
func (s *server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "" {
		// Start where the photos are, else at the user's home.
		s.photoMu.RLock()
		if len(s.photoRoots) > 0 {
			p = s.photoRoots[0]
		}
		s.photoMu.RUnlock()
		if p == "" {
			if home, err := os.UserHomeDir(); err == nil {
				p = home
			} else {
				p = "."
			}
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	// The saved folder may be on a drive that is not connected, or may have
	// been renamed: show the nearest folder that still exists rather than an
	// error, so the dialog stays usable.
	abs, note := resolveBrowsePath(abs)
	if abs == "" {
		httpErr(w, 400, errors.New(note))
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		httpErr(w, 403, err)
		return
	}
	resp := browseResp{Path: abs, Note: note, Roots: browseRoots()}
	if parent := filepath.Dir(abs); parent != abs {
		resp.Parent = parent
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(abs, name)
		if !isDir(e, full) {
			switch strings.ToLower(filepath.Ext(name)) {
			case ".x3f":
				resp.X3F++
				resp.Images++
			case ".jpg", ".jpeg", ".tif", ".tiff", ".png":
				resp.Images++
			}
			continue
		}
		resp.Dirs = append(resp.Dirs, browseDir{Name: name, Path: full})
	}
	sort.Slice(resp.Dirs, func(i, j int) bool {
		return strings.ToLower(resp.Dirs[i].Name) < strings.ToLower(resp.Dirs[j].Name)
	})
	writeJSON(w, resp)
}

// resolveBrowsePath returns the nearest existing folder at or above p, and a
// note saying so when it had to move. An empty result means nothing at all
// could be shown.
func resolveBrowsePath(p string) (string, string) {
	if st, err := os.Stat(p); err == nil {
		if st.IsDir() {
			return p, ""
		}
		parent := filepath.Dir(p)
		return parent, p + " is not a folder — showing " + parent
	}
	for cur := filepath.Dir(p); ; cur = filepath.Dir(cur) {
		if st, err := os.Stat(cur); err == nil && st.IsDir() {
			return cur, p + " is not available right now — showing " + cur
		}
		if filepath.Dir(cur) == cur {
			break
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home, p + " is not available right now — showing " + home
	}
	return "", p + " is not available"
}

// isDir reports whether an entry is a directory, following symlinks and
// Windows junctions one level.
func isDir(e os.DirEntry, full string) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink != 0 {
		if st, err := os.Stat(full); err == nil {
			return st.IsDir()
		}
	}
	return false
}

// browseRoots lists the filesystem roots: the drives on Windows, "/" else.
func browseRoots() []browseDir {
	if os.PathSeparator == '\\' {
		var out []browseDir
		for c := 'A'; c <= 'Z'; c++ {
			p := string(c) + ":\\"
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				out = append(out, browseDir{Name: string(c) + ":", Path: p})
			}
		}
		return out
	}
	return []browseDir{{Name: "/", Path: "/"}}
}
