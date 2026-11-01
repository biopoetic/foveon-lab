package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleDemoUnavailable(t *testing.T) {
	s := &server{}
	w := httptest.NewRecorder()
	s.handleDemo(w, httptest.NewRequest(http.MethodGet, "/api/demo", nil))
	if !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatalf("GET without a demo: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleDemo(w, httptest.NewRequest(http.MethodPost, "/api/demo", strings.NewReader(`{"action":"open"}`)))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "No demo photo") {
		t.Fatalf("POST without a demo: %d %s", w.Code, w.Body.String())
	}
}

func TestHandleDemoOpenCopiesIntoTheCacheOnly(t *testing.T) {
	cache := t.TempDir()
	meta := filepath.Join(t.TempDir(), "meta.json")
	t.Setenv("LOCALAPPDATA", cache)
	t.Setenv("FOVEON_META_CACHE", meta)
	src := filepath.Join(t.TempDir(), "chosen.X3F")
	if err := os.WriteFile(src, []byte("a demo test file, not a real X3F"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &server{demo: src, byID: map[string]*Photo{}}
	w := httptest.NewRecorder()
	s.handleDemo(w, httptest.NewRequest(http.MethodGet, "/api/demo", nil))
	if !strings.Contains(w.Body.String(), `"available":true`) {
		t.Fatalf("GET with a demo: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleDemo(w, httptest.NewRequest(http.MethodPost, "/api/demo", strings.NewReader(`{"action":"open"}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("POST demo: %d %s", w.Code, w.Body.String())
	}
	// The copy is in the cache, not next to the original.
	copied := filepath.Join(cache, "foveon-lab", "demo", "FoveonLab-Demo.x3f")
	if _, err := os.Stat(copied); err != nil {
		t.Fatalf("demo was not copied into the cache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(src), "FoveonLab-Demo.x3f")); err == nil {
		t.Fatal("demo was written next to the supplied file")
	}
	if len(s.photos) != 1 {
		t.Fatalf("demo added %d photos, want 1 (even an unreadable one is listed)", len(s.photos))
	}
	if !strings.Contains(strings.ToLower(s.photos[0].Unsupported), "damaged") {
		t.Fatalf("demo photo should carry the damaged sentence, got %q", s.photos[0].Unsupported)
	}
	// A second click must not add a duplicate.
	w = httptest.NewRecorder()
	s.handleDemo(w, httptest.NewRequest(http.MethodPost, "/api/demo", strings.NewReader(`{"action":"open"}`)))
	if len(s.photos) != 1 {
		t.Fatalf("second open added a duplicate: %d", len(s.photos))
	}
}
