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
	"strings"

	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

const (
	rawNone     = ""
	rawDecoding = "decoding"
	rawReady    = "ready"
	rawFailed   = "failed"
)

// rawCorrectionFor is the raw→SPP mapping for ph, or nil.
func rawCorrectionFor(ph *Photo) *render.RawCorrection {
	if ph.Kind != "x3f" || ph.RawDecoded || ph.Unsupported != "" || !rawDecodable(ph.props) {
		return nil
	}
	return render.RawBaseFor(x3f.PresetFamily(ph.Camera))
}

// rawDecodable reports whether x3f_extract decodes the file correctly.
// It does not for the SD1's reduced-size modes (RESOLUTION "MED"/"LOW"):
// the frame comes out cropped, stretched and green (checked on SDIM0067
// against SPP, Oct 2026), so those photos keep the camera-JPEG base.
func rawDecodable(props map[string]string) bool {
	r := props["RESOLUTION"]
	return r == "" || r == "HI"
}

func (s *server) wantRaw(ph *Photo) bool {
	return s.accurate.Load() && ph.Accurate && rawTool != ""
}

func (s *server) rawStateOf(ph *Photo) string {
	s.rawMu.Lock()
	defer s.rawMu.Unlock()
	st := s.rawState[ph.ID]
	if st == rawNone {
		if p, err := rawCachePath(ph); err == nil {
			if _, err := os.Stat(p); err == nil {
				st = rawReady
				s.rawState[ph.ID] = st
			}
		}
	}
	return st
}

// useRaw: render ph from its corrected raw decode right now.
func (s *server) useRaw(ph *Photo) bool {
	return s.wantRaw(ph) && s.rawStateOf(ph) == rawReady
}

// requestRaw queues ph's raw decode if accurate mode wants it; the photo
// on screen goes before prefetched neighbours.
func (s *server) requestRaw(ph *Photo) { s.queueRaw(ph, true) }

func (s *server) queueRaw(ph *Photo, urgent bool) {
	if !s.wantRaw(ph) || s.rawStateOf(ph) != rawNone {
		return
	}
	s.rawMu.Lock()
	s.rawState[ph.ID] = rawDecoding
	s.rawMu.Unlock()
	if urgent {
		go func() { s.rawQueue <- ph }()
	} else {
		go func() { s.rawQueueLow <- ph }()
	}
}

// rawWorker decodes queued photos one at a time (x3f_extract is heavy).
func (s *server) rawWorker() {
	for {
		var ph *Photo
		select {
		case ph = <-s.rawQueue:
		default:
			select {
			case ph = <-s.rawQueue:
			case ph = <-s.rawQueueLow:
			}
		}
		_, err := decodeRaw(ph)
		s.rawMu.Lock()
		if err != nil {
			log.Printf("accurate preview for %s: %v", ph.Name, err)
			s.rawState[ph.ID] = rawFailed
		} else {
			s.rawState[ph.ID] = rawReady
		}
		s.rawMu.Unlock()
	}
}

// rawLinear decodes ph's raw data (blocking), fixes the aspect of
// reduced-resolution files, rotates it and maps it onto SPP's neutral look.
func rawLinear(ph *Photo, maxW int) (*render.Linear, error) {
	img, err := decodeRaw(ph)
	if err != nil {
		return nil, err
	}
	l := render.FromImage(img, maxW, 0)
	if ph.previewW > 0 && ph.previewH > 0 {
		// e.g. an SD1 medium-size file decodes at 4704×1568 for a 3:2 frame.
		wantH := l.W * ph.previewH / ph.previewW
		if d := wantH - l.H; d > l.H/30 || -d > l.H/30 {
			l = stretchTo(l, l.W, wantH)
		}
	}
	if ph.rotation != 0 {
		l = l.Rotate(ph.rotation)
	}
	if c := rawCorrectionFor(ph); c != nil {
		c.Apply(l)
	}
	return l, nil
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
	writeJSON(w, map[string]any{"state": st, "accurate": s.useRaw(ph), "wanted": s.wantRaw(ph)})
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
	writeJSON(w, map[string]any{"on": s.accurate.Load(), "available": rawTool != ""})
}
