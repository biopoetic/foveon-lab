//go:build windows

package spp

import "testing"

// TestWindowsOfRunningSPP only LISTS windows of a running SPP (it never
// closes anything); it skips when SPP is not open.
func TestWindowsOfRunningSPP(t *testing.T) {
	pids := sppPIDs()
	if len(pids) == 0 {
		t.Skip("SPP not running")
	}
	ws := windowsOf(pids)
	if len(ws) == 0 {
		t.Fatalf("SPP running (pids %v) but no visible windows found", pids)
	}
	for _, w := range ws {
		t.Logf("window %#x dialog=%v", w.h, w.dialog)
	}
}
