package render

// A lab scanner's per-frame levels for a colour negative (task 89).
//
// A Frontier or a Noritsu does not print a negative at one fixed exposure
// and contrast. It reads the frame's densities, sets a black point and a
// white point (per channel) from the film base and the densest highlight,
// and maps the range between them onto its output. On a thin, underexposed
// negative that range is short and the scanner stretches it; on a dark frame
// the range sits low and the scanner opens it up.
//
// Foveon Lab's scan (newColorScan) has one fixed curve for every frame: the
// film's mid-grey lands at 18% and a density of zero (the film base) at
// black. On a dark frame the same curve puts the frame's tones low and
// steep, so the shadows go black and a bright shirt clips. The fix here is
// the scanner's: read the frame's density percentiles on the same grid as
// FrameLogKey, and map the frame's black and white points onto the range an
// ordinary frame covers. A frame whose points already are that range is
// unchanged.
//
// The map is the scanner's. The frame's 0.5% and 99.5% densities are its
// black and white points; the middle of the scale is a gamma that puts the
// frame's key where an ordinary frame's key sits. The map is anchored on the
// film's own mid-grey density per channel, so a neutral stays neutral; each
// channel's own white point takes over at the highlight end, so a saturated
// highlight is rolled off instead of clipped, while a mid-tone's colour does
// not move. A frame whose points already are an ordinary frame's, and whose
// key is inside the lab's quarter-stop tolerance, is left exactly as it is.

import (
	"math"
	"sort"
	"strconv"
)

// ScanLevels is one frame's scanner levels: the frame's black and white
// points in the negative's density, the midpoint (gamma) that puts the
// frame's key where an ordinary frame's key sits, and the mix into the
// fixed curve (0 = an ordinary frame, untouched; 1 = the full correction).
// The map is applied around the film's own mid-grey density per channel, so
// it cannot move the colour balance; it is monotone and its top is a soft
// shoulder. It is computed once per photo and cached, so the grid, the
// editor, a 1:1 window and the export all scan the frame alike.
type ScanLevels struct {
	Lo, Hi float64    // the frame's black and white density points (luminance)
	LoCh   [3]float64 // the same points per channel: the highlight roll-off
	HiCh   [3]float64
	Mid    [3]float64 // the film's mid-grey density above base, per channel
	MidY   float64    // the luminance of Mid
	Range  float64    // the output density range (the ordinary range, capped for a thin frame)
	Gamma  float64    // the midpoint: a gamma on the normalised density
	Mix    float64    // 0 = the fixed curve, 1 = the levels
}

// apply maps one channel's base-relative density through the scanner's
// levels and mixes it with the fixed curve's own reading. The channel's
// film mid-grey is the anchor, so the film's neutral axis stays neutral;
// the neutral tube in the table then pulls a bright neutral back onto it.
func (lv *ScanLevels) apply(k int, e float64) float64 {
	if lv == nil || lv.Mix == 0 {
		return e
	}
	r := lv.Hi - lv.Lo
	if r < 1e-6 {
		return e
	}
	// tL is the channel's offset from the film's mid-grey, placed on the
	// frame's black-to-white range. Using the offset (not the raw density)
	// keeps the film's neutral axis neutral: at mid-grey it is the same for
	// all three channels. A bright, saturated colour's own channel range
	// (tC) takes over towards the white point, which is where a scanner's
	// per-channel white point rolls a colour off instead of clipping it;
	// the middle of the scale stays the luminance map, so a mid-tone's
	// colour does not move.
	tL := (e - lv.Mid[k] - lv.Lo + lv.MidY) / r
	t := tL
	if rk := lv.HiCh[k] - lv.LoCh[k]; rk > 1e-6 {
		tC := (e - lv.LoCh[k]) / rk
		w := (tL - 0.60) / 0.35
		switch {
		case w <= 0:
			w = 0
		case w >= 1:
			w = 1
		default:
			w = w * w * (3 - 2*w)
		}
		t = tL + w*(tC-tL)
	}
	if t <= 0 {
		t = 0
	}
	var tp float64
	if t <= 1 {
		tp = math.Pow(t, lv.Gamma)
	} else {
		// Above the frame's white point the curve rolls into a soft
		// shoulder: it approaches a small fixed top, so the few tones above
		// the measured white point cannot run far past an ordinary frame's
		// white and clip. The slope at the join is the gamma itself.
		const top = 1.04
		tp = 1 + (top-1)*(1-math.Exp(-lv.Gamma*(t-1)/(top-1)))
	}
	out := lv.Mid[k] + (scanOrdBlack - lv.MidY) + tp*lv.Range
	return e + lv.Mix*(out-e)
}

