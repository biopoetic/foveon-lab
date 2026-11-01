package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/biopoetic/foveon-lab/render"
)

// dp2Sample finds a real DP2 file in the local sample library. The tests
// below need one because the balance only exists for that body; without the
// library they skip, like the other sample-based tests.
func dp2Sample(t *testing.T, name string) *Photo {
	t.Helper()
	home, _ := os.UserHomeDir()
	dirs := []string{
		filepath.Join(home, "Desktop", "foveon-samples", "archive"),
		filepath.Join(home, "Desktop", "foveon-samples"),
		"testdata",
	}
	if d := os.Getenv("FL_DP2_DIR"); d != "" {
		dirs = append([]string{d}, dirs...)
	}
	for _, dir := range dirs {
		for _, ph := range scanPhotos([]string{dir}, false) {
			if ph.Name == name {
				if modelOf(ph) != "SIGMA DP2" {
					t.Fatalf("%s: model %q, want SIGMA DP2", name, modelOf(ph))
				}
				return ph
			}
		}
	}
	t.Skipf("no DP2 sample %s", name)
	return nil
}

// The balance is one fixed gain per photo: whatever image a path hands over
// (a crop, a tile, a downsized view) gets the same three numbers, so two
// views of one photo cannot disagree in colour.
func TestJPEGBalanceOneFixedGain(t *testing.T) {
	ph := dp2Sample(t, "kron_DPReditmypic_SDIM0140.X3F")
	g, ok := jpegBalanceFor(ph)
	if !ok {
		t.Fatal("a normal DP2 frame was refused")
	}
	measured := balanceMeasures.Load()
	for _, tc := range []struct {
		name string
		w, h int
		v    float32
	}{
		{"dark", 64, 64, 0.03},
		{"mid", 200, 120, 0.25},
		{"bright", 32, 32, 0.60},
	} {
		l := &render.Linear{W: tc.w, H: tc.h, Pix: make([]float32, tc.w*tc.h*3)}
		for i := range l.Pix {
			l.Pix[i] = tc.v
		}
		rawJPEGBalance(ph, l)
		for i, v := range l.Pix {
			want := tc.v * g[i%3]
			if math.Abs(float64(v-want)) > 1e-6*math.Max(1, math.Abs(float64(want))) {
				t.Fatalf("%s: gain %d applied %.6f, want %.6f", tc.name, i%3, v, want)
			}
		}
	}
	if got := balanceMeasures.Load(); got != measured {
		t.Errorf("the image content was measured again: %d -> %d", measured, got)
	}

	// A refused frame (too dark) is refused everywhere.
	refused := dp2Sample(t, "kron_DP2CardX3Fs_SDIM0686.X3F")
	if _, ok := jpegBalanceFor(refused); ok {
		t.Fatal("SDIM0686 is no longer a refused frame")
	}
	l := &render.Linear{W: 80, H: 50, Pix: make([]float32, 80*50*3)}
	for i := range l.Pix {
		l.Pix[i] = 0.2
	}
	rawJPEGBalance(refused, l)
	for i, v := range l.Pix {
		if v != 0.2 {
			t.Fatalf("refused frame changed pixel %d: %.6f", i, v)
		}
	}
}

// Max Detail mixes two corrected copies of the same development: base
// through rawCorrect and the rebuilt highlights through rawCorrectApply. If
// either stage measured the image it was given, the two copies would get
// different gains and the mix would leave a colour edge. With one cached
// balance the stages must agree bit for bit.
func TestMaxDetailCorrectionStagesAgree(t *testing.T) {
	ph := dp2Sample(t, "kron_DPReditmypic_SDIM0140.X3F")
	if _, ok := jpegBalanceFor(ph); !ok {
		t.Fatal("balance refused on this sample")
	}
	// H < 64 keeps the bright-chroma denoise (spatial, and only in
	// rawCorrect) out of the comparison: what is left is the per-body
	// correction and the balance, which both stages must apply alike.
	const w, h = 48, 32
	src := &render.Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	for i := 0; i < w*h; i++ {
		y := float32((i/w)*255/(h-1)) / 255
		src.Pix[i*3], src.Pix[i*3+1], src.Pix[i*3+2] = y, y*0.9, y*0.8
	}
	base := &render.Linear{W: w, H: h, Pix: append([]float32(nil), src.Pix...)}
	rebuilt := &render.Linear{W: w, H: h, Pix: append([]float32(nil), src.Pix...)}
	rawCorrect(ph, base)
	rawCorrectApply(ph, rebuilt)
	for i := range base.Pix {
		if base.Pix[i] != rebuilt.Pix[i] {
			t.Fatalf("the two Max Detail correction stages disagree at %d: %.7f vs %.7f", i, base.Pix[i], rebuilt.Pix[i])
		}
	}
}

// A preset white balance is the user's balance, not the camera's as-shot one:
// developing with it must not measure or apply the camera-JPEG balance. The
// as-shot path still does.
func TestExplicitWBLeavesBalanceAlone(t *testing.T) {
	ph := dp2Sample(t, "kronometric_SDIM0790.X3F")
	before := balanceMeasures.Load()
	if _, err := rawLinearFor(ph, 800, "sRGB", "wb:Shade"); err != nil {
		t.Fatal(err)
	}
	if got := balanceMeasures.Load(); got != before {
		t.Fatalf("a user WB development measured the as-shot balance (%d -> %d)", before, got)
	}
	if ph.balanceOK {
		t.Fatal("a user WB development cached the as-shot balance")
	}
	if _, err := rawLinear(ph, 800, "sRGB"); err != nil {
		t.Fatal(err)
	}
	if got := balanceMeasures.Load(); got != before+1 {
		t.Fatalf("the as-shot development did not measure once (%d -> %d)", before, got)
	}
	if !ph.balanceOK {
		t.Fatal("the as-shot balance was not adopted")
	}
}

// A Max Detail render on a DP2 measures the balance once, no matter how many
// of its highlight stages run, and a second render is identical: the seam
// cannot move between calls or between stages.
func TestMaxDetailRenderOneBalanceMeasurement(t *testing.T) {
	ph := dp2Sample(t, "kron_DPReditmypic_SDIM0140.X3F")
	before := balanceMeasures.Load()
	l1, err := rawMaxDetail(ph, 1200, defaultNR)
	if err != nil {
		t.Fatal(err)
	}
	if !ph.balanceOK {
		t.Fatal("a normal DP2 frame was refused")
	}
	if got := balanceMeasures.Load(); got != before+1 {
		t.Fatalf("Max Detail measured the balance %d times, want 1", got-before)
	}
	l2, err := rawMaxDetail(ph, 1200, defaultNR)
	if err != nil {
		t.Fatal(err)
	}
	if got := balanceMeasures.Load(); got != before+1 {
		t.Fatalf("a second render measured again: %d -> %d", before, got)
	}
	if l1.W != l2.W || l1.H != l2.H || len(l1.Pix) != len(l2.Pix) {
		t.Fatal("the two renders have different geometry")
	}
	for i := range l1.Pix {
		if l1.Pix[i] != l2.Pix[i] {
			t.Fatalf("a second Max Detail render differs at %d: %.7f vs %.7f", i, l1.Pix[i], l2.Pix[i])
		}
	}
}
