// Package main rebuilds a spectral-sensitivity curve from the fragments of a
// maker's graph: the short 4 pt fills E-7022 (Gold 200) draws the three
// curves with, or a curve split into pieces. It takes every fragment's
// points, maps page coordinates to wavelength and log sensitivity, and joins
// them into one upper envelope per layer. A clean single-path curve is just a
// one-fragment input, so the same code reads both forms.
//
// The method was proved against curves already in render/colorfilms.json
// (Ektar 100) and against Portra 400's own vector path cut into Gold-style
// fragments; see the test data and sensfit_test.go.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

type axis struct {
	PX  [2]float64 `json:"px"`
	Val [2]float64 `json:"val"`
}

type layer struct {
	Fragments [][][2]float64 `json:"fragments"`
}

type input struct {
	Film   string                  `json:"film"`
	Note   string                  `json:"note"`
	X      axis                    `json:"x"`
	Y      axis                    `json:"y"`
	Layers map[string]layer        `json:"layers"`
	Known  map[string][][2]float64 `json:"known,omitempty"`
}

type point struct{ X, Y float64 }

func (a axis) at(px float64) float64 {
	return a.Val[0] + (px-a.PX[0])/(a.PX[1]-a.PX[0])*(a.Val[1]-a.Val[0])
}

// Reconstruct joins the fragments of one layer into a curve at step spacing
// in the x axis. Where fragments overlap, the upper edge wins (a filled
// sensitivity band's lower outline never rises above its top).
func Reconstruct(frags [][][2]float64, ax, ay axis, step float64) []point {
	type bin struct {
		y float64
	}
	bins := map[int]*bin{}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, f := range frags {
		for _, p := range f {
			x := ax.at(p[0])
			if x < ax.Val[0] || x > ax.Val[1] {
				continue
			}
			y := ay.at(p[1])
			if y < ay.Val[0] || y > ay.Val[1] {
				continue
			}
			k := int(math.Round((x - ax.Val[0]) / step))
			if b, ok := bins[k]; ok {
				if y > b.y {
					b.y = y
				}
			} else {
				bins[k] = &bin{y: y}
			}
			if x < lo {
				lo = x
			}
			if x > hi {
				hi = x
			}
		}
	}
	if len(bins) < 2 || math.IsInf(lo, 1) {
		return nil
	}
	// Sorted filled bins; gaps inside the drawn range are interpolated from
	// the two neighbours, which is what joining the segment endpoints does.
	ks := make([]int, 0, len(bins))
	for k := range bins {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	k0, k1 := ks[0], ks[len(ks)-1]
	out := make([]point, 0, k1-k0+1)
	for k := k0; k <= k1; k++ {
		if b, ok := bins[k]; ok {
			out = append(out, point{ax.Val[0] + float64(k)*step, b.y})
			continue
		}
		// nearest filled neighbours
		ia := sort.SearchInts(ks, k)
		ka, kb := ks[ia-1], ks[ia]
		ya, yb := bins[ka].y, bins[kb].y
		t := float64(k-ka) / float64(kb-ka)
		out = append(out, point{ax.Val[0] + float64(k)*step, ya + t*(yb-ya)})
	}
	// Only return the drawn range (not the outer bin half-widths).
	for len(out) > 0 && out[0].X < lo {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1].X > hi {
		out = out[:len(out)-1]
	}
	return out
}

type stats struct {
	RMS, Max, PeakGot, PeakKnown, PeakDiff float64
	N                                      int
}

// Compare samples got at the known curve's wavelengths and reports the
// log-sensitivity error and the two peaks.
func Compare(got, known []point) stats {
	var s stats
	if len(got) == 0 || len(known) == 0 {
		return s
	}
	gx := make([]float64, len(got))
	gy := make([]float64, len(got))
	for i, p := range got {
		gx[i], gy[i] = p.X, p.Y
	}
	var e2, emax float64
	for _, k := range known {
		if k.X < gx[0] || k.X > gx[len(gx)-1] {
			continue
		}
		y := interp(gx, gy, k.X)
		e := y - k.Y
		e2 += e * e
		if math.Abs(e) > math.Abs(emax) {
			emax = e
		}
		s.N++
	}
	if s.N == 0 {
		return s
	}
	s.RMS = math.Sqrt(e2 / float64(s.N))
	s.Max = emax
	pg, pk := 0.0, 0.0
	for _, p := range got {
		if p.Y > pg {
			pg, s.PeakGot = p.Y, p.X
		}
	}
	for _, p := range known {
		if p.Y > pk {
			pk, s.PeakKnown = p.Y, p.X
		}
	}
	s.PeakDiff = math.Abs(s.PeakGot - s.PeakKnown)
	return s
}

func interp(x, y []float64, v float64) float64 {
	i := sort.SearchFloat64s(x, v)
	if i <= 0 {
		return y[0]
	}
	if i >= len(x) {
		return y[len(y)-1]
	}
	if x[i] == v {
		return y[i]
	}
	t := (v - x[i-1]) / (x[i] - x[i-1])
	return y[i-1] + t*(y[i]-y[i-1])
}

func load(path string) (*input, error) {
	js, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var in input
	if err := json.Unmarshal(js, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

func main() {
	args := os.Args[1:]
	step, out := 1.0, ""
	validate := false
	for len(args) > 0 && args[0][0] == '-' {
		switch args[0] {
		case "-step":
			fmt.Sscanf(args[1], "%g", &step)
			args = args[2:]
		case "-out":
			out = args[1]
			args = args[2:]
		case "-validate":
			validate = true
			args = args[1:]
		default:
			fmt.Fprintf(os.Stderr, "sensfit: unknown flag %s\n", args[0])
			os.Exit(2)
		}
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: sensfit [-step nm] [-out curves.json] [-validate] fragments.json")
		os.Exit(2)
	}
	in, err := load(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "sensfit:", err)
		os.Exit(1)
	}
	res := map[string][][2]float64{}
	bad := false
	names := make([]string, 0, len(in.Layers))
	for k := range in.Layers {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		c := Reconstruct(in.Layers[name].Fragments, in.X, in.Y, step)
		pts := make([][2]float64, len(c))
		for i, p := range c {
			pts[i] = [2]float64{math.Round(p.X*10) / 10, math.Round(p.Y*10000) / 10000}
		}
		res[name] = pts
		fmt.Printf("%-8s %d points, %.0f..%.0f nm", name, len(c), c[0].X, c[len(c)-1].X)
		pk, peak := 0.0, -math.MaxFloat64
		for _, p := range c {
			if p.Y > peak {
				peak, pk = p.Y, p.X
			}
		}
		fmt.Printf(", peak %.0f nm at %.3f", pk, peak)
		if k, ok := in.Known[name]; ok {
			known := make([]point, len(k))
			for i, p := range k {
				known[i] = point{p[0], p[1]}
			}
			s := Compare(c, known)
			fmt.Printf("; known RMS %.4f (max %.3f), peak %.0f nm (diff %.0f)",
				s.RMS, s.Max, s.PeakKnown, s.PeakDiff)
			if s.RMS > 0.05 || s.PeakDiff > 5 {
				bad = true
			}
		}
		fmt.Println()
	}
	if out != "" {
		js, _ := json.MarshalIndent(res, "", " ")
		if err := os.WriteFile(out, append(js, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "sensfit:", err)
			os.Exit(1)
		}
	}
	if validate && bad {
		os.Exit(1)
	}
}
