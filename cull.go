package main

// Cull: rate (1–5), reject (X) and clear (U) a folder of photos without
// touching a single original. The decisions live in a small JSON sidecar in
// the photo's own folder, keyed by file name, so they survive a restart and
// move with the folder when the folder (and its sidecar) is moved.
//
// A rejection is a decision, not a deletion: the photo stays on disk, stays
// in the catalogue, and can be shown again with the status filter. The star
// is rating 5 (see README/report): a star has always meant "keep", and a
// five-star frame is the cull's keeper; ratings 1–4 are the way there.

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/biopoetic/foveon-lab/render"
	"github.com/biopoetic/foveon-lab/x3f"
)

const cullSidecarFile = "FoveonLab_Cull.json"

type cullDecision struct {
	Rating int  `json:"rating"`           // 0 = unrated, 1–5 stars
	Reject bool `json:"reject,omitempty"` // hidden from the default list, never deleted
}

type cullSidecar struct {
	Version int                     `json:"version"`
	Photos  map[string]cullDecision `json:"photos"`
}

// cullMu serialises the read-modify-write of every cull sidecar. Decisions
// are small and infrequent; one lock keeps two key presses on different
// folders from losing each other.
var cullMu sync.Mutex

func cullSidecarPath(dir string) string { return filepath.Join(dir, cullSidecarFile) }

func readCullSidecar(dir string) cullSidecar {
	sc := cullSidecar{Version: 1, Photos: map[string]cullDecision{}}
	b, err := os.ReadFile(cullSidecarPath(dir))
	if err != nil {
		return sc
	}
	if err := json.Unmarshal(b, &sc); err != nil {
		// A damaged sidecar only means the ratings are not known; the photos
		// are still there and the next decision rewrites the file.
		return cullSidecar{Version: 1, Photos: map[string]cullDecision{}}
	}
	if sc.Photos == nil {
		sc.Photos = map[string]cullDecision{}
	}
	if sc.Version == 0 {
		sc.Version = 1
	}
	return sc
}

// writeCullSidecar writes the sidecar in one step (a temporary file beside it,
// then a rename), so a crash never leaves a half-written decision file.
func writeCullSidecar(dir string, sc cullSidecar) error {
	sc.Version = 1
	if sc.Photos == nil {
		sc.Photos = map[string]cullDecision{}
	}
	b, err := json.MarshalIndent(sc, "", " ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(dir, ".foveonlab-cull-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // after a successful rename this is a no-op
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, cullSidecarPath(dir))
}

// applyCullSidecars loads every folder's decisions once and puts them on the
// scanned photos. favorites is the app's older, path-keyed star list: a photo
// that has no cull decision yet keeps its old star as a five-star rating, so
// existing favourites keep working and start moving with the folder too.
func applyCullSidecars(photos []*Photo, favorites map[string]bool) {
	decided := map[string]map[string]cullDecision{}
	for _, p := range photos {
		dir := filepath.Dir(p.path)
		m, ok := decided[dir]
		if !ok {
			m = readCullSidecar(dir).Photos
			decided[dir] = m
		}
		d, ok := m[p.Name]
		if !ok {
			// Case-insensitive fallback: a folder moved from a case-sensitive
			// system may have changed only the case of a name.
			for name, v := range m {
				if strings.EqualFold(name, p.Name) {
					d, ok = v, true
					break
				}
			}
		}
		if ok {
			p.Rating, p.Reject = d.Rating, d.Reject
		} else if favorites[p.path] {
			p.Rating = 5
		}
		p.Favorite = p.Rating == 5
	}
}

// saveCullDecision records one photo's decision in its folder's sidecar and
// returns the path of that sidecar.
func saveCullDecision(photoPath string, rating int, reject bool) (string, error) {
	if rating < 0 || rating > 5 {
		return "", errors.New("rating must be 0 to 5")
	}
	dir := filepath.Dir(photoPath)
	cullMu.Lock()
	defer cullMu.Unlock()
	sc := readCullSidecar(dir)
	sc.Photos[filepath.Base(photoPath)] = cullDecision{Rating: rating, Reject: reject}
	if err := writeCullSidecar(dir, sc); err != nil {
		return "", err
	}
	return cullSidecarPath(dir), nil
}

// syncFavoriteConfig keeps the app's old path-keyed favourites list in step
// with the new five-star rating, so a photo starred here is still starred in
// an older build and a share of config.json loses nothing.
func syncFavoriteConfig(path string, on bool) error {
	return updateConfig(func(c *appConfig) bool {
		var out []string
		have := false
		for _, f := range c.Favorites {
			if f == path {
				have = true
				if !on {
					continue
				}
			}
			out = append(out, f)
		}
		if on && !have {
			out = append(out, path)
		}
		if len(out) == len(c.Favorites) && have == on {
			return false
		}
		c.Favorites = out
		return true
	})
}

