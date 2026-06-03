package restapi

import (
	"net/http"

	"windshift/internal/auth"
	"windshift/internal/database"
	"windshift/internal/services"
)

// Deps carries the dependencies v1 (and future versions) need so we can add
// services without churning every call site. New fields go at the end with
// nil-safe defaults so unrelated callers compile unchanged.
type Deps struct {
	Mux               *http.ServeMux
	DB                database.Database
	TokenManager      *auth.TokenManager
	PermissionService *services.PermissionService
	// ActionService is the optional cache-invalidation hook for the actions
	// surface. v1 falls back to "next periodic refresh" when nil, which is
	// fine for cold-start tooling but worth wiring for production.
	ActionService *services.ActionService
	// AttachmentPath is the base directory where attachment blobs are stored.
	// Empty when attachments are disabled — the v1 download route falls back
	// to a not-enabled response in that case.
	AttachmentPath string
	// ItemLinkService is the fully-wired link orchestration service
	// (asset/page permission checkers, notification + action emitters)
	// shared with the cookie-auth handler. Required for the v1 link
	// surface; the v1 router falls back to a bare service if nil so old
	// embedders that haven't wired this yet still work for everything
	// EXCEPT link endpoints.
	ItemLinkService *services.ItemLinkService
	// AssetPermissionService gates the v1 asset surface against the
	// per-set role model. Shared with the cookie-auth handler so both
	// surfaces consult one role-check pipeline. The v1 router constructs
	// a fresh instance when nil so embedders that haven't wired this yet
	// still serve asset routes correctly.
	AssetPermissionService *services.AssetPermissionService
}

// SetupRoutesFunc is a function type for setting up v1 routes
// This breaks the import cycle by allowing main.go to wire the dependency
type SetupRoutesFunc func(deps Deps)

// SetupRoutes registers all REST API routes under /rest/api
// The v1Setup function is called to register v1 routes on the provided mux
func SetupRoutes(deps Deps, v1Setup SetupRoutesFunc) {
	// Register v1 routes (they handle their own prefix /rest/api/v1)
	if v1Setup != nil {
		v1Setup(deps)
	}

	// Future: v2 routes
	// v2Setup(deps)
}
