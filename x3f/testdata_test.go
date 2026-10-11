package x3f

import (
	"bytes"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// TestSampleLibrary parses every X3F under ../testdata (sample files from
// other Foveon cameras, git-ignored) and logs what each one contains. It
// fails only on parse errors or a preview that does not decode; it skips
// when no samples are present.
func TestSampleLibrary(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "testdata", "*", "*.[Xx]3[Ff]"))
	if len(files) == 0 {
		t.Skip("no samples in ../testdata")
	}
	for _, p := range files {
		f, err := Open(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		t.Logf("%-14s v%s model=%q camera=%q rot=%d full-preview=%v",
			filepath.Base(filepath.Dir(p)), f.Version, f.Props["CAMMODEL"], f.Camera(), f.Rotation, f.HasFullPreview())
		for _, im := range f.Images {
			t.Logf("    %s type=%d format=%d %dx%d len=%d", im.Type, im.ImgType, im.Format, im.Width, im.Height, im.Length)
		}
		if f.HasFullPreview() {
			data, im, err := f.PreviewJPEG()
			if err != nil {
				t.Errorf("%s: preview: %v", p, err)
				continue
			}
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				t.Errorf("%s: preview does not decode: %v", p, err)
				continue
			}
			if uint32(cfg.Width) != im.Width {
				t.Logf("    note: JPEG is %dx%d, section says %dx%d", cfg.Width, cfg.Height, im.Width, im.Height)
			}
		}
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
}
