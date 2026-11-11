package main

// A short test on synthetic pairs: the decomposition must improve down the
// ladder, a known exposure error must land in +gains, and a known hue twist
// must land in +own matrix.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/render"
)

func curveT(v float64) float64 {
	v = math.Max(0, math.Min(1, v))
	return math.Pow(v, 0.85)
}

var synthM = [9]float64{0.90, 0.06, 0.02, 0.03, 0.94, 0.03, 0.02, 0.05, 0.90}

func synthPhoto(name string, i int) *Photo {
	const w, h = 48, 32
	raw := &render.Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	gain := [3]float64{1 + 0.03*float64(i), 1, 1 - 0.02*float64(i)}
	spp := &render.Linear{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		fy := float64(y) / float64(h-1)
		for x := 0; x < w; x++ {
			fx := float64(x) / float64(w-1)
			r := 0.04 + 0.55*fx
			g := 0.04 + 0.55*fy
			b := 0.04 + 0.55*(0.5+0.5*math.Sin(3*fx+fy+float64(i)))
			j := (y*w + x) * 3
			raw.Pix[j], raw.Pix[j+1], raw.Pix[j+2] = float32(r), float32(g), float32(b)
			// The photograph's own channel balance, then the group's matrix,
			// then SPP's per-channel tone.
			c := gain
			vr := synthM[0]*r*c[0] + synthM[1]*g*c[1] + synthM[2]*b*c[2]
			vg := synthM[3]*r*c[0] + synthM[4]*g*c[1] + synthM[5]*b*c[2]
			vb := synthM[6]*r*c[0] + synthM[7]*g*c[1] + synthM[8]*b*c[2]
			spp.Pix[j], spp.Pix[j+1], spp.Pix[j+2] = float32(curveT(vr)), float32(curveT(vg)), float32(curveT(vb))
		}
	}
	return &Photo{Name: name, Raw: raw, SPP: spp, Body: "x", ISO: 100}
}

func TestLadderOnSyntheticPairs(t *testing.T) {
	n := 6
	ps := make([]*Photo, n)
	for i := range ps {
		ps[i] = synthPhoto("syn"+string(rune('a'+i)), i)
	}
	folds := buildFolds(ps, func(string, ...any) {})
	for h := range ps {
		r := ladder(ps[h], folds[h], nil)
		if r.EOwn > r.EOwnCalib+0.01 {
			t.Fatalf("%s: refined own fit %.2f worse than calib's %.2f", ps[h].Name, r.EOwn, r.EOwnCalib)
		}
		if r.E0 < r.EExp || r.EExp < r.EGains {
			t.Fatalf("%s: exposure/gains rungs not monotone: %.2f %.2f %.2f", ps[h].Name, r.E0, r.EExp, r.EGains)
		}
		for _, v := range []float64{r.E0, r.EExp, r.EGains, r.EMatrix, r.ECurves, r.EOwn} {
			if math.IsNaN(v) || v < 0 {
				t.Fatalf("%s: bad rung %v", ps[h].Name, v)
			}
		}
	}
}

func TestBlueGainGate(t *testing.T) {
	ps := make([]*Photo, 6)
	for i := range ps {
		ps[i] = synthPhoto("syn"+string(rune('a'+i)), i)
	}
	folds := buildFolds(ps, func(string, ...any) {})
	h := 2
	base := ladder(ps[h], folds[h], nil)

	blue := &Photo{Name: ps[h].Name, Raw: &render.Linear{W: ps[h].Raw.W, H: ps[h].Raw.H,
		Pix: append([]float32(nil), ps[h].Raw.Pix...)}, SPP: ps[h].SPP}
	for i := 2; i < len(blue.Raw.Pix); i += 3 {
		blue.Raw.Pix[i] *= float32(math.Exp2(0.3))
	}
	r := ladder(blue, folds[h], nil)
	dE0 := r.E0 - base.E0
	t.Logf("baseline %.2f %.2f %.2f %.2f; blue %.2f %.2f %.2f %.2f; B-G %.3f -> %.3f",
		base.E0, base.EExp, base.EGains, base.EOwn, r.E0, r.EExp, r.EGains, r.EOwn, base.LG[2]-base.LG[1], r.LG[2]-r.LG[1])
	if dE0 < 0.3 {
		t.Fatalf("blue gain raised held-out error by only %.2f; it must be visible", dE0)
	}
	if d := r.EGains - base.EGains; math.Abs(d) > 0.25*dE0+0.05 {
		t.Fatalf("+gains left %.2f of a known blue gain that raised held-out error by %.2f", d, dE0)
	}
	if d := (r.LG[2] - r.LG[1]) - (base.LG[2] - base.LG[1]); math.Abs(d+0.3) > 0.12 {
		t.Fatalf("ideal B-G moved %.3f stops, want -0.30", d)
	}
	if d := r.EOwn - base.EOwn; math.Abs(d) > 0.25*dE0+0.05 {
		t.Fatalf("own fit left %.2f of a known blue gain that raised held-out error by %.2f", d, dE0)
	}
}

func TestHueTwistGate(t *testing.T) {
	ps := make([]*Photo, 6)
	for i := range ps {
		ps[i] = synthPhoto("syn"+string(rune('a'+i)), i)
	}
	folds := buildFolds(ps, func(string, ...any) {})
	h := 3
	base := ladder(ps[h], folds[h], nil)

	hue := &Photo{Name: ps[h].Name, Raw: &render.Linear{W: ps[h].Raw.W, H: ps[h].Raw.H,
		Pix: append([]float32(nil), ps[h].Raw.Pix...)}, SPP: ps[h].SPP}
	const ang = 8 * math.Pi / 180
	c, s := math.Cos(ang), math.Sin(ang)
	for i := 0; i < len(hue.Raw.Pix); i += 3 {
		r, g := float64(hue.Raw.Pix[i]), float64(hue.Raw.Pix[i+1])
		hue.Raw.Pix[i] = float32(c*r + s*g)
		hue.Raw.Pix[i+1] = float32(-s*r + c*g)
	}
	r := ladder(hue, folds[h], nil)
	if d := r.E0 - base.E0; d < 0.3 {
		t.Fatalf("hue twist raised held-out error by only %.2f; it must be visible", d)
	}
	if d := r.EMatrix - base.EMatrix; math.Abs(d) > 0.15 {
		t.Fatalf("own matrix left %.2f of the known hue twist", d)
	}
	if d := r.EGains - base.EGains; d < 0.2 {
		t.Fatalf("+gains absorbed the hue twist (delta %.2f); it must not", d)
	}
}
