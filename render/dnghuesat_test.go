package render

import (
	"math"
	"testing"
)

// The SD1 base's DNG HueSatMap: greys untouched, greens gain saturation,
// reds and blues stay put.
func TestHueSatMapGreens(t *testing.T) {
	dims, data := rawBases["SD1"].hueSatMap()
	if dims != [3]uint32{hsmHueDivs, hsmSatDivs, 1} || len(data) != hsmHueDivs*hsmSatDivs*3 {
		t.Fatalf("dims %v, %d values", dims, len(data))
	}
	cell := func(hueDeg float64, si int) (shift, sat, val float32) {
		hi := int(math.Round(hueDeg/360*hsmHueDivs)) % hsmHueDivs
		o := (hi*hsmSatDivs + si) * 3
		return data[o], data[o+1], data[o+2]
	}
	for hi := 0; hi < hsmHueDivs; hi++ {
		if s, sat, v := cell(float64(hi)*360/hsmHueDivs, 0); s != 0 || sat != 1 || v != 1 {
			t.Fatalf("grey cell at hue %d: %v %v %v", hi, s, sat, v)
		}
	}
	if _, sat, _ := cell(120, 15); sat < 1.1 { // ProPhoto green
		t.Errorf("green saturation scale %.3f, want > 1.1", sat)
	}
	for _, h := range []float64{0, 240} { // red, blue
		if s, sat, v := cell(h, 15); math.Abs(float64(s)) > 0.5 || math.Abs(float64(sat)-1) > 0.01 || math.Abs(float64(v)-1) > 0.01 {
			t.Errorf("hue %v: shift %.2f sat %.3f val %.3f, want ~identity", h, s, sat, v)
		}
	}
	if _, d := (&RawCorrection{}).hueSatMap(); d != nil {
		t.Error("a base without hue correction must write no map")
	}
}
