package calib

import (
	"math"
	"testing"
)

// Pairs from Sharma, Wu & Dalal, "The CIEDE2000 Color-Difference Formula:
// Implementation Notes, Supplementary Test Data, and Mathematical
// Observations" (2005), Table 1.
func TestDeltaE2000Sharma(t *testing.T) {
	for _, c := range []struct{ l1, a1, b1, l2, a2, b2, want float64 }{
		{50, 2.6772, -79.7751, 50, 0, -82.7485, 2.0425},
		{50, 0, 0, 50, -1, 2, 2.3669},
		{50, 2.5, 0, 73, 25, -18, 27.1492},
		{60.2574, -34.0099, 36.2677, 60.4626, -34.1751, 39.4387, 1.2644},
		{2.0776, 0.0795, -1.1350, 0.9033, -0.0636, -0.5514, 0.9082},
		{50, -0.001, 2.49, 50, 0.0009, -2.49, 4.8045},
		{50, 2.5, 0, 50, 0, -2.5, 4.3065},
	} {
		got := DeltaE2000(c.l1, c.a1, c.b1, c.l2, c.a2, c.b2)
		if math.Abs(got-c.want) > 1e-4 {
			t.Errorf("dE00(%v,%v,%v / %v,%v,%v) = %.4f, want %.4f", c.l1, c.a1, c.b1, c.l2, c.a2, c.b2, got, c.want)
		}
	}
}
