package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// TestBrowse lists a folder the way the editor's folder dialog does.
func TestBrowse(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.X3F", "b.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, _ := filepath.Abs(dir)

	var s server
	rec := httptest.NewRecorder()
	s.handleBrowse(rec, httptest.NewRequest("GET", "/api/browse?path="+url.QueryEscape(dir), nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp browseResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Path != want {
		t.Errorf("path %q, want %q", resp.Path, want)
	}
	if len(resp.Dirs) != 1 || resp.Dirs[0].Name != "sub" || resp.Dirs[0].Path != filepath.Join(want, "sub") {
		t.Errorf("dirs %+v", resp.Dirs)
	}
	if resp.X3F != 1 || resp.Images != 2 {
		t.Errorf("x3f %d images %d, want 1 and 2", resp.X3F, resp.Images)
	}
	if resp.Parent != filepath.Dir(want) {
		t.Errorf("parent %q, want %q", resp.Parent, filepath.Dir(want))
	}
	if len(resp.Roots) == 0 {
		t.Error("no roots")
	}
}

// A path that is not a folder is not an error: the dialog shows the folder
// that contains it, with a note, so it stays usable.
func TestBrowseNotAFolder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var s server
	rec := httptest.NewRecorder()
	s.handleBrowse(rec, httptest.NewRequest("GET", "/api/browse?path="+url.QueryEscape(file), nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp browseResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(dir)
	if resp.Path != want || resp.Note == "" {
		t.Errorf("path %q note %q, want %q and a note", resp.Path, resp.Note, want)
	}
}

// A folder on a drive that is not connected falls back to one that exists.
func TestBrowseMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-folder", "deeper")
	var s server
	rec := httptest.NewRecorder()
	s.handleBrowse(rec, httptest.NewRequest("GET", "/api/browse?path="+url.QueryEscape(missing), nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp browseResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Path == "" || resp.Note == "" {
		t.Errorf("path %q note %q, want an existing folder and a note", resp.Path, resp.Note)
	}
	if st, err := os.Stat(resp.Path); err != nil || !st.IsDir() {
		t.Errorf("fallback %q is not a folder", resp.Path)
	}
}
