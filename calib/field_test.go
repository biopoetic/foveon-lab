package calib

// Tests for the colour-field measurement: a known shared field must be found
// (held out), independent noise must not be, and a field built in sensor
// coordinates must read higher in sensor coordinates than in display ones.

import (
	"math"
	"math/rand"
	"testing"
)

func fieldFixture(seed int64, n int, field func(i, j, rot int, c int) float64, noise float64) []*FieldSample {
	rng := rand.New(rand.NewSource(seed))
	out := make([]*FieldSample, n)
	for p := 0; p < n; p++ {
		r := 0
		if p%2 == 1 {
			r = 90
		}
		s := &FieldSample{Rotation: r, Name: "p"}
		for c := 0; c < 3; c++ {
			s.G[c] = make([]float64, FieldGW*FieldGH)
			s.W[c] = make([]float64, FieldGW*FieldGH)
			for j := 0; j < FieldGH; j++ {
				for i := 0; i < FieldGW; i++ {
					k := j*FieldGW + i
					s.G[c][k] = field(i, j, r, c) + noise*rng.NormFloat64()
					s.W[c][k] = 1
				}
			}
		}
		s.Surface()
		out[p] = s
	}
	return out
}

func TestFieldSharedDetected(t *testing.T) {
	ss := fieldFixture(1, 12, func(i, j, rot, c int) float64 {
		u, _ := fieldUV(i, j)
		return 0.04 * u * float64(c-1)
	}, 0.01)
	ids := make([]int, len(ss))
	for i := range ids {
		ids[i] = i
	}
	for c := 0; c < 3; c++ {
		r2, _ := sharedShapeR2(ss, ids, c, false, 1)
		if c == 1 { // this channel's synthetic field is zero
			if r2 > 0.2 {
				t.Fatalf("no-field channel %d: R2 %.2f, want < 0.2", c, r2)
			}
			continue
		}
		if r2 < 0.7 {
			t.Fatalf("shared field channel %d: R2 %.2f, want > 0.7", c, r2)
		}
	}
}

func TestFieldNoiseNotShared(t *testing.T) {
	ss := fieldFixture(2, 12, func(i, j, rot, c int) float64 { return 0 }, 0.02)
	ids := make([]int, len(ss))
	for i := range ids {
		ids[i] = i
	}
	for c := 0; c < 3; c++ {
		r2, _ := sharedShapeR2(ss, ids, c, false, 4)
		if r2 > 0.2 {
			t.Fatalf("independent noise channel %d: R2 %.2f, want < 0.2", c, r2)
		}
	}
}

func TestFieldSensorCoordinates(t *testing.T) {
	// The field is a sensor-fixed horizontal ramp. On a portrait photo the
	// display's v axis is the sensor's u axis, so the sensor fit should
	// share it while the display fit cannot.
	ss := make([]*FieldSample, 12)
	for p := range ss {
		sp := &FieldSample{Rotation: 0}
		if p%2 == 1 {
			sp.Rotation = 90
		}
		for c := 0; c < 3; c++ {
			sp.G[c] = make([]float64, FieldGW*FieldGH)
			sp.W[c] = make([]float64, FieldGW*FieldGH)
			for j := 0; j < FieldGH; j++ {
				for i := 0; i < FieldGW; i++ {
					u, v := fieldUV(i, j)
					if sp.Rotation == 90 {
						u = v // u_sensor = v_display; u_display is not the ramp
					}
					sp.G[c][j*FieldGW+i] = 0.05 * u
					sp.W[c][j*FieldGW+i] = 1
				}
			}
		}
		sp.Surface()
		ss[p] = sp
	}
	ids := make([]int, len(ss))
	for i := range ids {
		ids[i] = i
	}
	disp, _ := sharedShapeR2(ss, ids, 0, false, 1)
	sen, _ := sharedShapeR2(ss, ids, 0, true, 1)
	if sen <= disp+0.2 {
		t.Fatalf("sensor R2 %.2f not clearly above display %.2f", sen, disp)
	}
}

func TestFieldSurfaceRecoversKnownTilt(t *testing.T) {
	s := &FieldSample{}
	for c := 0; c < 3; c++ {
		s.G[c] = make([]float64, FieldGW*FieldGH)
		s.W[c] = make([]float64, FieldGW*FieldGH)
		for j := 0; j < FieldGH; j++ {
			for i := 0; i < FieldGW; i++ {
				u, v := fieldUV(i, j)
				s.G[c][j*FieldGW+i] = 0.02*u - 0.01*v + float64(c)*0.003
				s.W[c][j*FieldGW+i] = 1
			}
		}
	}
	s.Surface()
	for c := 0; c < 3; c++ {
		if math.Abs(s.Coef[c][1]-0.02) > 1e-6 || math.Abs(s.Coef[c][2]+0.01) > 1e-6 {
			t.Fatalf("channel %d coefs u %.6f v %.6f, want 0.02/-0.01", c, s.Coef[c][1], s.Coef[c][2])
		}
		if s.OwnR2[c] < 0.999 {
			t.Fatalf("channel %d own R2 %.3f, want ~1", c, s.OwnR2[c])
		}
	}
}
