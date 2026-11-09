package render

// Task 81: SPP's monochrome grain must be the same grain at a reduced view.
// The view draws the variance the 1:1 export keeps after the area reduction,
// and it is laid on after SPP and the Sharpness slider. These tests pin that
// against the 1:1 render box-reduced, at 2.94x (1600) and 7.84x (600), with
// the Sharpness slider at 0 and +1.

import (
	"image"
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func t81MonoBase(w, h int, scale float64, native int) *Base {
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: scale,
		Frame: native, NativeFrame: native, Raw: true, ISO: 100, Sensor: "Merrill"}
	for i := range l.Pix {
		l.Pix[i] = 0.2
	}
	return NewBase(l)
}

func t81Diff(a, b *image.RGBA64) []float32 {
	w, h := a.Rect.Dx(), a.Rect.Dy()
	out := make([]float32, w*h)
	for i := range out {
		la := 0.2126*float64(a.Pix[i*8])*256 + 0.2126*float64(a.Pix[i*8+1]) +
			0.7152*float64(a.Pix[i*8+2])*256 + 0.7152*float64(a.Pix[i*8+3]) +
			0.0722*float64(a.Pix[i*8+4])*256 + 0.0722*float64(a.Pix[i*8+5])
		lb := 0.2126*float64(b.Pix[i*8])*256 + 0.2126*float64(b.Pix[i*8+1]) +
			0.7152*float64(b.Pix[i*8+2])*256 + 0.7152*float64(b.Pix[i*8+3]) +
			0.0722*float64(b.Pix[i*8+4])*256 + 0.0722*float64(b.Pix[i*8+5])
		out[i] = float32(la - lb)
	}
	return out
}

// t81Reduce is a one-channel fractional box reduction (the app's boxTaps).
func t81Reduce(src []float32, sw, sh, dw, dh int) []float32 {
	tx, ty := boxTaps(sw, dw), boxTaps(sh, dh)
	tmp := make([]float32, dw*sh)
	for y := 0; y < sh; y++ {
		row, out := src[y*sw:], tmp[y*dw:]
		for x, ts := range tx {
			var v float32
			for _, t := range ts {
				v += row[t.i] * t.w
			}
			out[x] = v
		}
	}
	dst := make([]float32, dw*dh)
	for y, ts := range ty {
		out := dst[y*dw : (y+1)*dw]
		for _, t := range ts {
			row := tmp[t.i*dw : (t.i+1)*dw]
			for i, v := range row {
				out[i] += v * t.w
			}
		}
	}
	return dst
}

func t81Stats(v []float32, w, h, margin int) (mean, sd float64) {
	var s, s2 float64
	var n int
	for y := margin; y < h-margin; y++ {
		for x := margin; x < w-margin; x++ {
			u := float64(v[y*w+x])
			s += u
			s2 += u * u
			n++
		}
	}
	mean = s / float64(n)
	return mean, math.Sqrt(math.Max(0, s2/float64(n)-mean*mean))
}

// t81Width is the integral autocorrelation width (1 + 2·Σρ) of a field,
// averaged over both axes. A white field is 1; the mono grain's reduced field
// is close to it.
func t81Width(v []float32, w, h int) float64 {
	var mean float64
	for _, x := range v {
		mean += float64(x)
	}
	mean /= float64(len(v))
	rho := func(dx, dy int) (float64, bool) {
		var num, den float64
		for y := 0; y < h-dy; y++ {
			for x := 0; x < w-dx; x++ {
				a := float64(v[y*w+x]) - mean
				b := float64(v[(y+dy)*w+x+dx]) - mean
				num += a * b
				den += a * a
			}
		}
		if den <= 0 {
			return 0, false
		}
		return num / den, true
	}
	width := func(dx, dy int) float64 {
		r, ok := rho(dx, dy)
		if !ok || r < 0 {
			return 1
		}
		return 1 + 2*r
	}
	return (width(1, 0) + width(0, 1)) / 2
}

func TestMonoGrainScaleConsistency(t *testing.T) {
	const nw, nh = 1200, 800
	comp := Compensation{Family: "SD1", Sensor: "Merrill", SatFactor: 1}
	for _, sz := range []float64{.1, .5, 1} {
		for _, sharp := range []float64{0, 1} {
			p := preset.Neutral()
			p.Mono, p.MonoMixSet = true, true
			p.MonoR, p.MonoG, p.MonoB = 1.0/3, 1.0/3, 1.0/3
			p.MonoGrainSize, p.MonoGrainRough = sz, .5
			p.Sharpness = sharp
			p0 := p
			p0.MonoGrainSize = 0

			b1 := t81MonoBase(nw, nh, 1, nw)
			d1 := t81Diff(Render16(b1, p, comp), Render16(b1, p0, comp))

			for _, tc := range []struct {
				name string
				w, h int
				sc   float64
			}{
				{"1600", 408, 272, 408.0 / 600.0},
				{"600", 153, 102, 153.0 / 600.0},
			} {
				b := t81MonoBase(tc.w, tc.h, tc.sc, nw)
				d := t81Diff(Render16(b, p, comp), Render16(b, p0, comp))
				ref := t81Reduce(d1, nw, nh, tc.w, tc.h)
				_, sd := t81Stats(d, tc.w, tc.h, 16)
				_, sdRef := t81Stats(ref, tc.w, tc.h, 16)
				if sdRef <= 0 {
					t.Fatalf("%s sharp %.0f: empty reference", tc.name, sharp)
				}
				if r := sd / sdRef; math.Abs(r-1) > 0.10 {
					t.Errorf("size %.1f %s sharp %.0f: std ratio %.3f (%.3f vs %.3f), want within 10%%",
						sz, tc.name, sharp, r, sd, sdRef)
				}
				wv := t81Width(d, tc.w, tc.h)
				wr := t81Width(ref, tc.w, tc.h)
				if r := wv / wr; math.Abs(r-1) > 0.15 {
					t.Errorf("size %.1f %s sharp %.0f: clump width ratio %.3f (%.3f vs %.3f), want within 15%%",
						sz, tc.name, sharp, r, wv, wr)
				}
			}
		}
	}
}

