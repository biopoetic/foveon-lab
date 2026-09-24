package preset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `<?xml version="1.0"?>
<Custom>
  <Adj>
    <Color>
      <Setting>
        <Name>[SLIDE][SD1] Velvia50 Sun Pop</Name>
        <Blackness>0,05</Blackness>
        <Contrast>0,30</Contrast>
        <Exposure>0,00</Exposure>
        <Highlight>-0,38</Highlight>
        <Saturation>0,50</Saturation>
        <Sharpness>0</Sharpness>
        <FillLight>0,06</FillLight>
        <ColorAdjustR>0,92</ColorAdjustR>
        <ColorAdjustG>1,02</ColorAdjustG>
        <ColorAdjustB>1,05</ColorAdjustB>
        <WhiteBalance>1</WhiteBalance>
        <ColorTemp>0</ColorTemp>
        <ColorMode>7</ColorMode>
        <ToneCurveEditorRangeSlider2nd>64</ToneCurveEditorRangeSlider2nd>
      </Setting>
      <Setting><Name>[BW] SD15 Neutral Mono</Name><Saturation>-1</Saturation></Setting>
    </Color>
    <BW />
  </Adj>
</Custom>`

func TestParseCommaDecimals(t *testing.T) {
	ps, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("got %d presets", len(ps))
	}
	p := ps[0].Params
	if p.Contrast != 0.30 || p.Highlight != -0.38 || p.R != 0.92 || p.ColorMode != 7 {
		t.Errorf("params = %+v", p)
	}
	// Missing colour-adjust fields default to 1, not 0 (0 would be black).
	if q := ps[1].Params; q.R != 1 || q.G != 1 || q.B != 1 || q.Saturation != -1 {
		t.Errorf("defaults = %+v", q)
	}
}

func TestTags(t *testing.T) {
	cases := []struct{ name, file, cam, group string }{
		{"[SLIDE][SD1] Velvia50", "x.xml", "SD1", "SLIDE"},
		{"[FOVEON] SD15 3D Pop", "x.xml", "SD15", "FOVEON"},
		{"Plain name", "SD15_BASE.xml", "SD15", "OTHER"},
		{"Plain name", "SD1_BASE.xml", "SD1", "OTHER"},
		{"Plain", "SOTA.xml", "", "OTHER"},
	}
	for _, c := range cases {
		if got := cameraOf(c.name, c.file); got != c.cam {
			t.Errorf("cameraOf(%q,%q) = %q, want %q", c.name, c.file, got, c.cam)
		}
		if got := groupOf(c.name); got != c.group {
			t.Errorf("groupOf(%q) = %q, want %q", c.name, got, c.group)
		}
	}
}

func TestAppendRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mine.xml")
	p := Neutral()
	p.Exposure, p.Contrast, p.R = 0.25, -0.1, 0.97
	if err := Append(path, "Moj <test>", p); err != nil {
		t.Fatal(err)
	}
	p.Exposure = 0.5
	if err := Append(path, "Moj <test>", p); err != nil { // same name → replace
		t.Fatal(err)
	}
	if err := Append(path, "Drugi", Neutral()); err != nil {
		t.Fatal(err)
	}
	ps, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].Name != "Moj <test>" || ps[0].Params.Exposure != 0.5 || ps[0].Params.R != 0.97 {
		t.Fatalf("round trip = %+v", ps)
	}
	data, _ := os.ReadFile(path)
	if want := "<Exposure>0,5</Exposure>"; !strings.Contains(string(data), want) {
		t.Errorf("file lacks %s:\n%s", want, data)
	}
}

// TestRealPack loads the user's actual preset folder when present.
func TestRealPack(t *testing.T) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, "Desktop", "foveon pack")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no preset folder")
	}
	ps, errs := LoadDir(dir)
	for _, e := range errs {
		t.Error(e)
	}
	cams := map[string]int{}
	groups := map[string]int{}
	dupes := 0
	for _, p := range ps {
		cams[p.Camera]++
		groups[p.Group]++
		dupes += p.Dupes
	}
	t.Logf("%d unique presets, %d duplicates folded; cameras=%v groups=%v", len(ps), dupes, cams, groups)
	if len(ps) < 100 {
		t.Errorf("expected well over 100 presets, got %d", len(ps))
	}
}
