package agent

import (
	"context"
	"image"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCritique checks the request the critique sends (the photo as an image,
// the note, no tools), that the JSON block is parsed out of the reply, and
// that the visible text no longer contains it.
func TestCritique(t *testing.T) {
	reply := "## Score\n7/10 — the light carries it.\n\n## What to improve\n- Crop the left edge.\n\n" +
		"```json\n{\"settings\":{\"exposure\":0.2,\"highlight\":-0.4}}\n```"
	f := &fakeModel{script: []Message{{Role: "assistant", Content: reply}}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg := Config{BaseURL: srv.URL, Model: "m", APIKey: "k", HTTP: srv.Client()}

	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	text, settings, err := Critique(context.Background(), cfg, img, "shot at dusk", map[string]float64{"exposure": 0})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "```") || strings.Contains(text, "\"settings\"") {
		t.Errorf("visible text still contains the JSON block: %q", text)
	}
	if !strings.Contains(text, "7/10") {
		t.Errorf("text lost the critique: %q", text)
	}
	if len(settings) != 2 || settings["exposure"] != 0.2 || settings["highlight"] != -0.4 {
		t.Errorf("settings %v, want exposure 0.2 and highlight -0.4", settings)
	}
	if len(f.requests) != 1 {
		t.Fatalf("%d requests, want 1", len(f.requests))
	}
	req := f.requests[0]
	if len(req.Tools) != 0 {
		t.Errorf("critique sent %d tools, want none", len(req.Tools))
	}
	raw := f.raw[0]
	for _, want := range []string{"data:image/jpeg;base64,", "shot at dusk", "\"image_url\"", "current settings"} {
		if !strings.Contains(raw, want) {
			t.Errorf("request does not contain %q", want)
		}
	}
}

// A model that answers with no text is an error, not an empty critique.
func TestCritiqueNoText(t *testing.T) {
	f := &fakeModel{script: []Message{{Role: "assistant"}}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg := Config{BaseURL: srv.URL, Model: "m", APIKey: "k", HTTP: srv.Client()}
	if _, _, err := Critique(context.Background(), cfg, image.NewRGBA(image.Rect(0, 0, 8, 8)), "", nil); err == nil {
		t.Error("want an error for an empty answer")
	}
}

// The reply arrives as a string, as parts, or as JSON-decoded parts; all
// three must read as text.
func TestMessageText(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", "hello"},
		{"parts", []Part{{Type: "text", Text: "a"}, {Type: "image_url"}, {Type: "text", Text: "b"}}, "a\nb"},
		{"json", []any{map[string]any{"type": "text", "text": "x"}}, "x"},
		{"empty", nil, ""},
	}
	for _, c := range cases {
		if got := messageText(Message{Content: c.in}); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// splitSettings removes the block whether or not it parses, and keeps the
// last block when the model writes more than one.
func TestSplitSettings(t *testing.T) {
	if text, s := splitSettings("no block here"); s != nil || text != "no block here" {
		t.Errorf("plain text: %q %v", text, s)
	}
	text, s := splitSettings("a\n```json\n{\"settings\":{\"exposure\":0.1}}\n```\nb")
	if text != "a\n\nb" || s["exposure"] != 0.1 {
		t.Errorf("block: %q %v", text, s)
	}
	text, s = splitSettings("a\n```json\n{broken\n```")
	if s != nil || strings.Contains(text, "```") {
		t.Errorf("broken block: %q %v", text, s)
	}
}
