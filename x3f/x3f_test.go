package x3f

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
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
