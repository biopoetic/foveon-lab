package render

// Task 67: a colour slide is viewed on a light box (or read by a scanner), and
// its black point never reaches zero light. Before task 67 the slide's D-max
// was mapped to pure black: the deepest tones lost both their gradation and
// their colour, and the 14-photo p5 fell to L* 1-2. The slide now has one
// fixed viewing black level (slideBlack, half a percent of the display white,
// the same 5 permille the black-and-white scan uses); it is light the viewing
// system passes, not film density, and it is added after the viewing gain.
//
// The colour model itself is unchanged: the neutral wedge and the photo
// medians are reported in REPORT-67, and the sheet-derived fit is not tuned
// to the web's Flickr numbers (the task says to change nothing on colour when
// the honest physics does not supply that direction).

import (
	"math"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

var t67Slides = []string{
	"Fujifilm Fujichrome Velvia 100",
	"Fujifilm Fujichrome Provia 400F",
	"Fujifilm Fujichrome Provia 400X",
	"Fujifilm Fujichrome Astia 100F",
	"Fujifilm Fujichrome T64",
}

// t67Lab is the CIELAB of a linear sRGB value (D65/2 deg).
func t67Lab(r, g, b float64) (L, a, bb float64) { return labOf(r, g, b) }

// The viewing black level: D-max keeps a little light, the mid-grey stays at
// 18%, and a deeper exposure cannot fall below the black level.
func TestSlideBlackPoint67(t *testing.T) {
	for _, key := range t67Slides {
		f := colorFilmOn(key, "", "Merrill", ColorPrint{})
		if f == nil {
			t.Fatalf("%s: no model", key)
		}
		if math.Abs(f.black-slideBlack) > 1e-12 {
			t.Errorf("%s: viewing black %.5f, want %.5f", key, f.black, slideBlack)
		}
		// 18% grey: the scene's mid-grey still lands at 18% (+ the black).
		r, g, b := f.apply(midGreyLayer, midGreyLayer, midGreyLayer)
		y := 0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)
		if math.Abs(y-0.18) > 0.008 {
			t.Errorf("%s: mid-grey Y %.4f, want 0.18 (+%.4f black)", key, y, slideBlack)
		}
		// A very deep neutral exposure: every channel is above the black
		// level, so the toe cannot go to zero.
		vr, vg, vb := f.apply(2e-4, 2e-4, 2e-4)
		if float64(vr) < slideBlack*0.9 || float64(vg) < slideBlack*0.9 || float64(vb) < slideBlack*0.9 {
			t.Errorf("%s: deep shadow %.5f %.5f %.5f is below the viewing black %.5f",
				key, vr, vg, vb, slideBlack)
		}
		L, a, bb := t67Lab(float64(vr), float64(vg), float64(vb))
		if L < 3.0 || L > 10.0 {
			t.Errorf("%s: deep shadow L* %.2f, want 3-10", key, L)
		}
		if math.Abs(a) > 1 || math.Abs(bb) > 1 {
			t.Errorf("%s: deep shadow a* %+.2f b* %+.2f, want neutral", key, a, bb)
		}
		t.Logf("%s: black %.4f, mid-grey Y %.4f, deep shadow %.5f/%.5f/%.5f L* %.2f a* %+.2f b* %+.2f",
			key, f.black, y, vr, vg, vb, L, a, bb)
	}
}

