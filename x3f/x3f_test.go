package x3f

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

// buildX3F assembles a minimal X3F container: header, one JPEG image
// section, one PROP section and the trailing directory.
func buildX3F(t *testing.T, jpg []byte, props [][2]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }

	hdr := make([]byte, 40)
	copy(hdr, "FOVb")
	binary.LittleEndian.PutUint16(hdr[4:], 3) // minor
	binary.LittleEndian.PutUint16(hdr[6:], 2) // major
	binary.LittleEndian.PutUint32(hdr[36:], 90)
	b.Write(hdr)

	imgOff := uint32(b.Len())
	b.WriteString("SECi")
	w(uint32(0))
	w(uint32(2))  // processed preview
	w(uint32(18)) // JPEG
	w(uint32(4))
	w(uint32(2))
	w(uint32(0))
	b.Write(jpg)
	imgLen := uint32(b.Len()) - imgOff

	var pool []uint16
	type ent struct{ n, v uint32 }
	var ents []ent
	for _, p := range props {
		e := ent{n: uint32(len(pool))}
		pool = append(pool, utf16.Encode([]rune(p[0]))...)
		pool = append(pool, 0)
		e.v = uint32(len(pool))
		pool = append(pool, utf16.Encode([]rune(p[1]))...)
		pool = append(pool, 0)
		ents = append(ents, e)
	}
	propOff := uint32(b.Len())
	b.WriteString("SECp")
	w(uint32(0))
	w(uint32(len(ents)))
	w(uint32(0))
	w(uint32(0))
	w(uint32(len(pool)))
	for _, e := range ents {
		w(e.n)
		w(e.v)
	}
	w(pool)
	propLen := uint32(b.Len()) - propOff

	dirOff := uint32(b.Len())
	b.WriteString("SECd")
	w(uint32(0))
	w(uint32(2))
	w(imgOff)
	w(imgLen)
	b.WriteString("IMA2")
	w(propOff)
	w(propLen)
	b.WriteString("PROP")
	w(dirOff)
	return b.Bytes()
}

func TestParseSynthetic(t *testing.T) {
	jpg := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	data := buildX3F(t, jpg, [][2]string{{"CAMMODEL", "SIGMA SD1 Merrill"}, {"ISO", "100"}})
	path := filepath.Join(t.TempDir(), "a.X3F")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != "2.3" || f.Rotation != 90 {
		t.Errorf("version/rotation = %s/%d", f.Version, f.Rotation)
	}
	if f.Props["ISO"] != "100" || f.Camera() != "SD1" {
		t.Errorf("props = %v camera = %q", f.Props, f.Camera())
	}
	got, im, err := f.PreviewJPEG()
	if err != nil || !bytes.Equal(got, jpg) || im.Width != 4 {
		t.Errorf("preview = %x %v %v", got, im, err)
	}
}

