package main

// Benchmarks for the render path, so an optimisation can be measured instead
// of felt. Set FL_BENCH_PHOTO to an X3F path (a 15 MP SD1 frame is a fair
// worst case) and run:
//
//	go test -run XXX -bench . -benchmem .
//
// The micro benchmarks use the same photo, decoded once in setup, so they
// measure the stage and not the file read. tools/bench.ps1 runs this and
// keeps the output for comparison.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

// benchPhoto is the photo the benchmarks run on: FL_BENCH_PHOTO, else the
// first X3F under the samples folder, else the benchmarks skip.
func benchPhoto(tb testing.TB) *Photo {
	tb.Helper()
	path := os.Getenv("FL_BENCH_PHOTO")
	if path == "" {
		home, _ := os.UserHomeDir()
		for _, dir := range []string{
			filepath.Join(home, "Desktop", "foveon-samples", "SD1"),
			filepath.Join(home, "Desktop", "foveon-samples", "dp2-Quattro"),
		} {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if filepath.Ext(e.Name()) == ".x3f" {
					path = filepath.Join(dir, e.Name())
					break
				}
			}
			if path != "" {
				break
			}
		}
	}
	if path == "" {
		tb.Skip("no photo: set FL_BENCH_PHOTO")
	}
	var ph *Photo
	for _, p := range scanPhotos([]string{filepath.Dir(path)}, false) {
		if filepath.Base(p.path) == filepath.Base(path) {
			ph = p
			break
		}
	}
	if ph == nil {
		tb.Fatalf("%s not found", path)
	}
	tb.Logf("photo: %s", ph.path)
	return ph
}

// benchDecoded opens and decodes the photo once per benchmark, outside the
// timed loop, and hands back the raw planes and CAMF.
func benchDecoded(tb testing.TB, ph *Photo) (*x3f.RawImage, *x3f.CAMF, *x3f.File) {
	tb.Helper()
	f, err := x3f.Open(ph.path)
	if err != nil {
		tb.Fatal(err)
	}
	c, err := f.CAMF()
	if err != nil {
		tb.Fatal(err)
	}
	raw, err := f.DecodeRaw()
	if err != nil {
		tb.Fatal(err)
	}
	x3f.FixBadPixels(raw, c)
	return raw, c, f
}

