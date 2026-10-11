package x3f

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

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
