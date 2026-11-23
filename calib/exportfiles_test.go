package calib

import (
	"os"
	"path/filepath"
	"testing"
)

// The scoring tools must find an export whether it is still SPP's TIFF or
// has been compressed to PNG, and prefer the exports folder's copy.
func TestExportsAnywhereFindsTIFFAndPNG(t *testing.T) {
	dir := t.TempDir()
	touch := func(sub, name string) string {
		p := filepath.Join(dir, sub, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tif := touch(ExportsDir, "SDIM0031_A00.tif")
	png := touch(ExportsDir, "sigma_dp3_merrill_01_A00.png")
	touch("adobergb", "SDIM0031_A00.png") // another folder: the exports one wins
	other := touch("charts", "CHART_A00.png")
	touch(ExportsDir, "SDIM0031_A03.png") // another step: not listed
	got := ExportsAnywhere(dir, "A00")
	want := map[string]string{"SDIM0031": tif, "SIGMA_DP3_MERRILL_01": png, "CHART": other}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
}
