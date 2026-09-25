// Package spp hands a preset to an installed Sigma Photo Pro 6.
//
// SPP keeps no per-photo edits. It stores its preset list in
// %LOCALAPPDATA%\SIGMA\SIGMA_PhotoPro6\X3F_Setting.xml (the same format as
// importable preset XML) and the current/last-used adjustments as X3F_*
// keys in SPhotoPro.xml. Both are read when SPP starts and rewritten when it
// exits, so they must only be edited while SPP is closed.
package spp

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/biopoetic/foveon-lab/preset"
)

const (
	exeName      = "SIGMA_PhotoPro6.exe"
	settingsFile = "X3F_Setting.xml"
	prefsFile    = "SPhotoPro.xml"
	backupDir    = "foveon-lab-backups"
	keepBackups  = 10
)

// ErrRunning means SPP is open, so its files must not be touched.
var ErrRunning = errors.New("Sigma Photo Pro is running — close it first (it rewrites its settings when it exits)")

// Install locates SPP's program and settings.
type Install struct {
	Exe string // SIGMA_PhotoPro6.exe
	Dir string // %LOCALAPPDATA%\SIGMA\SIGMA_PhotoPro6
}

// Detect finds SPP 6 on this machine.
func Detect() (*Install, error) {
	if runtime.GOOS != "windows" {
		return nil, errors.New("Sigma Photo Pro integration is Windows-only")
	}
	in := &Install{Dir: filepath.Join(os.Getenv("LOCALAPPDATA"), "SIGMA", "SIGMA_PhotoPro6")}
	for _, pf := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		p := filepath.Join(pf, "SIGMA", "SIGMA Photo Pro 6", exeName)
		if _, err := os.Stat(p); err == nil {
			in.Exe = p
			break
		}
	}
	if in.Exe == "" {
		return nil, errors.New("Sigma Photo Pro 6 not found in Program Files")
	}
	for _, f := range []string{settingsFile, prefsFile} {
		if _, err := os.Stat(filepath.Join(in.Dir, f)); err != nil {
			return nil, fmt.Errorf("SPP settings not found (%s) — start SPP once so it creates them", f)
		}
	}
	return in, nil
}

// Running reports whether SPP is currently open.
func (in *Install) Running() bool {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+exeName, "/NH").Output()
	return err == nil && strings.Contains(strings.ToLower(string(out)), strings.ToLower(exeName))
}

// Backup copies both settings files to a timestamped folder and prunes old
// backups. It returns the backup folder.
func (in *Install) Backup() (string, error) {
	root := filepath.Join(in.Dir, backupDir)
	dst := filepath.Join(root, time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	for _, f := range []string{settingsFile, prefsFile} {
		b, err := os.ReadFile(filepath.Join(in.Dir, f))
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dst, f), b, 0o644); err != nil {
			return "", err
		}
	}
	if ents, err := os.ReadDir(root); err == nil && len(ents) > keepBackups {
		var names []string
		for _, e := range ents {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names[:max(0, len(names)-keepBackups)] {
			os.RemoveAll(filepath.Join(root, n))
		}
	}
	return dst, nil
}

