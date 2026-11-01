package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCullModeQueuesNoRawDecode is the unit half of the "100 cull moves, zero
// raw decodes" proof: while the cull is open queueRaw is a no-op, and a job
// already queued is forgotten rather than decoded.
func TestCullModeQueuesNoRawDecode(t *testing.T) {
	oldQueued := rawFullQueued
	rawFullQueued = map[string]bool{}
	defer func() { rawFullQueued = oldQueued }()

	s := &server{
		rawState:    map[string]string{},
		rawQueue:    make(chan *Photo, 4),
		rawQueueLow: make(chan *Photo, 4),
	}
	ph := &Photo{ID: "cullraw", Name: "cullraw.X3F", Kind: "x3f", RawDecoded: true, Accurate: true}
	s.cullActive.Store(true)
	s.queueRaw(ph, true)
	if got := len(s.rawQueue) + len(s.rawQueueLow); got != 0 {
		t.Fatalf("a raw decode was queued while the cull was open: %d", got)
	}
	if st := s.rawState[ph.ID]; st != rawNone {
		t.Fatalf("state = %q, want none", st)
	}

	// A job queued before the cull is dropped by drainRawQueues.
	s.rawState[ph.ID] = rawDecoding
	s.rawQueue <- ph
	s.drainRawQueues()
	if got := len(s.rawQueue) + len(s.rawQueueLow); got != 0 {
		t.Fatalf("drain left %d queued", got)
	}
	if st := s.rawState[ph.ID]; st != rawNone {
		t.Fatalf("dropped job state = %q, want none", st)
	}

	// Outside the cull the same call does queue one (the normal path). The
	// send is deliberately async, so wait briefly for the worker's hand-off.
	s.cullActive.Store(false)
	s.queueRaw(ph, true)
	deadline := time.Now().Add(time.Second)
	for len(s.rawQueue) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(s.rawQueue); got != 1 {
		t.Fatalf("queued %d outside the cull, want 1", got)
	}
	s.dropQueuedRaw(ph)
}

func TestHandleCullModeCountsDecodes(t *testing.T) {
	s := &server{}
	before := rawDecodeCount.Load()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/cullmode", strings.NewReader(`{"on":true}`))
	s.handleCullMode(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"on":true`) {
		t.Fatalf("POST cullmode: %d %s", w.Code, w.Body.String())
	}
	if !s.cullActive.Load() {
		t.Fatal("cull mode was not set")
	}
	w = httptest.NewRecorder()
	s.handleCullMode(w, httptest.NewRequest(http.MethodGet, "/api/cullmode", nil))
	if !strings.Contains(w.Body.String(), `"decodes":`) {
		t.Fatalf("GET cullmode has no decode count: %s", w.Body.String())
	}
	if rawDecodeCount.Load() != before {
		t.Fatal("entering the cull started a raw decode")
	}
}

// TestDamagedX3FStaysInTheList: a folder whose only .X3F cannot be read must
// show that file with a sentence, not look like an empty folder.
func TestDamagedX3FStaysInTheList(t *testing.T) {
	t.Setenv("FOVEON_META_CACHE", filepath.Join(t.TempDir(), "meta.json"))
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.X3F")
	if err := os.WriteFile(path, []byte("this is not a Sigma raw file"), 0o644); err != nil {
		t.Fatal(err)
	}
	ps := scanPhotos([]string{dir}, false)
	if len(ps) != 1 {
		t.Fatalf("got %d photos, want the damaged file to stay visible", len(ps))
	}
	if ps[0].Unsupported == "" || !strings.Contains(strings.ToLower(ps[0].Unsupported), "damaged") {
		t.Fatalf("damaged file has no plain message: %q", ps[0].Unsupported)
	}
	if ps[0].Kind != "x3f" {
		t.Fatalf("kind = %q", ps[0].Kind)
	}
}

// TestCullPreviewNeverDecodesRawOnDamagedFile: the cull's error path is a
// sentence, not a raw development.
func TestCullPreviewNeverDecodesRawOnDamagedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.X3F")
	if err := os.WriteFile(path, []byte("not an x3f"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &server{cullImgs: newLRU(2)}
	before := rawDecodeCount.Load()
	_, err := s.cullPreview(&Photo{ID: "broken", Kind: "x3f", path: path})
	if err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("error = %v, want a damaged-file sentence", err)
	}
	if rawDecodeCount.Load() != before {
		t.Fatal("cull preview started a raw decode")
	}
}
