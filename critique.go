package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/biopoetic/foveon-lab/agent"
	"github.com/biopoetic/foveon-lab/render"
)

// handleCritique renders the photo the request names and asks the AI to rate
// it and say where it could be better. The render parameters are the same as
// /api/render, so it judges the photo as edited.
func (s *server) handleCritique(w http.ResponseWriter, r *http.Request) {
	cfg := s.ai.get()
	if cfg.BaseURL == "" || cfg.APIKey == "" {
		httpErr(w, 400, errors.New("the AI agent is not set up"))
		return
	}
	b, p, comp, ok := s.renderBase(w, r)
	if !ok {
		return
	}
	out := render.Render(b, p, comp)
	text, err := agent.Critique(r.Context(), cfg, out, strings.TrimSpace(r.URL.Query().Get("note")))
	if err != nil {
		httpErr(w, 502, err)
		return
	}
	writeJSON(w, map[string]string{"text": text})
}
