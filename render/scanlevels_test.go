package render

// Task 89: the scanner's per-frame levels for a colour negative. A frame at
// the reference key is left on the fixed curve; a dark frame is opened up
// between its own black and white points, and only the scan moves. The
// tests here guard those three promises and the highlight gate.

import (
	"bytes"
	"image"
	"math"
	"sort"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// scanTestFrame is a frame of one luminance and small per-pixel texture, so
// the density percentiles are well defined.
func scanTestFrame(w, h int, v float64) *Linear {
	l := &Linear{W: w, H: h, Pix: make([]float32, w*h*3), Scale: 1, ISO: 100, Sensor: "Merrill"}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := v * (0.9 + 0.2*float64((x*7+y*3)%11)/10)
			i := (y*w + x) * 3
			l.Pix[i], l.Pix[i+1], l.Pix[i+2] = float32(s*1.02), float32(s), float32(s*0.98)
		}
	}
	return l
}

// Task 89: guards that a frame at the reference key, and one inside the lab's quarter-stop
// tolerance, keep the fixed scan table, and that the read does not depend on the view's size.
// A failure means an ordinary frame's scan moved, or the levels read the view instead of the frame.
// Change only when the tolerance or the percentile read changes; update Table 4 in REPORT-89 with it.
func TestScanLevelsLeaveAnOrdinaryFrameAlone(t *testing.T) {
	const film = "Kodak Portra 400"
	for _, v := range []float64{negKeyRef, negKeyRef / math.Pow(2, 0.2)} {
		if lv := FrameScanLevels(scanTestFrame(80, 60, v), film, 0); lv != nil {
			t.Errorf("a frame at %.4f (%.2f stop from the key) got levels: %+v", v, math.Log2(v/negKeyRef), lv)
		}
	}
	dark := FrameScanLevels(scanTestFrame(80, 60, negKeyRef/4), film, 0)
	if dark == nil {
		t.Fatal("a two-stop dark frame got no levels")
	}
	if dark.Gamma >= 1 || dark.Mix <= 0 {
		t.Errorf("dark frame levels %+v: gamma must be under 1 and mix over 0", dark)
	}
	// The percentile read is a frame read: a reduced copy lands on the same
	// levels, as FrameLogKey does for the print.
	l := scanTestFrame(400, 300, negKeyRef/4)
	a := FrameScanLevels(l, film, 0)
	b := FrameScanLevels(l.Downscale(200), film, 0)
	if a == nil || b == nil {
		t.Fatalf("levels at two sizes: %v %v", a, b)
	}
	if math.Abs(a.Gamma-b.Gamma) > 0.02 || math.Abs(a.Range-b.Range) > 0.03 {
		t.Errorf("levels read the view, not the frame: full %+v reduced %+v", a, b)
	}
}

