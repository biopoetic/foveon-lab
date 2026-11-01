package main

// Manual timing of the cull view on a real folder. It is skipped unless
// FOVEON_CULL_DIR names a folder with 100+ X3F files:
//
//	$env:FOVEON_CULL_DIR="C:\Users\Korisnik\Desktop\foveon-samples"
//	go test -run TestCullTiming -v .
//
// It measures the server's side of "arrows stay instant": extracting the
// camera's embedded JPEG for every photo, cold and after the LRU has the
// neighbours the page prefetches. It cannot measure the browser's decode.

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"
)

func TestCullTiming(t *testing.T) {
	dir := os.Getenv("FOVEON_CULL_DIR")
	if dir == "" {
		t.Skip("FOVEON_CULL_DIR not set")
	}
	photos := scanPhotos([]string{dir}, false)
	var x3f []*Photo
	for _, p := range photos {
		if p.Kind == "x3f" && p.Unsupported == "" {
			x3f = append(x3f, p)
		}
	}
	if len(x3f) < 100 {
		t.Fatalf("%s has %d usable X3F files, want at least 100", dir, len(x3f))
	}
	s := &server{cullImgs: newLRU(12)}
	durs := make([]time.Duration, 0, len(x3f))
	type slow struct {
		name string
		kind string
		rot  int
		d    time.Duration
	}
	var slows []slow
	for _, p := range x3f {
		start := time.Now()
		if _, err := s.cullPreview(p); err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		d := time.Since(start)
		durs = append(durs, d)
		slows = append(slows, slow{p.Name, p.Kind, p.Rotation, d})
	}
	sort.Slice(slows, func(i, j int) bool { return slows[i].d > slows[j].d })
	for i := 0; i < min(6, len(slows)); i++ {
		fmt.Printf("  slow %-32s %-4s rot %3d  %s\n", slows[i].name, slows[i].kind, slows[i].rot, slows[i].d.Round(time.Millisecond))
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	pct := func(q float64) time.Duration { return durs[min(len(durs)-1, int(q*float64(len(durs))))] }
	var sum time.Duration
	for _, d := range durs {
		sum += d
	}
	fmt.Printf("cull preview, %d X3F files in %s\n", len(x3f), dir)
	fmt.Printf("  mean %s  p50 %s  p95 %s  max %s\n", (sum / time.Duration(len(durs))).Round(time.Millisecond),
		pct(.50).Round(time.Millisecond), pct(.95).Round(time.Millisecond), durs[len(durs)-1].Round(time.Millisecond))

	// The page prefetches the next two; measure a step that is already warm
	// in the server's LRU.
	var warm []time.Duration
	for i := 0; i+3 < len(x3f); i += 3 {
		if _, err := s.cullPreview(x3f[i]); err != nil {
			t.Fatal(err)
		}
		// What the page does after showing x3f[i]: warm the next two.
		for k := 1; k <= 2; k++ {
			if _, err := s.cullPreview(x3f[i+k]); err != nil {
				t.Fatal(err)
			}
		}
		start := time.Now()
		if _, err := s.cullPreview(x3f[i+1]); err != nil {
			t.Fatal(err)
		}
		warm = append(warm, time.Since(start))
	}
	sort.Slice(warm, func(i, j int) bool { return warm[i] < warm[j] })
	var ws time.Duration
	for _, d := range warm {
		ws += d
	}
	if len(warm) > 0 {
		fmt.Printf("  a step after the prefetch (server LRU): mean %s  p95 %s\n",
			(ws / time.Duration(len(warm))).Round(time.Microsecond), warm[len(warm)*95/100].Round(time.Microsecond))
	}
}
