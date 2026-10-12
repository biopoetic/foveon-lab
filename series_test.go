package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func TestRenderSheetLayout(t *testing.T) {
	s := &server{sem: make(chan struct{}, 2), sources: newLRU(4), bases: newLRU(8)}
	var phs []*Photo
	for i := 0; i < 5; i++ {
		phs = append(phs, &Photo{ID: string(rune('a' + i)), Name: "x", Kind: "image", path: writeTestPNG(t, 300, 200)})
	}
	img, err := s.renderSheet(phs, preset.Neutral(), false, 600)
	if err != nil {
		t.Fatal(err)
	}
	// 5 photos -> 3 columns x 2 rows of 3:2 cells.
	cellW := (600 - 2*4) / 3
	if b := img.Bounds(); b.Dx() != 3*cellW+2*4 || b.Dy() != 2*(cellW*2/3)+4 {
		t.Fatalf("sheet %v", b)
	}
}

func writeTestPNG(t *testing.T, w, h int) string {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(i)
	}
	p := filepath.Join(t.TempDir(), "x.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, img)
	f.Close()
	return p
}
