package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAISettingsSaveAndRemove(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)         // Windows
	t.Setenv("XDG_CONFIG_HOME", dir) // Linux
	t.Setenv("HOME", dir)            // macOS
	var gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`))
	}))
	defer api.Close()

	s := &server{}
	h := sameOrigin(http.HandlerFunc(s.handleAISettings))
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/agent/status", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := post(`{"provider":"custom","baseURL":"` + api.URL + `","model":"m1","key":"sk-secret-123456"}`)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if gotAuth != "Bearer sk-secret-123456" {
		t.Fatalf("test request auth = %q", gotAuth)
	}
	if strings.Contains(w.Body.String(), "sk-secret") {
		t.Fatal("status leaks the key")
	}
	var st map[string]any
	json.Unmarshal(w.Body.Bytes(), &st)
	if st["enabled"] != true || st["keyHint"] != "…3456" || st["source"] != "app" {
		t.Fatalf("status %v", st)
	}
	if s.ai.get().APIKey != "sk-secret-123456" {
		t.Fatal("key not live")
	}
	if c, _ := loadConfig(); c.AI == nil || c.AI.Key != "sk-secret-123456" {
		t.Fatalf("not saved: %+v", c.AI)
	}
	// Changing only the model keeps the saved key.
	if w := post(`{"provider":"custom","baseURL":"` + api.URL + `","model":"m2"}`); w.Code != 200 || s.ai.get().Model != "m2" || s.ai.get().APIKey != "sk-secret-123456" {
		t.Fatalf("model change: %d %s", w.Code, w.Body)
	}
	// Plain http to a remote host is refused.
	if w := post(`{"provider":"custom","baseURL":"http://example.com/v1","model":"m","key":"k"}`); w.Code != 400 {
		t.Fatalf("http base accepted: %d", w.Code)
	}
	if w := post(`{"remove":true}`); w.Code != 200 || s.ai.get().APIKey != "" {
		t.Fatalf("remove: %d", w.Code)
	}
	if c, _ := loadConfig(); c.AI != nil {
		t.Fatal("key still in config")
	}
	if _, err := os.Stat(configPath()); err != nil {
		t.Fatal(err)
	}
}

func TestSameOriginRefusesOtherSites(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := sameOrigin(ok)
	cases := []struct {
		method, ct, origin, site string
		want                     int
	}{
		{"POST", "application/json", "", "", 200},
		{"POST", "application/json", "http://example.com", "", 403},
		{"POST", "text/plain", "", "", 415},
		{"GET", "", "", "cross-site", 403},
		{"GET", "", "", "same-origin", 200},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://127.0.0.1:8777/api/x", strings.NewReader("{}"))
		if c.ct != "" {
			r.Header.Set("Content-Type", c.ct)
		}
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.site != "" {
			r.Header.Set("Sec-Fetch-Site", c.site)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%+v: got %d", c, w.Code)
		}
	}
}
