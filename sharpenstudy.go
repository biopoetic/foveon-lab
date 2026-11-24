package main

// foveon-lab sharpen-study: how much of the Quattro raw base's held-out error
// against SPP is the difference between SPP's sharpening and ours?
//
// TASK-90. The photos are the 60 Quattro samples with an A00 neutral export;
// the pairs and the held-out base are made as `fit-raw -go -diagnose` and
// tools/t86 make them (native decode, camera WB + matrix, highlight clip, the
// app's default denoise, the Quattro mottle pass, per-model exposure, reduced
// to the export's analysis frame). The measure is calib.DiagnoseRaw's: mean
// absolute 8-bit sRGB difference at the 1200 px frame.
//
// The tool then:
//   1. splits each held-out error into a low-pass part (both images through
//      the same Gaussian) and the remainder (detail);
//   2. measures edge width, overshoot and halo on high-gradient pixels, per
//      body, ours vs SPP;
//   3. sweeps the Detail slider (the SPP mask amount: 0.5×(1+Detail), held to
//      the slider's -1..+1) and picks a setting leave-one-photo-out and
//      leave-one-body-out;
//   4. measures flat-patch luminance noise per ISO, ours vs SPP.
//
// Nothing in the app is read or written except the raw decode/development and
// the render path. The tool is a measurement: it changes no shipped number.
//
//	foveon-lab sharpen-study [-photos dir] [-dir calib] [-out zz-out/t90]
//	                         [-limit N] [-pics a,b,c]

import (
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

// t90Photo is one Quattro sample with its SPP neutral export and its raw
// development, both at the export's analysis frame.
type t90Photo struct {
	ph     *Photo
	name   string
	body   string
	iso    float64
	export string
	spp    *render.Linear
	raw    *render.Linear
}

// t90Details is the Detail sweep. 0 is the shipped default (mask amount
// 0.5); -1 is the same development without SPP's mask; +1 is twice the
// fitted amount. DetailScale holds the slider to [-1,+1].
var t90Details = []float64{-1, -0.75, -0.5, -0.25, 0, 0.25, 0.5, 0.75, 1}

var t90Sigmas = []float64{2, 4, 8}

func t90Body(folder string) string {
	switch strings.ToLower(folder) {
	case "dp0-quattro":
		return "dp0"
	case "dp1-quattro":
		return "dp1"
	case "dp2-quattro":
		return "dp2"
	case "dp3-quattro":
		return "dp3"
	case "sd-quattro":
		return "sd"
	case "sd-quattro-h":
		return "sdH"
	}
	return ""
}

func t90Apply(c *render.RawCorrection, l *render.Linear) *render.Linear {
	o := *l
	o.Pix = append([]float32(nil), l.Pix...)
	c.Apply(&o)
	return &o
}

// t90Render renders pred as the app's neutral preview at the given Detail
// (SPP mask amount), returning sRGB bytes as floats.
func t90Render(b *render.Base, ph *Photo, detail float64) []float32 {
	p := preset.Neutral()
	p.SPPDetail = detail
	img := render.RenderOpt(b, p, rawCompensation(ph), true)
	out := make([]float32, b.W*b.H*3)
	for i := range out {
		out[i] = float32(img.Pix[(i/3)*4+i%3])
	}
	return out
}

// t90Cache writes the developed raw frame so a re-run does not decode again.
func t90CachePath(dir string, ph *Photo) string {
	st, err := os.Stat(ph.path)
	key := ph.Name
	if err == nil {
		key = fmt.Sprintf("%d_%d_%x", st.Size(), st.ModTime().Unix(), t90FNV(ph.path))
	}
	return filepath.Join(dir, "raw_"+key+".bin")
}

func t90FNV(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func t90LoadCache(path string) (*render.Linear, bool) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 40 || string(b[:4]) != "T90R" {
		return nil, false
	}
	bo := binary.LittleEndian
	w, h := int(bo.Uint32(b[4:])), int(bo.Uint32(b[8:]))
	scale := math.Float64frombits(bo.Uint64(b[12:]))
	frame := int(int32(bo.Uint32(b[20:])))
	native := int(int32(bo.Uint32(b[24:])))
	iso := math.Float64frombits(bo.Uint64(b[28:]))
	n := w * h * 3
	if n <= 0 || 40+n*4 != len(b) {
		return nil, false
	}
	pix := make([]float32, n)
	for i := range pix {
		pix[i] = math.Float32frombits(bo.Uint32(b[40+i*4:]))
	}
	return &render.Linear{W: w, H: h, Pix: pix, Scale: scale, Frame: frame, NativeFrame: native, Raw: true, ISO: iso, Sensor: "Quattro"}, true
}

func t90SaveCache(path string, l *render.Linear) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	b := make([]byte, 40+len(l.Pix)*4)
	copy(b, "T90R")
	bo := binary.LittleEndian
	bo.PutUint32(b[4:], uint32(l.W))
	bo.PutUint32(b[8:], uint32(l.H))
	bo.PutUint64(b[12:], math.Float64bits(l.Scale))
	bo.PutUint32(b[20:], uint32(int32(l.Frame)))
	bo.PutUint32(b[24:], uint32(int32(l.NativeFrame)))
	bo.PutUint64(b[28:], math.Float64bits(l.ISO))
	for i, v := range l.Pix {
		bo.PutUint32(b[40+i*4:], math.Float32bits(v))
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		os.Rename(tmp, path)
	}
}

