package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"windshift/internal/llm"
)

const logbookArticlesFeature = "logbook_articles"

// NewInternalLLMProxy creates an HTTP handler that proxies chat completion
// requests to the admin-configured default LLM connection.
// Authentication uses a shared secret (SSO_SECRET) with constant-time comparison.
func NewInternalLLMProxy(llmManager *llm.ConnectionManager, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validateInternalToken(r, secret) {
			writeLLMProxyError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req llm.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

		resp, err := client.ChatCompletion(r.Context(), req)
		if err != nil {
			slog.Error("LLM proxy: chat completion failed", "error", err)
			writeLLMProxyError(w, http.StatusBadGateway, "LLM request failed")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
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

		if _, status, message, _ := resolveLogbookLLMClient(llmManager); message != "" {
			writeLLMProxyError(w, status, message)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
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
	body, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: message})
	_, _ = w.Write(body)
}

// validateInternalToken extracts the bearer token from the Authorization header
// and compares it against the expected secret using constant-time comparison.
func validateInternalToken(r *http.Request, secret string) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) <= len(prefix) {
		return false
	}
	token := auth[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1
}
