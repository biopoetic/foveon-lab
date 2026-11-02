package main

// Publish: make a JPEG at a social site's size from the current look, in
// sRGB, and then open the folder and the site's upload page. Nothing is sent
// anywhere: the app writes a file, opens the OS's file browser and browser,
// and the person drags the file in. Direct API upload is a later task.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	xdraw "golang.org/x/image/draw"

	"github.com/biopoetic/foveon-lab/preset"
	"github.com/biopoetic/foveon-lab/render"
)

// publishSite is one destination. Fixed sizes are exact; MaxLong caps a free
// shape's long edge; TargetShort makes the short edge a size. URL is the one
// fixed official upload page opened after writing; empty means "just the
// folder" (Web).
type publishSite struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	W           int    `json:"w,omitempty"`
	H           int    `json:"h,omitempty"`
	MaxLong     int    `json:"maxLong,omitempty"`
	TargetShort int    `json:"targetShort,omitempty"`
	Aspect      string `json:"aspect,omitempty"`
	URL         string `json:"url"`
	Note        string `json:"note"`
}

// publishSites is the whole destination list, in the order the dialog shows.
// The exact Instagram sizes are the ones in the brief; 500px's 2048 shortest
// side is from its support page; Facebook, Flickr and Web have no confirmed
// current pixel recommendation, so they use 2048 on the long edge (Flickr's
// long-standing display size and a safe Facebook upload) and the report says
// so.
var publishSites = []publishSite{
	{ID: "instagram-portrait", Label: "Instagram portrait 4:5 (1080×1350)", W: 1080, H: 1350, Aspect: "4:5",
		URL: "https://www.instagram.com/", Note: "Instagram portrait post."},
	{ID: "instagram-square", Label: "Instagram square (1080×1080)", W: 1080, H: 1080, Aspect: "1:1",
		URL: "https://www.instagram.com/", Note: "Instagram square post."},
	{ID: "instagram-landscape", Label: "Instagram landscape (1080×566)", W: 1080, H: 566, Aspect: "1.91:1",
		URL: "https://www.instagram.com/", Note: "Instagram landscape post."},
	{ID: "facebook", Label: "Facebook (long edge 2048)", MaxLong: 2048, Aspect: "photo's own",
		URL: "https://www.facebook.com/", Note: "No fixed crop; Facebook takes the photo's own shape."},
	{ID: "flickr", Label: "Flickr (long edge 2048)", MaxLong: 2048, Aspect: "photo's own",
		URL: "https://www.flickr.com/photos/upload/", Note: "Flickr accepts the photo's own shape; 2048 is its display size."},
	{ID: "500px", Label: "500px (short edge 2048)", TargetShort: 2048, Aspect: "photo's own",
		URL: "https://500px.com/upload", Note: "500px asks for at least 2048 pixels on the shortest side."},
	{ID: "web", Label: "Web (long edge 2048)", MaxLong: 2048, Aspect: "photo's own",
		URL: "", Note: "No site page; the JPEG is written and the folder opens."},
}

func publishSiteByID(id string) (publishSite, bool) {
	for _, s := range publishSites {
		if s.ID == id {
			return s, true
		}
	}
	return publishSite{}, false
}