// scanPcts are the density percentiles read off a frame: the black point,
// the white point, and the shape between them. The same list is used for
// the report's measurements.
var scanPcts = [13]float64{0.5, 1, 2, 5, 10, 25, 50, 75, 90, 95, 98, 99, 99.5}

// ScanStats is what a lab scanner reads off one frame: its key and the
// percentiles of its luminance density above the film base, on the same
// grid as FrameLogKey. Densities are the scan's own forward path (the sRGB
// layer map and the film's characteristic curves), not the scene's light.
type ScanStats struct {
	Key  float64
	W, H int
	Dmin [3]float64
	// Pct is the luminance density (0.2126 R + 0.7152 G + 0.0722 B, each
	// channel's density above its own base) at scanPcts.
	Pct [13]float64
	// ChPct is each channel's density above its base at 0.5, 1, 5, 50, 95,
	// 99 and 99.5%.
	ChPct [3][7]float64
}

// ScanStatsFor reads a frame's negative densities for film at push, on the
// scan's forward path. ok is false when the film has no colour-negative
// model or the frame is empty.
func ScanStatsFor(l *Linear, film string, push float64) (st ScanStats, ok bool) {
	if l == nil || l.W < 1 || l.H < 1 {
		return ScanStats{}, false
	}
	f := colorFilmOnPush(film, PaperScan, "sRGB", push, ColorPrint{})
	if f == nil {
		return ScanStats{}, false
	}
	var dmin [3]float64
	for k := range dmin {
		dmin[k] = f.curve[k][0][1]
		for _, p := range f.curve[k] {
			dmin[k] = math.Min(dmin[k], p[1])
		}
	}
	st.W, st.H, st.Dmin = l.W, l.H, dmin
	step := max(1, l.W/200)
	var ys []float64
	var ch [3][]float64
	n := 0
	for y := step / 2; y < l.H; y += step {
		for x := step / 2; x < l.W; x += step {
			i := (y*l.W + x) * 3
			r, g, b := l.Pix[i], l.Pix[i+1], l.Pix[i+2]
			xf := rpFeatures(r, g, b)
			var e [3]float64
			for k := 0; k < 3; k++ {
				h := float64(f.rp[k*3]*xf[0] + f.rp[k*3+1]*xf[1] + f.rp[k*3+2]*xf[2] +
					f.rp[9+k*3]*xf[3] + f.rp[9+k*3+1]*xf[4] + f.rp[9+k*3+2]*xf[5])
				D := f.density(k, math.Log2(math.Max(h*f.hWhite, 1e-6))*ln10ln2)
				e[k] = D - dmin[k]
				ch[k] = append(ch[k], e[k])
			}
			ys = append(ys, 0.2126*e[0]+0.7152*e[1]+0.0722*e[2])
			n++
		}
	}
	if n == 0 {
		return ScanStats{}, false
	}
	sort.Float64s(ys)
	for i, p := range scanPcts {
		st.Pct[i] = scanPct(ys, p)
	}
	for k := 0; k < 3; k++ {
		sort.Float64s(ch[k])
		for i, p := range [7]float64{0.5, 1, 5, 50, 95, 99, 99.5} {
			st.ChPct[k][i] = scanPct(ch[k], p)
		}
	}
	st.Key = FrameLogKey(l)
	return st, true
}

// scanPct is the p-th percentile of a sorted slice, interpolated between
// the two neighbouring samples.
func scanPct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	x := p / 100 * float64(len(sorted)-1)
	i := int(math.Floor(x))
	if i < 0 {
		i = 0
	}
	if i >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	t := x - float64(i)
	return sorted[i] + t*(sorted[i+1]-sorted[i])
}

// The scanner's ordinary range: the black and white points of an ordinary
// frame, i.e. the luminance density (above the film base) an ordinary frame
// covers at its 0.5% and 99.5% points. Measured on the ten sample photos
// nearest negKeyRef (task 89): black 0.000, white 0.9935. A frame with this
// range is left as the fixed curve renders it.
var (
	scanOrdBlack = 0.0
	scanOrdWhite = 0.9935
)

// The lab's exposure tolerance: a frame within a quarter stop of an ordinary
// one gets no levels at all (the scanner leaves a correctly exposed frame
// alone), and the correction is full by 1.5 stops under. This is not fitted
// to one photo; it is the same quarter-stop tolerance the print's own
// correction range starts from, and it is what keeps the ten nearest frames
// byte for byte as they were.
//
// scanMinRange is the density range at which the scanner reaches its full
// output swing; a thinner negative is stretched less (down to a quarter of
// the swing), so a nearly blank frame does not become a field of amplified
// grain.
const (
	scanLevelsStart = 0.25
	scanLevelsFull  = 1.5
	scanMinRange    = 0.40
)

