package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"sort"
	"strings"

	"github.com/biopoetic/foveon-lab/preset"
)

// Photo is what the agent is told about the shot.
type Photo struct {
	Name   string
	Camera string
	Info   map[string]string // X3F PROP subset: ISO, SH_DESC, CM_DESC, WB_DESC…
}

// RenderFunc renders the photo with p at (at most) width w.
type RenderFunc func(p preset.Params, w int) (*image.RGBA, error)

// ImageRef identifies a rendered image so the UI can re-render the same view.
type ImageRef struct {
	Label    string         `json:"label"`
	PresetID string         `json:"presetId,omitempty"`
	Params   *preset.Params `json:"params,omitempty"`
	Stats    Stats          `json:"stats"`
}

// Result is the agent's final choice.
type Result struct {
	PresetID   string        `json:"presetId"`
	PresetName string        `json:"presetName"`
	Params     preset.Params `json:"params"`
	Summary    string        `json:"summary"`
}

// Event is one progress update streamed to the UI.
type Event struct {
	Type   string     `json:"type"` // "status" "thinking" "say" "tool" "images" "final" "error"
	Text   string     `json:"text,omitempty"`
	Images []ImageRef `json:"images,omitempty"`
	Final  *Result    `json:"final,omitempty"`
	Usage  *Usage     `json:"usage,omitempty"`
	Step   int        `json:"step"`
}

const (
	compareW = 512 // candidate comparison renders
	refineW  = 768 // fine-tuning renders
	maxBatch = 6   // presets per render_presets call
)

// Stats are objective numbers about a render, so the model does not have
// to judge clipping from a small image.
type Stats struct {
	Mean       float64 `json:"mean"`       // mean brightness 0-255
	P5         float64 `json:"p5"`         // 5th percentile brightness
	P95        float64 `json:"p95"`        // 95th percentile brightness
	Clipped    float64 `json:"clipped"`    // % pixels with a channel >= 250
	Crushed    float64 `json:"crushed"`    // % pixels with brightness <= 6
	Saturation float64 `json:"saturation"` // mean chroma 0-255
	WarmCool   float64 `json:"warmCool"`   // mean (R-B): >0 warm, <0 cool
}

// ComputeStats measures a render.
func ComputeStats(img *image.RGBA) Stats {
	var s Stats
	var hist [256]int
	n := 0
	var sum, chroma, wc float64
	var clip, crush int
	for i := 0; i+3 < len(img.Pix); i += 4 {
		r, g, b := float64(img.Pix[i]), float64(img.Pix[i+1]), float64(img.Pix[i+2])
		y := 0.2126*r + 0.7152*g + 0.0722*b
		hist[int(y)]++
		sum += y
		mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
		chroma += mx - mn
		wc += r - b
		if mx >= 250 {
			clip++
		}
		if y <= 6 {
			crush++
		}
		n++
	}
	if n == 0 {
		return s
	}
	pct := func(p float64) float64 {
		target := int(p * float64(n))
		acc := 0
		for v, c := range hist {
			acc += c
			if acc > target {
				return float64(v)
			}
		}
		return 255
	}
	fn := float64(n)
	s = Stats{Mean: sum / fn, P5: pct(0.05), P95: pct(0.95), Clipped: 100 * float64(clip) / fn,
		Crushed: 100 * float64(crush) / fn, Saturation: chroma / fn, WarmCool: wc / fn}
	for _, p := range []*float64{&s.Mean, &s.P5, &s.P95, &s.Saturation, &s.WarmCool} {
		*p = math.Round(*p*10) / 10
	}
	s.Clipped = math.Round(s.Clipped*100) / 100
	s.Crushed = math.Round(s.Crushed*100) / 100
	return s
}

func (s Stats) String() string {
	return fmt.Sprintf("brightness mean %.0f (p5 %.0f, p95 %.0f), clipped highlights %.2f%%, crushed shadows %.2f%%, saturation %.0f, warmth R-B %+.0f",
		s.Mean, s.P5, s.P95, s.Clipped, s.Crushed, s.Saturation, s.WarmCool)
}

