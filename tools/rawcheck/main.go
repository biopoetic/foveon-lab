// rawcheck compares x3f.DecodeRaw with x3f_extract -unprocessed -no-crop,
// value by value, and times both.
//
//	go run ./tools/rawcheck <x3f_extract> <file.X3F>...
package main

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/biopoetic/foveon-lab/x3f"
	"golang.org/x/image/tiff"
)

func main() {
	tool := os.Args[1]
	tmp, _ := os.MkdirTemp("", "rawcheck")
	defer os.RemoveAll(tmp)
	for _, p := range os.Args[2:] {
		f, err := x3f.Open(p)
		if err != nil {
			fmt.Println(p, err)
			continue
		}
		t0 := time.Now()
		raw, err := f.DecodeRaw()
		goT := time.Since(t0)
		if err != nil {
			fmt.Println(filepath.Base(p), "go:", err)
			continue
		}
		t0 = time.Now()
		if out, err := exec.Command(tool, "-q", "-tiff", "-unprocessed", "-no-crop", "-o", tmp, p).CombinedOutput(); err != nil {
			fmt.Println(p, err, string(out))
			continue
		}
		cT := time.Since(t0)
		tp := filepath.Join(tmp, filepath.Base(p)+".tif")
		fh, _ := os.Open(tp)
		img, err := tiff.Decode(fh)
		fh.Close()
		os.Remove(tp)
		if err != nil {
			fmt.Println(p, "tiff:", err)
			continue
		}
		m, ok := img.(*image.RGBA64)
		if !ok {
			fmt.Printf("%s: tiff is %T\n", filepath.Base(p), img)
			continue
		}
		b := m.Bounds()
		if b.Dx() != raw.W || b.Dy() != raw.H {
			fmt.Printf("%s: size go %dx%d vs extract %dx%d\n", filepath.Base(p), raw.W, raw.H, b.Dx(), b.Dy())
			continue
		}
		diff, first := 0, ""
		for y := 0; y < raw.H; y++ {
			for x := 0; x < raw.W; x++ {
				o := m.PixOffset(x, y)
				for c := 0; c < 3; c++ {
					want := uint16(m.Pix[o+2*c])<<8 | uint16(m.Pix[o+2*c+1])
					if got := raw.Pix[(y*raw.W+x)*3+c]; got != want {
						if diff == 0 {
							first = fmt.Sprintf(" first at (%d,%d) c%d: go %d extract %d", x, y, c, got, want)
						}
						diff++
					}
				}
			}
		}
		fmt.Printf("%s %s %dx%d: %d differing values%s  (go %v, x3f_extract %v)\n", filepath.Base(p), f.Version, raw.W, raw.H, diff, first, goT.Round(time.Millisecond), cT.Round(time.Millisecond))
	}
}
