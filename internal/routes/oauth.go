package routes

import "net/http"

// RegisterOAuthRoutes configures OAuth consent and token endpoints.
// Consent uses sessions; token exchange is public and IP-rate-limited.
func RegisterOAuthRoutes(deps *Deps) {
	if deps.Users.OAuth == nil {
		return
	}
	api := deps.API
	auth := deps.AuthMiddleware.RequireAuth

	api.HandleH("GET /oauth/authorize/info", auth(http.HandlerFunc(deps.Users.OAuth.AuthorizeInfo)))
	api.HandleH("POST /oauth/authorize/approve", auth(http.HandlerFunc(deps.Users.OAuth.AuthorizeApprove)))
	api.HandleH("POST /oauth/authorize/deny", auth(http.HandlerFunc(deps.Users.OAuth.AuthorizeDeny)))

	// Userinfo accepts OAuth bearer tokens, not cookie authentication.
	api.HandleH("GET /oauth/userinfo", http.HandlerFunc(deps.Users.OAuth.Userinfo))

	// Token exchange needs the dedicated IP limiter because it is unauthenticated.
	api.HandleH("POST /oauth/token", deps.OAuthTokenLimiter.Limit(http.HandlerFunc(deps.Users.OAuth.Token)))

	if !deps.Users.OAuth.MCPDiscoveryEnabled() {
		return
	}

	// Dynamic registration shares the token endpoint's IP limiter.
	api.HandleH("POST /oauth/register", deps.OAuthTokenLimiter.Limit(http.HandlerFunc(deps.Users.OAuth.RegisterDynamicClient)))
	deps.Mux.Handle("GET /.well-known/oauth-protected-resource", http.HandlerFunc(deps.Users.OAuth.ProtectedResourceMetadata))
	deps.Mux.Handle("GET /.well-known/oauth-protected-resource/mcp", http.HandlerFunc(deps.Users.OAuth.ProtectedResourceMetadata))
	deps.Mux.Handle("GET /.well-known/oauth-authorization-server", http.HandlerFunc(deps.Users.OAuth.AuthorizationServerMetadata))
}