func dataURL(img *image.RGBA) (string, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// paramArgs is the tool-facing shape of the adjustable parameters. Pointers
// distinguish "not given" (keep the base preset's value) from zero.
type paramArgs struct {
	BasePresetID string   `json:"base_preset_id"`
	Exposure     *float64 `json:"exposure"`
	Contrast     *float64 `json:"contrast"`
	Shadow       *float64 `json:"shadow"`
	Highlight    *float64 `json:"highlight"`
	FillLight    *float64 `json:"fill_light"`
	Saturation   *float64 `json:"saturation"`
	Sharpness    *float64 `json:"sharpness"`
	R            *float64 `json:"color_adjust_r"`
	G            *float64 `json:"color_adjust_g"`
	B            *float64 `json:"color_adjust_b"`
	ColorMode    *int     `json:"color_mode"`
	Label        string   `json:"label"`
	Summary      string   `json:"summary"`
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// resolve applies the overrides on top of the base preset (or neutral).
func (a paramArgs) resolve(presets map[string]preset.Preset) (preset.Params, string, error) {
	p := preset.Neutral()
	name := "Neutral"
	if a.BasePresetID != "" {
		pr, ok := presets[a.BasePresetID]
		if !ok {
			return p, "", fmt.Errorf("unknown base_preset_id %q", a.BasePresetID)
		}
		p, name = pr.Params, pr.Name
	}
	set := func(dst *float64, v *float64, lo, hi float64) {
		if v != nil {
			*dst = clamp(*v, lo, hi)
		}
	}
	set(&p.Exposure, a.Exposure, -2, 2)
	set(&p.Contrast, a.Contrast, -2, 2)
	set(&p.Blackness, a.Shadow, -2, 2)
	set(&p.Highlight, a.Highlight, -2, 2)
	set(&p.FillLight, a.FillLight, -2, 2)
	set(&p.Saturation, a.Saturation, -2, 2)
	set(&p.Sharpness, a.Sharpness, -2, 2)
	set(&p.R, a.R, 0.7, 1.3)
	set(&p.G, a.G, 0.7, 1.3)
	set(&p.B, a.B, 0.7, 1.3)
	if a.ColorMode != nil {
		switch *a.ColorMode {
		case preset.ModeStandard, preset.ModePortrait, preset.ModeLandscape:
			p.ColorMode = *a.ColorMode
		default:
			return p, "", fmt.Errorf("color_mode must be 4, 6 or 7")
		}
	}
	return p, name, nil
}

func paramSchema(extra map[string]any, required ...string) map[string]any {
	num := func(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
	props := map[string]any{
		"base_preset_id": map[string]any{"type": "string", "description": "Preset to start from (id from the list). Omit to start from neutral."},
		"exposure":       num("EV, -2..2"),
		"contrast":       num("-2..2 (typical presets -0.2..0.5)"),
		"shadow":         num("SPP 'Shadow'/Blackness: + deepens blacks, - lifts them (matte). -2..2, typical -0.1..0.2"),
		"highlight":      num("- pulls highlights down / recovers, + brightens them. -2..2, typical -0.9..0"),
		"fill_light":     num("X3 Fill Light: + lifts shadows locally. -2..2, typical 0..0.7"),
		"saturation":     num("-2..2; -1 is muted colour (about half), -2 = black & white"),
		"sharpness":      num("-2..2, usually 0"),
		"color_adjust_r": num("red multiplier 0.7..1.3 (1 = none)"),
		"color_adjust_g": num("green multiplier 0.7..1.3"),
		"color_adjust_b": num("blue multiplier 0.7..1.3 (<1 warms, >1 cools)"),
		"color_mode":     map[string]any{"type": "integer", "enum": []int{4, 6, 7}, "description": "4 Standard, 6 Portrait (softer colour, skin kept), 7 Landscape (greens/blues pushed)"},
	}
	for k, v := range extra {
		props[k] = v
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func tools() []Tool {
	return []Tool{
		{Type: "function", Function: ToolFunction{
			Name:        "render_presets",
			Description: fmt.Sprintf("Render up to %d presets on the photo and look at them side by side (each comes back as its own image with objective stats). Use this to shortlist.", maxBatch),
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"preset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": maxBatch},
			}, "required": []string{"preset_ids"}},
		}},
		{Type: "function", Function: ToolFunction{
			Name:        "render",
			Description: "Render one adjusted version (a base preset plus slider overrides) at a larger size, to check a fine-tuning idea.",
			Parameters: paramSchema(map[string]any{
				"label": map[string]any{"type": "string", "description": "Short name for this variant"},
			}),
		}},
		{Type: "function", Function: ToolFunction{
			Name:        "finish",
			Description: "Deliver the final choice: the base preset and the tuned sliders, with a short explanation for the photographer.",
			Parameters: paramSchema(map[string]any{
				"summary": map[string]any{"type": "string", "description": "2-4 sentences: why this preset suits the photo and what you adjusted."},
			}, "base_preset_id", "summary"),
		}},
	}
}

func presetLine(p preset.Preset) string {
	q := p.Params
	return fmt.Sprintf("%s | %s | exp %+.2f con %+.2f shd %+.2f hl %+.2f fill %+.2f sat %+.2f rgb %.2f/%.2f/%.2f %s",
		p.ID, p.Name, q.Exposure, q.Contrast, q.Blackness, q.Highlight, q.FillLight, q.Saturation, q.R, q.G, q.B, preset.ModeName(q.ColorMode))
}

const systemPrompt = `You are an expert photo editor for Sigma Foveon cameras (SD1 Merrill, SD15), working inside "Foveon Lab".
Your job: choose the Sigma Photo Pro (SPP) preset that suits THIS photo best, fine-tune its sliders, and explain briefly.

How it works:
- You see the photo as an emulation of SPP rendering (not pixel-exact SPP). Judge looks, not tiny differences.
- Every render comes with objective stats. Trust them for clipping and brightness: avoid clipped highlights above ~1% unless they are light sources or specular glints, and crushed shadows above ~2% unless the photo is meant to be low-key.
- Workflow: (1) look at the photo and decide what it needs (scene, light, mood, subject). (2) shortlist with render_presets — at most 3 calls, up to 6 presets each, chosen by name/params that fit the scene. (3) pick the best one and fine-tune it with render (at most 4 calls), changing only what it needs. (4) call finish.
- Prefer small, purposeful adjustments. Keep skin natural in portraits. Respect the photographer's direction if one is given.
- Keep your text short. Always act through tools; end with finish.`

// Run executes the agent loop, sending progress to emit. It returns the
// final result, or an error if the model never finished.
func Run(ctx context.Context, cfg Config, photo Photo, direction string, candidates []preset.Preset,
	render RenderFunc, emit func(Event)) (*Result, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("no API key configured")
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 12
	}
	byID := map[string]preset.Preset{}
	for _, p := range candidates {
		byID[p.ID] = p
	}

	orig, err := render(preset.Neutral(), refineW)
	if err != nil {
		return nil, err
	}
	origStats := ComputeStats(orig)
	origURL, err := dataURL(orig)
	if err != nil {
		return nil, err
	}
	emit(Event{Type: "images", Images: []ImageRef{{Label: "Starting point (neutral)", Params: ptr(preset.Neutral()), Stats: origStats}}})

	var info []string
	keys := make([]string, 0, len(photo.Info))
	for k := range photo.Info {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		info = append(info, k+"="+photo.Info[k])
	}
	var list strings.Builder
	for _, p := range candidates {
		list.WriteString(presetLine(p) + "\n")
	}
	dir := strings.TrimSpace(direction)
	if dir == "" {
		dir = "(none — use your judgement)"
	}
	intro := fmt.Sprintf("Photo: %s, camera %s. Metadata: %s\nPhotographer's direction: %s\n\nNeutral render stats: %s\n\nAvailable presets (id | name | params):\n%s",
		photo.Name, photo.Camera, strings.Join(info, ", "), dir, origStats, list.String())

	msgs := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: []Part{{Type: "text", Text: intro}, {Type: "image_url", ImageURL: &ImageURL{URL: origURL}}}},
	}
	var usage Usage
	nudged := false
	for step := 1; step <= cfg.MaxSteps; step++ {
		req := chatRequest{Model: cfg.Model, Messages: msgs, Tools: tools(), MaxTokens: 4000}
		if step == cfg.MaxSteps {
			req.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": "finish"}}
			msgs = append(msgs, Message{Role: "user", Content: "Step budget reached: call finish now with your best choice."})
			req.Messages = msgs
		}
		emit(Event{Type: "status", Text: "thinking…", Step: step})
		resp, err := cfg.chat(ctx, req)
		if err != nil {
			return nil, err
		}
		usage.PromptTokens += resp.Usage.PromptTokens
		usage.CompletionTokens += resp.Usage.CompletionTokens
		m := resp.Choices[0].Message
		if m.ReasoningContent != "" {
			emit(Event{Type: "thinking", Text: m.ReasoningContent, Step: step, Usage: &usage})
		}
		if s, _ := m.Content.(string); strings.TrimSpace(s) != "" {
			emit(Event{Type: "say", Text: s, Step: step, Usage: &usage})
		}
		if m.Content == nil {
			m.Content = ""
		}
		msgs = append(msgs, m)

		if len(m.ToolCalls) == 0 {
			if nudged {
				return nil, errors.New("the model stopped without choosing a preset")
			}
			nudged = true
			msgs = append(msgs, Message{Role: "user", Content: "Please continue using the tools, and end with finish."})
			continue
		}

		var imgParts []Part
		for _, tc := range m.ToolCalls {
			var a struct {
				paramArgs
				PresetIDs []string `json:"preset_ids"`
			}
			result := ""
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &a); err != nil {
				result = "error: arguments are not valid JSON: " + err.Error()
			} else {
				switch tc.Function.Name {
				case "render_presets":
					emit(Event{Type: "tool", Text: fmt.Sprintf("comparing %d presets", min(len(a.PresetIDs), maxBatch)), Step: step})
					var refs []ImageRef
					var lines []string
					for i, id := range a.PresetIDs {
						if i >= maxBatch {
							break
						}
						p, ok := byID[id]
						if !ok {
							lines = append(lines, fmt.Sprintf("%s: unknown preset id", id))
							continue
						}
						img, err := render(p.Params, compareW)
						if err != nil {
							return nil, err
						}
						u, err := dataURL(img)
						if err != nil {
							return nil, err
						}
						st := ComputeStats(img)
						label := fmt.Sprintf("[%s] %s", p.ID, p.Name)
						refs = append(refs, ImageRef{Label: p.Name, PresetID: p.ID, Stats: st})
						lines = append(lines, fmt.Sprintf("image %d = %s — %s", len(refs), label, st))
						imgParts = append(imgParts, Part{Type: "text", Text: fmt.Sprintf("Image %d: %s", len(refs), label)},
							Part{Type: "image_url", ImageURL: &ImageURL{URL: u}})
					}
					if len(refs) > 0 {
						emit(Event{Type: "images", Images: refs, Step: step})
					}
					result = "Rendered; the images follow in the next message.\n" + strings.Join(lines, "\n")
				case "render":
					p, base, err := a.resolve(byID)
					if err != nil {
						result = "error: " + err.Error()
						break
					}
					label := a.Label
					if label == "" {
						label = base + " (tuned)"
					}
					emit(Event{Type: "tool", Text: "trying: " + label, Step: step})
					img, err := render(p, refineW)
					if err != nil {
						return nil, err
					}
					u, err := dataURL(img)
					if err != nil {
						return nil, err
					}
					st := ComputeStats(img)
					emit(Event{Type: "images", Images: []ImageRef{{Label: label, PresetID: a.BasePresetID, Params: ptr(p), Stats: st}}, Step: step})
					imgParts = append(imgParts, Part{Type: "text", Text: "Variant: " + label}, Part{Type: "image_url", ImageURL: &ImageURL{URL: u}})
					result = fmt.Sprintf("Rendered %q; the image follows in the next message. Stats: %s", label, st)
				case "finish":
					p, base, err := a.resolve(byID)
					if err != nil || a.BasePresetID == "" {
						if err == nil {
							err = errors.New("base_preset_id is required")
						}
						result = "error: " + err.Error()
						break
					}
					res := &Result{PresetID: a.BasePresetID, PresetName: base, Params: p, Summary: strings.TrimSpace(a.Summary)}
					emit(Event{Type: "final", Final: res, Usage: &usage, Step: step})
					return res, nil
				default:
					result = "error: unknown tool " + tc.Function.Name
				}
			}
			msgs = append(msgs, Message{Role: "tool", ToolCallID: tc.ID, Content: result})
		}
		// Images are only accepted in user messages, so tool results carry
		// text and the pictures follow as one user message.
		if len(imgParts) > 0 {
			msgs = append(msgs, Message{Role: "user", Content: append([]Part{{Type: "text", Text: "Renders for your last tool call(s):"}}, imgParts...)})
		}
	}
	return nil, errors.New("the model did not finish within the step budget")
}

func ptr[T any](v T) *T { return &v }
