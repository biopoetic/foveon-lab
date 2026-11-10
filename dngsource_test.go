package main

// The DNG source through the app: the photo list, metadata, preview source,
// base, thumbnail, export and Publish. The file is a synthetic camera DNG
// written by render.WriteCameraDNG, so no third-party raw is needed.

import (
	"bytes"
	"context"
	"image/jpeg"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/biopoetic/foveon-lab/dng"
	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

func writeTestCameraDNG(t *testing.T, dir string, orientation uint16) (*Photo, string) {
	t.Helper()
	const w, h = 24, 16
	pix := make([]float32, w*h*3)
	for i := range pix {
		pix[i] = float32((i*17)%700) / 1000
	}
	var buf bytes.Buffer
	gain := [3]float64{1.2, 1, 1.1}
	cc := [9]float64{1.2, -0.1, -0.1, -0.1, 1.1, 0, -0.02, -0.08, 1.1}
	if err := render.WriteCameraDNG(&buf, pix, w, h, gain, cc, nil, orientation, [2]float64{1, 1}, "TEST", "Synthetic DNG", "test"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "test.dng")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Photo{ID: idOf(path), Name: "test.dng", Folder: filepath.Base(dir), path: path, Kind: "dng", Info: map[string]string{}}, path
}

func dngTestServer(ps []*Photo) *server {
	s := &server{
		sources: newLRU(4), bases: newLRU(8), thumbs: newLRU(8),
		sem: make(chan struct{}, 1), byID: map[string]*Photo{}, rawState: map[string]string{},
	}
	s.photos = ps
	for _, ph := range ps {
		s.byID[ph.ID] = ph
	}
	return s
}

func TestDNGPhotoEndToEnd(t *testing.T) {
	isolateUserDirs(t) // thumbnails/caches stay in the test folder
	dir := t.TempDir()
	ph, path := writeTestCameraDNG(t, dir, 6) // 90° clockwise
	if !loadMeta(ph) {
		t.Fatal("loadMeta returned false")
	}
	if ph.Kind != "dng" || ph.Unsupported != "" {
		t.Fatalf("kind %q unsupported %q", ph.Kind, ph.Unsupported)
	}
	if ph.Rotation != 90 {
		t.Fatalf("rotation %d, want 90", ph.Rotation)
	}
	if ph.Info["DNG"] == "" || ph.Info["CAMMODEL"] != "Synthetic DNG" {
		t.Fatalf("metadata: %v", ph.Info)
	}

	ps := scanPhotos([]string{dir}, false)
	if len(ps) != 1 || ps[0].Kind != "dng" {
		t.Fatalf("scan found %d photos (want one dng)", len(ps))
	}
	s := dngTestServer(ps)

	// The source is turned upright (the DNG is 24×16, a 90° file 16×24).
	src, err := s.sourceAs(ph, "jpg") // "jpg" must still mean the DNG
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if src.W != 16 || src.H != 24 {
		t.Fatalf("source %dx%d, want 16x24", src.W, src.H)
	}
	b, err := s.baseAs(ph, 120, "dng", render.Geometry{})
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	img := render.Render(b, preset.Neutral(), s.compensation(ph, false))
	if img.Rect.Dx() < 2 || img.Rect.Dy() < 2 {
		t.Fatalf("render %v", img.Rect)
	}
	// A preset with a film and grain must run too.
	p := preset.Neutral()
	p.Film = "Kodak Portra 400"
	p.Grain = 20
	if _, err := s.baseAs(ph, 120, "dng", render.Geometry{}); err != nil {
		t.Fatal(err)
	}
	if out := render.Render(b, p, s.compensation(ph, false)); out.Rect.Empty() {
		t.Fatal("film render empty")
	}

	// Thumbnail.
	rec := httptest.NewRecorder()
	s.handleThumb(rec, httptest.NewRequest("GET", "/api/thumb?id="+ph.ID, nil))
	if rec.Code != 200 {
		t.Fatalf("thumb: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Fatalf("thumb is not a JPEG: %v", err)
	}

	// Render like the page asks for it.
	rec = httptest.NewRecorder()
	s.handleRender(rec, httptest.NewRequest("GET", "/api/render?id="+ph.ID+"&w=120&preset=", nil))
	if rec.Code != 200 {
		t.Fatalf("render API: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Fatalf("render is not a JPEG: %v", err)
	}

	// Export.
	outDir := filepath.Join(dir, "export")
	out, err := s.exportImage(ph, preset.Neutral(), "Neutral", false, "jpeg", nil, "srgb", outDir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("export file %q: %v", out, err)
	}

	// Publish.
	site := publishSites[0]
	for _, x := range publishSites {
		if x.ID == "web" {
			site = x
		}
	}
	pub, err := s.publishOne(context.Background(), ph, preset.Neutral(), "Neutral", site, nil, false, false, nil)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if st, err := os.Stat(pub); err != nil || st.Size() == 0 {
		t.Fatalf("publish file %q: %v", pub, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("source DNG disappeared: %v", err)
	}
}

func TestDNGOrientationMatchesSpec(t *testing.T) {
	// The editor's path (Linear.Rotate by Meta.Rotation, then mirrorLinearH
	// when Meta.Mirror) must equal dng.ApplyOrientation, which writes out the
	// TIFF mappings directly, for every Orientation value.
	const w, h = 3, 2
	src := make([]float32, w*h*3)
	for i := 0; i < w*h; i++ {
		src[i*3] = float32(i + 1)
	}
	for o := 1; o <= 8; o++ {
		var m dng.Meta
		switch o {
		case 3, 4:
			m.Rotation = 180
		case 5, 6:
			m.Rotation = 90
		case 7, 8:
			m.Rotation = 270
		}
		m.Mirror = o == 2 || o == 4 || o == 5 || o == 7
		want := dng.ApplyOrientation(&dng.Image{W: w, H: h, Pix: append([]float32(nil), src...)}, o)
		l := &render.Linear{W: w, H: h, Pix: append([]float32(nil), src...), Scale: 1, Frame: w}
		if m.Rotation != 0 {
			l = l.Rotate(m.Rotation)
		}
		if m.Mirror {
			mirrorLinearH(l)
		}
		if l.W != want.W || l.H != want.H {
			t.Fatalf("orientation %d: size %dx%d, want %dx%d", o, l.W, l.H, want.W, want.H)
		}
		for i := 0; i < w*h; i++ {
			if l.Pix[i*3] != want.Pix[i*3] {
				t.Fatalf("orientation %d: got %v, want %v", o, l.Pix, want.Pix)
			}
		}
	}
}

func TestDNGLayersAreSanitised(t *testing.T) {
	ph := &Photo{Kind: "dng"}
	p := preset.Neutral()
	p.Filter = "ir-ir720"
	p.LayerBottom, p.LayerMiddle, p.LayerTop = 2, -1, 0.5
	p.WhiteBalance = 3
	p.CamMode = "Vivid"
	got := sourceParams(ph, p)
	if got.Filter != "" || got.LayerBottom != 0 || got.LayerMiddle != 0 || got.LayerTop != 0 || got.WhiteBalance != 0 || got.CamMode != "" {
		t.Fatalf("Foveon-only settings survived: %+v", got)
	}
	x := &Photo{Kind: "x3f"}
	if keep := sourceParams(x, p); keep.Filter != "ir-ir720" || keep.LayerBottom != 2 {
		t.Fatal("an X3F's layer settings were stripped")
	}
}