// publishCrop is a rectangle in the rendered (upright, already user-cropped)
// image, as fractions of its width and height. The dialog only sends one
// after showing it and letting the person move it.
type publishCrop struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// cropRect validates the crop and turns it into pixels. A fixed-shape site
// refuses a photo whose shape differs unless a crop is supplied: Publish must
// never centre-crop silently.
func (site publishSite) cropRect(w, h int, c *publishCrop) (image.Rectangle, error) {
	if w <= 0 || h <= 0 {
		return image.Rectangle{}, errors.New("the render is empty")
	}
	if c == nil || (c.W == 0 && c.H == 0) {
		if site.W > 0 && math.Abs(float64(w)/float64(h)-float64(site.W)/float64(site.H)) > 0.005 {
			return image.Rectangle{}, fmt.Errorf("this photo is %d×%d; choose a crop for %s first", w, h, site.Aspect)
		}
		return image.Rect(0, 0, w, h), nil
	}
	if math.IsNaN(c.X) || math.IsNaN(c.Y) || math.IsNaN(c.W) || math.IsNaN(c.H) ||
		c.W <= 0 || c.H <= 0 || c.X < 0 || c.Y < 0 || c.X+c.W > 1.0001 || c.Y+c.H > 1.0001 {
		return image.Rectangle{}, errors.New("the crop is outside the photo")
	}
	x0, y0 := int(math.Round(c.X*float64(w))), int(math.Round(c.Y*float64(h)))
	x1, y1 := int(math.Round((c.X+c.W)*float64(w))), int(math.Round((c.Y+c.H)*float64(h)))
	r := image.Rect(x0, y0, x1, y1).Intersect(image.Rect(0, 0, w, h))
	if r.Dx() < 2 || r.Dy() < 2 {
		return image.Rectangle{}, errors.New("the crop is too small")
	}
	if site.W > 0 {
		want := float64(site.W) / float64(site.H)
		if math.Abs(float64(r.Dx())/float64(r.Dy())-want) > 0.01 {
			return image.Rectangle{}, fmt.Errorf("the crop is not %s", site.Aspect)
		}
	}
	return r, nil
}

// outputSize is the JPEG's pixel size for a cropped rectangle. Fixed sites
// are exact. Free shapes keep their own shape and are never enlarged: 2048 on
// the long edge, or 500px's 2048 on the short edge.
func (site publishSite) outputSize(w, h int) (int, int) {
	if site.W > 0 && site.H > 0 {
		return site.W, site.H
	}
	if short := min(w, h); site.TargetShort > 0 && short > site.TargetShort {
		f := float64(site.TargetShort) / float64(short)
		return max(1, int(math.Round(float64(w)*f))), max(1, int(math.Round(float64(h)*f)))
	}
	if site.MaxLong > 0 && max(w, h) > site.MaxLong {
		f := float64(site.MaxLong) / float64(max(w, h))
		return max(1, int(math.Round(float64(w)*f))), max(1, int(math.Round(float64(h)*f)))
	}
	return w, h
}

func (s *server) handlePublishSites(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, errors.New("GET only"))
		return
	}
	writeJSON(w, map[string]any{"sites": publishSites, "keepNote": "By default the JPEG carries only its sRGB profile: no EXIF, no GPS, no camera or capture data. Keep location copies the original JPEG's EXIF (including GPS) when the source is a JPEG; X3F files have no location block."})
}

