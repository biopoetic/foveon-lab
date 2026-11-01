package main

import (
	"encoding/json"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

// Task 19/20: Max Detail is the preset name, and the mode's sliders are
// adjustments on top of the raw development: they start at zero (the pack's
// SPP values live in SPPFallback) and moving one reaches the renderer.
func TestMaxDetailIsAName(t *testing.T) {
	for _, n := range []string{"[FL] Max Detail", " [FL] Max Detail "} {
		if !isMaxDetail(n) {
			t.Fatalf("%q not recognised", n)
		}
	}
	for _, n := range []string{"Max Detail", "[FL] Max Detail Lite", ""} {
		if isMaxDetail(n) {
			t.Fatalf("%q wrongly recognised", n)
		}
	}
	p := preset.Params{Exposure: .08, Highlight: -1, FillLight: .8, Saturation: .12, R: 1, G: 1, B: 1, Sharpness: .3}
	q := maxDetailRenderParams(p)
	if !q.MaxDetail || q.Exposure != p.Exposure || q.Highlight != p.Highlight || q.FillLight != p.FillLight {
		t.Fatalf("mode must keep the user's adjustments: %+v", q)
	}
	if q.Sharpness != p.Sharpness || q.R != 1 {
		t.Fatalf("post-development controls lost: %+v", q)
	}
}

// TestMaxDetailPresetZeroesSliders: loading the pack's entry moves its SPP
// bundle into SPPFallback and starts the visible sliders at zero.
func TestMaxDetailPresetZeroesSliders(t *testing.T) {
	p := preset.Preset{Name: "[FL] Max Detail", Params: preset.Params{
		Exposure: .08, Contrast: -.1, Blackness: -.08, Highlight: -1,
		FillLight: .8, Saturation: .12, Sharpness: 0, R: 1, G: 1, B: 1, ColorMode: 4,
	}}
	maxDetailPreset(&p)
	if !p.Params.MaxDetail {
		t.Fatal("mode flag not set")
	}
	if p.Params.Exposure != 0 || p.Params.Contrast != 0 || p.Params.Blackness != 0 ||
		p.Params.Highlight != 0 || p.Params.FillLight != 0 || p.Params.Saturation != 0 {
		t.Fatalf("sliders do not start at zero: %+v", p.Params)
	}
	fb := p.Params.SPPFallback
	if fb == nil || fb.Exposure != .08 || fb.Highlight != -1 || fb.FillLight != .8 || fb.MaxDetail || fb.SPPFallback != nil {
		t.Fatalf("SPP fallback lost: %+v", fb)
	}
	// The fallback must survive the JSON round trip the page does.
	b, err := json.Marshal(p.Params)
	if err != nil {
		t.Fatal(err)
	}
	var back preset.Params
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.SPPFallback == nil || back.SPPFallback.FillLight != .8 || !back.MaxDetail {
		t.Fatalf("JSON round trip lost the mode: %+v", back)
	}
}

// TestMaxDetailSPPParams: the hand-off (and a saved preset) writes the pack's
// SPP bundle plus whatever the user moved, never the zero-based UI values.
func TestMaxDetailSPPParams(t *testing.T) {
	fb := preset.Params{Exposure: .08, Contrast: -.1, Blackness: -.08, Highlight: -1, FillLight: .8, Saturation: .12, R: 1, G: 1, B: 1}
	p := preset.Params{MaxDetail: true, SPPFallback: &fb, Exposure: .25, FillLight: -.1, R: .95, G: 1, B: 1.02}
	q := maxDetailSPPParams(p)
	if q.MaxDetail || q.SPPFallback != nil {
		t.Fatal("SPP params still carry Foveon Lab mode fields")
	}
	if !closeTo(q.Exposure, .33) || !closeTo(q.FillLight, .7) || !closeTo(q.Highlight, -1) || !closeTo(q.Blackness, -.08) {
		t.Fatalf("fallback + adjustment wrong: %+v", q)
	}
	if q.R != .95 || q.B != 1.02 {
		t.Fatalf("post-development controls lost: %+v", q)
	}
	// An ordinary preset passes through unchanged.
	o := preset.Params{Exposure: .4, FillLight: .2}
	if r := maxDetailSPPParams(o); r.Exposure != .4 || r.FillLight != .2 {
		t.Fatalf("ordinary preset changed: %+v", r)
	}
}

// An SD1 medium file decodes twice as wide as its frame and a portrait frame
// decodes sideways: every raw path, Max Detail included, must fix both.
func TestUprightRawFrame(t *testing.T) {
	l := &render.Linear{W: 4704, H: 1568, Pix: make([]float32, 4704*1568*3), Raw: true, ISO: 200, Sensor: "x"}
	got := uprightRaw(&Photo{previewW: 3000, previewH: 2000, Rotation: 90}, l)
	if got.W != 3136 || got.H != 4704 {
		t.Errorf("frame = %dx%d, want 3136x4704", got.W, got.H)
	}
	if !got.Raw || got.ISO != 200 || got.Sensor != "x" {
		t.Errorf("the reshaped frame lost what the decode knows: %+v", *got)
	}
	// A 16:9 frame in a 3:2 decode is cropped, and the crop must keep them too.
	l = &render.Linear{W: 300, H: 200, Pix: make([]float32, 300*200*3), Raw: true, ISO: 400, Sensor: "x"}
	got = uprightRaw(&Photo{previewW: 1600, previewH: 900}, l)
	if got.W != 300 || got.H != 168 {
		t.Errorf("16:9 frame = %dx%d, want 300x168", got.W, got.H)
	}
	if !got.Raw || got.ISO != 400 || got.Sensor != "x" {
		t.Errorf("the cropped frame lost what the decode knows: %+v", *got)
	}
}

// Task 29: the rebuilt-pixel gate must reach the same frame as the image,
// through the same crop/stretch and rotation, or Max Detail would shoulder
// the wrong pixels (or silently fall back to the global shoulder).
func TestUprightGateFrame(t *testing.T) {
	// An SD1 medium file: the gate is stretched to the frame and rotated 90.
	const w, h = 4704, 1568
	gate := make([]bool, w*h)
	gate[10*w+10] = true
	ph := &Photo{previewW: 3000, previewH: 2000, Rotation: 90}
	got := uprightRaw(ph, &render.Linear{W: w, H: h, Pix: make([]float32, w*h*3)})
	g := uprightGate(ph, gate, w, h)
	if len(g) != got.W*got.H {
		t.Fatalf("gate %d entries, frame %dx%d", len(g), got.W, got.H)
	}
	// The source pixel is stretched to rows 20 and 21 (nearest) and then
	// rotated 90, so it lights two adjacent pixels. That is the expected
	// nearest-neighbour thickness; the shoulder feathers it anyway.
	if !g[10*got.W+3115] || !g[10*got.W+3114] {
		t.Errorf("the marked pixel did not land at (3115,10)/(3114,10)")
	}
	n := 0
	for _, v := range g {
		if v {
			n++
		}
	}
	if n != 2 {
		t.Errorf("gate has %d marked pixels, want 2", n)
	}
	// A 16:9 frame in a 3:2 decode is centre-cropped: source row 0 is gone
	// and source row 16 becomes destination row 0.
	const cw, ch = 300, 200
	ph2 := &Photo{previewW: 1600, previewH: 900}
	g2a := make([]bool, cw*ch)
	g2a[0] = true // source (0,0), cropped away
	g3a := uprightGate(ph2, g2a, cw, ch)
	if len(g3a) != cw*168 {
		t.Fatalf("16:9 gate %d entries, want %d", len(g3a), cw*168)
	}
	for _, v := range g3a {
		if v {
			t.Errorf("a cropped-away row survived")
			break
		}
	}
	g2b := make([]bool, cw*ch)
	g2b[16*cw] = true // source (0,16), the first kept row
	g3b := uprightGate(ph2, g2b, cw, ch)
	if !g3b[0] {
		t.Errorf("the first kept row did not land at the top")
	}
	// A nil or wrong-sized gate stays nil (the caller then keeps the old
	// global behaviour).
	if uprightGate(ph, nil, w, h) != nil || uprightGate(ph, gate[:10], w, h) != nil {
		t.Errorf("bad gate did not fall back")
	}
}

func closeTo(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
