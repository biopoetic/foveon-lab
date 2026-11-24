package main

// Task 89 scratch measurement. Manual only:
//
//	T89_KEYS=1  TestT89Keys — the frame key of every sample photo
//	T89_LIMIT=n run only the first n photos (development)
//
// Not part of the normal test run.

import (
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const t89Out = "zz-out/t89"

type t89Key struct {
	Name string  `json:"name"`
	Dir  string  `json:"dir"`
	W    int     `json:"w"`
	H    int     `json:"h"`
	Key  float64 `json:"key"`
}

func t89Samples() string {
	if d := os.Getenv("T89_SAMPLES"); d != "" {
		return d
	}
	return `C:\Users\Korisnik\Desktop\foveon-samples`
}

func t89Paths(t *testing.T) []string {
	var out []string
	err := filepath.Walk(t89Samples(), func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() || !strings.EqualFold(filepath.Ext(p), ".x3f") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	if n := os.Getenv("T89_LIMIT"); n != "" {
		var k int
		fmt.Sscanf(n, "%d", &k)
		if k > 0 && k < len(out) {
			out = out[:k]
		}
	}
	return out
}

func t89LoadPhoto(t *testing.T, path string) *Photo {
	t.Helper()
	ph := &Photo{ID: idOf(path), Name: filepath.Base(path), Folder: filepath.Base(filepath.Dir(path)), path: path, Info: map[string]string{}}
	ph.Kind = "x3f"
	if !loadMeta(ph) {
		t.Fatalf("loadMeta %s", path)
	}
	ph.Accurate = ph.RawDecoded || rawCorrectionFor(ph) != nil
	return ph
}

// t89Source is the source the app renders from once a photo is decoded: the
// SD9/SD10 still path for the bodies with only a small preview, the raw
// development for every other body.
func t89Source(ph *Photo) (*render.Linear, error) {
	if ph.RawDecoded {
		return rawStillLinear(ph, sourceW)
	}
	return rawLinear(ph, sourceW, "sRGB")
}

func TestT89Keys(t *testing.T) {
	if os.Getenv("T89_KEYS") == "" {
		t.Skip("set T89_KEYS=1 to run the task 89 key measurement")
	}
	isolateUserDirs(t)
	if err := os.MkdirAll(t89Out, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := t89Paths(t)
	var out []t89Key
	for i, p := range paths {
		ph := t89LoadPhoto(t, p)
		l, err := t89Source(ph)
		if err != nil {
			fmt.Printf("%3d %-40s ERROR %v\n", i, ph.Name, err)
			continue
		}
		k := t89Key{Name: ph.Name, Dir: filepath.Base(filepath.Dir(p)), W: l.W, H: l.H, Key: render.FrameLogKey(l)}
		out = append(out, k)
		fmt.Printf("%3d %-40s %-14s %4dx%-4d key %.5f\n", i, k.Name, k.Dir, k.W, k.H, k.Key)
	}
	b, _ := json.MarshalIndent(out, "", " ")
	if err := os.WriteFile(filepath.Join(t89Out, "keys.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	fmt.Println("DARKEST 10")
	for _, k := range out[:min(10, len(out))] {
		fmt.Printf("  %-40s %-14s %.5f\n", k.Name, k.Dir, k.Key)
	}
}

// t89Luma is a 8-bit sRGB pixel's luminance in linear light.
func t89Luma(r, g, b uint8) float64 {
	lin := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// t89ToneOf is the tone of an 8-bit render: the median and the central box's
// median linear luminance, the share of pixels with any channel at 255, and
// the share with every channel at or below 8 (a black shadow).
type t89Tone struct {
	Median  float64 `json:"median"`
	Box     float64 `json:"box"`
	Clip    float64 `json:"clipPct"`
	Black   float64 `json:"blackPct"`
	Mean256 float64 `json:"mean8"`
}

func t89ToneOf(img *image.RGBA) t89Tone {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	var lums []float64
	var clip, black, sum int
	x0, x1, y0, y1 := w*3/10, w*7/10, h*3/10, h*7/10
	var box []float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			r, g, b := img.Pix[i], img.Pix[i+1], img.Pix[i+2]
			l := t89Luma(r, g, b)
			lums = append(lums, l)
			if x >= x0 && x < x1 && y >= y0 && y < y1 {
				box = append(box, l)
			}
			if r == 255 || g == 255 || b == 255 {
				clip++
			}
			if r <= 8 && g <= 8 && b <= 8 {
				black++
			}
			sum += int(r) + int(g) + int(b)
		}
	}
	pct := func(v []float64, p float64) float64 {
		sort.Float64s(v)
		x := p / 100 * float64(len(v)-1)
		i := int(x)
		if i >= len(v)-1 {
			return v[len(v)-1]
		}
		return v[i] + (x-float64(i))*(v[i+1]-v[i])
	}
	n := float64(w * h)
	return t89Tone{
		Median:  pct(lums, 50),
		Box:     pct(box, 50),
		Clip:    100 * float64(clip) / n,
		Black:   100 * float64(black) / n,
		Mean256: float64(sum) / (3 * n),
	}
}

func t89Stops(a, b float64) float64 {
	if a <= 0 || b <= 0 {
		return math.NaN()
	}
	return math.Log2(b / a)
}

type t89Frame struct {
	Name  string           `json:"name"`
	Group string           `json:"group"`
	Key   float64          `json:"key"`
	EV    float64          `json:"ev"`
	Ct    string           `json:"crop"`
	Stats render.ScanStats `json:"stats"`
	Gamma float64          `json:"gamma"`
	Mix   float64          `json:"mix"`
	Diff  float64          `json:"diff"`
	Today t89Tone          `json:"scanToday"`
	New   t89Tone          `json:"scanNew"`
	Print t89Tone          `json:"print"`
}

func TestT89Tone(t *testing.T) {
	if os.Getenv("T89_TONE") == "" {
		t.Skip("set T89_TONE=1 to run the task 89 tone measurement")
	}
	isolateUserDirs(t)
	var keys []t89Key
	b, err := os.ReadFile(filepath.Join(t89Out, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	byKey := append([]t89Key{}, keys...)
	sort.Slice(byKey, func(i, j int) bool { return byKey[i].Key < byKey[j].Key })
	dark := append([]t89Key{}, byKey[:10]...)
	near := append([]t89Key{}, keys...)
	dist := func(k float64) float64 { return math.Abs(math.Log(k / 0.070)) } // negKeyRef
	sort.Slice(near, func(i, j int) bool {
		di, dj := dist(near[i].Key), dist(near[j].Key)
		if di != dj {
			return di < dj
		}
		return near[i].Name < near[j].Name
	})
	near = near[:10]
	seen := map[string]bool{}
	var picks []struct {
		k t89Key
		g string
	}
	for _, k := range dark {
		if !seen[k.Name+k.Dir] {
			seen[k.Name+k.Dir] = true
			picks = append(picks, struct {
				k t89Key
				g string
			}{k, "dark"})
		}
	}
	for _, k := range near {
		if !seen[k.Name+k.Dir] {
			seen[k.Name+k.Dir] = true
			picks = append(picks, struct {
				k t89Key
				g string
			}{k, "near"})
		}
	}
	paths := t89PathMap(t)
	var out []t89Frame
	for _, pk := range picks {
		path := pk.k.path(paths)
		ph := t89LoadPhoto(t, path)
		l, err := t89Source(ph)
		if err != nil {
			t.Fatalf("%s: %v", ph.Name, err)
		}
		st, ok := render.ScanStatsFor(l, "Kodak Portra 400", 0)
		if !ok {
			t.Fatalf("%s: no scan stats", ph.Name)
		}
		base := render.NewBase(l.Downscale(1200))
		comp := rawCompensation(ph)
		p := preset.Neutral()
		p.Film, p.NoGrain = "Kodak Portra 400", true
		render1 := func(paper string, lv *render.ScanLevels, ev float64) *image.RGBA {
			p.Paper = paper
			c := comp
			c.NegEV, c.ScanLevels = ev, lv
			return render.RenderOpt(base, p, c, false)
		}
		today := render1(render.PaperScan, nil, 0)
		lv := render.FrameScanLevels(l, "Kodak Portra 400", 0)
		newScan := render1(render.PaperScan, lv, 0)
		print := render1("", nil, render.NegAutoEV(st.Key))
		var diff float64
		if len(today.Pix) == len(newScan.Pix) {
			for i := range today.Pix {
				d := int(today.Pix[i]) - int(newScan.Pix[i])
				if d < 0 {
					d = -d
				}
				diff += float64(d)
			}
			diff /= float64(len(today.Pix))
		}
		lvG, lvMix := 1.0, 0.0
		if lv != nil {
			lvG, lvMix = lv.Gamma, lv.Mix
		}
		f := t89Frame{Name: ph.Name, Group: pk.g, Key: st.Key, EV: render.NegAutoEV(st.Key), Stats: st,
			Gamma: lvG, Mix: lvMix, Diff: diff,
			Today: t89ToneOf(today), New: t89ToneOf(newScan), Print: t89ToneOf(print)}
		out = append(out, f)
		fmt.Printf("%-5s %-34s key %.5f EV %+5.2f  lv g %5.3f mix %4.2f  p1 %5.3f p99 %5.3f  med %8.5f %8.5f %8.5f  box %8.5f %8.5f %8.5f  clip %5.2f %5.2f %5.2f  blk %5.1f %5.1f %5.1f\n",
			f.Group, f.Name, f.Key, f.EV, lvG, lvMix, st.Pct[1], st.Pct[11],
			f.Today.Median, f.New.Median, f.Print.Median,
			f.Today.Box, f.New.Box, f.Print.Box,
			f.Today.Clip, f.New.Clip, f.Print.Clip,
			f.Today.Black, f.New.Black, f.Print.Black)
	}
	bb, _ := json.MarshalIndent(out, "", " ")
	if err := os.WriteFile(filepath.Join(t89Out, "tone.json"), bb, 0o644); err != nil {
		t.Fatal(err)
	}
}

type t89StatRow struct {
	Name  string           `json:"name"`
	Dir   string           `json:"dir"`
	Key   float64          `json:"key"`
	Stats render.ScanStats `json:"stats"`
}

// TestT89Stats reads every sample frame's negative density percentiles for
// Portra 400 on the scan's forward path. It is the measurement the scanner's
// ordinary range is taken from; manual only (T89_STATS=1).
func TestT89Stats(t *testing.T) {
	if os.Getenv("T89_STATS") == "" {
		t.Skip("set T89_STATS=1 to run the task 89 density measurement")
	}
	isolateUserDirs(t)
	var keys []t89Key
	b, err := os.ReadFile(filepath.Join(t89Out, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	paths := t89PathMap(t)
	// The ten darkest frames and the ten nearest the reference key: the
	// frames the gates and the pictures use. Reading all 183 densities adds
	// nothing to the ordinary range that these twenty do not show.
	byKey := append([]t89Key{}, keys...)
	sort.Slice(byKey, func(i, j int) bool { return byKey[i].Key < byKey[j].Key })
	near := append([]t89Key{}, keys...)
	dist := func(k float64) float64 { return math.Abs(math.Log(k / 0.070)) } // negKeyRef
	sort.Slice(near, func(i, j int) bool {
		di, dj := dist(near[i].Key), dist(near[j].Key)
		if di != dj {
			return di < dj
		}
		return near[i].Name < near[j].Name
	})
	picks := append(append([]t89Key{}, byKey[:10]...), near[:10]...)
	seen := map[string]bool{}
	var rows []t89StatRow
	for i, k := range picks {
		id := strings.ToLower(k.Dir) + "/" + strings.ToLower(k.Name)
		if seen[id] {
			continue
		}
		seen[id] = true
		ph := t89LoadPhoto(t, k.path(paths))
		l, err := t89Source(ph)
		if err != nil {
			fmt.Printf("%3d %-40s ERROR %v\n", i, ph.Name, err)
			continue
		}
		st, ok := render.ScanStatsFor(l, "Kodak Portra 400", 0)
		if !ok {
			continue
		}
		rows = append(rows, t89StatRow{Name: k.Name, Dir: k.Dir, Key: st.Key, Stats: st})
		fmt.Printf("%3d %-40s key %.5f  p0.5 %6.3f p1 %6.3f p50 %6.3f p99 %6.3f p99.5 %6.3f\n",
			i, k.Name, st.Key, st.Pct[0], st.Pct[1], st.Pct[6], st.Pct[11], st.Pct[12])
	}
	bb, _ := json.MarshalIndent(rows, "", " ")
	if err := os.WriteFile(filepath.Join(t89Out, "stats.json"), bb, 0o644); err != nil {
		t.Fatal(err)
	}
	// Summary: the median black/white point over the twenty picked frames,
	// and over the ten nearest the reference key.
	med := func(v []float64) float64 {
		if len(v) == 0 {
			return 0
		}
		sort.Float64s(v)
		return v[len(v)/2]
	}
	var lo, hi, rng []float64
	for _, r := range rows {
		lo = append(lo, r.Stats.Pct[0])
		hi = append(hi, r.Stats.Pct[12])
		rng = append(rng, r.Stats.Pct[12]-r.Stats.Pct[0])
	}
	fmt.Printf("PICKS %d: p0.5 median %.4f  p99.5 median %.4f  range median %.4f\n",
		len(rows), med(append([]float64{}, lo...)), med(append([]float64{}, hi...)), med(append([]float64{}, rng...)))
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	fmt.Println("DARKEST 10 (key, black, white, range)")
	for _, r := range rows[:10] {
		fmt.Printf("  %-38s %.5f  %6.3f %6.3f %6.3f\n", r.Name, r.Key, r.Stats.Pct[0], r.Stats.Pct[12], r.Stats.Pct[12]-r.Stats.Pct[0])
	}
	type nk struct {
		k   float64
		idx int
	}
	var nearRows []nk
	for i, r := range rows {
		nearRows = append(nearRows, nk{math.Abs(render.NegAutoEV(r.Key)), i})
	}
	sort.Slice(nearRows, func(i, j int) bool { return nearRows[i].k < nearRows[j].k })
	fmt.Println("NEAREST negKeyRef 10 (key, black, white, range)")
	var nlo, nhi, nrng []float64
	for _, n := range nearRows[:10] {
		r := rows[n.idx]
		fmt.Printf("  %-38s %.5f  %6.3f %6.3f %6.3f\n", r.Name, r.Key, r.Stats.Pct[0], r.Stats.Pct[12], r.Stats.Pct[12]-r.Stats.Pct[0])
		nlo = append(nlo, r.Stats.Pct[0])
		nhi = append(nhi, r.Stats.Pct[12])
		nrng = append(nrng, r.Stats.Pct[12]-r.Stats.Pct[0])
	}
	fmt.Printf("NEAR: p0.5 median %.4f  p99.5 median %.4f  range median %.4f\n",
		med(nlo), med(nhi), med(nrng))
}

// t89PathMap finds each keyed photo by its folder and file name; the samples
// tree has one nested folder (rytterfalk/collection), so a plain join does
// not always work.
func t89PathMap(t *testing.T) map[string]string {
	m := map[string]string{}
	for _, p := range t89Paths(t) {
		k := strings.ToLower(filepath.Base(filepath.Dir(p))) + "/" + strings.ToLower(filepath.Base(p))
		m[k] = p
	}
	return m
}

func (k t89Key) path(m map[string]string) string {
	return m[strings.ToLower(k.Dir)+"/"+strings.ToLower(k.Name)]
}

// t89Panel lays three renders side by side under one-line labels and saves
// them as a PNG.
func t89Panel(path string, labels []string, imgs []*image.RGBA, w int) error {
	h := imgs[0].Rect.Dy() * w / imgs[0].Rect.Dx()
	const bar = 18
	W, H := w*len(imgs), h+bar
	out := image.NewRGBA(image.Rect(0, 0, W, H))
	for i := range labels {
		draw.Draw(out, image.Rect(i*w, bar, (i+1)*w, bar+h), imgs[i], image.Point{}, draw.Src)
		d := &font.Drawer{Dst: out, Src: image.White, Face: basicfont.Face7x13,
			Dot: fixed.P(i*w+6, 13)}
		d.DrawString(labels[i])
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}

func TestT89Pictures(t *testing.T) {
	if os.Getenv("T89_PICS") == "" {
		t.Skip("set T89_PICS=1 to write the task 89 three-panel pictures")
	}
	isolateUserDirs(t)
	var keys []t89Key
	b, err := os.ReadFile(filepath.Join(t89Out, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	paths := t89PathMap(t)
	want := []string{
		"DP1MOUTBAP0.X3F", "sigma-photo_IMG00046_a.X3F", "kron_bougainvillea_IMG00805.X3F",
		"kron_DP2CardX3Fs_SDIM0687.X3F", "sigma_dp2x_07.x3f", "sigma_sd_quattro_h_02.x3f",
	}
	if err := os.MkdirAll(t89Out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range want {
		var k t89Key
		found := false
		for _, x := range keys {
			if strings.EqualFold(x.Name, name) {
				k, found = x, true
				break
			}
		}
		if !found {
			t.Fatalf("%s not in keys.json", name)
		}
		ph := t89LoadPhoto(t, k.path(paths))
		l, err := t89Source(ph)
		if err != nil {
			t.Fatalf("%s: %v", ph.Name, err)
		}
		base := render.NewBase(l.Downscale(1200))
		comp := rawCompensation(ph)
		p := preset.Neutral()
		p.Film, p.NoGrain = "Kodak Portra 400", true
		render1 := func(paper string, lv *render.ScanLevels, ev float64) *image.RGBA {
			p.Paper = paper
			c := comp
			c.NegEV, c.ScanLevels = ev, lv
			return render.RenderOpt(base, p, c, false)
		}
		today := render1(render.PaperScan, nil, 0)
		newScan := render1(render.PaperScan, render.FrameScanLevels(l, "Kodak Portra 400", 0), 0)
		print := render1("", nil, render.NegAutoEV(render.FrameLogKey(l)))
		out := filepath.Join(t89Out, "pic_"+strings.TrimSuffix(name, filepath.Ext(name))+".png")
		if err := t89Panel(out, []string{"scan today", "scan + levels", "print"}, []*image.RGBA{today, newScan, print}, 560); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%s (key %.5f) -> %s\n", ph.Name, render.FrameLogKey(l), out)
	}
}
