package render

// Task 18: a negative Highlight must compress highlights, never turn clipped
// white into a flat grey, and a smooth ramp through the whole pipeline must
// stay monotonic, end near white, and stay neutral where it clips.

import (
	"image"
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func maxDetailPreset(t *testing.T) preset.Params {
	t.Helper()
	pack, err := preset.Load(preset.Source{FS: preset.Builtin()})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pack {
		if p.Name == "[FL] Max Detail" {
			return p.Params
		}
	}
	t.Fatal("[FL] Max Detail not in the built-in pack")
	return preset.Params{}
}

// linearRamp is a smooth increasing ramp in linear light, top at the last
// column. Channel gains let a test tint it; a gain above 1 puts that channel
// past display white before the others.
func linearRamp(w, h int, gains [3]float64, top float64) *Linear {
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(top * float64(x) / float64(w-1))
			i := (y*w + x) * 3
			for c := 0; c < 3; c++ {
				l.Pix[i+c] = v * float32(gains[c])
			}
		}
	}
	return l
}

func rowLum(img *image.RGBA, x, y int) (int, int, int) {
	i := (y*img.Rect.Dx() + x) * 4
	return int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])
}

// checkRamp asserts the requirements on the middle row: non-decreasing
// brightness, near white at the end, and a neutral clipped tail.
func checkRamp(t *testing.T, img *image.RGBA, wantWhite int) {
	t.Helper()
	w, h := img.Rect.Dx(), img.Rect.Dy()
	y := h / 2
	prev := -1
	for x := 0; x < w; x++ {
		r, g, b := rowLum(img, x, y)
		v := (2126*r + 7152*g + 722*b) / 10000
		if v+1 < prev {
			t.Fatalf("not monotonic at x=%d: %d then %d", x, prev, v)
		}
		prev = v
	}
	r, g, b := rowLum(img, w-1, y)
	if (2126*r+7152*g+722*b)/10000 < wantWhite {
		t.Fatalf("clipped white ends at rgb(%d,%d,%d), want >= %d", r, g, b, wantWhite)
	}
	for x := w - 24; x < w; x++ {
		r, g, b := rowLum(img, x, y)
		if max(r, max(g, b))-min(r, min(g, b)) > 2 {
			t.Fatalf("clipped white is not neutral at x=%d: rgb(%d,%d,%d)", x, r, g, b)
		}
	}
}

func TestMaxDetailRampStaysMonotonicAndWhite(t *testing.T) {
	p := maxDetailPreset(t)
	l := linearRamp(512, 64, [3]float64{1, 1, 1}, 1.4)
	// A dark frame with a bright patch (the alley and sky of the report): the
	// Fill Light model's frame-mean term used to turn this bright tail grey.
	l.FillMean = 0.3
	out := Render(NewBase(l), p, Compensation{Family: "SD1", Sensor: "Merrill", Model: "SD1", SatFactor: 1})
	checkRamp(t, out, 248)
}

// The formula Highlight path is what a colour film (or a build with no
// measured model) uses: pixOp.apply's non-measured branch. It used to map
// white to Y/(1+0.35) — sRGB 223 — a flat grey.
func TestNegativeHighlightIsAShoulder(t *testing.T) {
	p := maxDetailPreset(t)
	p.FillLight = 0
	p.Sharpness = 0
	op := newPixOp(p, Compensation{Family: "SD1", Sensor: "Merrill", Model: "SD1", SatFactor: 1})
	op.measured = false // force the formula path
	const w, h = 512, 8
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		v := float32(1.4 * float64(x) / float64(w-1))
		r, g, b := op.apply(v, v, v, 0, 0, 0)
		for y := 0; y < h; y++ {
			i := (y*w + x) * 4
			out.Pix[i] = uint8(enc(r)*255 + 0.5)
			out.Pix[i+1] = uint8(enc(g)*255 + 0.5)
			out.Pix[i+2] = uint8(enc(b)*255 + 0.5)
			out.Pix[i+3] = 255
		}
	}
	checkRamp(t, out, 248)
}

// An uneven clip must not leave a coloured rim: a tinted ramp that reaches
// display white and goes past it must fade to neutral, not jump to a hue.
func TestUnevenClipFadesNeutral(t *testing.T) {
	p := maxDetailPreset(t)
	p.FillLight = 0
	op := newPixOp(p, Compensation{Family: "SD1", Sensor: "Merrill", Model: "SD1", SatFactor: 1})
	op.measured = false
	const w, h = 512, 8
	gains := [3]float64{1.06, 1.0, 0.94}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		v := 1.35 * float64(x) / float64(w-1)
		r, g, b := op.apply(float32(v*gains[0]), float32(v*gains[1]), float32(v*gains[2]), 0, 0, 0)
		for y := 0; y < h; y++ {
			i := (y*w + x) * 4
			out.Pix[i] = uint8(enc(r)*255 + 0.5)
			out.Pix[i+1] = uint8(enc(g)*255 + 0.5)
			out.Pix[i+2] = uint8(enc(b)*255 + 0.5)
			out.Pix[i+3] = 255
		}
	}
	y := h / 2
	// Where all three channels are past display white the result must be
	// neutral, and the chroma must not rise from one column to the next.
	lastChroma := 0.0
	for x := 0; x < w; x++ {
		r, g, b := rowLum(out, x, y)
		c := float64(max(r, max(g, b)) - min(r, min(g, b)))
		if c > lastChroma+2 {
			t.Fatalf("chroma rises at x=%d: %.0f -> %.0f (rgb %d,%d,%d)", x, lastChroma, c, r, g, b)
		}
		lastChroma = c
	}
	r, g, b := rowLum(out, w-1, y)
	if max(r, max(g, b))-min(r, min(g, b)) > 2 {
		t.Fatalf("past-white clip is coloured: rgb(%d,%d,%d)", r, g, b)
	}
}

func TestMeasuredHighlightShoulders(t *testing.T) {
	// Every measured negative Highlight step (all camera models) must stay
	// monotonic and end at exactly zero change, so no measured body can turn
	// clipped white grey through its table either.
	for fam, md := range models {
		for _, s := range md.Tone["Highlight"] {
			if s.Value >= 0 {
				continue
			}
			prev := -1.0
			for i := 0; i <= 2000; i++ {
				x := float64(i) / 2000
				y := x + knot(&s.Delta, x)
				if y < prev-1e-6 {
					t.Fatalf("%s Highlight %g not monotonic at x=%g: %g then %g", fam, s.Value, x, prev, y)
				}
				prev = y
			}
			if d := knot(&s.Delta, 1); math.Abs(d) > 1e-12 {
				t.Fatalf("%s Highlight %g leaves white at %g", fam, s.Value, 1+d)
			}
		}
	}
}

func TestHighlightShoulderMonotonicOverRange(t *testing.T) {
	// The formula's slope must stay positive for the whole slider range, and
	// the shoulder must leave white exactly white.
	for _, hl := range []float64{-0.01, -0.35, -0.7} {
		prev := -1.0
		for i := 0; i <= 2000; i++ {
			y := 1.4 * float64(i) / 2000
			f := y * (1 + 2*hl*y*(1-y))
			if f < prev-1e-9 {
				t.Fatalf("hl=%g not monotonic at Y=%g: %g then %g", hl, y, prev, f)
			}
			prev = f
		}
		if f := 1 * (1 + 2*hl*1*(1-1)); f != 1 {
			t.Fatalf("hl=%g maps white to %g", hl, f)
		}
	}
}
