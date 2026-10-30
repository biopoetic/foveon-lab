package render

// The sensor's three layers -> a film's three layer exposures.
//
// The Foveon layers are broad and overlap; a film's red-sensitive layer is
// narrow. No 3×3 matrix turns one into the other for saturated colours: on
// a red tulip it gave Velvia's green layer 77% too much exposure (too
// little magenta dye: red printed orange) and the red layer too little.
// A root-polynomial (Finlayson, Mackiewicz & Hurlbert, "Color Correction
// Using Root-Polynomial Regression", IEEE TIP 2015) adds √rg, √gb, √rb:
// still scaled by exposure exactly like the layers (degree 1), so a
// brighter scene is the same colour, brighter. Fitted on the natural
// reflectance set plus saturated edge and band spectra, weighted by
// relative error (density is a log), white pinned to exactly white; on
// held-out saturated spectra its log-exposure error is 0.145 vs the 3×3's
// 0.345 (Velvia), on the natural set 0.042 vs 0.068.
//
// What no mapping can recover: the sensor sees a deep red (edge 625 nm) and
// a tulip red (600 nm) in the same layer ratios, so a deep red prints as a
// darker tulip red.

import "math"

// rpFeatures is (r, g, b, √rg, √gb, √rb) (negative layers count as 0 under
// the roots).
func rpFeatures(r, g, b float32) [6]float32 {
	rr, gg, bb := max(r, 0), max(g, 0), max(b, 0)
	return [6]float32{r, g, b,
		float32(math.Sqrt(float64(rr * gg))), float32(math.Sqrt(float64(gg * bb))), float32(math.Sqrt(float64(rr * bb)))}
}

// fitSpectra is the training set: the natural reflectances and saturated
// spectra (logistic edges rising and falling across 450-660 nm, Gaussian
// bands across 420-680 nm, two widths).
func fitSpectra() [][]float64 {
	sp := spectral()
	out := append([][]float64{}, sp.refl...)
	edge := func(e float64, up bool) []float64 {
		r := make([]float64, len(sp.lams))
		for i, l := range sp.lams {
			x := 1 / (1 + math.Exp(-(l-e)/8))
			if !up {
				x = 1 - x
			}
			r[i] = 0.03 + 0.77*x
		}
		return r
	}
	band := func(c, w, lo, hi float64) []float64 {
		r := make([]float64, len(sp.lams))
		for i, l := range sp.lams {
			r[i] = lo + (hi-lo)*math.Exp(-math.Pow((l-c)/w, 2))
		}
		return r
	}
	for e := 450.0; e <= 660; e += 10 {
		out = append(out, edge(e, true), edge(e, false))
	}
	for c := 420.0; c <= 680; c += 20 {
		out = append(out, band(c, 25, 0.03, 0.7), band(c, 50, 0.05, 0.6))
	}
	return out
}

// fitLayerMap fits f.rp: the sensor's white-balanced layers (sensitivities
// S) -> the film's layer exposures (log sensitivities sens R, G, B), in
// daylight, both normalised by white.
func fitLayerMap(f *colorFilm, S [][3]float64, sens map[string][][2]float64) bool {
	return fitLayerMapOn(f, S, sens, fitSpectra())
}

// fitLayerMapOn is fitLayerMap on an explicit training set, so a test can
// hold half the spectra out.
func fitLayerMapOn(f *colorFilm, S [][3]float64, sens map[string][][2]float64, train [][]float64) bool {
	sp := spectral()
	fs := make([][3]float64, len(sp.lams))
	var ws, wf [3]float64
	for i, l := range sp.lams {
		fs[i] = [3]float64{sensAt(sens["R"], l), sensAt(sens["G"], l), sensAt(sens["B"], l)}
		for k := 0; k < 3; k++ {
			ws[k] += S[i][k] * sp.illum[i]
			wf[k] += fs[i][k] * sp.illum[i]
		}
	}
	white := make([]float64, len(sp.lams))
	for i := range white {
		white[i] = 1
	}
	train = append(append([][]float64{}, train...), white)
	type sample struct {
		x [6]float64
		h [3]float64
	}
	samples := make([]sample, len(train))
	for n, r := range train {
		var p, h [3]float64
		for i := range sp.lams {
			e := sp.illum[i] * r[i]
			for k := 0; k < 3; k++ {
				p[k] += S[i][k] * e
				h[k] += fs[i][k] * e
			}
		}
		for k := 0; k < 3; k++ {
			p[k] /= ws[k]
			h[k] /= wf[k]
		}
		x := rpFeatures(float32(p[0]), float32(p[1]), float32(p[2]))
		for j := range x {
			samples[n].x[j] = float64(x[j])
		}
		samples[n].h = h
	}
	for k := 0; k < 3; k++ {
		var ata [6][6]float64
		var atb [6]float64
		for n, s := range samples {
			// Relative error (density is a log); white pinned.
			w := 1 / (s.h[k] + 0.02)
			if n == len(samples)-1 {
				w = 1000
			}
			w *= w
			for a := 0; a < 6; a++ {
				atb[a] += w * s.x[a] * s.h[k]
				for b := 0; b < 6; b++ {
					ata[a][b] += w * s.x[a] * s.x[b]
				}
			}
		}
		c, ok := solve6(ata, atb)
		if !ok {
			return false
		}
		for j := 0; j < 3; j++ {
			f.rp[k*3+j] = float32(c[j])
			f.rp[9+k*3+j] = float32(c[3+j])
		}
	}
	return true
}

// solve6 solves a·x = b (Gauss-Jordan, partial pivoting).
func solve6(a [6][6]float64, b [6]float64) ([6]float64, bool) {
	var x [6]float64
	for c := 0; c < 6; c++ {
		p := c
		for r := c + 1; r < 6; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		if math.Abs(a[p][c]) < 1e-300 {
			return x, false
		}
		a[c], a[p] = a[p], a[c]
		b[c], b[p] = b[p], b[c]
		for r := 0; r < 6; r++ {
			if r == c {
				continue
			}
			m := a[r][c] / a[c][c]
			for k := c; k < 6; k++ {
				a[r][k] -= m * a[c][k]
			}
			b[r] -= m * b[c]
		}
	}
	for i := range x {
		x[i] = b[i] / a[i][i]
	}
	return x, true
}
