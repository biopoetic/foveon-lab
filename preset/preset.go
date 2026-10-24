// Package preset reads and writes Sigma Photo Pro (SPP) custom-setting XML
// files (<Custom><Adj><Color><Setting>…).
package preset

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Params are the SPP adjustment values the preview emulates.
type Params struct {
	Exposure   float64 `json:"exposure"`
	Contrast   float64 `json:"contrast"`
	Blackness  float64 `json:"blackness"` // SPP "Shadow"
	Highlight  float64 `json:"highlight"`
	Saturation float64 `json:"saturation"`
	Sharpness  float64 `json:"sharpness"`
	FillLight  float64 `json:"fillLight"`
	R          float64 `json:"r"`
	G          float64 `json:"g"`
	B          float64 `json:"b"`
	// Stored and written back, but not emulated (meaning not documented).
	WhiteBalance int `json:"whiteBalance"`
	ColorTemp    int `json:"colorTemp"`
	ColorMode    int `json:"colorMode"`
	// Mono marks a black & white preset. SPP's XML has no such flag: preset
	// packs mark B&W with Saturation -1, which SPP actually renders as muted
	// colour; -2 is fully grey in SPP (checked by eye). So Mono is READ from
	// Saturation <= -1 and WRITTEN as -2. SPP's separate Monochrome mode
	// can't be used — SPP always opens photos in Color mode, whatever its
	// saved X3F_FilterMode says (tested).
	Mono bool `json:"mono"`
	// Layer filter (Foveon Lab only; not in SPP's XML): a gain per sensor
	// layer in stops (0 = unchanged), applied to the camera's own layers
	// before its colour matrix, like a filter on the lens. In Monochrome
	// the grey is the layers' weighted sum (bottom heavy = a red filter;
	// top only = the cleanest layer).
	// Filter is a lens filter simulated on the sensor's modelled layer
	// sensitivities (render/layerfilters.json): "yellow", "deepyellow",
	// "orange", "red", "deepred", "yellowgreen", "green", "blue", "warm",
	// "cool"; "" = none.
	Filter      string  `json:"filter,omitempty"`
	LayerBottom float64 `json:"layerBottom"`
	LayerMiddle float64 `json:"layerMiddle"`
	LayerTop    float64 `json:"layerTop"`
	// Film (Foveon Lab only): Grain is a film's RMS granularity (the
	// published diffuse RMS density x 1000 through a 48 µm aperture; 0 =
	// none), FilmWidth the film frame's long side in mm (0 = 36, 35 mm
	// film), Halation 0-1 the light the film base reflects back into the
	// red-sensitive layer (0 = an anti-halation layer, 1 = none).
	Grain     float64 `json:"grain,omitempty"`
	FilmWidth float64 `json:"filmWidth,omitempty"`
	Halation  float64 `json:"halation,omitempty"`
	// Film names the library film: its grain, and for a black-and-white
	// film its spectral sensitivity and characteristic curve.
	Film string `json:"film,omitempty"`
	// Development of a film with a characteristic curve: Developer from
	// its data sheet ("" = the first), DevTime in minutes (0 = the
	// developer's standard time), Push in stops over the developer's EI.
	Developer string  `json:"developer,omitempty"`
	DevTime   float64 `json:"devTime,omitempty"`
	Push      float64 `json:"push,omitempty"`
	// Paper: a printing paper from the library ("" = a scan), at Grade
	// ("00", "0" … "5"; "" = 2).
	Paper string `json:"paper,omitempty"`
	Grade string `json:"grade,omitempty"`
	// CamMode (Foveon Lab only; not in SPP's XML): one of the camera's own
	// colour modes ("Vivid", "Neutral", "Portrait", "Landscape", "FCBlue";
	// "" or "Standard" = none), applied as Sigma's calibration defines it
	// (render/camf.go). SPP's preset ColorMode codes for these modes are not
	// known reliably, so an SPP copy of such a preset is Standard.
	CamMode string `json:"camMode,omitempty"`
	// The colour darkroom (a colour negative on colour paper): the
	// enlarger's colour head on top of the neutral filtration, Y/M/C in CC
	// units (density × 100), and the print exposure in stops.
	CCY      float64 `json:"ccY,omitempty"`
	CCM      float64 `json:"ccM,omitempty"`
	CCC      float64 `json:"ccC,omitempty"`
	PrintExp float64 `json:"printExp,omitempty"`
	// Density filters (Foveon Lab only; not in SPP's XML). ND names a
	// neutral-density filter from the library (render/brandfilters.json,
	// class "nd"): its stops are the product's density. A neutral filter
	// does not change colour — it costs exposure — so NDMetered chooses
	// which to show: false = the photographer opened up by the filter
	// factor (no pixel change), true = the same settings with the filter
	// on (darker by exactly its stops).
	ND        string `json:"nd,omitempty"`
	NDMetered bool   `json:"ndMetered,omitempty"`
	// GND names a graduated neutral-density filter (class "gnd"): the same
	// density, but across a gradient in the frame. GNDAngle rotates the
	// gradient (0° = dark at the top, the sky), GNDPos moves the transition
	// across the frame (-0.5 = the bottom edge, 0 = centred, +0.5 = the top
	// edge), GNDSoft is the transition's softness (0 = hard edge, 1 = the
	// whole frame).
	GND      string  `json:"gnd,omitempty"`
	GNDAngle float64 `json:"gndAngle,omitempty"`
	GNDPos   float64 `json:"gndPos,omitempty"`
	GNDSoft  float64 `json:"gndSoft,omitempty"`
}

