package main

import "testing"

// TestCameraDNGDefaultScale: the camera DNG declares the same stretch the
// app's frame applies when the raw readout is not the preview's shape.
func TestCameraDNGDefaultScale(t *testing.T) {
	cases := []struct {
		name               string
		previewW, previewH int
		w, h               int
		want               [2]float64
	}{
		{"normal", 4704, 3136, 4704, 3136, [2]float64{1, 1}},
		{"no preview", 0, 0, 4704, 3136, [2]float64{1, 1}},
		{"SD1 medium", 4704, 3136, 4704, 1568, [2]float64{1, 2}},
		{"SD14 3:1 outlier", 1776, 1184, 2688, 896, [2]float64{1, 2}},
		{"near enough", 3000, 2000, 3000, 1990, [2]float64{1, 1}},
	}
	for _, c := range cases {
		ph := &Photo{previewW: c.previewW, previewH: c.previewH}
		if got := cameraDNGDefaultScale(ph, c.w, c.h); got != c.want {
			t.Errorf("%s: DefaultScale %v, want %v", c.name, got, c.want)
		}
	}
}
