package main

// Measured layer sensitivities against the camera's own colour matrix.
//
// MEASURED=<json> GEN=<generation> [IRFIT=1] foveonmodel photo.X3F
//
// The JSON holds "bottom", "middle", "top" as [[nm, value], ...] (any
// scale: each layer is white-balanced). Measured sensitivities include
// the camera's IR filter (Merrill, NPL) or not (the F7 bare sensor, Lyon &
// Hubel 2002): IRFIT=1 fits only an IR edge in front of them. Printed:
// the RMS of the camera's Sunlight matrix on the layers vs what the
// observer sees (white = 1), on the fitted colours, held-out colours and
// a 3200 K light — next to the depth model of GEN.

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
)

func measuredMain(path, gen string, cc [9]float64, irFit bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	var d map[string][][2]float64
	if err := json.Unmarshal(b, &d); err != nil {
		log.Fatal(err)
	}
	at := func(pts [][2]float64, l float64) float64 {
		sort.Slice(pts, func(i, j int) bool { return pts[i][0] < pts[j][0] })
		n := len(pts)
		if l < pts[0][0] || l > pts[n-1][0] {
			return 0 // outside the measured range: nothing (UV/IR cut)
		}
		for i := 1; i < n; i++ {
			if pts[i][0] >= l {
				f := (l - pts[i-1][0]) / (pts[i][0] - pts[i-1][0])
				return math.Max(0, pts[i-1][1]+f*(pts[i][1]-pts[i-1][1]))
			}
		}
		return pts[n-1][1]
	}
	base := make([][3]float64, len(lams))
	for i, l := range lams {
		base[i] = [3]float64{at(d["bottom"], l), at(d["middle"], l), at(d["top"], l)}
	}
	withIR := func(lc, w float64) [][3]float64 {
		S := make([][3]float64, len(lams))
		for i, l := range lams {
			ir := 1.0
			if w > 0 {
				ir = 1 / (1 + math.Exp((l-lc)/w))
			}
			for k := 0; k < 3; k++ {
				S[i][k] = base[i][k] * ir
			}
		}
		return S
	}
	sc := newScene(5500, 400, 1)
	S := withIR(0, 0)
	if irFit {
		obj := func(p []float64) float64 {
			if p[0] < 560 || p[0] > 760 || p[1] < 0.05 || p[1] > 60 {
				return 1e9
			}
			return sc.errCCS(withIR(p[0], p[1]), cc)
		}
		best, bv := []float64(nil), math.Inf(1)
		for _, lc := range []float64{620, 650, 680, 710} {
			p, v := nelderMead(obj, []float64{lc, 10}, []float64{15, 5}, 400)
			if v < bv {
				best, bv = p, v
			}
		}
		fmt.Printf("fitted IR edge %.1f nm (width %.2f)\n", best[0], best[1])
		S = withIR(best[0], best[1])
	}
	m := loadModel(gen)
	ms := make([][3]float64, len(lams))
	for i := range lams {
		ms[i] = m.sens(i)
	}
	fmt.Printf("%-28s %10s %10s %10s\n", "", "fitted set", "held out", "3200 K")
	for _, c := range []struct {
		name string
		S    [][3]float64
	}{{"measured", S}, {"depth model (" + gen + ")", ms}} {
		h := newScene(5500, 400, 99)
		t := newScene(3200, 400, 99)
		fmt.Printf("%-28s %10.4f %10.4f %10.4f\n", c.name, sc.errCCS(c.S, cc), h.errCCS(c.S, cc), t.errCCS(c.S, cc))
	}
	// The best 3x3 for each (least squares on the fitted set), held out.
	fmt.Printf("%-28s %10s %10s %10s\n", "own best 3x3", "fitted set", "held out", "3200 K")
	for _, c := range []struct {
		name string
		S    [][3]float64
	}{{"measured", S}, {"depth model (" + gen + ")", ms}} {
		M := sc.bestMatrixS(c.S)
		h := newScene(5500, 400, 99)
		t := newScene(3200, 400, 99)
		fmt.Printf("%-28s %10.4f %10.4f %10.4f\n", c.name, sc.errCCS(c.S, M), h.errCCS(c.S, M), t.errCCS(c.S, M))
	}
	// FILTERS=1: the lens filters' layer-space matrices on the measured
	// sensitivities, written into render/layerfilters.json for gen (the
	// depth model there stays: the IR looks use it).
	if os.Getenv("FILTERS") != "" {
		path := "render/layerfilters.json"
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatal(err)
		}
		var lf map[string]map[string]json.RawMessage
		if err := json.Unmarshal(data, &lf); err != nil {
			log.Fatal(err)
		}
		mm := model{meas: S}
		F := map[string][9]float64{}
		names := make([]string, 0, len(filters))
		for n := range filters {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			M, e := sc.filterMatrix(mm, filters[n])
			for k := range M {
				M[k] = math.Round(M[k]*1e5) / 1e5
			}
			F[n] = M
			fmt.Printf("  filter %-11s matrix fit RMS %.4f\n", n, e)
		}
		fb, _ := json.Marshal(F)
		lf[gen]["filters"] = fb
		lf[gen]["fittedOn"] = json.RawMessage(`"measured sensitivities (render/layersens.json)"`)
		out, _ := json.Marshal(lf)
		if err := os.WriteFile(path, out, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	if out := os.Getenv("OUT"); out != "" {
		var rows [][4]float64
		for i, l := range lams {
			rows = append(rows, [4]float64{l, S[i][0], S[i][1], S[i][2]})
		}
		b, _ := json.Marshal(rows)
		if err := os.WriteFile(out, b, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}

// errCCS is errCC for sensitivities S on the grid.
func (s *scene) errCCS(S [][3]float64, cc [9]float64) float64 {
	var wht [3]float64
	for i := range lams {
		for k := 0; k < 3; k++ {
			wht[k] += S[i][k] * s.illum[i]
		}
	}
	var e float64
	for j, r := range s.refl {
		var L [3]float64
		for i := range lams {
			for k := 0; k < 3; k++ {
				L[k] += S[i][k] * s.illum[i] * r[i]
			}
		}
		for k := 0; k < 3; k++ {
			L[k] /= wht[k]
		}
		p := apply(cc, L)
		for k := 0; k < 3; k++ {
			dd := p[k] - s.target[j][k]
			e += dd * dd
		}
	}
	return math.Sqrt(e / float64(3*len(s.refl)))
}

// bestMatrixS is the least-squares 3x3 from S's white-balanced layers to
// what the observer sees, on s.
func (s *scene) bestMatrixS(S [][3]float64) [9]float64 {
	var wht [3]float64
	for i := range lams {
		for k := 0; k < 3; k++ {
			wht[k] += S[i][k] * s.illum[i]
		}
	}
	var ata [3][3]float64
	var atb [3][3]float64
	for j, r := range s.refl {
		var L [3]float64
		for i := range lams {
			for k := 0; k < 3; k++ {
				L[k] += S[i][k] * s.illum[i] * r[i]
			}
		}
		for k := 0; k < 3; k++ {
			L[k] /= wht[k]
		}
		for a := 0; a < 3; a++ {
			for b := 0; b < 3; b++ {
				ata[a][b] += L[a] * L[b]
				atb[a][b] += s.target[j][a] * L[b]
			}
		}
	}
	inv := inv3(ata)
	var M [9]float64
	for a := 0; a < 3; a++ {
		for b := 0; b < 3; b++ {
			for k := 0; k < 3; k++ {
				M[a*3+b] += atb[a][k] * inv[k][b]
			}
		}
	}
	return M
}
