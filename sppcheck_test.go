package main

import (
	"math"
	"testing"
)

// The summary must show where a typical photo stands, not only the mean:
// one bad photo moves a group's mean far more than its median.
func TestSPPGroupStatsMedianAndTarget(t *testing.T) {
	scores := map[string]sppScore{
		"A": {Group: "DPM", All: 3, Area: 2.0},
		"B": {Group: "DPM", All: 3, Area: 2.5},
		"C": {Group: "DPM", All: 4, Area: 3.5},
		"D": {Group: "DPM", All: 60, Area: 58, EV: -1.4}, // one outlier
		"E": {Group: "SD9", All: 9, Area: 8},
	}
	by := map[string]sppGroupStat{}
	for _, s := range sppGroupStats(scores) {
		by[s.Group] = s
	}
	d := by["DPM"]
	if d.N != 4 || d.UnderTarget != 2 || d.OffExposure != 1 {
		t.Fatalf("DPM: %+v", d)
	}
	if math.Abs(d.Area-16.5) > 1e-9 || math.Abs(d.AreaMedian-3.0) > 1e-9 {
		t.Fatalf("DPM mean %.3f (want 16.5), median %.3f (want 3.0: between 2.5 and 3.5)", d.Area, d.AreaMedian)
	}
	if d.OffExposureAll != 60 {
		t.Fatalf("DPM off-exposure mean %.2f, want 60", d.OffExposureAll)
	}
	if s := by["SD9"]; s.N != 1 || s.AreaMedian != 8 || s.UnderTarget != 0 {
		t.Fatalf("SD9: %+v", s)
	}
	if a := by["ALL"]; a.N != 5 || a.AreaMedian != 3.5 || a.UnderTarget != 2 {
		t.Fatalf("ALL: %+v", a)
	}
}
