package render

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// A neutral filter costs exactly its density in exposure: with NDMetered the
// image is darker by 2^-stops per channel, and with it off (the photographer
// opened up) not at all. No colour change either way.
func TestNeutralDensity(t *testing.T) {
	for _, id := range []string{"nd-bw-09", "nd-hoya-nd64", "nd-lee-bigstopper"} {
		f, ok := DensityFilter(id)
		if !ok {
			t.Fatalf("%s: not a density filter", id)
		}
		want := math.Exp2(-f.Stops)

		s := densitySetupFor(preset.Params{ND: id, NDMetered: true})
		for _, px := range [][2]int{{0, 0}, {50, 25}, {199, 99}} {
			if g := s.gain(px[0], px[1], 200, 100); math.Abs(float64(g)-want) > 1e-6 {
				t.Errorf("%s metered: gain %.6f, want %.6f", id, g, want)
			}
		}

		s = densitySetupFor(preset.Params{ND: id})
		for _, px := range [][2]int{{0, 0}, {50, 25}, {199, 99}} {
			if g := s.gain(px[0], px[1], 200, 100); g != 1 {
				t.Errorf("%s compensated: gain %.6f, want 1 (no pixel change)", id, g)
			}
		}
	}
}

// A graduated filter darkens one side of the frame by its stops and leaves
// the other side alone; the transition is monotonic between them.
func TestGraduatedDensity(t *testing.T) {
	f, ok := DensityFilter("gnd-lee-09-soft")
	if !ok {
		t.Fatal("no graduated entry")
	}
	want := math.Exp2(-f.Stops) // the dark side
	s := densitySetupFor(preset.Params{GND: "gnd-lee-09-soft", GNDSoft: 0.5})
	const w, h = 100, 100
	top := s.gain(w/2, 0, w, h)
	bottom := s.gain(w/2, h-1, w, h)
	if math.Abs(float64(top)-want) > 0.01 {
		t.Errorf("top (dark side) gain %.4f, want %.4f", top, want)
	}
	if math.Abs(float64(bottom)-1) > 0.01 {
		t.Errorf("bottom (clear side) gain %.4f, want 1", bottom)
	}
	prev := float32(1)
	for y := h - 1; y >= 0; y-- {
		g := s.gain(w/2, y, w, h)
		if g > prev+1e-6 {
			t.Fatalf("gain not monotonic at y=%d: %.4f after %.4f", y, g, prev)
		}
		prev = g
	}
	// Rotating by 90° puts the dark side on the right instead of the top.
	s = densitySetupFor(preset.Params{GND: "gnd-lee-09-soft", GNDAngle: 90, GNDSoft: 0.5})
	if g := s.gain(w-1, h/2, w, h); math.Abs(float64(g)-want) > 0.01 {
		t.Errorf("rotated: right gain %.4f, want %.4f", g, want)
	}
	if g := s.gain(0, h/2, w, h); math.Abs(float64(g)-1) > 0.01 {
		t.Errorf("rotated: left gain %.4f, want 1", g)
	}
}

// The density class is honest about what it is: every nd/gnd entry carries
// its stops, and nothing else claims a density.
func TestDensityCatalogue(t *testing.T) {
	nd, gnd := 0, 0
	for _, f := range BrandFilters() {
		switch f.Kind {
		case "nd":
			nd++
			if f.Stops <= 0 {
				t.Errorf("%s: neutral density without stops", f.ID)
			}
			if f.Glass != "" || f.Wratten != "" || f.Edge != 0 {
				t.Errorf("%s: a density filter must not claim a spectrum", f.ID)
			}
		case "gnd":
			gnd++
			if f.Stops <= 0 {
				t.Errorf("%s: graduated density without stops", f.ID)
			}
			if f.Hardness != "soft" && f.Hardness != "hard" {
				t.Errorf("%s: hardness %q", f.ID, f.Hardness)
			}
		default:
			if f.Stops != 0 {
				t.Errorf("%s: %s entry carries stops", f.ID, f.Kind)
			}
		}
	}
	if nd < 3 || gnd < 3 {
		t.Errorf("catalogue thin: %d neutral, %d graduated", nd, gnd)
	}
}
