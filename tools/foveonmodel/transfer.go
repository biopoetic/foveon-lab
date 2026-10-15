package main

// TRANSFER=1 GEN=<target> foveonmodel target.X3F
//
// Carries silicon physics measured on one generation to another:
//  1. the soft-boundary model (softSens) is fitted to the MEASURED Merrill
//     curves (render/layersens.json): dead layer, zone boundaries, how soft
//     each boundary is (carrier diffusion), IR edge;
//  2. for the target generation, only its zone depths and IR edge are
//     fitted — to its own camera calibration (calibration objective, all
//     lights) — with the softness and dead layer kept from Merrill;
//  3. if the target has its own measurement (SD9: the F7), the prediction
//     is compared with it, next to the target's current depth model.
// OUT writes the transferred curves.

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"

	"github.com/biopoetic/foveon-lab/x3f"
)

func measuredGrid(gen string) [][3]float64 {
	var d map[string]struct {
		Curves [][4]float64 `json:"curves"`
	}
	b, err := os.ReadFile("render/layersens.json")
	if err != nil {
		log.Fatal(err)
	}
	json.Unmarshal(b, &d)
	g, ok := d[gen]
	if !ok || len(g.Curves) != len(lams) {
		return nil
	}
	S := make([][3]float64, len(lams))
	for i, r := range g.Curves {
		S[i] = [3]float64{r[1], r[2], r[3]}
	}
	return S
}

// shapeErr: per-layer RMS of peak-normalised curves over 400-680 nm.
func shapeErr(S, M [][3]float64) [3]float64 {
	var out [3]float64
	for k := 0; k < 3; k++ {
		var ms, mm, e2, n float64
		for i, l := range lams {
			if l >= 400 && l <= 680 {
				ms, mm = math.Max(ms, S[i][k]), math.Max(mm, M[i][k])
			}
		}
		if ms == 0 || mm == 0 {
			out[k] = math.NaN()
			continue
		}
		for i, l := range lams {
			if l >= 400 && l <= 680 {
				d := S[i][k]/ms - M[i][k]/mm
				e2, n = e2+d*d, n+1
			}
		}
		out[k] = math.Sqrt(e2 / n)
	}
	return out
}