// t90Load finds the six Quattro sample folders' X3F files with an A00 export
// and develops each one.
func t90Load(photosRoot, calibDir, cacheDir string, limit int, logf func(string, ...any)) ([]*t90Photo, error) {
	want := map[string]string{
		"dp0-quattro": "dp0", "dp1-quattro": "dp1", "dp2-quattro": "dp2",
		"dp3-quattro": "dp3", "sd-quattro": "sd", "sd-quattro-h": "sdH",
	}
	exports := calib.ExportsAnywhere(calibDir, "A00")
	var out []*t90Photo
	skipped := 0
	for _, ph := range scanPhotos([]string{photosRoot}, false) {
		if ph.Kind != "x3f" || ph.Unsupported != "" {
			continue
		}
		body, ok := want[strings.ToLower(ph.Folder)]
		if !ok || calibGroup(ph) != "Quattro" {
			continue
		}
		if e, bad := calib.ExcludedReference(ph.Name, "Quattro"); bad {
			logf("  %s: excluded reference, skipped: %s", ph.Name, e.Reason)
			continue
		}
		stem := strings.ToUpper(strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name)))
		exp, ok := exports[stem]
		if !ok {
			skipped++
			continue
		}
		out = append(out, &t90Photo{ph: ph, name: strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name)), body: body, iso: photoISO(ph), export: exp})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].body != out[j].body {
			return out[i].body < out[j].body
		}
		return out[i].name < out[j].name
	})
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no Quattro X3F with an A00 export under %s / %s", photosRoot, calibDir)
	}
	// Load the export and the development one at a time (one process at a
	// time; the decode is the expensive step).
	for i, p := range out {
		spp, err := calib.LoadLinear(p.export)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.name, err)
		}
		p.spp = spp
		if cacheDir != "" {
			if l, ok := t90LoadCache(t90CachePath(cacheDir, p.ph)); ok {
				p.raw = l
				logf("  [%d/%d] %s %s ISO %.0f (cached) %dx%d", i+1, len(out), p.body, p.name, p.iso, l.W, l.H)
				continue
			}
		}
		maxW := sourceW
		if p.ph.Rotation == 90 || p.ph.Rotation == 270 {
			maxW = max(sourceW, spp.H)
		}
		l, err := goLinear(p.ph, maxW, defaultNR)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.name, err)
		}
		if p.ph.Rotation != 0 {
			l = l.Rotate(p.ph.Rotation)
		}
		l = fitAspect(l.Downscale(spp.W), spp.W, spp.H)
		p.raw = l
		if cacheDir != "" {
			t90SaveCache(t90CachePath(cacheDir, p.ph), l)
		}
		logf("  [%d/%d] %s %s ISO %.0f %dx%d", i+1, len(out), p.body, p.name, p.iso, l.W, l.H)
	}
	logf("  %d photos, %d without an export", len(out), skipped)
	return out, nil
}

// t90Folds fits each photo's held-out base (matrix and curves on the other
// photos) and caches it by photo name.
func t90Folds(path string, photos []*t90Photo, logf func(string, ...any)) []render.RawCorrection {
	type foldFile struct {
		Names []string
		Folds []render.RawCorrection
	}
	if b, err := os.ReadFile(path); err == nil {
		var f foldFile
		if json.Unmarshal(b, &f) == nil && len(f.Folds) == len(photos) {
			ok := true
			for i := range photos {
				if i >= len(f.Names) || f.Names[i] != photos[i].name {
					ok = false
					break
				}
			}
			if ok {
				logf("  using fold cache %s", path)
				return f.Folds
			}
		}
	}
	logf("  fitting %d held-out bases…", len(photos))
	folds := make([]render.RawCorrection, len(photos))
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(1, runtime.GOMAXPROCS(0)))
	for h := range photos {
		wg.Add(1)
		go func(h int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			train := make([]calib.RawPair, 0, len(photos)-1)
			for i := range photos {
				if i != h {
					train = append(train, calib.RawPair{Name: photos[i].name, Raw: photos[i].raw, SPP: photos[i].spp})
				}
			}
			folds[h] = calib.FitMapping(train)
		}(h)
	}
	wg.Wait()
	var f foldFile
	for _, p := range photos {
		f.Names = append(f.Names, p.name)
	}
	f.Folds = folds
	if b, err := json.Marshal(f); err == nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			os.WriteFile(path, b, 0o644)
		}
	}
	return folds
}

// t90Result is everything one photo contributes.
type t90Result struct {
	p       *t90Photo
	baseErr float64 // held-out base, unsharpened (calib.MeanError)
	baseF   float64 // the same in the sRGB-byte space, must equal baseErr
	errs    []float64
	low     [3]float64 // sigma 2, 4, 8 on the unsharpened base
	det     [3]float64
	// After the current default and after the best setting.
	lowCur, detCur, lowBest, detBest float64
	baseSplitLow, baseSplitDet       float64
	edgeBase, edgeCur, edgeBest      [2]edgeStat // [ours, SPP]
	noiseBase, noiseCur, noiseBest   noiseStat
	// The gate: our Detail 0 render, and the same blurred by 1 px, split at
	// sigma 4.
	// The gate at sigma 4 and 8: our Detail 0 render, and the same blurred
	// by 1 px, split by both low-passes.
	gateLow, gateDet, gateLowBlur, gateDetBlur [2]float64
	// picK is this photo's body's in-sample best Detail (used by the crops).
	picK int
}

