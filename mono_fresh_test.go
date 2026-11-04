package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestMonoFreshOpenFromRealHeaders pins the three owner observations behind
// task 55's fresh-open rule: a DP1 Merrill whose 2.3 header has no field
// opens equal; _P3M9383's 3.0 header stores .33/.33/.34; and 06's 3.0
// header stores zeros, which SPP opens as pure B. The samples are the
// owner's own files, never committed; skip when they are absent.
func TestMonoFreshOpenFromRealHeaders(t *testing.T) {
	root := os.Getenv("FOVEON_SAMPLES")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, "Desktop", "foveon-samples")
	}
	for _, tc := range []struct {
		name string
		rel  string
		want [3]float64
	}{
		{"DP1M 400, no header field", `DP1-Merrill\DP1MhSLI0400.X3F`, [3]float64{1.0 / 3, 1.0 / 3, 1.0 / 3}},
		{"_P3M9383, stored equal", `DP3-Merrill\_P3M9383.X3F`, [3]float64{.33, .33, .34}},
		{"06, stored zeros", `DP3-Merrill\sigma_dp3_merrill_06.x3f`, [3]float64{0, 0, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(root, tc.rel)
			if _, err := os.Stat(p); err != nil {
				t.Skipf("sample not present: %v", err)
			}
			ph := &Photo{ID: "t55", Name: filepath.Base(p), Kind: "x3f", path: p, Info: map[string]string{}}
			if !loadMeta(ph) {
				t.Fatalf("loadMeta failed for %s", p)
			}
			for i := range tc.want {
				if math.Abs(ph.MonoFresh[i]-tc.want[i]) > 1e-6 {
					t.Errorf("MonoFresh = %v, want %v", ph.MonoFresh, tc.want)
				}
			}
		})
	}
}