func BenchmarkPreview(b *testing.B) {
	ph := benchPhoto(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := rawLinear(ph, sourceW, "sRGB"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDevelop(b *testing.B) {
	ph := benchPhoto(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := goDevelopFor(ph, false, false, ""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeRaw(b *testing.B) {
	ph := benchPhoto(b)
	f, err := x3f.Open(ph.path)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.DecodeRaw(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeSection decodes the raw section from memory, so the file
// read and its cache effects stay out of the measurement.
func BenchmarkDecodeSection(b *testing.B) {
	ph := benchPhoto(b)
	f, err := x3f.Open(ph.path)
	if err != nil {
		b.Fatal(err)
	}
	im, ok := f.RawSection()
	if !ok {
		b.Skip("no raw section")
	}
	data, err := os.ReadFile(ph.path)
	if err != nil {
		b.Fatal(err)
	}
	sec := data[im.Offset : im.Offset+im.Length]
	payload := sec[28:]
	w, h := int(im.Width), int(im.Height)
	quattro := im.Format == 35 || im.Format == 37 || im.Format == 39
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := x3f.DecodeTrueForBench(payload, w, h, quattro); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFixBadPixels(b *testing.B) {
	ph := benchPhoto(b)
	f, err := x3f.Open(ph.path)
	if err != nil {
		b.Fatal(err)
	}
	c, err := f.CAMF()
	if err != nil {
		b.Fatal(err)
	}
	raw, err := f.DecodeRaw()
	if err != nil {
		b.Fatal(err)
	}
	src := append([]uint16(nil), raw.Pix...)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copy(raw.Pix, src)
		x3f.FixBadPixels(raw, c)
	}
}

func BenchmarkCameraLinear(b *testing.B) {
	ph := benchPhoto(b)
	raw, c, f := benchDecoded(b, ph)
	wb := developWB(c, ph)
	sg := x3f.NewSpatialGain(c, f.Props, wb)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := x3f.CameraLinear(raw, c, sg, wb); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBin(b *testing.B) {
	ph := benchPhoto(b)
	raw, c, f := benchDecoded(b, ph)
	wb := developWB(c, ph)
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		b.Fatal(err)
	}
	bx := max(1, (img.W+sourceW-1)/sourceW)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = binCam2(img, bx, bx)
	}
}

func BenchmarkDenoise(b *testing.B) {
	ph := benchPhoto(b)
	raw, c, f := benchDecoded(b, ph)
	wb := developWB(c, ph)
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	if err != nil {
		b.Fatal(err)
	}
	// At the preview's scale, as the app runs it.
	bx := max(1, (img.W+sourceW-1)/sourceW)
	binned := binCam2(img, bx, bx)
	opts := x3f.DenoiseOptions{Strength: defaultNR, Scale: 1 / float64(bx), ISO: photoISO(ph), Grey: greyOf(render.CamTransform{})}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cp := *binned
		cp.Pix = append([]float32(nil), binned.Pix...)
		x3f.Denoise(&cp, opts)
	}
}

func BenchmarkToLinear(b *testing.B) {
	ph := benchPhoto(b)
	raw, c, f := benchDecoded(b, ph)
	wb := developWB(c, ph)
	img, err := x3f.CameraLinear(raw, c, x3f.NewSpatialGain(c, f.Props, wb), wb)
	_, cc, err := c.WBTransform(wb)
	if err != nil {
		b.Fatal(err)
	}
	t := render.CamTransform{CC: cc}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = render.FromCamLinear(img.Pix, img.W, img.H, sourceW, 0, t)
	}
}

// TestBenchSummary prints one line per stage with the wall time it took, so
// a run without -bench still says where the time goes:
//
//	FL_BENCH_SUMMARY=1 FL_BENCH_PHOTO=... go test -run TestBenchSummary -v .
func TestBenchSummary(t *testing.T) {
	if os.Getenv("FL_BENCH_SUMMARY") == "" {
		t.Skip("set FL_BENCH_SUMMARY=1 (and FL_BENCH_PHOTO)")
	}
	ph := benchPhoto(t)

	start := time.Now()
	bf, err := x3f.Open(ph.path)
	if err != nil {
		t.Fatal(err)
	}
	tOpen := time.Since(start)
	start = time.Now()
	c, err := bf.CAMF()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bf.DecodeRaw()
	if err != nil {
		t.Fatal(err)
	}
	tRaw := time.Since(start)
	start = time.Now()
	x3f.FixBadPixels(raw, c)
	tBad := time.Since(start)
	f := bf
	tDecode := tOpen + tRaw + tBad

	start = time.Now()
	wb := developWB(c, ph)
	sg := x3f.NewSpatialGain(c, f.Props, wb)
	img, err := x3f.CameraLinear(raw, c, sg, wb)
	if err != nil {
		t.Fatal(err)
	}
	tCam := time.Since(start)

	start = time.Now()
	bx := max(1, (img.W+sourceW-1)/sourceW)
	binned := binCam2(img, bx, bx)
	tBin := time.Since(start)

	_, cc, err := c.WBTransform(wb)
	if err != nil {
		t.Fatal(err)
	}
	tr := render.CamTransform{CC: cc}
	opts := x3f.DenoiseOptions{Strength: defaultNR, Scale: 1 / float64(bx), ISO: photoISO(ph), Grey: greyOf(tr)}

	start = time.Now()
	cp := *binned
	cp.Pix = append([]float32(nil), binned.Pix...)
	x3f.Denoise(&cp, opts)
	tDen := time.Since(start)

	start = time.Now()
	_ = render.FromCamLinear(cp.Pix, cp.W, cp.H, sourceW, 0, tr)
	tLin := time.Since(start)

	start = time.Now()
	if _, err := rawLinear(ph, sourceW, "sRGB"); err != nil {
		t.Fatal(err)
	}
	tAll := time.Since(start)

	total := tDecode + tCam + tBin + tDen + tLin
	fmt.Printf("\n%s (%dx%d, bin %d)\n", filepath.Base(ph.path), img.W, img.H, bx)
	for _, row := range []struct {
		name string
		d    time.Duration
	}{
		{"open", tOpen}, {"decode raw", tRaw}, {"bad pixels", tBad}, {"camera linear", tCam}, {"bin", tBin},
		{"denoise", tDen}, {"to linear", tLin},
	} {
		fmt.Printf("  %-14s %7.1f ms  %4.0f%%\n", row.name, float64(row.d.Microseconds())/1000, 100*float64(row.d)/float64(total))
	}
	fmt.Printf("  %-14s %7.1f ms  (measured separately)\n", "sum", float64(total.Microseconds())/1000)
	fmt.Printf("  %-14s %7.1f ms  (the whole preview)\n", "preview", float64(tAll.Microseconds())/1000)
}
