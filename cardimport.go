package main

// Import from card: copy new X3F originals off a camera card into a dated
// folder in a library the user chooses once. The card is only ever read.
// A copy goes to a temporary name, its size and SHA-256 are checked against
// the source, and only then is it renamed into place. A small manifest in
// the library remembers what was copied (name, size and file date, plus the
// content hash), so an interrupted import resumes without duplicates and a
// repeated camera number is never mistaken for a photo already imported.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/biopoetic/foveon-lab/x3f"
)

const (
	cardDCIM        = "DCIM"
	cardManifest    = ".foveon-lab-imports.json"
	cardPartPrefix  = ".foveon-lab-import-"
	cardManifestVer = 1
)

// cardVolume is one mounted card the app can offer.
type cardVolume struct {
	Root   string `json:"root"`
	Name   string `json:"name"`
	Photos int    `json:"photos"`
	New    int    `json:"new"`
	Bytes  int64  `json:"bytes"`
	// Already is how many of the card's files the library manifest knows.
	Already int `json:"already"`
}

// cardFile is one X3F on the card.
type cardFile struct {
	Path string `json:"-"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Mod  int64  `json:"mod"` // Unix seconds from the card's file date
	Shot string `json:"shot"`
	Hash string `json:"hash,omitempty"`
}

// importEntry is one completed copy, kept in the library manifest.
type importEntry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Mod      int64  `json:"mod"`
	SHA256   string `json:"sha256"`
	Dest     string `json:"dest"`
	Shot     string `json:"shot,omitempty"`
	Imported string `json:"imported,omitempty"`
}

type importManifest struct {
	Version int           `json:"version"`
	Files   []importEntry `json:"files"`
}

// cardRoots lists places a mounted card may appear. It only looks for a
// DCIM folder, and never creates or changes anything.
func cardRoots() []string {
	switch runtime.GOOS {
	case "windows":
		var out []string
		for c := 'A'; c <= 'Z'; c++ {
			p := string(c) + ":\\"
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				if st, err := os.Stat(filepath.Join(p, cardDCIM)); err == nil && st.IsDir() {
					out = append(out, p)
				}
			}
		}
		return out
	case "darwin":
		entries, _ := os.ReadDir("/Volumes")
		var out []string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join("/Volumes", e.Name())
			if st, err := os.Stat(filepath.Join(p, cardDCIM)); err == nil && st.IsDir() {
				out = append(out, p)
			}
		}
		return out
	default:
		var out []string
		for _, base := range []string{"/media", "/run/media", "/mnt"} {
			entries, err := os.ReadDir(base)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				p := filepath.Join(base, e.Name())
				if st, err := os.Stat(filepath.Join(p, cardDCIM)); err == nil && st.IsDir() {
					out = append(out, p)
					continue
				}
				// /run/media/<user>/<card>
				sub, err := os.ReadDir(p)
				if err != nil {
					continue
				}
				for _, s := range sub {
					q := filepath.Join(p, s.Name())
					if st, err := os.Stat(filepath.Join(q, cardDCIM)); err == nil && st.IsDir() {
						out = append(out, q)
					}
				}
			}
		}
		return out
	}
}

// scanCard walks the card's DCIM folder for X3F files. Camera JPEGs that sit
// beside an X3F are not photographs of their own and are never listed.
func scanCard(root string) ([]cardFile, error) {
	dcim := filepath.Join(root, cardDCIM)
	st, err := os.Stat(dcim)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a camera card (no DCIM folder)", root)
	}
	var out []cardFile
	filepath.WalkDir(dcim, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dcim && strings.Count(p, string(filepath.Separator))-strings.Count(dcim, string(filepath.Separator)) > 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".x3f") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, cardFile{Path: p, Name: d.Name(), Size: fi.Size(), Mod: fi.ModTime().Unix(), Shot: shotDate(p, fi.ModTime())})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// shotDate is the photo's own capture date when the X3F header has one, so
// the library folders are named by when the picture was made, not by when it
// was copied. A damaged or unreadable header falls back to the file's date.
func shotDate(path string, mod time.Time) string {
	if f, err := x3f.Open(path); err == nil {
		if t, err := strconv.ParseInt(strings.TrimSpace(f.Props["TIME"]), 10, 64); err == nil && t > 0 {
			return time.Unix(t, 0).Format("2006-01-02")
		}
	}
	return mod.Format("2006-01-02")
}

// loadManifest reads a library's import record. A missing file is an empty
// record, not an error.
func loadManifest(library string) (*importManifest, error) {
	m := &importManifest{Version: cardManifestVer}
	data, err := os.ReadFile(filepath.Join(library, cardManifest))
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("the library's import record is damaged: %w", err)
	}
	return m, nil
}

// saveManifest writes the record through a temporary file, so a crash can
// never leave half a JSON file behind.
func saveManifest(library string, m *importManifest) error {
	if err := os.MkdirAll(library, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(library, ".imports-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, filepath.Join(library, cardManifest)); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// known reports whether the manifest already has this card file, by content
// hash when both are known, else by name, size and file date together.
// newCardFiles splits card files into ones the library already has and ones
// it does not. When a name and size match but the date differs (a card
// rewritten by a computer, say), the card file's hash settles it: the same
// picture is still the same picture.
func newCardFiles(files []cardFile, m *importManifest) (fresh []cardFile, already int, err error) {
	for _, f := range files {
		dup := false
		for _, e := range m.Files {
			if e.Name != f.Name || e.Size != f.Size {
				continue
			}
			if e.Mod == f.Mod {
				dup = true
				break
			}
			// Same name and size, different date: compare content.
			if e.SHA256 != "" {
				if f.Hash == "" {
					if f.Hash, err = hashFile(f.Path); err != nil {
						return nil, already, err
					}
				}
				if f.Hash == e.SHA256 {
					dup = true
					break
				}
			}
			// The destination still on disk with the recorded size is also
			// proof the copy completed.
			if e.Dest != "" {
				if st, err := os.Stat(e.Dest); err == nil && st.Size() == e.Size {
					dup = true
					break
				}
			}
		}
		if dup {
			already++
			continue
		}
		fresh = append(fresh, f)
	}
	return fresh, already, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ctxReader makes a long copy stop when the browser cancels the import.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// uniqueDest returns a name that does not exist yet. A photo already in the
// library is never replaced: a second different file with the same name gets
// " (2)" (or the next free number).
func uniqueDest(dir, name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	for i := 2; ; i++ {
		_, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return p
		}
		if err != nil {
			return p // let the rename report the real problem
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
}

// copyCardFile copies one X3F to destDir under a temporary name, checks the
// size and SHA-256, and only then gives it its final name. It returns the
// final path and the content hash. The source is only ever read.
func copyCardFile(ctx context.Context, src string, wantSize int64, destDir, name string) (string, string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(destDir, cardPartPrefix+"*")
	if err != nil {
		return "", "", err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		tmp.Close()
		if !done {
			os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), ctxReader{ctx, in})
	if err != nil {
		return "", "", err
	}
	if n != wantSize {
		return "", "", fmt.Errorf("copied %d of %d bytes", n, wantSize)
	}
	if err := tmp.Sync(); err != nil {
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	// Verify what is actually on disk, not just what we meant to write.
	if st, err := os.Stat(tmpName); err != nil || st.Size() != wantSize {
		if err != nil {
			return "", "", err
		}
		return "", "", fmt.Errorf("the copy is %d bytes, expected %d", st.Size(), wantSize)
	}
	if got, err := hashFile(tmpName); err != nil || got != sum {
		if err == nil {
			err = errors.New("the copy's content does not match the card")
		}
		return "", "", err
	}
	dest := uniqueDest(destDir, name)
	for i := 0; i < 5; i++ {
		err = renameNoReplace(tmpName, dest)
		if err == nil {
			done = true
			return dest, sum, nil
		}
		if _, statErr := os.Stat(dest); statErr != nil {
			return "", "", err
		}
		dest = uniqueDest(destDir, name)
	}
	return "", "", err
}

// cleanupPartials removes copies a crash left half-written. It only touches
// files this app named, inside the chosen library.
func cleanupPartials(library string) {
	matches, _ := filepath.Glob(filepath.Join(library, "*", cardPartPrefix+"*"))
	for _, p := range matches {
		os.Remove(p)
	}
}

// addLibraryRoot makes the library a photo folder, so imported photos appear
// in the normal grid at once and next time the app starts.
func (s *server) addLibraryRoot(library string) {
	s.photoMu.RLock()
	roots := append([]string(nil), s.photoRoots...)
	all := s.allImages
	s.photoMu.RUnlock()
	found := false
	for _, r := range roots {
		if samePath(r, library) || within(r, library) {
			found = true
			break
		}
	}
	if !found {
		roots = append(roots, library)
	}
	if err := updateConfig(func(c *appConfig) bool {
		c.Library = library
		c.Photos = roots
		return true
	}); err != nil {
		return
	}
	s.setPhotos(roots, all)
}

// within reports whether p is base or inside base.
func within(base, p string) bool {
	b, err1 := filepath.Abs(base)
	q, err2 := filepath.Abs(p)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(b, q)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func samePath(a, b string) bool {
	x, err1 := filepath.Abs(a)
	y, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && strings.EqualFold(filepath.Clean(x), filepath.Clean(y))
}

// defaultLibrary suggests a library folder the first time: a folder under
// Pictures on Windows and Linux, or Documents on macOS, never on the card.
func defaultLibrary() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Pictures", "Foveon Lab Library")
	}
	return filepath.Join(home, "Pictures", "Foveon Lab Library")
}

// iCloudLibrary is the iCloud Drive library used when the user turns that on.
func iCloudLibrary() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "Foveon Lab")
}

// cardSettings is what the page needs to draw the import panel.
type cardSettings struct {
	Library   string `json:"library"`
	Suggested string `json:"suggested"`
	ICloudDir string `json:"icloudDir"`
	PhotosApp bool   `json:"photosApp"`
	ICloud    bool   `json:"icloud"`
	Platform  string `json:"platform"`
	PhotosOK  bool   `json:"photosOK"`
}

func currentCardSettings() cardSettings {
	cfg, _ := loadConfig()
	lib := cfg.Library
	return cardSettings{
		Library: lib, Suggested: defaultLibrary(), ICloudDir: iCloudLibrary(),
		PhotosApp: cfg.PhotosApp, ICloud: cfg.ICloud,
		Platform: runtime.GOOS, PhotosOK: photosSupported,
	}
}

// handleCardDetect lists mounted cards and the saved import settings.
func (s *server) handleCardDetect(w http.ResponseWriter, r *http.Request) {
	cfg, _ := loadConfig()
	m := &importManifest{Version: cardManifestVer}
	if cfg.Library != "" {
		if loaded, err := loadManifest(cfg.Library); err == nil {
			m = loaded
		}
	}
	var cards []cardVolume
	for _, root := range cardRoots() {
		files, err := scanCard(root)
		if err != nil || len(files) == 0 {
			continue
		}
		fresh, already, _ := newCardFiles(files, m)
		var bytes int64
		for _, f := range fresh {
			bytes += f.Size
		}
		cards = append(cards, cardVolume{Root: root, Name: filepath.Base(root), Photos: len(files), New: len(fresh), Bytes: bytes, Already: already})
	}
	writeJSON(w, map[string]any{"cards": cards, "settings": currentCardSettings()})
}

// handleCardScan looks at one card or folder and says what is new.
func (s *server) handleCardScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root string `json:"root"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	root := strings.TrimSpace(req.Root)
	if root == "" {
		httpErr(w, 400, errors.New("choose a card or a folder"))
		return
	}
	files, err := scanCard(root)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	cfg, _ := loadConfig()
	m := &importManifest{Version: cardManifestVer}
	if cfg.Library != "" {
		var err error
		if m, err = loadManifest(cfg.Library); err != nil {
			httpErr(w, 500, err)
			return
		}
	}
	fresh, already, err := newCardFiles(files, m)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	var bytes int64
	names := make([]string, 0, min(len(fresh), 12))
	for _, f := range fresh {
		bytes += f.Size
		if len(names) < 12 {
			names = append(names, f.Name)
		}
	}
	writeJSON(w, map[string]any{
		"root": root, "name": filepath.Base(root), "total": len(files),
		"new": len(fresh), "already": already, "bytes": bytes, "names": names,
	})
}