// writeAtomic replaces path via a temp file + rename.
func writeAtomic(path, text string) error {
	tmp := path + ".foveon-lab.tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Apply puts the preset into SPP's list (replacing one with the same name)
// and makes it SPP's current adjustment. SPP must be closed.
func (in *Install) Apply(name string, p preset.Params) (backup string, err error) {
	if in.Running() {
		return "", ErrRunning
	}
	sPath, pPath := filepath.Join(in.Dir, settingsFile), filepath.Join(in.Dir, prefsFile)
	sText, err := os.ReadFile(sPath)
	if err != nil {
		return "", err
	}
	pText, err := os.ReadFile(pPath)
	if err != nil {
		return "", err
	}
	newS, err := UpsertPreset(string(sText), name, p)
	if err != nil {
		return "", err
	}
	newP, err := SetCurrent(string(pText), name, p)
	if err != nil {
		return "", err
	}
	if backup, err = in.Backup(); err != nil {
		return "", fmt.Errorf("backup failed, nothing changed: %w", err)
	}
	if err := writeAtomic(sPath, newS); err != nil {
		return backup, err
	}
	if err := writeAtomic(pPath, newP); err != nil {
		return backup, err
	}
	return backup, nil
}

// ErrStillOpen means SPP did not exit after being asked to close — it is
// probably showing a dialog. It is never force-killed.
var ErrStillOpen = errors.New("Sigma Photo Pro did not close — it may be asking something (unsaved work?). Answer it or close SPP yourself, then try again")

// Close asks SPP to exit the normal way (a window close, like clicking X —
// never a forced kill) and waits for it, so SPP can save its own settings
// and prompt about anything unsaved.
func (in *Install) Close(timeout time.Duration) error {
	if !in.Running() {
		return nil
	}
	// SPP has several top-level windows (browser, review); closing one can
	// leave the others, so keep asking — each window at most every 3 s.
	lastAsked := map[uintptr]time.Time{}
	resend := func(h uintptr) bool {
		if t, ok := lastAsked[h]; ok && time.Since(t) < 3*time.Second {
			return false
		}
		lastAsked[h] = time.Now()
		return true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		requestClose(resend)
		time.Sleep(400 * time.Millisecond)
		if !in.Running() {
			// SPP writes its settings files while exiting; give the OS a
			// moment to finish flushing them before we edit them.
			time.Sleep(700 * time.Millisecond)
			return nil
		}
	}
	return ErrStillOpen
}

// Launch starts SPP with the given X3F.
func (in *Install) Launch(x3f string) error {
	return exec.Command(in.Exe, x3f).Start()
}

// ---- pure text transforms (unit-tested) ----

var (
	reSetting = regexp.MustCompile(`(?s)([ \t]*)<Setting>.*?</Setting>`)
	reName    = regexp.MustCompile(`(?s)<Name>(.*?)</Name>`)
)

// coreFields maps SPP preset tags to values.
func coreFields(p preset.Params) [][2]string {
	n := preset.FormatNumber
	return [][2]string{
		{"Blackness", n(p.Blackness)}, {"Contrast", n(p.Contrast)}, {"Exposure", n(p.Exposure)},
		{"Highlight", n(p.Highlight)}, {"Saturation", n(preset.XMLSaturation(p))}, {"Sharpness", n(p.Sharpness)},
		{"FillLight", n(p.FillLight)}, {"ColorAdjustR", n(p.R)}, {"ColorAdjustG", n(p.G)},
		{"ColorAdjustB", n(p.B)}, {"WhiteBalance", strconv.Itoa(p.WhiteBalance)},
		{"ColorTemp", strconv.Itoa(p.ColorTemp)}, {"ColorMode", strconv.Itoa(p.ColorMode)},
	}
}

func escape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func unescape(s string) string {
	var v string
	if err := xml.Unmarshal([]byte("<x>"+s+"</x>"), &v); err != nil {
		return s
	}
	return v
}

func setTag(block, tag, val string) (string, bool) {
	re := regexp.MustCompile(`<` + tag + `>[^<]*</` + tag + `>`)
	if !re.MatchString(block) {
		return block, false
	}
	return re.ReplaceAllLiteralString(block, "<"+tag+">"+val+"</"+tag+">"), true
}

// UpsertPreset returns the X3F_Setting.xml text with the named colour preset
// set to p. An existing preset of that name is replaced in place; otherwise
// a new one is appended, cloned from the first existing preset so it carries
// every field SPP writes. All other presets are left byte-for-byte intact.
func UpsertPreset(text, name string, p preset.Params) (string, error) {
	cs, ce := strings.Index(text, "<Color>"), strings.Index(text, "</Color>")
	if cs < 0 || ce < cs {
		return "", errors.New("X3F_Setting.xml: no <Color> section")
	}
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	section := text[cs:ce]
	locs := reSetting.FindAllStringSubmatchIndex(section, -1)

	build := func(template string) (string, error) {
		b := template
		var ok bool
		if b, ok = setTag(b, "Name", escape(name)); !ok {
			return "", errors.New("template preset has no <Name>")
		}
		for _, f := range coreFields(p) {
			if nb, ok := setTag(b, f[0], f[1]); ok {
				b = nb
			} else {
				// Field missing from the template: add it after <Name>.
				b = strings.Replace(b, "</Name>", "</Name>"+nl+"        <"+f[0]+">"+f[1]+"</"+f[0]+">", 1)
			}
		}
		return b, nil
	}

	for _, l := range locs {
		block := section[l[0]:l[1]]
		if m := reName.FindStringSubmatch(block); m != nil && unescape(m[1]) == name {
			nb, err := build(block)
			if err != nil {
				return "", err
			}
			out := text[:cs+l[0]] + nb + text[cs+l[1]:]
			return out, verify(out, name, p)
		}
	}

	// The match includes the line's indentation, so the clone lines up.
	var template string
	if len(locs) > 0 {
		template = section[locs[0][0]:locs[0][1]]
	} else {
		template = strings.TrimRight(strings.ReplaceAll(preset.SettingXML(name, p), "\n", nl), nl)
	}
	nb, err := build(template)
	if err != nil {
		return "", err
	}
	// Insert before </Color>, keeping the closing tag on its own line.
	insertAt := ce
	for insertAt > 0 && (text[insertAt-1] == ' ' || text[insertAt-1] == '\t') {
		insertAt--
	}
	prefix := ""
	if insertAt > 0 && text[insertAt-1] != '\n' {
		prefix = nl
	}
	out := text[:insertAt] + prefix + nb + nl + text[insertAt:]
	return out, verify(out, name, p)
}

// verify re-parses the result and checks the preset reads back exactly.
func verify(text, name string, p preset.Params) error {
	ps, err := preset.Parse([]byte(text))
	if err != nil {
		return fmt.Errorf("result would not parse: %w", err)
	}
	for _, q := range ps {
		if q.Name == name && q.Section == "Color" {
			if q.Params != roundTrip(p) {
				return fmt.Errorf("preset reads back as %+v, want %+v", q.Params, p)
			}
			return nil
		}
	}
	return errors.New("preset missing after write")
}

// roundTrip applies the same 2-decimal rounding the XML format uses.
func roundTrip(p preset.Params) preset.Params {
	p.Saturation = preset.XMLSaturation(p)
	r := func(v float64) float64 {
		f, _ := strconv.ParseFloat(strings.ReplaceAll(preset.FormatNumber(v), ",", "."), 64)
		return f
	}
	p.Exposure, p.Contrast, p.Blackness, p.Highlight = r(p.Exposure), r(p.Contrast), r(p.Blackness), r(p.Highlight)
	p.Saturation, p.Sharpness, p.FillLight = r(p.Saturation), r(p.Sharpness), r(p.FillLight)
	p.R, p.G, p.B = r(p.R), r(p.G), r(p.B)
	return p
}

// filterColor is SPP's X3F_FilterMode for Color (3 is Monochrome, observed
// by switching modes in SPP 6.9; SPP opens photos in Color regardless).
const filterColor = "1"

// SetCurrent returns SPhotoPro.xml with SPP's current colour adjustment set
// to p (a Mono preset as Saturation -2, which is fully grey in SPP). SPP is
// also put back in Color mode. Only existing keys are changed; the file must
// contain the core ones or the format is considered unknown.
func SetCurrent(text, name string, p preset.Params) (string, error) {
	n := preset.FormatNumber
	set := [][2]string{
		{"X3F_FilterMode", filterColor},
		{"X3F_Brightness", n(p.Exposure)}, {"X3F_Contrast", n(p.Contrast)}, {"X3F_Shadow", n(p.Blackness)},
		{"X3F_Hilight", n(p.Highlight)}, {"X3F_Saturation", n(preset.XMLSaturation(p))}, {"X3F_Sharpness", n(p.Sharpness)},
		{"X3F_FillLight", n(p.FillLight)}, {"X3F_ColorR", n(p.R)}, {"X3F_ColorG", n(p.G)}, {"X3F_ColorB", n(p.B)},
		{"X3F_WhiteBalancePreset", strconv.Itoa(p.WhiteBalance)}, {"X3F_ColorMode", strconv.Itoa(p.ColorMode)},
		{"X3F_Name", escape(name)},
	}
	if p.ColorTemp > 0 {
		set = append(set, [2]string{"X3F_WhiteBalanceTemp", strconv.Itoa(p.ColorTemp)})
	}
	required := map[string]bool{"X3F_FilterMode": true, "X3F_Brightness": true, "X3F_Contrast": true, "X3F_Hilight": true, "X3F_FillLight": true, "X3F_Saturation": true}
	for _, kv := range set {
		nt, ok := setTag(text, kv[0], kv[1])
		if !ok && required[kv[0]] {
			return "", fmt.Errorf("SPhotoPro.xml has no <%s> — unknown SPP version, not touching it", kv[0])
		}
		text = nt
	}
	return text, nil
}
