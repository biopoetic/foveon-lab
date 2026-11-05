package render

// Task 58: at a reduced view the film grain must not be sharpened at the
// view's scale. SPP's mask and the Sharpness slider sharpen the full-size
// export, and the export's grain keeps only the gain that survives the area
// reduction; the view's own mask would add far more (84% against 35% at
// 1600). The render holds the grain back at a reduced view and lays it on
// after the sharpening passes with this gain. The 1:1 path is unchanged.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func t58GrainOp(t *testing.T, scale float64, film string) (*Base, preset.Params, *filmOp) {
	t.Helper()
	f, ok := FilmByKey(film)
	if !ok {
		t.Fatalf("%s missing", film)
	}
	l := flat(96, 64, 0.2)
	l.Scale, l.Frame, l.NativeFrame = scale, 4704, 4704
	l.Raw, l.ISO, l.Sensor = true, 100, "DPM"
	b := NewBase(l)
	p := preset.Params{Film: film, Grain: f.Grain, FilmWidth: 36, Mono: f.BW, GrainSet: true}
	op := b.newFilm(p, f.BW)
	if op == nil {
		t.Fatalf("%s: no grain", film)
	}
	return b, p, op
}

func TestGrainDeferredGain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scale float64
		sharp float64
		want  float64
	}{
		// The export's SPP mask at 1:1, seen through the reduction: the
		// gain of sppView(1/n) on white grain (Σh²).
		{"1600", 1600.0 / 4704.0, 0, 1.341},
		{"600", 600.0 / 4704.0, 0, 1.048},
		// The Sharpness slider at +1/-1 is part of the export's chain too.
		// render.go clamps the amount to [-1, 2], so -1 is a cross of -1.
		{"1600 sharp +1", 1600.0 / 4704.0, 1, 1.341 * 1.592},
		{"1600 sharp -1", 1600.0 / 4704.0, -1, 1.341 * 0.740},
	} {
		b, p, op := t58GrainOp(t, tc.scale, "Kodak Tri-X Pan 400 (TX)")
		p.Sharpness = tc.sharp
		if !op.reduced {
			t.Fatalf("%s: view not reduced", tc.name)
		}
		if got := op.deferredGain(b, p, true); math.Abs(got-tc.want) > 0.004 {
			t.Errorf("%s: deferred gain %.4f, want %.4f", tc.name, got, tc.want)
		}
		if got := op.deferredGain(b, p, false); got != 1 {
			t.Errorf("%s: unsharpened deferred gain %.4f, want 1", tc.name, got)
		}
	}
	// The 1:1 render is the reference: nothing is deferred there.
	b, p, op := t58GrainOp(t, 1, "Kodak Tri-X Pan 400 (TX)")
	if op.reduced {
		t.Error("1:1 view marked reduced")
	}
	if got := op.deferredGain(b, p, true); got != 1 {
		t.Errorf("1:1 deferred gain %.4f, want 1", got)
	}
}

// TestGrainDeferredRender pins the render-order decision: at a reduced view
// with sharpening the film grain is held out of the tone pass (it is added
// after the mask), and at 1:1 it is not.
func TestGrainDeferredRender(t *testing.T) {
	for _, scale := range []float64{1, 1600.0 / 4704.0} {
		b, p, _ := t58GrainOp(t, scale, "Kodak Tri-X Pan 400 (TX)")
		film := renderIntoOpt(b, p, Compensation{}, true, func(int, float32, float32, float32) {})
		if film == nil {
			t.Fatalf("scale %.3f: no film op", scale)
		}
		if want := scale < 1; film.deferGrain != want {
			t.Errorf("scale %.3f: deferGrain %v, want %v", scale, film.deferGrain, want)
		}
	}
}