// handleCardSettings saves the one-time setup: where the library is and
// whether Photos and iCloud are on (both off unless the user turns them on).
func (s *server) handleCardSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var req struct {
		Library   string `json:"library"`
		PhotosApp bool   `json:"photosApp"`
		ICloud    bool   `json:"icloud"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	lib := strings.TrimSpace(req.Library)
	if lib == "" && req.ICloud {
		lib = iCloudLibrary()
	}
	if lib != "" {
		if err := os.MkdirAll(lib, 0o755); err != nil {
			httpErr(w, 400, fmt.Errorf("cannot use that library folder: %w", err))
			return
		}
	}
	err := updateConfig(func(c *appConfig) bool {
		c.Library, c.PhotosApp, c.ICloud = lib, req.PhotosApp, req.ICloud
		return true
	})
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	writeJSON(w, currentCardSettings())
}

// cardImportLine is one line of the import's progress stream.
type cardImportLine struct {
	I       int    `json:"i,omitempty"`
	N       int    `json:"n,omitempty"`
	Name    string `json:"name,omitempty"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
	Start   bool   `json:"start,omitempty"`
	Done    bool   `json:"done,omitempty"`
	Copied  int    `json:"copied,omitempty"`
	Failed  int    `json:"failed,omitempty"`
	Skipped int    `json:"skipped,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Cancel  bool   `json:"cancelled,omitempty"`
	Library string `json:"library,omitempty"`
	// IDs are the grid ids of the copied files, so the page can select them.
	IDs []string `json:"ids,omitempty"`
}

// handleCardImport copies the new originals off the card, streaming one
// JSON line per photo. Nothing on the card is written, moved or deleted.
func (s *server) handleCardImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root    string `json:"root"`
		Library string `json:"library"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	root := strings.TrimSpace(req.Root)
	if root == "" {
		httpErr(w, 400, errors.New("choose a card or a folder"))
		return
	}
	cfg, _ := loadConfig()
	library := strings.TrimSpace(req.Library)
	if library == "" {
		library = cfg.Library
	}
	if library == "" {
		httpErr(w, 400, errors.New("choose where the library should live first"))
		return
	}
	if within(root, library) || within(library, root) {
		httpErr(w, 400, errors.New("the library cannot be on the card; choose a folder on this computer"))
		return
	}
	files, err := scanCard(root)
	if err != nil {
		httpErr(w, 400, err)
		return
	}
	if err := os.MkdirAll(library, 0o755); err != nil {
		httpErr(w, 400, fmt.Errorf("cannot use that library folder: %w", err))
		return
	}
	cleanupPartials(library)
	m, err := loadManifest(library)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	fresh, already, err := newCardFiles(files, m)
	if err != nil {
		httpErr(w, 500, err)
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	send := func(v any) bool {
		if enc.Encode(v) != nil {
			return false
		}
		if fl != nil {
			fl.Flush()
		}
		return true
	}
	var total int64
	for _, f := range fresh {
		total += f.Size
	}
	if !send(cardImportLine{Start: true, N: len(fresh), Bytes: total, Skipped: already, Library: library}) {
		return
	}
	copied, failed := 0, 0
	var ids []string
	for i, f := range fresh {
		if r.Context().Err() != nil {
			send(cardImportLine{Done: true, Copied: copied, Failed: failed, Skipped: already, Cancel: true, Library: library, IDs: ids})
			return
		}
		line := cardImportLine{I: i + 1, N: len(fresh), Name: f.Name}
		destDir := filepath.Join(library, f.Shot)
		dest, sum, err := copyCardFile(r.Context(), f.Path, f.Size, destDir, f.Name)
		if err != nil {
			if r.Context().Err() != nil {
				send(cardImportLine{Done: true, Copied: copied, Failed: failed, Skipped: already, Cancel: true, Library: library, IDs: ids})
				return
			}
			line.Error = err.Error()
			failed++
		} else {
			line.Path = dest
			copied++
			ids = append(ids, idOf(dest))
			m.Files = append(m.Files, importEntry{Name: f.Name, Size: f.Size, Mod: f.Mod, SHA256: sum, Dest: dest, Shot: f.Shot, Imported: time.Now().Format(time.RFC3339)})
			if err := saveManifest(library, m); err != nil {
				line.Error = "copied, but the import record could not be saved: " + err.Error()
				failed++
				copied--
			}
		}
		if !send(line) {
			return
		}
	}
	if len(ids) > 0 {
		s.addLibraryRoot(library)
	}
	_ = updateConfig(func(c *appConfig) bool {
		c.Library = library
		return true
	})
	send(cardImportLine{Done: true, Copied: copied, Failed: failed, Skipped: already, Library: library, IDs: ids})
}