// scanMidDensities is the film's mid-grey density above base per channel
// and its luminance: the anchor the scanner's tone map is applied around.
func scanMidDensities(f *colorFilm) (mid [3]float64, midY float64) {
	var dmin [3]float64
	for k := range dmin {
		dmin[k] = f.curve[k][0][1]
		for _, p := range f.curve[k] {
			dmin[k] = math.Min(dmin[k], p[1])
		}
	}
	lgGrey := math.Log10(scanMidGrey * f.hWhite)
	for k := 0; k < 3; k++ {
		mid[k] = f.density(k, lgGrey) - dmin[k]
	}
	return mid, 0.2126*mid[0] + 0.7152*mid[1] + 0.0722*mid[2]
}

// scanLevelsFor turns one frame's density percentiles into the scanner's
// levels. The frame's 0.5% and 99.5% luminance densities are its black and
// white points; the frame's density is normalised between them and the
// ordinary frame's points. A gamma then sets where the frame's own key
// lands: the exponent that puts the key's density at the same fraction of
// the range that an ordinary frame's key has. A thin, underexposed negative
// (its whole range low) is therefore stretched and opened up; a dark frame
// with a normal highlight keeps that highlight at the ordinary white point,
// so the lift cannot push it past what an ordinary frame already does.
func scanLevelsFor(st ScanStats, f *colorFilm) *ScanLevels {
	lo, hi := st.Pct[0], st.Pct[12]
	if hi-lo < 0.002 {
		// A frame with no density range at all (a lens cap, fog): there is
		// no picture to open up.
		return nil
	}
	ev := NegAutoEV(st.Key)
	if ev <= scanLevelsStart {
		return nil
	}
	mix := math.Min(1, (ev-scanLevelsStart)/(scanLevelsFull-scanLevelsStart))
	mid, midY := scanMidDensities(f)
	// The black and white point per channel: the frame's own 0.5% and
	// 99.5% density in each channel. A saturated highlight (a red flower, a
	// red channel much denser than the luminance) therefore gets its own
	// white point and cannot run past it, which is what keeps the levels
	// from clipping a colour that the print's paper keeps.
	var lv ScanLevels
	lv.Lo, lv.Hi = lo, hi
	for k := 0; k < 3; k++ {
		lv.LoCh[k], lv.HiCh[k] = st.ChPct[k][0], st.ChPct[k][6]
		if lv.HiCh[k]-lv.LoCh[k] < 0.002 {
			lv.LoCh[k], lv.HiCh[k] = lo, hi
		}
	}
	lv.Mid, lv.MidY = mid, midY
	// The density of the frame's key: the film records a scene at
	// scanNegGamma over the log exposure, and scanMidGrey is the linear
	// scene light of the model's 18% grey.
	eKey := midY + scanNegGamma*math.Log10(st.Key/scanMidGrey)
	eRef := midY + scanNegGamma*math.Log10(negKeyRef/scanMidGrey)
	tKey := (eKey - lo) / (hi - lo)
	tKey = math.Max(1e-3, math.Min(1, tKey))
	tRef := (eRef - scanOrdBlack) / (scanOrdWhite - scanOrdBlack)
	tRef = math.Max(1e-3, math.Min(1, tRef))
	g := math.Log(tRef) / math.Log(tKey)
	g = math.Max(0.5, math.Min(1, g))
	lv.Gamma, lv.Mix = g, mix
	// The scanner's contrast range: a very thin negative would need an
	// enormous stretch, which only lifts its grain and colour noise. The
	// output range is the ordinary frame's for a normal-density frame and
	// shrinks, to a quarter of it, for a frame with almost no density.
	k := (hi - lo) / scanMinRange
	k = math.Max(0.25, math.Min(1, k))
	lv.Range = (scanOrdWhite - scanOrdBlack) * k
	return &lv
}

// FrameScanLevels is the scanner levels for one frame of film at push: nil
// when there is nothing to correct.
func FrameScanLevels(l *Linear, film string, push float64) *ScanLevels {
	st, ok := ScanStatsFor(l, film, push)
	if !ok {
		return nil
	}
	f := colorFilmOnPush(film, PaperScan, "sRGB", push, ColorPrint{})
	if f == nil {
		return nil
	}
	return scanLevelsFor(st, f)
}

// scanLevelsKey is the levels' part of a film table's cache key.
func (lv *ScanLevels) key() string {
	if lv == nil {
		return ""
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	k := "sl:" + f(lv.Gamma) + "," + f(lv.Mix) + "," + f(lv.Range)
	for c := 0; c < 3; c++ {
		k += ";" + f(lv.LoCh[c]) + "," + f(lv.HiCh[c])
	}
	return k
}
