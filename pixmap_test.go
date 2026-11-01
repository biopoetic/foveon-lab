package main

// The pixmap study's machinery: interpolation, the normal equations and the
// solver, on signals with a known answer.

import (
	"math"
	"testing"
)

func pmTestSample(n int) *pmSample {
	s := &pmSample{Name: "synthetic", Group: "T", W: n, H: 1, Stride: 1}
	s.Lab = make([]float32, 3*n)
	s.Base = make([]uint8, 3*n)
	s.Targ = make([]uint16, 3*n)
	for i := 0; i < n; i++ {
		u := float64(i) / float64(n-1)
		L := 10 + 80*u
		A := -20 + 40*u
		B := 30 - 30*u
		s.Lab[i*3], s.Lab[i*3+1], s.Lab[i*3+2] = float32(L), float32(A), float32(B)
		for c := 0; c < 3; c++ {
			s.Base[i*3+c] = 100
			s.Targ[i*3+c] = uint16((100.0/255 + 10.0/255) * 65535)
		}
	}
	return s
}

func TestPMTrilinearWeights(t *testing.T) {
	g := pmNewGrid(4, 0, 100, -50, 50, -50, 50)
	u, v, w := 0.37, 0.62, 0.11
	off, wt := g.pmNodeWeights(u, v, w)
	var sum float64
	for i := range wt {
		if wt[i] < 0 {
			t.Fatalf("negative weight %g", wt[i])
		}
		sum += wt[i]
	}
	if math.Abs(sum-1) > 1e-12 {
		t.Fatalf("weights sum to %g", sum)
	}
	// A trilinear field must be reproduced exactly at any point: take the
	// field's values at the eight corners of the cell the point falls in.
	val := func(a, b, c float64) float64 { return 0.1 + 0.2*a - 0.3*b + 0.05*c + 0.4*a*b - 0.2*b*c }
	h := 1 / float64(g.N-1)
	u0, v0, w0 := math.Floor(u*float64(g.N-1))*h, math.Floor(v*float64(g.N-1))*h, math.Floor(w*float64(g.N-1))*h
	x := [8]float64{}
	for i := 0; i < 8; i++ {
		a := u0 + float64(i>>2&1)*h
		b := v0 + float64(i>>1&1)*h
		c := w0 + float64(i&1)*h
		x[i] = val(a, b, c)
	}
	_ = off
	var got float64
	for i := 0; i < 8; i++ {
		got += wt[i] * x[i]
	}
	if math.Abs(got-val(0.37, 0.62, 0.11)) > 1e-12 {
		t.Fatalf("trilinear interpolation %.15f want %.15f", got, val(0.37, 0.62, 0.11))
	}
}

func TestPMSolverConstantOffset(t *testing.T) {
	s := pmTestSample(4000)
	g := pmNewGrid(3, 0, 100, -50, 50, -50, 50)
	c := pmNewCells(3)
	g.pmAccumulate(c, s, nil)
	mat := pmAssemble([]*pmCells{c}, 3)
	rhs := pmRHS([]*pmCells{c}, 3)
	th, _ := pmSolve(mat, nil, 0, 3, 0.01*mat.diagMean, 0, rhs, nil)
	score := pmScore(th, g, s)
	if score > 0.5 {
		t.Fatalf("constant +10 offset still scores %.3f (want < 0.5)", score)
	}
	base := pmBaseScore(s)
	if score >= base {
		t.Fatalf("mapping did not improve: %.3f vs base %.3f", score, base)
	}
}

func TestPMOracleFitsCurve(t *testing.T) {
	// A per-photo delta that varies with L must be recoverable by the
	// per-photo (oracle) fit with a fine grid.
	s := pmTestSample(8000)
	for i := 0; i < s.n(); i++ {
		targ := float64(s.Base[i*3])/255 + 0.05*float64(s.Lab[i*3])/100
		for c := 0; c < 3; c++ {
			s.Targ[i*3+c] = uint16(math.Max(0, math.Min(1, targ)) * 65535)
		}
	}
	g := pmNewGrid(6, 0, 100, -50, 50, -50, 50)
	c := pmNewCells(6)
	g.pmAccumulate(c, s, nil)
	rhs := pmRHS([]*pmCells{c}, 6)
	th, _ := pmSolve(nil, c, 1, 6, 1e-6, 0, rhs, nil)
	base := pmBaseScore(s)
	score := pmScore(th, g, s)
	if score > 0.3 || score >= base {
		t.Fatalf("oracle fit %.3f base %.3f, want a clear improvement", score, base)
	}
}
