package render

import (
	"math"
	"testing"
)

// The AdobeRGB branch keeps neutrals neutral and only pulls red (and a
// little blue) against green.
func TestSPPAdobeRGBNeutral(t *testing.T) {
	for r := 0; r < 3; r++ {
		s := sppAdobeRGB[r*3] + sppAdobeRGB[r*3+1] + sppAdobeRGB[r*3+2]
		if math.Abs(float64(s)-1) > 1e-3 {
			t.Errorf("row %d sums to %.4f, want 1", r, s)
		}
	}
	l := &Linear{W: 2, H: 1, Pix: []float32{0.18, 0.18, 0.18, 0.05, 0.3, 0.4}}
	ApplySPPAdobeRGB(l)
	for k := 0; k < 3; k++ {
		if math.Abs(float64(l.Pix[k])-0.18) > 1e-4 {
			t.Errorf("grey moved: %v", l.Pix[:3])
		}
	}
	if !(l.Pix[3] < 0.05) || l.Pix[4] != 0.3 {
		t.Errorf("a cyan should lose red and keep green: %v", l.Pix[3:])
	}
}