func transferMain(c *x3f.CAMF, gen string) {
	merrill := measuredGrid("Merrill")
	if merrill == nil {
		log.Fatal("no measured Merrill curves")
	}
	rng := rand.New(rand.NewSource(11))
	clampS := func(p []float64) []float64 {
		d0 := math.Exp(p[0])
		b1 := d0 + math.Exp(p[1])
		b2 := b1 + math.Exp(p[2])
		b3 := b2 + math.Exp(p[3])
		return []float64{d0, b1, b2, b3, math.Exp(p[4]), math.Exp(p[5]), math.Exp(p[6]), p[7], math.Exp(p[8])}
	}
	sum := func(e [3]float64) float64 { return e[0] + e[1] + e[2] }
	// 1. Physics from the Merrill measurement.
	best, bv := []float64(nil), math.Inf(1)
	for st := 0; st < 25; st++ {
		x0 := []float64{math.Log(0.02 + 0.3*rng.Float64()), math.Log(0.2 + 0.8*rng.Float64()), math.Log(0.5 + 3*rng.Float64()),
			math.Log(1 + 8*rng.Float64()), math.Log(0.02 + 0.5*rng.Float64()), math.Log(0.05 + 0.8*rng.Float64()), math.Log(0.2 + 3*rng.Float64()),
			640 + 30*rng.Float64(), math.Log(1 + 10*rng.Float64())}
		p, v := nelderMead(func(p []float64) float64 {
			q := clampS(p)
			if q[3] > 25 || q[7] < 600 || q[7] > 720 || q[8] > 60 || q[4] > 4 || q[5] > 4 || q[6] > 8 {
				return 1e9
			}
			return sum(shapeErr(softSens(q), merrill))
		}, x0, []float64{0.4, 0.3, 0.3, 0.3, 0.4, 0.4, 0.4, 10, 0.4}, 900)
		if v < bv {
			best, bv = p, v
		}
	}
	phys := clampS(best)
	e := shapeErr(softSens(phys), merrill)
	fmt.Printf("1. soft model fitted to the MEASURED Merrill: dead %.2f µm, boundaries %.2f/%.2f/%.2f µm, softness %.2f/%.2f/%.2f µm, IR %.0f nm (w %.1f)\n",
		phys[0], phys[1], phys[2], phys[3], phys[4], phys[5], phys[6], phys[7], phys[8])
	fmt.Printf("   shape error vs Merrill measured: bottom %.3f middle %.3f top %.3f  (depth model fitted to Sunlight: 0.233 / 0.189 / 0.139)\n", e[0], e[1], e[2])

	// 2. Target: depths + IR only, softness and dead layer from Merrill,
	// fitted to the target's own calibration.
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
			continue
		}
		if L.name == "Sunlight" {
			sun = g
		}
		o = append(o, obs{newScene(L.T, 400, 1), cc, g})
	}
	sunScene := newScene(5500, 400, 1)
	white := func(S [][3]float64, sc *scene) [3]float64 {
		var w [3]float64
		for i := range lams {
			for k := 0; k < 3; k++ {
				w[k] += S[i][k] * sc.illum[i]
			}
		}
		return w
	}
	cal := func(S [][3]float64, sunOnly bool) float64 {
		var em, eg float64
		n := 0
		ws := white(S, sunScene)
		for j, ob := range o {
			if sunOnly && calLights[j].name != "Sunlight" {
				continue
			}
			n++
			em += ob.sc.errCCS(S, ob.cc)
			if sunOnly {
				continue
			}
			w := white(S, ob.sc)
			for _, k := range []int{0, 2} {
				d := math.Log((w[k]/ws[k])/(w[1]/ws[1])) - math.Log((sun[k]/ob.gain[k])/(sun[1]/ob.gain[1]))
				eg += d * d
			}
		}
		em /= float64(n)
		if sunOnly {
			return em
		}
		return em + math.Sqrt(eg/float64(2*len(o)))
	}
	fmt.Printf("2. %s: %d calibrated lights in its CAMF\n", gen, len(o))
	target := measuredGrid(gen)
	report := func(name string, S [][3]float64) {
		fmt.Printf("   %-44s calibration %.4f", name, cal(S, false))
		if target != nil {
			t := shapeErr(S, target)
			fmt.Printf(" | vs %s measured: bottom %.3f middle %.3f top %.3f", gen, t[0], t[1], t[2])
		}
		fmt.Println()
	}
	cur := loadModel(gen)
	cs := make([][3]float64, len(lams))
	for i := range lams {
		cs[i] = cur.sens(i)
	}
	report("current depth model", cs)
	if target != nil {
		report("measured", target)
	}
	for _, sunOnly := range []bool{true, false} {
		bestT, bt := []float64(nil), math.Inf(1)
		for st := 0; st < 20; st++ {
			x0 := []float64{math.Log(phys[1]-phys[0]) + 0.3*rng.NormFloat64(), math.Log(phys[2]-phys[1]) + 0.3*rng.NormFloat64(),
				math.Log(phys[3]-phys[2]) + 0.3*rng.NormFloat64(), 620 + 60*rng.Float64(), math.Log(1 + 10*rng.Float64())}
			p, v := nelderMead(func(p []float64) float64 {
				b1 := phys[0] + math.Exp(p[0])
				b2 := b1 + math.Exp(p[1])
				b3 := b2 + math.Exp(p[2])
				if b3 > 25 || p[3] < 600 || p[3] > 760 || math.Exp(p[4]) > 60 {
					return 1e9
				}
				return cal(softSens([]float64{phys[0], b1, b2, b3, phys[4], phys[5], phys[6], p[3], math.Exp(p[4])}), sunOnly)
			}, x0, []float64{0.3, 0.3, 0.3, 12, 0.4}, 600)
			if v < bt {
				bestT, bt = p, v
			}
		}
		b1 := phys[0] + math.Exp(bestT[0])
		b2 := b1 + math.Exp(bestT[1])
		b3 := b2 + math.Exp(bestT[2])
		q := []float64{phys[0], b1, b2, b3, phys[4], phys[5], phys[6], bestT[3], math.Exp(bestT[4])}
		label := "transferred physics, depths fitted (all lights)"
		if sunOnly {
			label = "transferred physics, depths fitted (Sunlight)"
		}
		report(fmt.Sprintf("%s %.2f/%.2f/%.2f IR %.0f", label, b1, b2, b3, q[7]), softSens(q))
		if out := os.Getenv("OUT"); out != "" && !sunOnly {
			var rows [][4]float64
			S := softSens(q)
			for i, l := range lams {
				rows = append(rows, [4]float64{l, S[i][0], S[i][1], S[i][2]})
			}
			b, _ := json.Marshal(map[string]any{"params": q, "physics": phys, "curves": rows})
			os.WriteFile(out, b, 0o644)
		}
	}
}
