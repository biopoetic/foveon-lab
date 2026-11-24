package main

import (
	"bytes"
	"image"
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

// t92RGBAFloats turns a rendered image into the sRGB-byte float space the
// study's edge and error measures work in.
func t92RGBAFloats(img *image.RGBA) []float32 {
	out := make([]float32, img.Rect.Dx()*img.Rect.Dy()*3)
	for i := 0; i < len(out)/3; i++ {
		out[i*3], out[i*3+1], out[i*3+2] = float32(img.Pix[i*4]), float32(img.Pix[i*4+1]), float32(img.Pix[i*4+2])
	}
	return out
}

// t92SyntheticLinear is a strong vertical step at a reduced-view scale, in
// the shape of a developed Quattro frame.
func t92SyntheticLinear(w, h int) *render.Linear {
	l := &render.Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 0.22, Frame: 4704, NativeFrame: 4704, Raw: true, ISO: 100, Sensor: "Quattro"}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(0.02)
			if x >= w/2 {
				v = 0.6
			}
			i := (y*w + x) * 3
			l.Pix[i], l.Pix[i+1], l.Pix[i+2] = v, v, v
		}
	}
	return l
}

func t92RenderSynthetic(t *testing.T, l *render.Linear) []float32 {
	t.Helper()
	img := render.Render(render.NewBase(l), preset.Neutral(), render.Compensation{})
	return t92RGBAFloats(img)
}

// The research hook must be inert when unset, and the deliberately wrong
// radius (3.0) must move the picture, raise the measured overshoot and be
// farther from the current render than a smaller radius is. That is the
// check that the study can tell a good radius from a bad one.
func TestT92RadiusHookAndFailureCheck(t *testing.T) {
	const w, h = 192, 64
	l := t92SyntheticLinear(w, h)

	render.SetSharpResearch(render.SharpResearch{})
	off := t92RenderSynthetic(t, l)
	render.SetSharpResearch(render.SharpResearch{Sigma: 1.0})
	one := t92RenderSynthetic(t, l)
	render.SetSharpResearch(render.SharpResearch{Sigma: 0.6})
	soft := t92RenderSynthetic(t, l)
	render.SetSharpResearch(render.SharpResearch{Sigma: 3.0})
	wrong := t92RenderSynthetic(t, l)
	render.SetSharpResearch(render.SharpResearch{})

	if !bytes.Equal(t90F32Bytes(off), t90F32Bytes(one)) {
		t.Fatalf("the hook at the shipped radius 1.0 changed the render")
	}
	o1, _ := edgeProfileStats(one, one, w, h)
	os, _ := edgeProfileStats(soft, soft, w, h)
	ow, _ := edgeProfileStats(wrong, wrong, w, h)
	if o1.N == 0 {
		t.Fatal("no edges found in the synthetic step")
	}
	t.Logf("synthetic edges n=%d overshoot one %.3f%% soft %.3f%% wrong %.3f%%", o1.N, 100*o1.Overshoot, 100*os.Overshoot, 100*ow.Overshoot)
	eWrong := meanAbs8(wrong, one)
	eSoft := meanAbs8(soft, one)
	if eWrong < 0.02 {
		t.Errorf("the deliberately wrong radius 3.0 barely moved the render: mean abs %.3f", eWrong)
	}
	if !(eWrong > 2*eSoft) {
		t.Errorf("radius 3.0 is not clearly farther from the current render than 0.6: %.3f vs %.3f", eWrong, eSoft)
	}
	if !(ow.Overshoot > o1.Overshoot+0.002) {
		t.Errorf("radius 3.0 did not raise the measured overshoot: %.2f%% vs %.2f%%", 100*ow.Overshoot, 100*o1.Overshoot)
	}
	if !(os.Overshoot < ow.Overshoot) {
		t.Errorf("radius 0.6 rings as much as 3.0: %.2f%% vs %.2f%%", 100*os.Overshoot, 100*ow.Overshoot)
	}
}

// t90F32Bytes copies a float32 slice as bytes, for a bit-identical comparison.
func t90F32Bytes(v []float32) []byte {
	out := make([]byte, len(v)*4)
	for i, x := range v {
		u := math.Float32bits(x)
		out[i*4], out[i*4+1], out[i*4+2], out[i*4+3] = byte(u), byte(u>>8), byte(u>>16), byte(u>>24)
	}
	return out
}

