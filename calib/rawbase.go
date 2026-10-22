package calib

import (
	"fmt"
	"io"
	"math"

	"github.com/biopoetic/foveon-lab/render"
)

// RawPair is one photo as SPP renders it neutrally (its A00 export) and as
// x3f_extract decodes its raw data, both at the analysis size.
type RawPair struct {
	Name     string
	Raw, SPP *render.Linear
	// JPEGErr: how far the camera-JPEG preview is from SPP (8-bit), the
	// baseline the raw base has to beat.
	JPEGErr float64
	WB      string         // in-camera white balance (X3F WB_DESC), for diagnostics
	Info    string         // other capture settings, for diagnostics
	JPEG    *render.Linear // the camera JPEG as the app's neutral shows it (nil if unreadable)
	// TrainOnly: a synthetic chart (tools/synth) exported from SPP. It
	// teaches the fit colours real photos rarely contain, but is never
	// held out or counted in the scores, which stay about real photos.
	TrainOnly bool
	// Neutral renders a mapped raw decode as the app's neutral preview
	// (tone model, sharpening); nil compares the mapping itself. Scores
	// taken without it judged a different image than the app shows: on
	// 2026-10-20 a refit scoring 4.0 against the shipped base's 4.7 made
	// the app worse (13 photos 4.31 -> 4.47).
	Neutral func(l *render.Linear) *render.Linear
}

// view is what pair p's error is measured on: the app's rendering of l when
// the pair has one.
func (p *RawPair) view(l *render.Linear) *render.Linear {
	if p.Neutral != nil {
		return p.Neutral(l)
	}
	return l
}

// ScoreRawBase is c's mean error over the real (not TrainOnly) pairs, each
// through its view; NaN when there are none.
func ScoreRawBase(pairs []RawPair, c *render.RawCorrection) float64 {
	var sum float64
	n := 0
	for i := range pairs {
		if pairs[i].TrainOnly {
			continue
		}
		sum += MeanError(pairs[i].view(applied(c, pairs[i].Raw)), pairs[i].SPP)
		n++
	}
	return sum / float64(n)
}

// MeanError is the mean abs difference (8-bit sRGB) between two images.
func MeanError(a, b *render.Linear) float64 {
	var e float64
	for i := range a.Pix {
		e += math.Abs(float64(render.ToSRGB(a.Pix[i]) - render.ToSRGB(b.Pix[i])))
	}
	return e / float64(len(a.Pix)) * 255
}

