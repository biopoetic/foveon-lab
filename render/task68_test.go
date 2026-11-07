package render

// Task 68: the remaining colour slides. Eighteen films now carry their own
// makers' characteristic curves, spectral sensitivities and dyes (the sheets
// that are raster-only or whose wedge drifts stay on the generic look, see
// REPORT-68).
//
// A film's three characteristic curves are sometimes drawn at the layer's own
// effective exposure rather than at one common log H. colorFilmData.Shift is
// that registration (decades, R G B); it is fitted from the sheet's own three
// curves at the ISO mid-grey and stored with the film. The five task-67 films
// have no shift and their renders are unchanged.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

var t68Films = []string{
	"Kodak Kodachrome 25",
	"Kodak Kodachrome 64",
	"Kodak Kodachrome 200",
	"Kodak Ektachrome E100G",
	"Kodak Ektachrome E100GX",
	"Kodak Ektachrome E100VS",
	"Kodak Ektachrome 100 Plus (EPP)",
	"Kodak Ektachrome 100 (EPN)",
	"Kodak Ektachrome 64 (EPR)",
	"Kodak Ektachrome E200",
	"Kodak Ektachrome 64T (EPY)",
	"Kodak Ektachrome 160T (EPT)",
	"Kodak Ektachrome 320T (EPJ)",
	"Kodak Ektachrome 400X (EPL)",
	"Fujifilm Fujichrome Velvia 100F",
	"Agfa Agfachrome RSX II 50",
	"Agfa Agfachrome RSX II 100",
	"Agfa Agfachrome RSX II 200",
}

// The nineteen new films have a model, and the model is built from three
// characteristic curves, three sensitivities and three dyes.
func TestSlideData68(t *testing.T) {
	for _, key := range t68Films {
		d := colorFilmData_()[key]
		if d == nil {
			t.Errorf("%s: no data entry", key)
			continue
		}
		for _, m := range []map[string][][2]float64{d.Char, d.Sens, d.Dye} {
			if len(m) != 3 {
				t.Errorf("%s: %d curve sets, want 3", key, len(m))
			}
		}
		if f := colorFilmFor(key, "Merrill"); f == nil {
			t.Fatalf("%s: no model", key)
		}
		// The drawn curves must cover the ISO mid-grey from -4 to +2 stops.
		lg := math.Log10(10 / d.ISO)
		for _, n := range []string{"R", "G", "B"} {
			ch := d.Char[n]
			lo, hi := ch[0][0], ch[0][0]
			for _, p := range ch {
				lo = math.Min(lo, p[0])
				hi = math.Max(hi, p[0])
			}
			if lo > lg-4*0.30103 || hi < lg+2*0.30103 {
				t.Errorf("%s %s: curves span %.2f..%.2f, mid-grey %.2f needs -4..+2 stops", key, n, lo, hi, lg)
			}
		}
	}
}

// The five task-67 films must keep the axis exactly as digitised: no shift.
func TestSlideNoShift67(t *testing.T) {
	for _, key := range t67Slides {
		if s := colorFilmData_()[key].Shift; len(s) != 0 {
			t.Errorf("%s: shift %v, want none", key, s)
		}
	}
}

// The neutral wedge of every new film, -4..+2 stops: the film may carry its
// own balance, but must not drift from it by more than 3 in a* or b*. This is
// the task-67 target, applied to the new sheets.
func TestSlideWedge68(t *testing.T) {
	for _, key := range t68Films {
		f := colorFilmOn(key, "", "Merrill", ColorPrint{})
		if f == nil {
			t.Fatalf("%s: no model", key)
		}
		d := colorFilmData_()[key]
		if d.ReadStatusA != f.integrated {
			t.Errorf("%s: integrated %v but read_statusa %v", key, f.integrated, d.ReadStatusA)
		}
		if f.integrated {
			// Task 73: one registration per kept film, inside the stated
			// tolerance, and the ISO mid-grey is neutral.
			if len(d.Shift) != 3 || math.Abs(d.Shift[0]) > t73Tolerance || math.Abs(d.Shift[2]) > t73Tolerance {
				t.Errorf("%s: registration %v is outside %.2f decade", key, d.Shift, t73Tolerance)
			}
			if L, a, b := t73MidLab(f); math.Abs(a) > 0.5 || math.Abs(b) > 0.5 || L < 45 || L > 55 {
				t.Errorf("%s: mid-grey L* %.2f a* %+.2f b* %+.2f", key, L, a, b)
			}
		}
		var pts [][2]float64
		var ys []float64
		for s := -4.0; s <= 2.01; s += 1 {
			v := float32(midGreyLayer * math.Exp2(s))
			r, g, b := f.apply(v, v, v)
			_, a, bb := t67Lab(float64(r), float64(g), float64(b))
			pts = append(pts, [2]float64{a, bb})
			ys = append(ys, 0.2126*float64(r)+0.7152*float64(g)+0.0722*float64(b))
		}
		var ma, mb float64
		for _, p := range pts {
			ma += p[0]
			mb += p[1]
		}
		ma /= float64(len(pts))
		mb /= float64(len(pts))
		worst := 0.0
		for i, p := range pts {
			d := math.Max(math.Abs(p[0]-ma), math.Abs(p[1]-mb))
			if d > worst {
				worst = d
			}
			if d > 3.0 {
				t.Errorf("%s: stop %d a* %+.2f b* %+.2f is %.2f from the balance (%.2f/%.2f)",
					key, i-4, p[0], p[1], d, ma, mb)
			}
		}
		for i := 1; i < len(ys); i++ {
			if ys[i] <= ys[i-1] {
				t.Errorf("%s: wedge is not monotone at stop %d", key, i-4)
			}
		}
		t.Logf("%-44s balance a* %+.2f b* %+.2f, worst drift %.2f", key, ma, mb, worst)
	}
}

