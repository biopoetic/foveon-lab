package main

// spp-decomp: per-photo decomposition of the spp-check error.
//
//	spp-decomp [-photos Desktop] [-dir ...] [-only DP,DPM,Quattro] [-save decomp.json]
//
// For every photo with a neutral SPP export it renders the photo exactly as
// spp-check does and splits the remaining error into the parts a correction
// could attack: exposure (the EV and area@ev0 spp-check prints), a
// brightness-free white-balance cast, error and signed bias by SPP
// brightness band (tone), fine detail (all - area), shadows, and three
// per-photo oracles that bound what a perfect per-photo fix could do (WB
// gains, per-channel tone, 3x3 matrix + tone). The oracles are in-sample
// upper bounds, not models: they map each photo's own histogram onto its
// own export, so they say where the error is, never that a group-wide fix
// would work.
//
// -refs also compares every export with the camera's own embedded JPEG
// (rotated like the photo): the RGB correlation says whether it is the same
// frame at all, the EV whether the export is neutral, and the gain-corrected
// error whether it is a reasonable neutral render of that frame. That is the
// reference audit, not a fitting step.

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

type decompRow struct {
	Name   string `json:"name"`
	Group  string `json:"group"`
	Model  string `json:"model"`
	Camera string `json:"camera"`
	WB     string `json:"wb"`
	ISO    string `json:"iso"`
	Lens   string `json:"lens"`

	All     float64 `json:"all"`
	Area    float64 `json:"area"`
	Shadow  float64 `json:"shadow"`
	Detail  float64 `json:"detail"` // all - area
	EV      float64 `json:"ev"`
	AreaEV0 float64 `json:"areaEV0"`

	// Cast is each channel's mean level against SPP in stops, brightness
	// removed (green-relative: the three sum to ~0). ShadowCast is the same
	// over pixels SPP shows darker than linear 0.06.
	Cast       [3]float64 `json:"cast"`
	ShadowCast [3]float64 `json:"shadowCast"`

	// Bands: mean absolute error and signed bias (ours - SPP, 0-255) in six
	// SPP brightness bands, 0-1/6 ... 5/6-1.
	Bands [6]float64 `json:"bands"`
	Bias  [6]float64 `json:"bias"`

	// Per-photo oracles (in-sample upper bounds, see the file comment).
	OracleWB         float64 `json:"oracleWB"`
	OracleTone       float64 `json:"oracleTone"`
	OracleMatrixTone float64 `json:"oracleMatrixTone"`

	// Clipped: fraction of SPP pixels with an 8-bit channel at 255; ClippedOurs
	// the same for our render. A large difference is a highlight question.
	Clipped     float64 `json:"clipped"`
	ClippedOurs float64 `json:"clippedOurs"`

	// Reference audit against the camera's embedded JPEG (-refs).
	JpegCorr float64 `json:"jpegCorr"`
	JpegEV   float64 `json:"jpegEV"`
	JpegErr  float64 `json:"jpegErr"`
	Ref      string  `json:"ref"`
}

