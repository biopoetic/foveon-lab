package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/jpeg"
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
		MaxTokens: 900,
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("the model returned no answer")
	}
	text, ok := resp.Choices[0].Message.Content.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", errors.New("the model returned no text")
	}
	return strings.TrimSpace(text), nil
}
