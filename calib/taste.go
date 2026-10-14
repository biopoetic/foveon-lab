package calib

// Research: a first-principles raw base. The camera's own CAMF colour
// matrix (with its Auto white balance) is already colorimetric: on the
// modelled sensor it reproduces what the CIE observer sees within ~2%
// (tools/foveonmodel). So what Sigma Photo Pro adds should be "taste" only:
// a tone curve on brightness and a saturation, not a colour matrix. Such a
// base has a handful of parameters, so it can be learnt from few photos and
// may carry over between bodies (learnt on the SD1, used for a Quattro or
// an SD9 with no SPP pairs at all).

import (
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/biopoetic/foveon-lab/render"
)

const tasteBins = 32

// Taste is SPP's look over the colorimetric development: a tone curve on
// luminance (sRGB-encoded in and out, one value per bin) and a saturation.
type Taste struct {
	Tone [tasteBins]float64
	Sat  float64
}

func lumOf(p []float32) float64 {
	return 0.2126*float64(p[0]) + 0.7152*float64(p[1]) + 0.0722*float64(p[2])
}

func (t *Taste) tone(y float64) float64 {
	e := float64(render.ToSRGB(float32(math.Max(0, math.Min(1, y)))))
	f := e*tasteBins - 0.5
	var v float64
	switch {
	case f <= 0:
		v = t.Tone[0] * e * 2 * tasteBins
	case f >= tasteBins-1:
		v = t.Tone[tasteBins-1]
	default:
		b := int(f)
		u := f - float64(b)
		v = t.Tone[b]*(1-u) + t.Tone[b+1]*u
	}
	return float64(render.ToLinear(float32(math.Max(0, math.Min(1, v)))))
}

// apply maps one linear pixel: scale to the new luminance (hue and
// saturation kept), then scale chroma around grey by Sat.
func (t *Taste) apply(p []float32) [3]float32 {
	y := lumOf(p)
	if y <= 1e-6 {
		return [3]float32{}
	}
	y2 := t.tone(y)
	k := y2 / y
	var o [3]float32
	for c := 0; c < 3; c++ {
		v := float64(p[c]) * k
		o[c] = float32(y2 + t.Sat*(v-y2))
	}
	return o
}

func (t *Taste) applied(l *render.Linear) *render.Linear {
	o := &render.Linear{W: l.W, H: l.H, Pix: make([]float32, len(l.Pix))}
	for i := 0; i < len(l.Pix); i += 3 {
		v := t.apply(l.Pix[i : i+3])
		copy(o.Pix[i:i+3], v[:])
	}
	return o
}

// fitTaste: the tone curve is the median SPP luminance per input luminance
// bin (monotone), the saturation the least-squares chroma ratio after it.
func fitTaste(pairs []RawPair) Taste {
	var t Taste
	bins := make([][]float64, tasteBins)
	for _, p := range pairs {
		for i := 0; i < len(p.Raw.Pix); i += 6 {
			a, b := p.Raw.Pix[i:i+3], p.SPP.Pix[i:i+3]
			// Skip only inputs clipped in the raw: skipping pixels SPP
			// renders near white too would keep, in the bright bins, only
			// the ones SPP made darker, flattening the curve's top (it came
			// out constant above ~75%, 2026-10-14).
			if max(a[0], a[1], a[2]) > 0.97 {
				continue
			}
			ya, yb := lumOf(a), lumOf(b)
			if ya <= 0 {
				continue
			}
			e := float64(render.ToSRGB(float32(math.Min(1, ya))))
			bin := min(tasteBins-1, int(e*tasteBins))
			bins[bin] = append(bins[bin], float64(render.ToSRGB(float32(math.Max(0, math.Min(1, yb))))))
		}
	}
	prev := 0.0
	for b := range bins {
		v := (float64(b) + 0.5) / tasteBins
		if len(bins[b]) > 30 {
			sort.Float64s(bins[b])
			v = bins[b][len(bins[b])/2]
		}
		v = math.Max(prev, v)
		t.Tone[b], prev = v, v
	}
	t.Sat = 1
	var num, den float64
	for _, p := range pairs {
		for i := 0; i < len(p.Raw.Pix); i += 6 {
			a, b := p.Raw.Pix[i:i+3], p.SPP.Pix[i:i+3]
			if max(a[0], a[1], a[2], b[0], b[1], b[2]) > 0.97 || lumOf(a) <= 1e-4 {
				continue
			}
			ya := lumOf(a)
			y2 := t.tone(ya)
			k := y2 / ya
			yb := lumOf(b)
			for c := 0; c < 3; c++ {
				x := float64(a[c])*k - y2
				y := float64(b[c]) - yb
				num += x * y
				den += x * x
			}
		}
	}
	if den > 0 {
		t.Sat = num / den
	}
	return t
}

// TasteReport prints held-out dE2000 vs SPP for the taste base within each
// group, next to the full fitted raw base, and across groups: the taste
// learnt on each group applied to every other.
func TasteReport(groups map[string][]RawPair, w io.Writer) {
	var names []string
	for g, ps := range groups {
		real := 0
		for _, p := range ps {
			if !p.TrainOnly {
				real++
			}
		}
		if real >= 3 {
			names = append(names, g)
		}
	}
	sort.Strings(names)
	fmt.Fprintf(w, "held out within each group, dE2000 mean vs SPP:\n")
	all := map[string]Taste{}
	for _, g := range names {
		ps := groups[g]
		var full, taste, cam float64
		n := 0
		for h := range ps {
			if ps[h].TrainOnly {
				continue
			}
			var train []RawPair
			for i := range ps {
				if i != h {
					train = append(train, ps[i])
				}
			}
			fc := fitRaw(train)
			a, _ := DeltaEStats(applied(&fc, ps[h].Raw), ps[h].SPP)
			t := fitTaste(train)
			b, _ := DeltaEStats(t.applied(ps[h].Raw), ps[h].SPP)
			c, _ := DeltaEStats(ps[h].Raw, ps[h].SPP)
			full, taste, cam, n = full+a, taste+b, cam+c, n+1
		}
		all[g] = fitTaste(ps)
		fmt.Fprintf(w, "  %-8s camera colour as is %5.2f | taste (tone + saturation) %5.2f | full raw base %5.2f   (%d photos; sat %.2f)\n",
			g, cam/float64(n), taste/float64(n), full/float64(n), n, all[g].Sat)
	}
	fmt.Fprintf(w, "across groups (taste learnt on the row's group, tested on the column's), dE2000 mean:\n  %-8s", "")
	for _, g := range names {
		fmt.Fprintf(w, " %8s", g)
	}
	fmt.Fprintln(w)
	for _, from := range names {
		fmt.Fprintf(w, "  %-8s", from)
		t := all[from]
		for _, to := range names {
			var s float64
			n := 0
			for _, p := range groups[to] {
				if p.TrainOnly {
					continue
				}
				d, _ := DeltaEStats(t.applied(p.Raw), p.SPP)
				s, n = s+d, n+1
			}
			fmt.Fprintf(w, " %8.2f", s/float64(max(1, n)))
		}
		fmt.Fprintln(w)
	}
}
