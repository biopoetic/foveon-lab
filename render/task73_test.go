package render

// Task 73: the slides' three characteristic curves are registered at one
// exposure, the ISO mid-grey, and nothing else is fitted. The integrated
// Status A read is kept only on the films whose sheet evidence and measured
// grey balance support it; every other slide keeps the v0.4.108 fitted read
// byte for byte. These tests pin the split, the tolerance and the registration
// itself.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// t73StatusAFilms is the complete list of films kept on the integrated read
// with a mid-grey registration. It is deliberately a literal list: a film may
// not drift into build C silently.
var t73StatusAFilms = []string{
	"Kodak Ektachrome E100G",
	"Kodak Ektachrome E100GX",
	"Kodak Ektachrome 64 (EPR)",
	"Kodak Ektachrome 64T (EPY)",
}

// t73Tolerance is the largest log-H registration the digitisation can justify:
// a 1.0 pt curve stroke on a 0.5-decade, 20 pt grid cell is 0.025 decade; the
// 0.05-decade sampling adds 0.025; the measured axis residual is at most 0.060
// decade (the 64T II label scatter; the grid fits are 0.002). The quadrature
// sum is 0.070, so 0.10 decade is the stated envelope. See REPORT-73 section 3.
const t73Tolerance = 0.10

func t73IsStatusA(key string) bool {
	for _, k := range t73StatusAFilms {
		if k == key {
			return true
		}
	}
	return false
}

// t73MidLab is the CIELAB of the ISO mid-grey through the app's own path.
func t73MidLab(f *colorFilm) (L, a, b float64) {
	r, g, bb := f.apply(midGreyLayer, midGreyLayer, midGreyLayer)
	return labOf(float64(r), float64(g), float64(bb))
}

// TestT73Registration pins the read split and the registration. A kept film
// carries one shift per film (never a second hidden one), inside the stated
// tolerance, and its mid-grey and deep shadow are neutral. Every other slide
// keeps the fitted read, including EIR.
func TestT73Registration(t *testing.T) {
	for key := range colorFilmData_() {
		d := colorFilmData_()[key]
		f := colorFilmFor(key, "Merrill")
		if f == nil {
			t.Fatalf("%s: no model", key)
		}
		want := t73IsStatusA(key)
		if d.ReadStatusA != want {
			t.Errorf("%s: read_statusa %v, want %v", key, d.ReadStatusA, want)
		}
		if f.integrated != want {
			t.Errorf("%s: integrated %v, want %v", key, f.integrated, want)
		}
		if !want {
			// The fitted path is the v0.4.108 code, and EIR keeps its three
			// fixed Status A band reads rather than a fitted grey balance.
			if d.IRApprox {
				if f.readLam != [3]float64{650, 540, 450} {
					t.Errorf("%s: EIR read %v, want 650/540/450", key, f.readLam)
				}
			} else if f.readLam[0] < 590 || f.readLam[0] > 690 ||
				f.readLam[1] < 515 || f.readLam[1] > 580 ||
				f.readLam[2] < 420 || f.readLam[2] > 475 {
				t.Errorf("%s: fitted read %v is outside the Status A bands", key, f.readLam)
			}
			for k := 0; k < 3; k++ {
				if f.shift[k] != oldShift(d, k) {
					t.Errorf("%s: fallback shift %d = %.4f, want the JSON %.4f", key, k, f.shift[k], oldShift(d, k))
				}
			}
			continue
		}
		if d.IRApprox {
			t.Errorf("%s: EIR has no neutral axis and must not be registered", key)
		}
		if len(d.Shift) != 3 {
			t.Errorf("%s: registered film has no shift", key)
			continue
		}
		if d.Shift[1] != 0 {
			t.Errorf("%s: G shift %.4f, must stay 0", key, d.Shift[1])
		}
		if math.Abs(d.Shift[0]) > t73Tolerance || math.Abs(d.Shift[2]) > t73Tolerance {
			t.Errorf("%s: registration %v is outside the %.2f decade tolerance", key, d.Shift, t73Tolerance)
		}
		if f.shift[0] != d.Shift[0] || f.shift[1] != d.Shift[1] || f.shift[2] != d.Shift[2] {
			t.Errorf("%s: film shift %v is not the single JSON shift %v", key, f.shift, d.Shift)
		}
		// The registration is solved on the same exposure the wedge's 0-stop
		// sample uses (midGreyLayer·hWhite); the rendered mid-grey must be
		// neutral relative to D-min white.
		if L, a, b := t73MidLab(f); math.Abs(a) > 0.5 || math.Abs(b) > 0.5 || L < 45 || L > 55 {
			t.Errorf("%s: mid-grey L* %.2f a* %+.2f b* %+.2f, want neutral 18%% grey", key, L, a, b)
		}
		// Deep shadow: L* 3-10 with a neutral cast (the task-67 criterion).
		sr, sg, sb := f.apply(2e-4, 2e-4, 2e-4)
		sL, sa, sbb := labOf(float64(sr), float64(sg), float64(sb))
		if sL < 3 || sL > 10 || math.Abs(sa) > 1 || math.Abs(sbb) > 1 {
			t.Errorf("%s: deep shadow L* %.2f a* %+.2f b* %+.2f", key, sL, sa, sbb)
		}
	}
}

// oldShift is the film's shift as stored in the JSON (zero when absent).
func oldShift(d *colorFilmData, k int) float64 {
	if len(d.Shift) == 3 {
		return d.Shift[k]
	}
	return 0
}

// TestT73FallbackList pins the exact split: four kept films, EIR and the rest
// on the fitted read. A new model must be added here, not silently switched.
func TestT73FallbackList(t *testing.T) {
	var got []string
	for key := range colorFilmData_() {
		if colorFilmData_()[key].ReadStatusA {
			got = append(got, key)
		}
	}
	if len(got) != len(t73StatusAFilms) {
		t.Fatalf("status-A films %v, want %v", got, t73StatusAFilms)
	}
	for _, k := range t73StatusAFilms {
		if !colorFilmData_()[k].ReadStatusA {
			t.Errorf("%s is missing from read_statusa", k)
		}
	}
	if colorFilmData_()["Kodak Ektachrome Professional Infrared (EIR)"].ReadStatusA {
		t.Error("EIR has no neutral axis and must stay on the fitted read")
	}
}

// TestT73GPU checks the GPU preview against the CPU on the four films the task
// names. The shader is unchanged; the table is the only difference.
func TestT73GPU(t *testing.T) {
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
	for _, key := range []string{
		"Fujifilm Fujichrome Velvia 50",
		"Kodak Ektachrome E100G",
		"Kodak Kodachrome 64",
		"Kodak Ektachrome Professional Infrared (EIR)",
	} {
		p := preset.Neutral()
		p.Film = key
		p.NoGrain = true
		cpu := Render(b, p, comp)
		r := NewGPURecipe(b, p, comp)
		if r.ColorFilm == nil {
			t.Fatalf("%s: no slide recipe", key)
		}
		maxd := 0
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
			}
		}
		if maxd > 1 {
			t.Errorf("%s: GPU differs from CPU by %d levels", key, maxd)
		}
		t.Logf("%s: GPU = CPU, max %d level", key, maxd)
	}
}
