package main

import (
	"math"
	"testing"
)

// The DP2 camera-JPEG balance: a per-channel gain that puts the raw render's
// mean colour on the camera's own JPEG, preserving luminance, with guards
// against frames whose mean is not a colour measurement.
func TestJPEGBalanceGains(t *testing.T) {
	mo := [3]float64{0.146, 0.126, 0.130}
	mj := [3]float64{0.092, 0.137, 0.134}
	g, ok := jpegBalanceGains(mo, mj, 0.13, 0, 0)
	if !ok {
		t.Fatal("a normal frame was refused")
	}
	want := [3]float64{mj[0] / mo[0], mj[1] / mo[1], mj[2] / mo[2]}
	lum := want[0]*0.2126 + want[1]*0.7152 + want[2]*0.0722
	for k := range g {
		if math.Abs(float64(g[k])-want[k]/lum) > 1e-5 {
			t.Errorf("gain %d = %.5f, want %.5f", k, g[k], want[k]/lum)
		}
	}
	if l := float64(g[0])*0.2126 + float64(g[1])*0.7152 + float64(g[2])*0.0722; math.Abs(l-1) > 1e-5 {
		t.Errorf("gain luminance %.6f, want 1", l)
	}

	// Neutral means leave the image alone.
	g, ok = jpegBalanceGains([3]float64{0.2, 0.2, 0.2}, [3]float64{0.2, 0.2, 0.2}, 0.2, 0, 0)
	if !ok {
		t.Fatal("neutral frame refused")
	}
	for k := range g {
		if math.Abs(float64(g[k])-1) > 1e-5 {
			t.Errorf("neutral gain %d = %.5f, want 1", k, g[k])
		}
	}

	for _, tc := range []struct {
		name          string
		meanY, co, cj float64
	}{
		{"too dark", 0.005, 0, 0},
		{"raw clipped", 0.2, 0.06, 0},
		{"jpeg clipped", 0.2, 0, 0.06},
	} {
		if _, ok := jpegBalanceGains(mo, mj, tc.meanY, tc.co, tc.cj); ok {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	if _, ok := jpegBalanceGains([3]float64{0, 0, 0}, mj, 0.2, 0, 0); ok {
		t.Error("zero raw mean accepted")
	}
}
