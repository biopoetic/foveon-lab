package main

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/biopoetic/foveon-lab/x3f"
)

// TestGoLinear develops a Merrill sample natively at preview size with and
// without the denoiser (skipped without ../testdata samples).
func TestGoLinear(t *testing.T) {
	p := filepath.Join("testdata", "DP1-Merrill", "SDIM0555.X3F")
	f, err := x3f.Open(p)
	if err != nil {
		t.Skip("no sample:", err)
	}
	ph := &Photo{Name: "SDIM0555.X3F", Kind: "x3f", path: p, props: f.Props, Camera: f.Camera()}
	plain, err := goLinear(ph, 1200, 0)
	if err != nil {
		t.Fatal(err)
	}
	dn, err := goLinear(ph, 1200, 1)
	if err != nil {
		t.Fatal(err)
	}
	// The two paths must agree so a view can swap between them without
	// shifting; the width is the binned one, at most the 1200 asked for
	// (binning rounds up to 4, giving 1176, rather than down to 1568 which
	// would cost 1.8x the pixels to denoise).
	if plain.W > 1200 || dn.W != plain.W || dn.H != plain.H {
		t.Fatalf("sizes %dx%d / %dx%d", plain.W, plain.H, dn.W, dn.H)
	}
	var mean [2]float64
	var rough [2]float64 // mean abs horizontal difference: noise + detail
	for i, l := range []*struct{ p []float32 }{{plain.Pix}, {dn.Pix}} {
		for j := 3; j < len(l.p); j++ {
			v := float64(l.p[j])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("non-finite value at %d", j)
			}
			mean[i] += v
			rough[i] += math.Abs(v - float64(l.p[j-3]))
		}
	}
	if d := math.Abs(mean[1]/mean[0] - 1); d > 0.02 {
		t.Errorf("denoising changed the mean level by %.1f%%", d*100)
	}
	if rough[1] >= rough[0] {
		t.Errorf("denoised image is not smoother: %.4g vs %.4g", rough[1], rough[0])
	}
	t.Logf("%dx%d, roughness %.3g -> %.3g", dn.W, dn.H, rough[0], rough[1])
}
