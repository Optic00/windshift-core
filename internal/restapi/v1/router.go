// Package v1 provides REST API version 1 endpoints and routing.
//
// # Auth boundary
//
// This package is the only surface that accepts API bearer tokens
// (Authorization: Bearer crw_*). The cookie-auth surface (/api/*,
// internal/middleware/auth.go) explicitly rejects bearer tokens — that
// would be a back door around the per-route token-scope checks below.
//
// Auth method by surface:
//   - /rest/api/v1/* (here): Authorization: Bearer crw_* — scope-checked.
//   - /api/* (cookie-auth): Cookie / X-Session-Token. No bearer.
//   - /api/internal/* (sidecar RPC): X-Internal-Service-Auth shared secret.
//
// # Routing convention
//
// New routes should follow the rules below so that token scopes,
// in-handler permission checks, and the user/workspace permission model
// stay aligned across the surface.
//
// Workspace-content resources (items, milestones, iterations, projects,
// comments) live at /workspaces/{id}/<resource>[...]. They are gated by:
//   - bearerAuth.RequirePermission("items:<read|write|delete>") at the route
//   - BaseHandler.RequireWorkspace<View|Edit>Access in the handler
//
// The URL constraint plus the in-handler check together guarantee a token
// can only reach resources in workspaces where the user holds the matching
// PermissionItem<View|Edit>. Global mirror routes for the same resources
// (e.g. /milestones, /iterations, /projects) remain available for cross-
// workspace use cases (search, dashboards) but their handlers must look up
// the resource's scope and apply the workspace check for workspace-scoped
// rows; see requireMilestoneAccessByID etc. in handlers/planning.go for
// the canonical pattern.
//
// Workspace-config resources (statuses, item-types, priorities, custom-
// fields) live at /workspaces/{id}/<resource> for read, gated by
// bearerAuth.RequirePermission("workspaces:read") + RequireWorkspaceViewAccess.
// Mutations are admin-only and sit on global routes.
//
// Truly global resources (workflows, status-categories, users) keep their
// global URLs and dedicated scopes (workflows:read, statuses:read,
// users:read).
//
// System-admin bypass intentionally does not apply to token-scope checks.
// PermissionService.HasWorkspacePermission and HasGlobalPermission auto-
// satisfy admins, so the in-handler check is bypassed; the bearer-token
// scope check still requires the scope to be present on the token. This
// is defense-in-depth: a token issued for a specific bot should not
// inherit owner privileges by accident.
package v1

import (
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/restapi/v1/handlers"
	"windshift/internal/restapi/v1/middleware"
	"windshift/internal/router"
	"windshift/internal/services"
)

