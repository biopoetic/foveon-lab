package calib

// Research into SPP's neutral render beyond a global colour/tone map:
// does it desaturate highlights, and does it add local contrast (a
// high-pass / unsharp mask)? Each candidate is fitted on all photos but
// one (on top of the held-out global raw-base fit) and scored on the
// photo left out, so the numbers are honest.

import (
	"fmt"
	"io"
	"math"

	"github.com/biopoetic/foveon-lab/render"
)

// localBins is the number of brightness bins of the highlight model.
const localBins = 16

// HPRadii are the unsharp-mask radii tried, as fractions of the width.
var HPRadii = []float64{0.0008, 0.0017, 0.0033, 0.0067}

type localFit struct {
	chroma [localBins]float64 // chroma scale per brightness bin
	amount []float64          // high-pass amount per radius
}

// lumaEnc is perceptual brightness (sRGB-encoded luma).
func lumaEnc(p []float32, i int) float32 { return render.ToSRGB(luma(p, i)) }

// splitChroma returns encoded luma and the linear chroma (rgb - Y).
func splitChroma(l *render.Linear) (y []float32, c []float32) {
	n := l.W * l.H
	y = make([]float32, n)
	c = make([]float32, n*3)
	for i := 0; i < n; i++ {
		Y := luma(l.Pix, i*3)
		y[i] = render.ToSRGB(Y)
		for k := 0; k < 3; k++ {
			c[i*3+k] = l.Pix[i*3+k] - Y
		}
	}
	return
}

// highPass is v - blur(v) at radius r (fraction of the width).
func highPass(v []float32, w, h int, r float64) []float32 {
	b := render.GaussBlur(v, w, h, math.Max(0.5, r*float64(w)))
	out := make([]float32, len(v))
	for i := range v {
		out[i] = v[i] - b[i]
	}
	return out
}

// fitLocal fits the highlight chroma curve and the high-pass amounts on
// prediction/SPP pairs.
func fitLocal(preds, spps []*render.Linear, radii []float64, chroma bool) localFit {
	var f localFit
	// Chroma: per brightness bin, least-squares scale of pred chroma to SPP's.
	if chroma {
		var num, den, sq [localBins]float64
		for p := range preds {
			yp, cp := splitChroma(preds[p])
			_, cs := splitChroma(spps[p])
			for i := range yp {
				b := min(localBins-1, int(yp[i]*localBins))
				for k := 0; k < 3; k++ {
					num[b] += float64(cp[i*3+k] * cs[i*3+k])
					den[b] += float64(cp[i*3+k] * cp[i*3+k])
					sq[b] += float64(cs[i*3+k] * cs[i*3+k])
				}
			}
		}
		// A regression of SPP's chroma on the (noisy) prediction's is biased
		// towards 0; compare chroma strength (RMS) instead, and only where
		// SPP is known to act: the highlights. Below, the scale stays 1.
		for b := range f.chroma {
			f.chroma[b] = 1
			if b >= HighlightFrom && den[b] > 1e-9 {
				f.chroma[b] = math.Min(1, math.Sqrt(sq[b]/den[b]))
			}
		}
	}
	// High-pass: regress the encoded-luma residual on the high-pass bands.
	if len(radii) > 0 {
		k := len(radii)
		ata := make([]float64, k*k)
		atb := make([]float64, k)
		for p := range preds {
			yp, _ := splitChroma(preds[p])
			ys, _ := splitChroma(spps[p])
			bands := make([][]float32, k)
			for j, r := range radii {
				bands[j] = highPass(yp, preds[p].W, preds[p].H, r)
			}
			for i := range yp {
				d := float64(ys[i] - yp[i])
				for a := 0; a < k; a++ {
					atb[a] += float64(bands[a][i]) * d
					for b := 0; b < k; b++ {
						ata[a*k+b] += float64(bands[a][i]) * float64(bands[b][i])
					}
				}
			}
		}
		for a := 0; a < k; a++ {
			ata[a*k+a] += 1e-6 + 1e-4*ata[a*k+a]
		}
		f.amount = solve(ata, atb, k)
	}
	return f
}

// applyLocal applies a local fit to l (a copy is returned).
func applyLocal(l *render.Linear, f localFit, radii []float64, chroma bool) *render.Linear {
	y, c := splitChroma(l)
	y2 := append([]float32(nil), y...)
	for j, r := range radii {
		if j >= len(f.amount) {
			break
		}
		hp := highPass(y, l.W, l.H, r)
		a := float32(f.amount[j])
		for i := range y2 {
			y2[i] += a * hp[i]
		}
	}
	out := &render.Linear{W: l.W, H: l.H, Pix: make([]float32, len(l.Pix))}
	for i := range y {
		Y := render.ToLinear(max(0, min(1, y2[i])))
		s := float32(1)
		if chroma {
			// Interpolate the chroma scale between bin centres.
			fb := math.Max(0, math.Min(localBins-1, float64(y[i])*localBins-0.5))
			b0 := min(int(fb), localBins-2)
			t := fb - float64(b0)
			s = float32(f.chroma[b0]*(1-t) + f.chroma[b0+1]*t)
		}
		for k := 0; k < 3; k++ {
			out.Pix[i*3+k] = max(0, Y+c[i*3+k]*s)
		}
	}
	return out
}

// RawLocalResearch reports, held out, what highlight desaturation and
// high-pass local contrast add on top of the global raw base.
func RawLocalResearch(family string, pairs []RawPair, w io.Writer) {
	n := len(pairs)
	if n < 3 {
		return
	}
	// Held-out global predictions for every photo.
	preds := make([]*render.Linear, n)
	for h := range pairs {
		var train []RawPair
		for i := range pairs {
			if i != h {
				train = append(train, pairs[i])
			}
		}
		fc := fitRaw(train)
		preds[h] = applied(&fc, pairs[h].Raw)
	}
	type cand struct {
		name   string
		radii  []float64
		chroma bool
	}
	// Earlier run (Oct 2026): high-pass at any radius gained <1% (SD1 6.29
	// -> 6.24, SD15 11.34 -> 11.24); a regression-fitted chroma curve hurt.
	cands := []cand{{"global raw base only", nil, false}}
	for _, from := range []int{8, 10, 12, 14} {
		cands = append(cands, cand{fmt.Sprintf("+ highlight chroma from bin %d", from), nil, true})
	}
	fmt.Fprintf(w, "\n%s, %d photos, held out (8-bit mean error):\n", family, n)
	for _, c := range cands {
		fmt.Sscanf(c.name, "+ highlight chroma from bin %d", &HighlightFrom)
		var sum float64
		var lastFit localFit
		for h := range pairs {
			var tp, ts []*render.Linear
			for i := range pairs {
				if i != h {
					tp, ts = append(tp, preds[i]), append(ts, pairs[i].SPP)
				}
			}
			f := fitLocal(tp, ts, c.radii, c.chroma)
			lastFit = f
			sum += MeanError(applyLocal(preds[h], f, c.radii, c.chroma), pairs[h].SPP)
		}
		fmt.Fprintf(w, "  %-28s %6.2f", c.name, sum/float64(n))
		if len(lastFit.amount) > 0 {
			fmt.Fprintf(w, "   amounts %.2f", lastFit.amount)
		}
		if c.chroma {
			fmt.Fprintf(w, "   chroma by brightness %.2f", lastFit.chroma)
		}
		fmt.Fprintln(w)
	}
}

// HighlightFrom is the first brightness bin (of localBins) the highlight
// chroma curve may act on.
var HighlightFrom = 10
