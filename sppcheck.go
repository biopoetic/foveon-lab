package main

// spp-check: the accuracy regression check against Sigma Photo Pro. Every
// photo with a neutral SPP export (<name>_A00.tif) is developed exactly as
// the app shows its preview (decoded at the working width with the app's
// noise reduction, turned, reduced, raw base, neutral render with SPP's
// sharpening) and compared with the export at the analysis width.
//
//	foveon-lab spp-check [-save now.json] [-compare before.json] [-only SD1,SDIM0031] [-step A17]
//
// -step scores another calibration step (A17 = Fill Light +1, ...) against
// the <name>_<step>.tif exports instead of the neutral A00.
//
// Three numbers per photo, mean absolute 8-bit sRGB differences: all
// pixels; "area" on both images reduced 4x (colour and tone without fine
// detail); "shadows" on the pixels SPP shows darker than linear 0.06.
// -compare prints each photo's change and flags those worse by more than
// -worse; run it before and after any change to rendering or calibration.

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
	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

type sppScore struct {
	Group  string  `json:"group"`
	Model  string  `json:"model"`
	All    float64 `json:"all"`
	Area   float64 `json:"area"`
	Shadow float64 `json:"shadow"`
	// EV is our overall brightness relative to SPP's in stops (mean linear
	// luminance); AreaEV0 is Area with that difference scaled out, i.e. what
	// a perfect exposure match would leave.
	EV      float64 `json:"ev"`
	AreaEV0 float64 `json:"areaEV0"`
}

func sppCheckCommand(desk string) {
	fs := flag.NewFlagSet("spp-check", flag.ExitOnError)
	dir := fs.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder (exports in its exports\\ and other subfolders)")
	photos := fs.String("photos", desk, "folders with the X3F files (separated by ;)")
	save := fs.String("save", "", "write the scores to this JSON file")
	compare := fs.String("compare", "", "a JSON file from an earlier -save: print each photo's change")
	only := fs.String("only", "", "comma-separated camera groups and/or photo names to check (default: all)")
	worse := fs.Float64("worse", 0.3, "with -compare: flag photos worse by more than this")
	stepCode := fs.String("step", "A00", "calibration step to score (e.g. A17 = Fill Light +1): developed with its settings, against <name>_<step>.tif")
	summary := fs.String("summary", "", "print the summary of a JSON file from an earlier -save and stop (nothing is developed)")
	fs.Parse(os.Args[2:])

	if *summary != "" {
		var saved map[string]sppScore
		if b, err := os.ReadFile(*summary); err != nil || json.Unmarshal(b, &saved) != nil {
			fmt.Printf("can't read %s\n", *summary)
			os.Exit(2)
		}
		fmt.Printf("%d photos in %s\n", len(saved), *summary)
		printSPPSummary(saved)
		return
	}

	step := strings.ToUpper(*stepCode)
	params := preset.Neutral()
	found := false
	for _, st := range calib.Steps() {
		if st.Code == step {
			params, found = st.Params, true
		}
	}
	if !found {
		fmt.Printf("no calibration step %s\n", step)
		os.Exit(2)
	}
	suffix := "_" + step + ".tif"
	exports := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(*dir, "*", "*"+suffix))
	for _, m := range matches {
		name := strings.ToUpper(strings.TrimSuffix(filepath.Base(m), suffix))
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
	scores := map[string]sppScore{}
	excluded := 0
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
			fmt.Printf("  excluded  %-10s %-8s ref %s  %s\n", name, group, filepath.Base(filepath.Dir(exp))+"/"+filepath.Base(exp), e.Reason)
			excluded++
			continue
		}
		if _, done := scores[name]; done {
			continue
		}
		s, err := sppCheckPhoto(ph, exp, params)
		if err != nil {
			fmt.Printf("  %s: %v\n", name, err)
			continue
		}
		s.Group, s.Model = group, modelOf(ph)
		scores[name] = s
		fmt.Printf("  %-10s %-8s all %6.2f  area %5.2f  shadows %5.2f  ev %+5.2f  area@ev0 %5.2f  ref %s\n", name, group, s.All, s.Area, s.Shadow, s.EV, s.AreaEV0, filepath.Base(filepath.Dir(exp))+"/"+filepath.Base(exp))
	}
	if excluded > 0 {
		fmt.Printf("%d excluded reference(s), not scored\n", excluded)
	}
	fmt.Printf("%d photos in %s\n", len(scores), time.Since(start).Round(time.Second))
	printSPPSummary(scores)
	if *compare != "" {
		var before map[string]sppScore
		if b, err := os.ReadFile(*compare); err != nil || json.Unmarshal(b, &before) != nil {
			fmt.Printf("can't read %s\n", *compare)
		} else {
			printSPPCompare(before, scores, *worse)
		}
	}
	if *save != "" {
		b, _ := json.MarshalIndent(scores, "", " ")
		if err := os.WriteFile(*save, b, 0o644); err != nil {
			fmt.Println(err)
		} else {
			fmt.Println("saved", *save)
		}
	}
}

