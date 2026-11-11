package render

import (
	"math"
	"testing"
)

// Task 61: guards the colour-paper library against the digitised E-7020/E-7021/AF3-212E sheets.
// A failure means a paper's D-max, contrast, sensitivity or dye peak left its sheet, or a citation is missing.
// Change only when a paper's data sheet is re-digitised; update the values and cite the sheet.
func TestColorPapersFromSheets(t *testing.T) {
	want := []string{
		"Kodak Portra Endura", "Kodak Supra Endura",
		"Kodak Ektacolor Edge", "Kodak Royal Digital Paper",
		"Fujicolor Crystal Archive Digital Paper Type DP II",
	}
	got := ColorPapers()
	if len(got) != len(want) {
		t.Fatalf("colour papers %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("paper %d = %q, want %q", i, got[i], want[i])
		}
	}
	type check struct {
		name       string
		dmax       [3]float64 // R, G, B
		slope      [3]float64 // max dD/dlogH, R, G, B
		sensPeak   [3]float64 // B-, G-, R-sensitive layer, nm
		dyePeak    [3]float64 // C, M, Y, nm
		dyePeakVal float64
	}
	for _, c := range []check{
		{"Kodak Ektacolor Edge", [3]float64{2.28, 2.37, 2.15}, [3]float64{3.56, 3.30, 3.19},
			[3]float64{484, 560, 714}, [3]float64{649, 548, 444}, 1.0},
		{"Kodak Royal Digital Paper", [3]float64{2.23, 2.41, 2.30}, [3]float64{3.31, 3.25, 3.42},
			[3]float64{484, 560, 714}, [3]float64{649, 548, 444}, 1.0},
		{"Fujicolor Crystal Archive Digital Paper Type DP II", [3]float64{2.67, 2.48, 2.53}, [3]float64{2.33, 2.69, 2.69},
			[3]float64{480, 550, 708}, [3]float64{638, 548, 447}, 1.0},
	} {
		p := colorNegs().papers[c.name]
		if p == nil {
			t.Fatalf("%s missing", c.name)
		}
		if p.Src == "" || p.URL == "" {
			t.Errorf("%s: no citation", c.name)
		}
		for k, key := range [3]string{"R", "G", "B"} {
			ch := p.Char[key]
			if len(ch) < 20 {
				t.Fatalf("%s char %s: %d points", c.name, key, len(ch))
			}
			dmin, dmax, slope := ch[0][1], ch[0][1], 0.0
			for i, q := range ch {
				dmin = math.Min(dmin, q[1])
				dmax = math.Max(dmax, q[1])
				if i > 0 && q[0] > ch[i-1][0] {
					slope = math.Max(slope, (q[1]-ch[i-1][1])/(q[0]-ch[i-1][0]))
				}
			}
			if dmin > 0.15 {
				t.Errorf("%s char %s: D-min %.3f", c.name, key, dmin)
			}
			if math.Abs(dmax-c.dmax[k]) > 0.08 {
				t.Errorf("%s char %s: D-max %.3f, sheet %.3f", c.name, key, dmax, c.dmax[k])
			}
			if math.Abs(slope-c.slope[k]) > 0.25 {
				t.Errorf("%s char %s: max slope %.2f, sheet %.2f", c.name, key, slope, c.slope[k])
			}
			t.Logf("%s %s: D %.3f-%.3f, max slope %.2f, toe at log H %.2f", c.name, key, dmin, dmax, slope, p.Char[key][0][0])
		}
		for k, key := range [3]string{"B", "G", "R"} { // yellow-, magenta-, cyan-forming
			s := p.Sens[key]
			if len(s) < 20 {
				t.Fatalf("%s sens %s: %d points", c.name, key, len(s))
			}
			peak, at := -1e9, 0.0
			for _, q := range s {
				if q[1] > peak {
					peak, at = q[1], q[0]
				}
			}
			if math.Abs(at-c.sensPeak[k]) > 30 {
				t.Errorf("%s sens %s peaks at %.0f nm, sheet ~%.0f", c.name, key, at, c.sensPeak[k])
			}
			t.Logf("%s sens %s: %.0f-%.0f nm, peak %.0f nm", c.name, key, s[0][0], s[len(s)-1][0], at)
		}
		for k, key := range [3]string{"C", "M", "Y"} {
			d := p.Dye[key]
			if len(d) < 20 {
				t.Fatalf("%s dye %s: %d points", c.name, key, len(d))
			}
			peak, at := -1e9, 0.0
			for _, q := range d {
				if q[1] > peak {
					peak, at = q[1], q[0]
				}
			}
			if math.Abs(at-c.dyePeak[k]) > 25 {
				t.Errorf("%s dye %s peaks at %.0f nm, sheet ~%.0f", c.name, key, at, c.dyePeak[k])
			}
			if peak < c.dyePeakVal*0.85 || peak > c.dyePeakVal*1.15 {
				t.Errorf("%s dye %s peak %.2f, sheet %.2f", c.name, key, peak, c.dyePeakVal)
			}
		}
		// The sheet's own curves print a neutral negative neutral.
		f := colorFilmOn("Kodak Portra 400", c.name, "Merrill", ColorPrint{})
		if f == nil {
			t.Fatalf("Portra 400 on %s", c.name)
		}
		if r, g, b := f.apply(0.128, 0.128, 0.128); math.Abs(float64(g)-0.18) > 0.01 || math.Abs(float64(r-b)) > 0.02 {
			t.Errorf("%s: mid-grey prints %.3f %.3f %.3f", c.name, r, g, b)
		}
	}
}

