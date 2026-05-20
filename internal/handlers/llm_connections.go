package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"windshift/internal/llm"
	"windshift/internal/logger"
	"windshift/internal/restapi"
	"windshift/internal/utils"
)

// LLMConnectionHandler handles admin CRUD for LLM connections and user queries.
type LLMConnectionHandler struct {
	manager   *llm.ConnectionManager
	auditor   *logger.Auditor
	cache     *llm.ModelCache
	refresher *llm.ModelRefresher
}

// NewLLMConnectionHandler creates a new LLM connection handler.
func NewLLMConnectionHandler(manager *llm.ConnectionManager, auditor *logger.Auditor, cache *llm.ModelCache, refresher *llm.ModelRefresher) *LLMConnectionHandler {
	return &LLMConnectionHandler{manager: manager, auditor: auditor, cache: cache, refresher: refresher}
}

// ListConnections returns all LLM connections (admin).
func (h *LLMConnectionHandler) ListConnections(w http.ResponseWriter, r *http.Request) {
	connections, err := h.manager.ListConnections()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, connections)
}

// GetConnection returns a single LLM connection (admin).
func (h *LLMConnectionHandler) GetConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	conn, err := h.manager.GetConnection(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if conn == nil {
		respondNotFound(w, r, "LLM connection")
		return
	}
	respondJSONOK(w, conn)
}

// validateConnectionRequest checks that name, provider_type, and model are
// non-empty and that base_url (when provided) is a valid admin-configured HTTP(S) URL.
// Returns true when validation passes; on failure it writes the error response
// and returns false.
func validateConnectionRequest(w http.ResponseWriter, r *http.Request, name string, providerType llm.ProviderType, model, baseURL string) bool {
	if name == "" || providerType == "" || model == "" {
		respondBadRequest(w, r, "name, provider_type, and model are required")
		return false
	}
	if baseURL != "" {
		if err := utils.ValidateHTTPBaseURL(baseURL); err != nil {
			respondBadRequest(w, r, "invalid base URL: "+err.Error())
			return false
		}
	}
	return true
}

// CreateConnection creates a new LLM connection (admin).
func (h *LLMConnectionHandler) CreateConnection(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeJSON[llm.CreateConnectionRequest](w, r)
	if !ok {
		return
	}
	if !validateConnectionRequest(w, r, req.Name, req.ProviderType, req.Model, req.BaseURL) {
		return
	}

	conn, err := h.manager.CreateConnection(req)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionLLMConnectionCreate, logger.ResourceLLMConnection, &conn.ID, req.Name)
	}
	respondJSONCreated(w, conn)
}

// UpdateConnection updates an existing LLM connection (admin).
func (h *LLMConnectionHandler) UpdateConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	req, ok := decodeJSON[llm.UpdateConnectionRequest](w, r)
	if !ok {
		return
	}
	if !validateConnectionRequest(w, r, req.Name, req.ProviderType, req.Model, req.BaseURL) {
		return
	}

	conn, err := h.manager.UpdateConnection(id, req)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if conn == nil {
		respondNotFound(w, r, "LLM connection")
		return
	}
	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionLLMConnectionUpdate, logger.ResourceLLMConnection, &id, req.Name)
	}
	respondJSONOK(w, conn)
}

// DeleteConnection deletes an LLM connection (admin).
func (h *LLMConnectionHandler) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.manager.DeleteConnection(id); err != nil {
		respondInternalError(w, r, err)
		return
	}
	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionLLMConnectionDelete, logger.ResourceLLMConnection, &id, "")
	}
	respondJSON(w, http.StatusNoContent, nil)
}

// TestConnection tests an LLM connection (admin).
func (h *LLMConnectionHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.manager.TestConnection(id); err != nil {
		slog.Warn("LLM connection test failed", slog.Int("connection_id", id), slog.Any("error", err))
		respondError(w, r, restapi.NewAPIError(http.StatusBadGateway, restapi.ErrCodeConnectionTestFailed,
			fmt.Sprintf("Connection test failed: %s", err.Error())))
		return
	}
	respondJSONOK(w, map[string]string{"status": "ok"})
}

// providerResponse is the wire shape returned by GetProviders. It extends
// ProviderInfo with the cached model list + refresh state for providers that
// expose a /models endpoint, so the frontend can render the searchable
// picker + "Last refreshed" indicator from a single round trip.
type providerResponse struct {
	llm.ProviderInfo
	CachedModels    []llm.ModelInfo `json:"cached_models,omitempty"`
	LastRefreshedAt *time.Time      `json:"last_refreshed_at,omitempty"`
	LastError       string          `json:"last_error,omitempty"`
}

// GetProviders returns the catalog of known LLM providers (user). For
// dynamic-models providers the response also embeds the cached models +
// refresh metadata.
func (h *LLMConnectionHandler) GetProviders(w http.ResponseWriter, _ *http.Request) {
	providers := llm.KnownProviders()
	out := make([]providerResponse, 0, len(providers))
	for _, p := range providers {
		entry := providerResponse{ProviderInfo: p}
		if p.HasDynamicModels() && h.cache != nil {
			cached, err := h.cache.Get(p.Type)
			if err != nil {
				slog.Warn("read model cache", slog.String("provider", string(p.Type)), slog.Any("error", err))
			} else {
				entry.CachedModels = cached.Models
				entry.LastRefreshedAt = cached.LastRefreshedAt
				entry.LastError = cached.LastError
			}
		}
		out = append(out, entry)
	}
	respondJSONOK(w, out)
}

// RefreshProviderModels triggers a network fetch of a dynamic-models provider's
// catalog and writes it to the cache. Admin-only. The response carries the
// fresh list on success; on failure the error is surfaced to the caller AND
// recorded in the cache so the UI can render "Last attempt failed: …".
func (h *LLMConnectionHandler) RefreshProviderModels(w http.ResponseWriter, r *http.Request) {
	providerType := llm.ProviderType(r.PathValue("type"))
	provider := llm.GetProvider(providerType)
	if provider == nil {
		respondNotFound(w, r, "provider")
		return
	}
	if !provider.HasDynamicModels() {
		respondBadRequest(w, r, fmt.Sprintf("provider %q does not support dynamic model refresh", providerType))
		return
	}
	if h.refresher == nil {
		respondInternalError(w, r, fmt.Errorf("model refresher not configured"))
		return
	}

	// No API key for the catalog fetch — OpenRouter's /models is unauthenticated.
	// If a future provider requires it, plumb a key from an existing connection here.
	models, err := h.refresher.Refresh(r.Context(), *provider, "")
	if err != nil {
		slog.Warn("LLM model refresh failed", slog.String("provider", string(providerType)), slog.Any("error", err))
		respondError(w, r, restapi.NewAPIError(http.StatusBadGateway, restapi.ErrCodeConnectionTestFailed,
			fmt.Sprintf("Refresh failed: %s", err.Error())))
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionLLMConnectionUpdate, logger.ResourceLLMConnection, nil, string(providerType))
	}
	respondJSONOK(w, map[string]interface{}{
		"provider_type":     providerType,
		"models":            models,
		"last_refreshed_at": time.Now(),
	})
}

// GetEnabledConnections returns all enabled connections (user).
//
// Returns the slim PublicConnectionInfo (no BaseURL, HasAPIKey, timestamps,
// or IsEnabled) — admin-side endpoint configuration must not leak to every
// authenticated user. See bughunt8 finding 4.
func (h *LLMConnectionHandler) GetEnabledConnections(w http.ResponseWriter, r *http.Request) {
	connections, err := h.manager.ListEnabledPublic()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, connections)
}
