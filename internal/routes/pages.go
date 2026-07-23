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
	api.HandleH("GET /workspaces/{workspaceId}/pages/archived", auth(http.HandlerFunc(deps.Pages.Page.ListArchived)))
	api.HandleH("GET /workspaces/{workspaceId}/pages/search", auth(http.HandlerFunc(deps.Pages.Page.Search)))
	api.HandleH("POST /workspaces/{workspaceId}/pages", auth(http.HandlerFunc(deps.Pages.Page.Create)))
	api.HandleH("GET /workspaces/{workspaceId}/pages/{pageId}", auth(http.HandlerFunc(deps.Pages.Page.Get)))
	api.HandleH("POST /workspaces/{workspaceId}/pages/{pageId}/unarchive", auth(http.HandlerFunc(deps.Pages.Page.Unarchive)))
	api.HandleH("PUT /workspaces/{workspaceId}/pages/{pageId}", auth(http.HandlerFunc(deps.Pages.Page.Update)))
	api.HandleH("DELETE /workspaces/{workspaceId}/pages/{pageId}", auth(http.HandlerFunc(deps.Pages.Page.Delete)))
	api.HandleH("POST /workspaces/{workspaceId}/pages/{pageId}/move", auth(http.HandlerFunc(deps.Pages.Page.Move)))
	api.HandleH("GET /workspaces/{workspaceId}/pages/{pageId}/history", auth(http.HandlerFunc(deps.Pages.Page.GetHistory)))
	api.HandleH("GET /workspaces/{workspaceId}/pages/{pageId}/history/{revisionId}", auth(http.HandlerFunc(deps.Pages.Page.GetRevision)))
	api.HandleH("POST /workspaces/{workspaceId}/pages/{pageId}/history/{revisionId}/restore", auth(http.HandlerFunc(deps.Pages.Page.RestoreRevision)))
	api.HandleH("GET /workspaces/{workspaceId}/pages/{pageId}/permissions", auth(http.HandlerFunc(deps.Pages.Page.GetPermissions)))
	api.HandleH("POST /workspaces/{workspaceId}/pages/{pageId}/permissions", auth(http.HandlerFunc(deps.Pages.Page.GrantPermission)))
	api.HandleH("DELETE /workspaces/{workspaceId}/pages/{pageId}/permissions/{permissionId}", auth(http.HandlerFunc(deps.Pages.Page.RevokePermission)))
	api.HandleH("PATCH /workspaces/{workspaceId}/pages/{pageId}/inheritance", auth(http.HandlerFunc(deps.Pages.Page.SetInheritance)))
	api.HandleH("GET /workspaces/{workspaceId}/pages/{pageId}/diagrams", auth(http.HandlerFunc(deps.Pages.Page.ListDiagrams)))
	api.HandleH("POST /workspaces/{workspaceId}/pages/{pageId}/diagrams", auth(http.HandlerFunc(deps.Pages.Page.CreateDiagram)))
	api.HandleH("GET /workspaces/{workspaceId}/pages/{pageId}/diagrams/{attachmentId}", auth(http.HandlerFunc(deps.Pages.Page.GetDiagram)))
	api.HandleH("PUT /workspaces/{workspaceId}/pages/{pageId}/diagrams/{attachmentId}", auth(http.HandlerFunc(deps.Pages.Page.UpdateDiagram)))

	if deps.Pages.KnowledgeSearch != nil {
		api.HandleH("GET /workspaces/{workspaceId}/knowledge/search", auth(http.HandlerFunc(deps.Pages.KnowledgeSearch.Search)))
	}

	if deps.Pages.PageLabel != nil {
		// Workspace-scoped label CRUD. Permission failures inside the
		// handler return 404 (memory: workspace-resource access checks
		// must not leak existence).
		api.HandleH("GET /workspaces/{workspaceId}/page-labels", auth(http.HandlerFunc(deps.Pages.PageLabel.List)))
		api.HandleH("POST /workspaces/{workspaceId}/page-labels", auth(http.HandlerFunc(deps.Pages.PageLabel.Create)))
		api.HandleH("GET /workspaces/{workspaceId}/page-labels/{labelId}", auth(http.HandlerFunc(deps.Pages.PageLabel.Get)))
		api.HandleH("PUT /workspaces/{workspaceId}/page-labels/{labelId}", auth(http.HandlerFunc(deps.Pages.PageLabel.Update)))
		api.HandleH("DELETE /workspaces/{workspaceId}/page-labels/{labelId}", auth(http.HandlerFunc(deps.Pages.PageLabel.Delete)))

		// Page-scoped attachments — gated per-page via PagePermissionService.
		api.HandleH("GET /workspaces/{workspaceId}/pages/{pageId}/labels", auth(http.HandlerFunc(deps.Pages.PageLabel.ListForPage)))
		api.HandleH("PUT /workspaces/{workspaceId}/pages/{pageId}/labels", auth(http.HandlerFunc(deps.Pages.PageLabel.SetForPage)))
		api.HandleH("POST /workspaces/{workspaceId}/pages/{pageId}/labels", auth(http.HandlerFunc(deps.Pages.PageLabel.AddToPage)))
		api.HandleH("DELETE /workspaces/{workspaceId}/pages/{pageId}/labels/{labelId}", auth(http.HandlerFunc(deps.Pages.PageLabel.RemoveFromPage)))
	}
}
