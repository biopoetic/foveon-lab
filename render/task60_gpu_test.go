package render

// Task 60: the GPU preview must match the CPU when a [WB] preset is used
// under a colour film. The balance is developed into the Base (the same
// Base both paths consume: /api/gpu/base serves GPUBase of the CPU render's
// Base), so the film shader must NOT apply a second white balance. This test
// builds a synthetic Base, render parameters and compensation exactly as the
// server does for a balanced film source (Compensation.WBDeveloped), and
// compares the CPU render with a Go mirror of web/gpu.js's film pass built
// from the recipe.

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

func t60Mat3(m [9]float32, v [3]float32) [3]float32 {
	return [3]float32{
		m[0]*v[0] + m[1]*v[1] + m[2]*v[2],
		m[3]*v[0] + m[4]*v[1] + m[5]*v[2],
		m[6]*v[0] + m[7]*v[1] + m[8]*v[2],
	}
}

func t60Clamp(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x)) }

// t60GPUCurve mirrors web/gpu.js cfCurve over GPUColorFilmData's table.
func t60GPUCurve(f *colorFilm, k int, lg float64) float64 {
	n := float64(gpuCFCurveN)
	fpos := t60Clamp((lg-gpuCFLgMin)/(gpuCFLgMax-gpuCFLgMin), 0, 1) * (n - 1)
	i := int(math.Min(fpos, n-2))
	a := f.density(k, gpuCFLgMin+(gpuCFLgMax-gpuCFLgMin)*float64(i)/(n-1))
	b := f.density(k, gpuCFLgMin+(gpuCFLgMax-gpuCFLgMin)*float64(i+1)/(n-1))
	return a + (b-a)*(fpos-float64(i))
}

// t60GPUFilm mirrors web/gpu.js colorFilm(): linear sRGB -> layers, root
// polynomial -> log exposure, each layer's curve, the 3-D table, 10^-D.
func t60GPUFilm(r *GPURecipe, c [3]float32) [3]float32 {
	cf := r.ColorFilm
	f := gpuCFKeys[cf.Key]
	if f == nil {
		panic("no colour film " + cf.Key)
	}
	l := t60Mat3(cf.Inv, c)
	q := [3]float32{max(l[0], 0), max(l[1], 0), max(l[2], 0)}
	s := [3]float32{
		float32(math.Sqrt(float64(q[0] * q[1]))),
		float32(math.Sqrt(float64(q[1] * q[2]))),
		float32(math.Sqrt(float64(q[0] * q[2]))),
	}
	var rpA, rpB [9]float32
	copy(rpA[:], cf.RP[0:9])
	copy(rpB[:], cf.RP[9:18])
	ha := t60Mat3(rpA, l)
	hb := t60Mat3(rpB, s)
	var D [3]float64
	for k := 0; k < 3; k++ {
		h := (float64(ha[k]) + float64(hb[k])) * float64(cf.HWhite)
		D[k] = t60GPUCurve(f, k, math.Log10(math.Max(h, 1e-6)))
	}
	// The texture is sampled with t = (D/dmax*(N-1)+0.5)/N and LINEAR
	// filtering: x = D/dmax*(N-1), clamped, then trilinear.
	var x [3]float64
	var i0 [3]int
	var fr [3]float64
	for k := 0; k < 3; k++ {
		x[k] = t60Clamp(D[k]/cfLutDMax, 0, 1) * (cfLutN - 1)
		i0[k] = int(math.Min(x[k], cfLutN-2))
		fr[k] = x[k] - float64(i0[k])
	}
	// NewGPURecipe stores the film whose view gain GPUColorFilmData folds into
	// the table; fold it the same way here.
	g := f.viewGain()
	lg := math.Log10(g)
	var out [3]float64
	for cc := 0; cc < 8; cc++ {
		w := 1.0
		var j [3]int
		for k := 0; k < 3; k++ {
			if cc>>k&1 == 1 {
				j[k], w = i0[k]+1, w*fr[k]
			} else {
				j[k], w = i0[k], w*(1-fr[k])
			}
		}
		idx := ((j[0]*cfLutN+j[1])*cfLutN + j[2]) * 3
		for k := 0; k < 3; k++ {
			out[k] += w * (float64(f.lut[idx+k]) - lg)
		}
	}
	return [3]float32{
		float32(math.Exp2(-out[0] * log2of10)),
		float32(math.Exp2(-out[1] * log2of10)),
		float32(math.Exp2(-out[2] * log2of10)),
	}
}

func TestFilmWBGPURecipeMirrorsCPU(t *testing.T) {
	const w, h = 32, 24
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, ISO: 100, Sensor: "Merrill"}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			u := float64(x) / float64(w-1)
			v := float64(y) / float64(h-1)
			// Different channel levels: the white balance has already made
			// the layers what the film sees.
			l.Pix[i] = float32(0.02 + 0.35*u + 0.10*v)
			l.Pix[i+1] = float32(0.03 + 0.30*u + 0.12*(1-v))
			l.Pix[i+2] = float32(0.01 + 0.25*v + 0.10*u)
		}
	}
	b := NewBase(l)
	cc := [9]float64{1.5, -0.4, -0.1, -0.2, 1.4, -0.2, 0.0, -0.5, 1.5}
	comp := Compensation{Family: "SD1", Sensor: "Merrill", CC: &cc, SatFactor: 1, WBDeveloped: true}

	p := preset.Neutral()
	p.Film = "Kodak Portra 400"
	p.NoGrain = true
	p.WhiteBalance = 3 // Shade: developed into the Base, not applied here

	cpu := Render(b, p, comp)
	r := NewGPURecipe(b, p, comp)
	if r.Measured {
		t.Fatal("film recipe is measured; the colour film must use the film path")
	}
	if r.WB != nil {
		t.Fatal("film recipe carries a WB correction: the balance is already in the Base and would be applied twice")
	}
	if r.ColorFilm == nil {
		t.Fatal("film recipe has no colour film")
	}

	// GPU pass 1 for one pixel, in web/gpu.js's order for this recipe:
	// no undo/layer, film, no fill, the (zero-setting) tone block, no mono.
	mism := 0
	maxd := 0
	for i := 0; i < w*h; i++ {
		c := [3]float32{l.Pix[i*3], l.Pix[i*3+1], l.Pix[i*3+2]}
		c = t60GPUFilm(r, [3]float32{c[0] * r.ColorFilm.Exp, c[1] * r.ColorFilm.Exp, c[2] * r.ColorFilm.Exp})
		for k := 0; k < 3; k++ {
			c[k] = c[k] * r.Gain[k] / r.ColorFilm.Exp
		}
		Y := float32(0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2])
		if Y > 1e-6 {
			// The shader's tone block always round-trips through enc/dec.
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
		t.Fatalf("%d channels differ by more than 1 level (max %d): the GPU film pass does not match the CPU", mism, maxd)
	}
}
