package routes

import "net/http"

// RegisterPageRoutes registers workspace knowledge-pages endpoints. All
// routes are workspace-scoped; permission failures inside the handlers
// return 404 (memory: workspace-resource access checks must not leak
// existence).
func RegisterPageRoutes(deps *Deps) {
	if deps.Pages.Page == nil {
		return
	}
	api := deps.API
	auth := deps.AuthMiddleware.RequireAuth

	api.HandleH("GET /workspaces/{workspaceId}/pages/tree", auth(http.HandlerFunc(deps.Pages.Page.GetTree)))
	api.HandleH("POST /workspaces/{workspaceId}/pages", auth(http.HandlerFunc(deps.Pages.Page.Create)))
	api.HandleH("GET /workspaces/{workspaceId}/pages/{pageId}", auth(http.HandlerFunc(deps.Pages.Page.Get)))
	api.HandleH("PUT /workspaces/{workspaceId}/pages/{pageId}", auth(http.HandlerFunc(deps.Pages.Page.Update)))
	api.HandleH("DELETE /workspaces/{workspaceId}/pages/{pageId}", auth(http.HandlerFunc(deps.Pages.Page.Delete)))
	api.HandleH("POST /workspaces/{workspaceId}/pages/{pageId}/move", auth(http.HandlerFunc(deps.Pages.Page.Move)))
	api.HandleH("GET /workspaces/{workspaceId}/pages/{pageId}/history", auth(http.HandlerFunc(deps.Pages.Page.GetHistory)))
	api.HandleH("GET /workspaces/{workspaceId}/pages/{pageId}/history/{revisionId}", auth(http.HandlerFunc(deps.Pages.Page.GetRevision)))
	api.HandleH("POST /workspaces/{workspaceId}/pages/{pageId}/history/{revisionId}/restore", auth(http.HandlerFunc(deps.Pages.Page.RestoreRevision)))
	api.HandleH("GET /workspaces/{workspaceId}/pages/{pageId}/permissions", auth(http.HandlerFunc(deps.Pages.Page.GetPermissions)))
}
