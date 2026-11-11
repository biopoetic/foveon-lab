// Command t86 diagnoses where the Quattro raw base's error against SPP comes
// from. It is a research tool: it changes nothing in the app and writes only
// to -out.
//
// It builds the same pairs `foveon-lab fit-raw -go -diagnose -groups Quattro`
// builds (see decode.go), then splits each photo's held-out error into an
// exposure scalar, three channel gains, its own 3x3, its own tone curves and
// its own fit, tests which recorded numbers predict the gains, and measures
// where the remaining error sits in the picture.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/render"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
)

type result struct {
	P    *Photo
	R    Rungs
	Tone ToneBins
	Grid Grid
	Full float64
	Red4 float64
	Own  renderOwnImg
}

// renderOwnImg is the own-fit image, kept only for the crops of the six
// selected photos; nil otherwise.
type renderOwnImg []float32

func main() {
	desk := desktop()
	photosFlag := flag.String("photos", filepath.Join(desk, "foveon-samples"), "folders with the X3F files")
	exportsFlag := flag.String("exports", filepath.Join(desk, "foveon-calibration", "exports"), "A00 SPP exports")
	outFlag := flag.String("out", filepath.Join("zz-out", "t86"), "output folder")
	cacheFlag := flag.String("cache", "", "decode cache folder (default <out>/cache; empty string disables)")
	limit := flag.Int("limit", 0, "only the first N photos (development)")
	metaOnly := flag.Bool("meta-only", false, "only report which metadata is available, then stop")
	foldCache := flag.String("folds", "", "fold cache file (default <out>/folds.json)")
	flag.Parse()

	out := *outFlag
	if err := os.MkdirAll(out, 0o755); err != nil {
		fatal(err)
	}
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	photos, err := LoadPhotos(*photosFlag, *exportsFlag)
	if err != nil {
		fatal(err)
	}
	if len(photos) == 0 {
		fatal(fmt.Errorf("no Quattro X3F with an A00 export under %s / %s", *photosFlag, *exportsFlag))
	}
	if *limit > 0 && *limit < len(photos) {
		photos = photos[:*limit]
	}
	cache := *cacheFlag
	if cache == "" {
		cache = filepath.Join(out, "cache")
	}
	if *metaOnly {
		if err := LoadMetadata(photos); err != nil {
			fatal(err)
		}
	} else if err := LoadPairs(photos, *exportsFlag, cache, logf); err != nil {
		fatal(err)
	}
	metaReport(photos, out, *metaOnly)
	if *metaOnly {
		return
	}

	fc := *foldCache
	if fc == "" {
		fc = filepath.Join(out, "folds.json")
	}
	folds, ok := loadFolds(fc, photos)
	if !ok {
		logf("fitting %d held-out bases (one matrix+curves fit per photo)…", len(photos))
		folds = buildFolds(photos, logf)
		saveFolds(fc, photos, folds)
	} else {
		logf("using fold cache %s", fc)
	}

	var res []result
	for i, p := range photos {
		own := ownFit(p)
		r := ladder(p, folds[i], &own)
		ownImg := applyCorrection(&own, p.Raw)
		em := errorMap(ownImg, p.SPP)
		t := toneBins(ownImg, p.SPP)
		d4 := boxDown(ownImg, 4)
		s4 := boxDown(p.SPP, 4)
		res = append(res, result{
			P: p, R: r, Tone: t, Grid: gridOf(em, ownImg.W, ownImg.H),
			Full: calibMean(em), Red4: errOf(d4, s4),
			Own: renderOwnImg(ownImg.Pix),
		})
		logf("  photo %d/%d %s own %.2f local %.2f", i+1, len(photos), p.Name, r.EOwn, r.EOwn)
	}
	writeCSV(filepath.Join(out, "photos.csv"), res)
	tables := filepath.Join(out, "tables.md")
	tf, err := os.Create(tables)
	if err != nil {
		fatal(err)
	}
	defer tf.Close()
	writeTables(tf, res)
	pred := evaluatePredictors(out, res, folds)
	writePredictorTables(tf, pred)
	gates := runGates(res, photos, folds)
	writeGateTables(tf, gates)
	if err := writePictures(out, res); err != nil {
		logf("pictures: %v", err)
	}
	fmt.Printf("wrote %s, %s, photos.csv and pictures in %s\n", tables, filepath.Join(out, "predictors.csv"), out)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "t86:", err)
	os.Exit(1)
}

func desktop() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{filepath.Join(home, "Desktop"), filepath.Join(home, "OneDrive", "Desktop")} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return home
}

