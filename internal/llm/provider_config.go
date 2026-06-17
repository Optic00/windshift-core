package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateProviderConfig verifies the per-connection provider_config blob.
// It is intentionally generic: any provider can use it, but it must be a JSON
// object because it is merged into the provider request body.
func ValidateProviderConfig(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return fmt.Errorf("provider_config must be valid JSON: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("provider_config must be a JSON object")
	}
	return nil
}

// MergeProviderConfig adds provider_config fields to an in-memory request
// body. Existing generated request fields win, so config cannot replace the
// prompt, model, tools, or other fields already set by the caller.
func MergeProviderConfig(body map[string]interface{}, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return fmt.Errorf("provider_config must be valid JSON: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("provider_config must be a JSON object")
	}
	for k, v := range cfg {
		if _, exists := body[k]; exists {
			continue
		}
		var decoded interface{}
		if err := json.Unmarshal(v, &decoded); err != nil {
			return fmt.Errorf("provider_config.%s must be valid JSON: %w", k, err)
		}
		body[k] = decoded
	}
	return nil
}

// MergeProviderConfigJSON adds provider_config fields to a raw JSON request
// body. It is used by the coding-agent proxy path, where the runner owns the
// OpenAI-compatible request body and the broker only injects connection config.
func MergeProviderConfigJSON(body []byte, raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return body, nil
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("request body must be a JSON object: %w", err)
	}
	if request == nil {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("provider_config must be valid JSON: %w", err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("provider_config must be a JSON object")
	}
	for k, v := range cfg {
		if _, exists := request[k]; exists {
			continue
		}
		request[k] = v
	}
	merged, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal provider-configured request: %w", err)
	}
	return merged, nil
}

func marshalWithProviderConfig(base interface{}, raw string) ([]byte, error) {
	body, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	return MergeProviderConfigJSON(body, raw)
}
