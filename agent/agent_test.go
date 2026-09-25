package agent

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/biopoetic/foveon-lab/preset"
)

// fakeModel replays scripted assistant messages and records requests.
type fakeModel struct {
	mu       sync.Mutex
	script   []Message
	requests []chatRequest
	raw      []string
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer k" {
		http.Error(w, `{"error":{"message":"bad key"}}`, 401)
		return
	}
	f.requests = append(f.requests, req)
	f.raw = append(f.raw, string(body))
	i := len(f.requests) - 1
	if i >= len(f.script) {
		http.Error(w, "script exhausted", 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": f.script[i], "finish_reason": "tool_calls"}},
		"usage":   map[string]int{"prompt_tokens": 100, "completion_tokens": 10},
	})
}

func call(id, name, args string) ToolCall {
	var tc ToolCall
	tc.ID, tc.Type = id, "function"
	tc.Function.Name, tc.Function.Arguments = name, args
	return tc
}

func fakeRender(p preset.Params, w int) (*image.RGBA, error) {
	img := image.NewRGBA(image.Rect(0, 0, w, w*2/3))
	v := uint8(120 + p.Exposure*40)
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, v, v, 255
	}
	img.Set(0, 0, color.RGBA{255, 255, 255, 255})
	return img, nil
}

func candidates() []preset.Preset {
	a := preset.Preset{ID: "p1", Name: "[SLIDE] Velvia", Params: preset.Neutral()}
	a.Params.Contrast, a.Params.Saturation, a.Params.ColorMode = 0.4, 0.5, preset.ModeLandscape
	b := preset.Preset{ID: "p2", Name: "[PORTRAIT] Portra", Params: preset.Neutral()}
	return []preset.Preset{a, b}
}

func TestRunLoop(t *testing.T) {
	fm := &fakeModel{script: []Message{
		{Role: "assistant", Content: "Sunny landscape.", ReasoningContent: "bright scene",
			ToolCalls: []ToolCall{call("c1", "render_presets", `{"preset_ids":["p1","p2","nope"]}`)}},
		{Role: "assistant", ToolCalls: []ToolCall{
			call("c2", "render", `{"base_preset_id":"p1","exposure":-0.3,"label":"Velvia darker"}`),
			call("c3", "render", `{not json`)}},
		{Role: "assistant", ToolCalls: []ToolCall{call("c4", "finish", `{"base_preset_id":"p1","exposure":-0.3,"highlight":-9,"summary":"Velvia suits the sky."}`)}},
	}}
	srv := httptest.NewServer(fm)
	defer srv.Close()

	var events []Event
	res, err := Run(context.Background(), Config{BaseURL: srv.URL, Model: "m", APIKey: "k"},
		Photo{Name: "SDIM0031.X3F", Camera: "SD1", Info: map[string]string{"ISO": "100"}}, "vivid",
		candidates(), fakeRender, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}

	// The final params: base preset p1, exposure overridden, highlight clamped.
	if res.PresetID != "p1" || res.Params.Exposure != -0.3 || res.Params.Contrast != 0.4 ||
		res.Params.Highlight != -2 || res.Params.ColorMode != preset.ModeLandscape || res.Summary == "" {
		t.Fatalf("result = %+v", res)
	}

	if len(fm.requests) != 3 {
		t.Fatalf("model called %d times, want 3", len(fm.requests))
	}
	// First request: system + user with text (presets, direction) + the neutral image.
	first := fm.raw[0]
	for _, want := range []string{"p1 | [SLIDE] Velvia", "vivid", "data:image/jpeg;base64,", `"render_presets"`} {
		if !strings.Contains(first, want) {
			t.Errorf("first request lacks %q", want)
		}
	}
	// Second request: tool result then a USER message carrying 2 images (unknown id skipped).
	msgs := fm.requests[1].Messages
	last, tool := msgs[len(msgs)-1], msgs[len(msgs)-2]
	if tool.Role != "tool" || tool.ToolCallID != "c1" || !strings.Contains(tool.Content.(string), "unknown preset id") {
		t.Errorf("tool message = %+v", tool)
	}
	if last.Role != "user" || strings.Count(fm.raw[1], "data:image/jpeg") != 3 { // neutral + 2 candidates
		t.Errorf("images not delivered as a user message: role=%s count=%d", last.Role, strings.Count(fm.raw[1], "data:image/jpeg"))
	}
	// The assistant turn (with reasoning) is echoed back.
	if !strings.Contains(fm.raw[1], "bright scene") {
		t.Error("reasoning_content not passed back")
	}
	// Third request: bad JSON reported as a tool error, not a crash.
	if !strings.Contains(fm.raw[2], "not valid JSON") {
		t.Error("bad tool JSON not reported back to the model")
	}

	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Type]++
	}
	if kinds["images"] < 3 || kinds["final"] != 1 || kinds["thinking"] != 1 || kinds["say"] != 1 {
		t.Errorf("events = %v", kinds)
	}
}

func TestForcesFinishOnLastStep(t *testing.T) {
	fm := &fakeModel{script: []Message{
		{Role: "assistant", ToolCalls: []ToolCall{call("c1", "render_presets", `{"preset_ids":["p2"]}`)}},
		{Role: "assistant", ToolCalls: []ToolCall{call("c2", "finish", `{"base_preset_id":"p2","summary":"ok"}`)}},
	}}
	srv := httptest.NewServer(fm)
	defer srv.Close()
	res, err := Run(context.Background(), Config{BaseURL: srv.URL, Model: "m", APIKey: "k", MaxSteps: 2},
		Photo{Name: "x"}, "", candidates(), fakeRender, func(Event) {})
	if err != nil || res.PresetID != "p2" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if fm.requests[0].ToolChoice != nil || fm.requests[1].ToolChoice == nil {
		t.Errorf("tool_choice should force finish only on the last step")
	}
}

func TestAPIErrorsSurface(t *testing.T) {
	srv := httptest.NewServer(&fakeModel{})
	defer srv.Close()
	_, err := Run(context.Background(), Config{BaseURL: srv.URL, Model: "m", APIKey: "wrong"},
		Photo{Name: "x"}, "", candidates(), fakeRender, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Run(context.Background(), Config{}, Photo{}, "", nil, fakeRender, func(Event) {}); err == nil {
		t.Error("missing API key not rejected")
	}
}

func TestStats(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 255, 128, 0, 255
	}
	s := ComputeStats(img)
	if s.Clipped != 100 || s.WarmCool != 255 || s.Saturation != 255 {
		t.Errorf("stats = %+v", s)
	}
}