// Every new film shares the task-67 viewing black level and its deep shadow
// is a neutral L* 3-10, not crushed black.
func TestSlideBlack68(t *testing.T) {
	for _, key := range t68Films {
		f := colorFilmOn(key, "", "Merrill", ColorPrint{})
		if f == nil {
			t.Fatalf("%s: no model", key)
		}
		if math.Abs(f.black-slideBlack) > 1e-12 {
			t.Errorf("%s: viewing black %.5f, want %.5f", key, f.black, slideBlack)
		}
		vr, vg, vb := f.apply(2e-4, 2e-4, 2e-4)
		L, a, bb := t67Lab(float64(vr), float64(vg), float64(vb))
		if L < 3.0 || L > 12.0 {
			t.Errorf("%s: deep shadow L* %.2f", key, L)
		}
		if math.Abs(a) > 3 || math.Abs(bb) > 3 {
			t.Errorf("%s: deep shadow a* %+.2f b* %+.2f", key, a, bb)
		}
		// A task-73 film carries the registration into the shadow too: the
		// measured deep shadow is neutral to the task-67 tolerance, not just
		// the wide t68 band.
		if f.integrated && (math.Abs(a) > 1 || math.Abs(bb) > 1) {
			t.Errorf("%s: registered deep shadow a* %+.2f b* %+.2f, want neutral", key, a, bb)
		}
	}
}

// GPU = CPU for three new films, one of them a Kodachrome. The shader is
// unchanged (task 67 added uCFBlack); this mirrors its slide pass on a
// deep-shadow frame and compares it with the CPU render to one 8-bit level.
func TestSlideGPU68(t *testing.T) {
	for _, key := range []string{"Kodak Kodachrome 64", "Kodak Ektachrome E100G", "Kodak Ektachrome E200"} {
		const w, h = 32, 24
		l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, ISO: 100, Sensor: "Merrill"}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := (y*w + x) * 3
				u := float64(x) / float64(w-1)
				v := float64(y) / float64(h-1)
				l.Pix[i] = float32(0.002 + 0.35*u + 0.10*v)
				l.Pix[i+1] = float32(0.002 + 0.30*u + 0.12*(1-v))
				l.Pix[i+2] = float32(0.002 + 0.25*v + 0.10*u)
			}
		}
		b := NewBase(l)
		cc := [9]float64{1.5, -0.4, -0.1, -0.2, 1.4, -0.2, 0.0, -0.5, 1.5}
		comp := Compensation{Family: "SD1", Sensor: "Merrill", CC: &cc, SatFactor: 1}
		p := preset.Neutral()
		p.Film = key
		p.NoGrain = true
		cpu := Render(b, p, comp)
		r := NewGPURecipe(b, p, comp)
		if r.ColorFilm == nil || r.ColorFilm.Black != float32(slideBlack) {
			t.Fatalf("%s: slide recipe black %v", key, r.ColorFilm)
		}
		maxd, mism := 0, 0
		for i := 0; i < w*h; i++ {
			c := [3]float32{l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2]}
			c = t60GPUFilm(r, [3]float32{c[0] * r.ColorFilm.Exp, c[1] * r.ColorFilm.Exp, c[2] * r.ColorFilm.Exp})
			for k := 0; k < 3; k++ {
				c[k] = c[k] * r.Gain[k] / r.ColorFilm.Exp
			}
			Y := float32(0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2])
			if Y > 1e-6 {
				d := dec(enc(Y))
				k := d / Y
				c[0], c[1], c[2] = c[0]*k, c[1]*k, c[2]*k
			}
			for k := 0; k < 3; k++ {
				want := int(float32(enc(c[k]))*255 + 0.5)
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
			t.Fatalf("%s: %d channels differ by more than 1 level (max %d)", key, mism, maxd)
		}
		t.Logf("%s: GPU = CPU, max %d 8-bit level", key, maxd)
	}
}
