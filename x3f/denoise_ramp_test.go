package x3f

import "testing"

// The noise-aware restore must not reach a low-ISO frame: at ISO 200/400 its
// ramp is zero, so the restore runs exactly as before the change. The ISO
// gate is what keeps a dark or synthetic low-ISO frame (whose measured
// relative noise can be high) on the old path.
func TestRestoreRampLowISO(t *testing.T) {
	for _, iso := range []float64{0, 50, 100, 200, 400} {
		for _, rel := range []float64{0, 0.02, 0.05, 0.10} {
			if got := restoreRamp(rel, iso); got != 0 {
				t.Errorf("restoreRamp(%g, ISO %g) = %g, want 0", rel, iso, got)
			}
		}
	}
}

func TestRestoreRampHighISO(t *testing.T) {
	// A noisy ISO 800+ frame reaches the new path.
	if got := restoreRamp(0.067, 1600); got != 1 {
		t.Errorf("restoreRamp(0.067, ISO 1600) = %g, want 1", got)
	}
	if got := restoreRamp(0.044, 800); got < 0.5 {
		t.Errorf("restoreRamp(0.044, ISO 800) = %g, want at least 0.5", got)
	}
	// A high ISO frame with the noise of a clean one stays on the old path.
	if got := restoreRamp(0.010, 1600); got != 0 {
		t.Errorf("restoreRamp(0.010, ISO 1600) = %g, want 0", got)
	}
}
