package main

// A cache of X3F header facts, so a start does not have to open every file
// again. Reading one header is cheap in CPU but each file costs a random
// read: on a cold disk 844 photos took 41.6 s to scan and 0.43 s with the
// files already in the OS cache, so the wait is disk latency, not decoding.
// The entry is keyed by path and validated by size and modification time, so
// a moved, edited or replaced file is read again.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/biopoetic/foveon-lab/x3f"
)

type metaEntry struct {
	Size        int64             `json:"size"`
	Mod         int64             `json:"mod"`
	Props       map[string]string `json:"props,omitempty"`
	Camera      string            `json:"camera,omitempty"`
	Rotation    int               `json:"rotation,omitempty"`
	PreviewW    int               `json:"previewW,omitempty"`
	PreviewH    int               `json:"previewH,omitempty"`
	Info        map[string]string `json:"info,omitempty"`
	Unsupported string            `json:"unsupported,omitempty"`
	RawDecoded  bool              `json:"rawDecoded,omitempty"`
	LowRes      bool              `json:"lowRes,omitempty"`
}

type metaCache struct {
	mu      sync.Mutex
	entries map[string]metaEntry
	path    string
	dirty   bool
	hits    int
	misses  int
}

func metaCachePath() string {
	if p := os.Getenv("FOVEON_META_CACHE"); p != "" {
		return p
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "foveon-lab", "photo-meta.json")
}

// loadMetaCache reads the cache, or returns an empty one when there is none
// (a first run, or a cache from another version).
func loadMetaCache() *metaCache {
	c := &metaCache{entries: map[string]metaEntry{}, path: metaCachePath()}
	b, err := os.ReadFile(c.path)
	if err != nil {
		return c
	}
	var file struct {
		Entries map[string]metaEntry `json:"entries"`
	}
	if json.Unmarshal(b, &file) == nil && file.Entries != nil {
		c.entries = file.Entries
	}
	return c
}

// lastScanCache reports the last scan's cache hits and misses, for the
// timing test.
var lastScanCache string

// statKey is the validation key: a file whose size or modification time
// changed is read afresh.
func statKey(st os.FileInfo) (int64, int64) { return st.Size(), st.ModTime().UnixNano() }

// get returns the cached entry for ph when it is still valid.
func (c *metaCache) get(ph *Photo, size, mod int64) (metaEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[ph.path]
	if !ok || e.Size != size || e.Mod != mod {
		c.misses++
		return metaEntry{}, false
	}
	c.hits++
	return e, true
}

// put records ph's facts after they have been read from the file.
func (c *metaCache) put(ph *Photo, size, mod int64) {
	e := metaEntry{
		Size: size, Mod: mod,
		Props: ph.props, Camera: ph.Camera, Rotation: ph.rotation,
		PreviewW: ph.previewW, PreviewH: ph.previewH, Info: ph.Info,
		Unsupported: ph.Unsupported, RawDecoded: ph.RawDecoded, LowRes: ph.LowRes,
	}
	c.mu.Lock()
	c.entries[ph.path] = e
	c.dirty = true
	c.mu.Unlock()
}

// save writes the cache if anything changed. Best effort: a failure only
// costs the next start its speed.
func (c *metaCache) save() {
	c.mu.Lock()
	dirty := c.dirty
	entries := c.entries
	c.mu.Unlock()
	if !dirty {
		return
	}
	b, err := json.Marshal(struct {
		When    time.Time            `json:"when"`
		Entries map[string]metaEntry `json:"entries"`
	}{time.Now(), entries})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	os.Rename(tmp, c.path)
}

// applyMeta fills ph from a cached entry, exactly as loadMeta would.
func applyMeta(ph *Photo, e metaEntry) {
	ph.props, ph.Camera, ph.rotation = e.Props, e.Camera, e.Rotation
	ph.previewW, ph.previewH = e.PreviewW, e.PreviewH
	ph.Info = e.Info
	if ph.Info == nil {
		ph.Info = map[string]string{}
	}
	ph.Unsupported, ph.RawDecoded, ph.LowRes = e.Unsupported, e.RawDecoded, e.LowRes
}

// loadMetaCached is loadMeta with the disk read skipped when nothing about
// the file changed.
func loadMetaCached(ph *Photo, c *metaCache) bool {
	st, err := os.Stat(ph.path)
	if err != nil {
		return false
	}
	size, mod := statKey(st)
	if e, ok := c.get(ph, size, mod); ok {
		applyMeta(ph, e)
		if ph.Camera == "" {
			ph.Camera = x3f.ShortModel(ph.Folder)
		}
		ph.Accurate = rawCorrectionFor(ph) != nil
		return true
	}
	if !loadMeta(ph) {
		return false
	}
	c.put(ph, size, mod)
	return true
}