// RegisterRoutes registers all v1 API routes on the given ServeMux
func RegisterRoutes(deps restapi.Deps) {
	mux := deps.Mux
	db := deps.DB
	tokenManager := deps.TokenManager
	permissionService := deps.PermissionService

	// Create auth middleware (with permission service for admin checks)
	bearerAuth := middleware.NewBearerAuthWithPermissions(tokenManager, permissionService)

	// Create rate limiter (1000 requests per minute)
	rateLimiter := middleware.NewRateLimiter(1000)

	// ItemHandler / CommentHandler share the fully-wired CommentService when
	// provided (notifications, mentions, webhooks, etc.) so comments created
	// via the bearer-token surface fire the same notifications as the web UI
	// (WI-434). A nil Deps.CommentService falls back to a bare service that
	// persists comments but skips side effects — kept so embedders that
	// haven't wired Deps yet still boot.
	sharedCommentService := deps.CommentService
	if sharedCommentService == nil {
		sharedCommentService = services.NewCommentService(db)
	}

	// Initialize handlers
	itemHandler := handlers.NewItemHandler(db, permissionService, sharedCommentService)
	workspaceHandler := handlers.NewWorkspaceHandler(db, permissionService)
	statusHandler := handlers.NewStatusHandler(db, permissionService)
	workflowHandler := handlers.NewWorkflowHandler(db, permissionService)
	itemTypeHandler := handlers.NewItemTypeHandler(db, permissionService)
	priorityHandler := handlers.NewPriorityHandler(db, permissionService)
	customFieldHandler := handlers.NewCustomFieldHandler(db, permissionService)
	userHandler := handlers.NewUserHandler(db, permissionService)
	commentHandler := handlers.NewCommentHandler(db, permissionService, sharedCommentService)
	milestoneHandler := handlers.NewMilestoneHandler(db, permissionService)
	iterationHandler := handlers.NewIterationHandler(db, permissionService)
	collectionHandler := handlers.NewCollectionHandler(db, permissionService)
	actionHandler := handlers.NewActionHandler(db, permissionService, deps.ActionService)
	attachmentHandler := handlers.NewAttachmentHandler(db, permissionService, deps.AttachmentPath)
	pageHandler := handlers.NewPageHandler(db, permissionService)
	pageLabelHandler := handlers.NewPageLabelHandler(db, permissionService)
	agentSkillHandler := handlers.NewAgentSkillHandler(db, permissionService)
	diagramHandler := handlers.NewDiagramHandler(db, permissionService)
	labelHandler := handlers.NewLabelHandler(db, permissionService)
	testMgmtHandler := handlers.NewTestManagementHandler(db, permissionService)

	pagePermissionService := services.NewPagePermissionService(db, permissionService)
	pageAttachmentUploadHandler := handlers.NewPageAttachmentUploadHandler(
		handlers.NewBaseHandler(db, permissionService),
		services.NewPageAttachmentUploadService(db, deps.AttachmentPath, permissionService, pagePermissionService),
	)
	itemAttachmentHandler := handlers.NewItemAttachmentHandler(
		handlers.NewBaseHandler(db, permissionService),
		services.NewItemAttachmentService(db, deps.AttachmentPath, permissionService),
	)

	// Time tracking
	timePermService := services.NewTimePermissionService(db, permissionService)
	timeProjectHandler := handlers.NewTimeProjectHandler(handlers.NewBaseHandler(db, permissionService), timePermService)
	timeWorklogHandler := handlers.NewTimeWorklogHandler(handlers.NewBaseHandler(db, permissionService), timePermService)
	timerRepo := repository.NewActiveTimerRepository(db)
	itemRepo := repository.NewItemRepository(db)
	timerService := services.NewTimerService(timerRepo, itemRepo, timePermService, permissionService)
	activeTimerHandler := handlers.NewActiveTimerHandler(handlers.NewBaseHandler(db, permissionService), timerRepo, timerService)

	// Public discovery routes (no bearer auth). Mounted on a sibling group
	// that shares the /rest/api/v1 prefix and rate limiter but skips
	// RequireAuth — the OpenAPI document describes the public surface and
	// has to be fetchable by clients that don't yet have a token.
	publicV1 := router.NewRouteGroup(mux, "/rest/api/v1",
		middleware.RequestID,
		rateLimiter.Middleware,
	)
	publicV1.Handle("GET /openapi.json", handlers.OpenAPISpecJSON)
	publicV1.Handle("GET /openapi.yaml", handlers.OpenAPISpecYAML)

	// Create authenticated route group with middleware chain:
	// RequestID -> RequireAuth -> RateLimiter
	v1 := router.NewRouteGroup(mux, "/rest/api/v1",
		middleware.RequestID,
		bearerAuth.RequireAuth,
		rateLimiter.Middleware,
	)

	// ============================================
	// Items
	// ============================================
	v1.HandleWithMiddleware("GET /items", itemHandler.List, bearerAuth.RequirePermission("items:read"))
	// Bulk fetch by id set. Literal segment, no RequireNumericID — registered
	// before /items/{id} so it isn't swallowed by the wildcard.
	v1.HandleWithMiddleware("GET /items/batch", itemHandler.GetBatch, bearerAuth.RequirePermission("items:read"))
	v1.HandleWithMiddleware("POST /items", itemHandler.Create, bearerAuth.RequirePermission("items:write"))
	v1.HandleWithMiddleware("GET /items/{id}", itemHandler.Get, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /items/{id}", itemHandler.Update, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /items/{id}", itemHandler.Delete, bearerAuth.RequirePermission("items:delete"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /items/{id}/comments", itemHandler.GetComments, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /items/{id}/comments", itemHandler.CreateComment, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /items/{id}/history", itemHandler.GetHistory, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /items/{id}/transitions", itemHandler.GetTransitions, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /items/{id}/transition", itemHandler.Transition, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /items/{id}/change-type", itemHandler.ChangeType, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /items/{id}/attachments", itemHandler.GetAttachments, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /items/{id}/attachments", itemAttachmentHandler.Upload, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /attachments/{id}/download", attachmentHandler.Download, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /attachments/{id}/thumbnail", attachmentHandler.Thumbnail, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /attachments/{id}", itemAttachmentHandler.Delete, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /items/{id}/children", itemHandler.GetChildren, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)

	// ============================================
	// Workspaces
	// ============================================
	v1.HandleWithMiddleware("GET /workspaces", workspaceHandler.List, bearerAuth.RequirePermission("workspaces:read"))
	v1.HandleWithMiddleware("POST /workspaces", workspaceHandler.Create, bearerAuth.RequirePermission("workspaces:write"))
	v1.HandleWithMiddleware("GET /workspaces/{id}", workspaceHandler.Get, bearerAuth.RequirePermission("workspaces:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /workspaces/{id}", workspaceHandler.Update, bearerAuth.RequirePermission("workspaces:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /workspaces/{id}", workspaceHandler.Delete, bearerAuth.RequirePermission("workspaces:delete"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/items", workspaceHandler.GetItems, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/statuses", workspaceHandler.GetStatuses, bearerAuth.RequirePermission("workspaces:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/statuses/completed", workspaceHandler.ListCompletedStatuses, bearerAuth.RequirePermission("workspaces:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/item-types", workspaceHandler.GetItemTypes, bearerAuth.RequirePermission("workspaces:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/priorities", workspaceHandler.GetPriorities, bearerAuth.RequirePermission("workspaces:read"), router.RequireNumericID)

	// Item lookup by stable (workspace_key, item_number) pair — for embed clients
	// (e.g. docmost) that store stable references rather than volatile numeric ids.
	v1.HandleWithMiddleware("GET /workspaces/{ws_key}/items/{number}", itemHandler.GetByKeyAndNumber, bearerAuth.RequirePermission("items:read"))

	// Workspace-scoped milestones. These mirror the global /milestones surface
	// but constrain every request to milestones owned by the workspace in the
	// URL. They are gated by items:* token scopes (matching the convention used
	// by /workspaces/{id}/items) rather than the global milestones:* scopes,
	// because a token authorized to read or edit items in a workspace should be
	// able to read or edit that workspace's milestones too — milestones here
	// are workspace content, not a global resource.
	v1.HandleWithMiddleware("GET /workspaces/{id}/milestones", milestoneHandler.ListForWorkspace, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /workspaces/{id}/milestones", milestoneHandler.CreateInWorkspace, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/milestones/{milestoneId}", milestoneHandler.GetInWorkspace, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /workspaces/{id}/milestones/{milestoneId}", milestoneHandler.UpdateInWorkspace, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /workspaces/{id}/milestones/{milestoneId}", milestoneHandler.DeleteInWorkspace, bearerAuth.RequirePermission("items:delete"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/milestones/{milestoneId}/items", milestoneHandler.GetItemsInWorkspace, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/milestones/{milestoneId}/progress", milestoneHandler.GetProgressInWorkspace, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)

	// Workspace-scoped iterations. Same convention as workspace-scoped
	// milestones — gated by items:* token scopes plus in-handler workspace
	// permission checks. Global iterations remain reachable via /iterations
	// for cross-workspace use cases.
	v1.HandleWithMiddleware("GET /workspaces/{id}/iterations", iterationHandler.ListForWorkspace, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /workspaces/{id}/iterations", iterationHandler.CreateInWorkspace, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/iterations/{iterationId}", iterationHandler.GetInWorkspace, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /workspaces/{id}/iterations/{iterationId}", iterationHandler.UpdateInWorkspace, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /workspaces/{id}/iterations/{iterationId}", iterationHandler.DeleteInWorkspace, bearerAuth.RequirePermission("items:delete"), router.RequireNumericID)

	// ============================================
	// Statuses & Status Categories
	// ============================================
	v1.HandleWithMiddleware("GET /statuses", statusHandler.List, bearerAuth.RequirePermission("statuses:read"))
	v1.HandleWithMiddleware("GET /statuses/{id}", statusHandler.Get, bearerAuth.RequirePermission("statuses:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /status-categories", statusHandler.ListCategories, bearerAuth.RequirePermission("statuses:read"))
	v1.HandleWithMiddleware("GET /status-categories/{id}", statusHandler.GetCategory, bearerAuth.RequirePermission("statuses:read"), router.RequireNumericID)

	// ============================================
	// Workflows
	// ============================================
	v1.HandleWithMiddleware("GET /workflows", workflowHandler.List, bearerAuth.RequirePermission("workflows:read"))
	v1.HandleWithMiddleware("GET /workflows/{id}", workflowHandler.Get, bearerAuth.RequirePermission("workflows:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workflows/{id}/transitions", workflowHandler.GetTransitions, bearerAuth.RequirePermission("workflows:read"), router.RequireNumericID)

	// ============================================
	// Item Types
	// ============================================
	v1.HandleWithMiddleware("GET /item-types", itemTypeHandler.List, bearerAuth.RequirePermission("item-types:read"))
	v1.HandleWithMiddleware("GET /item-types/{id}", itemTypeHandler.Get, bearerAuth.RequirePermission("item-types:read"), router.RequireNumericID)

	// ============================================
	// Priorities
	// ============================================
	v1.HandleWithMiddleware("GET /priorities", priorityHandler.List, bearerAuth.RequirePermission("priorities:read"))
	v1.HandleWithMiddleware("GET /priorities/{id}", priorityHandler.Get, bearerAuth.RequirePermission("priorities:read"), router.RequireNumericID)

	// ============================================
	// Custom Fields
	// ============================================
	v1.HandleWithMiddleware("GET /custom-fields", customFieldHandler.List, bearerAuth.RequirePermission("custom-fields:read"))
	v1.HandleWithMiddleware("GET /custom-fields/{id}", customFieldHandler.Get, bearerAuth.RequirePermission("custom-fields:read"), router.RequireNumericID)

	// ============================================
	// Users
	// ============================================
	v1.HandleWithMiddleware("GET /users", userHandler.List, bearerAuth.RequirePermission("users:read"))
	v1.HandleWithMiddleware("GET /users/me", userHandler.GetCurrent, bearerAuth.RequirePermission("users:read"))
	v1.HandleWithMiddleware("GET /users/{id}", userHandler.Get, bearerAuth.RequirePermission("users:read"), router.RequireNumericID)

	// ============================================
	// Comments (standalone)
	// ============================================
	v1.HandleWithMiddleware("GET /comments/{id}", commentHandler.Get, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /comments/{id}", commentHandler.Update, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /comments/{id}", commentHandler.Delete, bearerAuth.RequirePermission("items:delete"), router.RequireNumericID)

	// ============================================
	// Milestones
	// ============================================
	v1.HandleWithMiddleware("GET /milestones", milestoneHandler.List, bearerAuth.RequirePermission("milestones:read"))
	v1.HandleWithMiddleware("POST /milestones", milestoneHandler.Create, bearerAuth.RequirePermission("milestones:write"))
	v1.HandleWithMiddleware("GET /milestones/{id}", milestoneHandler.Get, bearerAuth.RequirePermission("milestones:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /milestones/{id}", milestoneHandler.Update, bearerAuth.RequirePermission("milestones:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /milestones/{id}", milestoneHandler.Delete, bearerAuth.RequirePermission("milestones:delete"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /milestones/{id}/items", milestoneHandler.GetItems, bearerAuth.RequirePermission("milestones:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /milestones/{id}/progress", milestoneHandler.GetProgress, bearerAuth.RequirePermission("milestones:read"), router.RequireNumericID)

	// ============================================
	// Iterations
	// ============================================
	v1.HandleWithMiddleware("GET /iterations", iterationHandler.List, bearerAuth.RequirePermission("iterations:read"))
	v1.HandleWithMiddleware("POST /iterations", iterationHandler.Create, bearerAuth.RequirePermission("iterations:write"))
	v1.HandleWithMiddleware("GET /iterations/{id}", iterationHandler.Get, bearerAuth.RequirePermission("iterations:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /iterations/{id}", iterationHandler.Update, bearerAuth.RequirePermission("iterations:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /iterations/{id}", iterationHandler.Delete, bearerAuth.RequirePermission("iterations:delete"), router.RequireNumericID)

	// ============================================
	// Collections — addressable by either numeric id or public_slug.
	// The handler picks the lookup based on whether {key} is all digits.
	// ============================================
	v1.HandleWithMiddleware("GET /collections", collectionHandler.List, bearerAuth.RequirePermission("collections:read"))
	v1.HandleWithMiddleware("GET /collections/{key}", collectionHandler.Get, bearerAuth.RequirePermission("collections:read"))
	v1.HandleWithMiddleware("GET /collections/{key}/items", collectionHandler.GetItems, bearerAuth.RequirePermission("collections:read", "items:read"))

	// ============================================
	// Actions (workspace-scoped automation graphs)
	// ============================================
	v1.HandleWithMiddleware("GET /workspaces/{id}/action-catalog", actionHandler.GetCatalog, bearerAuth.RequirePermission("actions:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/actions", actionHandler.ListActions, bearerAuth.RequirePermission("actions:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /workspaces/{id}/actions", actionHandler.CreateAction, bearerAuth.RequirePermission("actions:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /workspaces/{id}/actions/validate", actionHandler.ValidateAction, bearerAuth.RequirePermission("actions:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/actions/{actionId}", actionHandler.GetAction, bearerAuth.RequirePermission("actions:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /workspaces/{id}/actions/{actionId}", actionHandler.UpdateAction, bearerAuth.RequirePermission("actions:write"), router.RequireNumericID)

	// ============================================
	// Pages (workspace knowledge / wiki). Per-page ACL is enforced in the
	// handler via PagePermissionService; the route layer gates on the
	// pages:* token scopes so a token scoped to a different surface can't
	// drive page CRUD even if its bearer-user has the workspace role.
	// ============================================
	v1.HandleWithMiddleware("GET /workspaces/{id}/pages", pageHandler.List, bearerAuth.RequirePermission("pages:read"), router.RequireNumericID)
	// Literal "search" segment; the ServeMux prefers it over the {pageId}
	// wildcard route below, so order is not load-bearing.
	v1.HandleWithMiddleware("GET /workspaces/{id}/pages/search", pageHandler.Search, bearerAuth.RequirePermission("pages:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /workspaces/{id}/pages", pageHandler.Create, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/pages/{pageId}", pageHandler.Get, bearerAuth.RequirePermission("pages:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /workspaces/{id}/pages/{pageId}", pageHandler.Update, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /workspaces/{id}/pages/{pageId}", pageHandler.Archive, bearerAuth.RequirePermission("pages:delete"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /workspaces/{id}/pages/{pageId}/move", pageHandler.Move, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/pages/{pageId}/history", pageHandler.GetHistory, bearerAuth.RequirePermission("pages:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/pages/{pageId}/history/{revisionId}", pageHandler.GetRevision, bearerAuth.RequirePermission("pages:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /workspaces/{id}/pages/{pageId}/history/{revisionId}/restore", pageHandler.RestoreRevision, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/pages/{pageId}/permissions", pageHandler.GetPermissions, bearerAuth.RequirePermission("pages:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /workspaces/{id}/pages/{pageId}/permissions", pageHandler.GrantPermission, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /workspaces/{id}/pages/{pageId}/permissions/{permissionId}", pageHandler.RevokePermission, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("PATCH /workspaces/{id}/pages/{pageId}/inheritance", pageHandler.SetInheritance, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)
	// Bearer-authenticated page-attachment upload (the cookie-auth
	// /api/attachments/upload route rejects crw_ tokens). Uses the shared
	// upload service so validation/storage/audit stay in one place.
	v1.HandleWithMiddleware("POST /workspaces/{id}/pages/{pageId}/attachments", pageAttachmentUploadHandler.Upload, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)

	// ============================================
	// Page labels (workspace-scoped, attach to pages only). Label CRUD
	// uses pages:write/pages:read scopes — same as page edits — because
	// the user-facing permission gate is also page.edit / page.view.
	// Attach/detach gates per-page via PagePermissionService.
	// ============================================
	v1.HandleWithMiddleware("GET /workspaces/{id}/agent-skills", agentSkillHandler.List, bearerAuth.RequirePermission("agent-skills:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/agent-skills/{skillId}", agentSkillHandler.Get, bearerAuth.RequirePermission("agent-skills:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/page-labels", pageLabelHandler.ListLabels, bearerAuth.RequirePermission("pages:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /workspaces/{id}/page-labels", pageLabelHandler.CreateLabel, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/page-labels/{labelId}", pageLabelHandler.GetLabel, bearerAuth.RequirePermission("pages:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /workspaces/{id}/page-labels/{labelId}", pageLabelHandler.UpdateLabel, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /workspaces/{id}/page-labels/{labelId}", pageLabelHandler.DeleteLabel, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)

	v1.HandleWithMiddleware("GET /workspaces/{id}/pages/{pageId}/labels", pageLabelHandler.ListForPage, bearerAuth.RequirePermission("pages:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /workspaces/{id}/pages/{pageId}/labels", pageLabelHandler.SetForPage, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /workspaces/{id}/pages/{pageId}/labels", pageLabelHandler.AddToPage, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /workspaces/{id}/pages/{pageId}/labels/{labelId}", pageLabelHandler.RemoveFromPage, bearerAuth.RequirePermission("pages:write"), router.RequireNumericID)

	// ============================================
	// Links (item ↔ item / item ↔ page / item ↔ test_case)
	// Shares the fully-wired *services.ItemLinkService that the cookie-
	// auth handler built (asset checker, page checker, notification +
	// action emitters) so both surfaces behave identically. deps.ItemLinkService
	// is nil during early-boot or in tests that don't construct the
	// cookie path; fall back to a bare service so the rest of v1 still
	// boots — link endpoints in that case fail closed (404) because the
	// permission checkers are absent.
	// ============================================
	linkSvc := deps.ItemLinkService
	if linkSvc == nil {
		linkSvc = services.NewItemLinkService(db).WithPermissionService(permissionService)
	}
	linkHandler := handlers.NewLinkHandler(handlers.NewBaseHandler(db, permissionService), linkSvc)

	v1.HandleWithMiddleware("GET /link-types", linkHandler.ListLinkTypes, bearerAuth.RequirePermission("items:read"))
	v1.HandleWithMiddleware("POST /links", linkHandler.CreateLink, bearerAuth.RequirePermission("items:write"))
	v1.HandleWithMiddleware("DELETE /links/{id}", linkHandler.DeleteLink, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /items/{id}/links", linkHandler.GetLinksForEntity, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /pages/{id}/links", linkHandler.GetLinksForEntity, bearerAuth.RequirePermission("pages:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /test-cases/{id}/links", linkHandler.GetLinksForEntity, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)

	// ============================================
	// Item diagrams (Mermaid / Excalidraw payloads attached to items).
	// Gated by items:* because diagrams are item-scoped content; the
	// handler still applies the workspace view/edit check on the owning
	// item so a token cannot probe diagrams it isn't authorized to see.
	// ============================================
	v1.HandleWithMiddleware("GET /items/{id}/diagrams", diagramHandler.ListForItem, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /items/{id}/diagrams", diagramHandler.CreateForItem, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /diagrams/{id}", diagramHandler.Get, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /diagrams/{id}", diagramHandler.Update, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /diagrams/{id}", diagramHandler.Delete, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)

	// ============================================
	// Item labels (workspace-scoped catalog + per-item attach/detach).
	// Mirrors the page-labels surface in shape: catalog CRUD lives under
	// /workspaces/{id}/labels, and the per-item attachments live under
	// /items/{id}/labels. Gated by items:* because these labels are
	// item-content; the handler enforces workspace view/edit on top.
	// ============================================
	v1.HandleWithMiddleware("GET /workspaces/{id}/labels", labelHandler.ListForWorkspace, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /workspaces/{id}/labels", labelHandler.CreateInWorkspace, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /workspaces/{id}/labels/{labelId}", labelHandler.GetInWorkspace, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /workspaces/{id}/labels/{labelId}", labelHandler.UpdateInWorkspace, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /workspaces/{id}/labels/{labelId}", labelHandler.DeleteInWorkspace, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)

	v1.HandleWithMiddleware("GET /items/{id}/labels", labelHandler.ListForItem, bearerAuth.RequirePermission("items:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /items/{id}/labels", labelHandler.SetForItem, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /items/{id}/labels", labelHandler.AddToItem, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /items/{id}/labels/{labelId}", labelHandler.RemoveFromItem, bearerAuth.RequirePermission("items:write"), router.RequireNumericID)

	// ============================================
	// Test management (WI-68 phase 1 + WI-81 phase 2).
	// Gated by tests:* token scope at the route layer; in-handler
	// workspace permission checks enforce test.view / test.manage /
	// test.execute so token scope alone never grants workspace access.
	// ============================================
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-folders", testMgmtHandler.ListTestFolders, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-folders", testMgmtHandler.CreateTestFolder, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-folders/{id}", testMgmtHandler.GetTestFolder, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-folders/{id}", testMgmtHandler.UpdateTestFolder, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-folders/{id}", testMgmtHandler.DeleteTestFolder, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-folders/reorder", testMgmtHandler.ReorderTestFolders, bearerAuth.RequirePermission("tests:write"))

	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-cases", testMgmtHandler.ListTestCases, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-cases", testMgmtHandler.CreateTestCase, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-cases/{id}", testMgmtHandler.GetTestCase, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-cases/{id}", testMgmtHandler.UpdateTestCase, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-cases/{id}", testMgmtHandler.DeleteTestCase, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-cases/{id}/move", testMgmtHandler.MoveTestCase, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-cases/reorder", testMgmtHandler.ReorderTestCases, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-cases/{id}/connections", testMgmtHandler.GetTestCaseConnections, bearerAuth.RequirePermission("tests:read"))

	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-cases/{testCaseId}/steps", testMgmtHandler.GetTestCaseSteps, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-cases/{testCaseId}/steps", testMgmtHandler.CreateTestCaseStep, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-cases/{testCaseId}/steps/{stepId}", testMgmtHandler.UpdateTestCaseStep, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-cases/{testCaseId}/steps/{stepId}", testMgmtHandler.DeleteTestCaseStep, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-cases/{testCaseId}/steps/reorder", testMgmtHandler.ReorderTestCaseSteps, bearerAuth.RequirePermission("tests:write"))

	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-labels", testMgmtHandler.ListTestLabels, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-labels", testMgmtHandler.CreateTestLabel, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-labels/{labelId}", testMgmtHandler.UpdateTestLabel, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-labels/{labelId}", testMgmtHandler.DeleteTestLabel, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-cases/{testCaseId}/labels", testMgmtHandler.ListTestCaseLabels, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-cases/{testCaseId}/labels", testMgmtHandler.AddTestCaseLabel, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-cases/{testCaseId}/labels/{labelId}", testMgmtHandler.RemoveTestCaseLabel, bearerAuth.RequirePermission("tests:write"))

	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-sets", testMgmtHandler.ListTestSets, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-sets", testMgmtHandler.CreateTestSet, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-sets/{id}", testMgmtHandler.GetTestSet, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-sets/{id}", testMgmtHandler.UpdateTestSet, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-sets/{id}", testMgmtHandler.DeleteTestSet, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-sets/{id}/test-cases", testMgmtHandler.GetTestSetCases, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-sets/{id}/test-cases", testMgmtHandler.AddTestSetCase, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-sets/{id}/test-cases/{testCaseId}", testMgmtHandler.RemoveTestSetCase, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-sets/{id}/runs", testMgmtHandler.ListTestSetRuns, bearerAuth.RequirePermission("tests:read"))

	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-plans", testMgmtHandler.ListTestPlans, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-plans", testMgmtHandler.CreateTestPlan, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-plans/{id}", testMgmtHandler.GetTestPlan, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-plans/{id}", testMgmtHandler.UpdateTestPlan, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-plans/{id}", testMgmtHandler.DeleteTestPlan, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-plans/{id}/test-cases", testMgmtHandler.ListTestPlanCases, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-plans/{id}/test-cases", testMgmtHandler.AddTestPlanCase, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-plans/{id}/test-cases/{testCaseId}", testMgmtHandler.RemoveTestPlanCase, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-plans/{id}/runs", testMgmtHandler.ListTestPlanRuns, bearerAuth.RequirePermission("tests:read"))

	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-run-templates", testMgmtHandler.ListTestRunTemplates, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-run-templates", testMgmtHandler.CreateTestRunTemplate, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-run-templates/{id}", testMgmtHandler.GetTestRunTemplate, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-run-templates/{id}", testMgmtHandler.UpdateTestRunTemplate, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-run-templates/{id}", testMgmtHandler.DeleteTestRunTemplate, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-run-templates/{id}/executions", testMgmtHandler.ListTestRunTemplateExecutions, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-run-templates/{id}/execute", testMgmtHandler.ExecuteTestRunTemplate, bearerAuth.RequirePermission("tests:write"))

	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-runs", testMgmtHandler.ListTestRuns, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-runs", testMgmtHandler.CreateTestRun, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-runs/{id}", testMgmtHandler.GetTestRun, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-runs/{id}", testMgmtHandler.UpdateTestRun, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-runs/{id}", testMgmtHandler.DeleteTestRun, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-runs/{id}/end", testMgmtHandler.EndTestRun, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-runs/{id}/results", testMgmtHandler.GetTestRunResults, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-runs/{id}/results/{resultId}", testMgmtHandler.UpdateTestRunResult, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-runs/{id}/steps", testMgmtHandler.GetTestRunStepResults, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("PUT /workspaces/{workspaceId}/test-runs/{id}/steps/{stepId}", testMgmtHandler.UpdateTestRunStepResult, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-runs/{id}/summary", testMgmtHandler.GetTestRunSummary, bearerAuth.RequirePermission("tests:read"))

	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-reports/summary", testMgmtHandler.GetTestReportsSummary, bearerAuth.RequirePermission("tests:read"))
	v1.HandleWithMiddleware("POST /workspaces/{workspaceId}/test-results/{resultId}/items", testMgmtHandler.LinkTestResultItem, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("DELETE /workspaces/{workspaceId}/test-results/{resultId}/items/{itemId}", testMgmtHandler.UnlinkTestResultItem, bearerAuth.RequirePermission("tests:write"))
	v1.HandleWithMiddleware("GET /workspaces/{workspaceId}/test-results/{resultId}/items", testMgmtHandler.ListTestResultItems, bearerAuth.RequirePermission("tests:read"))

	// ============================================
	// Assets. Gated by assets:* at the route layer; the handler still asks
	// the per-set asset role (Viewer / Editor / Administrator with the
	// asset.view/create/edit/delete/admin keys) via AssetPermissionService
	// so a token can't reach a set the user can't see. 404 (not 403) on
	// any permission failure mirrors the items convention — set / asset
	// existence is never leaked.
	//
	// Mutating sets / types / categories / statuses / role assignments and
	// the asset-actions automation graphs stay admin-UI-only in this slice;
	// follow-ups can promote subsets behind explicit asset-sets:write etc.
	// ============================================
	assetRepo := repository.NewAssetRepository(db)
	assetPermSvc := deps.AssetPermissionService
	if assetPermSvc == nil {
		// Nil-safe fallback for embedders that haven't wired the shared
		// service yet — construct a fresh one so asset routes still serve.
		assetPermSvc = services.NewAssetPermissionService(assetRepo, permissionService)
	}
	assetSvc := deps.AssetService
	if assetSvc == nil {
		assetSvc = services.NewAssetService(db, assetRepo)
	}
	assetHandler := handlers.NewAssetHandler(db, permissionService, assetPermSvc, assetSvc)

	// Asset entities
	v1.HandleWithMiddleware("GET /asset-sets/{setId}/assets", assetHandler.List, bearerAuth.RequirePermission("assets:read"))
	v1.HandleWithMiddleware("POST /asset-sets/{setId}/assets", assetHandler.Create, bearerAuth.RequirePermission("assets:write"))
	v1.HandleWithMiddleware("POST /asset-sets/{setId}/assets/import", assetHandler.ImportCSV, bearerAuth.RequirePermission("assets:write"))
	v1.HandleWithMiddleware("GET /assets/{id}", assetHandler.Get, bearerAuth.RequirePermission("assets:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("PUT /assets/{id}", assetHandler.Update, bearerAuth.RequirePermission("assets:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /assets/{id}", assetHandler.Delete, bearerAuth.RequirePermission("assets:delete"), router.RequireNumericID)

	// Asset sets (read-only on v1)
	v1.HandleWithMiddleware("GET /asset-sets", assetHandler.ListSets, bearerAuth.RequirePermission("assets:read"))
	v1.HandleWithMiddleware("GET /asset-sets/{setId}", assetHandler.GetSet, bearerAuth.RequirePermission("assets:read"))

	// Asset types (read-only on v1)
	v1.HandleWithMiddleware("GET /asset-sets/{setId}/types", assetHandler.ListTypes, bearerAuth.RequirePermission("assets:read"))
	v1.HandleWithMiddleware("GET /asset-types/{id}", assetHandler.GetType, bearerAuth.RequirePermission("assets:read"), router.RequireNumericID)

	// Asset categories (read-only on v1)
	v1.HandleWithMiddleware("GET /asset-sets/{setId}/categories", assetHandler.ListCategories, bearerAuth.RequirePermission("assets:read"))
	v1.HandleWithMiddleware("GET /asset-categories/{id}", assetHandler.GetCategory, bearerAuth.RequirePermission("assets:read"), router.RequireNumericID)

	// Asset statuses (read-only on v1)
	v1.HandleWithMiddleware("GET /asset-sets/{setId}/statuses", assetHandler.ListStatuses, bearerAuth.RequirePermission("assets:read"))
	v1.HandleWithMiddleware("GET /asset-statuses/{id}", assetHandler.GetStatus, bearerAuth.RequirePermission("assets:read"), router.RequireNumericID)

	// ============================================
	// Search
	// ============================================
	v1.HandleWithMiddleware("GET /search/items", itemHandler.Search, bearerAuth.RequirePermission("items:read"))

	// ============================================
	// Time tracking
	// ============================================
	v1.HandleWithMiddleware("GET /time/projects", timeProjectHandler.List, bearerAuth.RequirePermission("time:read"))
	v1.HandleWithMiddleware("GET /time/projects/{id}", timeProjectHandler.Get, bearerAuth.RequirePermission("time:read"), router.RequireNumericID)
	v1.HandleWithMiddleware("GET /time/worklogs", timeWorklogHandler.ListMine, bearerAuth.RequirePermission("time:read"))
	v1.HandleWithMiddleware("POST /time/worklogs", timeWorklogHandler.Create, bearerAuth.RequirePermission("time:write"))
	v1.HandleWithMiddleware("PUT /time/worklogs/{id}", timeWorklogHandler.Update, bearerAuth.RequirePermission("time:write"), router.RequireNumericID)
	v1.HandleWithMiddleware("DELETE /time/worklogs/{id}", timeWorklogHandler.Delete, bearerAuth.RequirePermission("time:delete"), router.RequireNumericID)
	v1.HandleWithMiddleware("POST /timer/start", activeTimerHandler.StartTimer, bearerAuth.RequirePermission("time:write"))
	v1.HandleWithMiddleware("GET /timer/active", activeTimerHandler.GetActiveTimer, bearerAuth.RequirePermission("time:read"))
	v1.HandleWithMiddleware("DELETE /timer/stop", activeTimerHandler.StopTimer, bearerAuth.RequirePermission("time:write"))

	// ============================================
	// Admin endpoints (require system admin + scope)
	// ============================================
	adminUserHandler := handlers.NewAdminUserHandler(db, permissionService)
	adminGroupHandler := handlers.NewAdminGroupHandler(db, permissionService)
	adminAuditLogHandler := handlers.NewAdminAuditLogHandler(db, permissionService)
	adminAPITokenHandler := handlers.NewAdminAPITokenHandler(db, tokenManager, permissionService)

	// Admin sub-group: inherits auth + rate limit, adds RequireSystemAdmin
	adminV1 := v1.Group("", bearerAuth.RequireSystemAdmin)

	// Admin: Users
	adminV1.HandleWithMiddleware("GET /admin/users", adminUserHandler.List, bearerAuth.RequirePermission("admin:users:read"))
	adminV1.HandleWithMiddleware("PUT /admin/users/{id}", adminUserHandler.Update, bearerAuth.RequirePermission("admin:users:write"), router.RequireNumericID)

	// Admin: Groups
	adminV1.HandleWithMiddleware("GET /admin/groups", adminGroupHandler.List, bearerAuth.RequirePermission("admin:groups:read"))
	adminV1.HandleWithMiddleware("POST /admin/groups", adminGroupHandler.Create, bearerAuth.RequirePermission("admin:groups:write"))
	adminV1.HandleWithMiddleware("PUT /admin/groups/{id}", adminGroupHandler.Update, bearerAuth.RequirePermission("admin:groups:write"), router.RequireNumericID)
	adminV1.HandleWithMiddleware("DELETE /admin/groups/{id}", adminGroupHandler.Delete, bearerAuth.RequirePermission("admin:groups:write"), router.RequireNumericID)

	// Admin: Audit Logs
	adminV1.HandleWithMiddleware("GET /admin/audit-logs", adminAuditLogHandler.List, bearerAuth.RequirePermission("admin:audit-logs:read"))
	adminV1.HandleWithMiddleware("GET /admin/audit-logs/since", adminAuditLogHandler.ListSince, bearerAuth.RequirePermission("admin:audit-logs:read"))

	// Admin: API Tokens
	adminV1.HandleWithMiddleware("GET /admin/api-tokens", adminAPITokenHandler.ListAll, bearerAuth.RequirePermission("admin:api-tokens:read"))
	adminV1.HandleWithMiddleware("DELETE /admin/api-tokens/{id}", adminAPITokenHandler.Revoke, bearerAuth.RequirePermission("admin:api-tokens:write"), router.RequireNumericID)
}