// Task 89: guards that the levels lift a dark negative's scan and touch no other path.
// A failure means the levels leaked into a print, a slide, a B&W film or the no-film render,
// or the GPU key stopped carrying them: that path would reuse another frame's table.
// Change only when one of those paths changes; re-check the byte comparisons in REPORT-89 section6.
func TestScanLevelsBrightenADarkNegativeOnly(t *testing.T) {
	b := negAutoFrame()
	lv := FrameScanLevels(b.Linear, "Kodak Portra 400", 0)
	if lv == nil {
		t.Fatal("the dark test frame got no levels")
	}
	cc := [9]float64{1.5, -0.4, -0.1, -0.2, 1.4, -0.2, 0.0, -0.5, 1.5}
	comp := Compensation{Family: "SD1", Sensor: "Merrill", CC: &cc, SatFactor: 1}
	render := func(film, paper string, levels *ScanLevels) []uint8 {
		p := preset.Neutral()
		p.Film, p.Paper, p.NoGrain = film, paper, true
		c := comp
		c.ScanLevels = levels
		return RenderOpt(b, p, c, false).Pix
	}
	off, on := render("Kodak Portra 400", PaperScan, nil), render("Kodak Portra 400", PaperScan, lv)
	if mOff, mOn := meanG(off), meanG(on); mOn <= mOff+3 {
		t.Errorf("a dark negative's scan did not brighten (green mean %.1f -> %.1f)", mOff, mOn)
	}
	// A print's own table does not take the scanner's levels.
	if !bytes.Equal(render("Kodak Portra 400", "", nil), render("Kodak Portra 400", "", lv)) {
		t.Error("the levels changed a printed negative")
	}
	for _, film := range []string{"Fujifilm Fujichrome Velvia 100", "Kodak Tri-X Pan 400 (TX)", ""} {
		if !bytes.Equal(render(film, "", nil), render(film, "", lv)) {
			t.Errorf("%q changed with the scanner's levels", film)
		}
	}
	// A neutral dark frame stays neutral: the levels act around the film's
	// mid-grey per channel, so they cannot make a cast.
	{
		nb := NewBase(scanTestFrame(120, 90, negKeyRef/8))
		nlv := FrameScanLevels(nb.Linear, "Kodak Portra 400", 0)
		p := preset.Neutral()
		p.Film, p.Paper, p.NoGrain = "Kodak Portra 400", PaperScan, true
		c := comp
		c.ScanLevels = nlv
		img := Render(nb, p, c)
		var sr, sg, sb float64
		for i := 0; i < len(img.Pix); i += 4 {
			sr += float64(img.Pix[i])
			sg += float64(img.Pix[i+1])
			sb += float64(img.Pix[i+2])
		}
		n := float64(len(img.Pix) / 4)
		if math.Abs(sr-sg)/n > 2 || math.Abs(sb-sg)/n > 2 {
			t.Errorf("a neutral dark frame got a cast: R %.1f G %.1f B %.1f", sr/n, sg/n, sb/n)
		}
	}
	// The GPU recipe key changes with the scan's levels, and not for a print.
	p := preset.Neutral()
	p.Film = "Kodak Portra 400"
	p.Paper = PaperScan
	c1 := comp
	c1.ScanLevels = lv
	if k0, k1 := NewGPURecipe(b, p, comp).ColorFilm.Key, NewGPURecipe(b, p, c1).ColorFilm.Key; k0 == k1 {
		t.Errorf("the GPU scan key ignores the levels: %s", k1)
	}
	p.Paper = ""
	if k0, k1 := NewGPURecipe(b, p, comp).ColorFilm.Key, NewGPURecipe(b, p, c1).ColorFilm.Key; k0 != k1 {
		t.Errorf("the GPU print key moved with the scan's levels: %s vs %s", k0, k1)
	}
}

// scanClipShare is the share of pixels with any channel at or above level,
// the highlight gate's measure (percent). The gate is written at 255; 250 is
// the stricter reading used to show the measure is not vacuous when the
// scan's own shoulder keeps everything a level or two under 255.
func scanClipShare(img *image.RGBA, level uint8) float64 {
	n := 0
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] >= level || img.Pix[i+1] >= level || img.Pix[i+2] >= level {
			n++
		}
	}
	return 100 * float64(n) / float64(len(img.Pix)/4)
}

