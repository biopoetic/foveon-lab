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
}

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

// LoadDir loads every *.xml under root. Presets that are identical (same
// name and values) across files are kept once, with Dupes counting copies.
func LoadDir(root string) ([]Preset, []error) {
	var files []string
	var errs []error
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".xml") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	var out []Preset
	seen := map[string]int{}
	for _, f := range files {
		ps, err := ParseFile(f)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(f), err))
			continue
		}
		rel, _ := filepath.Rel(root, f)
		for _, p := range ps {
			key := fmt.Sprintf("%s|%+v", p.Name, p.Params)
			if i, ok := seen[key]; ok {
				out[i].Dupes++
				continue
			}
			p.Source = filepath.ToSlash(rel)
			p.Camera = cameraOf(p.Name, f)
			p.Group = groupOf(p.Name)
			h := sha1.Sum([]byte(p.Source + "\x00" + key))
			p.ID = hex.EncodeToString(h[:6])
			seen[key] = len(out)
			out = append(out, p)
		}
	}
	return out, errs
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
	row("Saturation", fmtNum(p.Saturation))
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