func calibMean(em []float64) float64 {
	var s float64
	for _, v := range em {
		s += v
	}
	return s / float64(len(em))
}

func errOf(a, b *render.Linear) float64 { return calib.MeanError(a, b) }

// ---------- fold cache ----------

type foldFile struct {
	Names []string
	Folds []Fold
}

func loadFolds(path string, photos []*Photo) ([]Fold, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var f foldFile
	if json.Unmarshal(b, &f) != nil || len(f.Folds) != len(photos) || len(f.Names) != len(photos) {
		return nil, false
	}
	for i := range photos {
		if f.Names[i] != photos[i].Name {
			return nil, false
		}
	}
	return f.Folds, true
}

func saveFolds(path string, photos []*Photo, folds []Fold) {
	var f foldFile
	for _, p := range photos {
		f.Names = append(f.Names, p.Name)
	}
	f.Folds = folds
	b, err := json.Marshal(f)
	if err == nil {
		os.WriteFile(path, b, 0o644)
	}
}

// ---------- metadata ----------

func metaReport(photos []*Photo, out string, only bool) {
	var f *os.File
	if !only {
		var err error
		f, err = os.Create(filepath.Join(out, "metadata.md"))
		if err != nil {
			return
		}
		defer f.Close()
	}
	w := os.Stdout
	if f != nil {
		w = f
	}
	fmt.Fprint(w, "# Quattro metadata availability\n")
	fmt.Fprintln(w, "| photo | body | ISO | WB (EXIF) | manual | LightSource | EXPCOMP | EXPNET | ColorTemp | auto gain R/G | auto gain B/G |")
	fmt.Fprintln(w, "|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|")
	nWB, nExp, nTemp := 0, 0, 0
	for _, p := range photos {
		man := ""
		if p.WBManual {
			man = "yes"
			nWB++
		}
		if p.HasEXIFWB {
			nExp++
		}
		if p.ColorTemp > 0 {
			nTemp++
		}
		arg, abg := 0.0, 0.0
		if p.AutoGain[1] > 0 {
			arg = math.Log2(p.AutoGain[0] / p.AutoGain[1])
			abg = math.Log2(p.AutoGain[2] / p.AutoGain[1])
		}
		ex := ""
		if p.HasExpNet {
			ex = fmt.Sprintf("%.2f", p.ExpNet)
		}
		fmt.Fprintf(w, "| %s | %s | %.0f | %s | %s | %d | %.2f | %s | %.0f | %+.3f | %+.3f |\n",
			p.Name, p.Body, p.ISO, p.WB, man, p.LightSource, p.ExpComp, ex, p.ColorTemp, arg, abg)
	}
	fmt.Fprintf(w, "\n%d photos; manual WB %d; EXIF WB readable %d; CAMF ColorTemp present %d.\n", len(photos), nWB, nExp, nTemp)
}

