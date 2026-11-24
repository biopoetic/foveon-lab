package main

import (
	"math"
	"testing"
)

// t90sRGBFrame builds an interleaved sRGB-byte frame from a luma function.
func t90sRGBFrame(w, h int, f func(x, y float64) float64) []float32 {
	out := make([]float32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(f(float64(x), float64(y)))
			i := (y*w + x) * 3
			out[i], out[i+1], out[i+2] = v, v, v
		}
	}
	return out
}

// t90Step is a smooth vertical edge: 20 on the left, 220 on the right, with a
// Gaussian transition of the given sigma. If maskSigma is positive the edge
// is ringed like an unsharp mask: base + amount*(base - blur(base)).
func t90Step(w, h int, sigma, maskSigma, amount float64) []float32 {
	erf := func(x float64) float64 { return 0.5 * (1 + math.Erf(x/(math.Sqrt2*sigma))) }
	base := t90sRGBFrame(w, h, func(x, y float64) float64 {
		return 20 + 200*erf(x-float64(w)/2)
	})
	if maskSigma == 0 {
		return base
	}
	blur := gaussBlur3(base, w, h, maskSigma)
	out := make([]float32, len(base))
	for i := range base {
		v := float64(base[i]) + amount*(float64(base[i])-float64(blur[i]))
		out[i] = float32(math.Max(0, math.Min(255, v)))
	}
	return out
}

// The edge measure must read back a known width and a known overshoot; a
// blurrier image must read wider, and an overshooting one must read as
// overshooting. This is the check that the edge table is not noise.
func TestT90EdgeProfiles(t *testing.T) {
	const w, h = 96, 48
	sharp := t90Step(w, h, 0.8, 0, 0)
	soft := t90Step(w, h, 1.8, 0, 0)
	ring := t90Step(w, h, 0.5, 0.8, 1.5) // an unsharp mask, amount 1.5
	o1, s1 := edgeProfileStats(soft, sharp, w, h)
	if o1.N == 0 || s1.N == 0 {
		t.Fatal("no edges found in a clean synthetic step")
	}
	if !(o1.Width > s1.Width+0.4) {
		t.Errorf("a 1.8 px step must read wider than a 0.8 px one: %.2f vs %.2f", o1.Width, s1.Width)
	}
	want := 2.5629 * 0.8 // 10-90% width of a Gaussian step
	if math.Abs(s1.Width-want) > 0.7 {
		t.Errorf("0.8 px step width = %.2f, want ~%.2f", s1.Width, want)
	}
	_, rs := edgeProfileStats(sharp, ring, w, h)
	if rs.Overshoot < 0.05 {
		t.Errorf("a sharpened step read as only %.1f%% overshoot", 100*rs.Overshoot)
	}
	// The deliberately wrong input: softening ours must not be read as an
	// overshoot.
	if o1.Overshoot >= s1.Overshoot+0.05 {
		t.Errorf("the soft image read as more overshoot than the sharp one: %.2f vs %.2f", o1.Overshoot, s1.Overshoot)
	}
}

// A one-pixel displacement must be read as a shift, and the aligned profile
// residual must be far smaller than the unaligned one: the edge error has to
// split into position and shape, not be one number.
func TestT90EdgeShift(t *testing.T) {
	const w, h = 96, 48
	ref := t90Step(w, h, 0.8, 0, 0)
	erf := func(x float64) float64 { return 0.5 * (1 + math.Erf(x/(math.Sqrt2*0.8))) }
	shifted := t90sRGBFrame(w, h, func(x, y float64) float64 {
		return 20 + 200*erf(x-1-float64(w)/2)
	})
	o, _ := edgeProfileStats(shifted, ref, w, h)
	if math.Abs(o.Shift-1.0) > 0.3 {
		t.Errorf("a 1 px displacement read as %.2f px", o.Shift)
	}
	if !(o.ProfileAligned < 0.5*o.ProfileUnaligned) {
		t.Errorf("local alignment did not remove the displacement: unaligned %.3f, aligned %.3f", o.ProfileUnaligned, o.ProfileAligned)
	}
}

