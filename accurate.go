package main

// Accurate mode: for camera families where it was measured to help (SD1
// Merrill so far, see render/rawbase.json), previews start from the raw data
// mapped onto SPP's neutral rendering instead of the camera's JPEG, which
// carries the in-camera colour mode. A raw decode takes ~10–25 s, so the
// JPEG preview shows at once and the raw one replaces it when ready
// (decoded in the background, one at a time, cached on disk).

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

const (
	rawNone     = ""
	rawDecoding = "decoding" // nothing usable yet
	rawFast     = "fast"     // the quick decode is ready; the full one may follow
	rawReady    = "ready"    // the full (denoised) decode is ready
	rawFailed   = "failed"
)

// rawFastColor is the quick preview decode: x3f_extract without its denoise,
// 7x faster (SD1 32 s -> 4.5 s). Held out vs SPP it scores dE2000 6.10
// against 5.83 for the full decode (Oct 2026), so it is shown first and
// replaced by the full decode; exports always use the full one.
const rawFastColor = "sRGB-nodenoise"

// rawFullQueued marks photos whose full decode is queued (guarded by rawMu).
var rawFullQueued = map[string]bool{}

// rawCorrectionFor is the raw→SPP mapping for ph, or nil.
func rawCorrectionFor(ph *Photo) *render.RawCorrection {
	if ph.Kind != "x3f" || ph.RawDecoded || ph.Unsupported != "" || !rawDecodable(ph.props) {
		return nil
	}
	if c := render.RawBaseFor(calibGroup(ph)); c != nil {
		return c
	}
	// At high ISO the camera JPEG is noisy and colour-shifted (Merrill high
	// ISO is pushed in processing), while the raw path is denoised by the
	// x3f package: use the raw path with the family's fitted correction even
	// where it did not beat the JPEG at base ISO.
	if goRaw && highISO(ph) {
		return render.RawBaseFitted(calibGroup(ph))
	}
	return nil
}

// highISO: ISO 800 and above.
func highISO(ph *Photo) bool {
	iso, err := strconv.Atoi(strings.TrimSpace(ph.props["ISO"]))
	return err == nil && iso >= 800
}

// rawDecodable reports whether the raw base handles the file's size mode.
// An SD1 MED file is read out with rows binned in pairs (raw 4704×1568 for
// a 3264×2176 frame); rawLinear stretches it back. Measured against SPP
// (tools/rescmp, Oct 2026): same framing, no crop, and its colour offset
// from SPP lies in the HI photos' range. LOW has no sample yet.
func rawDecodable(props map[string]string) bool {
	r := props["RESOLUTION"]
	return r == "" || r == "HI" || r == "MED"
}

func (s *server) wantRaw(ph *Photo) bool {
	return s.accurate.Load() && ph.Accurate && rawAvailable()
}

// goRaw: decode and develop raw data with the x3f package (no x3f_extract);
// FOVEON_RAW=x3f switches back to x3f_extract.
var goRaw = os.Getenv("FOVEON_RAW") != "x3f"

// rawAvailable reports whether raw-based rendering is possible at all.
func rawAvailable() bool { return goRaw || rawTool != "" }

// nativeRaw: the x3f package decodes this file's raw data (every body,
// Quattro included).
func nativeRaw(ph *Photo) bool {
	f, err := x3f.Open(ph.path)
	if err != nil {
		return false
	}
	_, ok := f.RawSection()
	return ok
}

// defaultNR is the noise reduction for previews and for exports left at
// the default: "Low". Side by side with SPP (SD1 ISO 100-6400, 2026-10-14)
// Sigma Photo Pro's own noise reduction looks better than Full; Low keeps
// the photo's grain and detail and only calms the colour noise.
const defaultNR = 0.3

// goStrength maps a decode name to the native denoiser's strength: the
// default decode defaultNR, "-nodenoise" 0, "-dnN" N/10 (NR Full: -dn10).
func goStrength(color string) float64 {
	_, opts, _ := strings.Cut(color, "-")
	switch {
	case strings.Contains(opts, "nodenoise"):
		return 0
	case strings.HasPrefix(opts, "dn"):
		if n, err := strconv.Atoi(strings.TrimPrefix(opts, "dn")); err == nil {
			return float64(max(0, min(10, n))) / 10
		}
	}
	return defaultNR
}