const (
	// MonoSaturationRead: packs mark B&W presets with this saturation.
	MonoSaturationRead = -1
	// MonoSaturationWrite: fully desaturated in SPP's colour mode.
	MonoSaturationWrite = -2
)

// Neutral returns the do-nothing parameter set.
func Neutral() Params { return Params{R: 1, G: 1, B: 1, ColorMode: ModeStandard} }

// SPP colour-mode codes as used in the preset files. The mapping was inferred
// from the pack's own documentation (Gold200 = Portrait = 6, Velvia =
// Landscape = 7, Provia/Ultramax = Standard = 4); other codes are unknown.
const (
	ModeStandard  = 4
	ModePortrait  = 6
	ModeLandscape = 7
)

// ModeName names a colour-mode code.
func ModeName(m int) string {
	switch m {
	case ModeStandard:
		return "Standard"
	case ModePortrait:
		return "Portrait"
	case ModeLandscape:
		return "Landscape"
	}
	return fmt.Sprintf("Unknown (%d)", m)
}

// Preset is one named SPP setting.
type Preset struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Group   string `json:"group"`  // first [TAG] of the name, e.g. SLIDE
	Camera  string `json:"camera"` // SD1, SD15 or "" (any)
	Source  string `json:"source"` // file path relative to the presets root
	Section string `json:"section"`
	Params  Params `json:"params"`
	Dupes   int    `json:"dupes"` // identical copies found in other files
	// Film: the library film the preset's name imitates with SPP settings
	// (render.FilmFor), "" if none.
	Film string `json:"film,omitempty"`
}

type xmlCustom struct {
	Adj struct {
		Color struct {
			Settings []xmlSetting `xml:"Setting"`
		} `xml:"Color"`
		BW struct {
			Settings []xmlSetting `xml:"Setting"`
		} `xml:"BW"`
	} `xml:"Adj"`
}

type xmlSetting struct {
	Fields []xmlField `xml:",any"`
}

type xmlField struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

