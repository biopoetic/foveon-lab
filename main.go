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
	// RawDecoded: shown from its raw data via x3f_extract (full resolution,
	// no in-camera styling) because the file has no full-size preview.
	RawDecoded bool              `json:"rawDecoded,omitempty"`
	Info       map[string]string `json:"info"`
	// Accurate: an accurate (raw-based) preview exists for this camera.
	Accurate bool `json:"accurate,omitempty"`

	path      string
	rotation  int
	props     map[string]string
	rawFailed atomic.Bool // raw decode failed once; stay on the preview
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

	// Reading X3F headers is I/O-latency bound; do it in parallel.
	ok := make([]bool, len(out))
	var wg sync.WaitGroup
	jobs := make(chan int)
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				ok[i] = loadMeta(out[i])
			}
		}()
	}
	for i := range out {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
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
			// data instead when x3f_extract is available.
			if rawTool != "" {
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
		srcs = append(srcs, preset.Source{FS: os.DirFS(s.presetsDir)})
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
			img, err := decodeRaw(ph)
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
		render.BalanceJPEG(x3f.PresetFamily(ph.Camera), l, ph.props["WB_DESC"] == "Auto")
	}
	return l
}

// sourceMode names which source a photo renders from right now: "raw" (the
// accurate, raw-based one), else "jpg" (its preview).
func (s *server) sourceMode(ph *Photo) string {
	if s.useRaw(ph) || ph.jpegBroken.Load() {
		return "raw"
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
			return rawLinear(ph, sourceW)
		}
		img, err := decodePreview(ph)
		if err != nil {
			// A damaged embedded JPEG: fall back to the raw data.
			if ph.Kind == "x3f" && rawTool != "" {
				if l, rerr := rawLinear(ph, sourceW); rerr == nil {
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

func (s *server) base(ph *Photo, w int) (*render.Base, error) {
	return s.baseAs(ph, w, s.sourceMode(ph))
}

func (s *server) baseAs(ph *Photo, w int, mode string) (*render.Base, error) {
	v, err := s.bases.get(ph.ID+"|"+mode+"|"+strconv.Itoa(w), func() (any, error) {
		src, err := s.sourceAs(ph, mode)
		if err != nil {
			return nil, err
		}
		return render.NewBase(src.Downscale(w)), nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*render.Base), nil
}

func (s *server) compensation(ph *Photo, on bool) render.Compensation {
	// A raw decode carries no in-camera styling, so there is nothing to undo.
	fam := x3f.PresetFamily(ph.Camera)
	if !on || ph.props == nil || (ph.RawDecoded && !ph.rawFailed.Load()) || s.useRaw(ph) || ph.jpegBroken.Load() {
		return render.Compensation{SatFactor: 1, Family: fam}
	}
	c := render.CompensationFor(ph.props, fam)
	c.Family = fam
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

func (s *server) handleRender(w http.ResponseWriter, r *http.Request) {
	ph, err := s.photo(r)
	if err != nil {
		httpErr(w, 404, err)
		return
	}
	if ph.Unsupported != "" {
		httpErr(w, 422, errors.New(ph.Unsupported))
		return
	}
	q := r.URL.Query()
	width, _ := strconv.Atoi(q.Get("w"))
	width = max(160, min(sourceW, (width+39)/40*40))
	p, _, err := s.params(q.Get("preset"), q.Get("p"))
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	// src=camera: the camera's own JPEG (the "Original" tile), even when
	// the accurate raw-based preview is in use.
	mode := s.sourceMode(ph)
	if q.Get("src") == "camera" && !ph.jpegBroken.Load() {
		mode = "jpg"
	}
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	b, err := s.baseAs(ph, width, mode)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	out := render.Render(b, p, s.compensation(ph, q.Get("comp") == "1"))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 88}); err != nil {
		httpErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.Write(buf.Bytes())
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
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
		s.exportDNG(w, ph, dir, stem, p, label)
		return
	}
	path, err := s.exportImage(ph, p, label, req.Comp, req.Format)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]string{"path": path})
}

// exportImage renders ph at full resolution with p and saves it as JPEG or
// 16-bit TIFF in a "foveon-lab" folder next to the photo.
func (s *server) exportImage(ph *Photo, p preset.Params, label string, comp bool, format string) (string, error) {
	dir := filepath.Join(filepath.Dir(ph.path), "foveon-lab")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	var src *render.Linear
	if s.wantRaw(ph) || ph.jpegBroken.Load() {
		// Accurate mode: export from the raw data even if the preview
		// hasn't switched yet (this waits for the decode).
		l, err := rawLinear(ph, 0)
		if err != nil {
			return "", err
		}
		s.rawMu.Lock()
		s.rawState[ph.ID] = rawReady
		s.rawMu.Unlock()
		src = l
	} else {
		img, err := decodePreview(ph)
		if err != nil {
			return "", err
		}
		src = balanceJPEG(ph, render.FromImage(img, 0, ph.rotation))
	}
	base := render.NewBase(src)
	c := s.compensation(ph, comp)
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
		err = render.WriteTIFF16(f, render.Render16(base, p, c))
	case "dng":
		make_, model := "SIGMA", ph.Camera
		if ph.props != nil {
			make_, model = ph.props["CAMMANUF"], ph.props["CAMMODEL"]
		}
		err = render.WriteRenderedDNG(f, render.Render16(base, p, c), make_, model, "Foveon Lab "+version)
	default:
		err = jpeg.Encode(f, render.Render(base, p, c), &jpeg.Options{Quality: 95})
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
func (s *server) exportDNG(w http.ResponseWriter, ph *Photo, dir, stem string, p preset.Params, label string) {
	if ph.Kind != "x3f" {
		httpErr(w, 400, errors.New("DNG export needs an X3F file"))
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
	autoSteps := fsFlags.String("steps", "A", "with -auto: step groups (A, B, C, V, W) and/or codes, comma-separated, e.g. A or A,W or A00,A16")
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
	for _, p := range scanPhotos(strings.Split(*photos, ";"), false) {
		if p.Kind == "x3f" {
			fams[strings.ToUpper(strings.TrimSuffix(p.Name, filepath.Ext(p.Name)))] = x3f.PresetFamily(p.Camera)
		}
	}
	calib.FamilyOf = func(n string) string { return fams[strings.ToUpper(n)] }
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
	if rawTool != "" {
		log.Printf("raw decoding for SD9/SD10 via %s", rawTool)
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
	mux.HandleFunc("/api/photos", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.photoList()) })
	mux.HandleFunc("/api/folders", s.handleFolders)
	mux.HandleFunc("/api/allimages", s.handleAllImages)
	mux.HandleFunc("/api/looks", s.handleLooks)
	mux.HandleFunc("/api/export/batch", s.handleExportBatch)
	mux.HandleFunc("/api/lut", s.handleLUT)
	mux.HandleFunc("/api/rawstate", s.handleRawState)
	mux.HandleFunc("/api/rawprefetch", s.handleRawPrefetch)
	mux.HandleFunc("/api/accurate", s.handleAccurate)
	mux.HandleFunc("/api/quit", handleQuit)
	mux.HandleFunc("/api/presets", s.handlePresets)
	mux.HandleFunc("/api/thumb", s.handleThumb)
	mux.HandleFunc("/api/render", s.handleRender)
	mux.HandleFunc("/api/export", s.handleExport)
	mux.HandleFunc("/api/reveal", s.handleReveal)
	mux.HandleFunc("/api/agent/status", s.handleAISettings)
	mux.HandleFunc("/api/agent", s.handleAgent)
	mux.HandleFunc("/api/spp/status", s.handleSPPStatus)
	mux.HandleFunc("/api/spp/open", s.handleSPPOpen)
	mux.HandleFunc("/api/caps", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-App", "foveon-lab") // lets a second launch find this one
		writeJSON(w, map[string]any{"rawTool": rawTool != "", "version": version})
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
