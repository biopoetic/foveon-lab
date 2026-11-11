package render

// Task 66: the B&W scan's highlights burned out because the old 0.95
// shoulder received the film's upper density range at 2-5x white and
// flattened all of it to 255. The scan now has a scanner's white point: one
// fixed shoulder, starting well below white and asymptotic to a paper white
// below the display clip. These tests pin the shoulder, the wedge, the films
// still differing in the highlights, and the GPU preview reading the same
// table (including the highlights, which the task-65 test did not reach).

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// bwScan8 is the 8-bit sRGB level the render would produce for a linear Y.
func bwScan8(y float64) int {
	return int(float64(enc(float32(y)))*255 + 0.5)
}

// Task 66: guards the B&W scan shoulder: identity at the knee, slope 1, monotone, below the 254 clip.
// A failure means the scanner's white point or shoulder moved and highlights can burn out.
// Change only when the scan shoulder is deliberately retuned; re-check the knee/white prints.
func TestBWScanShoulder(t *testing.T) {
	if bwScanKnee <= 0 || bwScanKnee >= 0.95 {
		t.Fatalf("knee %.3f is not well below white", bwScanKnee)
	}
	if bwScanWhite <= bwScanKnee || bwScanWhite >= 1 {
		t.Fatalf("paper white %.4f is not between the knee and 1", bwScanWhite)
	}
	if bwScan8(bwScanWhite) > 253 {
		t.Errorf("paper white %.4f encodes to %d, not below 254", bwScanWhite, bwScan8(bwScanWhite))
	}
	if math.Abs(bwScanShoulder(bwScanKnee)-bwScanKnee) > 1e-12 {
		t.Errorf("shoulder is not the identity at the knee")
	}
	// Slope 1 at the knee: one step of 1e-4 must move the output by ~1e-4.
	if d := (bwScanShoulder(bwScanKnee+1e-4) - bwScanShoulder(bwScanKnee)) / 1e-4; math.Abs(d-1) > 1e-3 {
		t.Errorf("shoulder slope at the knee is %.4f, want 1", d)
	}
	prev := -1.0
	for _, y := range []float64{0, 0.18, 0.3, 0.6, 0.8, 1, 2, 4, 10, 100, 1e6} {
		v := bwScanShoulder(y)
		if v < prev-1e-12 {
			t.Fatalf("shoulder is not monotone at Y=%g (%.6f after %.6f)", y, v, prev)
		}
		if v > bwScanWhite+1e-12 {
			t.Fatalf("shoulder at Y=%g is %.6f, above the paper white", y, v)
		}
		prev = v
	}
	if v := bwScanShoulder(1e6); v < bwScanWhite-1e-4 {
		t.Errorf("shoulder at a huge Y is %.6f, not close to the paper white %.4f", v, bwScanWhite)
	}
	if v := bwScanShoulder(1); v > 0.85 {
		t.Errorf("scene white maps to %.4f: the shoulder starts too high", v)
	}
	t.Logf("knee %.2f L* %.1f; paper white %.4f = 8-bit %d; scene Y=1 -> %.4f (L* %.1f)",
		bwScanKnee, cieLightness(bwScanKnee), bwScanWhite, bwScan8(bwScanWhite),
		bwScanShoulder(1), cieLightness(bwScanShoulder(1)))
}

