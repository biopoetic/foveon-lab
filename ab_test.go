package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeABPNG(t *testing.T, path string, w, h int, c color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), c.B, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func abServer(t *testing.T, photoPath string) *server {
	t.Helper()
	useTemporaryConfig(t)
	ph := &Photo{ID: "p1", Name: filepath.Base(photoPath), Kind: "image", path: photoPath, Info: map[string]string{}}
	return &server{
		byID:    map[string]*Photo{"p1": ph},
		sources: newLRU(4), bases: newLRU(4), thumbs: newLRU(4),
		sem: make(chan struct{}, 1), rawState: map[string]string{},
	}
}

func abPrepare(t *testing.T, s *server, refPath string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": "p1", "path": refPath, "comp": false})
	rr := httptest.NewRecorder()
	s.handleABPrepare(rr, httptest.NewRequest("POST", "/api/ab/prepare", bytes.NewReader(body)))
	var out map[string]any
	if rr.Code == 200 {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad prepare JSON: %v", err)
		}
	}
	return rr, out
}

func TestABComparisonAndReveal(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "own.png")
	ref := filepath.Join(dir, "other.png")
	writeABPNG(t, own, 800, 600, color.RGBA{0, 0, 128, 255})
	writeABPNG(t, ref, 800, 600, color.RGBA{0, 0, 200, 255})
	s := abServer(t, own)
	rr, out := abPrepare(t, s, ref)
	if rr.Code != 200 {
		t.Fatalf("prepare: %d %s", rr.Code, rr.Body.String())
	}
	if out["width"].(float64) != 800 || out["height"].(float64) != 600 {
		t.Fatalf("common size %v x %v, want 800 x 600", out["width"], out["height"])
	}
	tok := out["token"].(string)
	if bytes.Contains(rr.Body.Bytes(), []byte("other.png")) {
		t.Fatal("the reference file name reached the page")
	}
	for _, side := range []string{"a", "b"} {
		ir := httptest.NewRecorder()
		s.handleABImg(ir, httptest.NewRequest("GET", "/api/ab/img?token="+tok+"&i="+side, nil))
		if ir.Code != 200 {
			t.Fatalf("side %s: %d", side, ir.Code)
		}
		if ir.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("side %s is %q", side, ir.Header().Get("Content-Type"))
		}
		if ir.Body.Len() < 100 {
			t.Fatalf("side %s is empty", side)
		}
	}
	rv := httptest.NewRecorder()
	s.handleABReveal(rv, httptest.NewRequest("GET", "/api/ab/reveal?token="+tok, nil))
	var reveal map[string]string
	if err := json.Unmarshal(rv.Body.Bytes(), &reveal); err != nil {
		t.Fatal(err)
	}
	if (reveal["ours"] != "a" && reveal["ours"] != "b") || reveal["other"] == reveal["ours"] {
		t.Fatalf("bad reveal: %v", reveal)
	}
}

func TestABRejectsMismatchedFrames(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "own.png")
	ref := filepath.Join(dir, "other.png")
	writeABPNG(t, own, 800, 600, color.RGBA{0, 0, 128, 255})
	writeABPNG(t, ref, 900, 600, color.RGBA{0, 0, 200, 255})
	s := abServer(t, own)
	rr, _ := abPrepare(t, s, ref)
	if rr.Code != 409 {
		t.Fatalf("mismatched frames: %d %s, want 409", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("do not line up")) {
		t.Fatalf("unclear message: %s", rr.Body.String())
	}
}

func TestABExpiredToken(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "own.png")
	writeABPNG(t, own, 400, 300, color.RGBA{0, 0, 128, 255})
	s := abServer(t, own)
	rr := httptest.NewRecorder()
	s.handleABImg(rr, httptest.NewRequest("GET", "/api/ab/img?token=nope&i=a", nil))
	if rr.Code != 404 {
		t.Fatalf("expired token: %d", rr.Code)
	}
}
