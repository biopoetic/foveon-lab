package main

// foveon-lab fit-raw: learn, per camera family, how to turn an x3f_extract
// raw decode into SPP's neutral rendering, from photos that have a neutral
// SPP export (<photo>_A00.tif in the calibration exports folder). Writes
// rawbase.json (copy it to render/rawbase.json and rebuild to ship it).

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

// fitAspect brings a raw decode to w×h: a reduced-resolution readout (SD1
// MED, rows binned in pairs: about twice the aspect) is stretched, while a
// frame shot at another aspect (Quattro 16:9 from the 3:2 sensor; SPP and
// the camera crop it centred, measured 2026-10-13) is centre-cropped first.
func fitAspect(l *render.Linear, w, h int) *render.Linear {
	r := float64(l.W) * float64(h) / (float64(l.H) * float64(w))
	if r > 1.02 && r < 1.5 { // wider: crop the sides
		cw := int(math.Round(float64(l.H) * float64(w) / float64(h)))
		l = cropLinear(l, (l.W-cw)/2, 0, cw, l.H)
	} else if r < 0.98 && r > 0.67 { // taller: crop top and bottom
		ch := int(math.Round(float64(l.W) * float64(h) / float64(w)))
		l = cropLinear(l, 0, (l.H-ch)/2, l.W, ch)
	}
	return stretchTo(l, w, h)
}

// cropLinear is the w×h region of l at (x0, y0).
func cropLinear(l *render.Linear, x0, y0, w, h int) *render.Linear {
	o := &render.Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: l.Scale}
	for y := 0; y < h; y++ {
		copy(o.Pix[y*w*3:(y+1)*w*3], l.Pix[((y+y0)*l.W+x0)*3:])
	}
	return o
}

// stretchTo resamples l to w×h (nearest neighbour): reduced-resolution raw
// decodes come out at another aspect than the full-size render.
func stretchTo(l *render.Linear, w, h int) *render.Linear {
	if l.W == w && l.H == h {
		return l
	}
	// Keep what the decode knows (scale, ISO, sensor, raw): a MED preview
	// lost it, so it got no SPP sharpening and no ISO-based tuning.
	o := *l
	o.W, o.H, o.Pix = w, h, make([]float32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := ((y*l.H/h)*l.W + x*l.W/w) * 3
			copy(o.Pix[(y*w+x)*3:], l.Pix[s:s+3])
		}
	}
	return &o
}

func rgbaToLinear(img *image.RGBA) *render.Linear {
	b := img.Bounds()
	l := &render.Linear{W: b.Dx(), H: b.Dy(), Pix: make([]float32, b.Dx()*b.Dy()*3)}
	for i := 0; i < l.W*l.H; i++ {
		for k := 0; k < 3; k++ {
			l.Pix[i*3+k] = render.ToLinear(float32(img.Pix[i*4+k]) / 255)
		}
	}
	return l
}

