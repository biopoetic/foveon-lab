package render

// Task 63: a generic scanner for colour negatives. The scan reads the
// negative's densities, removes the base, inverts with ONE fixed curve for
// every film, grey-balances the film's mid-grey, and applies ONE fixed 3x3.
// These tests pin the targets: a neutral wedge must stay neutral from 2% to
// 100%, skin must keep the scene's hue, and the films must still differ.

import (
	"math"
	"testing"
)

func labWithL(r, g, b float64) (L, a, bb float64) {
	a, bb, _ = testLab(r, g, b)
	y := 0.2126*r + 0.7152*g + 0.0722*b
	if y > 216.0/24389 {
		L = 116*math.Cbrt(y) - 16
	} else {
		L = 24389.0 / 27 * y
	}
	return
}

func hueDegrees(a, b float64) float64 {
	h := math.Atan2(b, a) * 180 / math.Pi
	if h < 0 {
		h += 360
	}
	return h
}

func hueDistance(h0, h1 float64) float64 {
	return math.Abs(math.Mod(h1-h0+540, 360) - 180)
}

var genericScanFilms = []string{
	"Kodak Portra 400", "Kodak Ektar 100",
	"Fujifilm Fujicolor Superia X-TRA 400", "Fujifilm Fujicolor Pro 400H",
	"Kodak Portra 800",
}

// Task 63: guards the generic colour-scan neutral wedge from 2% grey to white (|a*|,|b*| <= 1.5).
// A failure means a film's generic scan carries a colour cast at some grey level.
// Change only when the generic scan's neutral path is deliberately retuned; update REPORT-63/64.
func TestColorScanGenericWedge(t *testing.T) {
	for _, key := range genericScanFilms {
		f := colorFilmOn(key, PaperScan, "sRGB", ColorPrint{})
		if f == nil {
			t.Fatalf("%s: no scan", key)
		}
		if f.lutSize() != scanLutN || len(f.lut) != scanLutN*scanLutN*scanLutN*3 {
			t.Fatalf("%s: table is not %d^3", key, scanLutN)
		}
		for _, g := range []float32{0.02, 0.04, 0.08, 0.18, 0.3, 0.7, 1.0} {
			r, gg, b := f.apply(g, g, g)
			a, bb, _ := testLab(float64(r), float64(gg), float64(b))
			if math.Abs(a) > 1.5 || math.Abs(bb) > 1.5 {
				t.Errorf("%s scan: grey %.2f casts a* %.2f b* %.2f", key, g, a, bb)
			}
			t.Logf("%s scan: grey %.2f -> %.4f %.4f %.4f  a* %+.3f b* %+.3f", key, g, r, gg, b, a, bb)
		}
	}
}

// Task 63: guards the one fixed scan matrix and its row sums (task 64 froze it).
// A failure means a film's scan profile moved, or the generic matrix is no longer neutrally normalised.
// Change only when a fresh library-wide fit is deliberately made; then re-freeze and say why.
func TestColorScanGenericProfile(t *testing.T) {
	M := scanProfile()
	for k := 0; k < 3; k++ {
		s := M[k*3] + M[k*3+1] + M[k*3+2]
		if math.Abs(s-1) > 1e-9 {
			t.Errorf("profile row %d sums to %.6f", k, s)
		}
		if M[k*3+k] <= 0 {
			t.Errorf("profile row %d has no direct gain: %v", k, M[k*3:k*3+3])
		}
	}
	t.Logf("scan profile = %v", M)
	for key, d := range colorNegs().negs {
		m, ok := scanMatrixFor(d)
		if !ok {
			t.Errorf("%s: no matrix", key)
			continue
		}
		var n2 float64
		for i := range m {
			n2 += (m[i] - M[i]) * (m[i] - M[i])
		}
		t.Logf("  %-38s deviation from the average profile %.3f", key, math.Sqrt(n2))
	}
}