// Task 66: guards every B&W film's highlight wedge and 8-bit ceiling (< 254).
// A failure means a film's scan reaches white, loses monotonicity, or stops differing in the highlights.
// Change only when the scan shoulder or a film's curve is deliberately changed; re-check all 11 films.
func TestBWScanHighlight(t *testing.T) {
	type row struct {
		name         string
		plus2, plus4 float64
		max8         int
	}
	var rows []row
	for _, film := range bwScanFilms {
		p := preset.Neutral()
		p.Film, p.NoGrain = film, true
		tone := newFilmTone(p)
		if tone == nil {
			t.Fatalf("%s: no tone", film)
		}
		if mid := cieLightness(float64(tone.at(0.18))); math.Abs(mid-49.5) > 0.1 {
			t.Errorf("%s: 18%% grey is L* %.2f, want 49.5", film, mid)
		}
		max8 := 0
		prev := -1.0
		for s := -8; s <= 4; s++ {
			y := float32(0.18 * math.Exp2(float64(s)))
			L := cieLightness(float64(tone.at(y)))
			if L < prev-0.05 {
				t.Errorf("%s: wedge is not monotonic at %+d stops (%.1f after %.1f)", film, s, L, prev)
			}
			if L < 2 {
				t.Errorf("%s: wedge reaches L* %.1f at %+d stops", film, L, s)
			}
			prev = L
		}
		for i := 0; i <= filmToneN; i++ {
			if n := bwScan8(float64(tone.lut[i])); n > max8 {
				max8 = n
			}
		}
		if max8 >= 254 {
			t.Errorf("%s: tone LUT reaches 8-bit %d", film, max8)
		}
		rows = append(rows, row{film,
			cieLightness(float64(tone.at(float32(0.18 * 4)))),
			cieLightness(float64(tone.at(float32(0.18 * 16)))), max8})
	}
	by := map[string]row{}
	for _, r := range rows {
		by[r.name] = r
	}
	tm := by["Kodak T-Max 100"]
	for _, name := range []string{"Ilford FP4 Plus", "Ilford Pan F Plus", "Agfa Agfapan APX 25"} {
		r := by[name]
		if r.plus2 <= tm.plus2+0.5 {
			t.Errorf("%s at +2 stops is L* %.1f, not above T-Max 100's %.1f", name, r.plus2, tm.plus2)
		}
		if r.plus4 <= tm.plus4+0.5 {
			t.Errorf("%s at +4 stops is L* %.1f, not above T-Max 100's %.1f", name, r.plus4, tm.plus4)
		}
		if r.plus4 > 99.5 {
			t.Errorf("%s at +4 stops is L* %.1f: burnt out", name, r.plus4)
		}
	}
	for _, r := range rows {
		t.Logf("%-32s +2 %.1f, +4 %.1f L*, max 8-bit %d", r.name, r.plus2, r.plus4, r.max8)
	}
}

// Task 66: guards GPU = CPU in the highlights, including scene white and the shoulder.
// A failure means web/gpu.js's measured pre table no longer matches the CPU above the knee.
// Change only when the shader's table path changes; update the Go mirror and this frame with it.
func TestBWScanGPUHighlights(t *testing.T) {
	const w, h = 40, 30
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, ISO: 100, Sensor: "Merrill"}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			u := float64(x) / float64(w-1)
			v := float64(y) / float64(h-1)
			// Up to scene white and beyond; enc() clamps the table at 1, as
			// the CPU's pre table does.
			y0 := 0.6 + 0.8*u*u
			l.Pix[i] = float32(y0)
			l.Pix[i+1] = float32(0.5 + 0.6*v*v)
			l.Pix[i+2] = float32(0.4 + 0.7*(1-u)*(1-v))
		}
	}
	b := NewBase(l)
	for _, film := range []string{"Kodak Tri-X Pan 400 (TX)", "Kodak T-Max 100", "Ilford FP4 Plus", "Ilford Pan F Plus"} {
		p := preset.Neutral()
		p.Film, p.NoGrain = film, true
		measured := Compensation{Family: "SD1", Sensor: "Merrill", Model: "SD1", SatFactor: 1}
		cpu := Render(b, p, measured)
		r := NewGPURecipe(b, p, measured)
		if !r.Measured || r.FilmTone != nil || len(r.Pre) != 2*(GPULutN+1) || len(r.Post) != 2*(GPULutN+1) {
			t.Fatalf("%s: recipe is not the measured pre table", film)
		}
		maxd, bad, max8 := 0, 0, 0
		for i := 0; i < w*h; i++ {
			c := [3]float32{l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2]}
			Y := float32(0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2])
			x := enc(Y)
			x = gpuLutSample(r.Pre, 0, x)
			x = gpuLutSample(r.Post, 0, x)
			Y = dec(x)
			for k := 0; k < 3; k++ {
				want := int(float32(enc(Y))*255 + 0.5)
				got := int(cpu.Pix[i*4+k])
				if got > max8 {
					max8 = got
				}
				d := got - want
				if d < 0 {
					d = -d
				}
				if d > maxd {
					maxd = d
				}
				if d > 1 {
					bad++
				}
			}
		}
		if bad > 0 {
			t.Fatalf("%s highlights: %d channels differ by more than one 8-bit level (max %d)", film, bad, maxd)
		}
		if max8 >= 254 {
			t.Errorf("%s highlights: CPU reaches 8-bit %d", film, max8)
		}
		t.Logf("%-32s CPU max 8-bit %d, GPU max |d| %d", film, max8, maxd)
	}
}
