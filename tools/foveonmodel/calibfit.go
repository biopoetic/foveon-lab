package main

// CALIBFIT=1 [MEASURED=<json>] foveonmodel photo.X3F
//
// Fits the layer sensitivities to ALL of the camera's own calibration
// (CAMF): for each named light (Sunlight, Overcast, Shade, Flash,
// Incandescent; Fluorescent is left out — its spiky spectrum is unknown),
//   - its colour matrix, applied to the modelled layers balanced for that
//     light, against what the observer sees under it (as errCC), and
//   - how its white-balance gains differ from Sunlight's, per layer
//     relative to the middle layer (ratios cancel each layer's own fixed
//     amplification).
// The lights are taken as black bodies at their usual temperatures. Two
// models: the depth model (hard zone boundaries) and a soft one, where
// each boundary is blurred over a fitted depth (carriers diffuse). With
// MEASURED (curves of the same generation) the fits are compared with the
// measurement — the check that the method recovers a real sensor.

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"sort"

	"github.com/biopoetic/foveon-lab/x3f"
)

type calLight struct {
	name string
	T    float64
}

var calLights = []calLight{{"Sunlight", 5500}, {"Overcast", 6500}, {"Shade", 7500}, {"Flash", 6000}, {"Incandescent", 2856}}

// softSens: collection zones with soft (logistic in depth) boundaries.
// p: d0 dead layer, b1, b2, b3 boundaries (µm), s1, s2, s3 softness (µm),
// lc, w IR edge.
func softSens(p []float64) [][3]float64 {
	d0, b1, b2, b3 := p[0], p[1], p[2], p[3]
	s1, s2, s3 := p[4], p[5], p[6]
	lc, w := p[7], p[8]
	// Collection weight of each layer as a function of depth z.
	const nz = 400
	zmax := b3 + 6*s3 + 2
	dz := zmax / nz
	sigm := func(z, b, s float64) float64 { return 1 / (1 + math.Exp(-(z-b)/s)) }
	S := make([][3]float64, len(lams))
	for i, l := range lams {
		a := alpha[i]
		ir := 1 / (1 + math.Exp((l-lc)/w))
		var r [3]float64
		for j := 0; j < nz; j++ {
			z := (float64(j) + 0.5) * dz
			if z < d0 {
				continue
			}
			absorbed := a * math.Exp(-a*z) * dz // photons absorbed in [z, z+dz]
			top := 1 - sigm(z, b1, s1)
			mid := sigm(z, b1, s1) * (1 - sigm(z, b2, s2))
			bot := sigm(z, b2, s2) * (1 - sigm(z, b3, s3))
			r[0] += bot * absorbed
			r[1] += mid * absorbed
			r[2] += top * absorbed
		}
		for k := 0; k < 3; k++ {
			S[i][k] = ir * r[k]
		}
	}
	return S
}

