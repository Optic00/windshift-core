package llm

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// ProviderType identifies an LLM provider.
type ProviderType string

//go:embed llm_providers.json
var defaultProvidersJSON []byte

// providerRegistry holds the loaded provider list.
var (
	providerMu       sync.RWMutex
	providerRegistry []ProviderInfo
)

// ModelInfo describes a model offered by a provider.
type ModelInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MaxTokens int    `json:"max_tokens"`
}

// ProviderInfo describes a known LLM provider and its available models.
//
// When ModelsEndpoint is set, the provider supports a `/models`-style catalog
// that the admin can refresh on demand; the static Models slice then acts as
// a seed (typically empty) and the live picker reads from the cache table.
//
// ModelsBaseURL / ModelsAuthScheme / ModelsResponseFormat let providers whose
// catalog endpoint diverges from the chat endpoint (different host, different
// auth, different response shape) plug into the same refresher. Gemini is the
// motivating case — its OpenAI-compatible chat lives under /v1beta/openai, but
// the catalog is at /v1beta/models with a Google-specific shape.
type ProviderInfo struct {
	Type                 ProviderType `json:"type"`
	Name                 string       `json:"name"`
	APIFormat            string       `json:"api_format"`
	ChatPath             string       `json:"chat_path,omitempty"`
	BaseURL              string       `json:"base_url"`
	ModelsEndpoint       string       `json:"models_endpoint,omitempty"`
	ModelsBaseURL        string       `json:"models_base_url,omitempty"`
	ModelsAuthScheme     string       `json:"models_auth_scheme,omitempty"`
	ModelsResponseFormat string       `json:"models_response_format,omitempty"`
	Models               []ModelInfo  `json:"models"`
}

// HasDynamicModels reports whether the provider exposes a `/models` catalog
// that we can refresh into the cache.
func (p *ProviderInfo) HasDynamicModels() bool {
	return p != nil && p.ModelsEndpoint != ""
}

// ModelsURL returns the absolute URL of the provider's catalog endpoint,
// preferring ModelsBaseURL when set (Gemini) and falling back to BaseURL.
func (p *ProviderInfo) ModelsURL() string {
	if p == nil || p.ModelsEndpoint == "" {
		return ""
	}
	base := p.ModelsBaseURL
	if base == "" {
		base = p.BaseURL
	}
	return trimSlash(base) + p.ModelsEndpoint
}

// AuthScheme returns the auth scheme used for the catalog request; defaults
// to "bearer" so existing OpenAI-compatible providers keep working unchanged.
func (p *ProviderInfo) AuthScheme() string {
	if p == nil || p.ModelsAuthScheme == "" {
		return "bearer"
	}
	return p.ModelsAuthScheme
}

// ResponseFormat returns the catalog response shape; defaults to "openai"
// (the OpenAI/OpenRouter `{"data":[…]}` shape).
func (p *ProviderInfo) ResponseFormat() string {
	if p == nil || p.ModelsResponseFormat == "" {
		return "openai"
	}
	return p.ModelsResponseFormat
}

func trimSlash(s string) string {
	for s != "" && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// providersFile is the JSON structure for the providers file.
type providersFile struct {
	Providers []ProviderInfo `json:"providers"`
}

// LoadProviders reads and parses an LLM providers JSON file.
func LoadProviders(filePath string) error {
	data, err := os.ReadFile(filePath) //nolint:gosec // G304 — filePath from trusted CLI flag (-llm-providers)
	if err != nil {
		return fmt.Errorf("read providers file: %w", err)
	}
	return loadProvidersFromJSON(data)
}

// LoadDefaultProviders loads providers from the embedded default JSON.
func LoadDefaultProviders() {
	if err := loadProvidersFromJSON(defaultProvidersJSON); err != nil {
		// This should never happen since the embedded JSON is compiled in.
		panic(fmt.Sprintf("failed to parse embedded llm_providers.json: %v", err))
	}
}

// loadProvidersFromJSON parses JSON bytes into the provider registry.
func loadProvidersFromJSON(data []byte) error {
	var f providersFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse providers JSON: %w", err)
	}
	providerMu.Lock()
	providerRegistry = f.Providers
	providerMu.Unlock()
	return nil
}

// GetProviders returns the loaded list of providers.
func GetProviders() []ProviderInfo {
	providerMu.RLock()
	defer providerMu.RUnlock()
	return providerRegistry
}

// GetProvider looks up a provider by type. Returns nil if not found.
func GetProvider(pt ProviderType) *ProviderInfo {
	providerMu.RLock()
	defer providerMu.RUnlock()
	for i := range providerRegistry {
		if providerRegistry[i].Type == pt {
			return &providerRegistry[i]
		}
	}
	return nil
}

// KnownProviders returns the list of supported LLM providers.
// Kept for backward compatibility; delegates to GetProviders.
func KnownProviders() []ProviderInfo {
	return GetProviders()
}