// ---------- tables ----------

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func writeTables(w *os.File, res []result) {
	fmt.Fprint(w, "# TASK-86 tables\n")
	fmt.Fprintln(w, "Errors are 8-bit sRGB mean absolute difference (the measure `calib.DiagnoseRaw` prints).")
	fmt.Fprintln(w, "`held out` = matrix+curves fitted on the other photos; `+exp` adds one exposure scalar; `+gains` three channel gains;")
	fmt.Fprint(w, "`+matrix` = own 3x3 with the group's curves; `+curves` = group matrix with own curves; `own fit` = both. +exp and +gains are in-sample for the photo; the base is held out.\n")

	fmt.Fprint(w, "## Table 1 — every photo\n")
	fmt.Fprintln(w, "| photo | body | ISO | WB | res | held out | +exp | +gains | +matrix | +curves | own fit | own fit (calib) | G | R-G | B-G |")
	fmt.Fprintln(w, "|---|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, r := range res {
		p := r.P
		fmt.Fprintf(w, "| %s | %s | %.0f | %s | %s | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %+.2f | %+.2f | %+.2f |\n",
			p.Name, p.Body, p.ISO, p.WB, p.Res, r.R.E0, r.R.EExp, r.R.EGains, r.R.EMatrix, r.R.ECurves, r.R.EOwn, r.R.EOwnCalib,
			r.R.LG[0], r.R.LG[1], r.R.LG[2])
	}

	groups := []struct {
		name string
		by   func(*Photo) string
	}{
		{"all", func(*Photo) string { return "all" }},
		{"body", func(p *Photo) string { return p.Body }},
		{"ISO", func(p *Photo) string { return isoBucket(p.ISO) }},
		{"WB", func(p *Photo) string { return p.WB }},
		{"resolution", func(p *Photo) string { return p.Res }},
	}
	for gi, g := range groups {
		fmt.Fprintf(w, "\n## Table %d — means and medians by %s\n\n", 2+gi, g.name)
		fmt.Fprintln(w, "| group | n | mean held out | mean +exp | mean +gains | mean +matrix | mean +curves | mean own | median held out | median +gains | median own |")
		fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
		keys := []string{}
		seen := map[string]bool{}
		for _, r := range res {
			k := g.by(r.P)
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		if g.name != "all" {
			sort.Strings(keys)
		}
		for _, k := range keys {
			var e0, e1, e2, e3, e4, e5 []float64
			for _, r := range res {
				if g.by(r.P) != k {
					continue
				}
				e0 = append(e0, r.R.E0)
				e1 = append(e1, r.R.EExp)
				e2 = append(e2, r.R.EGains)
				e3 = append(e3, r.R.EMatrix)
				e4 = append(e4, r.R.ECurves)
				e5 = append(e5, r.R.EOwn)
			}
			fmt.Fprintf(w, "| %s | %d | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f |\n",
				k, len(e0), mean(e0), mean(e1), mean(e2), mean(e3), mean(e4), mean(e5),
				median(e0), median(e2), median(e5))
		}
	}

	// Share of the held-out gap explained by each step, by body.
	fmt.Fprint(w, "\n## Table 7 — where the held-out error goes (mean, all photos)\n")
	var e0, e1, e2, e3, e4, e5 float64
	for _, r := range res {
		e0 += r.R.E0
		e1 += r.R.EExp
		e2 += r.R.EGains
		e3 += r.R.EMatrix
		e4 += r.R.ECurves
		e5 += r.R.EOwn
	}
	n := float64(len(res))
	e0, e1, e2, e3, e4, e5 = e0/n, e1/n, e2/n, e3/n, e4/n, e5/n
	fmt.Fprintln(w, "| step | error | drop from previous | share of held-out − own fit |")
	fmt.Fprintln(w, "|---|---:|---:|---:|")
	tot := e0 - e5
	row := func(name string, v, prev float64) {
		share := "—"
		if tot > 0.05 {
			share = fmt.Sprintf("%.0f%%", 100*(prev-v)/tot)
		}
		fmt.Fprintf(w, "| %s | %.2f | %.2f | %s |\n", name, v, prev-v, share)
	}
	fmt.Fprintf(w, "| held out | %.2f | — | — |\n", e0)
	row("+exposure", e1, e0)
	row("+gains (white balance)", e2, e1)
	row("+own matrix, group curves", e3, e2)
	row("+group matrix, own curves", e4, e2)
	fmt.Fprintf(w, "| own fit (both; in-sample for the photo) | %.2f | %.2f from +gains | — |\n", e5, e2-e5)
	var ecal float64
	for _, r := range res {
		ecal += r.R.EOwnCalib
	}
	fmt.Fprintf(w, "| own fit, calib's linear-LS matrix (in-sample) | %.2f | — | — |\n", ecal/n)

	// How far one shared base gets: the same model fitted on all 60 photos
	// (in-sample, so optimistic), and fitted on the other five bodies and
	// applied to the held-out body (a real transfer test).
	var allPairs []calib.RawPair
	for _, r := range res {
		allPairs = append(allPairs, rawPair(r.P))
	}
	inBase := calib.FitMapping(allPairs)
	var inErr []float64
	for _, r := range res {
		inErr = append(inErr, calib.MeanError(applyCorrection(&inBase, r.P.Raw), r.P.SPP))
	}
	bodies := []string{}
	seenB := map[string]bool{}
	for _, r := range res {
		if !seenB[r.P.Body] {
			seenB[r.P.Body] = true
			bodies = append(bodies, r.P.Body)
		}
	}
	sort.Strings(bodies)
	var loboErr []float64
	perBody := map[string]float64{}
	for _, b := range bodies {
		var train []calib.RawPair
		for _, r := range res {
			if r.P.Body != b {
				train = append(train, rawPair(r.P))
			}
		}
		if len(train) == 0 {
			continue
		}
		fb := calib.FitMapping(train)
		var eb []float64
		for _, r := range res {
			if r.P.Body != b {
				continue
			}
			eb = append(eb, calib.MeanError(applyCorrection(&fb, r.P.Raw), r.P.SPP))
		}
		loboErr = append(loboErr, eb...)
		perBody[b] = mean(eb)
	}
	fmt.Fprintf(w, "| one base on all photos (in-sample) | %.2f | — | — |\n", mean(inErr))
	fmt.Fprintf(w, "| one base on the other five bodies (transfer) | %.2f | — | — |\n", mean(loboErr))
	fmt.Fprintf(w, "\nPer-body transfer error: ")
	for _, b := range bodies {
		fmt.Fprintf(w, "%s %.2f; ", b, perBody[b])
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "\nMatrix marginal (own curves held): %.2f. Curves marginal (own matrix held): %.2f. Local after own fit: %.2f (%.0f%% of the held-out error).\n",
		e4-e5, e3-e5, e5, 100*e5/e0)

	fmt.Fprint(w, "\n## Table 8 — error left after own fit, by tone and detail (mean over photos)\n")
	fmt.Fprintln(w, "| group | n | shadow share | shadow err | mid share | mid err | highlight share | highlight err | full err | 4x-reduced err | reduced/full |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	keys := []string{}
	seen := map[string]bool{}
	for _, r := range res {
		if !seen[r.P.Body] {
			seen[r.P.Body] = true
			keys = append(keys, r.P.Body)
		}
	}
	sort.Strings(keys)
	for _, k := range append([]string{"all"}, keys...) {
		var sh, mi [3]float64
		var fu, r4 []float64
		cnt := 0
		for _, r := range res {
			if k != "all" && r.P.Body != k {
				continue
			}
			for b := 0; b < 3; b++ {
				sh[b] += r.Tone.Err[b] * r.Tone.Share[b]
				mi[b] += r.Tone.Share[b]
			}
			fu = append(fu, r.Full)
			r4 = append(r4, r.Red4)
			cnt++
		}
		if cnt == 0 {
			continue
		}
		sh[0] /= float64(cnt)
		sh[1] /= float64(cnt)
		sh[2] /= float64(cnt)
		mi[0] /= float64(cnt)
		mi[1] /= float64(cnt)
		mi[2] /= float64(cnt)
		err := func(b int) float64 {
			if mi[b] > 0 {
				return sh[b] / mi[b]
			}
			return 0
		}
		fmt.Fprintf(w, "| %s | %d | %.0f%% | %.2f | %.0f%% | %.2f | %.0f%% | %.2f | %.2f | %.2f | %.2f |\n",
			k, cnt, 100*mi[0], err(0), 100*mi[1], err(1), 100*mi[2], err(2), mean(fu), mean(r4), mean(r4)/mean(fu))
	}

	fmt.Fprint(w, "\n## Table 9 — 4x4 frame grid of the error after own fit (mean over each body's photos)\n")
	for _, k := range keys {
		fmt.Fprintf(w, "\n%s:\n\n", k)
		var g Grid
		cnt := 0
		for _, r := range res {
			if r.P.Body != k {
				continue
			}
			for y := 0; y < 4; y++ {
				for x := 0; x < 4; x++ {
					g[y][x] += r.Grid[y][x]
				}
			}
			cnt++
		}
		if cnt == 0 {
			continue
		}
		for y := 0; y < 4; y++ {
			fmt.Fprintf(w, "| %.2f | %.2f | %.2f | %.2f |\n", g[y][0]/float64(cnt), g[y][1]/float64(cnt), g[y][2]/float64(cnt), g[y][3]/float64(cnt))
		}
	}
}

func isoBucket(iso float64) string {
	switch {
	case iso <= 100:
		return "100"
	case iso <= 200:
		return "200"
	case iso <= 400:
		return "400"
	}
	return "800+"
}

func writeCSV(path string, res []result) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	cw := csv.NewWriter(f)
	defer cw.Flush()
	hdr := []string{"photo", "body", "iso", "wb", "res", "exiftemp", "expcomp", "expnet", "auto_rg", "auto_bg",
		"G", "R-G", "B-G", "heldout", "exp", "gains", "matrix", "curves", "own", "own_calib",
		"sh_share", "mid_share", "hi_share", "sh_err", "mid_err", "hi_err", "full", "red4"}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			hdr = append(hdr, fmt.Sprintf("g%d%d", y, x))
		}
	}
	cw.Write(hdr)
	for _, r := range res {
		p := r.P
		ex := ""
		if p.HasExpNet {
			ex = fmt.Sprintf("%.3f", p.ExpNet)
		}
		arg, abg := 0.0, 0.0
		if p.AutoGain[1] > 0 {
			arg = math.Log2(p.AutoGain[0] / p.AutoGain[1])
			abg = math.Log2(p.AutoGain[2] / p.AutoGain[1])
		}
		row := []string{p.Name, p.Body, fmt.Sprintf("%.0f", p.ISO), p.WB, p.Res, fmt.Sprintf("%.0f", p.ColorTemp),
			fmt.Sprintf("%.3f", p.ExpComp), ex, fmt.Sprintf("%.4f", arg), fmt.Sprintf("%.4f", abg),
			fmt.Sprintf("%.4f", r.R.LG[0]), fmt.Sprintf("%.4f", r.R.LG[1]), fmt.Sprintf("%.4f", r.R.LG[2]),
			fmt.Sprintf("%.3f", r.R.E0), fmt.Sprintf("%.3f", r.R.EExp), fmt.Sprintf("%.3f", r.R.EGains),
			fmt.Sprintf("%.3f", r.R.EMatrix), fmt.Sprintf("%.3f", r.R.ECurves), fmt.Sprintf("%.3f", r.R.EOwn),
			fmt.Sprintf("%.3f", r.R.EOwnCalib),
			fmt.Sprintf("%.4f", r.Tone.Share[0]), fmt.Sprintf("%.4f", r.Tone.Share[1]), fmt.Sprintf("%.4f", r.Tone.Share[2]),
			fmt.Sprintf("%.3f", r.Tone.Err[0]), fmt.Sprintf("%.3f", r.Tone.Err[1]), fmt.Sprintf("%.3f", r.Tone.Err[2]),
			fmt.Sprintf("%.3f", r.Full), fmt.Sprintf("%.3f", r.Red4)}
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				row = append(row, fmt.Sprintf("%.3f", r.Grid[y][x]))
			}
		}
		cw.Write(row)
	}
}

