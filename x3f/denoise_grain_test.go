package x3f

// Task 53: the high-ISO restore must leave even, fine grain instead of a
// smooth field with only the residual's >2 sigma tail. These tests pin the
// two halves of that: the grain blend is on at ISO 3200, and it cannot touch
// ISO 100 (the base-ISO frame stays bit-identical).

import (
	"math"
	"testing"
)

// grainFrame is a flat mid-tone with deterministic Gaussian noise. A flat
// field keeps estimateNoise on the noise model, and the 16x16 blocks are
// large enough for it to fit.
func grainFrame(w, h int, sigma float64) *CamImage {
	img := &CamImage{W: w, H: h, Pix: make([]float32, w*h*3)}
	s := uint32(1234567)
	rnd := func() float64 {
		// Two uniforms -> one Gaussian (Box-Muller), deterministic.
		s = s*1664525 + 1013904223
		u1 := (float64(s>>8) + 0.5) / float64(1<<24)
		s = s*1664525 + 1013904223
		u2 := (float64(s>>8) + 0.5) / float64(1<<24)
		return math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
	}
	for i := 0; i < w*h; i++ {
		v := float32(0.06 + sigma*rnd())
		img.Pix[i*3], img.Pix[i*3+1], img.Pix[i*3+2] = v, v, v
	}
	return img
}

// hpStats is the mean, standard deviation, largest absolute value and
// kurtosis of the 0-1 px luminance residual in the central region. The
// residual is what the eye reads as fine grain; the kurtosis says whether
// it is even (near 3) or a flat field with a sparse tail (much higher).
func hpStats(img *CamImage) (mean, std, maxAbs, kurt float64) {
	w, h := img.W, img.H
	n := w * h
	y := make([]float32, n)
	for i := 0; i < n; i++ {
		y[i] = 0.2126*img.Pix[i*3] + 0.7152*img.Pix[i*3+1] + 0.0722*img.Pix[i*3+2]
	}
	hp := boxBlur(y, w, h, 1)
	// Central region, away from the mirroring at the edges.
	x0, x1, y0, y1 := w/4, 3*w/4, h/4, 3*h/4
	var sum, sum2, sum4 float64
	var cnt int
	for j := y0; j < y1; j++ {
		for i := x0; i < x1; i++ {
			d := float64(y[j*w+i] - hp[j*w+i])
			sum += d
			sum2 += d * d
			sum4 += d * d * d * d
			if math.Abs(d) > maxAbs {
				maxAbs = math.Abs(d)
			}
			cnt++
		}
	}
	mean = sum / float64(cnt)
	v := sum2/float64(cnt) - mean*mean
	if v > 0 {
		std = math.Sqrt(v)
		kurt = sum4 / float64(cnt) / (v * v)
	}
	return
}

func TestHighISOGrainIsEven(t *testing.T) {
	oldGrain := dnGrain
	defer func() { dnGrain = oldGrain }()

	const w, h = 192, 192
	run := func(g float64) (mean, std, maxAbs, kurt float64) {
		dnGrain = g
		img := grainFrame(w, h, 0.004)
		Denoise(img, DenoiseOptions{Strength: 0.3, ISO: 3200, NoiseAware: true})
		return hpStats(img)
	}
	offMean, offStd, offMax, offKurt := run(0)
	onMean, onStd, onMax, onKurt := run(0.7)

	t.Logf("grain off: mean %.6f std %.6f max %.6f kurtosis %.2f", offMean, offStd, offMax, offKurt)
	t.Logf("grain on:  mean %.6f std %.6f max %.6f kurtosis %.2f", onMean, onStd, onMax, onKurt)

	// The fine band must carry real, visible grain, not a token amount.
	if onStd < offStd*1.5 {
		t.Errorf("fine grain did not come back: std %.6f -> %.6f", offStd, onStd)
	}
	// The blend is linear, so the tail is scaled with the body: the largest
	// residual must no longer stand many body-sigmas above the grain. (The
	// absolute max may be a little larger because a Gaussian tail has more
	// samples above the old flat body; the ratio is what the eye sees.)
	if onMax/onStd > offMax/offStd*0.9 {
		t.Errorf("tail still dominates the body: max/std %.2f -> %.2f", offMax/offStd, onMax/onStd)
	}
	// An even field has a Gaussian-ish residual. The thresholded path leaves
	// a flat body plus a sparse tail, which has a much higher kurtosis.
	if onKurt > offKurt*0.75 {
		t.Errorf("residual still tail-heavy: kurtosis %.2f -> %.2f", offKurt, onKurt)
	}
	if math.Abs(onMean-offMean) > 0.002 {
		t.Errorf("mean luma moved: %.6f -> %.6f", offMean, onMean)
	}
}

func TestGrainLeavesISO100BitIdentical(t *testing.T) {
	oldGrain := dnGrain
	defer func() { dnGrain = oldGrain }()

	const w, h = 96, 64
	run := func(g float64) *CamImage {
		dnGrain = g
		img := grainFrame(w, h, 0.002)
		Denoise(img, DenoiseOptions{Strength: 0.3, Scale: 1, ISO: 100, NoiseAware: true})
		return img
	}
	a, b := run(0), run(0.7)
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			t.Fatalf("ISO 100 moved at sample %d: %g != %g", i, a.Pix[i], b.Pix[i])
		}
	}
}
