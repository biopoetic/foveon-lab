package main

// The batch scheduler's contract: every photo gets one honest progress line,
// a cancel leaves no file under a final name (and no temporary behind), the
// memory gate admits a job only when it fits, and two exports racing for the
// same name never replace one another.

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bpPhotos(t *testing.T, n int) ([]*Photo, map[string]*Photo) {
	t.Helper()
	dir := t.TempDir()
	var photos []*Photo
	byID := map[string]*Photo{}
	for i := 0; i < n; i++ {
		name := "photo" + string(rune('a'+i)) + ".png"
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 16, 12))
		for y := 0; y < 12; y++ {
			for x := 0; x < 16; x++ {
				img.Set(x, y, color.RGBA{uint8(x * 15), uint8(y * 20), uint8(40 + i*30), 255})
			}
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
		ph := &Photo{ID: "p" + string(rune('a'+i)), Name: name, Kind: "image", path: p, Info: map[string]string{}}
		photos = append(photos, ph)
		byID[ph.ID] = ph
	}
	return photos, byID
}

func TestBatchProgressLines(t *testing.T) {
	useTemporaryConfig(t)
	photos, byID := bpPhotos(t, 4)
	dest := t.TempDir()
	s := &server{byID: byID, sem: make(chan struct{}, 4)}

	ids := make([]string, len(photos))
	for i, p := range photos {
		ids[i] = p.ID
	}
	body, _ := json.Marshal(map[string]any{"ids": ids, "format": "jpeg", "look": "Neutral", "destination": dest})
	rec := httptest.NewRecorder()
	s.handleExportBatch(rec, httptest.NewRequest("POST", "/api/export/batch", bytes.NewReader(body)))

	var seen []float64
	var done map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("bad line %q: %v", ln, err)
		}
		if m["done"] == true {
			done = m
			continue
		}
		if m["error"] != nil {
			t.Fatalf("unexpected error line: %v", m)
		}
		seen = append(seen, m["i"].(float64))
	}
	if done == nil {
		t.Fatal("no done line")
	}
	if len(seen) != len(photos) {
		t.Fatalf("%d progress lines for %d photos", len(seen), len(photos))
	}
	for i, v := range seen {
		if v != float64(i+1) {
			t.Fatalf("progress line %d says i=%v, want %d", i, v, i+1)
		}
	}
	if done["ok"] != float64(len(photos)) || done["failed"] != float64(0) {
		t.Fatalf("done says ok=%v failed=%v", done["ok"], done["failed"])
	}
	files, _ := filepath.Glob(filepath.Join(dest, "*.jpg"))
	if len(files) != len(photos) {
		t.Fatalf("%d files exported, want %d", len(files), len(photos))
	}
}

func TestBatchCancelLeavesNoFile(t *testing.T) {
	useTemporaryConfig(t)
	photos, byID := bpPhotos(t, 3)
	dest := t.TempDir()
	s := &server{byID: byID, sem: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body, _ := json.Marshal(map[string]any{"ids": []string{photos[0].ID, photos[1].ID}, "format": "jpeg", "destination": dest})
	rec := httptest.NewRecorder()
	s.handleExportBatch(rec, httptest.NewRequest("POST", "/api/export/batch", bytes.NewReader(body)).WithContext(ctx))
	if strings.Contains(rec.Body.String(), `"done":true`) {
		t.Fatalf("a cancelled batch sent a done line: %s", rec.Body.String())
	}
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		t.Fatalf("a cancelled export left %q", e.Name())
	}
}

func TestMemGateFitsTheBudget(t *testing.T) {
	g := newMemGate(100)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if !g.acquire(ctx, 60) {
		t.Fatal("the first job should fit")
	}
	if g.acquire(ctx, 60) {
		t.Fatal("a second job that does not fit was admitted")
	}
	g.release(60)
	if !g.acquire(context.Background(), 90) {
		t.Fatal("after the release the budget should be free")
	}
	g.release(90)
}

func TestCommitTempNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	taken := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(taken, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp, err := os.CreateTemp(dir, ".foveon-lab-*.part.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString("new"); err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	got, err := commitTemp(tmp.Name(), dir, "photo", ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	if got == taken {
		t.Fatal("commitTemp replaced an existing file")
	}
	old, _ := os.ReadFile(taken)
	if string(old) != "old" {
		t.Fatal("the existing file changed")
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("the new file is missing: %v", err)
	}
}
