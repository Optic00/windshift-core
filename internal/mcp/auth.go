package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"windshift/internal/aitools"
	"windshift/internal/auth"
	"windshift/internal/models"
	"windshift/internal/restapi"
)

const mcpAuthInspectionMaxBytes = 1 << 20

// AuthConfig contains the OAuth resource metadata advertised by the MCP
// bearer-token middleware. Empty values preserve the legacy PAT-only mode.
type AuthConfig struct {
	ResourceURI         string
	ResourceMetadataURI string
}

// contextKey is a private type for context values set by this package so
// they can't collide with (or be forged by) values from other packages.
type contextKey string

// contextKeyAPIToken carries the validated *models.APIToken for the request
// so tool dispatch can enforce per-tool token scopes (see tools_registry.go).
const contextKeyAPIToken contextKey = "apiToken"

// bearerAuthMiddleware retains the original unit-test and PAT-facing helper.
//
//nolint:unused // exercised by the private test overlay and kept for PAT-mode coverage
func bearerAuthMiddleware(tokenManager *auth.TokenManager, next http.Handler) http.Handler {
	return bearerAuthMiddlewareWithConfig(tokenManager, AuthConfig{}, next)
}

// bearerAuthMiddlewareWithConfig validates Bearer tokens, enforces the MCP
// resource audience for OAuth-issued tokens, and emits standards-compliant
// discovery and incremental-scope challenges.
func bearerAuthMiddlewareWithConfig(tokenManager *auth.TokenManager, cfg AuthConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeMCPAuthError(w, http.StatusUnauthorized, cfg, auth.DefaultAgentScopes, "", "missing or invalid Authorization header")
			return
		}

		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		user, apiToken, err := tokenManager.ValidateToken(token)
		if err != nil {
			writeMCPAuthError(w, http.StatusUnauthorized, cfg, auth.DefaultAgentScopes, "", "invalid token")
			return
		}

		// PATs intentionally remain a supported fallback. OAuth-issued tokens
		// carry a client id and must be bound to this exact RFC 8707 audience.
		if cfg.ResourceURI != "" && apiToken.OAuthClientID != "" && apiToken.OAuthResource != cfg.ResourceURI {
			writeMCPAuthError(w, http.StatusUnauthorized, cfg, auth.DefaultAgentScopes, "", "token was not issued for this resource")
			return
		}

		if !tokenManager.CheckTokenPermissions(apiToken, []string{auth.ScopeMCPAccess}) {
			writeMCPAuthError(w, http.StatusForbidden, cfg, auth.DefaultAgentScopes, "insufficient_scope", "token missing required scope: mcp:access")
			return
		}

		missing, err := missingToolScopes(r, tokenManager, apiToken)
		if err != nil {
			writeMCPJSONError(w, http.StatusRequestEntityTooLarge, "MCP request body is too large")
			return
		}
		if len(missing) > 0 {
			required := incrementalOAuthScopes(apiToken.Permissions, missing)
			writeMCPAuthError(w, http.StatusForbidden, cfg, required, "insufficient_scope",
				fmt.Sprintf("token missing required scope: %s", strings.Join(missing, ", ")))
			return
		}

		// Keep the validated token alongside the user: mcp:access only gates
		// entry to the surface, while tools_registry.go repeats the fine-grained
		// check at dispatch as defense in depth.
		ctx := context.WithValue(r.Context(), restapi.ContextKeyUser, user)
		ctx = context.WithValue(ctx, contextKeyAPIToken, apiToken)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func missingToolScopes(r *http.Request, tokenManager *auth.TokenManager, token *models.APIToken) ([]string, error) {
	if r.Method != http.MethodPost || r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, mcpAuthInspectionMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > mcpAuthInspectionMaxBytes {
		return nil, fmt.Errorf("request body exceeds inspection limit")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var request struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &request); err != nil || request.Method != "tools/call" {
		return nil, nil //nolint:nilerr // malformed JSON is reported by the MCP transport
	}
	entry, ok := aitools.Default.Lookup(request.Params.Name)
	if !ok {
		return nil, nil
	}
	missing := make([]string, 0, len(entry.Scopes))
	for _, scope := range entry.Scopes {
		if !tokenManager.CheckTokenPermissions(token, []string{scope}) {
			missing = append(missing, scope)
		}
	}
	return missing, nil
}

func incrementalOAuthScopes(rawCurrent string, missing []string) []string {
	var current []string
	_ = json.Unmarshal([]byte(rawCurrent), &current)
	set := map[string]struct{}{auth.ScopeMCPAccess: {}}
	for _, scope := range append(append([]string{}, current...), missing...) {
		if auth.IsAdminScope(scope) {
			continue
		}
		if auth.ValidateScopes([]string{scope}) == nil {
			set[scope] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for scope := range set {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

func writeMCPAuthError(w http.ResponseWriter, status int, cfg AuthConfig, scopes []string, oauthError, message string) {
	params := make([]string, 0, 3)
	if cfg.ResourceMetadataURI != "" {
		params = append(params, fmt.Sprintf(`resource_metadata=%q`, cfg.ResourceMetadataURI))
	}
	if oauthError != "" {
		params = append(params, fmt.Sprintf(`error=%q`, oauthError))
	}
	if len(scopes) > 0 {
		params = append(params, fmt.Sprintf(`scope=%q`, strings.Join(scopes, " ")))
	}
	challenge := "Bearer"
	if len(params) > 0 {
		challenge += " " + strings.Join(params, ", ")
	}
	w.Header().Set("WWW-Authenticate", challenge)
	writeMCPJSONError(w, status, message)
}

func writeMCPJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// userFromContext extracts the authenticated user from context.
func userFromContext(ctx context.Context) *models.User {
	if user, ok := ctx.Value(restapi.ContextKeyUser).(*models.User); ok {
		return user
	}
	return nil
}

// apiTokenFromContext extracts the validated API token from context.
func apiTokenFromContext(ctx context.Context) *models.APIToken {
	if token, ok := ctx.Value(contextKeyAPIToken).(*models.APIToken); ok {
		return token
	}
	return nil
}