// linearForCheck decodes ph and brings it to spp's analysis size with its
// raw base applied: the image the app's preview starts from.
func linearForCheck(ph *Photo, spp *render.Linear) (*render.Linear, error) {
	maxW := sourceW
	if ph.Rotation == 90 || ph.Rotation == 270 {
		maxW = max(sourceW, spp.H) // the short side at least the export's width
	}
	l, err := goLinear(ph, maxW, defaultNR)
	if err != nil {
		return nil, err
	}
	if ph.Rotation != 0 {
		l = l.Rotate(ph.Rotation)
	}
	l = fitAspect(l.Downscale(spp.W), spp.W, spp.H)
	c := rawCorrectionFor(ph)
	if ph.RawDecoded {
		// SD9/SD10 (no camera JPEG to show): the app applies the group's
		// base in decodePreview, which rawCorrectionFor leaves out.
		c = render.RawBaseFor(calibGroup(ph), wbDesc(ph))
	}
	if c != nil {
		c.Apply(l)
		rawJPEGBalance(ph, l)
		// The app's raw development also runs the bright near-neutral chroma
		// denoise (accurate.go); score the image the app shows.
		render.BrightChromaDenoiseFor(l, calibGroup(ph))
	}
	return l, nil
}

// checkRender develops ph as the app shows its preview in the neutral
// preset and returns the render and its SPP export, both at the analysis
// size. spp-check and spp-decomp share it so both judge the same image.
func checkRender(ph *Photo, export string) (*render.Linear, *render.Linear, error) {
	spp, err := calib.LoadLinear(export)
	if err != nil {
		return nil, nil, err
	}
	l, err := linearForCheck(ph, spp)
	if err != nil {
		return nil, nil, err
	}
	v := rgbaToLinear(render.Render(render.NewBase(l), preset.Neutral(), rawCompensation(ph)))
	return v, spp, nil
}

// sppCheckPhoto develops ph as the app's preview with settings p and scores
// it against its SPP export.
func sppCheckPhoto(ph *Photo, export string, p preset.Params) (sppScore, error) {
	spp, err := calib.LoadLinear(export)
	if err != nil {
		return sppScore{}, err
	}
	l, err := linearForCheck(ph, spp)
	if err != nil {
		return sppScore{}, err
	}
	v := rgbaToLinear(render.Render(render.NewBase(l), p, rawCompensation(ph)))
	s := sppScore{All: calib.MeanError(v, spp)}
	s.Area = calib.MeanError(v.Downscale(spp.W/4), spp.Downscale(spp.W/4))
	var sum float64
	n := 0
	for i := 0; i+2 < len(spp.Pix); i += 3 {
		y := 0.2126*spp.Pix[i] + 0.7152*spp.Pix[i+1] + 0.0722*spp.Pix[i+2]
		if y >= 0.06 {
			continue
		}
		for k := 0; k < 3; k++ {
			sum += math.Abs(float64(render.ToSRGB(clamp01(v.Pix[i+k]))-render.ToSRGB(clamp01(spp.Pix[i+k])))) * 255
		}
		n += 3
	}
	if n > 0 {
		s.Shadow = sum / float64(n)
	}
	var yo, ys float64
	for i := 0; i+2 < len(spp.Pix) && i+2 < len(v.Pix); i += 3 {
		yo += 0.2126*float64(v.Pix[i]) + 0.7152*float64(v.Pix[i+1]) + 0.0722*float64(v.Pix[i+2])
		ys += 0.2126*float64(spp.Pix[i]) + 0.7152*float64(spp.Pix[i+1]) + 0.0722*float64(spp.Pix[i+2])
	}
	if yo > 0 && ys > 0 {
		s.EV = math.Log2(yo / ys)
		k := float32(ys / yo)
		m := v.Downscale(spp.W / 4)
		for i := range m.Pix {
			m.Pix[i] *= k
		}
		s.AreaEV0 = calib.MeanError(m, spp.Downscale(spp.W/4))
	}
	return s, nil
}

func clamp01(v float32) float32 { return max(0, min(1, v)) }

