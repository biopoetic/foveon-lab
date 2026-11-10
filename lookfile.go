package main

// ".foveonlook": one plain file that carries a whole look. SPP's XML can
// carry the exposure, contrast, colour and white balance, but not the film,
// its push or paper, the darkroom, the lens filter or the layer mix; those
// live in FoveonLab_FL.json beside the XML. A shared preset therefore used to
// lose everything that made it a look. This file is the whole of it, in one
// JSON object, exported and imported without SPP, a network or any code from
// the file: importing only reads it and writes the two local files.
//
// Format (version 1):
//
//	{"format":"foveonlook","version":1,"name":"...","camera":"ALL","params":{...}}
//
// Every field is checked before anything is written. An existing preset of
// the same name is never replaced: the import is refused with 409 and the
// page asks for another name.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

const (
	lookFormat     = "foveonlook"
	lookVersion    = 1
	lookExt        = ".foveonlook"
	lookMaxBytes   = 1 << 20 // a look is a few kilobytes; do not read more
	lookNameMaxLen = 120
)

type lookFile struct {
	Format  string        `json:"format"`
	Version int           `json:"version"`
	Name    string        `json:"name"`
	Camera  string        `json:"camera"`
	Params  preset.Params `json:"params"`
}

var lookMu sync.Mutex // serialises name check + write of an import

// errLookNameExists means the import would replace an existing setting; the
// page can ask for another name and retry.
var errLookNameExists = errors.New("a preset with this name already exists")

func newLookFile(name, camera string, p preset.Params) lookFile {
	if camera != "SD1" && camera != "SD15" {
		camera = "ALL"
	}
	return lookFile{Format: lookFormat, Version: lookVersion, Name: strings.TrimSpace(name), Camera: camera, Params: p}
}

// decodeLook parses a .foveonlook file strictly: unknown fields, a wrong
// format/version, a second JSON value or a dangerous name are all refused.
func decodeLook(b []byte) (lookFile, error) {
	var l lookFile
	if len(b) > lookMaxBytes {
		return l, fmt.Errorf("the file is larger than %d KB", lookMaxBytes/1024)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return l, fmt.Errorf("not a valid .foveonlook file: %w", err)
	}
	// Exactly one JSON value, no trailing text.
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return l, errors.New("not a valid .foveonlook file: more than one JSON value")
	}
	l.Name = strings.TrimSpace(l.Name)
	if err := validateLook(l); err != nil {
		return l, err
	}
	return l, nil
}

func validateLookName(name string) error {
	if name == "" {
		return errors.New("the look has no name")
	}
	if len(name) > lookNameMaxLen {
		return fmt.Errorf("the look's name is longer than %d characters", lookNameMaxLen)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("the look's name contains a control character")
		}
	}
	if strings.ContainsAny(name, `/\:*?"<>|`) {
		return errors.New(`the look's name contains a file-name character (\ / : * ? " < > |)`)
	}
	return nil
}

// validateLook checks every field, not only that the JSON parsed. The ranges
// are the ones the app itself can produce; a look outside them would either
// do nothing or change a render in a way the UI cannot show.
func validateLook(l lookFile) error {
	if l.Format != lookFormat {
		return fmt.Errorf("format is %q, want %q", l.Format, lookFormat)
	}
	if l.Version != lookVersion {
		return fmt.Errorf("version %d is not supported (this build reads version %d)", l.Version, lookVersion)
	}
	if err := validateLookName(l.Name); err != nil {
		return err
	}
	switch l.Camera {
	case "", "ALL", "SD1", "SD15":
	default:
		return fmt.Errorf("camera %q is not SD1, SD15 or ALL", l.Camera)
	}
	return validateLookParams(l.Params, false)
}

