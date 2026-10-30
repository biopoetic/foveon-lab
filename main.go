// Foveon Lab — try Sigma Photo Pro presets on SD1 / SD15 X3F files.
//
// A local web app: it lists your X3F shots, renders each one through every
// SPP preset XML it finds (an emulation on the JPEG preview embedded in the
// X3F), lets you fine-tune a preset with sliders, and writes the result back
// as an SPP-importable XML.
package main

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/biopoetic/foveon-lab/agent"
	"github.com/biopoetic/foveon-lab/calib"
	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/spp"
	"github.com/biopoetic/foveon-lab/x3f"
)

//go:embed web
var webFS embed.FS

// Photo is one shot in the catalogue.
type Photo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Folder string `json:"folder"`
	Camera string `json:"camera"`
	Kind   string `json:"kind"` // "x3f", "jpg" (Sigma camera JPEG) or "image" (any other picture)
	// Unsupported explains why the photo can't be previewed; empty when it can.
	Unsupported string `json:"unsupported,omitempty"`
	// LowRes: only a small preview exists (SD9/SD10 carry a 567 px one and
	// no full-size JPEG), so renders are low resolution.
	LowRes bool `json:"lowRes,omitempty"`
	// RawDecoded: shown from its raw data (natively, else via x3f_extract;
	// full resolution, no in-camera styling) because the file has no
	// full-size preview.
	RawDecoded bool `json:"rawDecoded,omitempty"`
	// Favorite: starred by the user (remembered in the app's config).
	Favorite bool              `json:"favorite,omitempty"`
	Info     map[string]string `json:"info"`
	// Accurate: an accurate (raw-based) preview exists for this camera.
	Accurate bool `json:"accurate,omitempty"`

	path      string
	rotation  int
	props     map[string]string
	rawFailed atomic.Bool // raw decode failed once; stay on the preview
	camfOnce  sync.Once   // photoCC: the as-shot WB colour matrix, read once
	camfCC    *[9]float64
	layerOnce sync.Once // layerCC: the colour matrix for the layer filter
	layerCC   *[9]float64
	// Size of the embedded full preview (for the raw decode's aspect).
	previewW, previewH int
	// jpegBroken: the embedded JPEG is damaged; shown from the raw data.
	jpegBroken atomic.Bool
}

// infoKeys are the PROP fields shown in the UI.
var infoKeys = []string{"CAMMODEL", "ISO", "SH_DESC", "AP_DESC", "FLENGTH", "EXPCOMP", "CM_DESC", "WB_DESC", "TIME"}

func desktopDir() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{filepath.Join(home, "Desktop"), filepath.Join(home, "OneDrive", "Desktop")} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return home
}

func idOf(s string) string {
	h := sha1.Sum([]byte(strings.ToLower(s)))
	return hex.EncodeToString(h[:6])
}

var reSDIM = regexp.MustCompile(`(?i)^SDIM\d+\.jpe?g$`)

