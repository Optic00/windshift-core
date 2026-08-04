package llm

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// DefaultRequestTimeout bounds a single in-product AI operation end to end. It
// is the one knob behind every in-product AI feature's time budget: the LLM
// client's per-call HTTP timeout, the agentic loop's overall budget (RunAgent),
// and the request/job context that the AI handlers and the briefing scheduler
// wrap around their LLM calls all derive from it. It is deliberately generous —
// multi-iteration agentic chat routinely runs longer than a minute — but
// bounded so a hung upstream cannot pin a connection or goroutine indefinitely.
//
// The coding-agent runner path (runner_broker) has its own, much longer budget
// and is intentionally independent of this value.
const DefaultRequestTimeout = 5 * time.Minute

// Client provides a provider-neutral interface to an LLM API.
type Client interface {
	// Complete sends a normalized generation request and returns its result.
	Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
	// Health checks if the LLM service is healthy.
	Health(ctx context.Context) error
	// Available returns true if the LLM service is configured.
	Available() bool
}

// Config contains configuration for the LLM client.
type Config struct {
	Endpoint string        // Base URL (e.g., http://llm:8081)
	APIKey   string        // Bearer token for authenticated endpoints
	Timeout  time.Duration // HTTP timeout (default: DefaultRequestTimeout)
}

// NewClient creates a new LLM client.
// Returns a noopClient if the endpoint is empty.
func NewClient(cfg Config) Client {
	endpoint := strings.TrimSuffix(cfg.Endpoint, "/")
	if endpoint == "" {
		return &noopClient{}
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultRequestTimeout
	}

	return &httpClient{
		endpoint: endpoint,
		apiKey:   cfg.APIKey,
		http:     newAdminConfiguredHTTPClient(timeout),
	}
}

// httpClient implements Client using HTTP requests to an OpenAI-compatible API.
type httpClient struct {
	endpoint         string
	apiKey           string
	completionTokens completionTokenNegotiator
	http             *http.Client
}

func (c *httpClient) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	body := baseChatBody(req, req.Model, c.completionTokens.parameter())

	// llama.cpp takes a GBNF grammar for structured output.
	if req.StructuredOutput != nil && len(req.StructuredOutput.Schema) > 0 {
		grammar, err := JSONSchemaToGBNF(req.StructuredOutput.Schema)
		if err != nil {
			slog.Warn("failed to generate GBNF grammar", slog.Any("error", err))
		} else if grammar != "" {
			slog.Debug("applying GBNF grammar", slog.Int("length", len(grammar)))
			body["grammar"] = grammar
		}
	}

	return c.completionTokens.post(ctx, c.http, c.endpoint+"/v1/chat/completions", c.apiKey, body)
}

func (c *httpClient) Health(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.endpoint+"/health", http.NoBody)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(httpReq) //nolint:gosec // G704: admin-configured LLM endpoint
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return ErrServiceNotReady
	}
	return nil
}

func (c *httpClient) Available() bool {
	return true
}

// ConnectionConfig holds configuration for creating a provider-specific client.
type ConnectionConfig struct {
	ProviderType   ProviderType
	Model          string
	APIKey         string
	BaseURL        string
	ProviderConfig string
	Timeout        time.Duration
}

// NewProviderClient creates a Client for a specific LLM provider.
func NewProviderClient(cfg ConnectionConfig) Client {
	provider := GetProvider(cfg.ProviderType)
	if provider == nil {
		return &noopClient{}
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = provider.BaseURL
	}

	contract := ProviderConfigAPIContract(cfg.ProviderConfig)
	usesCatalogEndpoint := cfg.BaseURL == "" || strings.TrimRight(cfg.BaseURL, "/") == strings.TrimRight(provider.BaseURL, "/")
	switch {
	case provider.APIFormat == "anthropic":
		return newAnthropicClient(baseURL, cfg.Model, cfg.APIKey, cfg.ProviderConfig, cfg.Timeout)
	case contract == APIContractResponses:
		return newOpenAIResponsesClient(baseURL, cfg.Model, cfg.APIKey, cfg.ProviderConfig, cfg.Timeout)
	case contract == APIContractChatCompletions:
		return newOpenAIClient(baseURL, cfg.Model, cfg.APIKey, cfg.ProviderConfig, cfg.Timeout, provider.ChatPath)
	case provider.APIFormat == "openai-responses" && usesCatalogEndpoint:
		return newOpenAIResponsesClient(baseURL, cfg.Model, cfg.APIKey, cfg.ProviderConfig, cfg.Timeout)
	default:
		// A custom base URL on an OpenAI connection may be LiteLLM or another
		// OpenAI-compatible gateway. Preserve Chat Completions unless the
		// connection explicitly opts into the Responses contract.
		return newOpenAIClient(baseURL, cfg.Model, cfg.APIKey, cfg.ProviderConfig, cfg.Timeout, provider.ChatPath)
	}
}