// Task 89: guards the highlight gate against a deliberately clipped input.
// A failure means the gate cannot see clipping, so its evidence in REPORT-89 section6 is void.
// Change only when the gate or the scan's shoulder changes; keep the stricter at or above 250 reading.
func TestScanLevelsHighlightGateCatchesClippedHighlights(t *testing.T) {
	b := negAutoFrame()
	cc := [9]float64{1.5, -0.4, -0.1, -0.2, 1.4, -0.2, 0.0, -0.5, 1.5}
	comp := Compensation{Family: "SD1", Sensor: "Merrill", CC: &cc, SatFactor: 1}
	p := preset.Neutral()
	p.Film, p.NoGrain = "Kodak Portra 400", true
	renderScan := func(l *Linear, levels *ScanLevels) *image.RGBA {
		c := comp
		c.ScanLevels = levels
		p.Paper = PaperScan
		return RenderOpt(NewBase(l), p, c, false)
	}
	p.Paper = ""
	c := comp
	c.NegEV = NegAutoEV(FrameLogKey(b.Linear))
	printImg := RenderOpt(b, p, c, false)
	printClip, printTop := scanClipShare(printImg, 255), scanClipShare(printImg, 250)

	// The real frame: its levels pass the gate at 255 and at the stricter
	// 250 reading.
	lv := FrameScanLevels(b.Linear, "Kodak Portra 400", 0)
	if lv == nil {
		t.Fatal("the dark test frame got no levels")
	}
	goodImg := renderScan(b.Linear, lv)
	good, goodTop := scanClipShare(goodImg, 255), scanClipShare(goodImg, 250)
	if good > printClip+0.5 || goodTop > printTop+0.5 {
		t.Fatalf("the levels' own scan clips %.2f%% (>=250: %.2f%%) against the print's %.2f%% (>=250: %.2f%%)",
			good, goodTop, printClip, printTop)
	}

	// Feed the level code a frame whose highlights are deliberately clipped
	// to white: 2% of the source set to 1.0 in every channel. The levels
	// read it and pin the white point, so the rendered scan is measured
	// both with the levels and on the fixed curve.
	const w, h = 160, 120
	clipped := scanTestFrame(w, h, negKeyRef/4)
	var lums []float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			lums = append(lums, 0.2126*float64(clipped.Pix[i])+0.7152*float64(clipped.Pix[i+1])+0.0722*float64(clipped.Pix[i+2]))
		}
	}
	sort.Float64s(lums)
	cut := lums[len(lums)*98/100]
	for i := 0; i < len(clipped.Pix); i += 3 {
		if 0.2126*float64(clipped.Pix[i])+0.7152*float64(clipped.Pix[i+1])+0.0722*float64(clipped.Pix[i+2]) >= cut {
			clipped.Pix[i], clipped.Pix[i+1], clipped.Pix[i+2] = 1, 1, 1
		}
	}
	lvClipped := FrameScanLevels(clipped, "Kodak Portra 400", 0)
	clippedWith := renderScan(clipped, lvClipped)
	clippedFixed := renderScan(clipped, nil)
	t.Logf("print clip %.2f%% (>=250 %.2f%%); levels' scan %.2f%% (>=250 %.2f%%); clipped frame with levels %.2f%% (>=250 %.2f%%); clipped frame fixed curve %.2f%% (>=250 %.2f%%)",
		printClip, printTop, good, goodTop,
		scanClipShare(clippedWith, 255), scanClipShare(clippedWith, 250),
		scanClipShare(clippedFixed, 255), scanClipShare(clippedFixed, 250))

	// The gate must catch a deliberately clipped picture: the same render
	// with its brightest 2% forced to 255. A gate that cannot see this is
	// not evidence.
	forced := image.NewRGBA(goodImg.Rect)
	copy(forced.Pix, goodImg.Pix)
	var gl []float64
	for y := 0; y < forced.Rect.Dy(); y++ {
		for x := 0; x < forced.Rect.Dx(); x++ {
			i := forced.PixOffset(x, y)
			gl = append(gl, 0.2126*float64(forced.Pix[i])+0.7152*float64(forced.Pix[i+1])+0.0722*float64(forced.Pix[i+2]))
		}
	}
	sort.Float64s(gl)
	gcut := gl[len(gl)*98/100]
	for y := 0; y < forced.Rect.Dy(); y++ {
		for x := 0; x < forced.Rect.Dx(); x++ {
			i := forced.PixOffset(x, y)
			if 0.2126*float64(forced.Pix[i])+0.7152*float64(forced.Pix[i+1])+0.0722*float64(forced.Pix[i+2]) >= gcut {
				forced.Pix[i], forced.Pix[i+1], forced.Pix[i+2] = 255, 255, 255
			}
		}
	}
	forcedClip := scanClipShare(forced, 255)
	t.Logf("forced 2%% white render: %.2f%% at 255 against the print's %.2f%%", forcedClip, printClip)
	if forcedClip <= printClip+0.5 {
		t.Fatalf("the gate did not catch a deliberately clipped render: %.2f%% against %.2f%%", forcedClip, printClip+0.5)
	}
}
