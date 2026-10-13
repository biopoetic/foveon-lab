package x3f

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRecoverTruncated cuts the end (directory and part of the last
// section) off a sample and checks the sections are found by scanning: the
// preview, CAMF and the raw data read back exactly as from the intact file.
func TestRecoverTruncated(t *testing.T) {
	src := filepath.Join("..", "testdata", "DP1-Merrill", "SDIM0555.X3F")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Skip("no sample:", err)
	}
	cut := filepath.Join(t.TempDir(), "cut.X3F")
	if err := os.WriteFile(cut, data[:len(data)-9000], 0o644); err != nil {
		t.Fatal(err)
	}
	good, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Open(cut)
	if err != nil {
		t.Fatalf("truncated file not recovered: %v", err)
	}
	if f.Recovered == "" || good.Recovered != "" {
		t.Errorf("Recovered = %q (intact: %q)", f.Recovered, good.Recovered)
	}
	if f.Props["CAMMODEL"] != good.Props["CAMMODEL"] {
		t.Errorf("properties lost: %q", f.Props["CAMMODEL"])
	}
	if _, _, err := f.PreviewJPEG(); err != nil {
		t.Errorf("preview: %v", err)
	}
	if _, err := f.CAMF(); err != nil {
		t.Errorf("CAMF: %v", err)
	}
	a, err1 := good.DecodeRaw()
	b, err2 := f.DecodeRaw()
	if err1 != nil || err2 != nil {
		t.Fatalf("raw: %v / %v", err1, err2)
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			t.Fatalf("raw differs at %d", i)
		}
	}
}