func validateLookParams(p preset.Params, nested bool) error {
	finite := func(name string, v, lo, hi float64) error {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%s is not a finite number", name)
		}
		if v < lo || v > hi {
			return fmt.Errorf("%s is %v, outside %v…%v", name, v, lo, hi)
		}
		return nil
	}
	for _, c := range []struct {
		name string
		v    float64
		lo   float64
		hi   float64
	}{
		{"exposure", p.Exposure, -10, 10},
		{"contrast", p.Contrast, -10, 10},
		{"blackness", p.Blackness, -10, 10},
		{"highlight", p.Highlight, -10, 10},
		{"saturation", p.Saturation, -4, 4},
		{"sharpness", p.Sharpness, -10, 10},
		{"fillLight", p.FillLight, -10, 10},
		{"r", p.R, 0.25, 4},
		{"g", p.G, 0.25, 4},
		{"b", p.B, 0.25, 4},
		{"layerBottom", p.LayerBottom, -12, 6},
		{"layerMiddle", p.LayerMiddle, -12, 6},
		{"layerTop", p.LayerTop, -12, 6},
		{"monoR", p.MonoR, 0, 1},
		{"monoG", p.MonoG, 0, 1},
		{"monoB", p.MonoB, 0, 1},
		{"monoShadeRadius", p.MonoShadeRadius, 0, 1},
		{"monoShadeAmount", p.MonoShadeAmount, 0, 1},
		{"monoGrainSize", p.MonoGrainSize, 0, 1},
		{"monoGrainRough", p.MonoGrainRough, 0, 1},
		{"grain", p.Grain, 0, 200},
		{"filmWidth", p.FilmWidth, 0, 1000},
		{"halation", p.Halation, 0, 1},
		{"devTime", p.DevTime, 0, 240},
		{"push", p.Push, -4, 8},
		{"ccY", p.CCY, -100, 100},
		{"ccM", p.CCM, -100, 100},
		{"ccC", p.CCC, -100, 100},
		{"printExp", p.PrintExp, -4, 4},
		{"gndAngle", p.GNDAngle, -180, 180},
		{"gndPos", p.GNDPos, -1, 1},
		{"gndSoft", p.GNDSoft, 0, 1},
	} {
		if err := finite(c.name, c.v, c.lo, c.hi); err != nil {
			return err
		}
	}
	if p.WhiteBalance < 0 || p.WhiteBalance > 7 {
		return fmt.Errorf("whiteBalance is %d, want 0…7", p.WhiteBalance)
	}
	if p.ColorTemp < 0 || p.ColorTemp > 100000 {
		return fmt.Errorf("colorTemp is %d, outside 0…100000", p.ColorTemp)
	}
	if p.ColorMode < 0 || p.ColorMode > 7 {
		return fmt.Errorf("colorMode is %d, want 0…7", p.ColorMode)
	}
	if p.Film != "" {
		if _, ok := render.FilmByKey(p.Film); !ok {
			return fmt.Errorf("film %q is not in the film library", p.Film)
		}
	}
	if p.Developer != "" {
		if p.Film == "" {
			return errors.New("a developer is set but there is no film")
		}
		f, _ := render.FilmByKey(p.Film)
		found := false
		if f.Curve != nil {
			for _, d := range f.Curve.Developers {
				if d.Name == p.Developer {
					found = true
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("developer %q is not a developer of %s", p.Developer, p.Film)
		}
	}
	if p.Paper != "" {
		found := false
		for _, pi := range render.PaperList() {
			if pi.Paper == p.Paper {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("paper %q is not in the paper library", p.Paper)
		}
	}
	if p.Grade != "" {
		found := false
		for _, g := range render.PaperGrades {
			if g == p.Grade {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("grade %q is not 00, 0…5", p.Grade)
		}
	}
	if p.CamMode != "" {
		switch p.CamMode {
		case "Standard", "Vivid", "Neutral", "Portrait", "Landscape", "FCBlue":
		default:
			return fmt.Errorf("camMode %q is not a camera colour mode", p.CamMode)
		}
	}
	if p.MonoTone != "" {
		if _, ok := preset.MonoToneOffset(p.MonoTone); !ok {
			return fmt.Errorf("monoTone %q is not one of SPP's toning presets", p.MonoTone)
		}
	}
	if p.Filter != "" && !knownLookFilter(p.Filter) {
		return fmt.Errorf("filter %q is not a lens filter this build knows", p.Filter)
	}
	if p.ND != "" {
		if _, ok := render.DensityFilter(p.ND); !ok {
			return fmt.Errorf("nd %q is not a neutral-density filter", p.ND)
		}
	}
	if p.GND != "" {
		f, ok := render.DensityFilter(p.GND)
		if !ok || f.Kind != "gnd" {
			return fmt.Errorf("gnd %q is not a graduated neutral-density filter", p.GND)
		}
	}
	if nested && (p.MaxDetail || p.SPPFallback != nil) {
		return errors.New("a look's SPP fallback cannot itself be a Max Detail preset")
	}
	if p.MaxDetail && p.SPPFallback != nil {
		if err := validateLookParams(*p.SPPFallback, true); err != nil {
			return fmt.Errorf("SPP fallback: %w", err)
		}
	}
	return nil
}

// knownLookFilter is the lens-filter vocabulary the render knows: the colour
// filters in render/layerfilters.json and the infrared looks in
// render/irlooks.json.
func knownLookFilter(f string) bool {
	switch f {
	case "yellow", "deepyellow", "orange", "red", "deepred", "yellowgreen", "green", "blue", "warm", "cool":
		return true
	}
	if strings.HasPrefix(f, "ir-") {
		switch strings.TrimPrefix(f, "ir-") {
		case "fs-aerochrome", "aerochrome", "ir590", "ir720":
			return true
		}
	}
	if strings.HasPrefix(f, "br-") {
		id := strings.TrimPrefix(f, "br-")
		for _, bf := range render.BrandFilters() {
			if bf.ID == id {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(f, "pad:") {
		// A measured pad filter: three layer gains, as the UI writes it.
		parts := strings.Split(strings.TrimPrefix(f, "pad:"), ",")
		if len(parts) != 3 {
			return false
		}
		for _, s := range parts {
			v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil || math.IsNaN(v) || v < 0 || v > 10 {
				return false
			}
		}
		return true
	}
	return false
}

// presetNamesInFolder lists the setting names already written in the preset
// folder's XML files, case-insensitively. This is what import must not
// overwrite: a look of the same name would replace the setting.
func presetNamesInFolder(dir string) (map[string]bool, error) {
	names := map[string]bool{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".xml") {
			return nil
		}
		ps, err := preset.ParseFile(p)
		if err != nil {
			return nil // a broken file is reported by the preset loader
		}
		for _, q := range ps {
			names[strings.ToLower(strings.TrimSpace(q.Name))] = true
		}
		return nil
	})
	return names, err
}

// importLook validates and writes one look into the preset folder. It refuses
// (409) rather than replacing an existing setting of the same name. The name
// may be overridden (the page asks the user), but the override is validated
// the same way.
func importLook(dir string, l lookFile, override string) (path string, err error) {
	if dir == "" {
		return "", errors.New("no preset folder is set; choose one in Folders… first")
	}
	if override = strings.TrimSpace(override); override != "" {
		l.Name = override
	}
	if err := validateLookName(l.Name); err != nil {
		return "", err
	}
	lookMu.Lock()
	defer lookMu.Unlock()
	names, err := presetNamesInFolder(dir)
	if err != nil {
		return "", err
	}
	if names[strings.ToLower(l.Name)] {
		return "", fmt.Errorf("%w: %q already exists in %s; give the imported look another name", errLookNameExists, l.Name, dir)
	}
	cam := l.Camera
	if cam != "SD1" && cam != "SD15" {
		cam = "ALL"
	}
	path = filepath.Join(dir, "FoveonLab_"+cam+".xml")
	// A Max Detail look is written as its SPP fallback plus the user's
	// adjustments, exactly like "Save as preset": SPP has no raw mode.
	save := maxDetailSPPParams(l.Params)
	if err := preset.Append(path, l.Name, save); err != nil {
		return "", err
	}
	if err := saveFLParams(dir, filepath.Base(path), l.Name, save); err != nil {
		return "", err
	}
	return path, nil
}

// handleLookExport downloads a preset (or the current tuned parameters) as
// one .foveonlook file. The FL sidecar is read from what the preset loader
// merged, so the export is the whole look the app shows.
func (s *server) handleLookExport(w http.ResponseWriter, r *http.Request) {
	p, label, err := s.params(r.URL.Query().Get("preset"), r.URL.Query().Get("p"))
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = label
	}
	cam := strings.TrimSpace(r.URL.Query().Get("camera"))
	if cam == "" {
		if ph := s.lookup(r.URL.Query().Get("id")); ph != nil {
			cam = ph.Camera
		}
	}
	l := newLookFile(name, cam, p)
	if err := validateLook(l); err != nil {
		httpErr(w, 400, err)
		return
	}
	b, err := json.MarshalIndent(l, "", " ")
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(l.Name)+lookExt+`"`)
	w.Write(append(b, '\n'))
}

// handleLookImport reads a .foveonlook body (the page sends the file's own
// bytes), validates every field, and writes the XML and its FL sidecar. It
// never fetches, runs or evaluates anything from the file, and never replaces
// an existing preset silently: a name collision is a 409.
func (s *server) handleLookImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, lookMaxBytes+1))
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	l, err := decodeLook(body)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	override := strings.TrimSpace(r.URL.Query().Get("name"))
	if override != "" {
		if err := validateLookName(override); err != nil {
			httpErr(w, 400, err)
			return
		}
		l.Name = override
	}
	s.pmu.RLock()
	dir := s.presetsDir
	s.pmu.RUnlock()
	if dir == "" {
		httpErr(w, 400, errors.New("no preset folder is set; choose one in Folders… first"))
		return
	}
	path, err := importLook(dir, l, "")
	if err != nil {
		code := 500
		switch {
		case errors.Is(err, errLookNameExists):
			code = http.StatusConflict
		case errors.Is(err, os.ErrPermission), errors.Is(err, os.ErrNotExist):
			code = 500
		}
		httpErr(w, code, err)
		return
	}
	log.Printf("imported look %q from a .foveonlook file into %s", l.Name, path)
	s.loadPresets()
	payload := s.presetsPayload().(map[string]any)
	payload["savedTo"] = path
	payload["imported"] = l.Name
	writeJSON(w, payload)
}
