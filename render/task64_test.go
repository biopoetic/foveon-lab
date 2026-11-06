package render

import (
	"math"
	"testing"
)

// task64CurveDensity interpolates a Status M characteristic curve at log H.
func task64CurveDensity(curve [][2]float64, x float64) float64 {
	if len(curve) == 0 {
		return math.NaN()
	}
	if x <= curve[0][0] {
		return curve[0][1]
	}
	if x >= curve[len(curve)-1][0] {
		return curve[len(curve)-1][1]
	}
	for i := 1; i < len(curve); i++ {
		if x <= curve[i][0] {
			t := (x - curve[i-1][0]) / (curve[i][0] - curve[i-1][0])
			return curve[i-1][1] + t*(curve[i][1]-curve[i-1][1])
		}
	}
	return math.NaN()
}

// task64SpeedPoint is the ISO speed point on the green curve: the log H where
// the Status M density is D-min + 0.15 (ISO 5800 for a colour negative).
func task64SpeedPoint(c *colorNegData) float64 {
	curve := c.Char["G"]
	dmin := math.Inf(1)
	for _, p := range curve {
		if p[1] < dmin {
			dmin = p[1]
		}
	}
	y := dmin + 0.15
	for i := 1; i < len(curve); i++ {
		if (curve[i-1][1]-y)*(curve[i][1]-y) <= 0 && curve[i][1] != curve[i-1][1] {
			t := (y - curve[i-1][1]) / (curve[i][1] - curve[i-1][1])
			return curve[i-1][0] + t*(curve[i][0]-curve[i-1][0])
		}
	}
	return math.NaN()
}

// Task 64: the Fuji Pro 800Z and Pro 400H sheets drew their characteristic
// curves on a relative log H axis. The stored curves were re-registered on the
// published ISO speed, checked against the same-ISO Kodak sheet: the Green
// curve's speed point (D-min + 0.15) must fall where the control's does.
// Pro 160C was a different fault: its stored curve was compressed vertically
// and shifted in x, so it did not sit on the sheet ink. It was re-digitised;
// its Green speed point is now within 0.10 of the published ISO and its B
// D-max is back at the sheet's 2.7. Every negative's mid-grey must sit on the
// straight part of its curve: the Green density gap from mid-grey (10/ISO
// lux·s) to -3 stops is at least 0.35.
func TestColorNegativeProSheetAxis64(t *testing.T) {
	negs := colorNegs().negs
	control := map[string]string{
		"Fujifilm Fujicolor Pro 800Z": "Kodak Portra 800",
		"Fujifilm Fujicolor Pro 400H": "Kodak Portra 400",
	}
	for name, ctlName := range control {
		c := negs[name]
		if c == nil {
			t.Fatalf("%s missing", name)
		}
		ctl := negs[ctlName]
		if ctl == nil {
			t.Fatalf("no control %s for %s", ctlName, name)
		}
		got, want := task64SpeedPoint(c), task64SpeedPoint(ctl)
		t.Logf("%s: speed point %+.3f, %s %+.3f", name, got, ctlName, want)
		if math.Abs(got-want) > 0.02 {
			t.Errorf("%s: speed point %+.3f, %s %+.3f", name, got, ctlName, want)
		}
	}
	if c := negs["Fujifilm Fujicolor Pro 160C"]; c == nil {
		t.Fatal("Pro 160C missing")
	} else {
		got, want := task64SpeedPoint(c), math.Log10(0.8/c.ISO)
		dmax := 0.0
		for _, p := range c.Char["B"] {
			if p[1] > dmax {
				dmax = p[1]
			}
		}
		t.Logf("Pro 160C: speed point %+.3f, published ISO %+.3f, B D-max %.2f", got, want, dmax)
		if math.Abs(got-want) > 0.10 {
			t.Errorf("Pro 160C: speed point %+.3f, published ISO %+.3f", got, want)
		}
		if dmax < 2.6 {
			t.Errorf("Pro 160C: B D-max %.2f, want the sheet's ~2.7", dmax)
		}
	}
	for name, c := range negs {
		mg := math.Log10(10 / c.ISO)
		g := c.Char["G"]
		mid := task64CurveDensity(g, mg)
		low := task64CurveDensity(g, mg-3*math.Log10(2)) // -3 stops
		gap := mid - low
		t.Logf("%s: mid G %.3f, -3 stops G %.3f, gap %.3f, speed point %+.3f",
			name, mid, low, gap, task64SpeedPoint(c))
		if gap < 0.35 {
			t.Errorf("%s: mid-grey -> -3 stops Green gap %.3f < 0.35", name, gap)
		}
	}
}
