package render

import (
	"math"
	"testing"
)

func TestGeometryCropOnly(t *testing.T) {
	l := &Linear{W: 10, H: 8, Pix: make([]float32, 10*8*3)}
	for i := range l.Pix {
		l.Pix[i] = float32(i)
	}
	out := ApplyGeometry(l, Geometry{X: 0.2, Y: 0.25, W: 0.5, H: 0.5})
	if out.W != 5 || out.H != 4 {
		t.Fatalf("size %dx%d, want 5x4", out.W, out.H)
	}
	// Output (0,0) is source (2,2).
	if got, want := out.Pix[0], l.Pix[(2*10+2)*3]; got != want {
		t.Errorf("pixel = %v, want %v", got, want)
	}
}

func TestGeometryRotationStaysInside(t *testing.T) {
	// A uniform image rotated and cropped to its valid area has no empty
	// (clamped-edge) artefacts: every pixel keeps the value.
	l := &Linear{W: 300, H: 200, Pix: make([]float32, 300*200*3)}
	for i := range l.Pix {
		l.Pix[i] = 0.5
	}
	out := ApplyGeometry(l, Geometry{Angle: 7, W: 1, H: 1})
	s := InscribedScale(7, 300, 200)
	if math.Abs(float64(out.W)-s*300) > 1 || math.Abs(float64(out.H)-s*200) > 1 {
		t.Errorf("size %dx%d, want ~%.0fx%.0f", out.W, out.H, s*300, s*200)
	}
	for _, v := range out.Pix {
		if math.Abs(float64(v)-0.5) > 1e-5 {
			t.Fatalf("value %v", v)
		}
	}
	if !(Geometry{}).IsZero() || !(Geometry{W: 1, H: 1}).IsZero() || (Geometry{Angle: 1}).IsZero() {
		t.Error("IsZero wrong")
	}
}

func TestInscribedScale(t *testing.T) {
	if s := InscribedScale(0, 300, 200); math.Abs(s-1) > 1e-9 {
		t.Errorf("0 deg: %v", s)
	}
	// 90° of a square fits exactly; 45° of a square: 1/sqrt2.
	if s := InscribedScale(45, 100, 100); math.Abs(s-1/math.Sqrt2) > 1e-9 {
		t.Errorf("45 deg square: %v", s)
	}
}
