package spp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// A trimmed copy of what SPP 6 writes (CRLF, full field set).
const settings = "<?xml version=\"1.0\"?>\r\n<Custom>\r\n  <Adj>\r\n    <Color>\r\n" +
	"      <Setting>\r\n        <Name>[FOVEON][SD1] 3D Pop Master</Name>\r\n        <Blackness>0,08</Blackness>\r\n" +
	"        <Contrast>0,25</Contrast>\r\n        <Exposure>0,05</Exposure>\r\n        <Highlight>-0,55</Highlight>\r\n" +
	"        <Saturation>0,18</Saturation>\r\n        <Sharpness>0</Sharpness>\r\n        <FillLight>0,15</FillLight>\r\n" +
	"        <ColorAdjustR>0,95</ColorAdjustR>\r\n        <ColorAdjustG>1,02</ColorAdjustG>\r\n        <ColorAdjustB>0,92</ColorAdjustB>\r\n" +
	"        <WhiteBalance>2</WhiteBalance>\r\n        <ColorTemp>0</ColorTemp>\r\n        <ColorMode>4</ColorMode>\r\n" +
	"        <HiglhightCorrection>1</HiglhightCorrection>\r\n        <ToneCurveEditorRangeSlider2nd>64</ToneCurveEditorRangeSlider2nd>\r\n" +
	"      </Setting>\r\n" +
	"      <Setting>\r\n        <Name>Other &amp; Co</Name>\r\n        <Exposure>0,3</Exposure>\r\n      </Setting>\r\n" +
	"    </Color>\r\n    <BW>\r\n      <Setting>\r\n        <Name>Mono</Name>\r\n        <Exposure>0</Exposure>\r\n      </Setting>\r\n    </BW>\r\n  </Adj>\r\n</Custom>\r\n"

func tuned() preset.Params {
	p := preset.Neutral()
	p.Exposure, p.Contrast, p.Highlight, p.FillLight, p.R, p.B, p.WhiteBalance = 0.1, 0.2, -0.8, 0.25, 0.98, 1.08, 2
	return p
}

func TestUpsertAppendsClone(t *testing.T) {
	out, err := UpsertPreset(settings, "[FL] Pop <tuned>", tuned())
	if err != nil {
		t.Fatal(err)
	}
	// Existing presets untouched, byte for byte.
	head := settings[:strings.Index(settings, "    </Color>")]
	if !strings.HasPrefix(out, head) {
		t.Fatal("existing presets were modified")
	}
	if !strings.HasSuffix(out, settings[strings.Index(settings, "    </Color>"):]) {
		t.Fatal("tail (</Color>, BW section) was modified")
	}
	// The clone carries SPP's extra fields and CRLF line endings.
	added := out[len(head):strings.Index(out, "    </Color>")]
	for _, want := range []string{"<Name>[FL] Pop &lt;tuned&gt;</Name>", "<HiglhightCorrection>1</HiglhightCorrection>",
		"<Exposure>0,1</Exposure>", "<Highlight>-0,8</Highlight>", "<ColorAdjustB>1,08</ColorAdjustB>", "\r\n"} {
		if !strings.Contains(added, want) {
			t.Errorf("new preset lacks %q:\n%s", want, added)
		}
	}
	if strings.Contains(strings.ReplaceAll(added, "\r\n", ""), "\n") {
		t.Error("mixed line endings")
	}
	ps, _ := preset.Parse([]byte(out))
	if len(ps) != 4 {
		t.Errorf("got %d presets, want 4", len(ps))
	}
}

func TestUpsertReplacesSameName(t *testing.T) {
	out, err := UpsertPreset(settings, "Other & Co", tuned())
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := preset.Parse([]byte(out))
	if len(ps) != 3 {
		t.Fatalf("got %d presets, want 3 (replaced, not added)", len(ps))
	}
	if ps[1].Name != "Other & Co" || ps[1].Params.Highlight != -0.8 || ps[1].Params.FillLight != 0.25 {
		t.Errorf("replaced preset = %+v", ps[1])
	}
	if ps[0].Params.Exposure != 0.05 {
		t.Error("first preset changed")
	}
	// Idempotent: applying again changes nothing.
	again, _ := UpsertPreset(out, "Other & Co", tuned())
	if again != out {
		t.Error("second upsert not idempotent")
	}
}

func TestUpsertEmptyList(t *testing.T) {
	empty := "<?xml version=\"1.0\"?>\n<Custom>\n  <Adj>\n    <Color>\n    </Color>\n    <BW />\n  </Adj>\n</Custom>\n"
	out, err := UpsertPreset(empty, "Solo", tuned())
	if err != nil {
		t.Fatal(err)
	}
	if ps, _ := preset.Parse([]byte(out)); len(ps) != 1 || ps[0].Name != "Solo" {
		t.Fatalf("parsed %+v from\n%s", ps, out)
	}
	if _, err := UpsertPreset("<Custom/>", "x", tuned()); err == nil {
		t.Error("file without <Color> accepted")
	}
}

