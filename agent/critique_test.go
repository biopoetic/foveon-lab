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
		"```json\n{\"settings\":{\"exposure\":0.2,\"highlight\":-0.4}," +
		"\"crop\":{\"x\":0.1,\"y\":0.05,\"w\":0.8,\"h\":0.7,\"angle\":1.5}}\n```"
	f := &fakeModel{script: []Message{{Role: "assistant", Content: reply}}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg := Config{BaseURL: srv.URL, Model: "m", APIKey: "k", HTTP: srv.Client()}

	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	text, settings, crop, err := Critique(context.Background(), cfg, img, "shot at dusk", map[string]float64{"exposure": 0})
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
	if crop == nil || crop.W != 0.8 || crop.H != 0.7 || crop.X != 0.1 || crop.Y != 0.05 || crop.Angle != 1.5 {
		t.Errorf("crop %+v, want 0.1,0.05 0.8x0.7 at 1.5°", crop)
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
	if _, _, _, err := Critique(context.Background(), cfg, image.NewRGBA(image.Rect(0, 0, 8, 8)), "", nil); err == nil {
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
// crop only when it would change the frame.
func TestSplitSettings(t *testing.T) {
	if text, s, c := splitSettings("no block here"); s != nil || c != nil || text != "no block here" {
		t.Errorf("plain text: %q %v %v", text, s, c)
	}
	text, s, c := splitSettings("a\n```json\n{\"settings\":{\"exposure\":0.1},\"crop\":{\"x\":0.1,\"y\":0.1,\"w\":0.5,\"h\":0.5}}\n```\nb")
	if text != "a\n\nb" || s["exposure"] != 0.1 {
		t.Errorf("block: %q %v", text, s)
	}
	if c == nil || c.W != 0.5 || c.H != 0.5 {
		t.Errorf("crop %+v, want 0.5x0.5", c)
	}
	// A crop of the whole frame changes nothing, so it is dropped.
	if _, _, c := splitSettings("```json\n{\"crop\":{\"x\":0,\"y\":0,\"w\":1,\"h\":1}}\n```"); c != nil {
		t.Errorf("full-frame crop kept: %+v", c)
	}
	// Values outside the frame are clamped, not passed on.
	if _, _, c := splitSettings("```json\n{\"crop\":{\"x\":0.8,\"y\":0.9,\"w\":1,\"h\":1,\"angle\":90}}\n```"); c == nil || c.W > 0.21 || c.Angle != 45 {
		t.Errorf("clamped crop %+v", c)
	}
	text, s, c = splitSettings("a\n```json\n{broken\n```")
	if s != nil || c != nil || strings.Contains(text, "```") {
		t.Errorf("broken block: %q %v %v", text, s, c)
	}
}
