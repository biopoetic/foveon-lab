package main

// Physical memory, so a batch can size itself to what this machine can hold.
// Pure Go: on Windows GlobalMemoryStatusEx, on macOS hw.memsize; elsewhere the
// caller falls back to a fixed, conservative budget.

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/biopoetic/foveon-lab/x3f"
)

type memInfo struct {
	total, avail uint64 // bytes; 0 = unknown
}

// exportMemoryBudget is the number of bytes a batch may use for its exports.
// FOVEON_EXPORT_MEM_MB overrides it (the harness uses that to prove the cap).
// By default it is half the physical memory and also leaves 2 GB or an eighth
// of RAM free for the system, the app and whatever else is running, so a
// 16 GB machine does not swap and a machine already busy with another job is
// sized to what is actually free now.
func exportMemoryBudget() int64 {
	if v := os.Getenv("FOVEON_EXPORT_MEM_MB"); v != "" {
		if mb, err := strconv.ParseInt(v, 10, 64); err == nil && mb >= 256 {
			return mb << 20
		}
	}
	mi := systemMemory()
	if mi.total == 0 {
		return 2048 << 20
	}
	budget := int64(mi.total / 2)
	if mi.avail > 0 {
		reserve := int64(mi.total / 8)
		if reserve < 2<<30 {
			reserve = 2 << 30
		}
		if free := int64(mi.avail) - reserve; free < budget {
			budget = free
		}
	}
	if budget < 768<<20 {
		budget = 768 << 20
	}
	return budget
}

// exportMemEstimate is the peak heap an export of ph is expected to need. The
// constants are measured on the sample set (REPORT-36): a fixed decoder and
// image overhead plus a per-sensor-pixel cost dominated by the raw planes,
// the camera-linear image, denoise scratch and the developed linear image.
func exportMemEstimate(ph *Photo) int64 {
	px := int64(ph.previewW) * int64(ph.previewH)
	if ph.Kind == "x3f" {
		if f, err := x3f.Open(ph.path); err == nil {
			if im, ok := f.RawSection(); ok {
				px = int64(im.Width) * int64(im.Height)
			}
		}
	}
	if px <= 0 {
		px = 12_000_000
	}
	const overhead = int64(384 << 20)
	const perPixel = int64(224)
	return overhead + px*perPixel
}

// memGate admits a batch export only while the sum of the jobs' estimates fits
// the budget. A single job larger than the whole budget is still allowed, but
// only when nothing else is running; without that a machine too small for one
// photo could never finish a batch.
type memGate struct {
	mu    sync.Mutex
	used  int64
	limit int64
}

func newMemGate(limit int64) *memGate { return &memGate{limit: limit} }

func (g *memGate) acquire(ctx context.Context, n int64) bool {
	if n > g.limit {
		n = g.limit
	}
	for {
		g.mu.Lock()
		if g.used == 0 || g.used+n <= g.limit {
			g.used += n
			g.mu.Unlock()
			return true
		}
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-time.After(15 * time.Millisecond):
		}
	}
}

func (g *memGate) release(n int64) {
	if n > g.limit {
		n = g.limit
	}
	g.mu.Lock()
	g.used -= n
	if g.used < 0 {
		g.used = 0
	}
	g.mu.Unlock()
}

// batchWorkers is how many exports a batch runs at once. One render slot is
// kept free so selecting a photo never waits behind the batch, and more than
// three all-core exports on an eight-core machine only adds contention.
func (s *server) batchWorkers() int {
	if v := os.Getenv("FOVEON_BATCH_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	n := cap(s.sem) - 1
	if n < 1 {
		n = 1
	}
	if c := runtime.NumCPU() - 1; c < n {
		n = c
	}
	if n < 1 {
		n = 1
	}
	if n > 3 {
		n = 3
	}
	return n
}
