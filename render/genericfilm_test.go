package render

// A colour-managed source with no Foveon layers (another camera's raw) gets
// the colour film fitted to the sRGB/CIE observer response, not to silicon.
// The point of the test is that the fit exists, is finite, and actually
// changes the picture -- the report says plainly that it is an
// approximation.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func TestGenericColourFilm(t *testing.T) {
	S := layerSens("sRGB")
	if len(S) == 0 {
		t.Fatal("no sRGB sensitivity")
	}
	for i, s := range S {
		for k, v := range s {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("sample %d channel %d is %v", i, k, v)
			}
		}
	}
	f := colorFilmOnPush("Fujifilm Fujichrome Velvia 100", "", "sRGB", 0, ColorPrint{})
	if f == nil {
		t.Fatal("no colour film on the generic sRGB response")
	}
	for i, v := range f.rp {
		d := float64(v)
		if math.IsNaN(d) || math.IsInf(d, 0) || math.Abs(d) > 100 {
			t.Fatalf("layer map %d is %v", i, v)
		}
	}
	l := &Linear{W: 3, H: 1, Pix: []float32{
		0.18, 0.18, 0.18,
		0.45, 0.10, 0.08,
		0.05, 0.30, 0.08,
	}}
	p := preset.Neutral()
	p.Film = "Fujifilm Fujichrome Velvia 100"
	out := Render(NewBase(l), p, Compensation{SatFactor: 1, GenericFilm: true})
	if out.Rect.Dx() != 3 {
		t.Fatalf("render %v", out.Rect)
	}
	var diff float64
	for i := 0; i < 3; i++ {
		for c := 0; c < 3; c++ {
			v := float64(out.Pix[i*4+c])
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("pixel %d channel %d is %v", i, c, v)
			}
			diff += math.Abs(v - float64(ToSRGB(l.Pix[i*3+c]))*255)
		}
	}
	if diff < 1 {
		t.Fatalf("the film changed nothing (diff %.3f)", diff)
	}
	// A generic film and a Foveon film of the same stock are different
	// fits; the generic one must not silently be the Merrill one.
	g := colorFilmOnPush("Fujifilm Fujichrome Velvia 100", "", "Merrill", 0, ColorPrint{})
	if g == nil {
		t.Fatal("no Merrill film")
	}
	if f.rp == g.rp {
		t.Fatal("the generic fit reused the Foveon layer map")
	}
}
