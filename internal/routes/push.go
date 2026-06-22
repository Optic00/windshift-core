package routes

import "net/http"

// RegisterPushRoutes wires the Web Push subscription lifecycle. All endpoints
// require authentication and are scoped to the current user inside the handler.
func RegisterPushRoutes(deps *Deps) {
	if deps.Push == nil {
		return
	}
	api := deps.API
	auth := deps.AuthMiddleware.RequireAuth

	api.HandleH("GET /push/vapid-public-key", auth(http.HandlerFunc(deps.Push.GetVAPIDKey)))
	api.HandleH("GET /push/subscriptions", auth(http.HandlerFunc(deps.Push.List)))
	api.HandleH("POST /push/subscriptions", auth(http.HandlerFunc(deps.Push.Subscribe)))
	api.HandleH("DELETE /push/subscriptions/{id}", auth(http.HandlerFunc(deps.Push.Delete)))
}