// scanPhotos finds X3F files (and SDIM*.JPG camera JPEGs) under the roots;
// with all it also takes any JPEG/TIFF/PNG.
func scanPhotos(roots []string, all bool) []*Photo {
	var out []*Photo
	seen := map[string]bool{}
	for _, root := range roots {
		root = filepath.Clean(root)
		base := strings.Count(root, string(filepath.Separator))
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := strings.ToLower(d.Name())
				// Skip app/library bundles (macOS Photos/Lightroom libraries
				// under Pictures hold huge trees that are never X3F sources).
				if p != root && (strings.HasPrefix(name, ".") || name == "foveon-lab" || name == "node_modules" ||
					strings.HasSuffix(name, ".photoslibrary") || strings.HasSuffix(name, ".lrdata") || strings.HasSuffix(name, ".app") ||
					strings.Count(p, string(filepath.Separator))-base > 3) {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			sigma := ext == ".x3f" || reSDIM.MatchString(d.Name())
			if !sigma && !(all && otherImageExt[ext]) {
				return nil
			}
			id := idOf(p)
			if seen[id] {
				return nil
			}
			seen[id] = true
			ph := &Photo{ID: id, Name: d.Name(), Folder: filepath.Base(filepath.Dir(p)), path: p, Info: map[string]string{}, Kind: "jpg"}
			switch {
			case ext == ".x3f":
				ph.Kind = "x3f"
			case !sigma:
				ph.Kind = "image"
			}
			out = append(out, ph)
			return nil
		})
	}

	// Reading X3F headers is I/O-latency bound; do it in parallel, and skip
	// the read altogether for a file whose header is already known
	// (photometa.go: a cold disk made 844 photos cost 41 s, a warm cache
	// 0.43 s).
	mc := loadMetaCache()
	ok := make([]bool, len(out))
	var wg sync.WaitGroup
	jobs := make(chan int)
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				ok[i] = loadMetaCached(out[i], mc)
			}
		}()
	}
	for i := range out {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	lastScanCache = fmt.Sprintf("%d hits, %d misses", mc.hits, mc.misses)
	mc.save()
	kept := out[:0]
	for i, ph := range out {
		if ok[i] {
			kept = append(kept, ph)
		}
	}
	out = kept

	sort.Slice(out, func(i, j int) bool {
		if out[i].Folder != out[j].Folder {
			return out[i].Folder < out[j].Folder
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// loadMeta fills camera, rotation and shot info from the X3F header.
func loadMeta(ph *Photo) bool {
	if ph.Kind == "x3f" {
		f, err := x3f.Open(ph.path)
		if err != nil {
			log.Printf("skipping %s: %v", ph.path, err)
			return false
		}
		ph.props, ph.Camera, ph.rotation = f.Props, f.Camera(), f.Rotation
		for _, im := range f.Images {
			if im.ImgType == 2 && im.Format == 18 && int(im.Width) > ph.previewW {
				ph.previewW, ph.previewH = int(im.Width), int(im.Height)
			}
		}
		switch {
		case !f.HasPreview():
			ph.Unsupported = "This X3F has no preview Foveon Lab can decode yet."
		case !f.HasFullPreview():
			// SD9/SD10: only the camera's 567 px preview — decode the raw
			// data instead (natively, or with x3f_extract).
			if rawAvailable() {
				ph.RawDecoded = true
			} else {
				ph.LowRes = true
			}
		}
		if r, err := strconv.Atoi(f.Props["ROTATION"]); err == nil && (r == 90 || r == 180 || r == 270) {
			ph.rotation = r
		}
		for _, k := range infoKeys {
			if v := strings.TrimSpace(f.Props[k]); v != "" {
				ph.Info[k] = v
			}
		}
		if l := lensDesc(f.Props); l != "" {
			ph.Info["LENS"] = l
		}
		if f.Recovered != "" {
			// Cut short (e.g. a card pulled mid-write): read by scanning.
			ph.Info["REPAIRED"] = "damaged file, read without its section directory"
			log.Printf("%s: damaged file (%s), sections recovered by scanning", ph.Name, f.Recovered)
		}
		if t, err := strconv.ParseInt(ph.Info["TIME"], 10, 64); err == nil {
			ph.Info["TIME"] = time.Unix(t, 0).UTC().Format("2006-01-02 15:04")
		}
	}
	if ph.Kind == "image" {
		ph.rotation = exifRotation(ph.path)
		return true // not a Sigma file: no camera, so every preset is offered
	}
	if ph.Camera == "" {
		ph.Camera = x3f.ShortModel(ph.Folder)
	}
	ph.Accurate = rawCorrectionFor(ph) != nil
	return true
}

// ---------- LRU cache with in-flight de-duplication ----------

type cell struct {
	key  string
	done chan struct{}
	val  any
	err  error
}

type lru struct {
	mu  sync.Mutex
	max int
	ll  *list.List
	m   map[string]*list.Element
}

func newLRU(n int) *lru { return &lru{max: n, ll: list.New(), m: map[string]*list.Element{}} }

func (c *lru) get(key string, load func() (any, error)) (any, error) {
	c.mu.Lock()
	if e, ok := c.m[key]; ok {
		c.ll.MoveToFront(e)
		cl := e.Value.(*cell)
		c.mu.Unlock()
		<-cl.done
		return cl.val, cl.err
	}
	cl := &cell{key: key, done: make(chan struct{})}
	c.m[key] = c.ll.PushFront(cl)
	for c.ll.Len() > c.max {
		old := c.ll.Back()
		c.ll.Remove(old)
		delete(c.m, old.Value.(*cell).key)
	}
	c.mu.Unlock()

	cl.val, cl.err = load()
	close(cl.done)
	if cl.err != nil {
		c.mu.Lock()
		if e, ok := c.m[key]; ok && e.Value == cl {
			c.ll.Remove(e)
			delete(c.m, key)
		}
		c.mu.Unlock()
	}
	return cl.val, cl.err
}

// ---------- server ----------

const sourceW = 1600 // working resolution for grid + detail views

type server struct {
	photoMu    sync.RWMutex // guards photos, byID, photoRoots (rescans)
	photos     []*Photo
	byID       map[string]*Photo
	photoRoots []string
	allImages  bool
	presetsDir string

	pmu     sync.RWMutex
	presets []preset.Preset
	pByID   map[string]preset.Preset
	pErrs   []string

	sources *lru // id → *render.Linear at sourceW
	bases   *lru // id|w → *render.Base
	thumbs  *lru // id → []byte
	sem     chan struct{}

	ai aiState

	// Accurate (raw-based) previews, see accurate.go.
	accurate atomic.Bool
	rawMu    sync.Mutex
	rawState map[string]string
	rawQueue chan *Photo
	// rawQueueLow: prefetches of the neighbours, decoded when nothing is urgent.
	rawQueueLow chan *Photo
}

func (s *server) loadPresets() {
	// Your own folder first, then the bundled pack, then the presets saved
	// in SPP itself; a preset already loaded (same values) isn't repeated.
	var srcs []preset.Source
	if s.presetsDir != "" {
		srcs = append(srcs, preset.Source{FS: os.DirFS(s.presetsDir), Merge: flMerge(s.presetsDir)})
	}
	srcs = append(srcs, preset.Source{Label: "Built-in", FS: preset.Builtin()})
	if f := spp.SettingsFile(); f != "" {
		srcs = append(srcs, preset.Source{
			Label: "SPP", FS: os.DirFS(filepath.Dir(f)),
			Match: func(p string) bool { return p == filepath.Base(f) },
			Skip:  func(name string) bool { return strings.HasPrefix(name, spp.PresetPrefix) },
		})
	}
	ps, errs := preset.Load(srcs...)
	for i := range ps {
		ps[i].Film = render.FilmFor(ps[i].Name)
	}
	// The film stocks first: each simulated film is a preset (filmpresets.go),
	// then the camera's own colour modes, the white balance presets and the
	// infrared looks.
	gen := append(append(filmPresets(), cameraModePresets()...), wbPresets()...)
	ps = append(append(gen, irPresets()...), ps...)
	m := map[string]preset.Preset{}
	for _, p := range ps {
		m[p.ID] = p
	}
	var es []string
	for _, e := range errs {
		es = append(es, e.Error())
	}
	s.pmu.Lock()
	s.presets, s.pByID, s.pErrs = ps, m, es
	s.pmu.Unlock()
	log.Printf("loaded %d presets (folder %q + built-in + SPP) with %d errors", len(ps), s.presetsDir, len(errs))
}

func decodePreview(ph *Photo) (image.Image, error) {
	if ph.Unsupported != "" {
		return nil, errors.New(ph.Unsupported)
	}
	if ph.Kind == "x3f" {
		if ph.RawDecoded && !ph.rawFailed.Load() {
			var img image.Image
			var err error
			if goRaw {
				var l *render.Linear
				if l, err = goLinear(ph, 0, defaultNR); err == nil {
					// SD9/SD10: no SPP pairs of their own yet; their base
					// is the SD1's taste (render/rawbase.json, tasteFrom).
					if c := render.RawBaseFor(calibGroup(ph), wbDesc(ph)); c != nil {
						c.Apply(l)
					}
					img = linearToSRGB16(l)
				}
			} else {
				img, err = decodeRaw(ph)
			}
			if err == nil {
				return img, nil
			}
			log.Printf("raw decode of %s failed, using its small preview: %v", ph.Name, err)
			ph.rawFailed.Store(true)
		}
		f, err := x3f.Open(ph.path)
		if err != nil {
			return nil, err
		}
		img, _, err := f.Preview()
		return img, err
	}
	if ph.Kind == "image" {
		return decodeImageFile(ph.path)
	}
	data, err := os.ReadFile(ph.path)
	if err != nil {
		return nil, err
	}
	return jpeg.Decode(bytes.NewReader(data))
}

// balanceJPEG applies the measured per-photo colour balance that brings a
// camera-JPEG preview closer to SPP's neutral rendering (render.BalanceJPEG).
func balanceJPEG(ph *Photo, l *render.Linear) *render.Linear {
	if ph.Kind == "x3f" && !ph.RawDecoded && ph.props != nil {
		render.BalanceJPEG(calibGroup(ph), l, ph.props["WB_DESC"] == "Auto")
	}
	return l
}

// sourceMode names which source a photo renders from right now: "raw" (the
// accurate, raw-based one), else "jpg" (its preview).
func (s *server) sourceMode(ph *Photo) string {
	if ph.jpegBroken.Load() {
		return "raw"
	}
	if s.useRaw(ph) {
		switch s.rawStateOf(ph) {
		case rawReady:
			return "raw"
		case rawFast:
			s.requestRaw(ph) // the full decode follows
			return "rawfast"
		}
		// Nothing raw is ready yet: paint the camera's own embedded JPEG at
		// once and let the quick and the full decode replace it (the client
		// polls /api/rawstate and re-renders when the mode changes), so the
		// first view of a photo does not wait for a decode at all.
		s.requestRaw(ph)
		return "jpg"
	}
	s.requestRaw(ph)
	return "jpg"
}

func (s *server) source(ph *Photo) (*render.Linear, error) {
	return s.sourceAs(ph, s.sourceMode(ph))
}

func (s *server) sourceAs(ph *Photo, mode string) (*render.Linear, error) {
	v, err := s.sources.get(ph.ID+"|"+mode, func() (any, error) {
		if mode == "raw" {
			return rawLinear(ph, sourceW, "sRGB")
		}
		if f, ok := strings.CutPrefix(mode, "rawlayers:"); ok {
			return rawLinearFor(ph, sourceW, "sRGB", f)
		}
		if f, ok := strings.CutPrefix(mode, "rawwb:"); ok {
			return rawLinearFor(ph, sourceW, "sRGB", f)
		}
		if mode == "rawfast" {
			return rawLinear(ph, sourceW, rawFastColor)
		}
		img, err := decodePreview(ph)
		if err != nil {
			// A damaged embedded JPEG: fall back to the raw data.
			if ph.Kind == "x3f" && rawAvailable() {
				if l, rerr := rawLinear(ph, sourceW, "sRGB"); rerr == nil {
					log.Printf("%s: embedded preview unreadable (%v), showing its raw data", ph.Name, err)
					ph.jpegBroken.Store(true)
					return l, nil
				}
			}
			return nil, err
		}
		return balanceJPEG(ph, render.FromImage(img, sourceW, ph.rotation)), nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*render.Linear), nil
}

// fullGeo is ph's source at full resolution with its geometry applied
// (the 100% view), cached.
func (s *server) fullGeo(ph *Photo, mode string, g render.Geometry) (*render.Linear, error) {
	v, err := s.sources.get(ph.ID+"|"+mode+"|full"+geoKey(g), func() (any, error) {
		var src *render.Linear
		var err error
		irFilter, ir := strings.CutPrefix(mode, "rawlayers:")
		wbFilter, wb := strings.CutPrefix(mode, "rawwb:")
		switch {
		case ir:
			src, err = rawLinearFor(ph, 0, "sRGB", irFilter)
		case wb:
			src, err = rawLinearFor(ph, 0, "sRGB", wbFilter)
		case mode == "raw", mode == "rawfast":
			src, err = rawLinear(ph, 0, "sRGB")
		default:
			var img image.Image
			if img, err = decodePreview(ph); err == nil {
				src = balanceJPEG(ph, render.FromImage(img, 0, ph.rotation))
			}
		}
		if err != nil {
			return nil, err
		}
		l := render.ApplyGeometry(src, g)
		l.Scale = 1
		return l, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*render.Linear), nil
}

func (s *server) base(ph *Photo, w int) (*render.Base, error) {
	return s.baseAs(ph, w, s.sourceMode(ph), geometryOf(ph))
}

func (s *server) baseAs(ph *Photo, w int, mode string, g render.Geometry) (*render.Base, error) {
	v, err := s.bases.get(ph.ID+"|"+mode+"|"+strconv.Itoa(w)+geoKey(g), func() (any, error) {
		src, err := s.sourceAs(ph, mode)
		if err != nil {
			return nil, err
		}
		// Straighten + crop at source resolution, then size for the view.
		return render.NewBase(render.ApplyGeometry(src, g).Downscale(w)), nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*render.Base), nil
}

// modelOf is the camera's own model name ("SIGMA SD14"), not the preset
// family: capabilities that differ between cameras sharing a family (the
// SD14 and the SD15 are both "SD15") need the model, not the family.
func modelOf(ph *Photo) string {
	if ph.props != nil {
		if m := ph.props["CAMMODEL"]; m != "" {
			return m
		}
		if m := ph.props["CAMNAME"]; m != "" {
			return m
		}
	}
	return ph.Camera
}

// rawCompensation is the compensation of a raw-based render: a raw decode
// carries no in-camera styling, so there is nothing to undo (also used by
// fit-raw to score raw bases as the app shows them).
func rawCompensation(ph *Photo) render.Compensation {
	camera := x3f.ShortModel(modelOf(ph))
	if camera == "" {
		camera = calibGroup(ph)
	}
	return render.Compensation{SatFactor: 1, Family: x3f.PresetFamily(ph.Camera), CC: layerCC(ph), Sensor: render.SensorGeneration(calibGroup(ph)), NoModes: !x3f.HasColourModes(modelOf(ph)), Model: camera}
}

func (s *server) compensation(ph *Photo, on bool) render.Compensation {
	// A raw decode carries no in-camera styling, so there is nothing to undo.
	fam := x3f.PresetFamily(ph.Camera)
	modes := !x3f.HasColourModes(modelOf(ph))
	camera := x3f.ShortModel(modelOf(ph))
	if camera == "" {
		camera = calibGroup(ph) // Quattro and the DP bodies have no SD number
	}
	if !on || ph.props == nil || (ph.RawDecoded && !ph.rawFailed.Load()) || s.useRaw(ph) || ph.jpegBroken.Load() {
		return rawCompensation(ph)
	}
	c := render.CompensationWith(ph.props, fam, photoCC(ph))
	c.Family = fam
	c.CC = layerCC(ph)
	c.Sensor = render.SensorGeneration(calibGroup(ph))
	c.NoModes = modes
	c.Model = camera
	return c
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, err error) {
	http.Error(w, err.Error(), code)
}

func (s *server) photo(r *http.Request) (*Photo, error) {
	ph := s.lookup(r.URL.Query().Get("id"))
	if ph == nil {
		return nil, errors.New("unknown photo")
	}
	return ph, nil
}

// params resolves render parameters: a preset id, optionally overridden by
// a full JSON parameter set in "p".
func (s *server) params(presetID, pJSON string) (preset.Params, string, error) {
	p := preset.Neutral()
	label := "original"
	if presetID != "" {
		s.pmu.RLock()
		pr, ok := s.pByID[presetID]
		s.pmu.RUnlock()
		if !ok {
			return p, "", errors.New("unknown preset")
		}
		p, label = pr.Params, pr.Name
	}
	if pJSON != "" {
		if err := json.Unmarshal([]byte(pJSON), &p); err != nil {
			return p, "", fmt.Errorf("params: %w", err)
		}
		if presetID != "" {
			label += " (tuned)"
		} else {
			label = "tuned"
		}
	}
	return p, label, nil
}

// renderBase is the Base and settings a render request names (photo, size,
// source, geometry, the 100% window), as /api/render and the GPU preview
// use them. On failure it has written the error.
func (s *server) renderBase(w http.ResponseWriter, r *http.Request) (*render.Base, preset.Params, render.Compensation, bool) {
	var comp render.Compensation
	ph, err := s.photo(r)
	if err != nil {
		httpErr(w, 404, err)
		return nil, preset.Params{}, comp, false
	}
	if ph.Unsupported != "" {
		httpErr(w, 422, errors.New(ph.Unsupported))
		return nil, preset.Params{}, comp, false
	}
	q := r.URL.Query()
	width, _ := strconv.Atoi(q.Get("w"))
	width = max(160, min(sourceW, (width+39)/40*40))
	p, _, err := s.params(q.Get("preset"), q.Get("p"))
	if err != nil {
		httpErr(w, 400, err)
		return nil, p, comp, false
	}
	// src=camera: the camera's own JPEG (the "Original" tile), even when
	// the accurate raw-based preview is in use.
	mode := s.sourceMode(ph)
	if q.Get("src") == "camera" && !ph.jpegBroken.Load() {
		mode = "jpg"
	} else if lf, ok := layersSource(ph, p); ok {
		mode = "rawlayers:" + lf
	} else if wf, ok := wbSource(ph, p); ok && mode == "raw" {
		mode = "rawwb:" + wf
	}
	// The GPU preview keeps its Base until the source changes (a colour
	// film develops the raw layers; the raw decode replaces the JPEG).
	w.Header().Set("X-Base-Source", mode)
	// nogeo=1: the whole (turned) frame, for the crop editor.
	g := geometryOf(ph)
	if q.Get("nogeo") == "1" {
		g = render.Geometry{Turn: g.Turn}
	}
	var b *render.Base
	if q.Get("z") == "1" {
		// 100% view: only the visible window of the full-resolution image.
		full, err := s.fullGeo(ph, mode, g)
		if err != nil {
			httpErr(w, 500, err)
			return nil, p, comp, false
		}
		zx, _ := strconv.ParseFloat(q.Get("zx"), 64)
		zy, _ := strconv.ParseFloat(q.Get("zy"), 64)
		zw, _ := strconv.Atoi(q.Get("zw"))
		zh, _ := strconv.Atoi(q.Get("zh"))
		zw, zh = max(64, min(full.W, zw, 4000)), max(64, min(full.H, zh, 4000))
		x0 := max(0, min(full.W-zw, int(math.Round(zx*float64(full.W)))-zw/2))
		y0 := max(0, min(full.H-zh, int(math.Round(zy*float64(full.H)))-zh/2))
		b = render.NewBase(full.Window(x0, y0, zw, zh))
		w.Header().Set("X-Full", fmt.Sprintf("%d,%d,%d,%d", full.W, full.H, x0, y0))
	} else if b, err = s.baseAs(ph, width, mode, g); err != nil {
		httpErr(w, 500, err)
		return nil, p, comp, false
	}
	comp = s.compensation(ph, q.Get("comp") == "1")
	if strings.HasPrefix(mode, "rawlayers:") {
		comp = layerCompensation(comp)
	}
	comp.WBDeveloped = strings.HasPrefix(mode, "rawwb:")
	return b, p, comp, true
}

// handleGPUBase sends the Base of a render request for the GPU preview
// (render.GPUBase: half floats).
func (s *server) handleGPUBase(w http.ResponseWriter, r *http.Request) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	b, _, _, ok := s.renderBase(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b.GPUBase())
}

// handleGPURecipe sends the settings of a render request for the GPU
// preview (render.NewGPURecipe).
func (s *server) handleGPURecipe(w http.ResponseWriter, r *http.Request) {
	b, p, comp, ok := s.renderBase(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, render.NewGPURecipe(b, p, comp))
}

func (s *server) handleRender(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	t0 := time.Now()
	b, p, comp, ok := s.renderBase(w, r)
	if !ok {
		return
	}
	t1 := time.Now()
	out := render.RenderOpt(b, p, comp, q.Get("fast") != "1")
	// norm=1: match the average brightness to mid-grey, for the film
	// comparison, where different films (and the un-filmed render) land at
	// different densities and the point is colour and tone, not exposure.
	if q.Get("norm") == "1" {
		normalizeMean(out, 128)
	}
	t2 := time.Now()
	var buf bytes.Buffer
	ctype := "image/jpeg"
	if q.Get("fmt") == "bmp" {
		// The editor on this machine: uncompressed (JPEG encoding took longer
		// than the render itself; over loopback size costs nothing).
		writeBMP(&buf, out)
		ctype = "image/bmp"
	} else if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 88}); err != nil {
		httpErr(w, 500, err)
		return
	}
	// Where the time goes (browser devtools show it; the editor measures it).
	w.Header().Set("Server-Timing", fmt.Sprintf("base;dur=%.1f, render;dur=%.1f, encode;dur=%.1f",
		t1.Sub(t0).Seconds()*1000, t2.Sub(t1).Seconds()*1000, time.Since(t2).Seconds()*1000))
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.Write(buf.Bytes())
}

// writeBMP writes img as a 24-bit top-down BMP.
func writeBMP(buf *bytes.Buffer, img *image.RGBA) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	stride := (w*3 + 3) &^ 3
	size := 54 + stride*h
	le := func(v uint32, n int) {
		for i := 0; i < n; i++ {
			buf.WriteByte(byte(v >> (8 * i)))
		}
	}
	buf.Grow(size)
	buf.WriteString("BM")
	le(uint32(size), 4)
	le(0, 4)
	le(54, 4)
	le(40, 4)
	le(uint32(w), 4)
	le(uint32(-int32(h)), 4) // negative height: rows top to bottom
	le(1, 2)
	le(24, 2)
	le(0, 4)
	le(uint32(stride*h), 4)
	le(2835, 4)
	le(2835, 4)
	le(0, 4)
	le(0, 4)
	row := make([]byte, stride)
	for y := 0; y < h; y++ {
		src := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			row[x*3], row[x*3+1], row[x*3+2] = src[x*4+2], src[x*4+1], src[x*4]
		}
		buf.Write(row)
	}
}

func (s *server) handleThumb(w http.ResponseWriter, r *http.Request) {
	ph, err := s.photo(r)
	if err != nil {
		httpErr(w, 404, err)
		return
	}
	v, err := s.thumbs.get(ph.ID, func() (any, error) {
		if ph.Kind == "x3f" {
			f, err := x3f.Open(ph.path)
			if err != nil {
				return nil, err
			}
			// SD9/SD10 thumbnails aren't JPEG; fall through and render one.
			if b, err := f.ThumbJPEG(); err == nil {
				return b, nil
			}
		}
		img, err := decodePreview(ph)
		if err != nil {
			return nil, err
		}
		b := render.NewBase(render.FromImage(img, 240, 0))
		var buf bytes.Buffer
		err = jpeg.Encode(&buf, render.Render(b, preset.Neutral(), render.Compensation{SatFactor: 1}), &jpeg.Options{Quality: 80})
		return buf.Bytes(), err
	})
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(v.([]byte))
}

var reUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeName(s string) string {
	s = strings.Trim(reUnsafe.ReplaceAllString(s, "_"), "_")
	if len(s) > 60 {
		s = s[:60]
	}
	if s == "" {
		s = "preset"
	}
	return s
}

// handleExport renders the full-size preview and saves it next to the shot
// in a "foveon-lab" sub-folder.
func (s *server) handleExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string          `json:"id"`
		Preset string          `json:"preset"`
		Params json.RawMessage `json:"params"`
		Comp   bool            `json:"comp"`
		Format string          `json:"format"` // "jpeg" (default), "tiff", "dng" (rendered), "rawdng"
		// Denoise: x3f_extract's noise reduction for raw-based exports, 0-10
		// (nil = 10, its default and what the SPP calibration used).
		Denoise *int `json:"denoise"`
		// Space: the export colour space (render.ColourSpaces; "" = sRGB),
		// for JPEG and TIFF.
		Space string `json:"space"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	if _, ok := render.ColourSpaceByID(req.Space); !ok {
		httpErr(w, 400, errors.New("unknown colour space "+req.Space))
		return
	}
	ph := s.lookup(req.ID)
	if ph == nil {
		httpErr(w, 404, errors.New("unknown photo"))
		return
	}
	dir := filepath.Join(filepath.Dir(ph.path), "foveon-lab")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httpErr(w, 500, err)
		return
	}
	stem := strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name))
	pj := ""
	if len(req.Params) > 0 && string(req.Params) != "null" {
		pj = string(req.Params)
	}
	p, label, err := s.params(req.Preset, pj)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	if req.Format == "rawdng" {
		s.exportDNG(w, ph, dir, stem, p, label, req.Denoise)
		return
	}
	path, err := s.exportImage(ph, p, label, req.Comp, req.Format, req.Denoise, req.Space)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]string{"path": path})
}

// exportImage renders ph at full resolution with p and saves it as JPEG or
// 16-bit TIFF in a "foveon-lab" folder next to the photo.
// rawExportColor is the raw decode for an export at denoise strength dn.
func rawExportColor(dn *int) string {
	switch {
	case dn == nil:
		return "sRGB" // the default (defaultNR)
	case *dn <= 0:
		return "sRGB-nodenoise"
	}
	return "sRGB-dn" + strconv.Itoa(*dn)
}

func (s *server) exportImage(ph *Photo, p preset.Params, label string, comp bool, format string, denoise *int, space string) (string, error) {
	cs, ok := render.ColourSpaceByID(space)
	if !ok {
		return "", errors.New("unknown colour space " + space)
	}
	if cs.ID != "srgb" && format != "dng" {
		label += "_" + cs.ID // e.g. _prophoto: the file says what it is
	}
	dir := filepath.Join(filepath.Dir(ph.path), "foveon-lab")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	var src *render.Linear
	wbDeveloped := false
	lf, ir := layersSource(ph, p)
	if ir {
		l, err := rawLinearFor(ph, 0, rawExportColor(denoise), lf)
		if err != nil {
			return "", err
		}
		src = l
	} else if s.wantRaw(ph) || ph.jpegBroken.Load() {
		// Accurate mode: export from the raw data even if the preview
		// hasn't switched yet (this waits for the decode).
		color := rawExportColor(denoise)
		wf, _ := wbSource(ph, p)
		wbDeveloped = wf != ""
		l, err := rawLinearFor(ph, 0, color, wf)
		if err != nil {
			return "", err
		}
		if color == "sRGB" { // the preview's full decode is ready now too
			s.rawMu.Lock()
			s.rawState[ph.ID] = rawReady
			s.rawMu.Unlock()
		} else {
			label += "_nr" + strconv.Itoa(max(0, *denoise)) // e.g. _nr0, _nr5
		}
		src = l
	} else {
		img, err := decodePreview(ph)
		if err != nil {
			return "", err
		}
		src = balanceJPEG(ph, render.FromImage(img, 0, ph.rotation))
	}
	base := render.NewBase(render.ApplyGeometry(src, geometryOf(ph)))
	c := s.compensation(ph, comp)
	if ir {
		c = layerCompensation(c)
	}
	c.WBDeveloped = wbDeveloped
	ext := ".jpg"
	switch format {
	case "tiff":
		ext = ".tif"
	case "dng":
		ext = ".dng"
	}
	stem := strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name))
	path := filepath.Join(dir, stem+"_"+safeName(label)+ext)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	switch format {
	case "tiff":
		img := render.Render16(base, p, c)
		render.ConvertFromSRGB(img, cs)
		err = render.WriteTIFF16ICC(f, img, cs.ICC())
	case "dng":
		make_, model := "SIGMA", ph.Camera
		if ph.props != nil {
			make_, model = ph.props["CAMMANUF"], ph.props["CAMMODEL"]
		}
		err = render.WriteRenderedDNG(f, render.Render16(base, p, c), make_, model, "Foveon Lab "+version)
	default:
		// sRGB straight from the 8-bit render; another space from the
		// 16-bit one, converted before rounding. Either way the profile is
		// embedded, so every viewer reads the colours right.
		var img image.Image
		if cs.ID == "srgb" {
			img = render.Render(base, p, c)
		} else {
			img16 := render.Render16(base, p, c)
			render.ConvertFromSRGB(img16, cs)
			img = render.To8(img16)
		}
		var buf bytes.Buffer
		if err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err == nil {
			_, err = f.Write(render.JPEGWithICC(buf.Bytes(), cs.ICC()))
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	log.Printf("exported %s", path)
	return path, nil
}

// exportDNG converts the X3F's raw data to a DNG with x3f_extract, for
// Lightroom / Capture One / darktable. It is raw, so the preset can't be
// baked in; it goes alongside as a .cube LUT to apply in that program.
func (s *server) exportDNG(w http.ResponseWriter, ph *Photo, dir, stem string, p preset.Params, label string, denoise *int) {
	if ph.Kind != "x3f" {
		httpErr(w, 400, errors.New("DNG export needs an X3F file"))
		return
	}
	if goRaw && nativeRaw(ph) {
		s.exportCameraDNG(w, ph, dir, stem, p, label, denoise)
		return
	}
	if rawTool == "" {
		httpErr(w, 400, errors.New("DNG export needs x3f_extract (shipped next to Foveon Lab in the release downloads)"))
		return
	}
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	tmp, err := os.MkdirTemp(dir, ".dng-")
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	defer os.RemoveAll(tmp)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, rawTool, "-q", "-dng", "-compress", "-o", tmp, ph.path).CombinedOutput()
	produced := filepath.Join(tmp, ph.Name+".dng")
	if _, statErr := os.Stat(produced); err != nil || statErr != nil {
		httpErr(w, 500, fmt.Errorf("x3f_extract: %v %s", err, trimOutput(out)))
		return
	}
	path := filepath.Join(dir, stem+".dng")
	os.Remove(path)
	if err := os.Rename(produced, path); err != nil {
		httpErr(w, 500, err)
		return
	}
	log.Printf("exported %s (DNG, %v)", path, time.Since(start).Round(100*time.Millisecond))
	cube := filepath.Join(dir, stem+"_"+safeName(label)+".cube")
	if f, err := os.Create(cube); err == nil {
		err = render.WriteCube(f, label, p, 33)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			log.Printf("LUT next to the DNG: %v", err)
			cube = ""
		}
	} else {
		cube = ""
	}
	writeJSON(w, map[string]string{"path": path, "lut": cube})
}

// handleReveal opens Explorer on a file this app exported.
func (s *server) handleReveal(w http.ResponseWriter, r *http.Request) {
	p := filepath.Clean(r.URL.Query().Get("path"))
	ext := strings.ToLower(filepath.Ext(p))
	if filepath.Base(filepath.Dir(p)) != "foveon-lab" || (ext != ".jpg" && ext != ".tif" && ext != ".dng") {
		httpErr(w, 403, errors.New("only allowed for exported images"))
		return
	}
	if runtime.GOOS == "windows" {
		exec.Command("explorer.exe", "/select,", p).Start()
	}
	w.WriteHeader(204)
}

func (s *server) presetsPayload() any {
	s.pmu.RLock()
	defer s.pmu.RUnlock()
	return map[string]any{"presets": s.presets, "errors": s.pErrs, "dir": s.presetsDir}
}

func (s *server) handlePresets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("reload") == "1" {
			s.loadPresets()
		}
		writeJSON(w, s.presetsPayload())
	case http.MethodPost:
		var req struct {
			Name   string        `json:"name"`
			Camera string        `json:"camera"`
			Params preset.Params `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpErr(w, 400, err)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" || s.presetsDir == "" {
			httpErr(w, 400, errors.New("a preset name and a presets folder are required"))
			return
		}
		cam := req.Camera
		if cam != "SD1" && cam != "SD15" {
			cam = "ALL"
		}
		path := filepath.Join(s.presetsDir, "FoveonLab_"+cam+".xml")
		if err := preset.Append(path, req.Name, req.Params); err != nil {
			httpErr(w, 500, err)
			return
		}
		// The film, push, paper and darkroom fields have no SPP XML
		// equivalent: keep them beside the file and merge them back on load
		// (presetsidecar.go).
		if err := saveFLParams(s.presetsDir, filepath.Base(path), req.Name, req.Params); err != nil {
			httpErr(w, 500, err)
			return
		}
		log.Printf("saved preset %q to %s", req.Name, path)
		s.loadPresets()
		payload := s.presetsPayload().(map[string]any)
		payload["savedTo"] = path
		writeJSON(w, payload)
	default:
		w.WriteHeader(405)
	}
}

