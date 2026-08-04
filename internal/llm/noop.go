package llm

import "context"

// noopClient is returned when LLM_ENDPOINT is not configured.
// All methods return ErrNotConfigured.
type noopClient struct{}

func (c *noopClient) Complete(_ context.Context, _ CompletionRequest) (*CompletionResponse, error) {
	return nil, ErrNotConfigured
}

func (c *noopClient) Health(_ context.Context) error {
	return ErrNotConfigured
}

func (c *noopClient) Available() bool {
	return false
}