// The held-out choice must not peek: a body that prefers the strongest
// setting must not drag the setting chosen on the other bodies, and the
// held-out body is then scored at the training choice.
func TestT90HeldOutSelection(t *testing.T) {
	// Three bodies; body a prefers k=8, the other two prefer k=4 (Detail 0).
	var errs [][]float64
	bodies := []string{"a", "a", "b", "b", "c", "c"}
	for _, b := range bodies {
		v := make([]float64, len(t90Details))
		for k := range v {
			v[k] = 5
			if b == "a" {
				v[k] = 5 + 0.01*float64(k-8)*float64(k-8)
			} else {
				v[k] = 5 + 0.01*float64(k-4)*float64(k-4)
			}
		}
		errs = append(errs, v)
	}
	var train []int
	for i, b := range bodies {
		if b != "a" {
			train = append(train, i)
		}
	}
	if k := t90Pick(errs, train); k != 4 {
		t.Errorf("training on b and c picked Detail %+.2f, want 0.00", t90Details[k])
	}
	if k := t90Pick(errs, []int{0}); k != 8 {
		t.Errorf("body a's own best is %+.2f, want +1.00", t90Details[k])
	}
}

// The frequency split must be exact (low+detail == full), flat on identical
// images, and must charge a 1 px blur of ours to detail, not to the low-pass.
func TestT90FreqSplitGate(t *testing.T) {
	const w, h = 160, 96
	// Fine texture (period ~4 px), so a 1 px blur is a detail change.
	base := t90sRGBFrame(w, h, func(x, y float64) float64 {
		return 128 + 40*math.Sin(x*1.7)*math.Cos(y*1.9)
	})
	spp := append([]float32(nil), base...)
	if f, lo, de := freqSplit(base, spp, w, h, 4); f > 1e-9 || lo > 1e-9 || de > 1e-9 {
		t.Fatalf("identical images split to %.4f/%.4f/%.4f", f, lo, de)
	}
	soft := gaussBlur3(spp, w, h, 1)
	f, lo, de := freqSplit(soft, spp, w, h, 4)
	if math.Abs(lo+de-f) > 1e-6 {
		t.Fatalf("split does not add up: %.4f + %.4f != %.4f", lo, de, f)
	}
	if !(de > 0.7*f) {
		t.Errorf("a 1 px blur of ours should be charged to detail: low %.2f, detail %.2f, full %.2f", lo, de, f)
	}
	more := gaussBlur3(soft, w, h, 1)
	_, lo2, de2 := freqSplit(more, spp, w, h, 4)
	if !(de2 > de+0.2) {
		t.Errorf("the 1 px gate did not grow detail: %.2f -> %.2f", de, de2)
	}
	if math.Abs(lo2-lo) > 0.02 {
		t.Errorf("the 1 px gate moved the low-pass term: %.2f -> %.2f", lo, lo2)
	}
	// A deliberate low-frequency error must land in the low-pass term.
	tint := append([]float32(nil), spp...)
	for i := range tint {
		tint[i] += 6
	}
	f3, lo3, de3 := freqSplit(tint, spp, w, h, 4)
	if !(lo3 > 0.9*f3) || de3 > 0.1*f3 {
		t.Errorf("a 6-level DC offset split wrong: low %.2f, detail %.2f (full %.2f)", lo3, de3, f3)
	}
}

// Flat-patch noise must recover a known sigma and must not count a textured
// block.
func TestT90FlatNoise(t *testing.T) {
	const w, h = 64, 64
	flat := t90sRGBFrame(w, h, func(x, y float64) float64 { return 100 })
	noisy := append([]float32(nil), flat...)
	var seed uint32 = 12345
	rnd := func() float64 {
		seed = seed*1664525 + 1013904223
		return float64(seed>>8)/float64(1<<24) - 0.5
	}
	for i := range noisy {
		noisy[i] += float32(6 * rnd()) // uniform(-3,3), sigma 1.73
	}
	s := flatNoise(noisy, flat, w, h)
	if s.Blocks == 0 {
		t.Fatal("flat blocks not found")
	}
	if math.Abs(s.Ours-1.73) > 0.5 {
		t.Errorf("recovered sigma %.2f, want ~1.73", s.Ours)
	}
	// A hard texture must be excluded by the flat mask.
	tex := t90sRGBFrame(w, h, func(x, y float64) float64 { return 100 + 30*math.Sin(x*1.7) })
	s2 := flatNoise(noisy, tex, w, h)
	if s2.Blocks > 2 {
		t.Errorf("a 30-level texture left %d flat blocks", s2.Blocks)
	}
}
