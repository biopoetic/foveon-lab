// compress-exports turns the SPP calibration exports (uncompressed 8-bit
// TIFF) into lossless PNG, about half the size. A TIFF is deleted only after
// its PNG decodes pixel-identical to it. The calib package reads either
// format. Run it while nothing is reading the exports folder.
//
//	go run ./tools/compress-exports [-dir <exports>] [-skip A00] [-n 1]
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/biopoetic/foveon-lab/render"
	_ "golang.org/x/image/tiff"
)

func main() {
	home, _ := os.UserHomeDir()
	dir := flag.String("dir", filepath.Join(home, "Desktop", "foveon-calibration", "exports"), "exports folder")
	skip := flag.String("skip", "", "step codes to leave alone, comma separated (e.g. A00 while a fit reads them)")
	n := flag.Int("n", 0, "stop after this many files (0 = all)")
	flag.Parse()
	skipped := map[string]bool{}
	for _, c := range strings.Split(*skip, ",") {
		if c = strings.TrimSpace(c); c != "" {
			skipped[strings.ToUpper(c)] = true
		}
	}
	files, _ := filepath.Glob(filepath.Join(*dir, "*.tif"))
	var before, after int64
	done := 0
	for _, f := range files {
		if *n > 0 && done >= *n {
			break
		}
		stem := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		if i := strings.LastIndex(stem, "_"); i >= 0 && skipped[strings.ToUpper(stem[i+1:])] {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			log.Fatal(err)
		}
		img, _, err := render.DecodeImage(data)
		if err != nil {
			fmt.Printf("%s: %v, left as is\n", filepath.Base(f), err)
			continue
		}
		var buf bytes.Buffer
		if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
			log.Fatal(err)
		}
		back, err := png.Decode(bytes.NewReader(buf.Bytes()))
		if err != nil || !samePixels(img, back) {
			fmt.Printf("%s: PNG differs, left as is\n", filepath.Base(f))
			continue
		}
		out := strings.TrimSuffix(f, filepath.Ext(f)) + ".png"
		if err := os.WriteFile(out+".tmp", buf.Bytes(), 0o644); err != nil {
			log.Fatal(err)
		}
		if err := os.Rename(out+".tmp", out); err != nil {
			log.Fatal(err)
		}
		if err := os.Remove(f); err != nil {
			log.Fatal(err)
		}
		before += int64(len(data))
		after += int64(buf.Len())
		done++
		if done%20 == 0 {
			fmt.Printf("%d files: %.2f -> %.2f GB\n", done, float64(before)/1e9, float64(after)/1e9)
		}
	}
	fmt.Printf("done, %d files: %.2f -> %.2f GB\n", done, float64(before)/1e9, float64(after)/1e9)
}

func samePixels(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			r1, g1, b1, a1 := a.At(x, y).RGBA()
			r2, g2, b2, a2 := b.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				return false
			}
		}
	}
	return true
}
