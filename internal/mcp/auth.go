package mcp

import (
	"context"
	"net/http"
	"strings"

	"windshift/internal/auth"
	"windshift/internal/models"
	"windshift/internal/restapi"
)

// contextKey is a private type for context values set by this package so
// they can't collide with (or be forged by) values from other packages.
type contextKey string

// contextKeyAPIToken carries the validated *models.APIToken for the request
// so tool dispatch can enforce per-tool token scopes (see tools_registry.go).
const contextKeyAPIToken contextKey = "apiToken"

// bearerAuthMiddleware validates Bearer tokens and injects the user into context.
func bearerAuthMiddleware(tokenManager *auth.TokenManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, `{"error":"missing or invalid Authorization header"}`, http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(header, "Bearer ")
		user, apiToken, err := tokenManager.ValidateToken(token)
		if err != nil {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}

		if !tokenManager.CheckTokenPermissions(apiToken, []string{auth.ScopeMCPAccess}) {
			http.Error(w, `{"error":"token missing required scope: mcp:access"}`, http.StatusForbidden)
			return
		}

		// Keep the validated token alongside the user: mcp:access only
		// gates entry to the surface, while the per-tool scope check in
		// tools_registry.go enforces the token's fine-grained scopes
		// (items:delete, actions:write, pages:write, ...) at dispatch.
		ctx := context.WithValue(r.Context(), restapi.ContextKeyUser, user)
		ctx = context.WithValue(ctx, contextKeyAPIToken, apiToken)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
