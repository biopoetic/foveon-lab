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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Folder string            `json:"folder"`
	Camera string            `json:"camera"`
	Kind   string            `json:"kind"` // "x3f" or "jpg"
	Info   map[string]string `json:"info"`

	path     string
	rotation int
	props    map[string]string
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

// scanPhotos finds X3F files (and SDIM*.JPG camera JPEGs) under the roots.
func scanPhotos(roots []string) []*Photo {
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
				if p != root && (strings.HasPrefix(name, ".") || name == "foveon-lab" || name == "node_modules" ||
					strings.Count(p, string(filepath.Separator))-base > 3) {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if ext != ".x3f" && !reSDIM.MatchString(d.Name()) {
				return nil
			}
			id := idOf(p)
			if seen[id] {
				return nil
			}
			seen[id] = true
			ph := &Photo{ID: id, Name: d.Name(), Folder: filepath.Base(filepath.Dir(p)), path: p, Info: map[string]string{}, Kind: "jpg"}
			if ext == ".x3f" {
				ph.Kind = "x3f"
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
		if r, err := strconv.Atoi(f.Props["ROTATION"]); err == nil && (r == 90 || r == 180 || r == 270) {
			ph.rotation = r
		}
		for _, k := range infoKeys {
			if v := strings.TrimSpace(f.Props[k]); v != "" {
				ph.Info[k] = v
			}
		}
		if t, err := strconv.ParseInt(ph.Info["TIME"], 10, 64); err == nil {
			ph.Info["TIME"] = time.Unix(t, 0).UTC().Format("2006-01-02 15:04")
		}
	}
	if ph.Camera == "" {
		ph.Camera = x3f.ShortModel(ph.Folder)
	}
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
	photos     []*Photo
	byID       map[string]*Photo
	presetsDir string

	pmu     sync.RWMutex
	presets []preset.Preset
	pByID   map[string]preset.Preset
	pErrs   []string

	sources *lru // id → *render.Linear at sourceW
	bases   *lru // id|w → *render.Base
	thumbs  *lru // id → []byte
	sem     chan struct{}

	ai agent.Config
}

func (s *server) loadPresets() {
	var ps []preset.Preset
	var errs []error
	if s.presetsDir != "" {
		ps, errs = preset.LoadDir(s.presetsDir)
	}
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
	log.Printf("loaded %d presets from %s (%d errors)", len(ps), s.presetsDir, len(errs))
}

func decodePreview(ph *Photo) (image.Image, error) {
	var data []byte
	var err error
	if ph.Kind == "x3f" {
		f, err := x3f.Open(ph.path)
		if err != nil {
			return nil, err
		}
		data, _, err = f.PreviewJPEG()
		if err != nil {
			return nil, err
		}
	} else if data, err = os.ReadFile(ph.path); err != nil {
		return nil, err
	}
	return jpeg.Decode(bytes.NewReader(data))
}

func (s *server) source(ph *Photo) (*render.Linear, error) {
	v, err := s.sources.get(ph.ID, func() (any, error) {
		img, err := decodePreview(ph)
		if err != nil {
			return nil, err
		}
		return render.FromImage(img, sourceW, ph.rotation), nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*render.Linear), nil
}

func (s *server) base(ph *Photo, w int) (*render.Base, error) {
	v, err := s.bases.get(ph.ID+"|"+strconv.Itoa(w), func() (any, error) {
		src, err := s.source(ph)
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
	if !on || ph.props == nil {
		return render.Compensation{SatFactor: 1}
	}
	return render.CompensationFor(ph.props)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, err error) {
	http.Error(w, err.Error(), code)
}

func (s *server) photo(r *http.Request) (*Photo, error) {
	ph := s.byID[r.URL.Query().Get("id")]
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
	q := r.URL.Query()
	width, _ := strconv.Atoi(q.Get("w"))
	width = max(160, min(sourceW, (width+39)/40*40))
	p, _, err := s.params(q.Get("preset"), q.Get("p"))
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	b, err := s.base(ph, width)
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
			return f.ThumbJPEG()
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	ph := s.byID[req.ID]
	if ph == nil {
		httpErr(w, 404, errors.New("unknown photo"))
		return
	}
	pj := ""
	if len(req.Params) > 0 && string(req.Params) != "null" {
		pj = string(req.Params)
	}
	p, label, err := s.params(req.Preset, pj)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	img, err := decodePreview(ph)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	out := render.Render(render.NewBase(render.FromImage(img, 0, ph.rotation)), p, s.compensation(ph, req.Comp))
	dir := filepath.Join(filepath.Dir(ph.path), "foveon-lab")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httpErr(w, 500, err)
		return
	}
	name := strings.TrimSuffix(ph.Name, filepath.Ext(ph.Name)) + "_" + safeName(label) + ".jpg"
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	err = jpeg.Encode(f, out, &jpeg.Options{Quality: 95})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	log.Printf("exported %s", path)
	writeJSON(w, map[string]string{"path": path})
}

// handleReveal opens Explorer on a file this app exported.
func (s *server) handleReveal(w http.ResponseWriter, r *http.Request) {
	p := filepath.Clean(r.URL.Query().Get("path"))
	if filepath.Base(filepath.Dir(p)) != "foveon-lab" || !strings.EqualFold(filepath.Ext(p), ".jpg") {
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
	ph := s.byID[req.ID]
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
	if s.ai.APIKey == "" {
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
	comp := s.compensation(ph, r.URL.Query().Get("comp") == "1")
	renderFn := func(p preset.Params, width int) (*image.RGBA, error) {
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		b, err := s.base(ph, width)
		if err != nil {
			return nil, err
		}
		return render.Render(b, p, comp), nil
	}
	info := map[string]string{}
	for k, v := range ph.Info {
		if k != "TIME" {
			info[k] = v
		}
	}
	log.Printf("AI agent: %s (%s, %s)", ph.Name, s.ai.Model, s.ai.BaseURL)
	_, err = agent.Run(r.Context(), s.ai, agent.Photo{Name: ph.Name, Camera: ph.Camera, Info: info},
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
	if len(os.Args) < 2 || (os.Args[1] != "calibrate" && os.Args[1] != "analyze") {
		return false
	}
	fsFlags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := fsFlags.String("dir", filepath.Join(desk, "foveon-calibration"), "calibration folder")
	photos := fsFlags.String("photos", desk, "folders with X3F photos (for suggesting source images)")
	fsFlags.Parse(os.Args[2:])

	if os.Args[1] == "calibrate" {
		var paths []string
		for _, p := range scanPhotos(strings.Split(*photos, ";")) {
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
		return true
	}

	exportDir := filepath.Join(*dir, calib.ExportsDir)
	fmt.Printf("Analyzing %s …\n", exportDir)
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
	return true
}

func main() {
	desk := desktopDir()
	if calibCommand(desk) {
		return
	}
	defPresets := filepath.Join(desk, "foveon pack")
	if _, err := os.Stat(defPresets); err != nil {
		defPresets = ""
	}
	photos := flag.String("photos", desk, "folders with X3F photos (separated by ;)")
	presetsDir := flag.String("presets", defPresets, "folder with SPP preset XML files")
	port := flag.Int("port", 8777, "first port to try")
	noOpen := flag.Bool("no-open", false, "do not open the browser")
	aiBase := flag.String("ai-base", "https://api.deepseek.com", "OpenAI-compatible API base URL for the AI agent")
	aiModel := flag.String("ai-model", "deepseek-flash", "vision model for the AI agent")
	aiKeyEnv := flag.String("ai-key-env", "DEEPSEEK_API_KEY", "environment variable holding the AI API key")
	flag.Parse()

	log.SetFlags(log.Ltime)
	start := time.Now()
	s := &server{
		presetsDir: *presetsDir,
		byID:       map[string]*Photo{},
		sources:    newLRU(6),
		bases:      newLRU(40),
		thumbs:     newLRU(2000),
		sem:        make(chan struct{}, max(2, runtime.NumCPU()/2)),
		ai:         agent.Config{BaseURL: *aiBase, Model: *aiModel, APIKey: os.Getenv(*aiKeyEnv), MaxSteps: 12},
	}
	s.photos = scanPhotos(strings.Split(*photos, ";"))
	for _, p := range s.photos {
		s.byID[p.ID] = p
	}
	log.Printf("found %d photos in %v", len(s.photos), time.Since(start).Round(time.Millisecond))
	s.loadPresets()

	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/photos", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.photos) })
	mux.HandleFunc("/api/presets", s.handlePresets)
	mux.HandleFunc("/api/thumb", s.handleThumb)
	mux.HandleFunc("/api/render", s.handleRender)
	mux.HandleFunc("/api/export", s.handleExport)
	mux.HandleFunc("/api/reveal", s.handleReveal)
	mux.HandleFunc("/api/agent/status", func(w http.ResponseWriter, r *http.Request) {
		host := s.ai.BaseURL
		if u, err := url.Parse(s.ai.BaseURL); err == nil {
			host = u.Host
		}
		writeJSON(w, map[string]any{"enabled": s.ai.APIKey != "", "model": s.ai.Model, "host": host, "keyEnv": *aiKeyEnv})
	})
	mux.HandleFunc("/api/agent", s.handleAgent)
	mux.HandleFunc("/api/spp/status", s.handleSPPStatus)
	mux.HandleFunc("/api/spp/open", s.handleSPPOpen)

	ln, err := listen(*port)
	if err != nil {
		log.Fatal(err)
	}
	addr := "http://" + ln.Addr().String() + "/"
	fmt.Printf("\n  Foveon Lab is running at %s\n  Close this window to quit.\n\n", addr)
	if !*noOpen {
		openBrowser(addr)
	}
	log.Fatal(http.Serve(ln, mux))
}