// handlePublish makes one JPEG per selected photo, in order, then opens the
// folder(s) and the site's upload page once. It streams NDJSON like the batch
// export so the dialog can show progress.
func (s *server) handlePublish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs        []string                   `json:"ids"`
		Preset     string                     `json:"preset"`
		Params     json.RawMessage            `json:"params"`
		ParamsByID map[string]json.RawMessage `json:"paramsById"`
		Comp       bool                       `json:"comp"`
		Denoise    *int                       `json:"denoise"`
		Look       string                     `json:"look"`
		Site       string                     `json:"site"`
		Crops      map[string]publishCrop     `json:"crops"`
		KeepGPS    bool                       `json:"keepGps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	site, ok := publishSiteByID(req.Site)
	if !ok {
		httpErr(w, 400, errors.New("unknown publish destination "+req.Site))
		return
	}
	if len(req.IDs) == 0 {
		httpErr(w, 400, errors.New("no photos to publish"))
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
	if strings.TrimSpace(req.Look) != "" {
		label = req.Look
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	send := func(v any) {
		enc.Encode(v)
		if fl != nil {
			fl.Flush()
		}
	}
	ctx := r.Context()
	okCount, failed := 0, 0
	folders := []string{}
	seenFolder := map[string]bool{}
	files := []string{}
	for i, id := range req.IDs {
		ph := s.lookup(id)
		line := map[string]any{"n": len(req.IDs), "i": i + 1}
		if ph == nil {
			line["error"] = "unknown photo"
			failed++
			send(line)
			continue
		}
		line["name"] = ph.Name
		if ph.Unsupported != "" {
			line["error"] = ph.Unsupported
			failed++
			send(line)
			continue
		}
		pp, plabel := p, label
		if raw, ok := req.ParamsByID[id]; ok && len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &pp); err != nil {
				line["error"] = "this photo's saved edits could not be read: " + err.Error()
				failed++
				send(line)
				continue
			}
			plabel = label + " Edited"
		}
		var crop *publishCrop
		if c, ok := req.Crops[id]; ok {
			crop = &c
		}
		path, err := s.publishOne(ctx, ph, pp, plabel, site, crop, req.KeepGPS, req.Comp, req.Denoise)
		if err != nil {
			if ctx.Err() != nil {
				line["cancelled"] = true
			} else {
				line["error"] = err.Error()
				failed++
			}
			send(line)
			if ctx.Err() != nil {
				return
			}
			continue
		}
		okCount++
		files = append(files, path)
		line["path"] = path
		if d := filepath.Dir(path); !seenFolder[d] {
			seenFolder[d] = true
			folders = append(folders, d)
		}
		send(line)
	}
	done := map[string]any{"done": true, "ok": okCount, "failed": failed, "files": files, "folders": folders, "url": site.URL}
	if len(folders) > 0 {
		done["folder"] = folders[0]
	}
	if okCount > 0 {
		openFolder := s.openFolder
		if openFolder == nil {
			openFolder = openFolderOS
		}
		openURL := s.openURL
		if openURL == nil {
			openURL = openBrowserOS
		}
		for _, d := range folders {
			if err := openFolder(d); err != nil {
				done["openFolderError"] = err.Error()
				break
			}
		}
		if site.URL != "" {
			if err := openURL(site.URL); err != nil {
				done["openURLError"] = err.Error()
			}
		}
	}
	if len(files) > 0 {
		if site.URL == "" {
			done["next"] = "The JPEG is in the publish folder. Upload it wherever you like."
		} else {
			done["next"] = "Drag the JPEG into the " + siteLabelShort(site) + " page that just opened."
		}
	}
	send(done)
}

func siteLabelShort(site publishSite) string {
	switch site.ID {
	case "instagram-portrait", "instagram-square", "instagram-landscape":
		return "Instagram"
	case "facebook":
		return "Facebook"
	case "flickr":
		return "Flickr"
	case "500px":
		return "500px"
	}
	return site.Label
}

// publishOne renders the current look at full size, crops and resizes it for
// the destination, sharpens for the final size and writes a JPEG that never
// replaces an existing file.
func (s *server) publishOne(ctx context.Context, ph *Photo, p preset.Params, label string, site publishSite, crop *publishCrop, keepGPS, comp bool, denoise *int) (string, error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.sem }()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	src, p, c, _, err := s.developSource(ctx, ph, p, comp, denoise)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if os.Getenv("FOVEON_EXPORT_GC") != "0" {
		runtime.GC()
	}
	img := render.Render16(render.NewBase(render.ApplyGeometry(src, geometryOf(ph))), p, c)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	rect, err := site.cropRect(w, h, crop)
	if err != nil {
		return "", err
	}
	dw, dh := site.outputSize(rect.Dx(), rect.Dy())
	dst := image.NewRGBA64(image.Rect(0, 0, dw, dh))
	if dw == rect.Dx() && dh == rect.Dy() {
		xdraw.Draw(dst, dst.Bounds(), img, rect.Min, xdraw.Src)
	} else {
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, rect, xdraw.Src, nil)
	}
	// Output sharpening for the final size: undo part of what the downscale
	// took away. The full-size render already carries SPP's sharpening and the
	// preset's Sharpness, so the amount is small.
	scale := float64(dw) / float64(rect.Dx())
	amount := 0.35 * (1 - scale)
	if amount < 0.06 {
		amount = 0
	}
	if amount > 0.4 {
		amount = 0.4
	}
	render.OutputSharpen16(dst, float32(amount))
	cs, _ := render.ColourSpaceByID("srgb")
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, render.To8(dst), &jpeg.Options{Quality: 95}); err != nil {
		return "", err
	}
	out := render.JPEGWithICC(buf.Bytes(), cs.ICC())
	// The JPEG writer adds no EXIF at all, so GPS is absent by default. Keep
	// location is only meaningful for a JPEG original: copy its EXIF, with
	// Orientation reset to 1 because the published pixels are upright.
	if keepGPS && isJPEGPath(ph.path) {
		if exif := readJPEGEXIF(ph.path); exif != nil {
			out = insertJPEGSegment(out, 0xE1, patchEXIFOrientation(exif))
		}
	}
	dir := filepath.Join(filepath.Dir(ph.path), "publish")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", plainFileError(err, dir)
	}
	tmp, err := os.CreateTemp(dir, ".foveon-lab-publish-*")
	if err != nil {
		return "", plainFileError(err, dir)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", plainFileError(err, tmpName)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", plainFileError(err, tmpName)
	}
	stem := exportFileStem(ph.Name, label, "srgb", "jpeg") + "_" + site.ID
	final, err := commitTemp(tmpName, dir, stem, ".jpg")
	if err != nil {
		os.Remove(tmpName)
		return "", plainFileError(err, final)
	}
	return final, nil
}

func isJPEGPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return true
	}
	return false
}

// readJPEGEXIF returns the APP1 "Exif\0\0" payload of a JPEG, or nil.
func readJPEGEXIF(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			break
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 {
			break
		}
		if marker >= 0xD0 && marker <= 0xD7 {
			i += 2
			continue
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			break
		}
		seg := b[i+4 : i+2+n]
		if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return append([]byte(nil), seg...)
		}
		i += 2 + n
	}
	return nil
}

// patchEXIFOrientation sets IFD0's Orientation to 1 in place, because the
// published pixels are already upright. If the tag is absent the EXIF is
// returned unchanged.
func patchEXIFOrientation(exif []byte) []byte {
	out := append([]byte(nil), exif...)
	if len(out) < 14 || !bytes.HasPrefix(out, []byte("Exif\x00\x00")) {
		return out
	}
	t := 6
	var bo binary.ByteOrder
	switch string(out[t : t+2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return out
	}
	if bo.Uint16(out[t+2:]) != 42 {
		return out
	}
	ifd := int(bo.Uint32(out[t+4:]))
	if ifd < 0 || 6+ifd+2 > len(out) {
		return out
	}
	n := int(bo.Uint16(out[6+ifd:]))
	for i := 0; i < n; i++ {
		e := 6 + ifd + 2 + 12*i
		if e+12 > len(out) {
			return out
		}
		if bo.Uint16(out[e:]) != 0x0112 {
			continue
		}
		if bo.Uint16(out[e+2:]) == 3 && bo.Uint32(out[e+4:]) >= 1 {
			bo.PutUint16(out[e+8:], 1)
		}
		return out
	}
	return out
}

// insertJPEGSegment inserts one APPn segment after SOI, before the ICC and
// image data. A segment too large for JPEG's 16-bit length is dropped.
func insertJPEGSegment(jpg []byte, marker byte, payload []byte) []byte {
	if len(jpg) < 2 || jpg[0] != 0xFF || jpg[1] != 0xD8 || len(payload) > 65533 {
		return jpg
	}
	out := make([]byte, 0, len(jpg)+len(payload)+4)
	out = append(out, 0xFF, 0xD8, 0xFF, marker, 0, 0)
	binary.BigEndian.PutUint16(out[4:], uint16(len(payload)+2))
	out = append(out, payload...)
	out = append(out, jpg[2:]...)
	return out
}

// openFolderOS shows dir in the platform's file browser.
func openFolderOS(dir string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer.exe", dir).Start()
	case "darwin":
		return exec.Command("open", dir).Start()
	default:
		return exec.Command("xdg-open", dir).Start()
	}
}

// openBrowserOS opens url in the platform's default browser.
func openBrowserOS(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