// setCullState is the one place a decision is made: validate, write the
// sidecar, update the in-memory photo and mirror a five-star rating into the
// old favourites list. clear means "no rating, no reject" but still records
// the decision explicitly, so a legacy star cannot come back through it.
func (s *server) setCullState(ph *Photo, rating int, reject bool) error {
	if rating < 0 || rating > 5 {
		return errors.New("rating must be 0 to 5")
	}
	s.photoMu.Lock()
	oldRating, oldReject := ph.Rating, ph.Reject
	ph.Rating, ph.Reject = rating, reject
	ph.Favorite = rating == 5
	path := ph.path
	s.photoMu.Unlock()
	if _, err := saveCullDecision(path, rating, reject); err != nil {
		s.photoMu.Lock()
		ph.Rating, ph.Reject = oldRating, oldReject
		ph.Favorite = oldRating == 5
		s.photoMu.Unlock()
		return err
	}
	// A failed config mirror must not lose the sidecar decision: the sidecar
	// is the source of truth, the config only carries it to older builds.
	_ = syncFavoriteConfig(path, rating == 5)
	return nil
}

// handleCull records a rating or a rejection: POST {id, rating?, reject?,
// clear?}. It is deliberately separate from the delete endpoint: nothing
// here removes a file.
func (s *server) handleCull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     string `json:"id"`
		Rating *int   `json:"rating"`
		Reject *bool  `json:"reject"`
		Clear  bool   `json:"clear"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	ph := s.lookup(req.ID)
	if ph == nil {
		httpErr(w, 404, errors.New("unknown photo"))
		return
	}
	s.photoMu.RLock()
	rating, reject := ph.Rating, ph.Reject
	s.photoMu.RUnlock()
	if req.Clear {
		rating, reject = 0, false
	}
	if req.Rating != nil {
		rating = *req.Rating
	}
	if req.Reject != nil {
		reject = *req.Reject
	}
	if req.Rating == nil && req.Reject == nil && !req.Clear {
		httpErr(w, 400, errors.New("nothing to change: give rating, reject or clear"))
		return
	}
	if rating < 0 || rating > 5 {
		httpErr(w, 400, errors.New("rating must be 0 to 5"))
		return
	}
	if err := s.setCullState(ph, rating, reject); err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"id": ph.ID, "rating": rating, "reject": reject, "favorite": rating == 5})
}

// cullPreview returns a JPEG of the photo for the cull view. It is always the
// camera's own embedded JPEG when the file has one (no raw development, no
// decode, no waiting); the page turns it with the photo's rotation. Only a
// file with no embedded JPEG at all (some old bodies, ordinary images) is
// decoded and encoded, and that result is cached too.
func (s *server) cullPreview(ph *Photo) ([]byte, error) {
	v, err := s.cullImgs.get(ph.ID, func() (any, error) {
		if ph.Kind == "x3f" {
			f, err := x3f.Open(ph.path)
			if err != nil {
				return nil, errors.New("This file is damaged and cannot be shown. Nothing was changed.")
			}
			if b, _, err := f.PreviewJPEG(); err == nil {
				return b, nil
			}
			// SD9/SD10 store a small preview that is not JPEG. Encode that
			// preview; never run the raw development, whose seconds per frame
			// are not what culling needs.
			if img, _, err := f.Preview(); err == nil {
				return encodeCullJPEG(img), nil
			}
			return nil, errors.New("This X3F has no embedded preview to cull from. Open it in the editor to develop its raw data.")
		}
		if ph.Unsupported != "" {
			return nil, errors.New(ph.Unsupported)
		}
		img, err := decodePreview(ph)
		if err != nil {
			return nil, err
		}
		return encodeCullJPEG(img), nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func encodeCullJPEG(img image.Image) []byte {
	l := render.FromImage(img, 1600, 0) // the page applies Rotation
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, linearToSRGB16(l), &jpeg.Options{Quality: 85})
	return buf.Bytes()
}

func (s *server) handleCullImg(w http.ResponseWriter, r *http.Request) {
	ph, err := s.photo(r)
	if err != nil {
		httpErr(w, 404, err)
		return
	}
	b, err := s.cullPreview(ph)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Rotation", strconv.Itoa(ph.Rotation))
	w.Write(b)
}