// Task 61: guards the colour-negative scan: own D-min removed, 18% mid-grey, flatter than the print.
// A failure means the scan stopped being neutral, stopped being flatter than the paper print, or became the print.
// Change only when the scan model is deliberately changed; update REPORT-72 and the film list with it.
func TestColorScan(t *testing.T) {
	if p := colorNegs().papers[PaperScan]; p != nil {
		t.Fatalf("scan must not be a paper entry")
	}
	found := false
	for _, p := range PaperList() {
		if p.Paper == PaperScan {
			found = true
			if p.Kind != "colour" {
				t.Errorf("scan kind %q", p.Kind)
			}
		}
	}
	if !found {
		t.Fatal("the paper list has no scan entry")
	}
	keys := []string{}
	for k := range colorNegs().negs {
		keys = append(keys, k)
	}
	for _, key := range keys {
		s := colorFilmOn(key, PaperScan, "Merrill", ColorPrint{})
		if s == nil {
			t.Fatalf("no scan for %s", key)
		}
		if s.printLUT != nil {
			t.Errorf("%s: scan has a print table", key)
		}
		if r, g, b := s.apply(0.128, 0.128, 0.128); math.Abs(float64(g)-0.18) > 0.015 || math.Abs(float64(r-b)) > 0.01 {
			t.Errorf("%s scan: mid-grey %.4f %.4f %.4f", key, r, g, b)
		}
		for _, g := range []float32{0.04, 0.08, 0.128, 0.3, 0.7, 1.0} {
			r, gg, b := s.apply(g, g, g)
			a, bb, _ := testLab(float64(r), float64(gg), float64(b))
			if math.Abs(a) > 2.5 || math.Abs(bb) > 2.5 {
				t.Errorf("%s scan: grey %.2f casts a*%.2f b*%.2f", key, g, a, bb)
			}
			t.Logf("%s scan: grey %.3f -> %.4f %.4f %.4f (a* %.2f b* %.2f)", key, g, r, gg, b, a, bb)
		}
		// Flatter than the paper print, and different from it. The check is
		// on the films the task's proof uses; the Pro films' curves are now
		// registered on their published ISO speed (task 64), so Pro 800Z is
		// no longer a thin-negative exception.
		p := colorFilmOn(key, "Kodak Portra Endura", "Merrill", ColorPrint{})
		gamma := func(f *colorFilm) float64 {
			y1 := filmLuma(f.apply(0.032, 0.032, 0.032))
			y2 := filmLuma(f.apply(0.512, 0.512, 0.512))
			return math.Log(y2/y1) / math.Log(16)
		}
		gs, gp := gamma(s), gamma(p)
		flatter := key == "Kodak Portra 400" || key == "Kodak Ektar 100" || key == "Fujifilm Fujicolor Superia X-TRA 400"
		if flatter && gs > gp*0.95 {
			t.Errorf("%s: scan gamma %.2f is not flatter than the print's %.2f", key, gs, gp)
		}
		t.Logf("%s: scan gamma %.2f, print gamma %.2f", key, gs, gp)
		// The scan and the print are different renderings. At 0.08 of the
		// layer scale the print is visibly deeper (its paper contrast) than
		// the flatter scan; at 0.4 the two tone curves cross, so comparing
		// there says nothing.
		r1, g1, b1 := s.apply(0.08, 0.08, 0.08)
		r2, g2, b2 := p.apply(0.08, 0.08, 0.08)
		if math.Abs(float64(r1-r2))+math.Abs(float64(g1-g2))+math.Abs(float64(b1-b2)) < 0.04 {
			t.Errorf("%s: the scan and the print are the same", key)
		}
	}
}

func filmLuma(r, g, b float32) float64 {
	return 0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)
}

// Task 61: guards old edits whose paper is "" so they keep printing on the first colour paper.
// A failure means an empty paper silently resolved to the scan, changing saved edits.
// Change only when that compatibility is deliberately dropped; then migrate the saved edits first.
func TestColorScanLegacyEmptyPaper(t *testing.T) {
	for _, key := range []string{"Kodak Portra 400", "Kodak Ektar 100", "Fujifilm Fujicolor Superia X-TRA 400"} {
		old := colorFilmOn(key, "", "Merrill", ColorPrint{})
		first := colorFilmOn(key, ColorPapers()[0], "Merrill", ColorPrint{})
		if old == nil || first == nil {
			t.Fatalf("%s: no model", key)
		}
		if old != first {
			t.Errorf("%s: paper \"\" is not the first paper", key)
		}
		scan := colorFilmOn(key, PaperScan, "Merrill", ColorPrint{})
		if scan == nil || scan == old {
			t.Fatalf("%s: scan not distinct", key)
		}
		if old.printLUT == nil || scan.printLUT != nil {
			t.Errorf("%s: print/scan tables mixed up", key)
		}
	}
}
