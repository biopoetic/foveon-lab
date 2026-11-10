package main

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cullTestConfig points the app's config at a throwaway folder so a test
// never touches the owner's real favourites.
func cullTestConfig(t *testing.T) {
	t.Helper()
	isolateUserDirs(t)
}

func writeCullPhoto(t *testing.T, dir, name string) (path string, sum [32]byte) {
	t.Helper()
	path = filepath.Join(dir, name)
	b := []byte("not really an X3F; the cull must never write here: " + name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, sha256.Sum256(b)
}

func TestCullSidecarLivesBesidePhotosAndMovesWithThem(t *testing.T) {
	cullTestConfig(t)
	base := t.TempDir()
	a := filepath.Join(base, "shoot-a")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	p1, sum1 := writeCullPhoto(t, a, "SDIM0001.X3F")
	p2, _ := writeCullPhoto(t, a, "SDIM0002.X3F")
	if _, err := saveCullDecision(p1, 4, false); err != nil {
		t.Fatal(err)
	}
	if _, err := saveCullDecision(p2, 0, true); err != nil {
		t.Fatal(err)
	}
	// The sidecar is in the photo folder, is valid JSON, and the originals
	// are byte-for-byte untouched.
	sc := readCullSidecar(a)
	if sc.Photos["SDIM0001.X3F"].Rating != 4 || !sc.Photos["SDIM0002.X3F"].Reject {
		t.Fatalf("sidecar decisions: %+v", sc)
	}
	if b, _ := os.ReadFile(p1); sha256.Sum256(b) != sum1 {
		t.Fatal("a cull decision changed an original")
	}

	photos := []*Photo{{Name: "SDIM0001.X3F", path: p1}, {Name: "SDIM0002.X3F", path: p2}}
	applyCullSidecars(photos, nil)
	if photos[0].Rating != 4 || photos[0].Favorite || !photos[1].Reject {
		t.Fatalf("applied decisions: %+v %+v", photos[0], photos[1])
	}

	// Move the whole folder (sidecar included): decisions follow the photos.
	b := filepath.Join(base, "moved")
	if err := os.Rename(a, b); err != nil {
		t.Fatal(err)
	}
	moved := []*Photo{{Name: "SDIM0001.X3F", path: filepath.Join(b, "SDIM0001.X3F")},
		{Name: "SDIM0002.X3F", path: filepath.Join(b, "SDIM0002.X3F")}}
	applyCullSidecars(moved, nil)
	if moved[0].Rating != 4 || !moved[1].Reject {
		t.Fatalf("decisions did not travel with the folder: %+v %+v", moved[0], moved[1])
	}
}

func TestCullStarIsFiveStarsAndClearStaysCleared(t *testing.T) {
	cullTestConfig(t)
	dir := t.TempDir()
	path, _ := writeCullPhoto(t, dir, "SDIM0010.X3F")
	ph := &Photo{ID: "x", Name: "SDIM0010.X3F", path: path}
	s := &server{byID: map[string]*Photo{ph.ID: ph}, photos: []*Photo{ph}}
	if err := s.setCullState(ph, 5, false); err != nil {
		t.Fatal(err)
	}
	if !ph.Favorite || ph.Rating != 5 {
		t.Fatalf("five stars did not star: %+v", ph)
	}
	cfg, ok := loadConfig()
	if !ok || len(cfg.Favorites) != 1 || cfg.Favorites[0] != path {
		t.Fatalf("the old favourites list was not mirrored: %+v", cfg)
	}
	// Clear writes an explicit 0 (not a missing entry), so an older
	// path-keyed favourite cannot resurrect the rating.
	if err := s.setCullState(ph, 0, false); err != nil {
		t.Fatal(err)
	}
	if ph.Favorite || ph.Rating != 0 {
		t.Fatalf("clear did not clear: %+v", ph)
	}
	sc := readCullSidecar(dir)
	if d, ok := sc.Photos["SDIM0010.X3F"]; !ok || d.Rating != 0 || d.Reject {
		t.Fatalf("clear was not recorded explicitly: %+v", sc)
	}
	cfg, _ = loadConfig()
	if len(cfg.Favorites) != 0 {
		t.Fatalf("the favourite mirror kept a cleared star: %+v", cfg.Favorites)
	}
}

func TestHandleCullRatingAndReject(t *testing.T) {
	cullTestConfig(t)
	dir := t.TempDir()
	path, _ := writeCullPhoto(t, dir, "SDIM0020.X3F")
	ph := &Photo{ID: "abc", Name: "SDIM0020.X3F", path: path}
	s := &server{byID: map[string]*Photo{"abc": ph}}

	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/cull", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handleCull(w, r)
		return w
	}
	if w := post(`{"id":"abc","rating":3}`); w.Code != 200 {
		t.Fatalf("rating: %d %s", w.Code, w.Body)
	}
	if ph.Rating != 3 || ph.Reject || ph.Favorite {
		t.Fatalf("rating state %+v", ph)
	}
	if w := post(`{"id":"abc","reject":true}`); w.Code != 200 {
		t.Fatalf("reject: %d %s", w.Code, w.Body)
	}
	if ph.Rating != 3 || !ph.Reject {
		t.Fatalf("reject must keep the rating: %+v", ph)
	}
	var got map[string]any
	json.Unmarshal(post(`{"id":"abc","clear":true}`).Body.Bytes(), &got)
	if got["rating"].(float64) != 0 || got["reject"].(bool) {
		t.Fatalf("clear response %+v", got)
	}
	if w := post(`{"id":"abc","rating":9}`); w.Code != 400 {
		t.Fatalf("rating 9 should be refused, got %d", w.Code)
	}
	if w := post(`{"id":"nope","rating":3}`); w.Code != 404 {
		t.Fatalf("unknown photo should be 404, got %d", w.Code)
	}
	// Rejecting never removes the original.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the original went away: %v", err)
	}
}
