package main

// Task 87: /api/reveal is a POST, and every non-GET body has a size limit.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func postJSON(h http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8777"+path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRevealRequiresPOST(t *testing.T) {
	isolateUserDirs(t)
	dir := t.TempDir()
	good := filepath.Join(dir, "export.jpg")
	if err := os.WriteFile(good, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "program.exe")
	if err := os.WriteFile(bad, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}

	called := ""
	old := revealFile
	revealFile = func(p string) { called = p }
	defer func() { revealFile = old }()

	s := &server{}
	h := http.HandlerFunc(s.handleReveal)

	// A GET has no effect and is refused, even with the old query parameter.
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8777/api/reveal?path="+good, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d, want 405", w.Code)
	}

	// A POST for a missing file is a 404.
	missing, _ := json.Marshal(map[string]string{"path": filepath.Join(dir, "missing.jpg")})
	if w := postJSON(h, "/api/reveal", missing); w.Code != http.StatusNotFound {
		t.Fatalf("missing file: %d, want 404 (%s)", w.Code, w.Body)
	}
	// A POST for a .exe is a 403, even when the file exists.
	exe, _ := json.Marshal(map[string]string{"path": bad})
	if w := postJSON(h, "/api/reveal", exe); w.Code != http.StatusForbidden {
		t.Fatalf(".exe: %d, want 403 (%s)", w.Code, w.Body)
	}
	if called != "" {
		t.Fatalf("reveal ran for a refused request: %q", called)
	}

	// The real thing: POST {"path"} opens the file and returns 204.
	ok, _ := json.Marshal(map[string]string{"path": good})
	if w := postJSON(h, "/api/reveal", ok); w.Code != http.StatusNoContent {
		t.Fatalf("good file: %d, want 204 (%s)", w.Code, w.Body)
	}
	if called != good {
		t.Fatalf("revealed %q, want %q", called, good)
	}
}

func TestRequestBodyLimits(t *testing.T) {
	isolateUserDirs(t)
	dir := t.TempDir()
	photoPath := filepath.Join(dir, "p.jpg")
	if err := os.WriteFile(photoPath, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	ph := &Photo{ID: "p", Name: "p.jpg", path: photoPath}
	s := &server{byID: map[string]*Photo{ph.ID: ph}, sem: make(chan struct{}, 1)}
	h := sameOrigin(http.HandlerFunc(s.handleFavorite))

	// A normal favourite command works.
	small, _ := json.Marshal(map[string]any{"id": ph.ID, "on": true})
	if w := postJSON(h, "/api/favorite", small); w.Code != http.StatusOK {
		t.Fatalf("normal body: %d, want 200 (%s)", w.Code, w.Body)
	}
	if !ph.Favorite {
		t.Fatal("normal command did not set the favourite")
	}

	// A 2 MiB JSON body is refused before it reaches the handler.
	big := []byte(`{"id":"p","on":false,"pad":"` + strings.Repeat("x", 2<<20) + `"}`)
	w := postJSON(h, "/api/favorite", big)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("2 MiB body: %d, want 413 (%s)", w.Code, w.Body)
	}
	if !ph.Favorite || ph.Rating != 5 {
		t.Fatalf("refused request changed the favourite: %+v", ph)
	}
}

func TestBodyLimitExceptions(t *testing.T) {
	isolateUserDirs(t)
	// The handler only reports how much it read, so the middleware's limits
	// are the only thing under test.
	h := sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		fmt.Fprintf(w, "%d", len(b))
	}))
	body := []byte(`{"ids":["a"],"pad":"` + strings.Repeat("x", 2<<20) + `"}`)
	for _, c := range []struct {
		path string
		want int
	}{
		{"/api/favorite", http.StatusRequestEntityTooLarge},
		{"/api/look/import", http.StatusRequestEntityTooLarge},
		{"/api/export", http.StatusRequestEntityTooLarge}, // 321 B in practice
		{"/api/export/batch", http.StatusOK},
		{"/api/publish", http.StatusOK},
	} {
		if w := postJSON(h, c.path, body); w.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", c.path, w.Code, c.want, w.Body)
		}
	}
}

func TestLookImportAtLimit(t *testing.T) {
	isolateUserDirs(t)
	base, err := json.Marshal(newLookFile("Task 87 limit look", "ALL", preset.Neutral()))
	if err != nil {
		t.Fatal(err)
	}
	if len(base) > lookMaxBytes {
		t.Fatalf("look fixture is %d bytes, over the %d limit", len(base), lookMaxBytes)
	}
	// JSON allows trailing whitespace: pad the valid look to exactly its limit.
	body := append(base, bytes.Repeat([]byte(" "), lookMaxBytes-len(base))...)
	s := &server{presetsDir: t.TempDir()}
	h := sameOrigin(http.HandlerFunc(s.handleLookImport))
	w := postJSON(h, "/api/look/import", body)
	if w.Code != http.StatusOK {
		t.Fatalf("a look at its own %d-byte limit: %d (%s)", lookMaxBytes, w.Code, w.Body)
	}
	// The import writes the XML preset and its FL sidecar, not a .foveonlook.
	if _, err := os.Stat(filepath.Join(s.presetsDir, "FoveonLab_ALL.xml")); err != nil {
		t.Fatalf("the look at the limit was not written: %v", err)
	}
}
