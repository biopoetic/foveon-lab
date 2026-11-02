package main

// Publish's contract: the JPEG is the destination's size and shape, a crop is
// refused unless the caller supplied one, files are never replaced, GPS is
// absent unless asked for, and a batch writes in order and opens the folder
// and page once.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func publishPhoto(t *testing.T, dir, name string, w, h int) *Photo {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), uint8((x + y) % 256), 255})
		}
	}
	if strings.HasSuffix(name, ".png") {
		err = png.Encode(f, img)
	} else {
		err = jpeg.Encode(f, img, &jpeg.Options{Quality: 92})
	}
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	return &Photo{ID: name, Name: name, Kind: "image", path: p, Info: map[string]string{}}
}

func readPublish(t *testing.T, body string) ([]map[string]any, map[string]any) {
	t.Helper()
	var lines []map[string]any
	var done map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(body), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("bad line %q: %v", ln, err)
		}
		if m["done"] == true {
			done = m
		} else {
			lines = append(lines, m)
		}
	}
	return lines, done
}

func publishRun(t *testing.T, s *server, body map[string]any) (string, []map[string]any, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	s.handlePublish(rec, httptest.NewRequest("POST", "/api/publish", bytes.NewReader(b)))
	lines, done := readPublish(t, rec.Body.String())
	return rec.Body.String(), lines, done
}

func jpegSize(t *testing.T, path string) (int, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height
}

