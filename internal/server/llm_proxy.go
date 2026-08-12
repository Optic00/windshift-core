package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"windshift/internal/llm"
)

const logbookArticlesFeature = "logbook_articles"

const internalLLMProxyMaxBody = 16 << 20

// NewInternalLLMProxy creates an HTTP handler that proxies chat completion
// requests to the admin-configured default LLM connection.
// Authentication uses a shared secret (SSO_SECRET) with constant-time comparison.
func NewInternalLLMProxy(llmManager *llm.ConnectionManager, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validateInternalToken(r, secret) {
			writeLLMProxyError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, internalLLMProxyMaxBody)
		var req llm.CompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeLLMProxyError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			writeLLMProxyError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		client, status, message, err := resolveLogbookLLMClient(llmManager)
		if message != "" {
			if message == "LLM service unavailable" {
				slog.Warn("LLM proxy: no client available", "error", err)
			}
			writeLLMProxyError(w, status, message)
			return
		}

		resp, err := client.Complete(r.Context(), req)
		if err != nil {
			slog.Error("LLM proxy: chat completion failed", "error", err)
			writeLLMProxyError(w, http.StatusBadGateway, "LLM request failed")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Debug("LLM proxy: failed to write response", "error", err)
		}
	})
}

// NewInternalLLMHealthCheck creates an HTTP handler that checks whether the
// admin-configured default LLM connection is available.
func NewInternalLLMHealthCheck(llmManager *llm.ConnectionManager, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validateInternalToken(r, secret) {
			writeLLMProxyError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		_, status, message, err := resolveLogbookLLMClient(llmManager)
		if message != "" {
			writeLLMProxyError(w, status, message)
			return
		}
		if err != nil {
			slog.Error("LLM proxy: health check failed", "error", err)
			writeLLMProxyError(w, http.StatusServiceUnavailable, "LLM service unavailable")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
			slog.Debug("LLM proxy: failed to write health response", "error", err)
		}
	})
}

func resolveLogbookLLMClient(llmManager *llm.ConnectionManager) (client llm.Client, status int, message string, err error) {
	client, err = llmManager.ResolveForFeature(logbookArticlesFeature)
	if errors.Is(err, llm.ErrFeatureDisabled) {
		return nil, http.StatusServiceUnavailable, "feature disabled", err
	}
	if err != nil || client == nil || !client.Available() {
		return nil, http.StatusServiceUnavailable, "LLM service unavailable", err
	}
	return client, http.StatusOK, "", nil
}

func writeLLMProxyError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, err := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: message})
	if err != nil {
		slog.Error("LLM proxy: failed to marshal error response", "error", err)
		body = []byte(`{"error":"internal error"}`)
	}
	if _, err := w.Write(body); err != nil {
		slog.Debug("LLM proxy: failed to write error response", "error", err)
	}
}

// validateInternalToken extracts the bearer token from the Authorization header
// and compares it against the expected secret using constant-time comparison.
func validateInternalToken(r *http.Request, secret string) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) || len(auth) <= len(prefix) {
		return false
	}
	token := auth[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1
}
