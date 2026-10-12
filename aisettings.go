package main

// AI agent settings chosen in the app: a provider, a model and the user's own
// API key. The key is kept in the local config file (readable only by this
// user on macOS/Linux) and only ever sent to the chosen provider; the API
// below never hands it back to the page.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/biopoetic/foveon-lab/agent"
)

// aiSettings is what the user picked (stored in config.json).
type aiSettings struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"baseURL"`
	Model    string `json:"model"`
	Key      string `json:"key"`
}

type aiProvider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseURL"`
	Model   string `json:"model"`
	KeyURL  string `json:"keyURL"` // where to create a key
	// OpenAI's newer models want max_completion_tokens.
	completionTokens bool
}

// aiProviders are OpenAI-compatible endpoints with vision + tool calling.
var aiProviders = []aiProvider{
	{ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", KeyURL: "https://platform.deepseek.com/api_keys"},
	{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Model: "gpt-5.4-mini", KeyURL: "https://platform.openai.com/api-keys", completionTokens: true},
	{ID: "gemini", Name: "Google Gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", Model: "gemini-2.5-flash", KeyURL: "https://aistudio.google.com/apikey"},
	{ID: "openrouter", Name: "OpenRouter (many models)", BaseURL: "https://openrouter.ai/api/v1", Model: "openai/gpt-5.4-mini", KeyURL: "https://openrouter.ai/keys"},
	{ID: "custom", Name: "Other OpenAI-compatible", BaseURL: "", Model: ""},
}

func providerByID(id string) (aiProvider, bool) {
	for _, p := range aiProviders {
		if p.ID == id {
			return p, true
		}
	}
	return aiProvider{}, false
}

// aiState holds the live agent config; the settings can change at runtime.
type aiState struct {
	mu       sync.RWMutex
	cfg      agent.Config
	provider string
	source   string // "app" (set in the UI), "env" (environment variable) or ""
	keyEnv   string
}

func (a *aiState) get() agent.Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

func (a *aiState) apply(st aiSettings) {
	p, _ := providerByID(st.Provider)
	a.mu.Lock()
	a.cfg.BaseURL, a.cfg.Model, a.cfg.APIKey = st.BaseURL, st.Model, st.Key
	a.cfg.CompletionTokens = p.completionTokens
	a.provider, a.source = st.Provider, "app"
	a.mu.Unlock()
}

func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}

func (a *aiState) status() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	hint := ""
	if k := a.cfg.APIKey; len(k) >= 8 {
		hint = "…" + k[len(k)-4:]
	}
	return map[string]any{
		"enabled": a.cfg.APIKey != "", "model": a.cfg.Model, "host": hostOf(a.cfg.BaseURL), "baseURL": a.cfg.BaseURL,
		"provider": a.provider, "source": a.source, "keyHint": hint, "keyEnv": a.keyEnv, "providers": aiProviders,
	}
}

// handleAISettings: GET = status; POST {provider, model, baseURL, key} tests
// the key with one tiny request and saves it only if that works; POST
// {"remove": true} forgets the saved key.
func (s *server) handleAISettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, s.ai.status())
		return
	}
	var req struct {
		aiSettings
		Remove bool `json:"remove"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, 400, err)
		return
	}
	cfg, _ := loadConfig()
	if req.Remove {
		cfg.AI = nil
		if err := saveConfig(cfg); err != nil {
			httpErr(w, 500, err)
			return
		}
		s.ai.mu.Lock()
		s.ai.cfg.APIKey, s.ai.source, s.ai.provider = "", "", ""
		s.ai.mu.Unlock()
		writeJSON(w, s.ai.status())
		return
	}
	p, ok := providerByID(req.Provider)
	if !ok {
		httpErr(w, 400, errors.New("unknown provider"))
		return
	}
	st := aiSettings{Provider: p.ID, BaseURL: strings.TrimSpace(req.BaseURL), Model: strings.TrimSpace(req.Model), Key: strings.TrimSpace(req.Key)}
	if st.BaseURL == "" {
		st.BaseURL = p.BaseURL
	}
	if st.Model == "" {
		st.Model = p.Model
	}
	if st.Key == "" { // keep the saved key when only the model changes
		if cfg.AI != nil && cfg.AI.Provider == p.ID {
			st.Key = cfg.AI.Key
		}
	}
	if u, err := url.Parse(st.BaseURL); err != nil || (u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		httpErr(w, 400, errors.New("the API address must start with https://"))
		return
	}
	if st.Model == "" || st.Key == "" {
		httpErr(w, 400, errors.New("enter the model and your API key"))
		return
	}
	test := agent.Config{BaseURL: st.BaseURL, Model: st.Model, APIKey: st.Key, CompletionTokens: p.completionTokens}
	if err := agent.Ping(context.Background(), test); err != nil {
		httpErr(w, 400, errors.New("the provider refused the test request: "+err.Error()))
		return
	}
	cfg.AI = &st
	if err := saveConfig(cfg); err != nil {
		httpErr(w, 500, err)
		return
	}
	s.ai.apply(st)
	writeJSON(w, s.ai.status())
}

// sameOrigin rejects state-changing requests from other web sites: a page
// elsewhere could otherwise POST to this local server (e.g. swap in its own
// AI endpoint and receive your photos).
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Browsers label every request with where it came from.
		if sf := r.Header.Get("Sec-Fetch-Site"); sf != "" && sf != "same-origin" && sf != "none" && strings.HasPrefix(r.URL.Path, "/api/") {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					http.Error(w, "cross-site request refused", http.StatusForbidden)
					return
				}
			}
			if ct := r.Header.Get("Content-Type"); r.ContentLength > 0 && !strings.HasPrefix(ct, "application/json") {
				http.Error(w, "JSON only", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
