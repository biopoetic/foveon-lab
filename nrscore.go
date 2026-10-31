package main

// nr-score: the high-ISO scoreboard for Foveon Lab against Sigma Photo Pro.
//
// It finds every ISO ladder on disk that has neutral SPP exports (an ISO 100
// frame plus ISO 800-6400 frames of the same scene), renders each frame the
// way the app exports it (full-resolution raw, NR Low, SPP's neutral look),
// and measures both pipelines against their own ISO 100 frame:
//
//   - noise power in flat areas, luminance and chroma, per spatial band;
//   - retained detail per band (slope of the high-ISO band on the ISO 100
//     band, so noise does not bias it and a smoother image scores lower);
//   - low-frequency chroma mottling ("blotches") and the drift of the
//     frame's mean a*/b* against the reference;
//   - banding: the spread of the high-pass image's row and column means.
//
// The flat-area mask comes from the ISO 100 reference's 16x16 blocks, so
// the noise itself cannot choose its own flat areas, and both pipelines are
// measured on the same blocks. Analysis is at the SPP export's own size (a
// reduced-resolution SPP export is compared with our render reduced to the
// same size) - not at preview size, where the upscale would hide detail.
//
//	foveon-lab nr-score [-photos dir] [-dir calib] [-miniso 800] [-save s.json] [-csv s.csv] [-only DPM]
//
// Numbers always come with the photo/block counts they rest on.

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

// nrMetric is one pipeline's measurement of one frame against its ISO 100
// reference. Bands are spatial: 0-1, 1-2, 2-4 and 4-8 px.
type nrMetric struct {
	NoiseY [4]float64 `json:"noiseY"` // 8-bit sRGB luminance, sigma
	NoiseA [4]float64 `json:"noiseA"` // CIELAB a*, sigma
	NoiseB [4]float64 `json:"noiseB"` // CIELAB b*, sigma
	Detail [4]float64 `json:"detail"` // slope of this band on this pipeline's ISO 100
	// DetailBasis is the same band-by-band slope, but on one fixed reference
	// per ladder: the ISO 100 frame rendered with noise reduction off (the
	// tools/sharpfit -gain basis). It does not move with the pipeline under
	// test, so ours before/after/off and SPP can be compared on one axis.
	DetailBasis [4]float64 `json:"detailBasis"`
	// Impulse is single-pixel impulse dots per megapixel in the flat blocks:
	// a pixel 80 or more 8-bit levels from the median of its eight neighbours
	// while those neighbours span less than 12. Black and white dust, not
	// texture or ordinary noise (80 levels is 8-10 sigma at ISO 6400 and 20
	// at ISO 800; a 48-level test counted 2,000/MP of grain at 6400).
	Impulse float64 `json:"impulse"`
	// Blotch is the 8-32 px band of a*/b* in flat areas: the low-frequency
	// chroma mottling SPP removes and a per-pixel filter does not.
	BlotchA float64 `json:"blotchA"`
	BlotchB float64 `json:"blotchB"`
	// Drift is this pipeline's mean a*/b* minus its own ISO 100 frame's.
	DriftA float64 `json:"driftA"`
	DriftB float64 `json:"driftB"`
	// Banding: standard deviation of the high-pass image's row and column
	// means (8-bit luminance).
	RowBand float64 `json:"rowBand"`
	ColBand float64 `json:"colBand"`
	Blocks  int     `json:"blocks"`
}

type nrRecord struct {
	Ladder string   `json:"ladder"`
	Ref    string   `json:"ref"`
	Name   string   `json:"name"`
	Group  string   `json:"group"`
	Model  string   `json:"model"`
	ISO    float64  `json:"iso"`
	W      int      `json:"w"`
	H      int      `json:"h"`
	Ours   nrMetric `json:"ours"`
	SPP    nrMetric `json:"spp"`
	// OursMS/SppMS: wall time for our full-resolution render and for loading
	// SPP's export, for the performance line.
	OursMS float64 `json:"oursMs"`
	SppMS  float64 `json:"sppMs"`
}

type nrLadder struct {
	key    string
	ref    *Photo
	refEx  string
	refISO float64
	items  []nrItem
}

type nrItem struct {
	ph  *Photo
	exp string
	iso float64
	n   int
}

var reLadder = regexp.MustCompile(`^(.*?)(\d+)(H?)$`)

