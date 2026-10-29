package main

import (
	"math"
	"testing"
)

func knownPoints(ps [][2]float64) []point {
	out := make([]point, len(ps))
	for i, p := range ps {
		out[i] = point{p[0], p[1]}
	}
	return out
}

// The validation the task asks for: draw a curve that is already known as
// 4 pt segment fills, read it back, and compare. Ektar 100's stored clean
// curves are the known answer.
func TestEktarColumns(t *testing.T) {
	in, err := load("testdata/ektar_columns.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, k := range in.Known {
		got := Reconstruct(in.Layers[name].Fragments, in.X, in.Y, 1)
		s := Compare(got, knownPoints(k))
		t.Logf("Ektar 100 %s: %d points, RMS %.4f, max %.3f, peak %.0f (known %.0f)",
			name, len(got), s.RMS, s.Max, s.PeakGot, s.PeakKnown)
		if s.N < 10 || s.RMS > 0.05 || s.PeakDiff > 5 {
			t.Errorf("Ektar 100 %s: RMS %.4f over %d points, peak diff %.0f", name, s.RMS, s.N, s.PeakDiff)
		}
	}
}

// A second proof on a real page: Portra 400's own vector path, cut into
// Gold-style 4 pt pieces. The uncut path is the known answer.
func TestPortra400Fragments(t *testing.T) {
	in, err := load("testdata/portra400_fragments.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, k := range in.Known {
		got := Reconstruct(in.Layers[name].Fragments, in.X, in.Y, 1)
		s := Compare(got, knownPoints(k))
		t.Logf("Portra 400 %s: %d points, RMS %.4f, max %.3f, peak %.0f (known %.0f)",
			name, len(got), s.RMS, s.Max, s.PeakGot, s.PeakKnown)
		if s.N < 10 || s.RMS > 0.05 || s.PeakDiff > 5 {
			t.Errorf("Portra 400 %s: RMS %.4f over %d points, peak diff %.0f", name, s.RMS, s.N, s.PeakDiff)
		}
	}
}

// Gold 200's page is the fragmented one: 4 pt open fills, three runs. The
// rebuilt curves must land on the printed peaks and span the drawn range.
func TestGoldSegments(t *testing.T) {
	in, err := load("testdata/gold200_segments.json")
	if err != nil {
		t.Fatal(err)
	}
	wantPeak := map[string]float64{"B": 469, "G": 549, "R": 654}
	for name, layer := range in.Layers {
		got := Reconstruct(layer.Fragments, in.X, in.Y, 1)
		if len(got) < 100 {
			t.Fatalf("Gold 200 %s: %d points", name, len(got))
		}
		pk, pv := got[0].X, -math.MaxFloat64
		for _, p := range got {
			if p.Y > pv {
				pv, pk = p.Y, p.X
			}
		}
		t.Logf("Gold 200 %s: %d points, %.0f..%.0f nm, peak %.0f nm at %.3f",
			name, len(got), got[0].X, got[len(got)-1].X, pk, pv)
		if d := math.Abs(pk - wantPeak[name]); d > 5 {
			t.Errorf("Gold 200 %s: peak %.0f nm, want %.0f", name, pk, wantPeak[name])
		}
	}
}
