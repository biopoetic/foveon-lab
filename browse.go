package main

import (
	"fmt"
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
	st, err := os.Stat(abs)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	if !st.IsDir() {
		httpErr(w, 400, fmt.Errorf("not a folder: %s", abs))
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		httpErr(w, 403, err)
		return
	}
	resp := browseResp{Path: abs, Roots: browseRoots()}
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
