package main

// Task 36 measurement harness: how long a raw export takes, where its stages
// go, and how a batch of twenty behaves per body group. Manual only, set
// FOVEON_T36_DIR (default: the samples folder on the Desktop) and run
// TestTask36. Nothing here is part of the app.
//
//	FOVEON_T36_DIR=... FOVEON_T36=1 go test -run TestTask36 -v -count=1 .
//
// Set FOVEON_TIMING=1 to have the production path print every stage (the
// "one" subtest).
//
// Peak memory is Go's HeapAlloc, sampled every 5 ms. CPU use is the runtime's
// /cpu/classes/total counter, so idle = 1 - CPU/(wall*GOMAXPROCS).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biopoetic/foveon-lab/preset"
)

type t36mem struct {
	mu      sync.Mutex
	peak    uint64
	peakSys uint64
	stop    chan struct{}
	done    chan struct{}
}

func t36startMem() *t36mem {
	m := &t36mem{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		var ms runtime.MemStats
		t := time.NewTicker(5 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
			}
			runtime.ReadMemStats(&ms)
			m.mu.Lock()
			if ms.HeapAlloc > m.peak {
				m.peak = ms.HeapAlloc
			}
			if ms.Sys > m.peakSys {
				m.peakSys = ms.Sys
			}
			m.mu.Unlock()
		}
	}()
	return m
}

func (m *t36mem) finish() (peak, sys uint64) {
	close(m.stop)
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peak, m.peakSys
}

func t36cpu() float64 {
	s := []metrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}}
	metrics.Read(s)
	if s[0].Value.Kind() == metrics.KindFloat64 {
		return s[0].Value.Float64()
	}
	return 0
}

func t36dir(t *testing.T) string {
	d := os.Getenv("FOVEON_T36_DIR")
	if d == "" {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, "Desktop", "foveon-samples")
	}
	if st, err := os.Stat(d); err != nil || !st.IsDir() {
		t.Skipf("no samples folder %s", d)
	}
	return d
}

func t36photos(t *testing.T, dir string) []*Photo {
	t.Helper()
	var out []*Photo
	for _, p := range scanPhotos([]string{dir}, false) {
		if p.Kind == "x3f" && p.Unsupported == "" {
			out = append(out, p)
		}
	}
	return out
}

// t36groups is the four body groups the task names. SD9/SD10 are picked out
// of the archive by their model name.
func t36groups(t *testing.T, root string) map[string][]*Photo {
	t.Helper()
	g := map[string][]*Photo{}
	add := func(group, sub string) {
		for _, p := range t36photos(t, filepath.Join(root, sub)) {
			g[group] = append(g[group], p)
		}
	}
	for _, sub := range []string{"DP1-Merrill", "DP2-Merrill", "DP3-Merrill"} {
		add("Merrill", sub)
	}
	for _, sub := range []string{"dp0-Quattro", "dp1-Quattro", "dp2-Quattro", "dp3-Quattro", "sd-Quattro", "sd-Quattro-H"} {
		add("Quattro", sub)
	}
	add("SD14", "SD14")
	if d := os.Getenv("FOVEON_T36_SD14_DIR"); d != "" {
		// Hard links to the one SD14 sample, so a 20-photo batch is twenty
		// distinct paths (the raw cache is keyed by path) without 20 copies.
		g["SD14"] = t36photos(t, d)
	}
	arch := t36photos(t, filepath.Join(root, "archive"))
	for _, p := range arch {
		m := modelOf(p)
		if strings.Contains(m, "SD9") || strings.Contains(m, "SD10") {
			g["SD9/SD10"] = append(g["SD9/SD10"], p)
		}
	}
	for k, v := range g {
		if len(v) == 0 {
			t.Fatalf("group %s has no samples", k)
		}
		sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
		t.Logf("group %-8s %3d photos", k, len(v))
	}
	return g
}

func t36server() *server {
	s := &server{sem: make(chan struct{}, max(2, runtime.NumCPU()/2)), rawState: map[string]string{}}
	s.accurate.Store(true)
	return s
}

// t36one exports one photo cold with the stage trace on (FOVEON_TIMING).
func t36one(t *testing.T, root string, groups map[string][]*Photo, out string) {
	only := os.Getenv("FOVEON_T36_GROUP")
	for _, name := range []string{"Merrill", "Quattro", "SD14", "SD9/SD10"} {
		if only != "" && only != name {
			continue
		}
		ph := groups[name][0]
		devRawMu.Lock()
		devRawOne = nil
		devRawMu.Unlock()
		runtime.GC()
		s := t36server()
		dir := filepath.Join(out, "one-"+strings.ReplaceAll(name, "/", "-"))
		os.MkdirAll(dir, 0o755)
		mem := t36startMem()
		c0 := t36cpu()
		t0 := time.Now()
		path, err := s.exportImage(ph, preset.Neutral(), "Neutral", false, "jpeg", nil, "", dir)
		wall := time.Since(t0)
		cpu := t36cpu() - c0
		peak, sys := mem.finish()
		if err != nil {
			t.Errorf("%s %s: %v", name, ph.Name, err)
			continue
		}
		fmt.Printf("T36 ONE\t%s\t%s\t%s\t%.3f\t%.2f\t%.2f\t%.0f\t%.0f\t%s\n",
			name, ph.Name, wall.Round(time.Millisecond), wall.Seconds(), cpu,
			100*cpu/(wall.Seconds()*float64(runtime.GOMAXPROCS(0))),
			float64(peak)/1e6, float64(sys)/1e6, filepath.Base(path))
	}
}

