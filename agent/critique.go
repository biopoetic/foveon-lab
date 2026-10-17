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
	"log"
	"regexp"
	"sort"
	"strings"
)

// Critique asks the model to rate a photograph, say how it could be better,
// and end with the global settings it would apply. The settings are returned
// separately so the app can apply them with one click; the text is the
// critique with the JSON block removed.
func Critique(ctx context.Context, c Config, img image.Image, note string, current map[string]float64) (string, map[string]float64, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		return "", nil, err
	}
	data := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	prompt := `Look at this photograph and critique it as a working photographer would.

Reply in Markdown, with exactly these sections:

## Score
One number out of 10, and one sentence why.

## What works
Two or three specific things, each naming where in the frame you see them.

## What to improve
Three to five points, each an action: framing, subject placement, exposure of
a named area, colour balance, contrast, grain or sharpness, or a distracting
element. Say where in the frame it is and which way to move or change it.

## Settings to try
Two or three concrete adjustments in plain language (exposure, contrast,
highlights, shadows, saturation, white balance), not a preset name.

Then end with one fenced JSON block the app can apply, with absolute target
values (not deltas) for the sliders it has:

` + "```json" + `
{"settings": {"exposure": 0.13, "contrast": 0.16, "blackness": -0.05, "highlight": -1.1, "fillLight": 0.3, "saturation": 0.2, "r": 1.0, "g": 1.0, "b": 0.97}}
` + "```" + `

Only these keys, within these ranges: exposure, contrast, blackness, highlight,
fillLight, saturation in [-2, 2]; r, g, b in [0.7, 1.3]. Omit any key you would
leave unchanged. The app cannot burn, dodge or crop from this block: put those
in the text instead.`
	if note != "" {
		prompt += "\n\nThe photographer adds: " + note
	}
	if len(current) > 0 {
		prompt += "\n\nThe photo's current settings are: " + settingsLine(current) +
			". Suggest absolute values for them, keeping what already works."
	}
	resp, err := c.chat(ctx, chatRequest{
		Model: c.Model,
		Messages: []Message{
			{Role: "system", Content: "You are a precise, fair photography critic. " +
				"Describe only what is in the frame; never invent detail. Be specific and brief."},
			{Role: "user", Content: []Part{
				{Type: "text", Text: prompt},
				{Type: "image_url", ImageURL: &ImageURL{URL: data}},
			}},
		},
		// Reasoning models think before they answer; a tight limit (900) was
		// spent on the thinking, leaving content empty (finish "length").
		MaxTokens: 4000,
	})
	if err != nil {
		return "", nil, err
	}
	if len(resp.Choices) == 0 {
		return "", nil, errors.New("the model returned no answer")
	}
	choice := resp.Choices[0]
	text := messageText(choice.Message)
	if text == "" {
		log.Printf("critique: empty content (finish %q, reasoning %d chars)", choice.FinishReason, len(choice.Message.ReasoningContent))
		if choice.FinishReason == "length" {
			return "", nil, errors.New("the model ran out of tokens before answering")
		}
		return "", nil, errors.New("the model returned no text")
	}
	text, settings := splitSettings(text)
	return text, settings, nil
}

// splitSettings removes the fenced JSON block from the reply and returns the
// settings it holds (nil if there is none or it is unusable).
func splitSettings(text string) (string, map[string]float64) {
	m := jsonBlock.FindStringSubmatchIndex(text)
	if m == nil {
		return strings.TrimSpace(text), nil
	}
	var body struct {
		Settings map[string]float64 `json:"settings"`
	}
	settings := map[string]float64(nil)
	if err := json.Unmarshal([]byte(strings.TrimSpace(text[m[2]:m[3]])), &body); err == nil && len(body.Settings) > 0 {
		settings = body.Settings
	}
	clean := strings.TrimSpace(text[:m[0]] + text[m[1]:])
	return clean, settings
}

// jsonBlock matches a fenced JSON block; the last one in the reply wins.
var jsonBlock = regexp.MustCompile("(?s)```json\\s*(.*?)```")

// settingsLine formats the current values for the prompt, in a stable order.
func settingsLine(v map[string]float64) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %.2f", k, v[k])
	}
	return b.String()
}

// messageText reads the reply whether the provider sends it as a string or as
// a list of content parts (both are seen in the wild).
func messageText(m Message) string {
	switch c := m.Content.(type) {
	case string:
		return strings.TrimSpace(c)
	case []Part:
		var b strings.Builder
		for _, p := range c {
			if p.Type == "text" && p.Text != "" {
				b.WriteString(p.Text)
				b.WriteString("\n")
			}
		}
		return strings.TrimSpace(b.String())
	case []any:
		var b strings.Builder
		for _, it := range c {
			if mm, ok := it.(map[string]any); ok {
				if s, ok := mm["text"].(string); ok && s != "" {
					b.WriteString(s)
					b.WriteString("\n")
				}
			}
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}