// sppTarget is the project's goal for colour and tone against SPP: a mean
// error under it is invisible side by side.
const sppTarget = 3.0

// sppGroupStat is one camera group's summary. The mean is what the project
// has always quoted, but a few bad photos move it a long way; the median
// and the count under the target say where a typical photo stands.
type sppGroupStat struct {
	Group                      string
	N                          int
	All, Area, Shadow          float64 // means
	AreaMedian                 float64
	UnderTarget                int // photos with Area < sppTarget
	OffExposure                int // photos with |EV| > 0.25
	OffExposureAll, OffExpArea float64
}

func sppGroupStats(scores map[string]sppScore) []sppGroupStat {
	type acc struct {
		st    sppGroupStat
		areas []float64
	}
	by := map[string]*acc{}
	for _, s := range scores {
		for _, g := range []string{s.Group, "ALL"} {
			a := by[g]
			if a == nil {
				a = &acc{st: sppGroupStat{Group: g}}
				by[g] = a
			}
			a.st.N++
			a.st.All += s.All
			a.st.Area += s.Area
			a.st.Shadow += s.Shadow
			a.areas = append(a.areas, s.Area)
			if s.Area < sppTarget {
				a.st.UnderTarget++
			}
			if math.Abs(s.EV) > 0.25 {
				a.st.OffExposure++
				a.st.OffExposureAll += s.All
				a.st.OffExpArea += s.Area
			}
		}
	}
	var out []sppGroupStat
	for _, a := range by {
		n := float64(a.st.N)
		a.st.All, a.st.Area, a.st.Shadow = a.st.All/n, a.st.Area/n, a.st.Shadow/n
		sort.Float64s(a.areas)
		if m := len(a.areas); m%2 == 1 {
			a.st.AreaMedian = a.areas[m/2]
		} else {
			a.st.AreaMedian = (a.areas[m/2-1] + a.areas[m/2]) / 2
		}
		if a.st.OffExposure > 0 {
			a.st.OffExposureAll /= float64(a.st.OffExposure)
			a.st.OffExpArea /= float64(a.st.OffExposure)
		}
		out = append(out, a.st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	return out
}

func printSPPSummary(scores map[string]sppScore) {
	stats := sppGroupStats(scores)
	for _, a := range stats {
		fmt.Printf("%-8s %3d photos: all %.3f  area %.3f  shadows %.3f  | area median %.2f, %d of %d under %.0f\n",
			a.Group, a.N, a.All, a.Area, a.Shadow, a.AreaMedian, a.UnderTarget, a.N, sppTarget)
	}
	// A neutral SPP export lands within a few hundredths of a stop of our own
	// brightness. A row far off that is one of two things: a reference made
	// with other SPP settings, or a render of ours that is too dark or too
	// bright. Both have been found here (RAW_SIGMA_DP1S, ISO 50: SPP's export
	// is normally exposed and ours is 1.2 stops dark and red), so these rows
	// are listed, not excused: look at each before blaming the reference.
	for _, a := range stats {
		if a.Group == "ALL" && a.OffExposure > 0 {
			fmt.Printf("          %d of %d photos are off in exposure (|ev| > 0.25), averaging %.2f: the reference or our render, check each\n", a.OffExposure, a.N, a.OffExposureAll)
		}
	}
	var names []string
	for name, s := range scores {
		if math.Abs(s.EV) > 0.25 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		s := scores[name]
		fmt.Printf("            %-36s %-8s area %6.2f  ev %+5.2f  area@ev0 %6.2f\n", name, s.Group, s.Area, s.EV, s.AreaEV0)
	}
}

func printSPPCompare(before, now map[string]sppScore, worse float64) {
	type d struct {
		name string
		s    sppScore
		diff float64
	}
	var ds []d
	var b, n float64
	for name, s := range now {
		if p, ok := before[name]; ok {
			ds = append(ds, d{name, s, s.All - p.All})
			b += p.All
			n += s.All
		}
	}
	if len(ds) == 0 {
		fmt.Println("no photos in common with the earlier run")
		return
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].diff < ds[j].diff })
	fmt.Printf("compared %d photos: all %.3f -> %.3f\n", len(ds), b/float64(len(ds)), n/float64(len(ds)))
	better, flagged := 0, 0
	for _, x := range ds {
		if x.diff < -0.05 {
			better++
		}
		if x.diff > worse {
			flagged++
			fmt.Printf("  WORSE %-10s %-8s %+.2f (now %.2f)\n", x.name, x.s.Group, x.diff, x.s.All)
		}
	}
	fmt.Printf("%d better by > 0.05, %d worse by > %.2f\n", better, flagged, worse)
}
