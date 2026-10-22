package main

// A manual check, not part of the suite: with FL_DUMP_PHOTO set to an X3F
// path, TestDumpPreview renders it through the same path the accurate
// preview uses and writes dist/preview-dump.png, so the result can be looked
// at without the browser.

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

func TestDumpPreview(t *testing.T) {
	path := os.Getenv("FL_DUMP_PHOTO")
	if path == "" {
		t.Skip("FL_DUMP_PHOTO not set")
	}
	var ph *Photo
	for _, p := range scanPhotos([]string{filepath.Dir(path)}, false) {
		if filepath.Base(p.path) == filepath.Base(path) {
			ph = p
			break
		}
	}
	if ph == nil {
		t.Fatalf("%s not found by the scanner", path)
	}
	l, err := rawLinear(ph, 1600, "sRGB")
	if err != nil {
		t.Fatal(err)
	}
	b := render.NewBase(l)
	img := render.Render(b, preset.Neutral(), render.Compensation{SatFactor: 1})
	out := filepath.Join("dist", "preview-dump.png")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%dx%d)", out, img.Bounds().Dx(), img.Bounds().Dy())
}
