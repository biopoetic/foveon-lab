package main

// Manual check: with FL_TIMING_PHOTO set to an X3F path, TestRenderTiming
// prints how long each stage of a raw preview takes.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

func TestRenderTiming(t *testing.T) {
	path := os.Getenv("FL_TIMING_PHOTO")
	if path == "" {
		t.Skip("FL_TIMING_PHOTO not set")
	}
	var ph *Photo
	for _, p := range scanPhotos([]string{filepath.Dir(path)}, false) {
		if filepath.Base(p.path) == filepath.Base(path) {
			ph = p
			break
		}
	}
	if ph == nil {
		t.Fatalf("%s not found", path)
	}

	t0 := time.Now()
	f, err := x3f.Open(ph.path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.CAMF()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.DecodeRaw()
	if err != nil {
		t.Fatal(err)
	}
	tOpen := time.Since(t0)

	t1 := time.Now()
	n := x3f.FixBadPixels(raw, c)
	tBad := time.Since(t1)

	t2 := time.Now()
	wb := "Sunlight"
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		t.Fatal(err)
	}
	tCam := time.Since(t2)

	t3 := time.Now()
	x3f.Denoise(img, x3f.DenoiseOptions{Strength: defaultNR, Scale: 1, ISO: photoISO(ph), Grey: greyOf(render.CamTransform{})})
	tDen := time.Since(t3)

	t4 := time.Now()
	if _, err := rawLinear(ph, sourceW, "sRGB"); err != nil {
		t.Fatal(err)
	}
	tPreview := time.Since(t4)
	t.Logf("preview (sourceW %d): %s", sourceW, tPreview.Round(time.Millisecond))
	t.Logf("%s: open+decode %s, bad pixels (%d) %s, camera linear %s, denoise %s, total %s",
		filepath.Base(path), tOpen.Round(time.Millisecond), n, tBad.Round(time.Millisecond),
		tCam.Round(time.Millisecond), tDen.Round(time.Millisecond),
		time.Since(t0).Round(time.Millisecond))
}