func sppDecompCommand(desk string) {
	fs := flag.NewFlagSet("spp-decomp", flag.ExitOnError)
	dir := fs.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder (exports in its exports\\ and other subfolders)")
	photos := fs.String("photos", desk, "folders with the X3F files (separated by ;)")
	save := fs.String("save", "", "write the per-photo rows to this JSON file")
	only := fs.String("only", "", "comma-separated camera groups and/or photo names (default: all)")
	refs := fs.Bool("refs", false, "also audit each export against the camera's embedded JPEG")
	fs.Parse(os.Args[2:])

	exports := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(*dir, "*", "*_A00.tif"))
	for _, m := range matches {
		name := strings.ToUpper(strings.TrimSuffix(filepath.Base(m), "_A00.tif"))
		if _, ok := exports[name]; !ok || filepath.Base(filepath.Dir(m)) == "exports" {
			exports[name] = m
		}
	}
	want := map[string]bool{}
	for _, s := range strings.Split(*only, ",") {
		if s = strings.TrimSpace(s); s != "" {
			want[strings.ToUpper(s)] = true
		}
	}
	printReferenceExclusions()
	start := time.Now()
	rows := map[string]*decompRow{}
	names := []string{}
	for _, ph := range scanPhotos(strings.Split(*photos, ";"), false) {
		if ph.Kind != "x3f" || ph.Unsupported != "" {
			continue
		}
		name := strings.ToUpper(strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name)))
		exp, ok := exports[name]
		group := calibGroup(ph)
		if !ok || (len(want) > 0 && !want[name] && !want[strings.ToUpper(group)]) {
			continue
		}
		if e, bad := calib.ExcludedReference(name, group); bad {
			fmt.Printf("  excluded  %-10s %-8s %s\n", name, group, e.Reason)
			continue
		}
		if _, done := rows[name]; done {
			continue
		}
		r, err := decompPhoto(ph, exp, *refs)
		if err != nil {
			fmt.Printf("  %s: %v\n", name, err)
			continue
		}
		r.Name, r.Group, r.Model = name, group, modelOf(ph)
		r.Camera, r.WB = ph.Camera, ph.props["WB_DESC"]
		r.ISO, r.Lens = ph.props["ISO"], ph.props["AP_DESC"]
		r.Ref = filepath.Base(filepath.Dir(exp)) + "/" + filepath.Base(exp)
		rows[name] = &r
		names = append(names, name)
		fmt.Printf("  %-10s %-8s all %5.2f area %5.2f det %4.2f sh %5.2f ev %+5.2f cast %+4.2f/%+4.2f/%+4.2f  wb %5.2f tone %5.2f m+t %5.2f\n",
			name, group, r.All, r.Area, r.Detail, r.Shadow, r.EV, r.Cast[0], r.Cast[1], r.Cast[2], r.OracleWB, r.OracleTone, r.OracleMatrixTone)
	}
	fmt.Printf("%d photos in %s\n", len(rows), time.Since(start).Round(time.Second))
	printDecompSummary(rows)
	if *save != "" {
		out := make([]*decompRow, 0, len(rows))
		for _, n := range names {
			out = append(out, rows[n])
		}
		b, _ := json.MarshalIndent(out, "", " ")
		if err := os.WriteFile(*save, b, 0o644); err != nil {
			fmt.Println(err)
		} else {
			fmt.Println("saved", *save)
		}
	}
}

