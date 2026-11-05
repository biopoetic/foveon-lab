package render

// A colour-negative scan is one more 3-D table over the negative's densities,
// so the GPU preview uses the same shader path as a printed negative. This
// mirrors web/gpu.js's film pass (as task60_gpu_test.go does for [WB]) over a
// scan recipe and checks it against the CPU render to one 8-bit level.

import (
	"strings"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func TestColorScanGPURecipeMirrorsCPU(t *testing.T) {
	const w, h = 32, 24
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, ISO: 100, Sensor: "Merrill"}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			u := float64(x) / float64(w-1)
			v := float64(y) / float64(h-1)
			l.Pix[i] = float32(0.02 + 0.35*u + 0.10*v)
			l.Pix[i+1] = float32(0.03 + 0.30*u + 0.12*(1-v))
			l.Pix[i+2] = float32(0.01 + 0.25*v + 0.10*u)
		}
	}
	b := NewBase(l)
	cc := [9]float64{1.5, -0.4, -0.1, -0.2, 1.4, -0.2, 0.0, -0.5, 1.5}
	comp := Compensation{Family: "SD1", Sensor: "Merrill", CC: &cc, SatFactor: 1}

	p := preset.Neutral()
	p.Film = "Kodak Portra 400"
	p.Paper = PaperScan
	p.NoGrain = true

	cpu := Render(b, p, comp)
	r := NewGPURecipe(b, p, comp)
	if r.ColorFilm == nil || !strings.Contains(r.ColorFilm.Key, "|"+PaperScan+"|") {
		t.Fatalf("scan recipe colour film: %+v", r.ColorFilm)
	}
	if f := gpuCFKeys[r.ColorFilm.Key]; f == nil || f.printLUT != nil {
		t.Fatalf("scan colour film table missing or a print table")
	}
	maxd, mism := 0, 0
	for i := 0; i < w*h; i++ {
		c := [3]float32{l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2]}
		c = t60GPUFilm(r, [3]float32{c[0] * r.ColorFilm.Exp, c[1] * r.ColorFilm.Exp, c[2] * r.ColorFilm.Exp})
		for k := 0; k < 3; k++ {
			c[k] = c[k] * r.Gain[k] / r.ColorFilm.Exp
		}
		Y := float32(0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2])
		if Y > 1e-6 {
			d := dec(enc(Y))
			k := d / Y
			c[0], c[1], c[2] = c[0]*k, c[1]*k, c[2]*k
		}
		for k := 0; k < 3; k++ {
			want := int(float32(enc(c[k]))*255 + 0.5)
			got := int(cpu.Pix[i*4+k])
			d := got - want
			if d < 0 {
				d = -d
			}
			if d > maxd {
				maxd = d
			}
			if d > 1 {
				mism++
			}
		}
	}
	if mism > 0 {
		t.Fatalf("%d channels differ by more than 1 level (max %d): the GPU scan pass does not match the CPU", mism, maxd)
	}
}