func (s *server) handleSPPStatus(w http.ResponseWriter, r *http.Request) {
	in, err := spp.Detect()
	if err != nil {
		writeJSON(w, map[string]any{"available": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"available": true, "running": in.Running(), "settingsDir": in.Dir})
}

// handleSPPOpen writes the preset into SPP's list, makes it SPP's current
// adjustment (both with a backup) and launches SPP on the photo.
func (s *server) handleSPPOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string        `json:"id"`
		Name    string        `json:"name"`
		Params  preset.Params `json:"params"`
		Restart bool          `json:"restart"` // close a running SPP first
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	ph := s.lookup(req.ID)
	if ph == nil || ph.Kind != "x3f" {
		httpErr(w, 400, errors.New("only X3F photos can be opened in SPP"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		httpErr(w, 400, errors.New("preset name required"))
		return
	}
	in, err := spp.Detect()
	if err != nil {
		httpErr(w, 404, err)
		return
	}
	if req.Restart {
		log.Printf("SPP: asking the running SPP to close")
		if err := in.Close(25 * time.Second); err != nil {
			httpErr(w, 409, err)
			return
		}
	}
	backup, err := in.Apply(name, req.Params)
	if errors.Is(err, spp.ErrRunning) {
		httpErr(w, 409, err)
		return
	}
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	if err := in.Launch(ph.path); err != nil {
		httpErr(w, 500, fmt.Errorf("settings written, but SPP did not start: %w", err))
		return
	}
	log.Printf("SPP: preset %q applied (backup %s), opened %s", name, backup, ph.path)
	writeJSON(w, map[string]string{"name": name, "backup": backup})
}

// agentCandidates returns the presets that fit the photo's camera.
func (s *server) agentCandidates(ph *Photo) []preset.Preset {
	s.pmu.RLock()
	defer s.pmu.RUnlock()
	var out []preset.Preset
	for _, p := range s.presets {
		if p.Camera == "" || ph.Camera == "" || p.Camera == ph.Camera {
			out = append(out, p)
		}
	}
	return out
}

// handleAgent streams an AI agent run as Server-Sent Events. Closing the
// browser's EventSource cancels the run.
func (s *server) handleAgent(w http.ResponseWriter, r *http.Request) {
	ph, err := s.photo(r)
	if err != nil {
		httpErr(w, 404, err)
		return
	}
	ai := s.ai.get()
	if ai.APIKey == "" {
		httpErr(w, 400, errors.New("no AI API key configured"))
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, 500, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	var mu sync.Mutex
	send := func(e agent.Event) {
		b, _ := json.Marshal(e)
		mu.Lock()
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
		mu.Unlock()
	}
	compOn := r.URL.Query().Get("comp") == "1"
	comp := s.compensation(ph, compOn)
	renderFn := func(p preset.Params, width int) (*image.RGBA, error) {
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		b, err := s.base(ph, width)
		if err != nil {
			return nil, err
		}
		return render.Render(b, p, comp), nil
	}
	series := 0
	if r.URL.Query().Get("series") == "1" {
		if phs := s.seriesPhotos(ph); len(phs) > 1 {
			series = len(phs)
			renderFn = func(p preset.Params, width int) (*image.RGBA, error) {
				return s.renderSheet(phs, p, compOn, width)
			}
		}
	}
	info := map[string]string{}
	for k, v := range ph.Info {
		if k != "TIME" {
			info[k] = v
		}
	}
	log.Printf("AI agent: %s (%s, %s)", ph.Name, ai.Model, ai.BaseURL)
	_, err = agent.Run(r.Context(), ai, agent.Photo{Name: ph.Name, Camera: ph.Camera, Info: info, Series: series},
		r.URL.Query().Get("dir"), s.agentCandidates(ph), renderFn, send)
	if err != nil {
		if r.Context().Err() == nil {
			log.Printf("AI agent failed: %v", err)
			send(agent.Event{Type: "error", Text: err.Error()})
		}
		return
	}
	send(agent.Event{Type: "done"})
}

// listen binds 127.0.0.1 on the first free port from start.
func listen(start int) (net.Listener, error) {
	var last error
	for p := start; p < start+20; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			return ln, nil
		}
		last = err
	}
	return nil, last
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

// calibCommand handles "calibrate" (write the SPP calibration pack) and
// "analyze" (measure the exports). It reports whether it handled argv.
func calibCommand(desk string) bool {
	if len(os.Args) >= 2 && os.Args[1] == "merge-models" {
		mergeModelsCommand()
		return true
	}
	if len(os.Args) >= 2 && os.Args[1] == "fill-research" {
		fillResearchCommand(desk)
		return true
	}
	if len(os.Args) >= 2 && os.Args[1] == "spp-check" {
		sppCheckCommand(desk)
		return true
	}
	if len(os.Args) >= 2 && os.Args[1] == "spp-decomp" {
		sppDecompCommand(desk)
		return true
	}
	if len(os.Args) >= 2 && os.Args[1] == "fit-raw" {
		rawTool = findRawTool("")
		fitRawCommand(desk)
		return true
	}
	if len(os.Args) < 2 || (os.Args[1] != "calibrate" && os.Args[1] != "analyze") {
		return false
	}
	fsFlags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := fsFlags.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder")
	photos := fsFlags.String("photos", desk, "folders with X3F photos (for suggesting source images)")
	intoSPP := fsFlags.Bool("spp", false, "also add the [CAL] presets straight into Sigma Photo Pro's preset list (SPP must be closed; its settings are backed up first)")
	auto := fsFlags.Bool("auto", false, "export the steps from SPP automatically: open -photo in SPP first, then don't touch mouse or keyboard")
	autoPhoto := fsFlags.String("photo", "", "with -auto: the photo open in SPP, e.g. SDIM0260, or the path of an X3F to open in SPP first")
	autoSteps := fsFlags.String("steps", "A", "with -auto: step groups (A, B, C, V, W, X) and/or codes, comma-separated, e.g. A or A,W or A00,A16")
	fsFlags.Parse(os.Args[2:])

	if os.Args[1] == "calibrate" && *auto {
		if *autoPhoto == "" {
			log.Fatal("-auto needs -photo (the file open in SPP, e.g. SDIM0260)")
		}
		var codes []string
		for _, want := range strings.Split(*autoSteps, ",") {
			want = strings.ToUpper(strings.TrimSpace(want))
			for _, st := range calib.Steps() {
				if st.Code == want || (len(want) == 1 && strings.HasPrefix(st.Code, want)) {
					codes = append(codes, st.Code)
				}
			}
		}
		if len(codes) == 0 {
			log.Fatalf("no calibration steps match %q", *autoSteps)
		}
		openPath := ""
		if strings.ContainsAny(*autoPhoto, `\/`) || strings.HasSuffix(strings.ToUpper(*autoPhoto), ".X3F") {
			openPath = *autoPhoto
			*autoPhoto = strings.TrimSuffix(filepath.Base(*autoPhoto), filepath.Ext(*autoPhoto))
		}
		out := filepath.Join(*dir, calib.ExportsDir)
		fmt.Printf("Exporting %d steps of %s from SPP into %s (existing files are skipped). Don't touch mouse or keyboard.\n", len(codes), *autoPhoto, out)
		if err := spp.AutoExport(*autoPhoto, openPath, codes, out, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return true
	}
	if os.Args[1] == "calibrate" {
		var paths []string
		for _, p := range scanPhotos(strings.Split(*photos, ";"), false) {
			if p.Kind == "x3f" {
				paths = append(paths, p.path)
			}
		}
		xmlPath, err := calib.WritePack(*dir, calib.Suggest(paths))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Calibration pack: %s\nInstructions: %s\nSave exports to: %s\n",
			xmlPath, filepath.Join(*dir, calib.InstructionsFile), filepath.Join(*dir, calib.ExportsDir))
		if *intoSPP {
			in, err := spp.Detect()
			if err != nil {
				log.Fatal(err)
			}
			var names []string
			var ps []preset.Params
			for _, st := range calib.Steps() {
				names, ps = append(names, calib.PresetName(st)), append(ps, st.Params)
			}
			backup, err := in.AddPresets(names, ps)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("Added %d [CAL] presets to SPP (backup of its settings: %s) - skip step 1 of the instructions.\n", len(names), backup)
		}
		return true
	}

	exportDir := filepath.Join(*dir, calib.ExportsDir)
	fmt.Printf("Analyzing %s …\n", exportDir)
	fams := map[string]string{}
	modes := map[string]bool{}
	camModels := map[string]string{}
	for _, p := range scanPhotos(strings.Split(*photos, ";"), false) {
		if p.Kind == "x3f" {
			// The analyser names a photo by its export stem without leading
			// underscores (_SDI0263_A01.tif -> SDI0263), so key it the same way.
			key := strings.TrimLeft(strings.ToUpper(strings.TrimSuffix(p.Name, filepath.Ext(p.Name))), "_")
			fams[key] = x3f.PresetFamily(p.Camera)
			modes[key] = x3f.HasColourModes(modelOf(p))
			cam := x3f.ShortModel(modelOf(p))
			if cam == "" {
				cam = calibGroup(p)
			}
			camModels[key] = cam
		}
	}
	calib.FamilyOf = func(n string) string { return fams[strings.TrimLeft(strings.ToUpper(n), "_")] }
	calib.NoModesOf = func(n string) bool { has, ok := modes[strings.TrimLeft(strings.ToUpper(n), "_")]; return ok && !has }
	calib.ModelOf = func(n string) string { return camModels[strings.TrimLeft(strings.ToUpper(n), "_")] }
	res, err := calib.Analyze(exportDir, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}
	js, _ := json.MarshalIndent(res, "", " ")
	jsonPath := filepath.Join(*dir, "calibration.json")
	reportPath := filepath.Join(*dir, "report.txt")
	if err := os.WriteFile(jsonPath, js, 0o644); err != nil {
		log.Fatal(err)
	}
	rep := res.Report()
	if err := os.WriteFile(reportPath, []byte(rep), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println(rep)
	fmt.Printf("Saved: %s, %s\n", reportPath, jsonPath)
	// The measured emulation, one model.json per photo (copy the one you
	// trust to render/model.json and rebuild to use it).
	seen := map[string]bool{}
	for _, m := range res.Measurements {
		if seen[m.Image] {
			continue
		}
		seen[m.Image] = true
		mod, err := calib.BuildModel(res, m.Image)
		if err != nil {
			fmt.Printf("No model for %s: %v\n", m.Image, err)
			continue
		}
		mj, _ := json.MarshalIndent(mod, "", " ")
		p := filepath.Join(*dir, "model_"+m.Image+".json")
		if err := os.WriteFile(p, mj, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Model: %s\n", p)
	}
	return true
}

// version is set at release time (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func main() {
	desk := desktopDir()
	if calibCommand(desk) {
		return
	}
	defPresets := filepath.Join(desk, "foveon pack")
	if _, err := os.Stat(defPresets); err != nil {
		defPresets = ""
	}
	photos := flag.String("photos", "", "folders with X3F photos (separated by ;); default: as set in the app, else Desktop + Pictures")
	presetsDir := flag.String("presets", "", "folder with SPP preset XML files; default: as set in the app, else Desktop/foveon pack")
	port := flag.Int("port", 8777, "first port to try")
	noOpen := flag.Bool("no-open", false, "do not open the browser")
	aiBase := flag.String("ai-base", "https://api.deepseek.com", "OpenAI-compatible API base URL for the AI agent")
	aiModel := flag.String("ai-model", "deepseek-flash", "vision model for the AI agent")
	aiKeyEnv := flag.String("ai-key-env", "DEEPSEEK_API_KEY", "environment variable holding the AI API key")
	rawToolPath := flag.String("x3f-extract", "", "path to x3f_extract (x3fuse-core) for SD9/SD10 raw decoding; default: next to this program or on PATH")
	// Older macOS hands a Finder-launched .app a -psn_… argument.
	args := os.Args[:1]
	for _, a := range os.Args[1:] {
		if !strings.HasPrefix(a, "-psn_") {
			args = append(args, a)
		}
	}
	os.Args = args
	flag.Parse()
	rawTool = findRawTool(*rawToolPath)
	// Raw data is decoded natively; x3f_extract (left next to the program by
	// v0.4.1 and older) is only used with FOVEON_RAW=x3f.
	if rawTool != "" && !goRaw {
		log.Printf("raw decoding via %s (FOVEON_RAW=x3f)", rawTool)
	}

	log.SetFlags(log.Ltime)

	// A second launch (double-click, Dock) just reopens the browser.
	if addr := alreadyRunning(*port); addr != "" {
		fmt.Printf("Foveon Lab is already running at %s\n", addr)
		if !*noOpen {
			openBrowser(addr)
		}
		return
	}

	// Folders: command line > what was set in the app > defaults.
	cfg, _ := loadConfig()
	loadGeometry()
	photoRoots := defaultPhotoRoots(desk)
	if len(cfg.Photos) > 0 {
		photoRoots = cfg.Photos
	}
	if *photos != "" {
		photoRoots = strings.Split(*photos, ";")
	}
	presetRoot := defPresets
	if cfg.Presets != "" {
		presetRoot = cfg.Presets
	}
	if *presetsDir != "" {
		presetRoot = *presetsDir
	}

	s := &server{
		presetsDir: presetRoot,
		byID:       map[string]*Photo{},
		sources:    newLRU(6),
		bases:      newLRU(40),
		thumbs:     newLRU(2000),
		sem:        make(chan struct{}, max(2, runtime.NumCPU()/2)),
	}
	// AI agent: what was set in the app wins over the environment variable.
	s.rawState, s.rawQueue, s.rawQueueLow = map[string]string{}, make(chan *Photo, 256), make(chan *Photo, 256)
	s.accurate.Store(cfg.Accurate == nil || *cfg.Accurate)
	go s.rawWorker()
	s.ai.cfg = agent.Config{BaseURL: *aiBase, Model: *aiModel, APIKey: os.Getenv(*aiKeyEnv), MaxSteps: 12}
	s.ai.keyEnv = *aiKeyEnv
	if s.ai.cfg.APIKey != "" {
		s.ai.source, s.ai.provider = "env", "custom"
		if strings.Contains(*aiBase, "deepseek") {
			s.ai.provider = "deepseek"
		}
	}
	if cfg.AI != nil && cfg.AI.Key != "" {
		s.ai.apply(*cfg.AI)
	}
	s.setPhotos(photoRoots, cfg.AllImages)
	s.loadPresets()

	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/photos", s.handlePhotos)
	mux.HandleFunc("/api/favorite", s.handleFavorite)
	mux.HandleFunc("/api/delete", s.handleDelete)
	mux.HandleFunc("/api/folders", s.handleFolders)
	mux.HandleFunc("/api/browse", s.handleBrowse)
	mux.HandleFunc("/api/pickfolder", s.handlePickFolder)
	mux.HandleFunc("/api/critique", s.handleCritique)
	mux.HandleFunc("/api/allimages", s.handleAllImages)
	mux.HandleFunc("/api/looks", s.handleLooks)
	mux.HandleFunc("/api/export/batch", s.handleExportBatch)
	mux.HandleFunc("/api/lut", s.handleLUT)
	mux.HandleFunc("/api/rawstate", s.handleRawState)
	mux.HandleFunc("/api/rawprefetch", s.handleRawPrefetch)
	mux.HandleFunc("/api/accurate", s.handleAccurate)
	mux.HandleFunc("/api/geometry", s.handleGeometry)
	mux.HandleFunc("/api/quit", handleQuit)
	mux.HandleFunc("/api/presets", s.handlePresets)
	mux.HandleFunc("/api/thumb", s.handleThumb)
	mux.HandleFunc("/api/render", s.handleRender)
	mux.HandleFunc("/api/gpu/base", s.handleGPUBase)
	mux.HandleFunc("/api/gpu/recipe", s.handleGPURecipe)
	mux.HandleFunc("/api/gpu/colorfilm", func(w http.ResponseWriter, r *http.Request) {
		b, ok := render.GPUColorFilmData(r.URL.Query().Get("key"))
		if !ok {
			httpErr(w, 404, errors.New("unknown colour film key"))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(b)
	})
	mux.HandleFunc("/api/export", s.handleExport)
	mux.HandleFunc("/api/films", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, render.Films()) })
	mux.HandleFunc("/api/papers", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, render.PaperList()) })
	mux.HandleFunc("/api/filters", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, render.BrandFilters()) })
	mux.HandleFunc("/api/reveal", s.handleReveal)
	mux.HandleFunc("/api/agent/status", s.handleAISettings)
	mux.HandleFunc("/api/agent", s.handleAgent)
	mux.HandleFunc("/api/spp/status", s.handleSPPStatus)
	mux.HandleFunc("/api/spp/open", s.handleSPPOpen)
	mux.HandleFunc("/api/caps", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-App", "foveon-lab") // lets a second launch find this one
		writeJSON(w, map[string]any{"rawTool": rawAvailable(), "version": version})
	})
	upd := newUpdater()
	cleanupOldUpdate(upd.inst)
	mux.HandleFunc("/api/update", upd.handleUpdate)

	ln, err := listen(*port)
	if err != nil {
		log.Fatal(err)
	}
	addr := "http://" + ln.Addr().String() + "/"
	fmt.Printf("\n  Foveon Lab %s is running at %s\n  Close this window to quit.\n\n", version, addr)
	if !*noOpen {
		openBrowser(addr)
	}
	upd.restart = func() { relaunch(ln, upd.inst) }
	go upd.loop()
	err = http.Serve(ln, sameOrigin(mux))
	if restarting.Load() {
		select {} // relaunch closed the listener and exits the process
	}
	log.Fatal(err)
}