// num parses SPP numbers, which use the Windows locale decimal separator
// (a comma on many European systems).
func num(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// ParseFile reads one SPP XML file.
func ParseFile(path string) ([]Preset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes SPP XML bytes into presets (without ID/Source set).
func Parse(data []byte) ([]Preset, error) {
	if !utf8.Valid(data) {
		// Older tools wrote ANSI (Windows-1252); treat as Latin-1.
		r := make([]rune, len(data))
		for i, c := range data {
			r[i] = rune(c)
		}
		data = []byte(string(r))
	}
	var c xmlCustom
	if err := xml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	var out []Preset
	add := func(section string, ss []xmlSetting) {
		for _, s := range ss {
			p := Preset{Section: section, Params: Neutral()}
			for _, f := range s.Fields {
				v, ok := num(f.Value)
				switch f.XMLName.Local {
				case "Name":
					p.Name = strings.TrimSpace(f.Value)
					continue
				}
				if !ok {
					continue
				}
				pp := &p.Params
				switch f.XMLName.Local {
				case "Exposure":
					pp.Exposure = v
				case "Contrast":
					pp.Contrast = v
				case "Blackness":
					pp.Blackness = v
				case "Highlight":
					pp.Highlight = v
				case "Saturation":
					pp.Saturation = v
				case "Sharpness":
					pp.Sharpness = v
				case "FillLight":
					pp.FillLight = v
				case "ColorAdjustR":
					pp.R = v
				case "ColorAdjustG":
					pp.G = v
				case "ColorAdjustB":
					pp.B = v
				case "WhiteBalance":
					pp.WhiteBalance = int(v)
				case "ColorTemp":
					pp.ColorTemp = int(v)
				case "ColorMode":
					pp.ColorMode = int(v)
				}
			}
			p.Params.Mono = section == "BW" || p.Params.Saturation <= MonoSaturationRead
			if p.Name != "" {
				out = append(out, p)
			}
		}
	}
	add("Color", c.Adj.Color.Settings)
	add("BW", c.Adj.BW.Settings)
	return out, nil
}

var (
	reSD15  = regexp.MustCompile(`(?i)(^|[^a-z0-9])SD15([^0-9]|$)`)
	reSD1   = regexp.MustCompile(`(?i)(^|[^a-z0-9])SD1([^0-9]|$)`)
	reGroup = regexp.MustCompile(`\[([^\]]+)\]`)
)

// cameraOf tags a preset SD1 / SD15 from its name, else from its file name.
func cameraOf(name, file string) string {
	for _, s := range []string{name, filepath.Base(file)} {
		if reSD15.MatchString(s) {
			return "SD15"
		}
		if reSD1.MatchString(s) {
			return "SD1"
		}
	}
	return ""
}

func groupOf(name string) string {
	for _, m := range reGroup.FindAllStringSubmatch(name, -1) {
		g := strings.ToUpper(strings.TrimSpace(m[1]))
		if g != "SD1" && g != "SD15" {
			return g
		}
	}
	return "OTHER"
}

// Source is one place presets come from.
type Source struct {
	Label string // shown before the file name, e.g. "Built-in" or "SPP"
	FS    fs.FS
	// Match picks the files to read (default: every *.xml).
	Match func(path string) bool
	// Skip drops presets by name (e.g. ones Foveon Lab itself wrote).
	Skip func(name string) bool
}

// LoadDir loads every *.xml under root.
func LoadDir(root string) ([]Preset, []error) {
	return Load(Source{FS: os.DirFS(root)})
}

// Load reads the sources in order. A preset identical to one already loaded
// (same name and values) is kept once, with Dupes counting copies; one with
// the same values from an EARLIER source under another name is dropped too,
// so a renamed copy of the same pack doesn't show twice.
func Load(sources ...Source) ([]Preset, []error) {
	var out []Preset
	var errs []error
	seen := map[string]int{}       // name|params → index
	seenParams := map[string]int{} // params → source number
	for si, src := range sources {
		if src.FS == nil {
			continue
		}
		var files []string
		fs.WalkDir(src.FS, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != "." && strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				return nil
			}
			ok := strings.EqualFold(path.Ext(p), ".xml")
			if src.Match != nil {
				ok = src.Match(p)
			}
			if ok {
				files = append(files, p)
			}
			return nil
		})
		sort.Strings(files)
		for _, f := range files {
			data, err := fs.ReadFile(src.FS, f)
			var ps []Preset
			if err == nil {
				ps, err = Parse(data)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", path.Join(src.Label, f), err))
				continue
			}
			rel := path.Join(src.Label, f)
			for _, p := range ps {
				if src.Skip != nil && src.Skip(p.Name) {
					continue
				}
				// The camera is part of the identity: the same values in the
				// SD1 and the SD15 pack are two presets, not a renamed copy
				// (the [FL] Max Detail entry is deliberately identical in
				// both).
				p.Camera = cameraOf(p.Name, f)
				pkey := fmt.Sprintf("%+v|%s", p.Params, p.Camera)
				key := p.Name + "|" + pkey
				if i, ok := seen[key]; ok {
					out[i].Dupes++
					continue
				}
				if s0, ok := seenParams[pkey]; ok && s0 < si {
					continue
				}
				p.Source = rel
				p.Group = groupOf(p.Name)
				h := sha1.Sum([]byte(p.Source + "\x00" + key))
				p.ID = hex.EncodeToString(h[:6])
				seen[key] = len(out)
				if _, ok := seenParams[pkey]; !ok {
					seenParams[pkey] = si
				}
				out = append(out, p)
			}
		}
	}
	return out, errs
}