// ---------- pictures ----------

func writePictures(out string, res []result) error {
	type pick struct {
		r    result
		kind string
	}
	sorted := append([]result(nil), res...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].R.EOwn < sorted[j].R.EOwn })
	var picks []pick
	for i := 0; i < 3 && i < len(sorted); i++ {
		picks = append(picks, pick{sorted[i], "best"})
	}
	for i := len(sorted) - 1; i >= 0 && i >= len(sorted)-3; i-- {
		picks = append(picks, pick{sorted[i], "worst"})
	}
	for _, pk := range picks {
		own := &render.Linear{W: pk.r.P.Raw.W, H: pk.r.P.Raw.H, Pix: []float32(pk.r.Own)}
		em := errorMap(own, pk.r.P.SPP)
		x0, y0 := worstWindow(em, own.W, own.H, 320, 240, 20)
		name := fmt.Sprintf("%02d_%s_%s.png", rank(res, pk.r.P.Name), pk.kind, pk.r.P.Name)
		if err := writePanels(filepath.Join(out, name), pk.r.P.SPP, own, em, own.W, x0, y0, 320, 240, 2); err != nil {
			return err
		}
	}
	return nil
}

func rank(res []result, name string) int {
	for i, r := range res {
		if r.P.Name == name {
			return i + 1
		}
	}
	return 0
}

