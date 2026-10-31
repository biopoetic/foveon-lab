package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/biopoetic/foveon-lab/preset"
)

// useTemporaryConfig keeps every test away from the real config file.
func useTemporaryConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

// fakeCard builds a card folder with DCIM/100SIGMA files.
func fakeCard(t *testing.T, names map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "DCIM", "100SIGMA")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScanCardSkipsCompanionJPEG(t *testing.T) {
	root := fakeCard(t, map[string]string{
		"SDIM0001.X3F": "raw one",
		"SDIM0001.JPG": "camera jpeg beside it",
		"SDIM0002.X3F": "raw two",
	})
	files, err := scanCard(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2: %+v", len(files), files)
	}
	for _, f := range files {
		if strings.EqualFold(filepath.Ext(f.Path), ".jpg") {
			t.Fatalf("a camera JPEG was listed as a photo: %s", f.Path)
		}
	}
}

func TestImportCopiesAndResumesWithoutDuplicates(t *testing.T) {
	useTemporaryConfig(t)
	root := fakeCard(t, map[string]string{
		"SDIM0001.X3F": "first raw",
		"SDIM0002.X3F": "second raw",
	})
	cardBefore := map[string]string{}
	cardMod := map[string]time.Time{}
	for name := range map[string]bool{"SDIM0001.X3F": true, "SDIM0002.X3F": true} {
		p := filepath.Join(root, "DCIM", "100SIGMA", name)
		sum, err := hashFile(p)
		if err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(p)
		cardBefore[name], cardMod[name] = sum, st.ModTime()
	}
	library := filepath.Join(t.TempDir(), "library")
	s := &server{byID: map[string]*Photo{}, sem: make(chan struct{}, 1)}

	lines := runImport(t, s, root, library)
	if got := lines[len(lines)-1].Copied; got != 2 {
		t.Fatalf("copied %d, want 2: %+v", got, lines)
	}
	for name, sum := range cardBefore {
		p := filepath.Join(root, "DCIM", "100SIGMA", name)
		after, err := hashFile(p)
		if err != nil || after != sum {
			t.Fatalf("%s changed on the card", name)
		}
		if st, _ := os.Stat(p); !st.ModTime().Equal(cardMod[name]) {
			t.Fatalf("%s has a new date on the card", name)
		}
	}
	m, err := loadManifest(library)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 2 {
		t.Fatalf("manifest has %d entries, want 2", len(m.Files))
	}
	for _, e := range m.Files {
		got, err := os.ReadFile(e.Dest)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := os.ReadFile(filepath.Join(root, "DCIM", "100SIGMA", e.Name))
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: copied bytes differ from the card", e.Name)
		}
		sum := sha256.Sum256(got)
		if hex.EncodeToString(sum[:]) != e.SHA256 {
			t.Fatalf("%s: recorded hash does not match the copy", e.Name)
		}
	}

	// A second import of the same card copies nothing.
	lines = runImport(t, s, root, library)
	last := lines[len(lines)-1]
	if last.Copied != 0 || last.Skipped != 2 {
		t.Fatalf("second import: copied %d skipped %d, want 0 and 2", last.Copied, last.Skipped)
	}
}