// mergeModelsCommand: foveon-lab merge-models -o out.json a.json b.json …
func mergeModelsCommand() {
	fsFlags := flag.NewFlagSet("merge-models", flag.ExitOnError)
	out := fsFlags.String("o", "", "output model file")
	fsFlags.Parse(os.Args[2:])
	var ms []*render.Model
	for _, p := range fsFlags.Args() {
		b, err := os.ReadFile(p)
		if err != nil {
			log.Fatal(err)
		}
		var m render.Model
		if err := json.Unmarshal(b, &m); err != nil {
			log.Fatalf("%s: %v", p, err)
		}
		ms = append(ms, &m)
	}
	if *out == "" || len(ms) == 0 {
		log.Fatal("usage: foveon-lab merge-models -o out.json model_a.json model_b.json …")
	}
	js, _ := json.MarshalIndent(calib.MergeModels(ms), "", " ")
	if err := os.WriteFile(*out, js, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("merged %d models into %s\n", len(ms), *out)
}

func fitRawCommand(desk string) {
	fsFlags := flag.NewFlagSet("fit-raw", flag.ExitOnError)
	dir := fsFlags.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder")
	photos := fsFlags.String("photos", desk, "folders with the X3F files (separated by ;)")
	diagnose := fsFlags.Bool("diagnose", false, "only print each photo's raw-base error breakdown (nothing is saved)")
	deltaE := fsFlags.Bool("deltae", false, "report held-out CIEDE2000 of the starting images vs SPP (comparable with other projects)")
	local := fsFlags.Bool("local", false, "research: held-out highlight desaturation and high-pass local contrast on top of the raw base")
	noDenoise := fsFlags.Bool("nodenoise", false, "decode without x3f_extract's denoise (the fast preview decode)")
	noSgain := fsFlags.Bool("nosgain", false, "research: decode without x3f_extract's spatial gain (colour shading)")
	goRaw := fsFlags.Bool("go", false, "decode and develop raw natively (x3f package) instead of with x3f_extract: camera RGB through each photo's CAMF white balance + colour matrix")
	camfRaw := fsFlags.Bool("camf", false, "decode raw as camera RGB and apply each photo's CAMF white balance + colour matrix (as SPP does) instead of x3f_extract's sRGB")
	taste := fsFlags.Bool("taste", false, "research: held-out dE of a first-principles base (the camera's colour + a tone curve and a saturation only), within and across camera groups")
	transfer := fsFlags.String("transfer", "", "research: FROM:TO (e.g. SD1:Quattro) — dE of TO's photos with FROM's taste and full base vs TO's own base and its camera JPEG")
	tasteOut := fsFlags.String("tasteout", "", "print the taste (tone + saturation) learnt on all of this group's photos, as JSON for render/rawbase.json")
	onlyGroups := fsFlags.String("groups", "", "decode only these groups (comma separated), to save time")
	dngProfile := fsFlags.Bool("dngprofile", false, "research: held-out dE of the raw base as a DNG camera profile (ForwardMatrix + one Adobe tone curve) vs the full per-channel raw base")
	onlyRes := fsFlags.String("res", "", "research: only photos of this RESOLUTION (HI, MED), e.g. to test a separate SD1 MED base")
	nr := fsFlags.Float64("nr", defaultNR, "with -go: noise reduction of the training decodes (the app's own by default; 0 until v0.4.56)")
	charts := fsFlags.Bool("charts", false, "research: also train on synthetic charts (…/charts/*.X3F). Off by default: on 2026-10-13 two in-gamut SD1 charts (166/192 clean patches) made the held-out SD1 starting image worse, 6.87 -> 7.58 dE (SPP's response on them is strongly non-linear in the shadows)")
	autoWB := fsFlags.Bool("autowb", false, "research: fit each group on its Auto-white-balance photos only, as a per-balance \"family|Auto\" entry (SD15: held out 8.05 vs the shipped all-photo base's 5.25, so not the default)")
	fsFlags.Parse(os.Args[2:])
	calib.AutoWBOnly = *autoWB
	if rawTool == "" && !*goRaw {
		log.Fatal("fit-raw needs x3f_extract (or -go)")
	}
	exports, names := calib.ExportsFor(*dir, "A00")
	byName := map[string]*Photo{}
	for _, p := range scanPhotos(strings.Split(*photos, ";"), false) {
		if p.Kind == "x3f" {
			byName[strings.ToUpper(strings.TrimSuffix(p.Name, filepath.Ext(p.Name)))] = p
		}
	}
	// Which group each export belongs to (cheap: no decoding), so the fit
	// can load one group at a time. All groups at once held four
	// analysis-size images per photo (~46 MB): ~12 GB for ~260 photos,
	// which ran the machine out of memory (October 2026).
	present := map[string]bool{}
	for i := range exports {
		name := names[i]
		ph := byName[strings.ToUpper(name)]
		if ph == nil {
			fmt.Printf("  %s: X3F not found under -photos, skipped\n", name)
			continue
		}
		family, group := x3f.PresetFamily(ph.Camera), calibGroup(ph)
		if family == "" && group != "Quattro" { // Quattro: no presets, but its own raw base
			fmt.Printf("  %s: %s has no family, skipped\n", name, ph.Camera)
			continue
		}
		if *onlyGroups != "" && !slices.Contains(strings.Split(*onlyGroups, ","), group) {
			continue
		}
		present[group] = true
	}
	groups := map[string][]calib.RawPair{}
	jgroups := map[string][]calib.JPEGPair{}
	// load decodes the photos of the wanted groups (nil = all present ones)
	// into groups and jgroups.
	load := func(want map[string]bool) {
		for i, e := range exports {
			name := names[i]
			ph := byName[strings.ToUpper(name)]
			if ph == nil {
				continue
			}
			family, group := x3f.PresetFamily(ph.Camera), calibGroup(ph)
			if !present[group] || (want != nil && !want[group]) {
				continue
			}
			loadPair(name, e, ph, family, group, groups, jgroups, *charts, *onlyRes, *goRaw, *camfRaw, *noSgain, *noDenoise, *nr)
		}
	}
	research := *deltaE || *tasteOut != "" || *transfer != "" || *taste || *dngProfile || *local || *diagnose
	if research {
		load(nil) // these compare groups with each other
	}
	famsOf := func() []string {
		var fams []string
		for f := range jgroups {
			fams = append(fams, f)
		}
		for f := range groups {
			if jgroups[f] == nil {
				fams = append(fams, f)
			}
		}
		sort.Strings(fams)
		return fams
	}
	fams := famsOf()
	if *deltaE {
		for _, f := range fams {
			calib.DeltaEReport(f, groups[f], jgroups[f], os.Stdout)
		}
		return
	}
	if *tasteOut != "" {
		js, _ := json.Marshal(calib.TasteJSON(groups[*tasteOut]))
		fmt.Println("TASTE " + string(js))
		return
	}
	if ft := strings.SplitN(*transfer, ":", 2); len(ft) == 2 {
		calib.TransferReport(groups, ft[0], ft[1], os.Stdout)
		return
	}
	if *taste {
		calib.TasteReport(groups, os.Stdout)
		return
	}
	if *dngProfile {
		for _, f := range fams {
			calib.DNGProfileReport(f, groups[f], os.Stdout)
		}
		return
	}
	if *local {
		for _, f := range fams {
			calib.RawLocalResearch(f, groups[f], os.Stdout)
		}
		return
	}
	if *diagnose {
		for _, f := range fams {
			calib.DiagnoseRaw(f, groups[f], os.Stdout)
		}
		return
	}
	var out []render.RawCorrection
	var bal []render.JPEGBalance
	fit := func(fams []string) {
		for _, f := range fams {
			c := calib.FitRawBase(f, groups[f])
			out = append(out, c)
			verdict := "camera JPEG stays (raw base not clearly better)"
			if c.UseRaw {
				verdict = "RAW BASE ON"
			}
			fmt.Printf("%s: %d photos, held-out error raw %.2f vs camera JPEG %.2f → %s\n", f, c.Photos, c.HeldOutRaw, c.HeldOutJPEG, verdict)
			if s := render.RawBaseFitted(f, ""); s != nil {
				fmt.Printf("%s: the shipped base on the same photos (not held out): %.2f\n", f, calib.ScoreRawBase(groups[f], s))
			}
			b := calib.FitJPEGBalance(f, jgroups[f])
			bal = append(bal, b)
			verdict = "off (not clearly better)"
			if b.Use {
				verdict = "ON"
			}
			fmt.Printf("%s camera-JPEG balance: %d photos, held-out %.1f vs %.1f without -> %s; + tone curve %.1f (on: %v)\n", f, b.Photos, b.HeldOut, b.Without, verdict, b.HeldOutTone, b.ToneUse)
		}
	}
	// One group at a time: only its photos are in memory.
	var want []string
	for g := range present {
		want = append(want, g)
	}
	sort.Strings(want)
	for _, g := range want {
		clear(groups)
		clear(jgroups)
		runtime.GC()
		load(map[string]bool{g: true})
		fit(famsOf())
	}
	// Written as the shipped files with the fitted groups replaced, so they
	// can be copied to render/ as they are.
	p := filepath.Join(*dir, "rawbase.json")
	if err := os.WriteFile(p, mergeByFamily(render.ShippedRawBaseJSON(), out), 0o644); err != nil {
		log.Fatal(err)
	}
	pj := filepath.Join(*dir, "jpegwb.json")
	if err := os.WriteFile(pj, mergeByFamily(render.ShippedJPEGBalanceJSON(), bal), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Saved %s and %s (the shipped files with %s refitted): copy them to render/ and rebuild to use them.\n", p, pj, strings.Join(want, ", "))
}

// fillResearchCommand compares Fill Light surround scales held out
// (calib.FillResearch), including the FIR_* proportions from CAMF.
func fillResearchCommand(desk string) {
	fsFlags := flag.NewFlagSet("fill-research", flag.ExitOnError)
	dir := fsFlags.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder")
	photos := fsFlags.String("photos", desk, "folders with the X3F files (separated by ;)")
	fsFlags.Parse(os.Args[2:])
	fam := map[string]string{}
	for _, p := range scanPhotos(strings.Split(*photos, ";"), false) {
		if p.Kind == "x3f" {
			fam[strings.ToUpper(strings.TrimSuffix(p.Name, filepath.Ext(p.Name)))] = x3f.PresetFamily(p.Camera)
		}
	}
	// "box" is the app's render.NewSurround (a guided filter since Oct 2026;
	// the block average it replaced scored SD1 7.80 / SD15 8.34).
	cands := []calib.FillCandidate{
		{Name: "global curve only (no surround)", Blur: "box"},
		{Name: "app: render.NewSurround", Fracs: render.SurroundFracs[:], Blur: "box"},
		{Name: "gauss 1/80 + 1/20", Fracs: render.SurroundFracs[:], Blur: "gauss"},
		{Name: "FIR: gauss .0019 + .019", Fracs: []float64{0.0019, 0.019}, Blur: "gauss"},
		{Name: "guided .08 1/80 + 1/20", Fracs: render.SurroundFracs[:], Blur: "guided0.08"},
		// A compressive response to the surround difference (d·|d| as well as
		// d) is the Retinex-style non-linearity: edges lift less than flat
		// shadows of the same brightness.
		{Name: "app + square", Fracs: render.SurroundFracs[:], Blur: "box", Square: true},
		{Name: "FIR + square", Fracs: []float64{0.0019, 0.019}, Blur: "gauss", Square: true},
		{Name: "guided .08 + square", Fracs: render.SurroundFracs[:], Blur: "guided0.08", Square: true},
	}
	if os.Getenv("FILL_GAINS") != "" {
		for _, v := range []float64{0.5, 1} {
			calib.FillGainStats(*dir, func(n string) string { return fam[strings.ToUpper(n)] }, cands[len(cands)-1], v, os.Stdout)
		}
		return
	}
	if err := calib.FillResearch(*dir, func(n string) string { return fam[strings.ToUpper(n)] }, cands, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// loadPair decodes one photo with an SPP export into its group's raw pairs
// (groups) and camera-JPEG pairs (jgroups).
func loadPair(name, e string, ph *Photo, family, group string, groups map[string][]calib.RawPair, jgroups map[string][]calib.JPEGPair,
	charts bool, onlyRes string, goRaw, camfRaw, noSgain, noDenoise bool, nr float64) {
	// Charts (tools/synth -gamut) are drawn inside the gamut on purpose.
	isChart := strings.Contains(strings.ToLower(filepath.ToSlash(ph.path)), "/charts/")
	if isChart && !charts {
		return
	}
	spp, err := calib.LoadLinear(e)
	if err != nil {
		log.Fatal(err)
	}
	// Skip scenes at the edge of sRGB (e.g. a saturated red flower): SPP
	// works on wider data there, so they teach clipping, not colour.
	var edge int
	for i := 0; i < len(spp.Pix); i += 3 {
		lo := min(spp.Pix[i], spp.Pix[i+1], spp.Pix[i+2])
		hi := max(spp.Pix[i], spp.Pix[i+1], spp.Pix[i+2])
		if lo < 0.0005 && hi > 0.2 || hi > 0.99 {
			edge++
		}
	}
	if frac := float64(edge) / float64(len(spp.Pix)/3); frac > 0.3 && !isChart {
		fmt.Printf("  %s: %.0f%% of it at the sRGB edge or clipped, skipped\n", name, frac*100)
		return
	}
	var jpegErr float64 = -1
	var jpegNeutral *render.Linear
	if f, err := x3f.Open(ph.path); err == nil {
		if img, _, err := f.Preview(); err == nil {
			cam := fitAspect(render.FromImage(img, calib.AnalysisW, 0), spp.W, spp.H)
			props := f.Props
			neutral := func(l *render.Linear) *render.Linear {
				return rgbaToLinear(render.Render(render.NewBase(l), preset.Neutral(), render.CompensationWith(props, family, photoCC(ph))))
			}
			jpegNeutral = neutral(cam)
			jpegErr = calib.MeanError(jpegNeutral, spp)
			if !strings.Contains(strings.ToLower(filepath.ToSlash(ph.path)), "/charts/") { // a chart's preview is its container photo's
				jgroups[group] = append(jgroups[group], calib.JPEGPair{Name: name, JPEG: cam, SPP: spp, AutoWB: props["WB_DESC"] == "Auto", Neutral: neutral})
			}
		}
	}
	// The raw base only: these photos' camera JPEG still trains the
	// JPEG balance above (the app shows them from it).
	if onlyRes != "" && strings.TrimSpace(ph.props["RESOLUTION"]) != onlyRes {
		return
	}
	if !rawDecodable(ph.props) {
		fmt.Printf("  %s: %s resolution, which x3f_extract decodes wrongly, raw skipped\n", name, ph.props["RESOLUTION"])
		return
	}
	fmt.Printf("  %s (%s): decoding raw…\n", name, ph.Camera)
	var raw *render.Linear
	if goRaw {
		// As the app makes a preview: developed at its working width,
		// then reduced. Developed straight at the analysis width (3×3
		// binning, denoised at that scale) the training image was another
		// one: the shipped SD1 base scored 5.05 on it against 3.73 on the
		// app's (16 photos; SDIM0064 14.3 vs 8.7).
		// A portrait photo's working width is its long side, so at
		// sourceW it came out 1067 wide, narrower than SPP's 1200 and
		// stretched (nearest neighbour) to fit: blurred, misaligned
		// detail (SDIM0418 15.3, area colour alone 2.7).
		maxW := sourceW
		if ph.rotation == 90 || ph.rotation == 270 {
			maxW = max(sourceW, spp.H)
		}
		l, err := goLinear(ph, maxW, nr)
		if err != nil {
			fmt.Printf("    go raw: %v\n", err)
			return
		}
		// Turned as the app shows it (rawLinearFor): unturned, a portrait
		// photo's landscape data was stretched onto SPP's portrait export
		// (SDIM0412, 0418: errors 92 and 59), poisoning the whole fit.
		if ph.rotation != 0 {
			l = l.Rotate(ph.rotation)
		}
		raw = fitAspect(l.Downscale(spp.W), spp.W, spp.H)
	} else if camfRaw {
		rimg, err := decodeRawAs(ph, "none")
		if err != nil {
			fmt.Printf("    raw decode failed: %v\n", err)
			return
		}
		t, err := camfTransform(ph)
		if err != nil {
			fmt.Printf("    %v\n", err)
			return
		}
		raw = stretchTo(render.FromCameraRaw(rimg, calib.AnalysisW, 0, t), spp.W, spp.H)
	} else {
		color := "sRGB"
		if noSgain {
			color += "-nosgain"
		}
		if noDenoise {
			color += "-nodenoise"
		}
		rimg, err := decodeRawAs(ph, color)
		if err != nil {
			fmt.Printf("    raw decode failed: %v\n", err)
			return
		}
		raw = stretchTo(render.FromImage(rimg, calib.AnalysisW, 0), spp.W, spp.H)
	}
	if jpegErr < 0 {
		fmt.Printf("    its camera JPEG can't be read; used for fitting only as raw\n")
		jpegErr = calib.MeanError(raw, spp) // no better baseline exists
	}
	pr := ph.props
	info := fmt.Sprintf("expnet %s expcomp %s iso %s f/%s %ss", pr["EXPNET"], pr["EXPCOMP"], pr["ISO"], pr["AP_DESC"], pr["SH_DESC"])
	if xf, err := x3f.Open(ph.path); err == nil {
		if c, err := xf.CAMF(); err == nil {
			for _, k := range []string{"AutoAdjustEXPCOMP", "ExpEstimate", "ExpNet", "AutoAdjustHeadroom", "CaptureExpComp"} {
				if v := c.Matrices[k]; len(v) > 0 && len(v) <= 4 {
					info += fmt.Sprintf(" %s %.3g", k, v)
				}
			}
		}
	}
	chart := strings.Contains(strings.ToLower(filepath.ToSlash(ph.path)), "/charts/")
	rp := calib.RawPair{Name: name, Raw: raw, SPP: spp, JPEGErr: jpegErr, WB: pr["WB_DESC"], Info: info, JPEG: jpegNeutral, TrainOnly: chart}
	if goRaw {
		// Score bases on the app's own neutral rendering of them.
		comp := rawCompensation(ph)
		rp.Neutral = func(l *render.Linear) *render.Linear {
			return rgbaToLinear(render.Render(render.NewBase(l), preset.Neutral(), comp))
		}
	}
	groups[group] = append(groups[group], rp)
}

// mergeByFamily is shipped (a JSON array of objects with a "family") with
// the entries of fitted replacing those of the same family; new families
// are appended. Entries keep their shipped order.
func mergeByFamily[T any](shipped []byte, fitted []T) []byte {
	var list []json.RawMessage
	json.Unmarshal(shipped, &list)
	// Keyed by family and white balance: a per-balance entry (family|Auto)
	// replaces only the same balance's entry, never the family's WB-less base.
	keyOf := func(m json.RawMessage) string {
		var v struct {
			Family string `json:"family"`
			WB     string `json:"wb"`
		}
		json.Unmarshal(m, &v)
		return v.Family + "|" + v.WB
	}
	for _, f := range fitted {
		m, _ := json.Marshal(f)
		key := keyOf(m)
		replaced := false
		for i := range list {
			if keyOf(list[i]) == key {
				list[i], replaced = m, true
			}
		}
		if !replaced {
			list = append(list, m)
		}
	}
	js, _ := json.MarshalIndent(list, "", " ")
	return append(js, '\n')
}