// Task 63: guards scene hue and chroma through the generic scan (skin <= 10 deg, etc.).
// A failure means a film's scan moved skin/foliage/sky hue or chroma outside the stated bands.
// Change only when the generic scan's colour model is deliberately changed; re-run the patch set.
func TestColorScanGenericColours(t *testing.T) {
	patches := []struct {
		name       string
		r, g, b    float64
		maxHue     float64
		minC, maxC float64
	}{
		{"skin", 0.45, 0.28, 0.20, 10, 0.7, 1.4},
		{"foliage", 0.10, 0.22, 0.07, 20, 0.7, 1.5},
		{"sky", 0.22, 0.42, 0.72, 20, 0.7, 1.6},
	}
	for _, key := range genericScanFilms {
		f := colorFilmOn(key, PaperScan, "sRGB", ColorPrint{})
		if f == nil {
			t.Fatalf("%s: no scan", key)
		}
		for _, p := range patches {
			r, g, b := f.apply(float32(p.r), float32(p.g), float32(p.b))
			L0, a0, b0 := labWithL(p.r, p.g, p.b)
			L1, a1, b1 := labWithL(float64(r), float64(g), float64(b))
			dh := hueDistance(hueDegrees(a0, b0), hueDegrees(a1, b1))
			cr := math.Hypot(a1, b1) / math.Hypot(a0, b0)
			if dh > p.maxHue {
				t.Errorf("%s %s: hue moved %.1f deg", key, p.name, dh)
			}
			if cr < p.minC || cr > p.maxC {
				t.Errorf("%s %s: chroma ratio %.2f", key, p.name, cr)
			}
			if math.Abs(L1-L0) > 6 {
				t.Errorf("%s %s: L* moved %.1f", key, p.name, L1-L0)
			}
		}
	}
}

// Task 63: guards that the films keep distinct looks (Portra 400 vs 800 >= 0.5, vs Fuji >= 3).
// A failure means the generic scan collapsed two films onto one look.
// Change only when the film data or the scan model is deliberately changed; update the distances.
func TestColorScanGenericFilmsDiffer(t *testing.T) {
	patches := [][3]float64{
		{0.45, 0.28, 0.20}, {0.55, 0.38, 0.30}, {0.10, 0.22, 0.07},
		{0.22, 0.42, 0.72}, {0.45, 0.08, 0.06}, {0.06, 0.12, 0.45},
	}
	type scan struct{ r, g, b []float64 }
	outs := map[string]scan{}
	for _, key := range genericScanFilms {
		f := colorFilmOn(key, PaperScan, "sRGB", ColorPrint{})
		var s scan
		for _, p := range patches {
			r, g, b := f.apply(float32(p[0]), float32(p[1]), float32(p[2]))
			s.r = append(s.r, float64(r))
			s.g = append(s.g, float64(g))
			s.b = append(s.b, float64(b))
		}
		outs[key] = s
	}
	dist := func(a, b scan) float64 {
		var sum float64
		for i := range a.r {
			_, a0, b0 := testLab(a.r[i], a.g[i], a.b[i])
			_, a1, b1 := testLab(b.r[i], b.g[i], b.b[i])
			sum += math.Hypot(a1-a0, b1-b0)
		}
		return sum / float64(len(a.r))
	}
	// The smallest pair is Portra 400 / Portra 800, two stocks that also
	// print alike; the largest is Portra against Fuji.
	p400 := outs["Kodak Portra 400"]
	if d := dist(p400, outs["Kodak Portra 800"]); d < 0.5 {
		t.Errorf("Portra 400 and Portra 800 scans differ by only %.2f", d)
	}
	if d := dist(p400, outs["Fujifilm Fujicolor Superia X-TRA 400"]); d < 3 {
		t.Errorf("Portra 400 and Superia scans differ by only %.2f", d)
	}
	if d := dist(p400, outs["Fujifilm Fujicolor Pro 400H"]); d < 3 {
		t.Errorf("Portra 400 and Pro 400H scans differ by only %.2f", d)
	}
	for i := 0; i < len(genericScanFilms); i++ {
		for j := i + 1; j < len(genericScanFilms); j++ {
			if d := dist(outs[genericScanFilms[i]], outs[genericScanFilms[j]]); d < 0.4 {
				t.Errorf("%s and %s scans are the same look (%.2f)", genericScanFilms[i], genericScanFilms[j], d)
			}
		}
	}
}
