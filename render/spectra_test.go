package render

import (
	"math"
	"testing"
)

// layerTripleBounds is the exact reachable range of each pairwise difference
// of a white-balanced sensor layer value over every reflectance in [0,1]:
// p_k = Σ a_k(λ) r(λ), a_k = S_k(λ)I(λ) / Σ S_k(λ)I(λ), and Σ a_k = 1, so
// p_i - p_j is bounded by taking every r(λ) at one end. A triple outside
// these bounds is not a colour any scene can produce on this sensor.
func layerTripleBounds(S [][3]float64) [3][2]float64 {
	sp := spectral()
	var w [3]float64
	for i := range sp.lams {
		for k := 0; k < 3; k++ {
			w[k] += S[i][k] * sp.illum[i]
		}
	}
	var a [][3]float64
	for i := range sp.lams {
		var v [3]float64
		for k := 0; k < 3; k++ {
			v[k] = S[i][k] * sp.illum[i] / w[k]
		}
		a = append(a, v)
	}
	pairs := [3][2]int{{0, 1}, {0, 2}, {1, 2}}
	var out [3][2]float64
	for _, v := range a {
		for n, p := range pairs {
			d := v[p[0]] - v[p[1]]
			if d < 0 {
				out[n][0] += d
			} else {
				out[n][1] += d
			}
		}
	}
	return out
}

func tripleReachable(p [3]float64, b [3][2]float64) bool {
	pairs := [3][2]int{{0, 1}, {0, 2}, {1, 2}}
	for n, q := range pairs {
		d := p[q[0]] - p[q[1]]
		if d < b[n][0]-1e-9 || d > b[n][1]+1e-9 {
			return false
		}
	}
	return true
}

// TestSensorTripleBounds: a triple fed to a film must be a colour the sensor
// can produce. The round-8 contact sheet's saturated layer triples were not:
// six of seven lay far outside these bounds (REPORT-9). A real scene's
// spectrum, through the sensor model, is always inside them.
func TestSensorTripleBounds(t *testing.T) {
	b := layerTripleBounds(layerSens("Merrill"))
	t.Logf("Merrill reachable layer differences R-G %.3f..%.3f, R-B %.3f..%.3f, G-B %.3f..%.3f",
		b[0][0], b[0][1], b[1][0], b[1][1], b[2][0], b[2][1])
	for _, c := range [][3]float64{
		{0.8, 0.1, 0.1}, {0.1, 0.8, 0.1}, {0.1, 0.1, 0.8},
		{0.1, 0.8, 0.8}, {0.8, 0.1, 0.8}, {0.8, 0.8, 0.1},
	} {
		if tripleReachable(c, b) {
			t.Errorf("impossible triple %v passed the bounds %v", c, b)
		}
	}
	// The report's skin triple is reachable.
	if !tripleReachable([3]float64{0.5, 0.3, 0.2}, b) {
		t.Errorf("skin triple 0.5/0.3/0.2 is outside %v", b)
	}
	// Spectra through the sensor model are inside the bounds by construction.
	S := layerSens("Merrill")
	sp := spectral()
	for _, r := range fitSpectra() {
		var p, w [3]float64
		for i := range sp.lams {
			e := sp.illum[i] * r[i]
			for k := 0; k < 3; k++ {
				p[k] += S[i][k] * e
				w[k] += S[i][k] * sp.illum[i]
			}
		}
		for k := 0; k < 3; k++ {
			p[k] /= w[k]
		}
		if !tripleReachable(p, b) {
			t.Fatalf("a real spectrum gave %v, outside %v", p, b)
		}
	}
}

// TestColorSpectraThroughFilm: colour checks use spectra through the sensor
// model, not arbitrary layer triples. Red, green and blue reflectances are
// reachable, and the film keeps their hue order (red < green < blue).
func TestColorSpectraThroughFilm(t *testing.T) {
	sp := spectral()
	S := layerSens("Merrill")
	b := layerTripleBounds(S)
	band := func(c, w, lo, hi float64) []float64 {
		r := make([]float64, len(sp.lams))
		for i, l := range sp.lams {
			r[i] = lo + (hi-lo)*math.Exp(-math.Pow((l-c)/w, 2))
		}
		return r
	}
	want := []float64{35, 120, 260}
	for n, r := range [][]float64{band(610, 40, 0.03, 0.9), band(540, 40, 0.03, 0.9), band(460, 40, 0.03, 0.9)} {
		var p, w [3]float64
		for i := range sp.lams {
			e := sp.illum[i] * r[i]
			for k := 0; k < 3; k++ {
				p[k] += S[i][k] * e
				w[k] += S[i][k] * sp.illum[i]
			}
		}
		for k := 0; k < 3; k++ {
			p[k] /= w[k]
		}
		if !tripleReachable(p, b) {
			t.Fatalf("patch %d: %v outside %v", n, p, b)
		}
		for _, key := range []string{"Kodak Portra 400", "Fujifilm Fujichrome Velvia 100"} {
			f := colorFilmFor(key, "Merrill")
			if f == nil {
				t.Fatalf("no model for %s", key)
			}
			x, y, z := f.apply(float32(p[0]), float32(p[1]), float32(p[2]))
			_, a, bl := labOf(float64(x), float64(y), float64(z))
			h := hueOf(a, bl)
			if d := hueDist(h, want[n]); d > 50 {
				t.Errorf("%s patch %d: layer %v -> hue %.0f, want near %.0f", key, n, p, h, want[n])
			}
		}
	}
}

// labOf and hueOf are the small CIELAB/hue helpers the colour tests need.
func labOf(r, g, b float64) (L, A, B float64) {
	r, g, b = math.Max(0, r), math.Max(0, g), math.Max(0, b)
	x := 0.4124564*r + 0.3575761*g + 0.1804375*b
	y := 0.2126729*r + 0.7151522*g + 0.0721750*b
	z := 0.0193339*r + 0.1191920*g + 0.9503041*b
	f := func(t float64) float64 {
		if t > 216.0/24389 {
			return math.Cbrt(t)
		}
		return (24389.0/27*t + 16) / 116
	}
	fx, fy, fz := f(x/0.95047), f(y), f(z/1.08883)
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

func hueOf(a, b float64) float64 {
	h := math.Atan2(b, a) * 180 / math.Pi
	if h < 0 {
		h += 360
	}
	return h
}

func hueDist(a, b float64) float64 {
	d := math.Abs(a - b)
	if d > 180 {
		d = 360 - d
	}
	return d
}
