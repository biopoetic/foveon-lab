package main

// spp-check: the accuracy regression check against Sigma Photo Pro. Every
// photo with a neutral SPP export (<name>_A00.tif) is developed exactly as
// the app shows its preview (decoded at the working width with the app's
// noise reduction, turned, reduced, raw base, neutral render with SPP's
// sharpening) and compared with the export at the analysis width.
//
//	foveon-lab spp-check [-save now.json] [-compare before.json] [-only SD1,SDIM0031]
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
	All    float64 `json:"all"`
	Area   float64 `json:"area"`
	Shadow float64 `json:"shadow"`
}

func sppCheckCommand(desk string) {
	fs := flag.NewFlagSet("spp-check", flag.ExitOnError)
	dir := fs.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder (exports in its exports\\ and other subfolders)")
	photos := fs.String("photos", desk, "folders with the X3F files (separated by ;)")
	save := fs.String("save", "", "write the scores to this JSON file")
	compare := fs.String("compare", "", "a JSON file from an earlier -save: print each photo's change")
	only := fs.String("only", "", "comma-separated camera groups and/or photo names to check (default: all)")
	worse := fs.Float64("worse", 0.3, "with -compare: flag photos worse by more than this")
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
	start := time.Now()
	scores := map[string]sppScore{}
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
		if _, done := scores[name]; done {
			continue
		}
		s, err := sppCheckPhoto(ph, exp)
		if err != nil {
			fmt.Printf("  %s: %v\n", name, err)
			continue
		}
		s.Group = group
		scores[name] = s
		fmt.Printf("  %-10s %-8s all %6.2f  area %5.2f  shadows %5.2f\n", name, group, s.All, s.Area, s.Shadow)
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

// sppCheckPhoto develops ph as the app's preview and scores it against its
// SPP export.
func sppCheckPhoto(ph *Photo, export string) (sppScore, error) {
	spp, err := calib.LoadLinear(export)
	if err != nil {
		return sppScore{}, err
	}
	maxW := sourceW
	if ph.rotation == 90 || ph.rotation == 270 {
		maxW = max(sourceW, spp.H) // the short side at least the export's width
	}
	l, err := goLinear(ph, maxW, defaultNR)
	if err != nil {
		return sppScore{}, err
	}
	if ph.rotation != 0 {
		l = l.Rotate(ph.rotation)
	}
	l = fitAspect(l.Downscale(spp.W), spp.W, spp.H)
	if c := rawCorrectionFor(ph); c != nil {
		c.Apply(l)
	}
	v := rgbaToLinear(render.Render(render.NewBase(l), preset.Neutral(), rawCompensation(ph)))
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
	return s, nil
}

func clamp01(v float32) float32 { return max(0, min(1, v)) }

func printSPPSummary(scores map[string]sppScore) {
	type agg struct {
		n                 int
		all, area, shadow float64
	}
	by := map[string]*agg{}
	for _, s := range scores {
		for _, g := range []string{s.Group, "ALL"} {
			a := by[g]
			if a == nil {
				a = &agg{}
				by[g] = a
			}
			a.n++
			a.all += s.All
			a.area += s.Area
			a.shadow += s.Shadow
		}
	}
	var gs []string
	for g := range by {
		gs = append(gs, g)
	}
	sort.Strings(gs)
	for _, g := range gs {
		a := by[g]
		fmt.Printf("%-8s %3d photos: all %.3f  area %.3f  shadows %.3f\n", g, a.n, a.all/float64(a.n), a.area/float64(a.n), a.shadow/float64(a.n))
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
