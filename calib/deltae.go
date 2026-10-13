package calib

// CIEDE2000 colour difference, the metric other Foveon projects quote
// (e.g. H Dot's SPP look recreation, dE2000 ~3.3-4.0), so our accuracy can
// be compared with theirs. Images are linear sRGB (D65).

import (
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/biopoetic/foveon-lab/render"
)

// lab converts linear sRGB (D65) to CIELAB (D65 white).
func lab(r, g, b float64) (L, A, B float64) {
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

// DeltaE2000 is the CIEDE2000 difference of two Lab colours.
func DeltaE2000(L1, a1, b1, L2, a2, b2 float64) float64 {
	const deg = math.Pi / 180
	c1, c2 := math.Hypot(a1, b1), math.Hypot(a2, b2)
	cm := (c1 + c2) / 2
	c7 := math.Pow(cm, 7)
	g := 0.5 * (1 - math.Sqrt(c7/(c7+math.Pow(25, 7))))
	a1p, a2p := (1+g)*a1, (1+g)*a2
	c1p, c2p := math.Hypot(a1p, b1), math.Hypot(a2p, b2)
	h := func(b, a float64) float64 {
		if a == 0 && b == 0 {
			return 0
		}
		v := math.Atan2(b, a) / deg
		if v < 0 {
			v += 360
		}
		return v
	}
	h1p, h2p := h(b1, a1p), h(b2, a2p)
	dLp := L2 - L1
	dCp := c2p - c1p
	var dhp float64
	if c1p*c2p != 0 {
		dhp = h2p - h1p
		if dhp > 180 {
			dhp -= 360
		} else if dhp < -180 {
			dhp += 360
		}
	}
	dHp := 2 * math.Sqrt(c1p*c2p) * math.Sin(dhp/2*deg)
	Lm := (L1 + L2) / 2
	cmp := (c1p + c2p) / 2
	hm := h1p + h2p
	if c1p*c2p != 0 {
		if math.Abs(h1p-h2p) > 180 {
			if h1p+h2p < 360 {
				hm += 360
			} else {
				hm -= 360
			}
		}
		hm /= 2
	}
	t := 1 - 0.17*math.Cos((hm-30)*deg) + 0.24*math.Cos(2*hm*deg) + 0.32*math.Cos((3*hm+6)*deg) - 0.20*math.Cos((4*hm-63)*deg)
	dTheta := 30 * math.Exp(-((hm-275)/25)*((hm-275)/25))
	cmp7 := math.Pow(cmp, 7)
	rc := 2 * math.Sqrt(cmp7/(cmp7+math.Pow(25, 7)))
	sl := 1 + 0.015*(Lm-50)*(Lm-50)/math.Sqrt(20+(Lm-50)*(Lm-50))
	sc := 1 + 0.045*cmp
	sh := 1 + 0.015*cmp*t
	rt := -math.Sin(2*dTheta*deg) * rc
	return math.Sqrt((dLp/sl)*(dLp/sl) + (dCp/sc)*(dCp/sc) + (dHp/sh)*(dHp/sh) + rt*(dCp/sc)*(dHp/sh))
}

// DeltaEStats is the mean and 95th percentile CIEDE2000 between two
// images of the same size (every other pixel in each direction).
func DeltaEStats(a, b *render.Linear) (mean, p95 float64) {
	var ds []float64
	for y := 0; y < a.H; y += 2 {
		for x := 0; x < a.W; x += 2 {
			i := (y*a.W + x) * 3
			cl := func(v float32) float64 { return math.Max(0, math.Min(1, float64(v))) }
			L1, A1, B1 := lab(cl(a.Pix[i]), cl(a.Pix[i+1]), cl(a.Pix[i+2]))
			L2, A2, B2 := lab(cl(b.Pix[i]), cl(b.Pix[i+1]), cl(b.Pix[i+2]))
			ds = append(ds, DeltaE2000(L1, A1, B1, L2, A2, B2))
		}
	}
	if len(ds) == 0 {
		return 0, 0
	}
	var s float64
	for _, d := range ds {
		s += d
	}
	sort.Float64s(ds)
	return s / float64(len(ds)), ds[len(ds)*95/100]
}

// DeltaEReport prints, per camera family, the held-out CIEDE2000 of the
// app's starting images against SPP's neutral export: the raw base, the
// camera JPEG as is, and the JPEG with its balance + tone (as the app shows
// it), plus the per-photo own-fit floor of a global colour/tone map.
func DeltaEReport(family string, raws []RawPair, jpegs []JPEGPair, w io.Writer) {
	type acc struct {
		m, p float64
		n    int
	}
	add := func(a *acc, x, y *render.Linear) {
		m, p := DeltaEStats(x, y)
		a.m, a.p, a.n = a.m+m, a.p+p, a.n+1
	}
	var raw, own, jpg, bal acc
	per := map[string]*[2]float64{} // photo → raw, app-JPEG dE (-1 = none)
	get := func(n string) *[2]float64 {
		if per[n] == nil {
			per[n] = &[2]float64{-1, -1}
		}
		return per[n]
	}
	for h := range raws {
		if raws[h].TrainOnly {
			continue
		}
		var train []RawPair
		for i := range raws {
			if i != h {
				train = append(train, raws[i])
			}
		}
		fc := fitRaw(train)
		pr := applied(&fc, raws[h].Raw)
		add(&raw, pr, raws[h].SPP)
		get(raws[h].Name)[0], _ = DeltaEStats(pr, raws[h].SPP)
		o := fitRaw(raws[h : h+1])
		add(&own, applied(&o, raws[h].Raw), raws[h].SPP)
	}
	for h := range jpegs {
		var train []JPEGPair
		for i := range jpegs {
			if i != h {
				train = append(train, jpegs[i])
			}
		}
		add(&jpg, jpegs[h].Neutral(jpegs[h].JPEG), jpegs[h].SPP)
		c := fitJPEGCoef(train, 0.01)
		src := balanced(jpegs[h], c)
		tc := fitTone(train, c)
		render.ApplyTone(src, render.PredictTone(&tc, render.ToneFeatures(src, jpegs[h].AutoWB)))
		nb := jpegs[h].Neutral(src)
		add(&bal, nb, jpegs[h].SPP)
		get(jpegs[h].Name)[1], _ = DeltaEStats(nb, jpegs[h].SPP)
	}
	row := func(name string, a acc) {
		if a.n > 0 {
			fmt.Fprintf(w, "  %-40s dE2000 mean %5.2f   p95 %5.2f   (%d photos)\n", name, a.m/float64(a.n), a.p/float64(a.n), a.n)
		}
	}
	fmt.Fprintf(w, "\n%s starting image vs SPP neutral, held out:\n", family)
	row("raw base (Accurate)", raw)
	row("camera JPEG, compensated", jpg)
	row("camera JPEG + balance + tone (app default)", bal)
	row("floor: own global fit per photo (raw)", own)
	var names []string
	for n := range per {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(w, "  per photo (dE2000 mean; raw base / camera JPEG as the app shows it):\n")
	for _, n := range names {
		v := per[n]
		f := func(x float64) string {
			if x < 0 {
				return "   -"
			}
			return fmt.Sprintf("%5.2f", x)
		}
		fmt.Fprintf(w, "    %-36s %s / %s\n", n, f(v[0]), f(v[1]))
	}
}
