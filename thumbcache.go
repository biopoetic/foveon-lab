package main

// A small on-disk cache for list thumbnails. The thumbnail is a pure function
// of the photo file and the thumbnail code (a 72 px list tile has no other
// input), so a cache hit returns exactly the bytes that would be generated
// again. It turns "open the folder after a restart" from a 30 s decode of a
// hundred full-size previews into a few file reads; the first run still
// decodes, and writes the result for the next one.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	thumbCacheVersion = "2"
	thumbCacheKeep    = 4000
)

func thumbCachePath(ph *Photo) (string, error) {
	st, err := os.Stat(ph.path)
	if err != nil {
		return "", err
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	k := fmt.Sprintf("%s|%d|%d|%s", ph.path, st.Size(), st.ModTime().UnixNano(), thumbCacheVersion)
	return filepath.Join(dir, "foveon-lab", "thumbs", idOf(k)+".jpg"), nil
}

// readThumbCache returns the cached thumbnail for ph, if one was written.
func readThumbCache(ph *Photo) ([]byte, bool) {
	p, err := thumbCachePath(ph)
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}

// saveThumbCache writes b atomically; a failure only costs the next decode.
func saveThumbCache(ph *Photo, b []byte) {
	p, err := thumbCachePath(ph)
	if err != nil || len(b) == 0 {
		return
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".thumb-")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Close()
	} else {
		tmp.Close()
	}
	if err != nil || os.Rename(name, p) != nil {
		os.Remove(name)
		return
	}
	trimThumbCache(dir)
}

// trimThumbCache keeps the cache bounded; thumbnails are ~15 KB each.
func trimThumbCache(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) <= thumbCacheKeep {
		return
	}
	type ent struct {
		name string
		mod  time.Time
	}
	files := make([]ent, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jpg" {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, ent{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, e := range files[thumbCacheKeep:] {
		os.Remove(filepath.Join(dir, e.name))
	}
}