func nrScoreCommand(desk string) {
	fs := flag.NewFlagSet("nr-score", flag.ExitOnError)
	dir := fs.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder (exports in its exports\\ and other subfolders)")
	photos := fs.String("photos", filepath.Join(desk, "foveon-samples"), "folders with the X3F files (separated by ;)")
	minISO := fs.Float64("miniso", 800, "lowest ISO scored as high ISO")
	save := fs.String("save", "", "write every per-photo measurement to this JSON file")
	csv := fs.String("csv", "", "write per-photo measurements to this CSV file")
	only := fs.String("only", "", "comma-separated camera groups to score (default: all)")
	nr := fs.Float64("nr", defaultNR, "noise reduction strength of our render (the app's default is 0.3)")
	cropDir := fs.String("cropdir", "", "write a centre crop of ours and of SPP for the crop ISO into this folder")
	cropISO := fs.Float64("cropiso", 6400, "with -cropdir: the ISO to crop")
	cropSize := fs.Int("cropsize", 480, "with -cropdir: crop side in pixels")
	cropX := fs.Int("cropx", -1, "with -cropdir: crop centre x (-1 = frame centre)")
	cropY := fs.Int("cropy", -1, "with -cropdir: crop centre y (-1 = frame centre)")
	cropOnly := fs.Bool("croponly", false, "with -cropdir: only render the crop ISO frames (no metrics)")
	fs.Parse(os.Args[2:])

	want := map[string]bool{}
	for _, s := range strings.Split(*only, ",") {
		if s = strings.TrimSpace(s); s != "" {
			want[strings.ToUpper(s)] = true
		}
	}

	// The neutral exports, keyed by photo name, as spp-check finds them.
	exports := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(*dir, "*", "*_A00.tif"))
	for _, m := range matches {
		name := strings.ToUpper(strings.TrimSuffix(filepath.Base(m), "_A00.tif"))
		if _, ok := exports[name]; !ok || filepath.Base(filepath.Dir(m)) == "exports" {
			exports[name] = m
		}
	}

	var phs []*Photo
	for _, ph := range scanPhotos(strings.Split(*photos, ";"), false) {
		if ph.Kind == "x3f" && ph.Unsupported == "" && ph.props["ISO"] != "" {
			phs = append(phs, ph)
		}
	}
	ladders := findLadders(phs, exports, *minISO, want)
	if len(ladders) == 0 {
		fmt.Println("no ISO ladders with SPP exports found")
		return
	}
	fmt.Printf("%d ladder(s), %d frame(s) to score; our NR %.1f, analysis at SPP's export size\n", len(ladders), len(ladders)*len(ladders[0].items), *nr)
	for _, l := range ladders {
		fmt.Printf("  %-22s %-10s ref ISO %g, %d frame(s)\n", l.key, calibGroup(l.ref), l.refISO, len(l.items))
	}

	var recs []nrRecord
	crop := nrCrop{dir: *cropDir, iso: *cropISO, size: *cropSize, x: *cropX, y: *cropY, only: *cropOnly}
	for _, l := range ladders {
		rs, err := scoreLadder(l, *nr, crop)
		if err != nil {
			fmt.Printf("%s: %v\n", l.key, err)
			continue
		}
		recs = append(recs, rs...)
	}

	printNRSummary(recs)
	if *csv != "" {
		if err := writeNRCSV(*csv, recs); err != nil {
			fmt.Println(err)
		} else {
			fmt.Println("saved", *csv)
		}
	}
	if *save != "" {
		b, _ := json.MarshalIndent(recs, "", " ")
		if err := os.WriteFile(*save, b, 0o644); err != nil {
			fmt.Println(err)
		} else {
			fmt.Println("saved", *save)
		}
	}
}