// At the 1200 px scored frame the fitted view kernel is the sigma-0.5 floor
// for every full-size radius, and the radius only scales the fitted amount by
// radius squared. This is the study's diagnosis, pinned so a future change to
// sppView cannot silently invalidate it.
func TestT92ViewRadiusIsAmount(t *testing.T) {
	for _, sensor := range []string{"Quattro", "DPM", "Merrill"} {
		sigma0 := 1.0
		if sensor == "DPM" {
			sigma0 = 0.7 // the shipped DPM sigma
		}
		render.SetSharpResearch(render.SharpResearch{})
		a1, k1 := render.SPPViewMask(0.22, 100, sensor)
		if a1 <= 0 {
			t.Fatalf("%s: no view amount", sensor)
		}
		prev := 0.0
		for _, rad := range []float64{0.6, 0.8, 1.0, 1.2, 1.5} {
			render.SetSharpResearch(render.SharpResearch{Sigma: rad})
			a, k := render.SPPViewMask(0.22, 100, sensor)
			render.SetSharpResearch(render.SharpResearch{})
			if k != k1 {
				t.Errorf("%s r%.1f: view kernel changed with the full-size radius", sensor, rad)
			}
			if a1 > 0 && a <= prev {
				t.Errorf("%s r%.1f: view amount %.4f did not grow (prev %.4f)", sensor, rad, a, prev)
			}
			prev = a
			want := a1 * (rad / sigma0) * (rad / sigma0)
			// The least-squares amount is close to, but not exactly, the
			// squared ratio at the larger radii (the target response has
			// higher-order terms): 20% is the empirical envelope.
			if math.Abs(a-want) > 0.20*want {
				t.Errorf("%s r%.1f: view amount %.4f, want about %.4f (radius/sigma0 squared)", sensor, rad, a, want)
			}
		}
	}
}

// The DoG fitter must recover a known difference-of-Gaussians shape, and must
// prefer the current Gaussian when the target is the Gaussian profile (not
// always choose a new shape).
func TestT92DoGFit(t *testing.T) {
	p := t92IdealProfile(t92Gauss7(0.01), 0) // a near-ideal step
	const a0 = 0.2
	s1, lam := 0.5, 1.0
	g1, g2 := t92Gauss7(s1), t92Gauss7(s1*t92DogRatio)
	c1, c2 := t92Conv1(p, g1), t92Conv1(p, g2)
	q := make([]float64, len(p))
	for j := range q {
		q[j] = p[j] + lam*a0*(c1[j]-c2[j])
	}
	var gauss05 [7]float32
	for i, v := range t92Gauss7(0.5) {
		gauss05[i] = float32(v)
	}
	got := t92FitDoG(p, q, a0, gauss05)
	if !got.OK {
		t.Fatal("fit returned no shape")
	}
	if math.Abs(got.Sigma1-s1) > 1e-9 || math.Abs(got.Lambda-lam) > 1e-9 {
		t.Errorf("recovered sigma1 %.2f lambda %.2f, want %.2f/%.2f", got.Sigma1, got.Lambda, s1, lam)
	}
	if !(got.TrainMAE < got.BaseMAE) {
		t.Errorf("DoG profile MAE %.6f is not better than the gauss baseline %.6f", got.TrainMAE, got.BaseMAE)
	}
	// A Gaussian target must not be improved by a DoG: the current mask is
	// already the right shape.
	var k0 [7]float64
	for i, v := range t92Gauss7(0.5) {
		k0[i] = v
	}
	c0 := t92Conv1(p, k0)
	qg := make([]float64, len(p))
	for j := range qg {
		qg[j] = p[j] + a0*(p[j]-c0[j])
	}
	same := t92FitDoG(p, qg, a0, gauss05)
	if same.OK && same.TrainMAE < same.BaseMAE*0.5 {
		t.Errorf("the fitter halved a Gaussian target's MAE with a DoG: %.6f vs %.6f", same.TrainMAE, same.BaseMAE)
	}
}

// The held-out choice must not peek: a body that prefers the strongest
// radius must not drag the choice made on the other bodies.
func TestT92HeldOutSelection(t *testing.T) {
	radii := []float64{0.6, 0.8, 1.0, 1.2, 1.5}
	bodies := []string{"a", "a", "b", "b", "c", "c"}
	res := make([]*t92Result, len(bodies))
	var errs [][]float64
	for range radii {
		errs = append(errs, make([]float64, len(bodies)))
	}
	for i, b := range bodies {
		res[i] = &t92Result{p: &t90Photo{body: b}}
		for ri := range radii {
			v := 5.0
			switch b {
			case "a":
				v = 4.0 + 0.1*float64(ri-4)*float64(ri-4)
			default:
				v = 4.0 + 0.1*float64(ri-2)*float64(ri-2)
			}
			errs[ri][i] = v
		}
	}
	cand := t92Cand(len(radii))
	var tr []int
	for i, b := range bodies {
		if b != "a" {
			tr = append(tr, i)
		}
	}
	if c := t92Pick(errs, cand, tr); c != 2 {
		t.Errorf("training on b and c chose radius %.2f, want 1.00", radii[c])
	}
	if c := t92Pick(errs, cand, []int{0, 1}); c != 4 {
		t.Errorf("body a's own best is %.2f, want 1.50", radii[c])
	}
}