func cachedAs(ph *Photo, color string) bool {
	p, err := rawCachePathFor(ph, color)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

func (s *server) rawStateOf(ph *Photo) string {
	s.rawMu.Lock()
	defer s.rawMu.Unlock()
	st := s.rawState[ph.ID]
	if !goRaw && (st == rawNone || st == rawFast) {
		switch {
		case cachedAs(ph, "sRGB"):
			st = rawReady
		case cachedAs(ph, rawFastColor):
			st = rawFast
		}
		s.rawState[ph.ID] = st
	}
	return st
}

// useRaw: render ph from its corrected raw decode right now.
func (s *server) useRaw(ph *Photo) bool {
	if !s.wantRaw(ph) {
		return false
	}
	st := s.rawStateOf(ph)
	return st == rawReady || st == rawFast
}

// rawMode is the source mode of a photo shown from raw data: "raw" (full
// decode) or "rawfast" (the quick one, until the full one is ready).
func (s *server) rawMode(ph *Photo) string {
	if s.rawStateOf(ph) == rawReady {
		return "raw"
	}
	return "rawfast"
}

// requestRaw queues ph's raw decode (quick, then full) if accurate mode
// wants it; the photo on screen goes before prefetched neighbours.
func (s *server) requestRaw(ph *Photo) { s.queueRaw(ph, true) }

// queueRaw: urgent = the photo on screen (quick then full decode);
// otherwise a neighbour prefetch (quick decode only).
func (s *server) queueRaw(ph *Photo, urgent bool) {
	if !s.wantRaw(ph) {
		return
	}
	st := s.rawStateOf(ph)
	s.rawMu.Lock()
	defer s.rawMu.Unlock()
	switch {
	case st == rawReady || st == rawFailed:
		return
	case st == rawFast && (!urgent || rawFullQueued[ph.ID]):
		return
	case st == rawDecoding && (!urgent || rawFullQueued[ph.ID]):
		return
	}
	if st == rawNone {
		s.rawState[ph.ID] = rawDecoding
	}
	if urgent {
		rawFullQueued[ph.ID] = true
		go func() { s.rawQueue <- ph }()
	} else {
		go func() { s.rawQueueLow <- ph }()
	}
}

// rawWorker decodes queued photos one at a time (x3f_extract is heavy):
// the quick decode first, then, for the photo on screen, the full one.
func (s *server) rawWorker() {
	for {
		var ph *Photo
		urgent := true
		select {
		case ph = <-s.rawQueue:
		default:
			select {
			case ph = <-s.rawQueue:
			case ph = <-s.rawQueueLow:
				urgent = false
			}
		}
		if goRaw {
			// Native: one pass (seconds), straight into the source cache.
			if _, err := s.sourceAs(ph, "raw"); err != nil {
				log.Printf("accurate preview for %s: %v", ph.Name, err)
				s.rawMu.Lock()
				s.rawState[ph.ID] = rawFailed
				s.rawMu.Unlock()
				continue
			}
			s.rawMu.Lock()
			s.rawState[ph.ID] = rawReady
			delete(rawFullQueued, ph.ID)
			s.rawMu.Unlock()
			continue
		}
		if !cachedAs(ph, "sRGB") && !cachedAs(ph, rawFastColor) {
			if _, err := decodeRawAs(ph, rawFastColor); err != nil {
				log.Printf("accurate preview for %s: %v", ph.Name, err)
				s.rawMu.Lock()
				s.rawState[ph.ID] = rawFailed
				s.rawMu.Unlock()
				continue
			}
			s.rawMu.Lock()
			s.rawState[ph.ID] = rawFast
			s.rawMu.Unlock()
		}
		s.rawMu.Lock()
		full := urgent || rawFullQueued[ph.ID]
		s.rawMu.Unlock()
		if !full {
			continue
		}
		_, err := decodeRaw(ph)
		s.rawMu.Lock()
		delete(rawFullQueued, ph.ID)
		if err != nil {
			log.Printf("accurate preview (full) for %s: %v", ph.Name, err)
		} else {
			s.rawState[ph.ID] = rawReady
		}
		s.rawMu.Unlock()
	}
}

// rawLinear decodes ph's raw data (blocking), fixes the aspect of
// reduced-resolution files, rotates it and maps it onto SPP's neutral look.
func rawLinear(ph *Photo, maxW int, color string) (*render.Linear, error) {
	return rawLinearFor(ph, maxW, color, "")
}

// rawLinearFor: for an infrared look (irFilter "ir-..."), the
// Sunlight-balanced sensor layers (native decode only), with no raw base
// on top and clipped highlights going to that look's white.
func rawLinearFor(ph *Photo, maxW int, color, irFilter string) (*render.Linear, error) {
	var l *render.Linear
	irWhite := render.IRWhite(render.SensorGeneration(calibGroup(ph)), irFilter)
	if irFilter == "film" {
		// Colour film: the layers, clipped highlights to white.
		irWhite = &[3]float64{1, 1, 1}
	}
	layers := irWhite != nil
	if goRaw {
		var err error
		if l, err = goLinearFor(ph, maxW, goStrength(color), irWhite); err != nil {
			return nil, err
		}
	} else {
		img, err := decodeRawAs(ph, color)
		if err != nil {
			return nil, err
		}
		l = render.FromImage(img, maxW, 0)
	}
	if ph.previewW > 0 && ph.previewH > 0 {
		// e.g. an SD1 medium-size file decodes at 4704×1568 for a 3:2 frame
		// (stretched); a Quattro shot at 16:9 decodes 3:2 (cropped).
		wantH := l.W * ph.previewH / ph.previewW
		if d := wantH - l.H; d > l.H/30 || -d > l.H/30 {
			l = fitAspect(l, l.W, wantH)
		}
	}
	if ph.rotation != 0 {
		l = l.Rotate(ph.rotation)
	}
	if c := rawCorrectionFor(ph); c != nil && !layers {
		c.Apply(l)
	}
	return l, nil
}

// irLayers: the params ask for an infrared look and ph's raw data can give
// it the sensor layers it models.
func irLayers(ph *Photo, p preset.Params) bool {
	return strings.HasPrefix(p.Filter, "ir-") && goRaw && ph.Kind == "x3f" && ph.Unsupported == "" && rawDecodable(ph.props)
}

// layersSource says whether p renders from the sensor's own layers rather
// than the SPP look, and which: an IR look (its filter name), or a colour
// film ("film": its physics starts from the scene, white-balanced for
// daylight, not from SPP's rendering).
func layersSource(ph *Photo, p preset.Params) (string, bool) {
	if irLayers(ph, p) {
		return p.Filter, true
	}
	if render.IsColorFilm(p.Film) && goRaw && ph.Kind == "x3f" && ph.Unsupported == "" && rawDecodable(ph.props) {
		return "film", true
	}
	return "", false
}

// layerCompensation is the compensation for a render from the layers: the
// layers are the output space (no colour matrix, nothing to undo).
func layerCompensation(c render.Compensation) render.Compensation {
	id := identity33
	return render.Compensation{SatFactor: 1, Family: c.Family, CC: &id, Sensor: c.Sensor}
}

// handleRawPrefetch queues the raw decode of the given photos (the ones
// next to the selected one), so stepping to them is instant.
func (s *server) handleRawPrefetch(w http.ResponseWriter, r *http.Request) {
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if ph := s.lookup(id); ph != nil {
			s.queueRaw(ph, false)
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleRawState: GET ?id= → {"state": "", "decoding", "ready" or "failed",
// "accurate": bool} so the page can swap in the accurate preview.
func (s *server) handleRawState(w http.ResponseWriter, r *http.Request) {
	ph := s.lookup(r.URL.Query().Get("id"))
	if ph == nil {
		httpErr(w, 404, errors.New("unknown photo"))
		return
	}
	st := s.rawStateOf(ph)
	mode := "jpg"
	if s.useRaw(ph) {
		mode = s.rawMode(ph)
	}
	writeJSON(w, map[string]any{"state": st, "accurate": s.useRaw(ph), "mode": mode, "wanted": s.wantRaw(ph)})
}

// handleAccurate switches accurate mode (POST {"on": bool}); remembered.
func (s *server) handleAccurate(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpErr(w, 400, err)
			return
		}
		s.accurate.Store(req.On)
		cfg, _ := loadConfig()
		cfg.Accurate = &req.On
		if err := saveConfig(cfg); err != nil {
			httpErr(w, 500, err)
			return
		}
	}
	writeJSON(w, map[string]any{"on": s.accurate.Load(), "available": rawAvailable()})
}

// calibGroup is the group ph's learned raw base and JPEG balance belong to
// (x3f.CalibGroup of the camera model; ph.Camera already holds the family).
func calibGroup(ph *Photo) string {
	m := ph.props["CAMMODEL"]
	if m == "" {
		m = ph.props["CAMNAME"]
	}
	if m == "" {
		return ph.Camera
	}
	return x3f.CalibGroup(m)
}