// XMLSaturation is the saturation to give SPP: a Mono preset is written as
// MonoSaturationWrite, so it is truly black & white in SPP and still reads
// back as Mono.
func XMLSaturation(p Params) float64 {
	if p.Mono && p.Saturation > MonoSaturationWrite {
		return MonoSaturationWrite
	}
	return p.Saturation
}

// FormatNumber writes a value the way SPP does on a comma-decimal locale.
func FormatNumber(v float64) string { return fmtNum(v) }

func fmtNum(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" || s == "" {
		s = "0"
	}
	return strings.ReplaceAll(s, ".", ",")
}

// SettingXML renders one <Setting> block with the core SPP fields.
func SettingXML(name string, p Params) string {
	var b strings.Builder
	var esc strings.Builder
	xml.EscapeText(&esc, []byte(name))
	fmt.Fprintf(&b, "      <Setting>\n        <Name>%s</Name>\n", esc.String())
	row := func(tag, v string) { fmt.Fprintf(&b, "        <%s>%s</%s>\n", tag, v, tag) }
	row("Blackness", fmtNum(p.Blackness))
	row("Contrast", fmtNum(p.Contrast))
	row("Exposure", fmtNum(p.Exposure))
	row("Highlight", fmtNum(p.Highlight))
	row("Saturation", fmtNum(XMLSaturation(p)))
	row("Sharpness", fmtNum(p.Sharpness))
	row("FillLight", fmtNum(p.FillLight))
	row("ColorAdjustR", fmtNum(p.R))
	row("ColorAdjustG", fmtNum(p.G))
	row("ColorAdjustB", fmtNum(p.B))
	row("WhiteBalance", strconv.Itoa(p.WhiteBalance))
	row("ColorTemp", strconv.Itoa(p.ColorTemp))
	row("ColorMode", strconv.Itoa(p.ColorMode))
	b.WriteString("      </Setting>\n")
	return b.String()
}

// FileXML renders a complete, importable SPP XML file.
func FileXML(settings ...string) string {
	return "<?xml version=\"1.0\"?>\n<Custom>\n  <Adj>\n    <Color>\n" +
		strings.Join(settings, "") + "    </Color>\n    <BW />\n  </Adj>\n</Custom>\n"
}

// Append adds a setting to an SPP XML file, creating the file if needed.
// A setting with the same name is replaced rather than duplicated.
func Append(path, name string, p Params) error {
	block := SettingXML(name, p)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return os.WriteFile(path, []byte(FileXML(block)), 0o644)
	}
	if err != nil {
		return err
	}
	existing, err := Parse(data)
	if err != nil {
		return fmt.Errorf("%s is not valid SPP XML: %w", filepath.Base(path), err)
	}
	var blocks []string
	for _, e := range existing {
		if e.Section == "Color" && e.Name != name {
			blocks = append(blocks, SettingXML(e.Name, e.Params))
		}
	}
	blocks = append(blocks, block)
	return os.WriteFile(path, []byte(FileXML(blocks...)), 0o644)
}
