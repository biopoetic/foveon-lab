package render

// Task 65: a black-and-white film scanned (no paper) was clipping its blacks
// to zero. The scan is now a scanner, not the inverse of the print: the
// film's base+fog is the black point with one fixed headroom, the density is
// inverted with one fixed slope for every film, mid-grey is 18%, and one
// gentle tone on luminance follows. These tests pin the scan's tone, the
// untouched print path, and the GPU preview's use of the same table.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

var bwScanFilms = []string{
	"Kodak Tri-X Pan 400 (TX)", "Kodak T-Max 100", "Ilford HP5 Plus",
	"Kodak T-Max 400", "Ilford FP4 Plus", "Ilford Pan F Plus",
	"Ilford Delta 400", "Ilford Delta 3200", "Agfa Agfapan APX 25",
	"Agfa Agfapan APX 100", "Agfa Agfapan APX 400",
}

// cieLightness is the CIE L* of a linear Y.
func cieLightness(y float64) float64 {
	if y > 216.0/24389 {
		return 116*math.Cbrt(y) - 16
	}
	return 24389.0 / 27 * y
}

// Task 65: guards the B&W scan wedge: 18% grey at L* 49.5, base+fog headroom 3-10, no clipped black.
// A failure means the scanner's fixed inversion or black point moved.
// Change only when the B&W scan model is deliberately retuned; re-check the 11 films and REPORT-65.
func TestBWScanWedge(t *testing.T) {
	lows := map[string]float64{}
	for _, film := range bwScanFilms {
		p := preset.Neutral()
		p.Film, p.NoGrain = film, true
		tone := newFilmTone(p)
		if tone == nil {
			t.Fatalf("%s: no tone", film)
		}
		if mid := cieLightness(float64(tone.at(0.18))); math.Abs(mid-49.5) > 0.1 {
			t.Errorf("%s: 18%% grey is L* %.2f, want 49.5", film, mid)
		}
		if floor := cieLightness(float64(tone.at(1e-9))); floor < 3 || floor > 10 {
			t.Errorf("%s: base+fog is L* %.2f, want 3-10", film, floor)
		}
		prev := -1.0
		for s := -8; s <= 4; s++ {
			L := cieLightness(float64(tone.at(float32(0.18 * math.Exp2(float64(s))))))
			if L < prev-0.05 {
				t.Errorf("%s: wedge is not monotonic at %+d stops (%.1f after %.1f)", film, s, L, prev)
			}
			if L < 2 {
				t.Errorf("%s: wedge reaches L* %.1f at %+d stops", film, L, s)
			}
			prev = L
			if s == -4 {
				lows[film] = L
			}
		}
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, L := range lows {
		lo, hi = math.Min(lo, L), math.Max(hi, L)
	}
	if hi-lo < 1.5 {
		t.Errorf("-4 stops: the films span only %.2f L* (%.1f-%.1f): the film's toe no longer decides the shadows", hi-lo, lo, hi)
	}
	t.Logf("-4 stops: %.1f-%.1f L* across the 11 films", lo, hi)
}

// Task 65: guards that a B&W film without a curve still gets no film tone.
// A failure means newFilmTone invented a curve for a generic film.
// Change only when curves are deliberately added for those films; update the film list with them.
func TestBWScanGenericUnchanged(t *testing.T) {
	for _, f := range Films() {
		if !f.BW {
			continue
		}
		key := f.Maker + " " + f.Name
		if _, ok := FilmCurveFor(key); ok {
			continue
		}
		p := preset.Neutral()
		p.Film, p.NoGrain = key, true
		if newFilmTone(p) != nil {
			t.Errorf("%s has no curve but newFilmTone built one", key)
		}
	}
}

// Task 65: guards the B&W print LUT against the old formula, entry by entry.
// A failure means the scan change touched the print path's tone.
// Change only when the print formula is deliberately changed; update this independent recomputation.
func TestBWPrintPathUnchanged(t *testing.T) {
	const paper = "Ilford Multigrade IV RC"
	for _, film := range bwScanFilms {
		c, ok := FilmCurveFor(film)
		if !ok {
			t.Fatalf("%s: no curve", film)
		}
		f, ok := FilmByKey(film)
		if !ok {
			t.Fatalf("%s: no film", film)
		}
		for _, grade := range []string{"", "2", "4"} {
			p := preset.Neutral()
			p.Film, p.Paper, p.Grade, p.NoGrain = film, paper, grade, true
			tone := newFilmTone(p)
			if tone == nil {
				t.Fatalf("%s grade %q: no print tone", film, grade)
			}
			// The print branch's own inputs, recomputed here.
			dv := c.DevelopmentFor(p)
			lgGrey := math.Log10(10/dv.EI) - math.Log10(float64(f.ISO)/dv.Dev.EI) +
				c.SpeedPoint() - math.Log10(0.8/float64(f.ISO))
			density := c.Density(dv.Gamma)
			dGrey := density(lgGrey)
			pp := papers()[paper]
			g := pp.Grades[grade]
			if g == nil {
				g = pp.Grades["2"]
			}
			dmin := g[0][1]
			for _, q := range g {
				dmin = math.Min(dmin, q[1])
			}
			lgE := func(D float64) float64 {
				for i := 1; i < len(g); i++ {
					if g[i][1] >= D {
						tt := (D - g[i-1][1]) / math.Max(1e-9, g[i][1]-g[i-1][1])
						return g[i-1][0] + tt*(g[i][0]-g[i-1][0])
					}
				}
				return g[len(g)-1][0]
			}
			k := lgE(dmin+0.745) + dGrey
			printY := func(d float64) float64 { return math.Pow(10, -(pinterp(g, k-d) - dmin)) }
			for i := 0; i <= filmToneN; i++ {
				y := math.Pow(float64(i)/float64(tone.scale), 4)
				if y <= 0 {
					continue
				}
				want := float32(printY(density(lgGrey + math.Log10(y/0.18))))
				if got := tone.lut[i]; math.Abs(float64(got-want)) > 1e-6 {
					t.Fatalf("%s grade %q entry %d: print LUT %.7f, old formula %.7f (the print path moved)", film, grade, i, got, want)
				}
			}
		}
	}
}

// gpuLutSample mirrors web/gpu.js lut(): one table row sampled on x in 0..1.
func gpuLutSample(tab []float32, row int, x float32) float32 {
	f := x
	if f < 0 {
		f = 0
	} else if f > 1 {
		f = 1
	}
	f *= GPULutN
	i := int(f)
	if i >= GPULutN {
		i = GPULutN - 1
	}
	a := tab[row*(GPULutN+1)+i]
	b := tab[row*(GPULutN+1)+i+1]
	return a + (b-a)*(f-float32(i))
}

// Task 65: guards GPU = CPU for the B&W scan through the measured pre/post tables.
// A failure means a scan recipe or web/gpu.js's measured-table path drifted.
// Change only when the measured path or shader changes; update the Go mirror in the same commit.
func TestBWScanGPURecipe(t *testing.T) {
	const w, h = 32, 24
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, ISO: 100, Sensor: "Merrill"}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			u := float64(x) / float64(w-1)
			v := float64(y) / float64(h-1)
			l.Pix[i] = float32(0.005 + 0.55*u*u)
			l.Pix[i+1] = float32(0.005 + 0.50*v*v)
			l.Pix[i+2] = float32(0.005 + 0.45*(1-u)*(1-v))
		}
	}
	b := NewBase(l)
	for _, film := range []string{"Kodak Tri-X Pan 400 (TX)", "Kodak T-Max 100", "Ilford Delta 3200"} {
		p := preset.Neutral()
		p.Film, p.NoGrain = film, true
		tone := newFilmTone(p)
		if tone == nil {
			t.Fatalf("%s: no tone", film)
		}

		// A Foveon body: the measured path, pre table.
		measured := Compensation{Family: "SD1", Sensor: "Merrill", Model: "SD1", SatFactor: 1}
		cpu := Render(b, p, measured)
		r := NewGPURecipe(b, p, measured)
		if !r.Measured || r.FilmTone != nil || len(r.Pre) != 2*(GPULutN+1) || len(r.Post) != 2*(GPULutN+1) {
			t.Fatalf("%s: recipe is not the measured pre table (%v, tone %d, pre %d)", film, r.Measured, len(r.FilmTone), len(r.Pre))
		}
		// Mid-grey inside the pre table is the scan's 18% grey.
		mid := dec(gpuLutSample(r.Pre, 0, enc(0.18)))
		if L := cieLightness(float64(mid)); math.Abs(L-49.5) > 0.1 {
			t.Errorf("%s: measured pre table puts 18%% grey at L* %.2f", film, L)
		}
		mism, maxd := 0, 0
		for i := 0; i < w*h; i++ {
			c := [3]float32{l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2]}
			// applyMeasured: no WB/mode map, unit tint, no fill, mono.
			Y := float32(0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2])
			x := enc(Y)
			x = gpuLutSample(r.Pre, 0, x)
			x = gpuLutSample(r.Post, 0, x)
			Y = dec(x)
			for k := 0; k < 3; k++ {
				want := int(float32(enc(Y))*255 + 0.5)
				got := int(cpu.Pix[i*4+k])
				d := got - want
				if d < 0 {
					d = -d
				}
				if d > maxd {
					maxd = d
				}
				if d > 1 {
					mism++
				}
			}
		}
		if mism > 0 {
			t.Fatalf("%s: measured path: %d channels differ by more than one 8-bit level (max %d)", film, mism, maxd)
		}

	}
}