// decompPhoto renders ph like spp-check and splits the error.
func decompPhoto(ph *Photo, export string, refs bool) (decompRow, error) {
	v, spp, err := checkRender(ph, export)
	if err != nil {
		return decompRow{}, err
	}
	var r decompRow
	r.All = calib.MeanError(v, spp)
	r.Area = calib.MeanError(v.Downscale(spp.W/4), spp.Downscale(spp.W/4))
	r.Detail = r.All - r.Area
	var shSum float64
	var shN int
	var yo, ys float64
	for i := 0; i+2 < len(spp.Pix); i += 3 {
		yr := 0.2126*spp.Pix[i] + 0.7152*spp.Pix[i+1] + 0.0722*spp.Pix[i+2]
		if yr < 0.06 {
			for k := 0; k < 3; k++ {
				shSum += math.Abs(float64(render.ToSRGB(clamp01(v.Pix[i+k]))-render.ToSRGB(clamp01(spp.Pix[i+k])))) * 255
			}
			shN += 3
		}
		yo += 0.2126*float64(v.Pix[i]) + 0.7152*float64(v.Pix[i+1]) + 0.0722*float64(v.Pix[i+2])
		ys += 0.2126*float64(spp.Pix[i]) + 0.7152*float64(spp.Pix[i+1]) + 0.0722*float64(spp.Pix[i+2])
	}
	if shN > 0 {
		r.Shadow = shSum / float64(shN)
	}
	if yo > 0 && ys > 0 {
		r.EV = math.Log2(yo / ys)
		m := v.Downscale(spp.W / 4)
		k := float32(ys / yo)
		for i := range m.Pix {
			m.Pix[i] *= k
		}
		r.AreaEV0 = calib.MeanError(m, spp.Downscale(spp.W/4))
	}

	// Cast and the WB oracle: per-channel means over unclipped pixels.
	var so, ss [3]float64
	for i := 0; i+2 < len(v.Pix); i += 3 {
		if max(spp.Pix[i], spp.Pix[i+1], spp.Pix[i+2]) > 0.95 {
			continue
		}
		for k := 0; k < 3; k++ {
			so[k] += float64(v.Pix[i+k])
			ss[k] += float64(spp.Pix[i+k])
		}
	}
	var lg [3]float64
	for k := 0; k < 3; k++ {
		if so[k] > 0 && ss[k] > 0 {
			lg[k] = math.Log2(ss[k] / so[k])
		}
	}
	mean := (lg[0] + lg[1] + lg[2]) / 3
	for k := 0; k < 3; k++ {
		r.Cast[k] = -(lg[k] - mean) // ours against SPP, as a level in stops
	}
	g := &render.Linear{W: v.W, H: v.H, Pix: append([]float32(nil), v.Pix...)}
	for i := 0; i < len(g.Pix); i++ {
		g.Pix[i] = clamp01(float32(float64(g.Pix[i]) * math.Exp2(lg[i%3])))
	}
	r.OracleWB = calib.MeanError(g, spp)

	// Shadow cast: the same over SPP's shadows only.
	var do, ds [3]float64
	for i := 0; i+2 < len(spp.Pix); i += 3 {
		if 0.2126*spp.Pix[i]+0.7152*spp.Pix[i+1]+0.0722*spp.Pix[i+2] >= 0.06 {
			continue
		}
		for k := 0; k < 3; k++ {
			do[k] += float64(v.Pix[i+k])
			ds[k] += float64(spp.Pix[i+k])
		}
	}
	var sl [3]float64
	for k := 0; k < 3; k++ {
		if do[k] > 0 && ds[k] > 0 {
			sl[k] = math.Log2(ds[k] / do[k])
		}
	}
	sm := (sl[0] + sl[1] + sl[2]) / 3
	for k := 0; k < 3; k++ {
		r.ShadowCast[k] = -(sl[k] - sm)
	}

	// Tone: absolute error and signed bias by SPP brightness band.
	var bandSum, bandBias [6]float64
	var bandN [6]int
	var clipS, clipO int
	for i := 0; i+2 < len(v.Pix); i += 3 {
		ys := render.ToSRGB(clamp01(spp.Pix[i]))*0.2126 + render.ToSRGB(clamp01(spp.Pix[i+1]))*0.7152 + render.ToSRGB(clamp01(spp.Pix[i+2]))*0.0722
		b := min(5, int(ys*6))
		var d float64
		for k := 0; k < 3; k++ {
			e := float64(render.ToSRGB(clamp01(v.Pix[i+k]))-render.ToSRGB(clamp01(spp.Pix[i+k]))) * 255
			d += math.Abs(e)
			bandBias[b] += e
		}
		bandSum[b] += d
		bandN[b] += 3
		if max(spp.Pix[i], spp.Pix[i+1], spp.Pix[i+2]) > 0.99 {
			clipS++
		}
		if max(v.Pix[i], v.Pix[i+1], v.Pix[i+2]) > 0.99 {
			clipO++
		}
	}
	for b := 0; b < 6; b++ {
		if bandN[b] > 0 {
			r.Bands[b] = bandSum[b] / float64(bandN[b])
			r.Bias[b] = bandBias[b] / float64(bandN[b])
		}
	}
	r.Clipped = float64(clipS) / float64(len(spp.Pix)/3)
	r.ClippedOurs = float64(clipO) / float64(len(v.Pix)/3)

	// Per-channel tone oracle (identity matrix + 64-bin curves) and matrix +
	// tone oracle (a 3x3 least-squares matrix and the same curves). The
	// curves are fitted with an isotonic (pool adjacent violators) pass:
	// the mean SPP value per input bin, then the closest non-decreasing
	// curve, so one sparse outlier bin (a few clipped pixels in ours where
	// SPP is dark) cannot flatten the whole curve the way a monotone clamp
	// from the top does.
	tc := fitTonePAV(v, spp)
	o := *v
	o.Pix = append([]float32(nil), v.Pix...)
	tc.Apply(&o)
	r.OracleTone = calib.MeanError(&o, spp)
	mtx := calib.FitMapping([]calib.RawPair{{Raw: v, SPP: spp}}).Matrix
	mc := fitTonePAV(applyMatrix(v, mtx), spp)
	mc.Matrix = mtx
	o2 := *v
	o2.Pix = append([]float32(nil), v.Pix...)
	mc.Apply(&o2)
	r.OracleMatrixTone = calib.MeanError(&o2, spp)

	if refs {
		r.JpegCorr, r.JpegEV, r.JpegErr = auditReference(ph, spp)
	}
	return r, nil
}

// fitTonePAV fits per-channel 64-bin curves from pred to spp, no matrix:
// the in-sample bound of a pure tone correction. Straight lines in sRGB
// space between bins are fitted, and the curve is the closest non-decreasing
// one (isotonic regression, pool adjacent violators) so a single sparse bin
// cannot dominate it.
func fitTonePAV(pred, spp *render.Linear) *render.RawCorrection {
	return fitTonePAVAt(pred, spp, [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1})
}