func meanF(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func maxAbsDelta(a, b []float64) float64 {
	best := 0.0
	for i := range a {
		if d := math.Abs(a[i] - b[i]); d > best {
			best = d
		}
	}
	return best
}

func minDelta(a, b []float64) float64 {
	if len(a) == 0 {
		return 0
	}
	best := math.Inf(1)
	for i := range a {
		if d := a[i] - b[i]; d < best {
			best = d
		}
	}
	return best
}

// t90Pick chooses the Detail that minimises the median error over idx.
func t90Pick(errs [][]float64, idx []int) int {
	bestK, bestMed, bestMean := 0, math.Inf(1), math.Inf(1)
	for k := range t90Details {
		v := make([]float64, 0, len(idx))
		for _, i := range idx {
			v = append(v, errs[i][k])
		}
		med, mn := medianF(v), meanF(v)
		// The gate and the report are about the median; a mean tie-break
		// keeps a flat median plateau from picking the sharper end by
		// accident (d=0 and d=-0.25 both had median 4.67).
		if med < bestMed-1e-9 || (math.Abs(med-bestMed) <= 1e-9 && mn < bestMean) {
			bestMed, bestMean, bestK = med, mn, k
		}
	}
	return bestK
}

func t90DetailIndex(d float64) int {
	for k, v := range t90Details {
		if v == d {
			return k
		}
	}
	return -1
}

func sharpenStudyCommand(desk string) {
	fs := flag.NewFlagSet("sharpen-study", flag.ExitOnError)
	photosRoot := fs.String("photos", filepath.Join(desk, "foveon-samples"), "folders with the X3F files")
	calibDir := fs.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder (exports under */)")
	out := fs.String("out", filepath.Join("zz-out", "t90"), "output folder")
	limit := fs.Int("limit", 0, "only the first N photos (development)")
	cache := fs.String("cache", "", "decode cache folder (default <out>/cache; empty string disables)")
	foldCache := fs.String("folds", "", "held-out base cache (default <out>/folds.json)")
	pics := fs.String("pics", "", "comma-separated photo names for the crops (default: one median-error photo from dp0, dp1 and sdH)")
	fs.Parse(os.Args[2:])

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "sharpen-study:", err)
		os.Exit(1)
	}
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	cacheDir := *cache
	if cacheDir == "" {
		cacheDir = filepath.Join(*out, "cache")
	}
	photos, err := t90Load(*photosRoot, *calibDir, cacheDir, *limit, logf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sharpen-study:", err)
		os.Exit(1)
	}
	fc := *foldCache
	if fc == "" {
		fc = filepath.Join(*out, "folds.json")
	}
	folds := t90Folds(fc, photos, logf)

	res := make([]*t90Result, len(photos))
	for i, p := range photos {
		pred := t90Apply(&folds[i], p.raw)
		baseSRGB := t90SRGB(pred.Pix)
		sppSRGB := t90SRGB(p.spp.Pix)
		r := &t90Result{p: p, baseErr: calib.MeanError(pred, p.spp), baseF: meanAbs8(baseSRGB, sppSRGB)}
		b := render.NewBase(pred)
		for _, d := range t90Details {
			r.errs = append(r.errs, meanAbs8(t90Render(b, p.ph, d), sppSRGB))
		}
		for s, sigma := range t90Sigmas {
			_, low, det := freqSplit(baseSRGB, sppSRGB, p.raw.W, p.raw.H, sigma)
			r.low[s], r.det[s] = low, det
			if s == 1 {
				r.baseSplitLow, r.baseSplitDet = low, det
			}
		}
		// The current default and the in-sample best global Detail, for the
		// split, edge and noise tables; the held-out tables below are the
		// honest ones.
		r.lowCur, r.detCur = t90Split(t90Render(b, p.ph, 0), sppSRGB, p.raw.W, p.raw.H, 4)
		res[i] = r
		logf("  %d/%d %s %s base %.2f", i+1, len(photos), p.body, p.name, r.baseF)
	}

	// The in-sample best global setting (for pictures/labelled tables) and
	// the held-out choices.
	all := make([]int, len(res))
	for i := range all {
		all[i] = i
	}
	kBest := t90Pick(errsOf(res), all)
	// Leave-one-photo-out.
	loo := make([]float64, len(res))
	for i := range res {
		var tr []int
		for j := range res {
			if j != i {
				tr = append(tr, j)
			}
		}
		k := t90Pick(errsOf(res), tr)
		loo[i] = res[i].errs[k]
	}
	// Leave-one-body-out.
	bodies := t90Bodies(res)
	lobo := make([]float64, len(res))
	loboK := map[string]int{}
	for _, bd := range bodies {
		var tr []int
		for i := range res {
			if res[i].p.body != bd {
				tr = append(tr, i)
			}
		}
		if len(tr) == 0 {
			continue
		}
		k := t90Pick(errsOf(res), tr)
		loboK[bd] = k
		for i := range res {
			if res[i].p.body == bd {
				lobo[i] = res[i].errs[k]
			}
		}
	}

	// Second pass: the best setting's images, edges, noise and crops.
	wantPics := map[string]bool{}
	for _, n := range strings.Split(*pics, ",") {
		if n = strings.TrimSpace(n); n != "" {
			wantPics[strings.ToUpper(n)] = true
		}
	}
	picNames := t90PickPictures(res, wantPics)
	picSet := map[string]bool{}
	for _, n := range picNames {
		picSet[strings.ToUpper(n)] = true
	}
	// The crops' third panel is the photo's own body's best Detail (labelled
	// in-sample in the report): a single global setting is the current one,
	// so a global "best" crop would show nothing.
	picK := map[string]int{}
	for _, body := range t90Bodies(res) {
		var idx []int
		for i, r := range res {
			if r.p.body == body {
				idx = append(idx, i)
			}
		}
		if len(idx) > 0 {
			picK[body] = t90Pick(errsOf(res), idx)
		}
	}
	for i, r := range res {
		pred := t90Apply(&folds[i], r.p.raw)
		b := render.NewBase(pred)
		sppSRGB := t90SRGB(r.p.spp.Pix)
		bestSRGB := t90Render(b, r.p.ph, t90Details[kBest])
		curSRGB := t90Render(b, r.p.ph, 0)
		r.picK = picK[r.p.body]
		picSRGB := bestSRGB
		if r.picK != kBest {
			picSRGB = t90Render(b, r.p.ph, t90Details[r.picK])
		}
		r.lowBest, r.detBest = t90Split(bestSRGB, sppSRGB, r.p.raw.W, r.p.raw.H, 4)
		blur := gaussBlur3(curSRGB, r.p.raw.W, r.p.raw.H, 1)
		for gs, sigma := range []float64{4, 8} {
			_, r.gateLow[gs], r.gateDet[gs] = freqSplit(curSRGB, sppSRGB, r.p.raw.W, r.p.raw.H, sigma)
			_, r.gateLowBlur[gs], r.gateDetBlur[gs] = freqSplit(blur, sppSRGB, r.p.raw.W, r.p.raw.H, sigma)
		}
		w, h := r.p.raw.W, r.p.raw.H
		r.edgeBase[0], r.edgeBase[1] = edgeProfileStats(t90SRGB(pred.Pix), sppSRGB, w, h)
		r.edgeCur[0], r.edgeCur[1] = edgeProfileStats(curSRGB, sppSRGB, w, h)
		r.edgeBest[0], r.edgeBest[1] = edgeProfileStats(bestSRGB, sppSRGB, w, h)
		r.noiseBase = flatNoise(t90SRGB(pred.Pix), sppSRGB, w, h)
		r.noiseCur = flatNoise(curSRGB, sppSRGB, w, h)
		r.noiseBest = flatNoise(bestSRGB, sppSRGB, w, h)
		if picSet[strings.ToUpper(r.p.name)] {
			x0, y0 := bestEdgeWindow(sppSRGB, w, h, 256, 8)
			path := filepath.Join(*out, fmt.Sprintf("crop_%02d_%s_%s.png", i+1, r.p.body, r.p.name))
			if err := writeCropPNG(path, [][]float32{curSRGB, sppSRGB, picSRGB}, w, h, x0, y0, 256); err != nil {
				logf("  crop %s: %v", path, err)
			} else {
				logf("  crop %s (%d,%d) body best Detail %+.2f", path, x0, y0, t90Details[r.picK])
			}
		}
		if (i+1)%10 == 0 {
			logf("  second pass %d/%d", i+1, len(res))
		}
	}

	writeT90Tables(*out, res, kBest, loo, lobo, loboK, picNames, logf)
	writeT90CSV(*out, res, kBest, loo, lobo, loboK)
	fmt.Printf("wrote %s and photos.csv\n", filepath.Join(*out, "tables.md"))
}

