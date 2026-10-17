package render

// Density filters: neutral density (ND) and graduated neutral density (GND).
//
// These are the one filter class a still-image renderer can represent exactly
// without any spectral data, because their specification IS the effect: a
// density of d attenuates every wavelength by 10^-d — that is, by d/0.3 stops
// — uniformly across the frame (ND) or across a gradient (GND). Nothing here
// is estimated; the only modelling choice is the shape of a graduated
// filter's transition, which manufacturers describe only as "soft" or "hard"
// and do not publish as a curve, so its width and position are the user's
// controls (GNDSoft, GNDPos).

import (
	"math"

	"github.com/biopoetic/foveon-lab/preset"
)

// densityGain returns the multi-pixel setup for a render: the ND gain (a
// constant) and whether a graduated filter is active.
type densitySetup struct {
	nd   float32 // per-pixel gain from a neutral filter (1 = none)
	gnd  bool
	stops float64
	cos, sin float64
	centre, soft float64
}

// densitySetupFor reads the preset's density filters once per render.
func densitySetupFor(p preset.Params) densitySetup {
	var s densitySetup
	s.nd = 1
	if f, ok := DensityFilter(p.ND); ok && f.Kind == "nd" {
		// A neutral filter does not change colour: it costs exposure. When
		// the photographer has opened up by the filter factor the image is
		// unchanged; NDMetered shows the same settings with the filter on.
		if p.NDMetered {
			s.nd = float32(math.Exp2(-f.Stops))
		}
	}
	if f, ok := DensityFilter(p.GND); ok && f.Kind == "gnd" {
		s.gnd = true
		s.stops = f.Stops
		a := p.GNDAngle * math.Pi / 180
		s.cos, s.sin = math.Cos(a), math.Sin(a)
		s.centre = p.GNDPos // -0.5 (bottom) .. 0 (centre, default) .. +0.5 (top)
		s.soft = 0.02 + 0.73*math.Max(0, math.Min(1, p.GNDSoft))
	}
	return s
}

// gain at a pixel: the neutral filter's gain times the graduated one's.
func (s densitySetup) gain(x, y, w, h int) float32 {
	if !s.gnd {
		return s.nd
	}
	// u runs along the gradient axis: +0.5 at the top (angle 0), -0.5 at
	// the bottom, rotating with the angle.
	u := (0.5-(float64(y)+0.5)/float64(h))*s.cos + ((float64(x)+0.5)/float64(w)-0.5)*s.sin
	t := (u-s.centre)/s.soft + 0.5
	t = math.Max(0, math.Min(1, t))
	t = t * t * (3 - 2*t) // smoothstep: the transition's shape
	return s.nd * float32(math.Exp2(-s.stops*t))
}

// NDStops reports a neutral filter's light loss in stops, for the UI's
// exposure read-out.
func NDStops(id string) (float64, bool) {
	f, ok := DensityFilter(id)
	if !ok || f.Kind != "nd" {
		return 0, false
	}
	return f.Stops, true
}
