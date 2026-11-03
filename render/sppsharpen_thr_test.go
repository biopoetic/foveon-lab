package render

// Task 51: the high-ISO unsharp-mask noise gate. It must be 0 at base ISO
// (bit-identical old mask), rise with ISO, keep a real one-pixel line, and
// stop doubling isolated noise.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func TestUSMThreshScalesWithISO(t *testing.T) {
	for _, c := range []struct {
		iso  float64
		want float32
	}{
		{100, 0}, {200, 0}, {400, 0}, {800, 8}, {1600, 16}, {3200, 24}, {6400, 32},
	} {
		if got := usmThresh(c.iso); got != c.want {
			t.Errorf("usmThresh(%g) = %g, want %g", c.iso, got, c.want)
		}
	}
}

func TestUSMThreshOverride(t *testing.T) {
	old := usmThrOverride
	defer func() { usmThrOverride = old }()
	usmThrOverride = 12
	if got := usmThresh(3200); got != 12 {
		t.Fatalf("override not used: %g", got)
	}
	usmThrOverride = 0
	if got := usmThresh(3200); got != 0 {
		t.Fatalf("override 0 not used: %g", got)
	}
	usmThrOverride = -1
}

// usmTestPlane builds one channel with a flat noisy field and a one-pixel
// bright line.
func usmTestPlane(w, h int) []float32 {
	p := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(100)
			if (x*7+y*13)%5 == 0 {
				v += 4
			} else if (x*11+y*3)%7 == 0 {
				v -= 4
			}
			for c := 0; c < 3; c++ {
				p[(y*w+x)*4+c] = v + float32(c)
			}
			p[(y*w+x)*4+3] = 255
		}
	}
	for x := 6; x < w-6; x++ {
		for c := 0; c < 3; c++ {
			p[(10*w+x)*4+c] = 170 + float32(c)
		}
	}
	return p
}

func TestGPUSppThrMirrorsTheCPU(t *testing.T) {
	l := &Linear{W: 16, H: 16, Pix: make([]float32, 16*16*3), Raw: true, Scale: 1, ISO: 3200, Sensor: "DPM"}
	for i := range l.Pix {
		l.Pix[i] = 0.1
	}
	b := NewBase(l)
	r := NewGPURecipe(b, preset.Neutral(), Compensation{})
	if r.SppThr != usmThresh(3200) {
		t.Fatalf("GPU threshold %g, CPU %g", r.SppThr, usmThresh(3200))
	}
	l.ISO = 100
	b = NewBase(l)
	r = NewGPURecipe(b, preset.Neutral(), Compensation{})
	if r.SppThr != 0 {
		t.Fatalf("ISO 100 GPU threshold %g, want 0", r.SppThr)
	}
}

func TestUSMThresholdKeepsLineAndQuietsNoise(t *testing.T) {
	const w, h = 48, 32
	in := usmTestPlane(w, h)
	k := gaussKernel(0.7)
	full := usmPlanes(in, w, h, 4, 1, k, 0)
	gate := usmPlanes(in, w, h, 4, 1, k, 12)

	at := func(p []float32, x, y int) float32 { return p[(y*w+x)*4] }
	// The line must still be sharpened, not erased.
	if got := at(full, 20, 10); got <= at(in, 20, 10) {
		t.Fatalf("line not sharpened: %g -> %g", at(in, 20, 10), got)
	}
	if got := at(gate, 20, 10); got < at(in, 20, 10)-1 {
		t.Fatalf("line lost by the gate: %g -> %g", at(in, 20, 10), got)
	}
	// A flat noise pixel (away from the line) must move less with the gate.
	var dFull, dGate float64
	n := 0
	for y := 2; y < h-2; y++ {
		for x := 2; x < w-2; x++ {
			if y >= 8 && y <= 12 {
				continue
			}
			dFull += math.Abs(float64(at(full, x, y) - at(in, x, y)))
			dGate += math.Abs(float64(at(gate, x, y) - at(in, x, y)))
			n++
		}
	}
	if dGate > 0.25*dFull {
		t.Fatalf("gate did not quiet noise: full %.1f gate %.1f", dFull, dGate)
	}
}
