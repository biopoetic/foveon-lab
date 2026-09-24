package calib

import (
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/tiff"

	"foveon-lab/preset"
	"foveon-lab/render"
)

func TestStepsAreSingleChange(t *testing.T) {
	steps := Steps()
	seen := map[string]bool{}
	ref := neutral()
	for _, s := range steps {
		if seen[s.Code] {
			t.Fatalf("duplicate code %s", s.Code)
		}
		seen[s.Code] = true
		if !reCode.MatchString("SDIM0031_" + s.Code) {
			t.Errorf("code %s not matched by file-name pattern", s.Code)
		}
		// Exactly one field may differ from the neutral base.
		diff := 0
		a, b := s.Params, ref
		for _, d := range []bool{a.Exposure != b.Exposure, a.Contrast != b.Contrast, a.Blackness != b.Blackness,
			a.Highlight != b.Highlight, a.Saturation != b.Saturation, a.Sharpness != b.Sharpness,
			a.FillLight != b.FillLight, a.R != b.R, a.G != b.G, a.B != b.B, a.ColorMode != b.ColorMode,
			a.WhiteBalance != b.WhiteBalance, a.ColorTemp != b.ColorTemp} {
			if d {
				diff++
			}
		}
		if want := map[bool]int{true: 0, false: 1}[s.Param == ""]; diff != want {
			t.Errorf("%s %s changes %d fields, want %d", s.Code, s.Label, diff, want)
		}
	}
	if steps[0].Code != Reference {
		t.Errorf("first step %s, want %s", steps[0].Code, Reference)
	}
	ps, err := preset.Parse([]byte(PackXML(steps)))
	if err != nil || len(ps) != len(steps) {
		t.Fatalf("pack XML parses to %d presets (%v), want %d", len(ps), err, len(steps))
	}
	for i := range ps {
		if ps[i].Params != steps[i].Params {
			t.Errorf("%s round-trips as %+v, want %+v", steps[i].Code, ps[i].Params, steps[i].Params)
		}
	}
}

// scene is a synthetic photo with gradients, colour and a dark region next
// to a bright one, so tone curves, saturation and locality are measurable.
func scene() *image.RGBA {
	w, h := 480, 320
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float64(x) / float64(w-1)
			if y < h/3 && x < w/2 { // dark region
				v *= 0.3
			}
			hue := float64(y) / float64(h) * 2 * math.Pi
			s := 0.35
			r := v * (1 + s*math.Cos(hue))
			g := v * (1 + s*math.Cos(hue-2.094))
			b := v * (1 + s*math.Cos(hue+2.094))
			img.Set(x, y, color.RGBA{c8(r), c8(g), c8(b), 255})
		}
	}
	return img
}

func c8(v float64) uint8 { return uint8(math.Max(0, math.Min(255, v*220+10))) }

func save(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := tiff.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
}

// TestRoundTrip fakes "SPP exports" with our own renderer. The analyser
// must then see our emulation as (near) perfect, and read each control's
// direction correctly off the measured curves.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := render.FromImage(scene(), 0, 0)
	base := render.NewBase(src)
	steps := ByCode(Steps())
	codes := []string{"A00", "A03", "A06", "A08", "A13", "A15", "A17", "A19", "A22"}
	for _, c := range codes {
		out := render.Render(base, steps[c].Params, render.Compensation{SatFactor: 1})
		save(t, filepath.Join(dir, "SDIM0031_"+c+".tif"), out)
	}
	// A file without a code and an orphan (no A00 for it) are reported, not fatal.
	save(t, filepath.Join(dir, "notes.tif"), scene())
	save(t, filepath.Join(dir, "OTHER_A03.tif"), scene())

	res, err := Analyze(dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Measurement{}
	for _, m := range res.Measurements {
		got[m.Code] = m
		if m.EmuError > 1.0 {
			t.Errorf("%s: emulation error %.2f on self-generated export, want ~0", m.Code, m.EmuError)
		}
	}
	if len(got) != len(codes)-1 {
		t.Fatalf("measured %d, want %d", len(got), len(codes)-1)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "OTHER") {
		t.Errorf("orphan export not warned about: %v", res.Warnings)
	}

	if m := got["A22"]; m.Change > 0.5 {
		t.Errorf("neutral re-export changed by %.2f", m.Change)
	}
	// Contrast +1 darkens shadows / brightens highlights; −1 the opposite.
	up, down := got["A03"], got["A06"]
	if !(curveAt(up.Curve, 0.2) < 0.2 && curveAt(up.Curve, 0.8) > 0.8) {
		t.Errorf("contrast +0.5 curve not an S: %.3f %.3f", curveAt(up.Curve, 0.2), curveAt(up.Curve, 0.8))
	}
	if !(curveAt(down.Curve, 0.2) > 0.2 && curveAt(down.Curve, 0.8) < 0.8) {
		t.Errorf("contrast -1 curve not flattened: %.3f %.3f", curveAt(down.Curve, 0.2), curveAt(down.Curve, 0.8))
	}
	if c := curveAt(got["A08"].Curve, 0.9); c >= 0.9 {
		t.Errorf("highlight -1 did not pull highlights: %.3f", c)
	}
	if s := curveAt(got["A13"].Chroma, 0.5); s < 1.2 {
		t.Errorf("saturation +1 chroma gain %.2f", s)
	}
	if s := curveAt(got["A15"].Chroma, 0.5); s > 0.05 {
		t.Errorf("saturation -1 chroma gain %.2f, want ~0 (mono)", s)
	}
	// Fill Light is local: dark surroundings lift more than bright ones at
	// the same pixel brightness.
	fl := got["A17"].Local
	lifted := false
	for p := 0; p < 8; p++ {
		for l := 0; l < 7; l++ {
			for l2 := l + 2; l2 < 8; l2++ {
				if fl[l][p] > -9 && fl[l2][p] > -9 && fl[l][p] > fl[l2][p]+0.01 {
					lifted = true
				}
			}
		}
	}
	if !lifted {
		t.Errorf("fill light did not show as local lift: %v", fl)
	}
	rep := res.Report()
	for _, want := range []string{"A03", "FillLight", "Upozorenja"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	t.Log("\n" + rep)
}

func TestMatrixFitRecoversChannelGain(t *testing.T) {
	src := render.FromImage(scene(), 0, 0)
	out := &render.Linear{W: src.W, H: src.H, Pix: make([]float32, len(src.Pix))}
	for i := 0; i < len(src.Pix); i += 3 {
		out.Pix[i], out.Pix[i+1], out.Pix[i+2] = src.Pix[i]*0.8, src.Pix[i+1], src.Pix[i+2]*0.9
	}
	m, err := measure(src, out, localMean(src))
	if err != nil {
		t.Fatal(err)
	}
	want := [9]float64{0.8, 0, 0, 0, 1, 0, 0, 0, 0.9}
	for i := range want {
		if math.Abs(m.Matrix[i]-want[i]) > 0.01 {
			t.Fatalf("matrix = %v, want %v", m.Matrix, want)
		}
	}
	if m.MatrixErr > 0.5 {
		t.Errorf("matrix fit error %.2f", m.MatrixErr)
	}
}