func calibFitMain(c *x3f.CAMF, gen string) {
	type obs struct {
		sc   *scene
		cc   [9]float64
		gain [3]float64
	}
	var o []obs
	var sun [3]float64
	for _, L := range calLights {
		g, cc, err := c.WBTransform(L.name)
		if err != nil {
			log.Printf("%s: %v (skipped)", L.name, err)
			continue
		}
		if L.name == "Sunlight" {
			sun = g
		}
		o = append(o, obs{newScene(L.T, 400, 1), cc, g})
	}
	white := func(S [][3]float64, sc *scene) [3]float64 {
		var w [3]float64
		for i := range lams {
			for k := 0; k < 3; k++ {
				w[k] += S[i][k] * sc.illum[i]
			}
		}
		return w
	}
	// objective: mean matrix error + gain-ratio error (log units, weighted
	// so a 1% ratio miss ~ a 0.01 matrix RMS).
	sunScene := newScene(5500, 400, 1)
	objS := func(S [][3]float64) (float64, float64, float64) {
		var em, eg float64
		ws := white(S, sunScene)
		for _, ob := range o {
			em += ob.sc.errCCS(S, ob.cc)
			w := white(S, ob.sc)
			for _, k := range []int{0, 2} {
				model := math.Log((w[k] / ws[k]) / (w[1] / ws[1]))
				camf := math.Log((sun[k] / ob.gain[k]) / (sun[1] / ob.gain[1]))
				d := model - camf
				eg += d * d
			}
		}
		em /= float64(len(o))
		eg = math.Sqrt(eg / float64(2*len(o)))
		return em + eg, em, eg
	}
	var meas [][3]float64
	if mp := os.Getenv("MEASURED"); mp != "" {
		var d map[string][][2]float64
		b, err := os.ReadFile(mp)
		if err != nil {
			log.Fatal(err)
		}
		json.Unmarshal(b, &d)
		at := func(pts [][2]float64, l float64) float64 {
			sort.Slice(pts, func(i, j int) bool { return pts[i][0] < pts[j][0] })
			if l < pts[0][0] || l > pts[len(pts)-1][0] {
				return 0
			}
			for i := 1; i < len(pts); i++ {
				if pts[i][0] >= l {
					f := (l - pts[i-1][0]) / (pts[i][0] - pts[i-1][0])
					return pts[i-1][1] + f*(pts[i][1]-pts[i-1][1])
				}
			}
			return pts[len(pts)-1][1]
		}
		meas = make([][3]float64, len(lams))
		for i, l := range lams {
			meas[i] = [3]float64{at(d["bottom"], l), at(d["middle"], l), at(d["top"], l)}
		}
	}
	// vsMeas: RMS of peak-normalised curves vs the measurement over its
	// range (400-680 nm), per layer.
	vsMeas := func(S [][3]float64) [3]float64 {
		var out [3]float64
		if meas == nil {
			return out
		}
		for k := 0; k < 3; k++ {
			var ms, mm, e2, n float64
			for i, l := range lams {
				if l < 400 || l > 680 {
					continue
				}
				ms, mm = math.Max(ms, S[i][k]), math.Max(mm, meas[i][k])
			}
			for i, l := range lams {
				if l < 400 || l > 680 {
					continue
				}
				d := S[i][k]/ms - meas[i][k]/mm
				e2, n = e2+d*d, n+1
			}
			out[k] = math.Sqrt(e2 / n)
		}
		return out
	}
	report := func(name string, S [][3]float64) {
		tot, em, eg := objS(S)
		v := vsMeas(S)
		fmt.Printf("%-34s objective %.4f (matrices %.4f, gain ratios %.4f)", name, tot, em, eg)
		if meas != nil {
			fmt.Printf(" | vs measured: bottom %.3f middle %.3f top %.3f", v[0], v[1], v[2])
		}
		fmt.Println()
	}
	// The current depth model of this generation (Sunlight matrix only).
	cur := loadModel(gen)
	cs := make([][3]float64, len(lams))
	for i := range lams {
		cs[i] = cur.sens(i)
	}
	report("current depth model (Sunlight only)", cs)
	if meas != nil {
		report("measured", meas)
	}
	// Depth model refitted on everything.
	clampD := func(p []float64) model {
		d0 := math.Exp(p[0])
		d1 := d0 + math.Exp(p[1])
		d2 := d1 + math.Exp(p[2])
		d3 := d2 + math.Exp(p[3])
		return model{d1: d1, d2: d2, d3: d3, lc: p[4], w: math.Exp(p[5]), d0: d0}
	}
	toS := func(m model) [][3]float64 {
		S := make([][3]float64, len(lams))
		for i := range lams {
			S[i] = m.sens(i)
		}
		return S
	}
	rng := rand.New(rand.NewSource(7))
	bestD, bv := []float64(nil), math.Inf(1)
	for st := 0; st < 30; st++ {
		x0 := []float64{math.Log(0.01 + 0.15*rng.Float64()), math.Log(0.1 + 0.6*rng.Float64()), math.Log(0.2 + 1.5*rng.Float64()),
			math.Log(0.5 + 5*rng.Float64()), 640 + 60*rng.Float64(), math.Log(1 + 20*rng.Float64())}
		p, v := nelderMead(func(p []float64) float64 {
			m := clampD(p)
			if m.d3 > 20 || m.lc < 600 || m.lc > 760 || m.w > 60 {
				return 1e9
			}
			t, _, _ := objS(toS(m))
			return t
		}, x0, []float64{0.5, 0.4, 0.4, 0.4, 15, 0.4}, 600)
		if v < bv {
			bestD, bv = p, v
		}
	}
	md := clampD(bestD)
	report(fmt.Sprintf("depth model, all lights (%.2f/%.2f/%.2f/%.2f µm, IR %.0f)", md.d0, md.d1, md.d2, md.d3, md.lc), toS(md))
	// Soft-boundary model on everything.
	clampS := func(p []float64) []float64 {
		d0 := math.Exp(p[0])
		b1 := d0 + math.Exp(p[1])
		b2 := b1 + math.Exp(p[2])
		b3 := b2 + math.Exp(p[3])
		return []float64{d0, b1, b2, b3, math.Exp(p[4]), math.Exp(p[5]), math.Exp(p[6]), p[7], math.Exp(p[8])}
	}
	bestS, bs := []float64(nil), math.Inf(1)
	for st := 0; st < 20; st++ {
		x0 := []float64{math.Log(0.02 + 0.2*rng.Float64()), math.Log(0.2 + 0.6*rng.Float64()), math.Log(0.5 + 2*rng.Float64()),
			math.Log(1 + 6*rng.Float64()), math.Log(0.02 + 0.3*rng.Float64()), math.Log(0.05 + 0.6*rng.Float64()), math.Log(0.2 + 2*rng.Float64()),
			630 + 50*rng.Float64(), math.Log(1 + 15*rng.Float64())}
		p, v := nelderMead(func(p []float64) float64 {
			q := clampS(p)
			if q[3] > 20 || q[7] < 600 || q[7] > 760 || q[8] > 60 || q[4] > 3 || q[5] > 3 || q[6] > 6 {
				return 1e9
			}
			t, _, _ := objS(softSens(q))
			return t
		}, x0, []float64{0.4, 0.3, 0.3, 0.3, 0.4, 0.4, 0.4, 12, 0.4}, 700)
		if v < bs {
			bestS, bs = p, v
		}
	}
	q := clampS(bestS)
	report(fmt.Sprintf("soft model, all lights (%.2f/%.2f/%.2f/%.2f µm, soft %.2f/%.2f/%.2f, IR %.0f)", q[0], q[1], q[2], q[3], q[4], q[5], q[6], q[7]), softSens(q))
	if out := os.Getenv("OUT"); out != "" {
		var rows [][4]float64
		S := softSens(q)
		for i, l := range lams {
			rows = append(rows, [4]float64{l, S[i][0], S[i][1], S[i][2]})
		}
		b, _ := json.Marshal(map[string]any{"soft": q, "depth": []float64{md.d0, md.d1, md.d2, md.d3, md.lc, md.w}, "curves": rows})
		if err := os.WriteFile(out, b, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}