// findLadders groups the photos by name prefix and keeps the groups that
// form an ISO series: one frame per ISO, an ISO <= 200 reference, and at
// least one frame at or above minISO. A group whose numeric suffixes are
// consecutive (01..07) uses the longest strictly rising ISO run, so the
// other shots that share a prefix are not pulled into the ladder.
func findLadders(phs []*Photo, exports map[string]string, minISO float64, want map[string]bool) []*nrLadder {
	type f struct {
		ph  *Photo
		n   int
		iso float64
	}
	groups := map[string][]f{}
	for _, ph := range phs {
		name := strings.ToUpper(strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name)))
		if _, ok := exports[name]; !ok {
			continue
		}
		if len(want) > 0 && !want[strings.ToUpper(calibGroup(ph))] {
			continue
		}
		if _, bad := calib.ExcludedReference(name, calibGroup(ph)); bad {
			continue
		}
		m := reLadder.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		key := m[1] + "|" + m[3]
		groups[key] = append(groups[key], f{ph, n, photoISO(ph)})
	}
	var out []*nrLadder
	valid := func(fs []f) bool {
		if len(fs) < 3 {
			return false
		}
		seen := map[float64]bool{}
		low, high := false, false
		for _, x := range fs {
			if seen[x.iso] {
				return false
			}
			seen[x.iso] = true
			if x.iso <= 200 {
				low = true
			}
			if x.iso >= minISO {
				high = true
			}
		}
		return low && high
	}
	for key, fs := range groups {
		sort.Slice(fs, func(i, j int) bool { return fs[i].n < fs[j].n })
		// Consecutive-suffix runs, longest first.
		var best []f
		for i := 0; i < len(fs); {
			j := i + 1
			for j < len(fs) && fs[j].n == fs[j-1].n+1 && fs[j].iso > fs[j-1].iso {
				j++
			}
			run := fs[i:j]
			if valid(run) && len(run) > len(best) {
				best = run
			}
			i = j
		}
		if best == nil && valid(fs) {
			// A ladder whose suffix is the ISO itself (DP1MhSLI0100...).
			best = append([]f(nil), fs...)
			sort.Slice(best, func(i, j int) bool { return best[i].iso < best[j].iso })
		}
		if best == nil {
			continue
		}
		sort.Slice(best, func(i, j int) bool { return best[i].iso < best[j].iso })
		l := &nrLadder{key: strings.ReplaceAll(key, "|", ""), ref: best[0].ph, refISO: best[0].iso}
		l.refEx = exports[strings.ToUpper(strings.TrimSuffix(best[0].ph.Name, filepath.Ext(best[0].ph.Name)))]
		for _, x := range best[1:] {
			name := strings.ToUpper(strings.TrimSuffix(x.ph.Name, filepath.Ext(x.ph.Name)))
			l.items = append(l.items, nrItem{ph: x.ph, exp: exports[name], iso: x.iso, n: x.n})
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

type nrCrop struct {
	dir  string
	iso  float64
	size int
	x, y int // crop centre; -1 = frame centre
	only bool
}

// scoreLadder renders and measures one ladder against its ISO 100 frame.
func scoreLadder(l *nrLadder, nr float64, crop nrCrop) ([]nrRecord, error) {
	if crop.only && crop.dir != "" {
		var out []nrRecord
		for _, it := range l.items {
			if it.iso != crop.iso {
				continue
			}
			spp, err := loadExport(it.exp)
			if err != nil {
				fmt.Printf("  %s: %v\n", it.exp, err)
				continue
			}
			ours, ms, err := ourFullRender(it.ph, nr)
			if err != nil {
				fmt.Printf("  %s: %v\n", it.ph.Name, err)
				continue
			}
			ours, spp = matchSize(ours, spp)
			saveNRCrops(crop, l.key, it.ph.Name, ours, spp)
			out = append(out, nrRecord{Ladder: l.key, Ref: l.ref.Name, Name: it.ph.Name,
				Group: calibGroup(it.ph), Model: modelOf(it.ph), ISO: it.iso, W: ours.W, H: ours.H, OursMS: ms})
			fmt.Printf("  crop %-18s ISO %g %dx%d\n", it.ph.Name, it.iso, ours.W, ours.H)
		}
		return out, nil
	}
	t0 := time.Now()
	refOurs, _, err := ourFullRender(l.ref, nr)
	if err != nil {
		return nil, err
	}
	refSPP, err := loadExport(l.refEx)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", l.refEx, err)
	}
	refOurs, refSPP = matchSize(refOurs, refSPP)
	// The fixed detail basis: the ISO 100 frame without noise reduction (the
	// same raw data, app settings and size as ours), so the reference does
	// not get softer when a pipeline's restore is switched off.
	refBasis, _, err := ourFullRender(l.ref, 0)
	if err != nil {
		return nil, err
	}
	refBasis, _ = matchSize(refBasis, refSPP)
	refBasisLab := toLab8(refBasis)
	refOursLab, refSPPLab := toLab8(refOurs), toLab8(refSPP)
	refOurs, refSPP = nil, nil
	refBasis = nil
	fmt.Printf("%s: ref %s ISO %g %.2fs\n", l.key, l.ref.Name, l.refISO, time.Since(t0).Seconds())

	var out []nrRecord
	for _, it := range l.items {
		t := time.Now()
		spp, err := loadExport(it.exp)
		if err != nil {
			fmt.Printf("  %s: %v\n", it.exp, err)
			continue
		}
		sppMS := time.Since(t).Seconds() * 1000
		t = time.Now()
		ours, oursMS, err := ourFullRender(it.ph, nr)
		if err != nil {
			fmt.Printf("  %s: %v\n", it.ph.Name, err)
			continue
		}
		ours, spp = matchSize(ours, spp)
		if crop.dir != "" && it.iso == crop.iso {
			saveNRCrops(crop, l.key, it.ph.Name, ours, spp)
		}
		// Reference planes at this frame's size: SPP's reduced-resolution
		// frames are compared at their own size, so the ISO 100 reference is
		// reduced to it too.
		rO, rS := refOursLab, refSPPLab
		rB := refBasisLab
		if rO.w != ours.W || rO.h != ours.H {
			rO = refOursLab.resize(ours.W, ours.H)
		}
		if rS.w != ours.W || rS.h != ours.H {
			rS = refSPPLab.resize(ours.W, ours.H)
		}
		if rB.w != ours.W || rB.h != ours.H {
			rB = refBasisLab.resize(ours.W, ours.H)
		}
		selO, blocks := flatMask(rO)
		selS := selO // the same scene blocks for both pipelines
		oLab, sLab := toLab8(ours), toLab8(spp)
		om := measureNR(oLab, rO, rB, selO)
		sm := measureNR(sLab, rS, rB, selS)
		om.Blocks, sm.Blocks = blocks, blocks
		out = append(out, nrRecord{
			Ladder: l.key, Ref: l.ref.Name, Name: it.ph.Name,
			Group: calibGroup(it.ph), Model: modelOf(it.ph), ISO: it.iso,
			W: ours.W, H: ours.H, Ours: om, SPP: sm,
			OursMS: oursMS, SppMS: sppMS,
		})
		fmt.Printf("  %-18s ISO %4g %dx%d  luma %.2f/%.2f  chroma %.2f/%.2f  detail2 %.2f/%.2f  blotch %.2f/%.2f  %.2fs\n",
			it.ph.Name, it.iso, ours.W, ours.H,
			sqrtMean(om.NoiseY), sqrtMean(sm.NoiseY),
			sqrtMean3(om.NoiseA, om.NoiseB), sqrtMean3(sm.NoiseA, sm.NoiseB),
			om.Detail[2], sm.Detail[2], om.BlotchA+om.BlotchB, sm.BlotchA+sm.BlotchB, oursMS/1000)
	}
	return out, nil
}

// loadExport reads an SPP export at its native size (calib.LoadLinear, used
// by spp-check, reduces to the 1200 px analysis width - too small to see
// sensor noise, let alone measure it).
func loadExport(path string) (*render.Linear, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	img, _, err := render.DecodeImage(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return render.FromImage(img, 0, 0), nil
}

// saveNRCrops writes the same centre crop of our render and of SPP's, both
// as sRGB JPEG, so a before/after/SPP strip can be assembled from two runs.
func saveNRCrops(c nrCrop, ladder, name string, ours, spp *render.Linear) {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		fmt.Println(err)
		return
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	for _, it := range []struct {
		tag string
		l   *render.Linear
	}{{"ours", ours}, {"spp", spp}} {
		cs := min(c.size, min(it.l.W, it.l.H))
		x0, y0 := (it.l.W-cs)/2, (it.l.H-cs)/2
		if c.x >= 0 && c.y >= 0 {
			x0, y0 = max(0, min(it.l.W-cs, c.x-cs/2)), max(0, min(it.l.H-cs, c.y-cs/2))
		}
		img := image.NewRGBA(image.Rect(0, 0, cs, cs))
		for y := 0; y < cs; y++ {
			for x := 0; x < cs; x++ {
				i := ((y0+y)*it.l.W + x0 + x) * 3
				for k := 0; k < 3; k++ {
					v := render.ToSRGB(max(0, min(1, it.l.Pix[i+k])))
					img.Pix[y*img.Stride+x*4+k] = uint8(v*255 + 0.5)
				}
				img.Pix[y*img.Stride+x*4+3] = 255
			}
		}
		path := filepath.Join(c.dir, base+"_"+it.tag+".jpg")
		f, err := os.Create(path)
		if err != nil {
			fmt.Println(err)
			return
		}
		jpeg.Encode(f, img, &jpeg.Options{Quality: 88})
		f.Close()
	}
}

// ourFullRender renders ph as a full-resolution export at NR strength nr,
// with SPP's neutral look and the app's compensation. It returns the linear
// image and the wall time of the render.
func ourFullRender(ph *Photo, nr float64) (*render.Linear, float64, error) {
	t := time.Now()
	l, err := rawLinearFor(ph, 0, rawExportColor(nrPtr(nr)), "")
	if err != nil {
		return nil, 0, err
	}
	img := render.Render(render.NewBase(l), preset.Neutral(), rawCompensation(ph))
	return rgbaToLinear(img), float64(time.Since(t).Milliseconds()), nil
}

// nrPtr maps a strength to the decode name the app uses (so the same NR
// path runs at any strength).
func nrPtr(nr float64) *int {
	n := int(math.Round(nr * 10))
	return &n
}

// matchSize brings ours and SPP to one size: the SPP export decides it
// (that is the size SPP rendered), and ours is reduced to it. When our
// frame is the smaller one, SPP's is reduced instead (a preview-size
// comparison, not used for the full-frame ladders). A frame at another
// aspect is centre-cropped first: our Quattro render is the full 3:2 sensor
// while SPP exports the camera's 16:9 crop (5424x3616 against 5424x3048),
// and a top-left crop there compares two different parts of the scene.
func matchSize(a, b *render.Linear) (*render.Linear, *render.Linear) {
	if a.W*b.H != b.W*a.H {
		if a.W*b.H > b.W*a.H {
			if cw := int(math.Round(float64(a.H) * float64(b.W) / float64(b.H))); cw > 0 && cw <= a.W {
				a = cropLinear(a, (a.W-cw)/2, 0, cw, a.H)
			}
		} else {
			if ch := int(math.Round(float64(a.W) * float64(b.H) / float64(b.W))); ch > 0 && ch <= a.H {
				a = cropLinear(a, 0, (a.H-ch)/2, a.W, ch)
			}
		}
	}
	w := min(a.W, b.W)
	if a.W != w {
		a = a.Downscale(w)
	}
	if b.W != w {
		b = b.Downscale(w)
	}
	h := min(a.H, b.H)
	a = cropLinear(a, (a.W-w)/2, (a.H-h)/2, w, h)
	b = cropLinear(b, (b.W-w)/2, (b.H-h)/2, w, h)
	return a, b
}

// lab8 is an image in measurement space: y is 8-bit sRGB luminance and a/b
// are CIELAB, all at the same size.
type lab8 struct {
	w, h int
	y    []float32
	a, b []float32
}

func toLab8(l *render.Linear) *lab8 {
	o := &lab8{w: l.W, h: l.H, y: make([]float32, l.W*l.H), a: make([]float32, l.W*l.H), b: make([]float32, l.W*l.H)}
	for i := 0; i < l.W*l.H; i++ {
		r := float64(max(0, min(1, l.Pix[i*3])))
		g := float64(max(0, min(1, l.Pix[i*3+1])))
		bl := float64(max(0, min(1, l.Pix[i*3+2])))
		y := 0.2126*r + 0.7152*g + 0.0722*bl
		o.y[i] = render.ToSRGB(float32(y)) * 255
		_, a, b := labD65(r, g, bl)
		o.a[i], o.b[i] = float32(a), float32(b)
	}
	return o
}

// resize reduces a measurement image to w×h by area-averaging each plane.
func (p *lab8) resize(w, h int) *lab8 {
	return &lab8{w: w, h: h, y: areaPlane(p.y, p.w, p.h, w, h), a: areaPlane(p.a, p.w, p.h, w, h), b: areaPlane(p.b, p.w, p.h, w, h)}
}

func areaPlane(p []float32, w, h, dw, dh int) []float32 {
	out := make([]float32, dw*dh)
	for y := 0; y < dh; y++ {
		y0 := y * h / dh
		y1 := max(y0+1, (y+1)*h/dh)
		for x := 0; x < dw; x++ {
			x0 := x * w / dw
			x1 := max(x0+1, (x+1)*w/dw)
			var s float32
			for yy := y0; yy < y1; yy++ {
				row := p[yy*w:]
				for xx := x0; xx < x1; xx++ {
					s += row[xx]
				}
			}
			out[y*dw+x] = s / float32((y1-y0)*(x1-x0))
		}
	}
	return out
}

// labD65 is calib's linear-sRGB -> CIELAB conversion, repeated here because
// that one is unexported and this tool measures rather than scores colour
// difference.
func labD65(r, g, b float64) (L, A, B float64) {
	x := 0.4124564*r + 0.3575761*g + 0.1804375*b
	y := 0.2126729*r + 0.7151522*g + 0.0721750*b
	z := 0.0193339*r + 0.1191920*g + 0.9503041*b
	f := func(t float64) float64 {
		if t > 216.0/24389 {
			return math.Cbrt(t)
		}
		return (24389.0/27*t + 16) / 116
	}
	fx, fy, fz := f(x/0.95047), f(y), f(z/1.08883)
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

// flatMask chooses 16x16 blocks that are flat in the ISO 100 reference,
// per brightness band, so the sample covers highlights, midtones and
// shadows instead of whatever happens to be smoothest. It returns a
// pixel-selection mask and the number of blocks used.
func flatMask(ref *lab8) ([]bool, int) {
	const B = 16
	bw, bh := ref.w/B, ref.h/B
	if bw < 2 || bh < 2 {
		// Tiny frame: every pixel counts as flat.
		return make([]bool, ref.w*ref.h), 0
	}
	type blk struct {
		bx, by int
		mean   float64
		score  float64
	}
	var blocks []blk
	for by := 0; by < bh; by++ {
		for bx := 0; bx < bw; bx++ {
			var n, sy, sa, sb, syy float64
			for y := by * B; y < (by+1)*B; y++ {
				for x := bx * B; x < (bx+1)*B; x++ {
					i := y*ref.w + x
					vy := float64(ref.y[i])
					n++
					sy += vy
					syy += vy * vy
					sa += float64(ref.a[i])
					sb += float64(ref.b[i])
				}
			}
			my := sy / n
			vy := math.Max(0, syy/n-my*my)
			var va, vb float64
			for y := by * B; y < (by+1)*B; y++ {
				for x := bx * B; x < (bx+1)*B; x++ {
					i := y*ref.w + x
					va += math.Pow(float64(ref.a[i])-sa/n, 2)
					vb += math.Pow(float64(ref.b[i])-sb/n, 2)
				}
			}
			// Fine-detail spread dominates; chroma spread counts a little,
			// so a smooth colour gradient is not mistaken for texture.
			score := math.Sqrt(vy) + 0.35*(math.Sqrt(va/n)+math.Sqrt(vb/n))
			blocks = append(blocks, blk{bx, by, my, score})
		}
	}
	// Six brightness bands: deep shadows to near-white.
	edges := []float64{-1, 32, 64, 96, 128, 176, 256}
	sel := make([]bool, ref.w*ref.h)
	nsel := 0
	for e := 0; e+1 < len(edges); e++ {
		var bin []blk
		for _, b := range blocks {
			if b.mean >= edges[e] && b.mean < edges[e+1] {
				bin = append(bin, b)
			}
		}
		if len(bin) == 0 {
			continue
		}
		sort.Slice(bin, func(i, j int) bool { return bin[i].score < bin[j].score })
		take := max(4, len(bin)*35/100)
		if take > len(bin) {
			take = len(bin)
		}
		for _, b := range bin[:take] {
			for y := b.by * B; y < (b.by+1)*B; y++ {
				for x := b.bx * B; x < (b.bx+1)*B; x++ {
					sel[y*ref.w+x] = true
				}
			}
			nsel++
		}
	}
	return sel, nsel
}

var nrBands = [5]int{0, 1, 2, 4, 8} // band k: box(r[k]) - box(r[k+1])

// measureNR computes one pipeline's metrics for target against its own
// ISO 100 reference, over the shared flat-block selection. Noise is the
// band-passed target-minus-reference in the flat blocks, so the scene (and
// the reference's own small noise) is not counted as noise. The detail
// slopes are also measured on basis, the same no-NR ISO 100 frame for every
// pipeline, so they do not move when the pipeline under test changes.
func measureNR(target, ref, basis *lab8, sel []bool) nrMetric {
	var m nrMetric
	w, h := target.w, target.h
	dy := make([]float32, len(target.y))
	for i := range dy {
		dy[i] = target.y[i] - ref.y[i]
	}
	da := make([]float32, len(target.a))
	db := make([]float32, len(target.b))
	for i := range da {
		da[i] = target.a[i] - ref.a[i]
		db[i] = target.b[i] - ref.b[i]
	}
	// Luminance bands: noise on the selected blocks, detail against the
	// reference over all pixels (noise is uncorrelated with the reference,
	// so it does not bias the slope).
	for b := 0; b < 4; b++ {
		tb := bandPlane(target.y, w, h, nrBands[b], nrBands[b+1])
		rb := bandPlane(ref.y, w, h, nrBands[b], nrBands[b+1])
		bb := bandPlane(basis.y, w, h, nrBands[b], nrBands[b+1])
		nb := bandPlane(dy, w, h, nrBands[b], nrBands[b+1])
		var ss, sxy, sxx, bxy, bxx float64
		var n float64
		for i := range tb {
			if sel[i] {
				ss += float64(nb[i]) * float64(nb[i])
				n++
			}
			if ref.y[i] > 4 && ref.y[i] < 251 {
				x, y := float64(rb[i]), float64(tb[i])
				sxy += x * y
				sxx += x * x
			}
			if basis.y[i] > 4 && basis.y[i] < 251 {
				bxy += float64(bb[i]) * float64(tb[i])
				bxx += float64(bb[i]) * float64(bb[i])
			}
		}
		if n > 0 {
			m.NoiseY[b] = math.Sqrt(ss / n)
		}
		if sxx > 1e-9 {
			m.Detail[b] = sxy / sxx
		}
		if bxx > 1e-9 {
			m.DetailBasis[b] = bxy / bxx
		}
	}
	// Chroma noise per band.
	for b := 0; b < 4; b++ {
		ta := bandPlane(da, w, h, nrBands[b], nrBands[b+1])
		tb := bandPlane(db, w, h, nrBands[b], nrBands[b+1])
		var sa, sb float64
		var n float64
		for i := range ta {
			if sel[i] {
				sa += float64(ta[i]) * float64(ta[i])
				sb += float64(tb[i]) * float64(tb[i])
				n++
			}
		}
		if n > 0 {
			m.NoiseA[b] = math.Sqrt(sa / n)
			m.NoiseB[b] = math.Sqrt(sb / n)
		}
	}
	// Low-frequency chroma mottling (8-32 px) in the flat blocks: the
	// added colour variation, not the scene's own colour gradients.
	for _, k := range []struct {
		plane []float32
		out   *float64
	}{{da, &m.BlotchA}, {db, &m.BlotchB}} {
		b8 := boxPlane(k.plane, w, h, 8)
		b32 := boxPlane(k.plane, w, h, 32)
		var s float64
		var n float64
		for i := range b8 {
			if sel[i] {
				d := float64(b8[i] - b32[i])
				s += d * d
				n++
			}
		}
		if n > 0 {
			*k.out = math.Sqrt(s / n)
		}
	}
	// Drift of the frame's mean chroma against the reference's.
	var ta, tb, ra, rb float64
	var n float64
	for i := range target.a {
		if !sel[i] {
			continue
		}
		ta += float64(target.a[i])
		tb += float64(target.b[i])
		ra += float64(ref.a[i])
		rb += float64(ref.b[i])
		n++
	}
	if n > 0 {
		m.DriftA = (ta - ra) / n
		m.DriftB = (tb - rb) / n
	}
	// Banding: the row/column means of the high-pass luminance. Scene
	// verticals survive it, but both pipelines see the same scene, so the
	// difference is the pipeline's own fixed-pattern/banding contribution.
	hp := make([]float32, len(target.y))
	bl := boxPlane(target.y, w, h, 2)
	for i := range hp {
		hp[i] = target.y[i] - bl[i]
	}
	rows := make([]float64, h)
	cols := make([]float64, w)
	for y := 0; y < h; y++ {
		var s float64
		for x := 0; x < w; x++ {
			s += float64(hp[y*w+x])
		}
		rows[y] = s / float64(w)
	}
	for x := 0; x < w; x++ {
		var s float64
		for y := 0; y < h; y++ {
			s += float64(hp[y*w+x])
		}
		cols[x] = s / float64(h)
	}
	m.RowBand = stdFloat(rows)
	m.ColBand = stdFloat(cols)
	// Impulse dots: a pixel far from the median of its eight neighbours
	// while those neighbours are themselves flat. A real single-pixel star
	// or specular point also counts; the point is to compare that number
	// with SPP's and not to reward erasing either.
	var dots, npix float64
	ring := make([]float32, 8)
	for y := 1; y+1 < h; y++ {
		for x := 1; x+1 < w; x++ {
			i := y*w + x
			if !sel[i] {
				continue
			}
			npix++
			j := 0
			for yy := y - 1; yy <= y+1; yy++ {
				for xx := x - 1; xx <= x+1; xx++ {
					if xx == x && yy == y {
						continue
					}
					ring[j] = target.y[yy*w+xx]
					j++
				}
			}
			lo, hi := ring[0], ring[0]
			for _, v := range ring[1:] {
				lo, hi = min(lo, v), max(hi, v)
			}
			med := median8(ring)
			if hi-lo < 12 && math.Abs(float64(target.y[i]-med)) > 80 {
				dots++
			}
		}
	}
	if npix > 0 {
		m.Impulse = dots / (npix / 1e6)
	}
	return m
}

// median8 is the median of eight values (the mean of the middle two).
func median8(v []float32) float32 {
	var a [8]float32
	copy(a[:], v)
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
	return (a[3] + a[4]) / 2
}

// bandPlane is box(r1) - box(r2): the spatial band between the two radii.
func bandPlane(p []float32, w, h, r1, r2 int) []float32 {
	a := boxPlane(p, w, h, r1)
	b := boxPlane(p, w, h, r2)
	if r1 == 0 {
		a = append([]float32(nil), p...)
	}
	out := make([]float32, len(p))
	for i := range out {
		out[i] = a[i] - b[i]
	}
	return out
}

// boxPlane is a separable box blur (edges average fewer pixels).
func boxPlane(p []float32, w, h, r int) []float32 {
	if r <= 0 {
		return p
	}
	tmp := make([]float32, len(p))
	out := make([]float32, len(p))
	render.ParallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			row, o := p[y*w:(y+1)*w], tmp[y*w:(y+1)*w]
			var s float32
			var n int
			for x := -r; x < w; x++ {
				if x+r < w {
					s += row[x+r]
					n++
				}
				if x-r-1 >= 0 {
					s -= row[x-r-1]
					n--
				}
				if x >= 0 {
					o[x] = s / float32(n)
				}
			}
		}
	})
	const band = 64
	nb := (w + band - 1) / band
	render.ParallelRows(nb, func(b0, b1 int) {
		for bi := b0; bi < b1; bi++ {
			x0 := bi * band
			bw := min(band, w-x0)
			sum := make([]float32, bw)
			cnt := 0
			for y := -r; y < h; y++ {
				if y+r < h {
					add := tmp[(y+r)*w+x0 : (y+r)*w+x0+bw]
					for i, v := range add {
						sum[i] += v
					}
					cnt++
				}
				if y-r-1 >= 0 {
					sub := tmp[(y-r-1)*w+x0 : (y-r-1)*w+x0+bw]
					for i, v := range sub {
						sum[i] -= v
					}
					cnt--
				}
				if y >= 0 {
					o := out[y*w+x0 : y*w+x0+bw]
					inv := 1 / float32(cnt)
					for i, v := range sum {
						o[i] = v * inv
					}
				}
			}
		}
	})
	return out
}

