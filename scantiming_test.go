package main

// Manual check: with FL_SCAN_ROOTS set to a ;-separated list of folders,
// TestScanTiming reports how long the startup scan takes for them.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestScanTiming(t *testing.T) {
	roots := os.Getenv("FL_SCAN_ROOTS")
	if roots == "" {
		t.Skip("FL_SCAN_ROOTS not set")
	}
	start := time.Now()
	photos := scanPhotos(strings.Split(roots, ";"), false)
	t.Logf("%d photos in %s (last scan cache: %s)", len(photos), time.Since(start).Round(time.Millisecond), lastScanCache)
}