func fitRaw(pairs []RawPair) render.RawCorrection {
	var c render.RawCorrection
	var ata, atb [3][3]float64
	for _, p := range pairs {
		for i := 0; i < len(p.SPP.Pix); i += 6 { // every other pixel
			if max(p.SPP.Pix[i], p.SPP.Pix[i+1], p.SPP.Pix[i+2], p.Raw.Pix[i], p.Raw.Pix[i+1], p.Raw.Pix[i+2]) > 0.95 {
				continue
			}
			for r := 0; r < 3; r++ {
				for q := 0; q < 3; q++ {
					ata[r][q] += float64(p.Raw.Pix[i+r]) * float64(p.Raw.Pix[i+q])
					atb[r][q] += float64(p.SPP.Pix[i+r]) * float64(p.Raw.Pix[i+q])
				}
			}
		}
	}
	inv, ok := inverse3(ata)
	if !ok {
		c.Matrix = [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	} else {
		for r := 0; r < 3; r++ {
			for q := 0; q < 3; q++ {
				for k := 0; k < 3; k++ {
					c.Matrix[r*3+q] += atb[r][k] * inv[k][q]
				}
			}
		}
	}
	// Tone curve per channel after the matrix: mean SPP output per input bin.
	var sum, cnt [3][64]float64
	m := c.Matrix
	for _, p := range pairs {
		for i := 0; i < len(p.SPP.Pix); i += 3 {
			r, g, b := float64(p.Raw.Pix[i]), float64(p.Raw.Pix[i+1]), float64(p.Raw.Pix[i+2])
			v := [3]float64{m[0]*r + m[1]*g + m[2]*b, m[3]*r + m[4]*g + m[5]*b, m[6]*r + m[7]*g + m[8]*b}
			for k := 0; k < 3; k++ {
				if v[k] <= 0 {
					continue // clipped: not a dark value, would pollute bin 0
				}
				e := float64(render.ToSRGB(float32(math.Max(0, math.Min(1, v[k])))))
				bin := min(63, int(e*64))
				sum[k][bin] += float64(render.ToSRGB(p.SPP.Pix[i+k]))
				cnt[k][bin]++
			}
		}
	}
	for k := 0; k < 3; k++ {
		for b := 0; b < 64; b++ {
			c.Curves[k][b] = -1
			if cnt[k][b] > 50 {
				c.Curves[k][b] = sum[k][b] / cnt[k][b]
			}
		}
	}
	c.MakeMonotone()
	return c
}

func applied(c *render.RawCorrection, l *render.Linear) *render.Linear {
	o := *l // keep scale, ISO, sensor: the app's rendering needs them
	o.Pix = append([]float32(nil), l.Pix...)
	c.Apply(&o)
	return &o
}

// FitRawBase fits the raw → SPP-neutral mapping for one camera family on
// all its pairs, and judges it honestly: each photo is predicted by a fit
// on the OTHER photos. The raw base is only switched on when that held-out
// error clearly beats the camera JPEG (and there are enough photos).
//
// The shipped base's hue correction (HueGain/HueRot, which fitRaw does not
// fit) is carried into the refit and every held-out fold.
func FitRawBase(family string, pairs []RawPair) render.RawCorrection {
	// Fit on Auto-white-balance photos only: SPP's neutral export does not
	// follow the recorded illuminant (the SD15 kronometric set records
	// Overcast but SPP renders it closest to Sunlight - dE 13.25 against
	// 10.81, 22 Oct 2026), so a fixed-WB photo would pull the fit toward the
	// wrong balance. Photos without a WB_DESC count as Auto.
	fit := make([]RawPair, 0, len(pairs))
	for _, p := range pairs {
		if p.WB == "" || p.WB == "Auto" {
			fit = append(fit, p)
		}
	}
	autoWB := false
	if len(fit) >= 3 {
		pairs = fit
		autoWB = true
	}
	var hg, hr []float64
	if s := render.RawBaseFitted(family, ""); s != nil {
		hg, hr = s.HueGain, s.HueRot
	}
	c := fitRaw(pairs)
	c.SetHue(hg, hr)
	real := 0
	for _, p := range pairs {
		if !p.TrainOnly {
			real++
		}
	}
	c.Family, c.Photos = family, real
	if autoWB {
		// The fit kept Auto-WB photos only, so the entry is for that balance:
		// it keys as "family|Auto" and leaves the family's WB-less base (what
		// fixed-WB photos use) alone.
		c.WB = "Auto"
	}
	if real < 3 {
		return c
	}
	var raw, jpg float64
	for h := range pairs {
		if pairs[h].TrainOnly {
			continue
		}
		var train []RawPair
		for i := range pairs {
			if i != h {
				train = append(train, pairs[i])
			}
		}
		fc := fitRaw(train)
		fc.SetHue(hg, hr)
		raw += MeanError(pairs[h].view(applied(&fc, pairs[h].Raw)), pairs[h].SPP)
		jpg += pairs[h].JPEGErr
	}
	c.HeldOutRaw, c.HeldOutJPEG = raw/float64(real), jpg/float64(real)
	c.UseRaw = c.HeldOutRaw < 0.8*c.HeldOutJPEG
	return c
}

// LoadLinear reads an export (TIFF/JPEG) at the analysis size.
func LoadLinear(path string) (*render.Linear, error) { return load(path) }

// JPEGPair is a camera-JPEG decode and its SPP neutral export, with the
// photo's in-camera white balance mode.
type JPEGPair struct {
	Name    string
	JPEG    *render.Linear
	SPP     *render.Linear
	AutoWB  bool
	Neutral func(l *render.Linear) *render.Linear // renders l as the app's neutral preview
}

func fitJPEGCoef(ps []JPEGPair, ridge float64) [3][6]float64 {
	var coef [3][6]float64
	for k := 0; k < 3; k++ {
		var a [6][6]float64
		var b [6]float64
		for _, p := range ps {
			f := render.JPEGBalanceFeatures(p.JPEG, p.AutoWB)
			n := p.Neutral(p.JPEG)
			var sn, ss float64
			for i := k; i < len(n.Pix); i += 3 {
				sn += float64(n.Pix[i])
				ss += float64(p.SPP.Pix[i])
			}
			y := math.Log(ss / sn)
			for i := 0; i < 6; i++ {
				b[i] += f[i] * y
				for j := 0; j < 6; j++ {
					a[i][j] += f[i] * f[j]
				}
			}
		}
		for i := 1; i < 6; i++ {
			a[i][i] += ridge
		}
		coef[k] = solve6(a, b)
	}
	return coef
}

func solve6(a [6][6]float64, b [6]float64) [6]float64 {
	n := 6
	for c := 0; c < n; c++ {
		p := c
		for r := c + 1; r < n; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		a[c], a[p] = a[p], a[c]
		b[c], b[p] = b[p], b[c]
		if a[c][c] == 0 {
			continue
		}
		for r := c + 1; r < n; r++ {
			f := a[r][c] / a[c][c]
			for k := c; k < n; k++ {
				a[r][k] -= f * a[c][k]
			}
			b[r] -= f * b[c]
		}
	}
	var x [6]float64
	for r := n - 1; r >= 0; r-- {
		if a[r][r] == 0 {
			continue
		}
		s := b[r]
		for k := r + 1; k < n; k++ {
			s -= a[r][k] * x[k]
		}
		x[r] = s / a[r][r]
	}
	return x
}

// FitJPEGBalance fits the per-photo JPEG balance for a family and judges it
// leave-one-out through the app's real path (gains on the source, then the
// neutral render); it is switched on only when that clearly helps.
func FitJPEGBalance(family string, ps []JPEGPair) render.JPEGBalance {
	const ridge = 0.01
	out := render.JPEGBalance{Family: family, Photos: len(ps), Coef: fitJPEGCoef(ps, ridge)}
	if len(ps) < 6 {
		return out
	}
	out.Tone = fitTone(ps, out.Coef)
	var with, without, tone float64
	for h := range ps {
		var train []JPEGPair
		for i := range ps {
			if i != h {
				train = append(train, ps[i])
			}
		}
		c := fitJPEGCoef(train, ridge)
		src := balanced(ps[h], c)
		with += MeanError(ps[h].Neutral(src), ps[h].SPP)
		without += MeanError(ps[h].Neutral(ps[h].JPEG), ps[h].SPP)
		tc := fitTone(train, c)
		render.ApplyTone(src, render.PredictTone(&tc, render.ToneFeatures(src, ps[h].AutoWB)))
		tone += MeanError(ps[h].Neutral(src), ps[h].SPP)
	}
	n := float64(len(ps))
	out.HeldOut, out.Without, out.HeldOutTone = with/n, without/n, tone/n
	out.Use = out.HeldOut < 0.95*out.Without
	out.ToneUse = out.Use && out.HeldOutTone < 0.97*out.HeldOut
	return out
}

func balanced(p JPEGPair, c [3][6]float64) *render.Linear {
	src := &render.Linear{W: p.JPEG.W, H: p.JPEG.H, Pix: append([]float32(nil), p.JPEG.Pix...)}
	f := render.JPEGBalanceFeatures(src, p.AutoWB)
	for k := 0; k < 3; k++ {
		var v float64
		for i := range f {
			v += c[k][i] * f[i]
		}
		g := float32(math.Exp(math.Max(-1.5, math.Min(1.5, v))))
		for i := k; i < len(src.Pix); i += 3 {
			src.Pix[i] *= g
		}
	}
	return src
}

// fitTone regresses, per brightness bin, the brightness gap between the
// app's neutral preview (after the balance) and SPP on ToneFeatures.
func fitTone(ps []JPEGPair, c [3][6]float64) [render.ToneBins][6]float64 {
	var out [render.ToneBins][6]float64
	var a [render.ToneBins][6][6]float64
	var b [render.ToneBins][6]float64
	for _, p := range ps {
		src := balanced(p, c)
		f := render.ToneFeatures(src, p.AutoWB)
		neu := p.Neutral(src)
		var s, n [render.ToneBins]float64
		for i := 0; i < neu.W*neu.H; i++ {
			x := float64(render.ToSRGB(luma(neu.Pix, i*3)))
			bin := min(render.ToneBins-1, int(x*render.ToneBins))
			s[bin] += float64(render.ToSRGB(luma(p.SPP.Pix, i*3))) - x
			n[bin]++
		}
		for bin := 0; bin < render.ToneBins; bin++ {
			if n[bin] < 30 {
				continue
			}
			d := s[bin] / n[bin]
			for i := 0; i < 6; i++ {
				b[bin][i] += f[i] * d
				for j := 0; j < 6; j++ {
					a[bin][i][j] += f[i] * f[j]
				}
			}
		}
	}
	for bin := range out {
		for i := 1; i < 6; i++ {
			a[bin][i][i] += 0.05
		}
		if a[bin][0][0] > 0 {
			out[bin] = solve6(a[bin], b[bin])
		}
	}
	return out
}

// FitJPEGCoefs fits the balance predictor on ps without judging it.
func FitJPEGCoefs(ps []JPEGPair) [3][6]float64 { return fitJPEGCoef(ps, 0.01) }

// DiagnoseRaw splits each photo's held-out raw-base error into what a
// per-photo correction could remove: "gains" = the held-out fit plus the
// photo's own three channel gains (a perfect white balance), "own fit" =
// a matrix and curves fitted on this photo alone (the best any global
// colour/tone transform can do). What is left after "own fit" is local:
// noise, sharpness, highlights, Fill-like operators.
func DiagnoseRaw(family string, pairs []RawPair, w io.Writer) {
	fmt.Fprintf(w, "\n%s raw base, per photo (8-bit mean error; ideal gains in stops: G, R-G, B-G):\n%-10s %-9s %7s %7s %7s %6s %6s %6s  %s\n", family, "photo", "WB", "heldout", "+gains", "ownfit", "G", "R-G", "B-G", "info")
	var s0, s1, s2, sw, ss, sj float64
	for h := range pairs {
		var train []RawPair
		for i := range pairs {
			if i != h {
				train = append(train, pairs[i])
			}
		}
		fc := fitRaw(train)
		pred := applied(&fc, pairs[h].Raw)
		e0 := MeanError(pred, pairs[h].SPP)
		var a, b [3]float64
		for i := 0; i < len(pred.Pix); i += 3 {
			if max(pairs[h].SPP.Pix[i], pairs[h].SPP.Pix[i+1], pairs[h].SPP.Pix[i+2]) > 0.95 {
				continue
			}
			for k := 0; k < 3; k++ {
				a[k] += float64(pred.Pix[i+k])
				b[k] += float64(pairs[h].SPP.Pix[i+k])
			}
		}
		g := &render.Linear{W: pred.W, H: pred.H, Pix: append([]float32(nil), pred.Pix...)}
		for i := range g.Pix {
			g.Pix[i] = float32(math.Min(1, float64(g.Pix[i])*b[i%3]/a[i%3]))
		}
		e1 := MeanError(g, pairs[h].SPP)
		own := fitRaw(pairs[h : h+1])
		e2 := MeanError(applied(&own, pairs[h].Raw), pairs[h].SPP)
		lg := func(k int) float64 { return math.Log2(b[k] / a[k]) }
		// Per-WB-setting gains learned from the OTHER photos with the same
		// in-camera white balance (relative to the same held-out fit).
		var wsum [3]float64
		var wn float64
		for _, tp := range train {
			if tp.WB != pairs[h].WB {
				continue
			}
			gt := oracleGains(applied(&fc, tp.Raw), tp.SPP)
			for k := 0; k < 3; k++ {
				wsum[k] += math.Log(gt[k])
			}
			wn++
		}
		ew := e0
		if wn > 0 {
			wg := &render.Linear{W: pred.W, H: pred.H, Pix: append([]float32(nil), pred.Pix...)}
			for i := range wg.Pix {
				wg.Pix[i] = float32(math.Min(1, float64(wg.Pix[i])*math.Exp(wsum[i%3]/wn)))
			}
			ew = MeanError(wg, pairs[h].SPP)
		}
		sw += ew
		// Scene-predicted gains (an auto white balance, as SPP's WB code 1
		// may be): log gains regressed on the image's mean colour, learned
		// on the other photos.
		var ata [3][3]float64
		var atb [3][3]float64
		for _, tp := range train {
			pt := applied(&fc, tp.Raw)
			f := meanColourFeatures(pt)
			gt := oracleGains(pt, tp.SPP)
			for r := 0; r < 3; r++ {
				for c := 0; c < 3; c++ {
					ata[r][c] += f[r] * f[c]
				}
				for k := 0; k < 3; k++ {
					atb[k][r] += f[r] * math.Log(gt[k])
				}
			}
		}
		for r := 0; r < 3; r++ {
			ata[r][r] += 0.05 // ridge
		}
		es := e0
		if inv, ok := inverse3(ata); ok {
			f := meanColourFeatures(pred)
			var lg3 [3]float64
			for k := 0; k < 3; k++ {
				for r := 0; r < 3; r++ {
					var c float64
					for q := 0; q < 3; q++ {
						c += inv[r][q] * atb[k][q]
					}
					lg3[k] += c * f[r]
				}
			}
			sg := &render.Linear{W: pred.W, H: pred.H, Pix: append([]float32(nil), pred.Pix...)}
			for i := range sg.Pix {
				sg.Pix[i] = float32(math.Min(1, float64(sg.Pix[i])*math.Exp(math.Max(-1.5, math.Min(1.5, lg3[i%3])))))
			}
			es = MeanError(sg, pairs[h].SPP)
		}
		ss += es
		// JPEG-guided gains: the camera JPEG comes from Sigma's own colour
		// engine; regress the raw's ideal log gains on the JPEG-vs-raw
		// colour difference, learned on the other photos.
		ej := e0
		if pairs[h].JPEG != nil {
			var A [3][3]float64
			var B [3][3]float64
			n := 0
			for _, tp := range train {
				if tp.JPEG == nil {
					continue
				}
				pt := applied(&fc, tp.Raw)
				f := jpegRawFeatures(tp.JPEG, pt)
				gt := oracleGains(pt, tp.SPP)
				for r := 0; r < 3; r++ {
					for c := 0; c < 3; c++ {
						A[r][c] += f[r] * f[c]
					}
					for k := 0; k < 3; k++ {
						B[k][r] += f[r] * math.Log(gt[k])
					}
				}
				n++
			}
			for r := 0; r < 3; r++ {
				A[r][r] += 0.05
			}
			if inv, ok := inverse3(A); ok && n >= 3 {
				f := jpegRawFeatures(pairs[h].JPEG, pred)
				var lg3 [3]float64
				for k := 0; k < 3; k++ {
					for r := 0; r < 3; r++ {
						var c float64
						for q := 0; q < 3; q++ {
							c += inv[r][q] * B[k][q]
						}
						lg3[k] += c * f[r]
					}
				}
				jg := &render.Linear{W: pred.W, H: pred.H, Pix: append([]float32(nil), pred.Pix...)}
				for i := range jg.Pix {
					jg.Pix[i] = float32(math.Min(1, float64(jg.Pix[i])*math.Exp(math.Max(-1.5, math.Min(1.5, lg3[i%3])))))
				}
				ej = MeanError(jg, pairs[h].SPP)
			}
		}
		sj += ej
		fmt.Fprintf(w, "%-10s %-9s %7.1f %7.1f %7.1f %+6.2f %+6.2f %+6.2f  %s\n", pairs[h].Name, pairs[h].WB, e0, e1, e2, lg(1), lg(0)-lg(1), lg(2)-lg(1), pairs[h].Info)
		s0, s1, s2 = s0+e0, s1+e1, s2+e2
	}
	n := float64(len(pairs))
	fmt.Fprintf(w, "held out with per-WB-setting gains: %.1f, with scene-predicted gains: %.1f, JPEG-guided gains: %.1f\n", sw/n, ss/n, sj/n)
	fmt.Fprintf(w, "%-10s %-9s %7.1f %7.1f %7.1f\n", "MEAN", "", s0/n, s1/n, s2/n)
}

// oracleGains are the per-channel gains that best match pred's channel
// means to spp's (unclipped pixels).
func oracleGains(pred, spp *render.Linear) [3]float64 {
	var a, b [3]float64
	for i := 0; i < len(pred.Pix); i += 3 {
		if max(spp.Pix[i], spp.Pix[i+1], spp.Pix[i+2]) > 0.95 {
			continue
		}
		for k := 0; k < 3; k++ {
			a[k] += float64(pred.Pix[i+k])
			b[k] += float64(spp.Pix[i+k])
		}
	}
	return [3]float64{b[0] / a[0], b[1] / a[1], b[2] / a[2]}
}

// meanColourFeatures is [1, ln(R/G), ln(B/G)] of an image's mean colour.
func meanColourFeatures(l *render.Linear) [3]float64 {
	var s [3]float64
	for i := 0; i < len(l.Pix); i += 3 {
		for k := 0; k < 3; k++ {
			s[k] += float64(l.Pix[i+k])
		}
	}
	return [3]float64{1, math.Log(s[0] / s[1]), math.Log(s[2] / s[1])}
}

// jpegRawFeatures is [1, d(R/G), d(B/G)]: how much redder / bluer the
// camera JPEG is than the raw prediction (log ratios of mean colours).
func jpegRawFeatures(jpeg, raw *render.Linear) [3]float64 {
	a, b := meanColourFeatures(jpeg), meanColourFeatures(raw)
	return [3]float64{1, a[1] - b[1], a[2] - b[2]}
}

// FitMapping is the colour mapping (matrix + per-channel curves) that best
// turns each pair's Raw rendering into its SPP one, as the white-balance
// and colour-mode steps are fitted.
func FitMapping(pairs []RawPair) render.RawCorrection { return fitRaw(pairs) }