func TestImportNeverOverwrites(t *testing.T) {
	useTemporaryConfig(t)
	root := fakeCard(t, map[string]string{"SDIM0001.X3F": "new content"})
	library := filepath.Join(t.TempDir(), "library")
	s := &server{byID: map[string]*Photo{}, sem: make(chan struct{}, 1)}

	files, err := scanCard(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatal("expected one card file")
	}
	dir := filepath.Join(library, files[0].Shot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "SDIM0001.X3F")
	if err := os.WriteFile(old, []byte("older, different file"), 0o644); err != nil {
		t.Fatal(err)
	}

	runImport(t, s, root, library)
	got, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "older, different file" {
		t.Fatal("the existing file was overwritten")
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 {
		t.Fatalf("got files %v, want the old one and a second copy", names)
	}
	found := false
	for _, n := range names {
		if strings.Contains(n, "(2)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the second copy was not given a new name: %v", names)
	}
}

func TestImportRejectsLibraryOnCard(t *testing.T) {
	useTemporaryConfig(t)
	root := fakeCard(t, map[string]string{"SDIM0001.X3F": "raw"})
	s := &server{byID: map[string]*Photo{}, sem: make(chan struct{}, 1)}
	body, _ := json.Marshal(map[string]string{"root": root, "library": filepath.Join(root, "library")})
	rr := httptest.NewRecorder()
	s.handleCardImport(rr, httptest.NewRequest("POST", "/api/card/import", bytes.NewReader(body)))
	if rr.Code != 400 {
		t.Fatalf("status %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "library")); !os.IsNotExist(err) {
		t.Fatal("a library folder was created on the card")
	}
}

func TestNewCardFilesUsesHashWhenDateDiffers(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "SDIM0001.X3F")
	if err := os.WriteFile(a, []byte("same picture"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("same picture"))
	m := &importManifest{Version: 1, Files: []importEntry{{Name: "SDIM0001.X3F", Size: 12, Mod: 1, SHA256: hex.EncodeToString(sum[:])}}}
	files := []cardFile{{Path: a, Name: "SDIM0001.X3F", Size: 12, Mod: 2}}
	fresh, already, err := newCardFiles(files, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 0 || already != 1 {
		t.Fatalf("same content with a new date: fresh %d already %d, want 0 and 1", len(fresh), already)
	}
	if err := os.WriteFile(a, []byte("other file!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	files = []cardFile{{Path: a, Name: "SDIM0001.X3F", Size: 12, Mod: 2}}
	fresh, already, err = newCardFiles(files, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || already != 0 {
		t.Fatalf("different content with the same name and size: fresh %d already %d, want 1 and 0", len(fresh), already)
	}
}

func TestCleanupPartialsRemovesOnlyOurTemps(t *testing.T) {
	lib := t.TempDir()
	day := filepath.Join(lib, "2026-10-16")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(day, cardPartPrefix+"123")
	keep := filepath.Join(day, "SDIM0001.X3F")
	os.WriteFile(stale, []byte("half"), 0o644)
	os.WriteFile(keep, []byte("complete"), 0o644)
	cleanupPartials(lib)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a half-written temporary copy was left behind")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("a complete photo was removed")
	}
}

func TestCopyCardFileStopsWhenCancelled(t *testing.T) {
	src := filepath.Join(t.TempDir(), "SDIM0001.X3F")
	if err := os.WriteFile(src, bytes.Repeat([]byte("x"), 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := copyCardFile(ctx, src, 1<<20, dir, "SDIM0001.X3F"); err == nil {
		t.Fatal("a cancelled copy reported success")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("a cancelled copy left files behind: %v", entries)
	}
}

func TestRenameNoReplace(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "tmp")
	dst := filepath.Join(dir, "taken")
	if err := os.WriteFile(old, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(old, dst); err == nil {
		t.Fatal("rename replaced a file that was already there")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "old" {
		t.Fatal("the existing file changed")
	}
}

func TestExportNeverOverwrites(t *testing.T) {
	useTemporaryConfig(t)
	// A plain picture is enough to exercise the export path.
	src := filepath.Join(t.TempDir(), "photo.png")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 30), uint8(y * 40), 90, 255})
		}
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	ph := &Photo{ID: "p1", Name: "photo.png", Kind: "image", path: src, Info: map[string]string{}}
	s := &server{sem: make(chan struct{}, 1)}
	dir := t.TempDir()
	first, err := s.exportImage(ph, preset.Neutral(), "look", false, "jpeg", nil, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(first)
	second, err := s.exportImage(ph, preset.Neutral(), "look", false, "jpeg", nil, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("the second export replaced the first")
	}
	after, _ := os.ReadFile(first)
	if !bytes.Equal(before, after) {
		t.Fatal("the first export changed")
	}
}

func TestBatchExportCallsPhotosImporter(t *testing.T) {
	useTemporaryConfig(t)
	src := filepath.Join(t.TempDir(), "photo.png")
	f, _ := os.Create(src)
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	png.Encode(f, img)
	f.Close()
	ph := &Photo{ID: "p1", Name: "photo.png", Kind: "image", path: src, Info: map[string]string{}}
	s := &server{byID: map[string]*Photo{"p1": ph}, sem: make(chan struct{}, 1)}

	oldSupported, oldImport := photosSupported, photosImport
	photosSupported = true
	var got []string
	var album string
	photosImport = func(paths []string, a string) error {
		got, album = append([]string(nil), paths...), a
		return nil
	}
	defer func() { photosSupported, photosImport = oldSupported, oldImport }()

	body, _ := json.Marshal(map[string]any{
		"ids": []string{"p1"}, "format": "jpeg", "space": "srgb",
		"destination": t.TempDir(), "photos": true, "album": "2026-10-16",
	})
	rr := httptest.NewRecorder()
	s.handleExportBatch(rr, httptest.NewRequest("POST", "/api/export/batch", bytes.NewReader(body)))
	var done map[string]any
	for _, line := range strings.Split(strings.TrimSpace(rr.Body.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad progress line %q: %v", line, err)
		}
		if m["done"] == true {
			done = m
		}
	}
	if done == nil {
		t.Fatal("no final line")
	}
	if done["photosError"] != nil {
		t.Fatalf("Photos step failed: %v", done["photosError"])
	}
	if len(got) != 1 || album != "2026-10-16" {
		t.Fatalf("importer got %v in album %q", got, album)
	}
}

func TestBatchExportUsesEachPhotosSavedEdits(t *testing.T) {
	useTemporaryConfig(t)
	dir := t.TempDir()
	var photos []*Photo
	byID := map[string]*Photo{}
	for i, name := range []string{"one.png", "two.png"} {
		p := filepath.Join(dir, name)
		f, _ := os.Create(p)
		img := image.NewRGBA(image.Rect(0, 0, 8, 6))
		for y := 0; y < 6; y++ {
			for x := 0; x < 8; x++ {
				img.Set(x, y, color.RGBA{uint8(x * 20), uint8(y*20 + i*30), 90, 255})
			}
		}
		png.Encode(f, img)
		f.Close()
		ph := &Photo{ID: "p" + name, Name: name, Kind: "image", path: p, Info: map[string]string{}}
		photos = append(photos, ph)
		byID[ph.ID] = ph
	}
	s := &server{byID: byID, sem: make(chan struct{}, 1)}
	body, _ := json.Marshal(map[string]any{
		"ids": []string{photos[0].ID, photos[1].ID}, "format": "jpeg", "space": "srgb",
		"destination": t.TempDir(),
		"paramsById": map[string]any{
			photos[0].ID: map[string]any{"saturation": 1.5},
			photos[1].ID: map[string]any{"saturation": -1.5},
		},
	})
	rr := httptest.NewRecorder()
	s.handleExportBatch(rr, httptest.NewRequest("POST", "/api/export/batch", bytes.NewReader(body)))
	var done map[string]any
	for _, line := range strings.Split(strings.TrimSpace(rr.Body.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad progress line %q: %v", line, err)
		}
		if m["done"] == true {
			done = m
		}
	}
	if done == nil || done["ok"].(float64) != 2 || done["failed"].(float64) != 0 {
		t.Fatalf("batch with saved edits: %v", done)
	}
}

func TestBatchExportPhotosUnavailable(t *testing.T) {
	useTemporaryConfig(t)
	src := filepath.Join(t.TempDir(), "photo.png")
	f, _ := os.Create(src)
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	png.Encode(f, img)
	f.Close()
	ph := &Photo{ID: "p1", Name: "photo.png", Kind: "image", path: src, Info: map[string]string{}}
	s := &server{byID: map[string]*Photo{"p1": ph}, sem: make(chan struct{}, 1)}
	if runtime.GOOS == "darwin" {
		t.Skip("on macOS Photos is available")
	}
	body, _ := json.Marshal(map[string]any{"ids": []string{"p1"}, "format": "jpeg", "space": "srgb", "photos": true})
	rr := httptest.NewRecorder()
	s.handleExportBatch(rr, httptest.NewRequest("POST", "/api/export/batch", bytes.NewReader(body)))
	if !strings.Contains(rr.Body.String(), "photosError") {
		t.Fatalf("no Photos error in %s", rr.Body.String())
	}
}

// runImport drives the real handler over a fake card and returns its lines.
func runImport(t *testing.T, s *server, root, library string) []cardImportLine {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"root": root, "library": library})
	rr := httptest.NewRecorder()
	s.handleCardImport(rr, httptest.NewRequest("POST", "/api/card/import", bytes.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("import status %d: %s", rr.Code, rr.Body.String())
	}
	var out []cardImportLine
	for _, line := range strings.Split(strings.TrimSpace(rr.Body.String()), "\n") {
		var l cardImportLine
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatalf("bad import line %q: %v", line, err)
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		t.Fatal("no import lines")
	}
	return out
}
