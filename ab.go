package main

// Blind A/B: the user brings a picture of the same frame that they made
// elsewhere (a JPEG or TIFF export), Foveon Lab develops its own version,
// and the page shows the two as A and B in a random order at one size. The
// page never knows which is which until the answer is given. The reference
// is decoded and re-encoded, so its metadata never reaches the page, and no
// file name is shown. Only a local tally of the answers is kept.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/jpeg"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biopoetic/foveon-lab/render"
	"golang.org/x/image/draw"
)

const (
	abMaxSide  = 3000
	abMaxArea  = 200 << 20 // 200 megapixels is not a photo, it is a mistake
	abLifeTime = 10 * time.Minute
)

// abSession holds the two finished pictures of one comparison.
type abSession struct {
	A, B      []byte
	AIsOurs   bool
	W, H      int
	Created   time.Time
	RefPixels int64
}

var (
	abMu       sync.Mutex
	abSessions = map[string]*abSession{}
)

func abToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// handleABPrepare develops ours, prepares the reference at the same size and
// stores both under an opaque token. A frame whose shape does not match is
// refused rather than compared badly.
func (s *server) handleABPrepare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string          `json:"id"`
		Preset string          `json:"preset"`
		Params json.RawMessage `json:"params"`
		Comp   bool            `json:"comp"`
		Path   string          `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	refPath := strings.TrimSpace(req.Path)
	if refPath == "" {
		httpErr(w, 400, errorsForAB("Choose the other version of the photo first."))
		return
	}
	ph := s.lookup(req.ID)
	if ph == nil {
		httpErr(w, 404, errorsForAB("That photo is not in the list any more."))
		return
	}
	refData, err := os.ReadFile(refPath)
	if err != nil {
		httpErr(w, 400, errorsForAB("Could not open that file: "+err.Error()))
		return
	}
	ref, _, err := render.DecodeImage(refData)
	if err != nil {
		httpErr(w, 400, errorsForAB("That file is not a JPEG, TIFF or PNG picture Foveon Lab can read."))
		return
	}
	rb := ref.Bounds()
	refW, refH := rb.Dx(), rb.Dy()
	if refW < 200 || refH < 200 {
		httpErr(w, 400, errorsForAB("That picture is too small to compare."))
		return
	}
	if int64(refW)*int64(refH) > abMaxArea {
		httpErr(w, 400, errorsForAB("That picture is too large to compare."))
		return
	}

	pj := ""
	if len(req.Params) > 0 && string(req.Params) != "null" {
		pj = string(req.Params)
	}
	p, _, err := s.params(req.Preset, pj)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	mode := s.sourceMode(ph)
	if lf, ok := layersSource(ph, p); ok {
		mode = "rawlayers:" + lf
	} else if wf, ok := wbSource(ph, p); ok && mode == "raw" {
		mode = "rawwb:" + wf
	}
	src, err := s.fullGeo(ph, mode, geometryOf(ph))
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	oursW, oursH := src.W, src.H
	aspectOurs := float64(oursW) / float64(oursH)
	aspectRef := float64(refW) / float64(refH)
	if math.Abs(aspectRef-aspectOurs)/aspectOurs > 0.015 {
		httpErr(w, 409, errorsForAB("These two frames do not line up: one is wider than the other. Export the same crop from the other program and try again."))
		return
	}
	commonW := min(refW, oursW, abMaxSide)
	commonH := int(math.Round(float64(commonW) * float64(refH) / float64(refW)))
	if commonH < 100 {
		httpErr(w, 400, errorsForAB("That picture is too small to compare."))
		return
	}
	comp := s.compensation(ph, req.Comp)
	if strings.HasPrefix(mode, "rawlayers:") {
		comp = layerCompensation(comp)
	}
	comp.WBDeveloped = strings.HasPrefix(mode, "rawwb:")
	base := render.NewBase(src.Downscale(commonW))
	ours := render.Render(base, p, comp)
	if ours.Bounds().Dx() != commonW || ours.Bounds().Dy() != commonH {
		ours = abResize(ours, commonW, commonH)
	}
	refPic := abResize(ref, commonW, commonH)
	var aBuf, bBuf bytes.Buffer
	if err := jpeg.Encode(&aBuf, ours, &jpeg.Options{Quality: 95}); err != nil {
		httpErr(w, 500, err)
		return
	}
	if err := jpeg.Encode(&bBuf, refPic, &jpeg.Options{Quality: 95}); err != nil {
		httpErr(w, 500, err)
		return
	}
	oursFirst := true
	var bit [1]byte
	if _, err := rand.Read(bit[:]); err == nil {
		oursFirst = bit[0]&1 == 0
	}
	sess := &abSession{W: commonW, H: commonH, Created: time.Now(), RefPixels: int64(refW) * int64(refH)}
	if oursFirst {
		sess.A, sess.B, sess.AIsOurs = aBuf.Bytes(), bBuf.Bytes(), true
	} else {
		sess.A, sess.B, sess.AIsOurs = bBuf.Bytes(), aBuf.Bytes(), false
	}
	tok := abToken()
	abMu.Lock()
	for k, old := range abSessions {
		if time.Since(old.Created) > abLifeTime {
			delete(abSessions, k)
		}
	}
	abSessions[tok] = sess
	abMu.Unlock()
	writeJSON(w, map[string]any{
		"token": tok, "width": commonW, "height": commonH,
		"a": "/api/ab/img?token=" + tok + "&i=a",
		"b": "/api/ab/img?token=" + tok + "&i=b",
	})
}

// handleABReveal says which side was ours. It is only asked after the answer.
func (s *server) handleABReveal(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	abMu.Lock()
	sess := abSessions[tok]
	abMu.Unlock()
	if sess == nil {
		httpErr(w, 404, errorsForAB("This comparison has expired. Start it again."))
		return
	}
	ours, other := "a", "b"
	if !sess.AIsOurs {
		ours, other = "b", "a"
	}
	writeJSON(w, map[string]string{"ours": ours, "other": other})
}

// handleABImg serves one side of the comparison as a plain JPEG.
func (s *server) handleABImg(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	abMu.Lock()
	sess := abSessions[tok]
	abMu.Unlock()
	if sess == nil {
		httpErr(w, 404, errorsForAB("This comparison has expired. Start it again."))
		return
	}
	data := sess.A
	if r.URL.Query().Get("i") == "b" {
		data = sess.B
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

// abResize fits a picture to the exact comparison size.
func abResize(img image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
	return dst
}

// errorsForAB keeps the user-facing text out of an error type import.
func errorsForAB(text string) error { return &abError{text} }

type abError struct{ text string }

func (e *abError) Error() string { return e.text }
