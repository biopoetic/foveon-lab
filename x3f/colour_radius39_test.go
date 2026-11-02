package x3f

import "testing"

// Task 39: the high-ISO colour radius is a single value (24 px at full
// resolution); the base-ISO radii stay exactly as they were, which is what
// keeps ISO 400 and below bit-identical.
func TestColourRadius(t *testing.T) {
	cases := []struct {
		iso  float64
		want float64
	}{
		{0, 32},
		{50, 8},
		{100, 8},
		{200, 8},
		{400, 16},
		{800, 24},
		{1600, 24},
		{3200, 24},
		{6400, 24},
	}
	for _, c := range cases {
		if got := colourRadius(c.iso); got != c.want {
			t.Errorf("colourRadius(%g) = %g, want %g", c.iso, got, c.want)
		}
	}
}