// The neutral wedge: a slide's grey may carry the sheet's own balance (the
// model's neutral path is not forced to 0), but it must not drift by more
// than 3 in a* or b* from that balance anywhere between -4 and +2 stops.
// The balance is taken as the mean of the path, so a constant cast is the
// film's, not a drift.
func TestSlideWedge67(t *testing.T) {
	for _, key := range t67Slides {
		f := colorFilmOn(key, "", "Merrill", ColorPrint{})
		if f == nil {
			t.Fatalf("%s: no model", key)
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
		t.Logf("%s: balance a* %+.2f b* %+.2f, worst drift %.2f", key, ma, mb, worst)
	}
}

// t67NaturalLayers is the 400-reflectance daylight set through the sensor,
// white = 1: the same scene colours the report's patch mean uses.
func t67NaturalLayers(sensor string) [][3]float64 {
	sp := spectral()
	S := layerSens(sensor)
	var ws [3]float64
	for i := range sp.lams {
		for k := 0; k < 3; k++ {
			ws[k] += S[i][k] * sp.illum[i]
		}
	}
	var out [][3]float64
	for _, r := range sp.refl {
		var p [3]float64
		for i := range sp.lams {
			e := sp.illum[i] * r[i]
			for k := 0; k < 3; k++ {
				p[k] += S[i][k] * e
			}
		}
		for k := 0; k < 3; k++ {
			p[k] /= ws[k]
		}
		out = append(out, p)
	}
	return out
}

// The slides must still be different films: Velvia the most saturated, and
// each pair clearly apart on the natural-reflectance set.
func TestSlideFilmsDiffer67(t *testing.T) {
	patches := t67NaturalLayers("Merrill")
	type out struct {
		a, b, C []float64
	}
	outs := map[string]out{}
	for _, key := range t67Slides {
		f := colorFilmOn(key, "", "Merrill", ColorPrint{})
		o := out{}
		for _, p := range patches {
			r, g, b := f.apply(float32(p[0]), float32(p[1]), float32(p[2]))
			_, a, bb := t67Lab(float64(r), float64(g), float64(b))
			o.a = append(o.a, a)
			o.b = append(o.b, bb)
			o.C = append(o.C, math.Hypot(a, bb))
		}
		outs[key] = o
	}
	meanC := func(k string) float64 {
		var s float64
		for _, c := range outs[k].C {
			s += c
		}
		return s / float64(len(outs[k].C))
	}
	velvia := meanC("Fujifilm Fujichrome Velvia 100")
	for _, k := range t67Slides[1:] {
		if meanC(k) >= velvia {
			t.Errorf("%s C* %.2f is not below Velvia's %.2f", k, meanC(k), velvia)
		}
	}
	// Astia is the softer of the two ISO 100 films: lower gamma and chroma.
	astia := colorFilmData_()["Fujifilm Fujichrome Astia 100F"]
	vel := colorFilmData_()["Fujifilm Fujichrome Velvia 100"]
	gamma := func(d *colorFilmData) float64 {
		var g, n float64
		for _, k := range []string{"R", "G", "B"} {
			ch := d.Char[k]
			lo, hi := ch[0][1], ch[0][1]
			for _, p := range ch {
				lo = math.Min(lo, p[1])
				hi = math.Max(hi, p[1])
			}
			var sx, sy, sxx, sxy, m float64
			for i := 1; i < len(ch); i++ {
				if ch[i][1] < lo+0.3 || ch[i][1] > hi-0.5 {
					continue
				}
				x, y := ch[i][0], ch[i][1]
				sx, sy, sxx, sxy, m = sx+x, sy+y, sxx+x*x, sxy+x*y, m+1
			}
			g += math.Abs((m*sxy - sx*sy) / (m*sxx - sx*sx))
			n++
		}
		return g / n
	}
	if gamma(astia) >= gamma(vel) {
		t.Errorf("Astia gamma %.2f is not softer than Velvia's %.2f", gamma(astia), gamma(vel))
	}
	// Pairwise mean CIELAB distance (the report has the ΔE2000 on the photos).
	dist := func(x, y string) float64 {
		var s float64
		for i := range outs[x].a {
			s += math.Hypot(outs[x].a[i]-outs[y].a[i], outs[x].b[i]-outs[y].b[i])
		}
		return s / float64(len(outs[x].a))
	}
	for i := 0; i < len(t67Slides); i++ {
		for j := i + 1; j < len(t67Slides); j++ {
			d := dist(t67Slides[i], t67Slides[j])
			if d < 2.0 {
				t.Errorf("%s and %s differ by only %.2f on the patches", t67Slides[i], t67Slides[j], d)
			}
			t.Logf("%-42s / %-42s mean CIE76 %.2f", t67Slides[i], t67Slides[j], d)
		}
	}
	t.Logf("mean C*: Velvia %.2f, 400F %.2f, 400X %.2f, Astia %.2f, T64 %.2f",
		velvia, meanC(t67Slides[1]), meanC(t67Slides[2]), meanC(t67Slides[3]), meanC(t67Slides[4]))
}

// The GPU preview reads the same 3-D table and adds the slide's viewing black
// level from the recipe (web/gpu.js uCFBlack). This mirrors the shader pass
// for a slide and checks it against the CPU to one 8-bit level, on a frame
// with deep shadows as well as midtones.
func TestSlideGPUBlack67(t *testing.T) {
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
	p.Film = "Fujifilm Fujichrome Velvia 100"
	p.NoGrain = true

	cpu := Render(b, p, comp)
	r := NewGPURecipe(b, p, comp)
	if r.ColorFilm == nil {
		t.Fatal("slide recipe has no colour film")
	}
	if r.ColorFilm.Black != float32(slideBlack) {
		t.Fatalf("slide recipe black %.5f, want %.5f", r.ColorFilm.Black, slideBlack)
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
		t.Fatalf("%d channels differ by more than 1 level (max %d): the GPU slide pass does not match the CPU", mism, maxd)
	}
	t.Logf("GPU = CPU: max %d 8-bit level", maxd)
}
