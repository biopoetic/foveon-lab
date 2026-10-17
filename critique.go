package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/biopoetic/foveon-lab/agent"
	"github.com/biopoetic/foveon-lab/render"
)

// handleCritique renders the photo the request names and asks the AI to rate
// it and say where it could be better. It returns the critique and the global
// settings the model would apply, so the editor can offer them as one click.
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
	current := map[string]float64{
		"exposure": p.Exposure, "contrast": p.Contrast, "blackness": p.Blackness,
		"highlight": p.Highlight, "fillLight": p.FillLight, "saturation": p.Saturation,
		"r": p.R, "g": p.G, "b": p.B,
	}
	text, settings, crop, err := agent.Critique(r.Context(), cfg, out, strings.TrimSpace(r.URL.Query().Get("note")), current)
	if err != nil {
		httpErr(w, 502, err)
		return
	}
	writeJSON(w, map[string]any{"text": text, "settings": settings, "crop": crop})
}