// TestHasFullPreview: a file with only a small thumbnail (like SD9/SD10)
// is recognised as unsupported; the real SD1/SD15 files are not.
func TestHasFullPreview(t *testing.T) {
	jpg := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	path := filepath.Join(t.TempDir(), "old.X3F")
	if err := os.WriteFile(path, buildX3F(t, jpg, nil), 0o644); err != nil { // 4x2 "preview"
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.HasFullPreview() {
		t.Error("tiny preview counted as full-size")
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{"SD1", "SD15"} {
		if m, _ := filepath.Glob(filepath.Join(home, "Desktop", dir, "*.X3F")); len(m) > 0 {
			if f, err := Open(m[0]); err == nil && !f.HasFullPreview() {
				t.Errorf("%s: real file reported without full preview", m[0])
			}
		}
	}
}

func TestPresetFamily(t *testing.T) {
	for in, want := range map[string]string{
		"SIGMA SD1 Merrill": "SD1", "SIGMA DP1 Merrill": "SD1", "SIGMA DP3 Merrill": "SD1",
		"SIGMA SD15": "SD15", "SIGMA SD14": "SD15", "SIGMA DP1": "SD15", "SIGMA DP2s": "SD15", "SIGMA DP1x": "SD15",
		"SIGMA dp2 Quattro": "", "SIGMA sd Quattro H": "", "SIGMA SD9": "SD9", "SIGMA SD10": "SD10", "": "",
	} {
		if got := PresetFamily(in); got != want {
			t.Errorf("PresetFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortModel(t *testing.T) {
	for in, want := range map[string]string{
		"SIGMA SD15": "SD15", "SIGMA SD1 Merrill": "SD1", "SIGMA DP2": "", "": "",
	} {
		if got := ShortModel(in); got != want {
			t.Errorf("ShortModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRealFiles runs against the user's own shots when they are present.
func TestRealFiles(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, dir := range []string{"SD1", "SD15"} {
		m, _ := filepath.Glob(filepath.Join(home, "Desktop", dir, "*.X3F"))
		if len(m) == 0 {
			t.Logf("no X3F in %s, skipping", dir)
			continue
		}
		f, err := Open(m[0])
		if err != nil {
			t.Fatalf("%s: %v", m[0], err)
		}
		jpg, im, err := f.PreviewJPEG()
		if err != nil {
			t.Fatalf("%s: %v", m[0], err)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(jpg))
		if err != nil {
			t.Fatalf("%s: preview does not decode: %v", m[0], err)
		}
		if uint32(cfg.Width) != im.Width || uint32(cfg.Height) != im.Height {
			t.Errorf("%s: jpeg %dx%d vs section %dx%d", m[0], cfg.Width, cfg.Height, im.Width, im.Height)
		}
		if f.Camera() != dir {
			t.Errorf("%s: camera %q, want %q", m[0], f.Camera(), dir)
		}
		t.Logf("%s v%s rot=%d preview=%dx%d props=%v", filepath.Base(m[0]), f.Version, f.Rotation, cfg.Width, cfg.Height, f.Props)
	}
}

// The EXIF colour space is read for every body: the SD14 test file was shot
// in AdobeRGB, the DP1 Merrill in sRGB.
func TestExifColourSpace(t *testing.T) {
	for path, want := range map[string]string{
		"../testdata/SD14/20120818-SDIM4579.X3F": "AdobeRGB",
		"../testdata/DP1-Merrill/SDIM0555.X3F":   "sRGB",
	} {
		f, err := Open(path)
		if err != nil {
			t.Skip(err)
		}
		if got := f.Props["EXIF_COLORSPACE"]; got != want {
			t.Errorf("%s: EXIF_COLORSPACE %q, want %q", path, got, want)
		}
	}
}

// buildMonoHeaderX3F makes a minimal container with a chosen header version
// and length, so the 3.0 Monochrome mixer field can be tested without a
// camera file.
func buildMonoHeaderX3F(t *testing.T, major, minor uint16, headerSize int, mix *[3]float32) []byte {
	t.Helper()
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	hdr := make([]byte, headerSize)
	copy(hdr, "FOVb")
	binary.LittleEndian.PutUint16(hdr[4:], minor)
	binary.LittleEndian.PutUint16(hdr[6:], major)
	binary.LittleEndian.PutUint32(hdr[28:], 4)
	binary.LittleEndian.PutUint32(hdr[32:], 2)
	if mix != nil {
		for i, v := range mix {
			binary.LittleEndian.PutUint32(hdr[252+4*i:], math.Float32bits(v))
		}
	}
	b.Write(hdr)
	imgOff := uint32(headerSize)
	b.WriteString("SECi")
	w(uint32(0))
	w(uint32(2))
	w(uint32(18)) // JPEG
	w(uint32(4))
	w(uint32(2))
	w(uint32(0))
	b.Write([]byte{0xFF, 0xD8, 0xFF, 0xD9})
	imgLen := uint32(b.Len()) - imgOff
	dirOff := uint32(b.Len())
	b.WriteString("SECd")
	w(uint32(0))
	w(uint32(1))
	w(imgOff)
	w(imgLen)
	b.WriteString("IMA2")
	w(dirOff)
	return b.Bytes()
}

func TestMonoMixHeader(t *testing.T) {
	for _, tc := range []struct {
		name   string
		major  uint16
		minor  uint16
		size   int
		mix    *[3]float32
		stored bool
		want   [3]float64
	}{
		{"Merrill 3.0, stored equal", 3, 0, 424, &[3]float32{.33, .33, .34}, true, [3]float64{.33, .33, .34}},
		{"Merrill 3.0, stored zero", 3, 0, 424, &[3]float32{0, 0, 0}, true, [3]float64{0, 0, 0}},
		{"DP1M 2.3, no field", 2, 3, 264, nil, false, [3]float64{}},
		{"Quattro 4.1, no field", 4, 1, 768, nil, false, [3]float64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "h.X3F")
			if err := os.WriteFile(path, buildMonoHeaderX3F(t, tc.major, tc.minor, tc.size, tc.mix), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if f.MonoMixStored != tc.stored {
				t.Fatalf("MonoMixStored = %v, want %v", f.MonoMixStored, tc.stored)
			}
			for i := range tc.want {
				if math.Abs(f.MonoMix[i]-tc.want[i]) > 1e-6 {
					t.Errorf("MonoMix = %v, want %v", f.MonoMix, tc.want)
				}
			}
		})
	}
}