// worstWindow finds the window with the largest mean error.
func worstWindow(em []float64, w, h, ww, wh, step int) (int, int) {
	maxW := min(ww, w)
	maxH := min(wh, h)
	bestX, bestY, best := 0, 0, -1.0
	// Integral image for cheap window sums.
	iw := w + 1
	ii := make([]float64, iw*(h+1))
	for y := 0; y < h; y++ {
		var row float64
		for x := 0; x < w; x++ {
			row += em[y*w+x]
			ii[(y+1)*iw+x+1] = ii[y*iw+x+1] + row
		}
	}
	for y := 0; y+maxH <= h; y += step {
		for x := 0; x+maxW <= w; x += step {
			s := ii[(y+maxH)*iw+x+maxW] - ii[y*iw+x+maxW] - ii[(y+maxH)*iw+x] + ii[y*iw+x]
			if s > best {
				best, bestX, bestY = s, x, y
			}
		}
	}
	return bestX, bestY
}

// writePanels writes SPP | own | 4x difference for the crop at (x0,y0),
// scaled by s (nearest neighbour) with 4 px separators.
func writePanels(path string, spp, own *render.Linear, em []float64, stride, x0, y0, w, h, s int) error {
	pw, ph := w*s, h*s
	const gap = 4
	img := image.NewRGBA(image.Rect(0, 0, 3*pw+2*gap, ph))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for py := 0; py < ph; py++ {
		for px := 0; px < pw; px++ {
			sx, sy := x0+px/s, y0+py/s
			i := (sy*stride + sx) * 3
			set := func(panel int, c color.RGBA) {
				x := panel * (pw + gap)
				img.SetRGBA(x+px, py, c)
			}
			set(0, color.RGBA{to8(spp.Pix[i]), to8(spp.Pix[i+1]), to8(spp.Pix[i+2]), 0xff})
			set(1, color.RGBA{to8(own.Pix[i]), to8(own.Pix[i+1]), to8(own.Pix[i+2]), 0xff})
			// difference x4: the per-pixel mean channel difference, amplified
			v := min(1, em[sy*stride+sx]/255*4)
			g := uint8(v * 255)
			set(2, color.RGBA{g, g, g, 0xff})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func to8(v float32) uint8 {
	return uint8(math.Max(0, math.Min(255, float64(render.ToSRGB(v))*255+0.5)))
}

// ---------- gates ----------

type gateOut struct {
	Name            string
	Base, Blue, Hue Rungs
}

func runGates(res []result, photos []*Photo, folds []Fold) []gateOut {
	if len(res) == 0 {
		return nil
	}
	// Use the median-error photo, so the gate is not run on an outlier.
	sorted := append([]result(nil), res...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].R.EOwn < sorted[j].R.EOwn })
	pick := sorted[len(sorted)/2]
	h := -1
	for i := range photos {
		if photos[i].Name == pick.P.Name {
			h = i
		}
	}
	if h < 0 {
		return nil
	}
	// The decomposition starts from the held-out base's output. The gate adds
	// the known error to that linear image (an output-channel gain and a hue
	// twist), so the gate's "held-out base" is the identity: this tests where
	// the ladder attributes the error, not the base again.
	pred := applyCorrection(&folds[h].Base, pick.P.Raw)
	id := render.RawCorrection{Matrix: [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}}
	for k := 0; k < 3; k++ {
		for b := 0; b < 64; b++ {
			id.Curves[k][b] = -1
		}
	}
	idFold := Fold{Base: id}
	mk := func(l *render.Linear) Rungs {
		return ladder(&Photo{Name: pick.P.Name, Raw: l, SPP: pick.P.SPP}, idFold, nil)
	}
	// +0.3 stop on the blue channel.
	blue := &render.Linear{W: pred.W, H: pred.H, Pix: append([]float32(nil), pred.Pix...)}
	for i := 2; i < len(blue.Pix); i += 3 {
		blue.Pix[i] *= float32(math.Exp2(0.3))
	}
	// A 7° hue twist in the R/G plane, a pure 3x3.
	const ang = 7 * math.Pi / 180
	c, s := math.Cos(ang), math.Sin(ang)
	hue := &render.Linear{W: pred.W, H: pred.H, Pix: append([]float32(nil), pred.Pix...)}
	for i := 0; i < len(hue.Pix); i += 3 {
		r, g := float64(hue.Pix[i]), float64(hue.Pix[i+1])
		hue.Pix[i] = float32(c*r + s*g)
		hue.Pix[i+1] = float32(-s*r + c*g)
	}
	return []gateOut{{Name: pick.P.Name, Base: mk(pred), Blue: mk(blue), Hue: mk(hue)}}
}

func writeGateTables(w *os.File, gs []gateOut) {
	fmt.Fprint(w, "\n## Table 11 — the check that must fail\n")
	for _, g := range gs {
		fmt.Fprintf(w, "\nPhoto %s. The gate starts from this photo's held-out base output; the gate's own base is the identity, so the perturbation is the whole error.\n\n", g.Name)
		fmt.Fprintln(w, "| case | held out | +exp | +gains | +matrix | +curves | own fit |")
		fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|")
		line := func(name string, r Rungs) {
			fmt.Fprintf(w, "| %s | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f |\n", name, r.E0, r.EExp, r.EGains, r.EMatrix, r.ECurves, r.EOwn)
		}
		line("baseline", g.Base)
		line("+0.3 stop blue", g.Blue)
		line("7° hue twist", g.Hue)
		fmt.Fprintf(w, "| delta blue (perturbed − baseline) | %+.2f | %+.2f | %+.2f | %+.2f | %+.2f | %+.2f |\n",
			g.Blue.E0-g.Base.E0, g.Blue.EExp-g.Base.EExp, g.Blue.EGains-g.Base.EGains, g.Blue.EMatrix-g.Base.EMatrix, g.Blue.ECurves-g.Base.ECurves, g.Blue.EOwn-g.Base.EOwn)
		fmt.Fprintf(w, "| delta hue (perturbed − baseline) | %+.2f | %+.2f | %+.2f | %+.2f | %+.2f | %+.2f |\n",
			g.Hue.E0-g.Base.E0, g.Hue.EExp-g.Base.EExp, g.Hue.EGains-g.Base.EGains, g.Hue.EMatrix-g.Base.EMatrix, g.Hue.ECurves-g.Base.ECurves, g.Hue.EOwn-g.Base.EOwn)
		fmt.Fprintf(w, "\nBlue case: ideal gains G %+.2f→%+.2f, R-G %+.2f→%+.2f, B-G %+.2f→%+.2f (stops; B-G should move by -0.30).\n",
			g.Base.LG[0], g.Blue.LG[0], g.Base.LG[1], g.Blue.LG[1], g.Base.LG[2], g.Blue.LG[2])
	}
}

// ---------- predictor evaluation ----------

type predRow struct {
	Name         string
	LOOErr       float64
	LOORec       float64
	LOORecMed    float64
	LOBORec      float64
	LOBOErr      float64
	GainRMSE     [3]float64
	GainRMSELOBO [3]float64
	PerBodyRec   map[string]float64
}

func evaluatePredictors(outDir string, res []result, folds []Fold) []predRow {
	photos := make([]*Photo, len(res))
	gains := make([][3]float64, len(res))
	for i := range res {
		photos[i] = res[i].P
		gains[i] = res[i].R.LG
	}
	X := features(photos)
	names := []string{}
	for k := range X {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []predRow
	for _, name := range names {
		x := X[name]
		row := predRow{Name: name, PerBodyRec: map[string]float64{}}
		// LOO
		var ePred, e0, e2 []float64
		var sq [3]float64
		for h := range photos {
			var tr []int
			for i := range photos {
				if i != h {
					tr = append(tr, i)
				}
			}
			pred := predictGains(name, x, gains, tr, h)
			img := applyCorrection(&folds[h].Base, photos[h].Raw)
			pimg := applyLGains(img, pred)
			ePred = append(ePred, calib.MeanError(pimg, photos[h].SPP))
			e0 = append(e0, res[h].R.E0)
			e2 = append(e2, res[h].R.EGains)
			for t := 0; t < 3; t++ {
				d := pred[t] - gains[h][t]
				sq[t] += d * d
			}
		}
		row.LOOErr = mean(ePred)
		den := mean(e0) - mean(e2)
		if den != 0 {
			row.LOORec = (mean(e0) - mean(ePred)) / den
		}
		var recs []float64
		for i := range ePred {
			d := e0[i] - e2[i]
			if d > 0.05 {
				recs = append(recs, (e0[i]-ePred[i])/d)
			}
		}
		row.LOORecMed = median(recs)
		for t := 0; t < 3; t++ {
			row.GainRMSE[t] = math.Sqrt(sq[t] / float64(len(photos)))
		}
		// leave-one-body-out
		bodies := []string{}
		seen := map[string]bool{}
		for _, p := range photos {
			if !seen[p.Body] {
				seen[p.Body] = true
				bodies = append(bodies, p.Body)
			}
		}
		sort.Strings(bodies)
		var ePredB []float64
		var sqB [3]float64
		for _, b := range bodies {
			var tr []int
			for i := range photos {
				if photos[i].Body != b {
					tr = append(tr, i)
				}
			}
			var eb, e0b, e2b []float64
			for h := range photos {
				if photos[h].Body != b {
					continue
				}
				pred := predictGains(name, x, gains, tr, h)
				img := applyCorrection(&folds[h].Base, photos[h].Raw)
				pimg := applyLGains(img, pred)
				eb = append(eb, calib.MeanError(pimg, photos[h].SPP))
				e0b = append(e0b, res[h].R.E0)
				e2b = append(e2b, res[h].R.EGains)
				for t := 0; t < 3; t++ {
					d := pred[t] - gains[h][t]
					sqB[t] += d * d
				}
			}
			if len(eb) == 0 {
				continue
			}
			ePredB = append(ePredB, eb...)
			d := mean(e0b) - mean(e2b)
			if d != 0 {
				row.PerBodyRec[b] = (mean(e0b) - mean(eb)) / d
			}
		}
		row.LOBOErr = mean(ePredB)
		// Overall LOBO recovery uses all photos' own (photo-held-out) baseline.
		var allE0, allE2 []float64
		for i := range res {
			allE0 = append(allE0, res[i].R.E0)
			allE2 = append(allE2, res[i].R.EGains)
		}
		if d := mean(allE0) - mean(allE2); d != 0 {
			row.LOBORec = (mean(allE0) - mean(ePredB)) / d
		}
		for t := 0; t < 3; t++ {
			row.GainRMSELOBO[t] = math.Sqrt(sqB[t] / float64(len(photos)))
		}
		out = append(out, row)
	}
	// CSV for the report.
	f, err := os.Create(filepath.Join(outDir, "predictors.csv"))
	if err != nil {
		f = nil
	}
	if f != nil {
		defer f.Close()
		cw := csv.NewWriter(f)
		cw.Write([]string{"predictor", "loo_err", "loo_recovery", "loo_recovery_median", "lobo_err", "lobo_recovery", "rmse_G", "rmse_RG", "rmse_BG", "lobo_rmse_G", "lobo_rmse_RG", "lobo_rmse_BG"})
		for _, r := range out {
			cw.Write([]string{r.Name, fmt.Sprintf("%.3f", r.LOOErr), fmt.Sprintf("%.3f", r.LOORec), fmt.Sprintf("%.3f", r.LOORecMed),
				fmt.Sprintf("%.3f", r.LOBOErr), fmt.Sprintf("%.3f", r.LOBORec),
				fmt.Sprintf("%.3f", r.GainRMSE[0]), fmt.Sprintf("%.3f", r.GainRMSE[1]), fmt.Sprintf("%.3f", r.GainRMSE[2]),
				fmt.Sprintf("%.3f", r.GainRMSELOBO[0]), fmt.Sprintf("%.3f", r.GainRMSELOBO[1]), fmt.Sprintf("%.3f", r.GainRMSELOBO[2])})
		}
		cw.Flush()
	}
	return out
}

// predictGains fits the model for predictor name on training photos and
// returns the held-out photo's predicted [G, R-G, B-G] in stops. The
// grey-world predictor is the classical fixed-slope estimate with a fitted
// offset; every other predictor is an unconstrained linear fit with ridge.
func predictGains(name string, x [][]float64, y [][3]float64, train []int, h int) [3]float64 {
	if name == "grey world" {
		var c [3]float64
		for _, i := range train {
			c[0] += y[i][0]
			c[1] += y[i][1] - x[i][1]
			c[2] += y[i][2] - x[i][2]
		}
		n := float64(len(train))
		return [3]float64{c[0] / n, c[1]/n + x[h][1], c[2]/n + x[h][2]}
	}
	var xt [][]float64
	var yt [][3]float64
	for _, i := range train {
		xt = append(xt, x[i])
		yt = append(yt, y[i])
	}
	m := FitModel(xt, yt, 0.01)
	return m.Predict(x[h])
}

func writePredictorTables(w *os.File, rows []predRow) {
	fmt.Fprint(w, "\n## Table 10 — predicting the per-photo gains\n")
	fmt.Fprint(w, "Held out: LOO = predictor fitted on the other 59 photos; LOBO = fitted on the other five bodies. Recovery = (mean held out − mean predicted)/(mean held out − mean +gains). RMSE is in stops for G, R-G, B-G. Negative recovery = worse than the held-out base.\n")
	fmt.Fprintln(w, "| predictor | LOO error | LOO recovery | LOO median | LOBO error | LOBO recovery | RMSE G | RMSE R-G | RMSE B-G |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, r := range rows {
		fmt.Fprintf(w, "| %s | %.2f | %+.0f%% | %+.0f%% | %.2f | %+.0f%% | %.3f | %.3f | %.3f |\n",
			r.Name, r.LOOErr, 100*r.LOORec, 100*r.LOORecMed, r.LOBOErr, 100*r.LOBORec, r.GainRMSE[0], r.GainRMSE[1], r.GainRMSE[2])
	}
	fmt.Fprint(w, "\nPer-body LOBO recovery:\n")
	bodies := []string{}
	seen := map[string]bool{}
	for _, r := range rows {
		for b := range r.PerBodyRec {
			if !seen[b] {
				seen[b] = true
				bodies = append(bodies, b)
			}
		}
	}
	sort.Strings(bodies)
	fmt.Fprint(w, "| predictor |")
	for _, b := range bodies {
		fmt.Fprintf(w, " %s |", b)
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, "|---|")
	for range bodies {
		fmt.Fprint(w, "---:|")
	}
	fmt.Fprintln(w)
	for _, r := range rows {
		fmt.Fprintf(w, "| %s |", r.Name)
		for _, b := range bodies {
			v, ok := r.PerBodyRec[b]
			if !ok {
				fmt.Fprint(w, " — |")
			} else {
				fmt.Fprintf(w, " %+.0f%% |", 100*v)
			}
		}
		fmt.Fprintln(w)
	}
}