func stdFloat(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var m float64
	for _, x := range v {
		m += x
	}
	m /= float64(len(v))
	var s float64
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(v)))
}

// sqrtMean combines bands into one RMS (power adds across bands).
func sqrtMean(v [4]float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x * x
	}
	return math.Sqrt(s / float64(len(v)))
}

func sqrtMean3(a, b [4]float64) float64 {
	var s float64
	for i := range a {
		s += a[i]*a[i] + b[i]*b[i]
	}
	return math.Sqrt(s / float64(2*len(a)))
}

// printNRSummary aggregates by body and ISO and prints the two comparison
// tables: noise and chroma first, detail and banding second. Every row
// carries its photo count.
func printNRSummary(recs []nrRecord) {
	if len(recs) == 0 {
		return
	}
	type key struct {
		body string
		iso  float64
	}
	type agg struct {
		n        int
		oy, sy   float64 // luma noise
		oc, sc   float64 // chroma noise
		od, sd   [4]float64
		oBs, sBs [4]float64
		ob, sb   float64 // blotch
		oi, si   float64 // impulse dots / MP
		oda, odb float64 // drift vector
		orb, srb float64 // row banding
		ocb, scb float64 // col banding
		oursMS   float64
	}
	by := map[key]*agg{}
	for _, r := range recs {
		k := key{r.Model, r.ISO}
		a := by[k]
		if a == nil {
			a = &agg{}
			by[k] = a
		}
		a.n++
		a.oy += sqrtMean(r.Ours.NoiseY)
		a.sy += sqrtMean(r.SPP.NoiseY)
		a.oc += sqrtMean3(r.Ours.NoiseA, r.Ours.NoiseB)
		a.sc += sqrtMean3(r.SPP.NoiseA, r.SPP.NoiseB)
		for b := 0; b < 4; b++ {
			a.od[b] += r.Ours.Detail[b]
			a.sd[b] += r.SPP.Detail[b]
			a.oBs[b] += r.Ours.DetailBasis[b]
			a.sBs[b] += r.SPP.DetailBasis[b]
		}
		a.ob += r.Ours.BlotchA + r.Ours.BlotchB
		a.sb += r.SPP.BlotchA + r.SPP.BlotchB
		a.oi += r.Ours.Impulse
		a.si += r.SPP.Impulse
		a.oda += math.Hypot(r.Ours.DriftA, r.Ours.DriftB)
		a.odb += math.Hypot(r.SPP.DriftA, r.SPP.DriftB)
		a.orb += r.Ours.RowBand
		a.srb += r.SPP.RowBand
		a.ocb += r.Ours.ColBand
		a.scb += r.SPP.ColBand
		a.oursMS += r.OursMS
	}
	var ks []key
	for k := range by {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].body != ks[j].body {
			return ks[i].body < ks[j].body
		}
		return ks[i].iso < ks[j].iso
	})
	fmt.Println("\nNoise in flat areas (lower is better), ours / SPP; chroma is CIELAB dE units:")
	fmt.Printf("%-24s %5s %3s  %13s %13s %13s %13s %13s %13s\n", "body", "ISO", "n", "luma", "chroma", "blotch 8-32", "row band", "col band", "dots/MP")
	for _, k := range ks {
		a := by[k]
		n := float64(a.n)
		fmt.Printf("%-24s %5g %3d  %6.2f/%5.2f %6.2f/%5.2f %6.2f/%5.2f %6.2f/%5.2f %6.2f/%5.2f %6.1f/%5.1f\n",
			k.body, k.iso, a.n, a.oy/n, a.sy/n, a.oc/n, a.sc/n, a.ob/n, a.sb/n, a.orb/n, a.srb/n, a.ocb/n, a.scb/n, a.oi/n, a.si/n)
	}
	fmt.Println("\nRetained detail per band (slope on each pipeline's own ISO 100; 1 = all kept) and mean |drift| a*b*:")
	fmt.Printf("%-24s %5s %3s  %13s %13s %13s %13s %9s %9s\n", "body", "ISO", "n", "0-1 px", "1-2 px", "2-4 px", "4-8 px", "drift", "ours s/frame")
	for _, k := range ks {
		a := by[k]
		n := float64(a.n)
		fmt.Printf("%-24s %5g %3d  %6.2f/%5.2f %6.2f/%5.2f %6.2f/%5.2f %6.2f/%5.2f %4.1f/%4.1f %8.1fs\n",
			k.body, k.iso, a.n, a.od[0]/n, a.sd[0]/n, a.od[1]/n, a.sd[1]/n, a.od[2]/n, a.sd[2]/n, a.od[3]/n, a.sd[3]/n,
			a.oda/n, a.odb/n, a.oursMS/n/1000)
	}
	fmt.Println("\nDetail on one fixed basis per ladder (ISO 100 rendered with NR off; 1 = the no-NR band, ours / SPP):")
	fmt.Printf("%-24s %5s %3s  %13s %13s %13s %13s\n", "body", "ISO", "n", "0-1 px", "1-2 px", "2-4 px", "4-8 px")
	for _, k := range ks {
		a := by[k]
		n := float64(a.n)
		fmt.Printf("%-24s %5g %3d  %6.3f/%5.3f %6.3f/%5.3f %6.3f/%5.3f %6.3f/%5.3f\n",
			k.body, k.iso, a.n, a.oBs[0]/n, a.sBs[0]/n, a.oBs[1]/n, a.sBs[1]/n, a.oBs[2]/n, a.sBs[2]/n, a.oBs[3]/n, a.sBs[3]/n)
	}
	// Where SPP wins, averaged over the high-ISO rows.
	var n, wy, wc, wb int
	for _, k := range ks {
		a := by[k]
		n++
		if a.sy < a.oy {
			wy++
		}
		if a.sc < a.oc {
			wc++
		}
		if a.sb < a.ob {
			wb++
		}
	}
	fmt.Printf("\nSPP has less noise on %d of %d body/ISO rows (luma), less chroma noise on %d, less blotch on %d.\n", wy, n, wc, wb)
}