func TestPublishSquareWritesSiteSizeAndNeverReplaces(t *testing.T) {
	useTemporaryConfig(t)
	dir := t.TempDir()
	ph := publishPhoto(t, dir, "square.png", 1200, 1200)
	var folders, urls []string
	s := &server{byID: map[string]*Photo{ph.ID: ph}, sem: make(chan struct{}, 1),
		openFolder: func(d string) error { folders = append(folders, d); return nil },
		openURL:    func(u string) error { urls = append(urls, u); return nil }}
	body := map[string]any{"ids": []string{ph.ID}, "site": "instagram-square", "look": "Neutral"}
	_, lines, done := publishRun(t, s, body)
	if len(lines) != 1 || lines[0]["error"] != nil {
		t.Fatalf("lines %v", lines)
	}
	if done == nil || done["ok"] != float64(1) || done["failed"] != float64(0) {
		t.Fatalf("done %v", done)
	}
	path, _ := lines[0]["path"].(string)
	if path == "" {
		t.Fatal("no path")
	}
	if !strings.HasPrefix(path, filepath.Join(dir, "publish")) {
		t.Fatalf("wrote %s, want the publish folder", path)
	}
	if w, h := jpegSize(t, path); w != 1080 || h != 1080 {
		t.Fatalf("size %dx%d, want 1080x1080", w, h)
	}
	if !strings.Contains(string(mustRead(t, path)), "ICC_PROFILE") {
		t.Error("no sRGB ICC profile")
	}
	// A second publish gets a new name; the first file is untouched.
	before := mustRead(t, path)
	_, lines2, _ := publishRun(t, s, body)
	path2, _ := lines2[0]["path"].(string)
	if path2 == path {
		t.Fatalf("second publish replaced %s", path)
	}
	if !bytes.Equal(before, mustRead(t, path)) {
		t.Fatal("first publish changed")
	}
	// Two publishes, one folder and one page each time; ditto the first run.
	if len(folders) != 2 || folders[0] != filepath.Dir(path) || folders[1] != filepath.Dir(path) {
		t.Fatalf("folders opened %v", folders)
	}
	if len(urls) != 2 || urls[0] != "https://www.instagram.com/" || urls[1] != "https://www.instagram.com/" {
		t.Fatalf("urls opened %v", urls)
	}
	if next, _ := done["next"].(string); !strings.Contains(next, "Drag") {
		t.Fatalf("next %q", next)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPublishRefusesSilentCropAndAcceptsChosenCrop(t *testing.T) {
	useTemporaryConfig(t)
	dir := t.TempDir()
	ph := publishPhoto(t, dir, "wide.png", 1600, 900)
	s := &server{byID: map[string]*Photo{ph.ID: ph}, sem: make(chan struct{}, 1),
		openFolder: func(string) error { return nil }, openURL: func(string) error { return nil }}
	base := map[string]any{"ids": []string{ph.ID}, "site": "instagram-portrait", "look": "Neutral"}

	_, _, done := publishRun(t, s, base)
	if done["failed"] != float64(1) || done["ok"] != float64(0) {
		t.Fatalf("a wide photo published without a crop: %v", done)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "publish")); len(entries) != 0 {
		t.Fatalf("a refused publish left %d files", len(entries))
	}

	base["crops"] = map[string]any{ph.ID: map[string]float64{"x": 0.275, "y": 0, "w": 0.45, "h": 1}}
	_, lines, done := publishRun(t, s, base)
	if done["ok"] != float64(1) {
		t.Fatalf("crop publish failed: %v %v", done, lines)
	}
	path, _ := lines[0]["path"].(string)
	if w, h := jpegSize(t, path); w != 1080 || h != 1350 {
		t.Fatalf("size %dx%d, want 1080x1350", w, h)
	}

	base["crops"] = map[string]any{ph.ID: map[string]float64{"x": 0, "y": 0, "w": 1, "h": 1}}
	_, lines, done = publishRun(t, s, base)
	if done["failed"] != float64(1) {
		t.Fatalf("a square crop was accepted for 4:5: %v %v", done, lines)
	}
	if !strings.Contains(lines[0]["error"].(string), "not 4:5") {
		t.Fatalf("error %q", lines[0]["error"])
	}
}

func TestPublishBatchInOrderOpensOnce(t *testing.T) {
	useTemporaryConfig(t)
	dir := t.TempDir()
	a := publishPhoto(t, dir, "a.png", 800, 800)
	b := publishPhoto(t, dir, "b.png", 1000, 1000)
	var folders, urls []string
	s := &server{byID: map[string]*Photo{a.ID: a, b.ID: b}, sem: make(chan struct{}, 1),
		openFolder: func(d string) error { folders = append(folders, d); return nil },
		openURL:    func(u string) error { urls = append(urls, u); return nil }}
	_, lines, done := publishRun(t, s, map[string]any{"ids": []string{a.ID, b.ID}, "site": "instagram-square", "look": "Neutral"})
	if len(lines) != 2 || done["ok"] != float64(2) {
		t.Fatalf("lines %v done %v", lines, done)
	}
	if lines[0]["i"] != float64(1) || lines[1]["i"] != float64(2) || lines[0]["name"] != "a.png" || lines[1]["name"] != "b.png" {
		t.Fatalf("out of order: %v", lines)
	}
	if len(folders) != 1 {
		t.Fatalf("folder opened %d times, want once", len(folders))
	}
	if len(urls) != 1 {
		t.Fatalf("page opened %d times, want once", len(urls))
	}
	for _, ln := range lines {
		p, _ := ln["path"].(string)
		if w, h := jpegSize(t, p); w != 1080 || h != 1080 {
			t.Fatalf("%s is %dx%d", p, w, h)
		}
	}
}

func TestPublishFreeShapesKeepTheirShape(t *testing.T) {
	useTemporaryConfig(t)
	dir := t.TempDir()
	web := publishPhoto(t, dir, "web.png", 3000, 2000)
	s := &server{byID: map[string]*Photo{web.ID: web}, sem: make(chan struct{}, 1),
		openFolder: func(string) error { return nil }, openURL: func(string) error { return nil }}
	_, lines, done := publishRun(t, s, map[string]any{"ids": []string{web.ID}, "site": "web", "look": "Neutral"})
	if done["ok"] != float64(1) {
		t.Fatalf("web publish: %v", done)
	}
	p, _ := lines[0]["path"].(string)
	if w, h := jpegSize(t, p); w != 2048 || h != 1365 {
		t.Fatalf("web %dx%d, want 2048x1365", w, h)
	}
	// The site's own page is not opened for Web; only the folder.
	if done["url"] != "" {
		t.Fatalf("web url %q", done["url"])
	}

	five := publishPhoto(t, dir, "five.png", 1200, 800)
	s = &server{byID: map[string]*Photo{five.ID: five}, sem: make(chan struct{}, 1),
		openFolder: func(string) error { return nil }, openURL: func(string) error { return nil }}
	_, lines, done = publishRun(t, s, map[string]any{"ids": []string{five.ID}, "site": "500px", "look": "Neutral"})
	if done["ok"] != float64(1) {
		t.Fatalf("500px publish: %v", done)
	}
	p, _ = lines[0]["path"].(string)
	if w, h := jpegSize(t, p); w != 3072 || h != 2048 {
		t.Fatalf("500px %dx%d, want 3072x2048", w, h)
	}
}

// jpegWithGPS makes a small JPEG whose EXIF has Orientation 6 and a GPS IFD.
func jpegWithGPS(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	// TIFF: header (8), IFD0 (2 + 2*12 + 4 = 30) at 8, GPS IFD at 38.
	tiff := make([]byte, 56)
	copy(tiff, "II*\x00")
	binary.LittleEndian.PutUint32(tiff[4:], 8)
	binary.LittleEndian.PutUint16(tiff[8:], 2)
	// Orientation = 6.
	binary.LittleEndian.PutUint16(tiff[10:], 0x0112)
	binary.LittleEndian.PutUint16(tiff[12:], 3)
	binary.LittleEndian.PutUint32(tiff[14:], 1)
	binary.LittleEndian.PutUint16(tiff[18:], 6)
	// GPSInfo -> 38.
	binary.LittleEndian.PutUint16(tiff[22:], 0x8825)
	binary.LittleEndian.PutUint16(tiff[24:], 4)
	binary.LittleEndian.PutUint32(tiff[26:], 1)
	binary.LittleEndian.PutUint32(tiff[30:], 38)
	// GPS IFD: one entry, GPSLatitudeRef = "N".
	binary.LittleEndian.PutUint16(tiff[38:], 1)
	binary.LittleEndian.PutUint16(tiff[40:], 0x0001)
	binary.LittleEndian.PutUint16(tiff[42:], 2)
	binary.LittleEndian.PutUint32(tiff[44:], 2)
	copy(tiff[48:], "N\x00")
	payload := append([]byte("Exif\x00\x00"), tiff...)
	out := insertJPEGSegment(jb.Bytes(), 0xE1, payload)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPublishGPSIsAbsentByDefaultAndKeptOnRequest(t *testing.T) {
	useTemporaryConfig(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "place.jpg")
	jpegWithGPS(t, path)
	ph := &Photo{ID: "place", Name: "place.jpg", Kind: "image", path: path, Info: map[string]string{}}
	s := &server{byID: map[string]*Photo{ph.ID: ph}, sem: make(chan struct{}, 1),
		openFolder: func(string) error { return nil }, openURL: func(string) error { return nil }}
	_, lines, done := publishRun(t, s, map[string]any{"ids": []string{ph.ID}, "site": "instagram-square", "look": "Neutral"})
	if done["ok"] != float64(1) {
		t.Fatalf("publish: %v", done)
	}
	p, _ := lines[0]["path"].(string)
	if bytes.Contains(mustRead(t, p), []byte("Exif\x00\x00")) {
		t.Error("default publish kept EXIF (and its GPS)")
	}
	_, lines, done = publishRun(t, s, map[string]any{"ids": []string{ph.ID}, "site": "instagram-square", "look": "Neutral", "keepGps": true})
	if done["ok"] != float64(1) {
		t.Fatalf("keepGps publish: %v", done)
	}
	p, _ = lines[0]["path"].(string)
	exif := readJPEGEXIF(p)
	if exif == nil || !bytes.Contains(exif, []byte{0x25, 0x88}) {
		t.Fatal("keepGps did not keep the GPS IFD pointer")
	}
	// Orientation was reset for upright pixels.
	if got := exifOrientationForTest(t, exif); got != 1 {
		t.Fatalf("orientation %d, want 1", got)
	}
}

func exifOrientationForTest(t *testing.T, exif []byte) int {
	t.Helper()
	if len(exif) < 14 || !bytes.HasPrefix(exif, []byte("Exif\x00\x00")) {
		t.Fatal("not EXIF")
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if string(exif[6:8]) == "MM" {
		bo = binary.BigEndian
	}
	ifd := int(bo.Uint32(exif[10:]))
	n := int(bo.Uint16(exif[6+ifd:]))
	for i := 0; i < n; i++ {
		e := 6 + ifd + 2 + 12*i
		if bo.Uint16(exif[e:]) == 0x0112 {
			return int(bo.Uint16(exif[e+8:]))
		}
	}
	return 0
}
