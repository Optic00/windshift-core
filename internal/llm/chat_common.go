package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
)

const (
	modernCompletionTokenParameter = "max_completion_tokens"
	legacyCompletionTokenParameter = "max_tokens"
	responsesTokenParameter        = "max_output_tokens"
)

// completionTokenNegotiator consolidates the two token-limit fields used by
// OpenAI-compatible chat APIs. New requests use OpenAI's current
// max_completion_tokens field. If an endpoint explicitly rejects that field,
// the failed request is retried once with max_tokens and the client remembers
// the legacy capability for subsequent calls.
type completionTokenNegotiator struct {
	useLegacy atomic.Bool
}

func (n *completionTokenNegotiator) parameter() string {
	if n.useLegacy.Load() {
		return legacyCompletionTokenParameter
	}
	return modernCompletionTokenParameter
}

func (n *completionTokenNegotiator) post(
	ctx context.Context,
	hc *http.Client,
	url string,
	apiKey string,
	body map[string]interface{},
) (*CompletionResponse, error) {
	result, err := postChatCompletion(ctx, hc, url, apiKey, body)
	if err == nil || !rejectsModernCompletionTokenParameter(err) {
		return result, err
	}

	limit, usedModernParameter := body[modernCompletionTokenParameter]
	if !usedModernParameter {
		return nil, err
	}
	delete(body, modernCompletionTokenParameter)
	body[legacyCompletionTokenParameter] = limit
	n.useLegacy.Store(true)
	return postChatCompletion(ctx, hc, url, apiKey, body)
}

func rejectsModernCompletionTokenParameter(err error) bool {
	if !errors.Is(err, ErrAPIError) {
		return false
	}
	message := strings.ToLower(err.Error())
	if (!strings.Contains(message, "status 400") && !strings.Contains(message, "status 422")) ||
		!strings.Contains(message, modernCompletionTokenParameter) {
		return false
	}
	for _, marker := range []string{
		"unsupported parameter",
		"not supported",
		"unknown parameter",
		"unknown field",
		"unrecognized request argument",
		"extra inputs are not permitted",
		"invalid parameter",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// isCompletionTokenLimitError identifies a provider response that proves the
// health probe reached a model but exhausted the probe's intentionally tiny
// output budget. It remains deliberately narrower than general API-error
// handling so authentication, model, quota, and malformed-request failures are
// not mistaken for a healthy connection.
func isCompletionTokenLimitError(err error) bool {
	if !errors.Is(err, ErrAPIError) {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "status 400") && !strings.Contains(message, "status 422") {
		return false
	}
	hasTokenLimit := strings.Contains(message, modernCompletionTokenParameter) ||
		strings.Contains(message, legacyCompletionTokenParameter) ||
		strings.Contains(message, responsesTokenParameter) ||
		strings.Contains(message, "model output limit")
	return hasTokenLimit && (strings.Contains(message, "reached") || strings.Contains(message, "exceeded"))
}

// baseChatBody assembles the fields every OpenAI-compatible chat completion request
// shares: messages, optional model, temperature, a provider-specific completion
// token limit, tools, and tool_choice.
// Provider-specific extras (grammar, response_format, etc.) are added by the caller.
func baseChatBody(req CompletionRequest, model, completionTokenParameter string) map[string]interface{} {
	body := map[string]interface{}{
		"messages": req.Messages,
	}
	if model != "" {
		body["model"] = model
	}
	if req.Temperature != 0 {
		body["temperature"] = req.Temperature
	}
	if req.MaxTokens != 0 {
		if completionTokenParameter == "" {
			completionTokenParameter = modernCompletionTokenParameter
		}
		body[completionTokenParameter] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
		if req.ToolChoice != nil {
			body["tool_choice"] = req.ToolChoice
		}
	}
	return body
}

// postChatCompletion marshals body, POSTs it with the given auth token, and
// decodes the response. The caller is responsible for setting Content-Type via
// the Authorization flag (empty apiKey means the Authorization header is omitted).
func postChatCompletion(ctx context.Context, hc *http.Client, url, apiKey string, body map[string]interface{}) (*CompletionResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := hc.Do(httpReq) //nolint:gosec // URL from server-configured LLM endpoint
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	return decodeCompletionResponse(resp)
}
