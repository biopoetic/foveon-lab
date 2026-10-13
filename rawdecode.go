package main

import (
	"context"
	"fmt"
	"image"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"golang.org/x/image/tiff"
)

// rawTool is the x3f_extract (x3fuse-core) binary used to decode raw data
// for bodies without a full-size preview (SD9/SD10); "" when unavailable.
var rawTool string

// findRawTool looks for x3f_extract: the explicit path, next to this
// executable, in third_party/x3fuse-core/bin (development checkout), then
// on PATH.
func findRawTool(explicit string) string {
	if explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit
		}
		log.Printf("x3f_extract not found at %s", explicit)
		return ""
	}
	name := "x3f_extract"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe), filepath.Join(filepath.Dir(exe), "third_party", "x3fuse-core", "bin"))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "third_party", "x3fuse-core", "bin"))
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// rawCachePath is where the decoded TIFF of a file is kept; it changes when
// the X3F does, so edits or replacements never serve a stale decode.
func rawCachePath(ph *Photo) (string, error) { return rawCachePathFor(ph, "sRGB") }

// rawCachePathFor is the cache file of a decode in colour mode color.
func rawCachePathFor(ph *Photo, color string) (string, error) {
	st, err := os.Stat(ph.path)
	if err != nil {
		return "", err
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	k := fmt.Sprintf("%s|%d|%d", ph.path, st.Size(), st.ModTime().UnixNano())
	if color != "sRGB" {
		k += "|" + color
	}
	key := idOf(k)
	return filepath.Join(dir, "foveon-lab", "raw", key+".tif"), nil
}

// decodeRaw converts the X3F's raw data to a full-resolution sRGB image via
// x3f_extract, caching the TIFF on disk.
func decodeRaw(ph *Photo) (image.Image, error) { return decodeRawAs(ph, "sRGB") }

// decodeRawAs decodes in x3f_extract colour mode color ("sRGB", or "none"
// for linear camera RGB).
func decodeRawAs(ph *Photo, color string) (image.Image, error) {
	cached, err := rawCachePathFor(ph, color)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(cached); err != nil {
		if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
			return nil, err
		}
		tmp, err := os.MkdirTemp(filepath.Dir(cached), "conv-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, rawTool, "-q", "-tiff", "-color", color, "-o", tmp, ph.path).CombinedOutput()
		produced := filepath.Join(tmp, filepath.Base(ph.path)+".tif")
		if _, statErr := os.Stat(produced); err != nil || statErr != nil {
			return nil, fmt.Errorf("x3f_extract: %v %s", err, trimOutput(out))
		}
		if err := os.Rename(produced, cached); err != nil {
			return nil, err
		}
		trimRawCache(filepath.Dir(cached), 40)
		log.Printf("raw-decoded %s in %v", ph.Name, time.Since(start).Round(100*time.Millisecond))
	}
	f, err := os.Open(cached)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return tiff.Decode(f)
}

// trimRawCache keeps the newest keep decodes (each SD1 decode is ~90 MB).
func trimRawCache(dir string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type f struct {
		p string
		t time.Time
	}
	var fs []f
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".tif" {
			continue
		}
		if info, err := e.Info(); err == nil {
			fs = append(fs, f{filepath.Join(dir, e.Name()), info.ModTime()})
		}
	}
	if len(fs) <= keep {
		return
	}
	sort.Slice(fs, func(a, b int) bool { return fs[a].t.After(fs[b].t) })
	for _, x := range fs[keep:] {
		os.Remove(x.p)
	}
}

func trimOutput(b []byte) string {
	if len(b) > 300 {
		b = b[len(b)-300:]
	}
	return string(b)
}
