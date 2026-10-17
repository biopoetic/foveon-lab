package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/jpeg"
	"log"
	"strings"
)

// Critique asks the model to rate a photograph and say how it could be
// better. The photograph is the finished render, so the model judges what the
// photographer would see. The reply is Markdown.
func Critique(ctx context.Context, c Config, img image.Image, note string) (string, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		return "", err
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
highlights, shadows, saturation, white balance), not a preset name.`
	if note != "" {
		prompt += "\n\nThe photographer adds: " + note
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
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("the model returned no answer")
	}
	choice := resp.Choices[0]
	text := messageText(choice.Message)
	if text == "" {
		log.Printf("critique: empty content (finish %q, reasoning %d chars)", choice.FinishReason, len(choice.Message.ReasoningContent))
		if choice.FinishReason == "length" {
			return "", errors.New("the model ran out of tokens before answering")
		}
		return "", errors.New("the model returned no text")
	}
	return text, nil
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