func writeNRCSV(path string, recs []nrRecord) error {
	var b strings.Builder
	b.WriteString("ladder,ref,name,group,model,iso,w,h,oursMs,sppMs,blocks," +
		"o_noiseY0,o_noiseY1,o_noiseY2,o_noiseY3,s_noiseY0,s_noiseY1,s_noiseY2,s_noiseY3," +
		"o_noiseA0,o_noiseA1,o_noiseA2,o_noiseA3,s_noiseA0,s_noiseA1,s_noiseA2,s_noiseA3," +
		"o_noiseB0,o_noiseB1,o_noiseB2,o_noiseB3,s_noiseB0,s_noiseB1,s_noiseB2,s_noiseB3," +
		"o_detail0,o_detail1,o_detail2,o_detail3,s_detail0,s_detail1,s_detail2,s_detail3," +
		"o_basis0,o_basis1,o_basis2,o_basis3,s_basis0,s_basis1,s_basis2,s_basis3,o_impulse,s_impulse," +
		"o_blotchA,o_blotchB,s_blotchA,s_blotchB,o_driftA,o_driftB,s_driftA,s_driftB," +
		"o_rowBand,o_colBand,s_rowBand,s_colBand\n")
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }
	for _, r := range recs {
		vals := []string{r.Ladder, r.Ref, r.Name, r.Group, r.Model,
			strconv.FormatFloat(r.ISO, 'f', 0, 64), strconv.Itoa(r.W), strconv.Itoa(r.H),
			f(r.OursMS), f(r.SppMS), strconv.Itoa(r.Ours.Blocks)}
		for _, m := range []nrMetric{r.Ours, r.SPP} {
			for _, v := range m.NoiseY {
				vals = append(vals, f(v))
			}
		}
		for _, m := range []nrMetric{r.Ours, r.SPP} {
			for _, v := range m.NoiseA {
				vals = append(vals, f(v))
			}
		}
		for _, m := range []nrMetric{r.Ours, r.SPP} {
			for _, v := range m.NoiseB {
				vals = append(vals, f(v))
			}
		}
		for _, m := range []nrMetric{r.Ours, r.SPP} {
			for _, v := range m.Detail {
				vals = append(vals, f(v))
			}
		}
		for _, m := range []nrMetric{r.Ours, r.SPP} {
			for _, v := range m.DetailBasis {
				vals = append(vals, f(v))
			}
		}
		vals = append(vals, f(r.Ours.Impulse), f(r.SPP.Impulse))
		vals = append(vals, f(r.Ours.BlotchA), f(r.Ours.BlotchB), f(r.SPP.BlotchA), f(r.SPP.BlotchB),
			f(r.Ours.DriftA), f(r.Ours.DriftB), f(r.SPP.DriftA), f(r.SPP.DriftB),
			f(r.Ours.RowBand), f(r.Ours.ColBand), f(r.SPP.RowBand), f(r.SPP.ColBand))
		b.WriteString(strings.Join(vals, ",") + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