// fitTonePAVAt is fitTonePAV on the image after applying m.
func fitTonePAVAt(pred, spp *render.Linear, m [9]float64) *render.RawCorrection {
	c := &render.RawCorrection{Matrix: m}
	src := applyMatrix(pred, m)
	var refsum [3][64]float64
	var cnt [3][64]int
	for i := 0; i+2 < len(src.Pix); i += 3 {
		for k := 0; k < 3; k++ {
			e := float64(render.ToSRGB(clamp01(src.Pix[i+k])))
			b := min(63, int(e*64))
			refsum[k][b] += float64(render.ToSRGB(clamp01(spp.Pix[i+k])))
			cnt[k][b]++
		}
	}
	c.Curves = curvesFromBins(refsum, cnt)
	return c
}

// curvesFromBins turns per-bin sums of the reference value into per-channel
// 64-bin curves: bin means, gaps interpolated, then an isotonic fit.
func curvesFromBins(refsum [3][64]float64, cnt [3][64]int) [3][64]float64 {
	var curves [3][64]float64
	for k := 0; k < 3; k++ {
		vals := make([]float64, 64)
		wt := make([]float64, 64)
		valid := make([]bool, 64)
		any := false
		for b := 0; b < 64; b++ {
			if cnt[k][b] >= 20 {
				vals[b] = refsum[k][b] / float64(cnt[k][b])
				wt[b] = float64(cnt[k][b])
				valid[b] = true
				any = true
			}
		}
		if !any {
			for b := 0; b < 64; b++ {
				vals[b], wt[b] = (float64(b)+0.5)/64, 1
			}
		} else {
			// Interpolate empty bins between their nearest valid neighbours;
			// outside the measured range hold the nearest value.
			first, last := -1, -1
			for b := 0; b < 64; b++ {
				if valid[b] {
					if first < 0 {
						first = b
					}
					last = b
				}
			}
			prev := first
			for b := first + 1; b <= last; b++ {
				if !valid[b] {
					continue
				}
				for q := prev + 1; q < b; q++ {
					t := float64(q-prev) / float64(b-prev)
					vals[q] = vals[prev]*(1-t) + vals[b]*t
					wt[q] = 1
				}
				prev = b
			}
			for b := 0; b < first; b++ {
				vals[b], wt[b] = vals[first]*float64(b+1)/float64(first+1), 1
			}
			for b := last + 1; b < 64; b++ {
				vals[b], wt[b] = min(1, vals[last]+float64(b-last)/64), 1
			}
		}
		pav(vals, wt)
		for b := 0; b < 64; b++ {
			curves[k][b] = vals[b]
		}
	}
	return curves
}

// pav replaces vals by the closest non-decreasing sequence in the weighted
// least-squares sense (pool adjacent violators).
func pav(vals, wt []float64) {
	n := len(vals)
	type block struct {
		sum, w float64
		n      int
	}
	var bs []block
	for i := 0; i < n; i++ {
		bs = append(bs, block{vals[i] * wt[i], wt[i], 1})
		for len(bs) > 1 && bs[len(bs)-2].sum/bs[len(bs)-2].w > bs[len(bs)-1].sum/bs[len(bs)-1].w {
			a, b := bs[len(bs)-2], bs[len(bs)-1]
			bs = bs[:len(bs)-1]
			bs[len(bs)-1] = block{a.sum + b.sum, a.w + b.w, a.n + b.n}
		}
	}
	i := 0
	for _, b := range bs {
		for q := 0; q < b.n; q++ {
			vals[i] = b.sum / b.w
			i++
		}
	}
}

// applyMatrix returns pred after the linear 3x3 m (clamped to 0..1).
func applyMatrix(pred *render.Linear, m [9]float64) *render.Linear {
	o := *pred
	o.Pix = make([]float32, len(pred.Pix))
	for i := 0; i+2 < len(pred.Pix); i += 3 {
		r, g, b := float64(pred.Pix[i]), float64(pred.Pix[i+1]), float64(pred.Pix[i+2])
		o.Pix[i] = clamp01(float32(m[0]*r + m[1]*g + m[2]*b))
		o.Pix[i+1] = clamp01(float32(m[3]*r + m[4]*g + m[5]*b))
		o.Pix[i+2] = clamp01(float32(m[6]*r + m[7]*g + m[8]*b))
	}
	return &o
}

