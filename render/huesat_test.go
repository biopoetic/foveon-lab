package render

import (
	"math"
	"testing"
)

// The SD1 base's green correction: greens gain chroma, greys and other hues
// stay put.
func TestHueSatGreensOnly(t *testing.T) {
	c := rawBases["SD1"]
	if c == nil || len(c.HueGain) != hueBins {
		t.Fatal("SD1 base has no hue correction")
	}
	chroma := func(r, g, b float32) float64 {
		var f [3]float64
		for i := 0; i < 3; i++ {
			v := (rgbToXYZ[i*3]*float64(r) + rgbToXYZ[i*3+1]*float64(g) + rgbToXYZ[i*3+2]*float64(b)) / whiteXYZ[i]
			f[i] = math.Cbrt(v)
		}
		return math.Hypot(500*(f[0]-f[1]), 200*(f[1]-f[2]))
	}
	for _, p := range []struct {
		name    string
		r, g, b float32
		more    bool
	}{{"grey", 0.2, 0.2, 0.2, false}, {"red", 0.4, 0.05, 0.05, false}, {"orange", 0.5, 0.2, 0.03, false},
		{"moss green", 0.04, 0.09, 0.03, true}, {"leaf green", 0.08, 0.25, 0.05, true}} {
		r, g, b := c.hueSat(p.r, p.g, p.b)
		before, after := chroma(p.r, p.g, p.b), chroma(r, g, b)
		if p.more && after < before*1.1 {
			t.Errorf("%s: chroma %.2f -> %.2f, want more", p.name, before, after)
		}
		if !p.more && math.Abs(after-before) > 1e-3*max(1, before) {
			t.Errorf("%s: chroma %.2f -> %.2f, want unchanged", p.name, before, after)
		}
	}
}

func BenchmarkHueSatApply(b *testing.B) {
	c := rawBases["SD1"]
	l := &Linear{W: 4704, H: 3136, Pix: make([]float32, 4704*3136*3)}
	for i := range l.Pix {
		l.Pix[i] = float32(i%997) / 997 * 0.5
	}
	for i := 0; i < b.N; i++ {
		c.Apply(l)
	}
}

// The a* < 0 shortcut is on for the SD1 table and gives the same result as
// converting every pixel.
func TestHueSatShortcut(t *testing.T) {
	c := *rawBases["SD1"]
	if !c.hueANeg {
		t.Fatal("SD1 hue table should allow the a* < 0 shortcut")
	}
	slow := c
	slow.hueANeg = false
	for i := 0; i < 20000; i++ {
		r, g, b := float32(i%37)/40, float32(i/37%29)/30, float32(i/1073%17)/18
		r1, g1, b1 := c.hueSat(r, g, b)
		r2, g2, b2 := slow.hueSat(r, g, b)
		if math.Abs(float64(r1-r2)) > 1e-6 || math.Abs(float64(g1-g2)) > 1e-6 || math.Abs(float64(b1-b2)) > 1e-6 {
			t.Fatalf("(%v %v %v): shortcut %v %v %v, full %v %v %v", r, g, b, r1, g1, b1, r2, g2, b2)
		}
	}
	all := RawCorrection{HueGain: []float64{1.2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, HueRot: make([]float64, 12)}
	if all.hueChangesOnlyANeg() {
		t.Error("a red correction must not take the shortcut")
	}
}