// TestMonoGrainFullResolutionUnchanged pins the 1:1 parameters to the old
// model: the deferred reduced-view path must not touch n = 1.
func TestMonoGrainFullResolutionUnchanged(t *testing.T) {
	b := t81MonoBase(120, 80, 1, 120)
	p := preset.Neutral()
	p.Mono, p.MonoMixSet = true, true
	p.MonoR, p.MonoG, p.MonoB = 1.0/3, 1.0/3, 1.0/3
	p.MonoGrainSize, p.MonoGrainRough = .5, .5
	gp := b.monoGrainFor(p)
	if gp == nil || gp.deferred {
		t.Fatalf("1:1 params %+v, want the old non-deferred model", gp)
	}
	if len(gp.oct) != 1 || math.Abs(gp.oct[0].cell-1.875) > 1e-9 {
		t.Errorf("1:1 coarse octave %+v, want cell 1.875", gp.oct)
	}
	if math.Abs(gp.fine-0.175) > 1e-9 {
		t.Errorf("1:1 fine share %.6f, want 0.175", gp.fine)
	}
	want := float32((16.9*.25 + 13*.5 + 1.25) / b.monoGrainSharpGainAt(&monoGrainParams{
		oct: []grainOctave{{cell: 1.875, amp: float32(math.Sqrt(0.825))}}, fine: .175,
		sigma: 1, mean: 1, q: monoGrainQ, seed: monoGrainSeed(p), n: 1}, 1))
	if math.Abs(float64(gp.sigma-want)) > 1e-5 {
		t.Errorf("1:1 sigma %.6f, want the old %.6f", gp.sigma, want)
	}
	if got := b.newMonoGrainDeferred(p); got != nil && got.deferred {
		t.Errorf("a 1:1 base took the deferred path")
	}
}

// TestMonoGrainGPUPostAgrees mirrors web/gpu.js's deferred mono grain on the
// recipe: the same field, the same retained gain, and the output pass's
// 8-bit application against monograin.go add8.
func TestMonoGrainGPUPostAgrees(t *testing.T) {
	b := t81MonoBase(408, 272, 408.0/600.0, 1200)
	p := preset.Neutral()
	p.Mono, p.MonoMixSet = true, true
	p.MonoR, p.MonoG, p.MonoB = 1.0/3, 1.0/3, 1.0/3
	p.MonoGrainSize, p.MonoGrainRough = .5, .5
	cpu := b.newMonoGrainDeferred(p)
	r := NewGPURecipe(b, p, Compensation{Family: "SD1", Sensor: "Merrill", SatFactor: 1})
	if r.SGrain == nil || !r.SGrain.Post {
		t.Fatalf("recipe mono grain %+v, want the deferred path", r.SGrain)
	}
	if r.SGrain.Gain != cpu.gain {
		t.Errorf("recipe gain %.6f, CPU %.6f", r.SGrain.Gain, cpu.gain)
	}
	for _, i := range []int{0, 1, 37, 133, 199, 1234, 5000, 50000, 100000} {
		x, y := i%408, i/408
		var n float32
		for j, o := range r.SGrain.Oct {
			n += o.Amp * t47GPUSample(x, y, float64(o.Cell), r.SGrain.Seed^0x1234567^uint32(j)*0x9e3779b9)
		}
		n += r.SGrain.Fine * t47GPUGauss(i, r.SGrain.Seed^0x1234567)
		if d := math.Abs(float64(n - cpu.noise[i])); d > 2e-5 {
			t.Errorf("pixel %d: GPU %.7f, CPU %.7f", i, n, cpu.noise[i])
		}
	}
	// The output pass: v = floor((v/255 + d)*255 + 0.5) with
	// d = (mean + sigma*n*gain)*w/255, w=(4u(1-u))^q on the pre-toning grey.
	img := image.NewRGBA(image.Rect(0, 0, 408, 272))
	for i := range cpu.noise {
		v := uint8(40 + (i*37)%170)
		img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = v, v, v, 255
	}
	want := append([]uint8(nil), img.Pix...)
	cpu.add8(img, float64(cpu.gain))
	for i := range cpu.noise {
		u := float32(want[i*4]) / 255
		wt := monoGrainWeight(u)
		d := (cpu.mean + cpu.sigma*cpu.noise[i]*cpu.gain) / 255 * wt
		e := float32(want[i*4])/255 + d
		got := uint8(max(0, min(255, e*255+0.5)))
		if diff := int(got) - int(img.Pix[i*4]); diff < -1 || diff > 1 {
			t.Fatalf("pixel %d: post grain %d, GPU mirror %d", i, img.Pix[i*4], got)
		}
	}
}
