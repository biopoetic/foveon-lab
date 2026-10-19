package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoopbackHost(t *testing.T) {
	for h, want := range map[string]bool{
		"127.0.0.1:8765": true, "localhost:8765": true, "[::1]:8765": true, "127.0.0.1": true,
		"evil.example:8765": false, "192.168.1.5:8765": false, "": false,
	} {
		if got := loopbackHost(h); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", h, got, want)
		}
	}
}

// A rebound name pointing at 127.0.0.1 looks same-origin to the browser;
// the Host header still gives it away.
func TestSameOriginRefusesRebinding(t *testing.T) {
	h := sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for host, want := range map[string]int{"127.0.0.1:8777": 200, "evil.example:8777": 403} {
		r := httptest.NewRequest("GET", "http://"+host+"/api/browse", nil)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", host, rec.Code, want)
		}
	}
}
