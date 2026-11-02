package main

import "testing"

// The X3F header stores the camera's clockwise rotation; the camera DNG
// carries the same information as the TIFF Orientation tag because its pixel
// data is the unrotated sensor development.
func TestDNGOrientation(t *testing.T) {
	for _, c := range []struct {
		deg  int
		want uint16
	}{{-90, 8}, {0, 1}, {90, 6}, {180, 3}, {270, 8}, {360, 1}, {450, 6}} {
		if got := dngOrientation(c.deg); got != c.want {
			t.Errorf("dngOrientation(%d) = %d, want %d", c.deg, got, c.want)
		}
	}
}
