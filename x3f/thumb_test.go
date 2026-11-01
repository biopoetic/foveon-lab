package x3f

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestEXIFThumbJPEG checks that a thumbnail in the preview's APP1 is found
// and the largest one within the width limit is returned. The X3F samples
// that carry one are the Quattros and the SD14; this test needs no file.
func TestEXIFThumbJPEG(t *testing.T) {
	small := encodeTestJPEG(t, 96, 72, 40)
	large := encodeTestJPEG(t, 160, 120, 200)
	main := encodeTestJPEG(t, 32, 24, 10)

	payload := append([]byte("Exif\x00\x00"), small...)
	payload = append(payload, large...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	seg = append(seg, payload...)
	data := append([]byte{}, main[:2]...)
	data = append(data, seg...)
	data = append(data, main[2:]...)

	got := exifThumbJPEG(data, 700)
	if got == nil {
		t.Fatal("no thumbnail found")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(got))
	if err != nil || cfg.Width != 160 || cfg.Height != 120 {
		t.Fatalf("got %dx%d (%v), want 160x120", cfg.Width, cfg.Height, err)
	}
	if got := exifThumbJPEG(data, 120); got == nil {
		t.Fatal("no thumbnail under 120 px")
	} else if cfg, _ := jpeg.DecodeConfig(bytes.NewReader(got)); cfg.Width != 96 {
		t.Fatalf("under 120 px: got %d wide, want 96", cfg.Width)
	}
	if got := exifThumbJPEG(main, 700); got != nil {
		t.Fatalf("thumbnail found in a JPEG without EXIF: %d bytes", len(got))
	}
}

func encodeTestJPEG(t *testing.T, w, h, shade int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(shade + i%37)
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestSD9SD10Previews decodes the small previews of the SD9 (plain RGB) and
// SD10 (Huffman) samples. Set X3F_DUMP_DIR to also write them as PNG for a
// visual check.
func TestSD9SD10Previews(t *testing.T) {
	for _, name := range []string{"SD9/RAW_SIGMA_SD9_SRGB.X3F", "SD10/RAW_SIGMA_SD10.X3F"} {
		p := filepath.Join("..", "testdata", filepath.FromSlash(name))
		f, err := Open(p)
		if err != nil {
			t.Skipf("sample %s not present", name)
		}
		img, full, err := f.Preview()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b := img.Bounds()
		if full || b.Dx() != 567 || b.Dy() != 378 {
			t.Errorf("%s: %dx%d full=%v, want 567x378 low-res", name, b.Dx(), b.Dy(), full)
		}
		// A real photo has many distinct colours and is not black.
		seen := map[uint32]bool{}
		var sum uint64
		for y := 0; y < b.Dy(); y += 3 {
			for x := 0; x < b.Dx(); x += 3 {
				r, g, bl, _ := img.At(x, y).RGBA()
				seen[(r>>8)<<16|(g>>8)<<8|bl>>8] = true
				sum += uint64(r>>8 + g>>8 + bl>>8)
			}
		}
		mean := float64(sum) / float64(len(seen)+1)
		t.Logf("%s: %d distinct colours", name, len(seen))
		if len(seen) < 500 || mean == 0 {
			t.Errorf("%s: decoded preview looks wrong (%d colours)", name, len(seen))
		}
		if dir := os.Getenv("X3F_DUMP_DIR"); dir != "" {
			out, _ := os.Create(filepath.Join(dir, filepath.Base(filepath.Dir(p))+"_preview.png"))
			png.Encode(out, img.(*image.RGBA))
			out.Close()
		}
	}
}
