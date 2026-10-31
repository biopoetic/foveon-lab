package main

import "testing"

// TestExportLookName checks the look part of an export file name: a preset
// keeps its own name, the unnamed starting points get plain words, and the
// app's "tuned"/"nr" notes never reach the file.
func TestExportLookName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "Neutral"},
		{"original", "Neutral"},
		{"tuned", "Edited"},
		{"edited", "Edited"},
		{"Neutral", "Neutral"},
		{"Velvia 100", "Velvia 100"},
		{"Velvia 100 (tuned)", "Velvia 100"},
		{"Velvia 100 (tuned) (tuned)", "Velvia 100"},
	} {
		if got := exportLookName(c.in); got != c.want {
			t.Errorf("exportLookName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExportFileStem checks the whole name: photo plus look, and the colour
// space only when it is not sRGB.
func TestExportFileStem(t *testing.T) {
	for _, c := range []struct {
		photo, label, space, format, want string
	}{
		{"DP1MFAR2I100.X3F", "tuned", "srgb", "jpeg", "DP1MFAR2I100_Edited"},
		{"DP1MFAR2I100.X3F", "", "srgb", "jpeg", "DP1MFAR2I100_Neutral"},
		{"DP1MFAR2I100.X3F", "Neutral", "prophoto", "jpeg", "DP1MFAR2I100_Neutral_prophoto"},
		{"DP1MFAR2I100.X3F", "Velvia 100", "adobe", "dng", "DP1MFAR2I100_Velvia_100"},
	} {
		if got := exportFileStem(c.photo, c.label, c.space, c.format); got != c.want {
			t.Errorf("exportFileStem(%q, %q, %q, %q) = %q, want %q",
				c.photo, c.label, c.space, c.format, got, c.want)
		}
	}
}
