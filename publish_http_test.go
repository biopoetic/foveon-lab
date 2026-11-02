package main

// The HTTP path the Publish dialog uses: real files, the real routes, a real
// streaming response.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishOverHTTP(t *testing.T) {
	useTemporaryConfig(t)
	dir := t.TempDir()
	ph := publishPhoto(t, dir, "http.png", 1600, 900)
	s := &server{byID: map[string]*Photo{ph.ID: ph}, sem: make(chan struct{}, 1),
		openFolder: func(string) error { return nil }, openURL: func(string) error { return nil }}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/publish/sites", s.handlePublishSites)
	mux.HandleFunc("/api/publish", s.handlePublish)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/publish/sites")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var sites struct {
		Sites []publishSite `json:"sites"`
	}
	if err := json.Unmarshal(body, &sites); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int{
		"instagram-portrait":  {1080, 1350},
		"instagram-square":    {1080, 1080},
		"instagram-landscape": {1080, 566},
	}
	for _, site := range sites.Sites {
		if w, ok := want[site.ID]; ok && (site.W != w[0] || site.H != w[1]) {
			t.Errorf("%s is %dx%d, want %dx%d", site.ID, site.W, site.H, w[0], w[1])
		}
		if site.URL != "" && !strings.HasPrefix(site.URL, "https://") {
			t.Errorf("%s URL %q", site.ID, site.URL)
		}
	}
	if len(sites.Sites) != len(publishSites) {
		t.Fatalf("%d sites over HTTP, want %d", len(sites.Sites), len(publishSites))
	}

	post := func(v any) string {
		t.Helper()
		b, _ := json.Marshal(v)
		res, err := http.Post(ts.URL+"/api/publish", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return string(out)
	}
	base := map[string]any{"ids": []string{ph.ID}, "site": "instagram-portrait", "look": "Neutral"}
	out := post(base)
	if !strings.Contains(out, `"failed":1`) {
		t.Fatalf("a wide photo was published without a crop: %s", out)
	}
	base["crops"] = map[string]any{ph.ID: map[string]float64{"x": 0.275, "y": 0, "w": 0.45, "h": 1}}
	out = post(base)
	if !strings.Contains(out, `"ok":1`) {
		t.Fatalf("crop publish failed: %s", out)
	}
	var last map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("bad line %q: %v", ln, err)
		}
		if m["done"] == true {
			last = m
		}
	}
	if last == nil {
		t.Fatal("no done line over HTTP")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "publish", "*.jpg"))
	if len(files) != 1 {
		t.Fatalf("%d files, want 1", len(files))
	}
	if w, h := jpegSize(t, files[0]); w != 1080 || h != 1350 {
		t.Fatalf("size %dx%d", w, h)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatal(err)
	}
}