func errsOf(res []*t90Result) [][]float64 {
	out := make([][]float64, len(res))
	for i, r := range res {
		out[i] = r.errs
	}
	return out
}

func t90Bodies(res []*t90Result) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range res {
		if !seen[r.p.body] {
			seen[r.p.body] = true
			out = append(out, r.p.body)
		}
	}
	sort.Strings(out)
	return out
}

func t90Split(ours, spp []float32, w, h int, sigma float64) (low, detail float64) {
	_, low, detail = freqSplit(ours, spp, w, h, sigma)
	return
}

// t90PickPictures chooses, per default, the median-error photo of dp0, dp1
// and sdH (so the crops are representative, not the worst three). A -pics
// list overrides it.
func t90PickPictures(res []*t90Result, want map[string]bool) []string {
	if len(want) > 0 {
		var out []string
		for _, r := range res {
			if want[strings.ToUpper(r.p.name)] {
				out = append(out, r.p.name)
			}
		}
		return out
	}
	var out []string
	for _, body := range []string{"dp0", "sd", "sdH"} {
		var idx []int
		for i, r := range res {
			if r.p.body == body {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 {
			continue
		}
		sort.Slice(idx, func(a, b int) bool { return res[idx[a]].errs[t90DetailIndex(0)] < res[idx[b]].errs[t90DetailIndex(0)] })
		out = append(out, res[idx[len(idx)/2]].p.name)
	}
	return out
}

// ---------- tables ----------

func writeT90Tables(dir string, res []*t90Result, kBest int, loo, lobo []float64, loboK map[string]int, picNames []string, logf func(string, ...any)) {
	f, err := os.Create(filepath.Join(dir, "tables.md"))
	if err != nil {
		logf("tables: %v", err)
		return
	}
	defer f.Close()
	w := f
	fmt.Fprint(w, "# TASK-90 tables\n\n")
	fmt.Fprint(w, "Errors are the 8-bit sRGB mean absolute difference at the 1200 px analysis frame,\n")
	fmt.Fprint(w, "the measure `calib.DiagnoseRaw` prints. `held out` = a matrix and curves fitted on\n")
	fmt.Fprint(w, "the other photos. The sharpened rows are the app's neutral render of that base\n")
	fmt.Fprint(w, "with the Detail slider at the given value (the SPP mask amount is\n")
	fmt.Fprint(w, "0.5 x (1+Detail), held to the slider's -1..+1). The low-pass split takes both\n")
	fmt.Fprint(w, "images through the same Gaussian; `detail` is the full error minus the\n")
	fmt.Fprint(w, "low-passed error, so low+detail is exactly full.\n\n")

	// Table 0 — cross-check with REPORT-86.
	var be, bf []float64
	for _, r := range res {
		be = append(be, r.baseErr)
		bf = append(bf, r.baseF)
	}
	fmt.Fprint(w, "## Table 0 — the tool reproduces REPORT-86's held-out base\n\n")
	fmt.Fprint(w, "REPORT-86: mean 5.63, median 5.04 (60 photos). The two tool columns are the\n")
	fmt.Fprint(w, "same measure computed on the Linear images and on sRGB-byte arrays; they must\n")
	fmt.Fprint(w, "agree with each other, and the aggregate must match the report.\n\n")
	fmt.Fprintln(w, "| quantity | mean | median | n |")
	fmt.Fprintln(w, "|---|---:|---:|---:|")
	fmt.Fprintf(w, "| held out, calib.MeanError | %.2f | %.2f | %d |\n", meanF(be), medianF(be), len(be))
	fmt.Fprintf(w, "| held out, sRGB-byte space | %.2f | %.2f | %d |\n", meanF(bf), medianF(bf), len(bf))
	fmt.Fprintf(w, "| REPORT-86 | 5.63 | 5.04 | 60 |\n")

	// Table 1 — the frequency split of the held-out base.
	fmt.Fprint(w, "\n## Table 1 — the frequency split of the held-out base (the 5.63)\n\n")
	fmt.Fprint(w, "`low share` is the low-pass term / full; `detail share` is the rest.\n\n")
	fmt.Fprintln(w, "| sigma px | full mean | full median | low mean | low median | detail mean | detail median | low share (mean) | detail share (mean) |")
	fmt.Fprintln(w, "|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for s, sigma := range t90Sigmas {
		var fu, lo, de []float64
		for _, r := range res {
			fu = append(fu, r.baseF)
			lo = append(lo, r.low[s])
			de = append(de, r.det[s])
		}
		fmt.Fprintf(w, "| %.0f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.0f%% | %.0f%% |\n",
			sigma, meanF(fu), medianF(fu), meanF(lo), medianF(lo), meanF(de), medianF(de),
			100*meanF(lo)/meanF(fu), 100*meanF(de)/meanF(fu))
	}
	fmt.Fprint(w, "\nThe same split after the current default sharpening (Detail 0) and after the\n")
	fmt.Fprint(w, "in-sample best global setting (Table 3's oracle, labelled in-sample):\n\n")
	fmt.Fprintln(w, "| version | full mean | low mean (sigma 4) | detail mean | low share |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|")
	fmt.Fprintf(w, "| unsharpened base | %.2f | %.2f | %.2f | %.0f%% |\n", meanF(bf), meanMean(res, func(r *t90Result) float64 { return r.baseSplitLow }), meanMean(res, func(r *t90Result) float64 { return r.baseSplitDet }), 100*meanMean(res, func(r *t90Result) float64 { return r.baseSplitLow })/meanF(bf))
	fmt.Fprintf(w, "| Detail 0 (current) | %.2f | %.2f | %.2f | %.0f%% |\n", meanMean(res, func(r *t90Result) float64 { return r.errs[t90DetailIndex(0)] }), meanMean(res, func(r *t90Result) float64 { return r.lowCur }), meanMean(res, func(r *t90Result) float64 { return r.detCur }), 100*meanMean(res, func(r *t90Result) float64 { return r.lowCur })/meanMean(res, func(r *t90Result) float64 { return r.errs[t90DetailIndex(0)] }))
	fmt.Fprintf(w, "| Detail %+.2f (in-sample best) | %.2f | %.2f | %.2f | %.0f%% |\n", t90Details[kBest], meanMean(res, func(r *t90Result) float64 { return r.errs[kBest] }), meanMean(res, func(r *t90Result) float64 { return r.lowBest }), meanMean(res, func(r *t90Result) float64 { return r.detBest }), 100*meanMean(res, func(r *t90Result) float64 { return r.lowBest })/meanMean(res, func(r *t90Result) float64 { return r.errs[kBest] }))

	// Table 1c — the split by body and ISO.
	fmt.Fprint(w, "\n### Table 1c — the same split by body and by ISO (sigma 4)\n\n")
	fmt.Fprint(w, "The 5.63 is an average over very different frames: the detail share runs\n")
	fmt.Fprint(w, "from 6% to 68% per photo, and high ISO is dominated by low-frequency\n")
	fmt.Fprint(w, "noise/mottle, not detail.\n\n")
	fmt.Fprintln(w, "| group | n | full | low | detail | detail share | d0 error | d0 drop |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|---:|")
	writeSplitGroup := func(name string, pick func(*t90Result) bool) {
		var f, lo, de, d0 []float64
		for _, r := range res {
			if !pick(r) {
				continue
			}
			f = append(f, r.baseF)
			lo = append(lo, r.low[1])
			de = append(de, r.det[1])
			d0 = append(d0, r.errs[t90DetailIndex(0)])
		}
		if len(f) == 0 {
			return
		}
		fmt.Fprintf(w, "| %s | %d | %.2f | %.2f | %.2f | %.0f%% | %.2f | %+.2f |\n", name, len(f), meanF(f), meanF(lo), meanF(de), 100*meanF(de)/meanF(f), meanF(d0), meanF(d0)-meanF(f))
	}
	for _, b := range t90Bodies(res) {
		body := b
		writeSplitGroup(body, func(r *t90Result) bool { return r.p.body == body })
	}
	for _, iso := range t90ISOs(res) {
		iso := iso
		writeSplitGroup(fmt.Sprintf("ISO %.0f", iso), func(r *t90Result) bool { return r.p.iso == iso })
	}

	// Table 2 — the sweep.
	fmt.Fprint(w, "\n## Table 2 — the Detail sweep (all in-sample for the setting)\n\n")
	bodies := t90Bodies(res)
	fmt.Fprint(w, "| Detail | amount | mean | median |")
	for _, b := range bodies {
		fmt.Fprintf(w, " %s median |", b)
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, "|---:|---:|---:|---:|")
	for range bodies {
		fmt.Fprint(w, "---:|")
	}
	fmt.Fprintln(w)
	for k, d := range t90Details {
		e := make([]float64, len(res))
		for i, r := range res {
			e[i] = r.errs[k]
		}
		fmt.Fprintf(w, "| %+.2f | %.3f | %.2f | %.2f |", d, 0.5*math.Max(0, math.Min(2, 1+d)), meanF(e), medianF(e))
		for _, b := range bodies {
			var v []float64
			for _, r := range res {
				if r.p.body == b {
					v = append(v, r.errs[k])
				}
			}
			fmt.Fprintf(w, " %.2f |", medianF(v))
		}
		fmt.Fprintln(w)
	}

	// Table 3 — the held-out choices.
	cur := make([]float64, len(res))
	for i, r := range res {
		cur[i] = r.errs[t90DetailIndex(0)]
	}
	best := make([]float64, len(res))
	for i, r := range res {
		best[i] = r.errs[kBest]
	}
	imp := func(v []float64) (int, int, int) {
		better, worse, same := 0, 0, 0
		for i := range v {
			switch {
			case v[i] < cur[i]-0.01:
				better++
			case v[i] > cur[i]+0.01:
				worse++
			default:
				same++
			}
		}
		return better, worse, same
	}
	lb, lw, ls := imp(loo)
	ob, ow, osame := imp(lobo)
	fmt.Fprint(w, "\n## Table 3 — held-out choice of the Detail setting\n\n")
	fmt.Fprint(w, "Each row picks one Detail on a training set and applies it unchanged to the\n")
	fmt.Fprint(w, "held-out photos. `delta median` and `delta mean` are against the current\n")
	fmt.Fprint(w, "default (Detail 0); negative is better. The oracle rows use the photo/body's\n")
	fmt.Fprint(w, "own errors and are in-sample, so only the LOO and LOBO rows are held out.\n\n")
	fmt.Fprintln(w, "| method | chosen | mean | median | delta mean | delta median | better/worse/same |")
	fmt.Fprintln(w, "|---|---|---:|---:|---:|---:|---:|")
	fmt.Fprintf(w, "| current default | 0.00 | %.2f | %.2f | — | — | — |\n", meanF(cur), medianF(cur))
	fmt.Fprintf(w, "| leave-one-photo-out | per photo | %.2f | %.2f | %+.2f | %+.2f | %d/%d/%d |\n", meanF(loo), medianF(loo), meanF(loo)-meanF(cur), medianF(loo)-medianF(cur), lb, lw, ls)
	fmt.Fprintf(w, "| leave-one-body-out | per body | %.2f | %.2f | %+.2f | %+.2f | %d/%d/%d |\n", meanF(lobo), medianF(lobo), meanF(lobo)-meanF(cur), medianF(lobo)-medianF(cur), ob, ow, osame)
	orb, orw, ors := imp(best)
	fmt.Fprintf(w, "| oracle, one setting on all photos | %+.2f | %.2f | %.2f | %+.2f | %+.2f | %d/%d/%d |\n", t90Details[kBest], meanF(best), medianF(best), meanF(best)-meanF(cur), medianF(best)-medianF(cur), orb, orw, ors)
	fmt.Fprint(w, "\nPer body: the LOBO setting, the held-out median at it, and (in-sample) the\n")
	fmt.Fprint(w, "body's own best setting.\n\n")
	fmt.Fprintln(w, "| body | n | LOBO Detail | median at current | median LOBO | delta | oracle Detail | oracle median | median at current (check) |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, b := range bodies {
		var idx []int
		for i, r := range res {
			if r.p.body == b {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 {
			continue
		}
		var c, l []float64
		for _, i := range idx {
			c = append(c, cur[i])
			l = append(l, lobo[i])
		}
		ko := t90Pick(errsOf(res), idx)
		var ov, cv []float64
		for _, i := range idx {
			ov = append(ov, res[i].errs[ko])
			cv = append(cv, cur[i])
		}
		fmt.Fprintf(w, "| %s | %d | %+.2f | %.2f | %.2f | %+.2f | %+.2f | %.2f | %.2f |\n",
			b, len(idx), t90Details[loboK[b]], medianF(c), medianF(l), medianF(l)-medianF(c), t90Details[ko], medianF(ov), medianF(cv))
	}
	fmt.Fprint(w, "\nLOBO choices by fold: ")
	for _, b := range bodies {
		fmt.Fprintf(w, "%s %+.2f; ", b, t90Details[loboK[b]])
	}
	fmt.Fprintln(w)

	// Table 4 — edges per body.
	fmt.Fprint(w, "\n## Table 4 — edges, ours vs SPP, per body\n\n")
	fmt.Fprint(w, "Candidates are the SPP image's thinned high-gradient pixels (Sobel > 24,\n")
	fmt.Fprint(w, "step > 25 levels); each image's 12 px profile is normalised by its own\n")
	fmt.Fprint(w, "plateaus and aligned on its own steepest point. `width` is the 10-90%\n")
	fmt.Fprint(w, "transition of the mean profile in px (lower is sharper), `overshoot` the\n")
	fmt.Fprint(w, "mean excursion beyond the plateaus in % of the step, `halo` the length of\n")
	fmt.Fprint(w, "the mean profile outside the plateaus by more than 0.2% of the step (px).\n")
	fmt.Fprint(w, "`shift` is the mean absolute perpendicular displacement of our steepest\n")
	fmt.Fprint(w, "point from SPP's (px). `profile err` is the mean absolute difference of the\n")
	fmt.Fprint(w, "two normalised profiles before / after that shift, in % of the step: the\n")
	fmt.Fprint(w, "part of the edge error that is position and the part that is shape. Rows\n")
	fmt.Fprint(w, "are edge-count weighted.\n\n")
	fmt.Fprintln(w, "| version | stat | "+bodyHeaders(bodies))
	fmt.Fprintln(w, "|---|---|"+dashFor(bodies))
	fmt.Fprint(w, edgeVersionRows(res, bodies, "unsharpened base", func(r *t90Result) [2]edgeStat { return r.edgeBase }))
	fmt.Fprint(w, edgeVersionRows(res, bodies, "Detail 0 (current)", func(r *t90Result) [2]edgeStat { return r.edgeCur }))
	fmt.Fprint(w, edgeVersionRows(res, bodies, fmt.Sprintf("Detail %+.2f (in-sample best)", t90Details[kBest]), func(r *t90Result) [2]edgeStat { return r.edgeBest }))
	fmt.Fprintln(w, "\nEvery cell is `ours / SPP`.")

	// Table 5 — noise per ISO.
	fmt.Fprint(w, "\n## Table 5 — flat-patch luminance noise per ISO, ours vs SPP\n\n")
	fmt.Fprint(w, "Median 16x16-block luma sigma on blocks flat in the SPP image (max |grad|\n")
	fmt.Fprint(w, "<= 8 levels, range <= 14, mean 6..249). `ratio` is ours/SPP: below 1 means\n")
	fmt.Fprint(w, "our noise reduction is stronger. `blocks` and `n` are the counts behind it.\n\n")
	fmt.Fprintln(w, "| ISO | n | blocks | ours | SPP | ratio |")
	fmt.Fprintln(w, "|---:|---:|---:|---:|---:|---:|")
	for _, iso := range t90ISOs(res) {
		var o, s []float64
		var blocks, n int
		for _, r := range res {
			if r.p.iso != iso {
				continue
			}
			if r.noiseBase.Blocks == 0 {
				continue
			}
			o = append(o, r.noiseBase.Ours)
			s = append(s, r.noiseBase.SPP)
			blocks += r.noiseBase.Blocks
			n++
		}
		if n == 0 {
			continue
		}
		mo, ms := meanF(o), meanF(s)
		fmt.Fprintf(w, "| %.0f | %d | %d | %.2f | %.2f | %.2f |\n", iso, n, blocks, mo, ms, mo/ms)
	}
	fmt.Fprint(w, "\nAt the current default and at the in-sample best setting (means over the same photos):\n\n")
	fmt.Fprintln(w, "| ISO | n | ours Detail 0 | ours best | SPP | ratio 0 | ratio best |")
	fmt.Fprintln(w, "|---:|---:|---:|---:|---:|---:|---:|")
	for _, iso := range t90ISOs(res) {
		var c, b, s []float64
		n := 0
		for _, r := range res {
			if r.p.iso != iso || r.noiseBase.Blocks == 0 {
				continue
			}
			c = append(c, r.noiseCur.Ours)
			b = append(b, r.noiseBest.Ours)
			s = append(s, r.noiseBase.SPP)
			n++
		}
		if n == 0 {
			continue
		}
		fmt.Fprintf(w, "| %.0f | %d | %.2f | %.2f | %.2f | %.2f | %.2f |\n", iso, n, meanF(c), meanF(b), meanF(s), meanF(c)/meanF(s), meanF(b)/meanF(s))
	}

	// Table 6 — the gate.
	fmt.Fprint(w, "\n## Table 6 — the check that must fail: blur our render by 1 px\n\n")
	fmt.Fprint(w, "Our render is Detail 0 (the current app output). We blur it by a Gaussian\n")
	fmt.Fprint(w, "sigma 1 in the measuring code only and split the result twice, with a 4 px\n")
	fmt.Fprint(w, "and an 8 px low-pass. `detail` must grow; `low` is the control. The 4 px row\n")
	fmt.Fprint(w, "is the headline split; the 8 px row separates the blur from the low-pass\n")
	fmt.Fprint(w, "more cleanly (a 1 px blur suppresses the 8 px content less).\n\n")
	for gs, sigma := range []float64{4, 8} {
		var gL0, gD0, gLb, gDb, gF0 []float64
		grew, lowMoved := 0, 0
		for _, r := range res {
			gL0 = append(gL0, r.gateLow[gs])
			gD0 = append(gD0, r.gateDet[gs])
			gLb = append(gLb, r.gateLowBlur[gs])
			gDb = append(gDb, r.gateDetBlur[gs])
			gF0 = append(gF0, r.gateLow[gs]+r.gateDet[gs])
			if r.gateDetBlur[gs] > r.gateDet[gs]+0.005 {
				grew++
			}
			if math.Abs(r.gateLowBlur[gs]-r.gateLow[gs]) <= 0.02 {
				lowMoved++
			}
		}
		fmt.Fprintf(w, "### sigma %.0f px\n\n", sigma)
		fmt.Fprintln(w, "| quantity | mean | median | photos where it grew / moved <=0.02 |")
		fmt.Fprintln(w, "|---|---:|---:|---:|")
		fmt.Fprintf(w, "| low-pass, before | %.2f | %.2f | — |\n", meanF(gL0), medianF(gL0))
		fmt.Fprintf(w, "| low-pass, after 1 px blur | %.2f | %.2f | %d/%d |\n", meanF(gLb), medianF(gLb), lowMoved, len(res))
		fmt.Fprintf(w, "| detail, before | %.2f | %.2f | — |\n", meanF(gD0), medianF(gD0))
		fmt.Fprintf(w, "| detail, after 1 px blur | %.2f | %.2f | %d/%d |\n", meanF(gDb), medianF(gDb), grew, len(res))
		fmt.Fprintf(w, "| delta low-pass | %+.2f | %+.2f | max |delta| %.2f |\n", meanF(gLb)-meanF(gL0), medianF(gLb)-medianF(gL0), maxAbsDelta(gLb, gL0))
		fmt.Fprintf(w, "| delta detail | %+.2f | %+.2f | min %+.2f |\n", meanF(gDb)-meanF(gD0), medianF(gDb)-medianF(gD0), minDelta(gDb, gD0))
		fmt.Fprintf(w, "\nFull error before %.2f, after the 1 px blur %.2f.\n\n", meanF(gF0), meanF(gF0)+meanF(gDb)-meanF(gD0))
	}

	// Table 7 — the pictures.
	fmt.Fprint(w, "\n## Table 7 — the crops\n\n")
	fmt.Fprint(w, "1:1 256 px crops, left = ours at Detail 0 (the current default), middle =\n")
	fmt.Fprint(w, "SPP's export, right = ours at the photo's own body's in-sample best Detail.\n")
	fmt.Fprint(w, "The honest held-out setting is 0.00 for every body (Table 3), so the right\n")
	fmt.Fprint(w, "panel differs from the left only where a body has an in-sample gain (sd).\n\n")
	fmt.Fprintln(w, "| photo | body | ISO | Detail 0 error | body best Detail | error at it | file |")
	fmt.Fprintln(w, "|---|---|---:|---:|---:|---:|---|")
	for i, r := range res {
		for _, n := range picNames {
			if n != r.p.name {
				continue
			}
			fmt.Fprintf(w, "| %s | %s | %.0f | %.2f | %+.2f | %.2f | crop_%02d_%s_%s.png |\n",
				r.p.name, r.p.body, r.p.iso, r.errs[t90DetailIndex(0)], t90Details[r.picK], r.errs[r.picK], i+1, r.p.body, r.p.name)
		}
	}
}

func meanMean(res []*t90Result, f func(*t90Result) float64) float64 {
	v := make([]float64, len(res))
	for i, r := range res {
		v[i] = f(r)
	}
	return meanF(v)
}

func bodyHeaders(bodies []string) string {
	var b strings.Builder
	for i, bd := range bodies {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s |", bd)
	}
	return b.String()
}

func dashFor(bodies []string) string {
	var b strings.Builder
	for range bodies {
		b.WriteString("---:|")
	}
	return b.String()
}

// edgeVersionRows writes five rows per version with one cell per body.
// Width, overshoot and halo are `ours / SPP`, edge-count weighted; the shift
// and the profile residual are ours against SPP, printed as `ours / —`.
func edgeVersionRows(res []*t90Result, bodies []string, name string, get func(*t90Result) [2]edgeStat) string {
	var b strings.Builder
	for _, stat := range []string{"width px", "overshoot %", "halo px", "shift px", "profile err % (unaligned/aligned)"} {
		fmt.Fprintf(&b, "| %s | %s |", name, stat)
		for _, body := range bodies {
			var no, ns int
			var sumO, sumS, sumAl float64
			for _, r := range res {
				if r.p.body != body {
					continue
				}
				e := get(r)
				for side, n := range []int{e[0].N, e[1].N} {
					if n == 0 {
						continue
					}
					var v float64
					switch stat {
					case "width px":
						v = e[side].Width
						if math.IsNaN(v) {
							continue
						}
					case "overshoot %":
						v = 100 * e[side].Overshoot
					case "halo px":
						v = e[side].Halo
					case "shift px":
						if side == 1 {
							continue
						}
						v = e[side].Shift
					case "profile err % (unaligned/aligned)":
						if side == 1 {
							continue
						}
						v = 100 * e[side].ProfileUnaligned
						sumAl += 100 * e[side].ProfileAligned * float64(n)
					}
					if side == 0 {
						no, sumO = no+n, sumO+v*float64(n)
					} else {
						ns, sumS = ns+n, sumS+v*float64(n)
					}
				}
			}
			if no == 0 {
				fmt.Fprint(&b, " — |")
				continue
			}
			switch stat {
			case "shift px":
				fmt.Fprintf(&b, " %.2f / — |", sumO/float64(no))
			case "profile err % (unaligned/aligned)":
				fmt.Fprintf(&b, " %.1f / %.1f |", sumO/float64(no), sumAl/float64(no))
			default:
				if ns == 0 {
					fmt.Fprint(&b, " — |")
					continue
				}
				fmt.Fprintf(&b, " %.2f / %.2f |", sumO/float64(no), sumS/float64(ns))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func t90ISOs(res []*t90Result) []float64 {
	seen := map[float64]bool{}
	var out []float64
	for _, r := range res {
		if !seen[r.p.iso] {
			seen[r.p.iso] = true
			out = append(out, r.p.iso)
		}
	}
	sort.Float64s(out)
	return out
}

// ---------- CSV ----------

func writeT90CSV(dir string, res []*t90Result, kBest int, loo, lobo []float64, loboK map[string]int) {
	f, err := os.Create(filepath.Join(dir, "photos.csv"))
	if err != nil {
		return
	}
	defer f.Close()
	cw := csv.NewWriter(f)
	defer cw.Flush()
	hdr := []string{"photo", "body", "iso", "base", "base_f"}
	for _, d := range t90Details {
		hdr = append(hdr, fmt.Sprintf("d%+.2f", d))
	}
	hdr = append(hdr, "loo", "lobo", "lobo_detail", "base_low4", "base_detail4", "cur_low4", "cur_detail4", "best_low4", "best_detail4", "pic_detail")
	hdr = append(hdr, "gate4_low", "gate4_detail", "gate4_low_blur1", "gate4_detail_blur1", "gate8_low", "gate8_detail", "gate8_low_blur1", "gate8_detail_blur1")
	hdr = append(hdr, "edge_ours_width", "edge_spp_width", "edge_ours_over", "edge_spp_over", "edge_ours_halo", "edge_spp_halo", "edge_shift", "edge_profile_unaligned", "edge_profile_aligned", "edge_n")
	hdr = append(hdr, "noise_blocks", "noise_ours", "noise_spp")
	cw.Write(hdr)
	for i, r := range res {
		row := []string{r.p.name, r.p.body, fmt.Sprintf("%.0f", r.p.iso), fmt.Sprintf("%.3f", r.baseErr), fmt.Sprintf("%.3f", r.baseF)}
		for _, e := range r.errs {
			row = append(row, fmt.Sprintf("%.3f", e))
		}
		row = append(row, fmt.Sprintf("%.3f", loo[i]), fmt.Sprintf("%.3f", lobo[i]), fmt.Sprintf("%+.2f", t90Details[loboK[r.p.body]]),
			fmt.Sprintf("%.3f", r.baseSplitLow), fmt.Sprintf("%.3f", r.baseSplitDet),
			fmt.Sprintf("%.3f", r.lowCur), fmt.Sprintf("%.3f", r.detCur),
			fmt.Sprintf("%.3f", r.lowBest), fmt.Sprintf("%.3f", r.detBest), fmt.Sprintf("%+.2f", t90Details[r.picK]))
		for gs := 0; gs < 2; gs++ {
			row = append(row, fmt.Sprintf("%.3f", r.gateLow[gs]), fmt.Sprintf("%.3f", r.gateDet[gs]),
				fmt.Sprintf("%.3f", r.gateLowBlur[gs]), fmt.Sprintf("%.3f", r.gateDetBlur[gs]))
		}
		row = append(row, fmt.Sprintf("%.2f", r.edgeCur[0].Width), fmt.Sprintf("%.2f", r.edgeCur[1].Width),
			fmt.Sprintf("%.2f", 100*r.edgeCur[0].Overshoot), fmt.Sprintf("%.2f", 100*r.edgeCur[1].Overshoot),
			fmt.Sprintf("%.2f", r.edgeCur[0].Halo), fmt.Sprintf("%.2f", r.edgeCur[1].Halo),
			fmt.Sprintf("%.2f", r.edgeCur[0].Shift),
			fmt.Sprintf("%.4f", r.edgeCur[0].ProfileUnaligned), fmt.Sprintf("%.4f", r.edgeCur[0].ProfileAligned),
			fmt.Sprintf("%d", r.edgeCur[0].N))
		row = append(row, fmt.Sprintf("%d", r.noiseCur.Blocks), fmt.Sprintf("%.2f", r.noiseCur.Ours), fmt.Sprintf("%.2f", r.noiseCur.SPP))
		cw.Write(row)
	}
}
