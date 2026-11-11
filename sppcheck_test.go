package main

import (
	"encoding/json"
	"math"
	"strings"
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

// The source rule: spp-check must score the image the app shows. With
// accurate mode on (the default) a photo with a fitted raw base is shown
// from raw; a readout the raw path refuses (LOW) is shown from the camera
// JPEG; SD9/SD10 have only their raw data, so they are always raw.
func TestSPPCheckSource(t *testing.T) {
	oldRaw := goRaw
	goRaw = true
	t.Cleanup(func() { goRaw = oldRaw })
	mk := func(model, res string) *Photo {
		return &Photo{Kind: "x3f", Camera: "SD1", props: map[string]string{
			"CAMMODEL": model, "RESOLUTION": res, "WB_DESC": "Auto",
		}}
	}
	for _, tc := range []struct {
		name string
		ph   *Photo
		want string
	}{
		{"DPM HI has a raw base", mk("SIGMA DP2 Merrill", "HI"), "raw"},
		{"DPM LOW is refused by rawDecodable", mk("SIGMA DP2 Merrill", "LOW"), "jpg"},
		{"DP HI has a raw base", mk("SIGMA DP2", "HI"), "raw"},
		{"unknown resolution is refused", mk("SIGMA DP2 Merrill", "SMALL"), "jpg"},
		{"SD10 has no full preview, so its raw data is the picture", func() *Photo {
			p := mk("SIGMA SD10", "LOW")
			p.RawDecoded = true
			return p
		}(), "raw"},
	} {
		if got := sppCheckSource(tc.ph); got != tc.want {
			t.Errorf("%s: source %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Old saved JSON files have no source field; they must still load. New files
// carry it. This is the compatibility the queue asked for.
func TestSPPScoreSourceJSON(t *testing.T) {
	const oldJSON = `{"X":{"group":"DPM","model":"SIGMA DP2 Merrill","all":58.62,"area":58.62,"shadow":9.1,"ev":-1.4,"areaEV0":9.68}}`
	var old map[string]sppScore
	if err := json.Unmarshal([]byte(oldJSON), &old); err != nil {
		t.Fatalf("old JSON without source: %v", err)
	}
	if old["X"].Source != "" {
		t.Fatalf("old JSON source %q, want empty", old["X"].Source)
	}

	now := map[string]sppScore{"X": {Group: "DPM", All: 5.0, Source: "jpg"}}
	b, err := json.Marshal(now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"source":"jpg"`) {
		t.Fatalf("new JSON has no source: %s", b)
	}
	var back map[string]sppScore
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("new JSON: %v", err)
	}
	if back["X"].Source != "jpg" {
		t.Fatalf("new JSON source %q, want jpg", back["X"].Source)
	}
}
