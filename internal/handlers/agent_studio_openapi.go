package handlers

import (
	"net/http"

	"windshift/api"
)

// AgentStudioOpenAPIYAML serves the authenticated, session-based Agent Studio
// API contract. The public REST v1 specification uses a different auth and URL
// surface and therefore remains separate.
func AgentStudioOpenAPIYAML(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(api.AgentStudioSpecYAML)
}
