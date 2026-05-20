package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"windshift/internal/utils"
)

// ModelRefresher fetches a provider's /models catalog and writes it to the
// ModelCache. Every call is admin-triggered — there is no background timer,
// so an airgapped deployment never makes outbound HTTP unless an admin asks.
type ModelRefresher struct {
	cache *ModelCache
	http  *http.Client
}

// NewModelRefresher constructs a ModelRefresher that uses the SSRF-safe HTTP
// client shared with the inference clients.
func NewModelRefresher(cache *ModelCache) *ModelRefresher {
	return newModelRefresherWithClient(cache, utils.NewSSRFSafeHTTPClient(30*time.Second))
}

// newModelRefresherWithClient lets tests substitute the SSRF-safe client
// (which blocks loopback) with a vanilla one so httptest servers work.
func newModelRefresherWithClient(cache *ModelCache, client *http.Client) *ModelRefresher {
	return &ModelRefresher{cache: cache, http: client}
}

// modelsResponse mirrors the OpenAI /v1/models shape, which OpenRouter and
// other OpenAI-compatible servers also emit. Fields we don't surface (pricing,
// architecture, …) are ignored.
type modelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		ContextLength int    `json:"context_length"`
	} `json:"data"`
}

// Refresh fetches and caches the model list for one provider. On failure it
// also writes the error string to the cache so the UI can render "Last attempt
// failed: …" without losing the previously cached models.
func (r *ModelRefresher) Refresh(ctx context.Context, provider ProviderInfo, apiKey string) ([]ModelInfo, error) {
	if !provider.HasDynamicModels() {
		return nil, fmt.Errorf("provider %q has no models_endpoint configured", provider.Type)
	}
	url := strings.TrimSuffix(provider.BaseURL, "/") + provider.ModelsEndpoint
	if err := utils.ValidateHTTPBaseURL(url); err != nil {
		return nil, fmt.Errorf("invalid models URL: %w", err)
	}

	models, err := r.fetch(ctx, url, apiKey)
	if err != nil {
		if cacheErr := r.cache.SaveFailure(provider.Type, err, time.Now()); cacheErr != nil {
			return nil, fmt.Errorf("%w (also failed to persist error: %v)", err, cacheErr)
		}
		return nil, err
	}
	if err := r.cache.SaveSuccess(provider.Type, models, time.Now()); err != nil {
		return nil, err
	}
	return models, nil
}

func (r *ModelRefresher) fetch(ctx context.Context, url, apiKey string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrConnectionFailed, err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConnectionFailed, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // cap at 4 MiB
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %v", ErrConnectionFailed, err)
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, fmt.Errorf("%w: HTTP 503", ErrServiceNotReady)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrAPIError, resp.StatusCode, truncateBody(body))
	}

	var parsed modelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w: decode response: %v", ErrAPIError, err)
	}

	out := make([]ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, ModelInfo{ID: m.ID, Name: name, MaxTokens: m.ContextLength})
	}
	return out, nil
}

func truncateBody(b []byte) string {
	const maxLen = 256
	if len(b) <= maxLen {
		return string(b)
	}
	return string(b[:maxLen]) + "…"
}
