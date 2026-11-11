package render

import (
	"bytes"
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// blotchFrame is a mid-grey frame at full sensor scale with two things on
// it: low-amplitude green/magenta blotches about 40 px across (what high
// ISO leaves), and one hard colour edge of equal luminance down the middle
// (orange | blue), which a careless chroma blur would bleed across.
func blotchFrame(blotch float64) (pix []float32, w, h int) {
	w, h = 240, 160
	pix = make([]float32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			l := 0.20
			// Object colour: orange on the left, blue on the right, same luminance.
			cr, cg, cb := 0.10, -0.01, -0.20
			if x >= w/2 {
				cr, cg, cb = -0.08, -0.005, 0.30
			}
			// The blotch: green <-> magenta, luminance-free.
			b := blotch * math.Sin(float64(x)*2*math.Pi/80) * math.Sin(float64(y)*2*math.Pi/80)
			r, g, bl := l+cr+b*0.7152, l+cg-b*(0.2126+0.0722), l+cb+b*0.7152
			i := (y*w + x) * 3
			pix[i], pix[i+1], pix[i+2] = float32(r), float32(g), float32(bl)
		}
	}
	return
}

func lumaOf(p []float32, i int) float64 {
	return 0.2126*float64(p[i]) + 0.7152*float64(p[i+1]) + 0.0722*float64(p[i+2])
}

// blotchRMS is the green-channel deviation from the side's own mean, away
// from the edge: the blotch amplitude that is left.
func blotchRMS(p []float32, w, h int) float64 {
	var s float64
	var n int
	for _, side := range [][2]int{{20, w/2 - 30}, {w/2 + 30, w - 20}} {
		var m float64
		c := 0
		for y := 20; y < h-20; y++ {
			for x := side[0]; x < side[1]; x++ {
				m += float64(p[(y*w+x)*3+1])
				c++
			}
		}
		m /= float64(c)
		for y := 20; y < h-20; y++ {
			for x := side[0]; x < side[1]; x++ {
				d := float64(p[(y*w+x)*3+1]) - m
				s += d * d
				n++
			}
		}
	}
	return math.Sqrt(s / float64(n))
}

func TestChromaSmoothRemovesBlotchesKeepsEdgeAndLuminance(t *testing.T) {
	pix, w, h := blotchFrame(0.02)
	d := chromaSmoothDelta(pix, w, h, 1)
	out := make([]float32, len(pix))
	for i := range pix {
		out[i] = pix[i] + d[i]
	}
	// Luminance is untouched.
	var worst float64
	for i := 0; i < len(pix); i += 3 {
		worst = math.Max(worst, math.Abs(lumaOf(out, i)-lumaOf(pix, i)))
	}
	if worst > 1e-5 {
		t.Errorf("luminance moved by %.6f", worst)
	}
	// The blotches are mostly gone.
	before, after := blotchRMS(pix, w, h), blotchRMS(out, w, h)
	if after > 0.45*before {
		t.Errorf("blotch amplitude %.5f -> %.5f: less than half removed", before, after)
	}
	// The colour edge holds: 12 px either side of it the blue channel still
	// has (almost) the full step.
	step := func(p []float32) float64 {
		var a, b float64
		for y := 20; y < h-20; y++ {
			a += float64(p[(y*w+w/2-12)*3+2])
			b += float64(p[(y*w+w/2+12)*3+2])
		}
		return (b - a) / float64(h-40)
	}
	if s0, s1 := step(pix), step(out); s1 < 0.93*s0 {
		t.Errorf("the colour edge's blue step fell from %.4f to %.4f: it bled", s0, s1)
	}
}

// The check that must fail: with no blotches there is nothing to remove, so
// a filter that "passes" the test above by blurring everything would move
// this frame too. It must leave it almost alone.
func TestChromaSmoothLeavesACleanFrameAlone(t *testing.T) {
	pix, w, h := blotchFrame(0)
	d := chromaSmoothDelta(pix, w, h, 1)
	var worst float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x > w/2-30 && x < w/2+30 {
				continue // the edge zone is judged by the step test
			}
			for k := 0; k < 3; k++ {
				worst = math.Max(worst, math.Abs(float64(d[(y*w+x)*3+k])))
			}
		}
	}
	if worst > 0.002 {
		t.Errorf("a clean frame moved by %.5f away from the edge", worst)
	}
}

// At 0 the render is the render as it always was; above 0 it changes, the
// control is clamped, and the GPU preview hands the picture to the CPU.
func TestChromaSmoothInRender(t *testing.T) {
	pix, w, h := blotchFrame(0.02)
	b := NewBase(&Linear{W: w, H: h, Pix: pix, Scale: 1, Frame: w, NativeFrame: w, Raw: true, ISO: 1600, Sensor: "DPM"})
	comp := Compensation{Family: "SD1", Sensor: "Merrill", SatFactor: 1}
	render := func(v float64) []uint8 {
		p := preset.Neutral()
		p.ChromaSmooth = v
		return RenderOpt(b, p, comp, false).Pix
	}
	off := render(0)
	if b.chromaD != nil {
		t.Error("the filter ran with the control at 0")
	}
	on := render(1)
	if bytes.Equal(off, on) {
		t.Fatal("the control has no effect on the render")
	}
	if !bytes.Equal(render(7), on) || !bytes.Equal(render(-2), off) {
		t.Error("the control is not clamped to 0..1")
	}
	p := preset.Neutral()
	if NewGPURecipe(b, p, comp).CPU != "" {
		t.Error("the default recipe asks for the CPU")
	}
	p.ChromaSmooth = 0.5
	if NewGPURecipe(b, p, comp).CPU == "" {
		t.Error("the recipe with Colour blotches on does not ask for the CPU")
	}
}
