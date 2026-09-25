// Package agent runs a vision-model loop that looks at a photo, compares
// SPP presets rendered on it, fine-tunes the best one and reports why.
//
// It talks to any OpenAI-compatible Chat Completions endpoint; the default
// is DeepSeek's "deepseek-flash", which accepts images and tool calls.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config selects the model endpoint.
type Config struct {
	BaseURL string // e.g. https://api.deepseek.com or https://api.openai.com/v1
	Model   string
	APIKey  string
	// MaxSteps bounds the number of model calls in one run.
	MaxSteps int
	// HTTP is optional (tests inject one).
	HTTP *http.Client
}

// Part is one element of a multimodal message.
type Part struct {
	Type     string    `json:"type"` // "text" or "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL carries an image as a data: URL.
type ImageURL struct {
	URL string `json:"url"`
}

// Message is a Chat Completions message. Content is a string or []Part.
type Message struct {
	Role             string     `json:"role"`
	Content          any        `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a function call requested by the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool declares a callable function.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is a tool's name, description and JSON-schema parameters.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type chatRequest struct {
	Model      string    `json:"model"`
	Messages   []Message `json:"messages"`
	Tools      []Tool    `json:"tools,omitempty"`
	ToolChoice any       `json:"tool_choice,omitempty"`
	MaxTokens  int       `json:"max_tokens,omitempty"`
}

// Usage is the token accounting of one or more calls.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Config) chat(ctx context.Context, req chatRequest) (*chatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 3 * time.Minute}
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		hr.Header.Set("Content-Type", "application/json")
		hr.Header.Set("Authorization", "Bearer "+c.APIKey)
		resp, err := hc.Do(hr)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("model API %s: %s", resp.Status, trim(string(data), 300))
			continue
		}
		var out chatResponse
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("model API %s: bad JSON: %s", resp.Status, trim(string(data), 300))
		}
		if resp.StatusCode != 200 || out.Error != nil {
			msg := trim(string(data), 300)
			if out.Error != nil {
				msg = out.Error.Message
			}
			return nil, fmt.Errorf("model API %s: %s", resp.Status, msg)
		}
		if len(out.Choices) == 0 {
			return nil, fmt.Errorf("model API returned no choices")
		}
		return &out, nil
	}
	return nil, lastErr
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