// auditReference compares the export with the camera's own embedded JPEG,
// rotated like the photo: RGB correlation (same frame?), EV (neutral?) and
// the error left after a per-channel gain (a reasonable neutral render?).
func auditReference(ph *Photo, spp *render.Linear) (corr, ev, gainErr float64) {
	f, err := x3f.Open(ph.path)
	if err != nil {
		return math.NaN(), math.NaN(), math.NaN()
	}
	img, _, err := f.Preview()
	if err != nil {
		return math.NaN(), math.NaN(), math.NaN()
	}
	cam := fitAspect(render.FromImage(img, calib.AnalysisW, ph.rotation), spp.W, spp.H)
	// Correlation of the two images' sRGB values.
	var n, sx, sy, sxx, syy, sxy float64
	var yo, ys float64
	for i := 0; i < len(cam.Pix); i++ {
		x := float64(render.ToSRGB(clamp01(cam.Pix[i])))
		y := float64(render.ToSRGB(clamp01(spp.Pix[i])))
		n++
		sx += x
		sy += y
		sxx += x * x
		syy += y * y
		sxy += x * y
		yo += x
		ys += y
	}
	if n > 0 {
		cov := sxy/n - (sx/n)*(sy/n)
		vx := sxx/n - (sx/n)*(sx/n)
		vy := syy/n - (sy/n)*(sy/n)
		if vx > 0 && vy > 0 {
			corr = cov / math.Sqrt(vx*vy)
		}
		if yo > 0 && ys > 0 {
			ev = math.Log2(yo / ys) // encoded means: a rough exposure read
		}
	}
	var co, cs [3]float64
	for i := 0; i+2 < len(cam.Pix); i += 3 {
		if max(spp.Pix[i], spp.Pix[i+1], spp.Pix[i+2]) > 0.95 {
			continue
		}
		for k := 0; k < 3; k++ {
			co[k] += float64(cam.Pix[i+k])
			cs[k] += float64(spp.Pix[i+k])
		}
	}
	g := &render.Linear{W: cam.W, H: cam.H, Pix: append([]float32(nil), cam.Pix...)}
	for i := 0; i < len(g.Pix); i++ {
		k := i % 3
		if co[k] > 0 {
			g.Pix[i] = clamp01(float32(float64(g.Pix[i]) * cs[k] / co[k]))
		}
	}
	gainErr = calib.MeanError(g, spp)
	return corr, ev, gainErr
}

func meanOf(f func(*decompRow) float64, rows []*decompRow) float64 {
	if len(rows) == 0 {
		return 0
	}
	var s float64
	for _, r := range rows {
		s += f(r)
	}
	return s / float64(len(rows))
}

func printDecompSummary(rows map[string]*decompRow) {
	by := map[string][]*decompRow{}
	for _, r := range rows {
		by[r.Group] = append(by[r.Group], r)
		by["ALL"] = append(by["ALL"], r)
	}
	var gs []string
	for g := range by {
		gs = append(gs, g)
	}
	sort.Strings(gs)
	fmt.Printf("%-8s %3s %6s %6s %5s %6s %6s %6s %6s %6s %6s %6s\n", "GROUP", "n", "all", "area", "det", "shad", "|ev|", "ev0", "cast", "wb", "tone", "m+t")
	for _, g := range gs {
		rs := by[g]
		absEV := meanOf(func(r *decompRow) float64 { return math.Abs(r.EV) }, rs)
		cast := meanOf(func(r *decompRow) float64 {
			return math.Sqrt((r.Cast[0]*r.Cast[0] + r.Cast[1]*r.Cast[1] + r.Cast[2]*r.Cast[2]) / 3)
		}, rs)
		fmt.Printf("%-8s %3d %6.3f %6.3f %5.2f %6.3f %6.2f %6.2f %6.2f %6.2f %6.2f %6.2f\n", g, len(rs),
			meanOf(func(r *decompRow) float64 { return r.All }, rs),
			meanOf(func(r *decompRow) float64 { return r.Area }, rs),
			meanOf(func(r *decompRow) float64 { return r.Detail }, rs),
			meanOf(func(r *decompRow) float64 { return r.Shadow }, rs),
			absEV,
			meanOf(func(r *decompRow) float64 { return r.AreaEV0 }, rs),
			cast,
			meanOf(func(r *decompRow) float64 { return r.OracleWB }, rs),
			meanOf(func(r *decompRow) float64 { return r.OracleTone }, rs),
			meanOf(func(r *decompRow) float64 { return r.OracleMatrixTone }, rs))
	}
}