// t36batch exports twenty photos (all distinct where the group has them; the
// single SD14 sample is re-decoded) one at a time, as the app does today.
func t36batch(t *testing.T, groups map[string][]*Photo, out string) {
	only := os.Getenv("FOVEON_T36_GROUP")
	for _, name := range []string{"Merrill", "Quattro", "SD14", "SD9/SD10"} {
		if only != "" && only != name {
			continue
		}
		list := groups[name]
		s := t36server()
		dir := filepath.Join(out, "batch-"+strings.ReplaceAll(name, "/", "-"))
		os.MkdirAll(dir, 0o755)
		devRawMu.Lock()
		devRawOne = nil
		devRawMu.Unlock()
		mem := t36startMem()
		c0 := t36cpu()
		t0 := time.Now()
		var slow time.Duration
		for i := 0; i < 20; i++ {
			ph := list[i%len(list)]
			if i >= len(list) {
				// The same file again stands in for a different photo: clear
				// the one-entry raw cache so its decode is paid as it would be.
				devRawMu.Lock()
				devRawOne = nil
				devRawMu.Unlock()
			}
			it := time.Now()
			if _, err := s.exportImage(ph, preset.Neutral(), "Neutral", false, "jpeg", nil, "", dir); err != nil {
				t.Fatalf("%s %s: %v", name, ph.Name, err)
			}
			if d := time.Since(it); d > slow {
				slow = d
			}
		}
		wall := time.Since(t0)
		cpu := t36cpu() - c0
		peak, sys := mem.finish()
		fmt.Printf("T36 BATCH\t%s\t20\t%s\t%.3f\t%.2f\t%.2f\t%.0f\t%.0f\t%s\n",
			name, wall.Round(time.Millisecond), wall.Seconds(), cpu,
			100*cpu/(wall.Seconds()*float64(runtime.GOMAXPROCS(0))),
			float64(peak)/1e6, float64(sys)/1e6, slow.Round(time.Millisecond))
	}
}

// t36batchHTTP drives the real /api/export/batch handler, so it measures the
// scheduler (workers, memory gate, progress lines) and not just exportImage.
func t36batchHTTP(t *testing.T, groups map[string][]*Photo, out string) {
	only := os.Getenv("FOVEON_T36_GROUP")
	for _, name := range []string{"Merrill", "Quattro", "SD14", "SD9/SD10"} {
		if only != "" && only != name {
			continue
		}
		list := groups[name]
		ids := make([]string, 20)
		byID := map[string]*Photo{}
		for i := 0; i < 20; i++ {
			ph := list[i%len(list)]
			ids[i] = ph.ID
			byID[ph.ID] = ph
		}
		s := t36server()
		s.byID = byID
		dir := filepath.Join(out, "http-"+strings.ReplaceAll(name, "/", "-"))
		os.MkdirAll(dir, 0o755)
		body, _ := json.Marshal(map[string]any{
			"ids": ids, "format": "jpeg", "preset": "", "look": "Neutral",
			"destination": dir, "space": "",
		})
		req := httptest.NewRequest("POST", "/api/export/batch", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		mem := t36startMem()
		c0 := t36cpu()
		t0 := time.Now()
		s.handleExportBatch(rec, req)
		wall := time.Since(t0)
		cpu := t36cpu() - c0
		peak, sys := mem.finish()
		lines := 0
		done := false
		var ok, failed int
		for _, ln := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
			if ln == "" {
				continue
			}
			var m map[string]any
			if json.Unmarshal([]byte(ln), &m) != nil {
				t.Fatalf("%s: bad progress line %q", name, ln)
			}
			if m["done"] == true {
				done = true
				ok, failed = int(m["ok"].(float64)), int(m["failed"].(float64))
			} else {
				lines++
			}
		}
		fmt.Printf("T36 HTTPBATCH\t%s\t20\t%s\t%.3f\t%.2f\t%.2f\t%.0f\t%.0f\tlines=%d done=%v ok=%d failed=%d\n",
			name, wall.Round(time.Millisecond), wall.Seconds(), cpu,
			100*cpu/(wall.Seconds()*float64(runtime.GOMAXPROCS(0))),
			float64(peak)/1e6, float64(sys)/1e6, lines, done, ok, failed)
	}
}

func TestTask36(t *testing.T) {
	if os.Getenv("FOVEON_T36") == "" {
		t.Skip("set FOVEON_T36=1")
	}
	root := t36dir(t)
	out := os.Getenv("FOVEON_T36_OUT")
	if out == "" {
		out = filepath.Join("zz-out", "task36", "run")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	groups := t36groups(t, root)
	mode := os.Getenv("FOVEON_T36_MODE")
	if mode == "" || mode == "one" {
		t.Run("one", func(t *testing.T) { t36one(t, root, groups, out) })
	}
	if mode == "" || mode == "batch" {
		t.Run("batch20", func(t *testing.T) { t36batch(t, groups, out) })
	}
	if mode == "httpbatch" {
		t.Run("httpbatch20", func(t *testing.T) { t36batchHTTP(t, groups, out) })
	}
}

// t36median is for repeated timings in the report.
func t36median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[len(c)/2]
}

var _ = math.Max
var _ = t36median