const prefs = "<?xml version=\"1.0\"?>\r\n<Pref>\r\n  <X3F_FilterMode>3</X3F_FilterMode>\r\n  <X3F_Brightness>0,2</X3F_Brightness>\r\n  <X3F_Contrast>0</X3F_Contrast>\r\n" +
	"  <X3F_ContrastBW>-0,2</X3F_ContrastBW>\r\n  <X3F_ShadowBW>1,7</X3F_ShadowBW>\r\n  <X3F_HilightBW>-1,2</X3F_HilightBW>\r\n" +
	"  <X3F_SharpnessBW>0,1</X3F_SharpnessBW>\r\n  <X3F_FillLightBW>0,2</X3F_FillLightBW>\r\n  <X3F_NameBW_Num>3</X3F_NameBW_Num>\r\n" +
	"  <X3F_NameBW>[FOVEON][SD1] 3D Pop Master</X3F_NameBW>\r\n" +
	"  <X3F_Shadow>0</X3F_Shadow>\r\n  <X3F_Hilight>-0,8</X3F_Hilight>\r\n  <X3F_Saturation>0,2</X3F_Saturation>\r\n" +
	"  <X3F_Sharpness>0</X3F_Sharpness>\r\n  <X3F_FillLight>0,6</X3F_FillLight>\r\n  <X3F_ColorR>1,039978</X3F_ColorR>\r\n" +
	"  <X3F_ColorG>1</X3F_ColorG>\r\n  <X3F_ColorB>0,8799744</X3F_ColorB>\r\n  <X3F_WhiteBalancePreset>1</X3F_WhiteBalancePreset>\r\n" +
	"  <X3F_WhiteBalanceTemp>5000</X3F_WhiteBalanceTemp>\r\n  <X3F_Name>old</X3F_Name>\r\n  <X3F_BrightnessBW>-0,9</X3F_BrightnessBW>\r\n" +
	"  <X3F_ColorMode>4</X3F_ColorMode>\r\n</Pref>\r\n"

func TestSetCurrent(t *testing.T) {
	out, err := SetCurrent(prefs, "[FL] Pop", tuned())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<X3F_Brightness>0,1</X3F_Brightness>", "<X3F_Hilight>-0,8</X3F_Hilight>",
		"<X3F_FillLight>0,25</X3F_FillLight>", "<X3F_ColorB>1,08</X3F_ColorB>", "<X3F_Name>[FL] Pop</X3F_Name>",
		"<X3F_WhiteBalancePreset>2</X3F_WhiteBalancePreset>",
		"<X3F_WhiteBalanceTemp>5000</X3F_WhiteBalanceTemp>", // ColorTemp 0 keeps SPP's value
		"<X3F_BrightnessBW>-0,9</X3F_BrightnessBW>"} { // B&W settings untouched
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	if !strings.Contains(out, "<X3F_FilterMode>1</X3F_FilterMode>") {
		t.Error("colour preset did not switch SPP to Color mode")
	}
	if _, err := SetCurrent("<Pref></Pref>", "x", tuned()); err == nil {
		t.Error("unknown prefs format accepted")
	}
}

func TestSetCurrentMono(t *testing.T) {
	p := tuned()
	p.Mono, p.Saturation, p.Contrast, p.Blackness = true, -1, 0.75, 0.28
	out, err := SetCurrent(prefs, "[FL] Mono Depth", p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<X3F_FilterMode>3</X3F_FilterMode>", "<X3F_BrightnessBW>0,1</X3F_BrightnessBW>",
		"<X3F_ContrastBW>0,75</X3F_ContrastBW>", "<X3F_ShadowBW>0,28</X3F_ShadowBW>", "<X3F_HilightBW>-0,8</X3F_HilightBW>",
		"<X3F_FillLightBW>0,25</X3F_FillLightBW>", "<X3F_NameBW>Current Unsaved Setting</X3F_NameBW>",
		"<X3F_NameBW_Num>1</X3F_NameBW_Num>",
		"<X3F_Brightness>0,2</X3F_Brightness>"} { // colour settings left alone
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	// A Mono preset still lands in SPP's (colour) preset list, marked by
	// the pack convention Saturation -1, and reads back as Mono.
	p.Saturation = 0 // toggled to Mono in Foveon Lab without touching saturation
	s, err := UpsertPreset(settings, "[FL] Mono Depth", p)
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := preset.Parse([]byte(s))
	if last := ps[len(ps)-2]; last.Name != "[FL] Mono Depth" || !last.Params.Mono || last.Params.Saturation != -1 {
		t.Errorf("mono preset in list = %+v", last)
	}
}

// TestRealFiles transforms copies of the user's actual SPP files in memory
// (never writes them) to prove the format matches.
func TestRealFiles(t *testing.T) {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "SIGMA", "SIGMA_PhotoPro6")
	s, err1 := os.ReadFile(filepath.Join(dir, settingsFile))
	p, err2 := os.ReadFile(filepath.Join(dir, prefsFile))
	if err1 != nil || err2 != nil {
		t.Skip("no SPP settings on this machine")
	}
	before, _ := preset.Parse(s)
	out, err := UpsertPreset(string(s), "[FL] test", tuned())
	if err != nil {
		t.Fatal(err)
	}
	after, _ := preset.Parse([]byte(out))
	if len(after) != len(before)+1 {
		t.Errorf("presets %d → %d", len(before), len(after))
	}
	if _, err := SetCurrent(string(p), "[FL] test", tuned()); err != nil {
		t.Fatal(err)
	}
}
