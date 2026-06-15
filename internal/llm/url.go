package llm

import (
	"net/http"
	"strings"
	"time"

	"windshift/internal/utils"
)

// joinProviderPath appends an API path to an admin-configured provider base URL.
//
// Many OpenAI-compatible tools document their base URL as .../v1, while the
// built-in Windshift provider definitions keep the host/root URL and store the
// /v1/... suffix in ChatPath/ModelsEndpoint. Accept both forms so a custom
// endpoint entered as http://localhost:11434/v1 does not become
// http://localhost:11434/v1/v1/chat/completions.
func joinProviderPath(baseURL, apiPath string) string {
	base := strings.TrimRight(baseURL, "/")
	path := "/" + strings.TrimLeft(apiPath, "/")
	if base == "" {
		return path
	}
	if strings.HasPrefix(path, "/v1/") && strings.HasSuffix(base, "/v1") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return base + path
}

// newAdminConfiguredHTTPClient returns a client for URLs configured by a system
// administrator. It is SSRF-safe by default (blocks loopback/private/metadata);
// operators reach local/internal LLM endpoints with the global
// --allow-local-connections switch, which utils.NewSSRFSafeHTTPClient honors.
func newAdminConfiguredHTTPClient(timeout time.Duration) *http.Client {
	if timeout == 0 {
		timeout = DefaultRequestTimeout
	}
	return utils.NewSSRFSafeHTTPClient(timeout)
}
