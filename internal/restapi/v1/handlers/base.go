package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/authz"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/restapi"
	"windshift/internal/restapi/v1/middleware"
	"windshift/internal/services"
)

// BaseHandler provides shared dependencies and utilities for REST API handlers.
type BaseHandler struct {
	DB                database.Database
	PermissionService *services.PermissionService
	Perms             *authz.Authz
}

// NewBaseHandler creates a new base handler with shared dependencies.
func NewBaseHandler(db database.Database, permissionService *services.PermissionService) BaseHandler {
	return BaseHandler{
		DB:                db,
		PermissionService: permissionService,
		Perms:             authz.New(db, permissionService),
	}
}

// ParsePagination extracts pagination params from a request.
func (b *BaseHandler) ParsePagination(r *http.Request) restapi.PaginationParams {
	return restapi.ParsePaginationParams(r)
}

// RespondOK writes a 200 OK response.
func (b *BaseHandler) RespondOK(w http.ResponseWriter, data interface{}) {
	restapi.RespondOK(w, data)
}

// RespondCreated writes a 201 Created response.
func (b *BaseHandler) RespondCreated(w http.ResponseWriter, data interface{}) {
	restapi.RespondCreated(w, data)
}

// RespondNoContent writes a 204 No Content response.
func (b *BaseHandler) RespondNoContent(w http.ResponseWriter) {
	restapi.RespondNoContent(w)
}

// RespondPaginated writes a paginated response.
func (b *BaseHandler) RespondPaginated(w http.ResponseWriter, data interface{}, pagination restapi.PaginationParams, total int) {
	restapi.RespondPaginated(w, data, restapi.NewPaginationMeta(pagination, total))
}

// RespondError writes an error response.
func (b *BaseHandler) RespondError(w http.ResponseWriter, r *http.Request, err *restapi.APIError) {
	restapi.RespondError(w, r, err)
}

// RespondInternalError writes a 500 error response.
func (b *BaseHandler) RespondInternalError(w http.ResponseWriter, r *http.Request) {
	restapi.RespondError(w, r, restapi.ErrInternalError)
}

// RespondNotFound writes a 404 error response.
func (b *BaseHandler) RespondNotFound(w http.ResponseWriter, r *http.Request) {
	restapi.RespondError(w, r, restapi.ErrNotFound)
}

// RequireAuth extracts the authenticated user from the request context.
// Returns nil and writes a 401 response if not authenticated.
func (b *BaseHandler) RequireAuth(w http.ResponseWriter, r *http.Request) (*models.User, bool) {
	user := middleware.GetUser(r.Context())
	if user == nil {
		restapi.RespondError(w, r, restapi.ErrUnauthorized)
		return nil, false
	}
	return user, true
}

// ParsePathID parses an integer path parameter from the request.
// Returns 0 and writes a 400 response if the parameter is not a valid integer.
func (b *BaseHandler) ParsePathID(w http.ResponseWriter, r *http.Request, param, label string) (int, bool) {
	id, err := strconv.Atoi(r.PathValue(param))
	if err != nil {
		restapi.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid "+label))
		return 0, false
	}
	return id, true
}

// DecodeBodyOrRespond decodes JSON body or writes 400 on error.
func (b *BaseHandler) DecodeBodyOrRespond(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		restapi.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid request body"))
		return false
	}
	return true
}

// RequireGlobalPermission checks global permission or writes 403.
func (b *BaseHandler) RequireGlobalPermission(w http.ResponseWriter, r *http.Request, userID int, permission, label string) bool {
	hasPermission, err := b.Perms.HasGlobalPermission(userID, permission)
	if err != nil || !hasPermission {
		restapi.RespondError(w, r, restapi.NewAPIError(http.StatusForbidden, "FORBIDDEN", label+" permission required"))
		return false
	}
	return true
}

// RequireWorkspaceViewAccess authenticates the request, parses the workspace
// ID from the {id} path parameter, and verifies the caller can view items in
// that workspace. On failure it writes the appropriate HTTP error and returns
// (0, false). Used by every /workspaces/{id}/<resource> read route.
func (b *BaseHandler) RequireWorkspaceViewAccess(w http.ResponseWriter, r *http.Request) (int, bool) {
	user, ok := b.RequireAuth(w, r)
	if !ok {
		return 0, false
	}
	wsID, ok := b.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return 0, false
	}
	canView, _ := b.Perms.CanViewWorkspace(user.ID, wsID)
	if !canView {
		restapi.RespondError(w, r, restapi.ErrWorkspaceNotFound)
		return 0, false
	}
	return wsID, true
}

// RequireWorkspaceEditAccess is the edit-permission counterpart to
// RequireWorkspaceViewAccess. Used by every /workspaces/{id}/<resource> write
// route.
func (b *BaseHandler) RequireWorkspaceEditAccess(w http.ResponseWriter, r *http.Request) (int, bool) {
	user, ok := b.RequireAuth(w, r)
	if !ok {
		return 0, false
	}
	wsID, ok := b.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return 0, false
	}
	canEdit, _ := b.Perms.CanEditWorkspace(user.ID, wsID)
	if !canEdit {
		restapi.RespondError(w, r, restapi.ErrWorkspaceNotFound)
		return 0, false
	}
	return wsID, true
}

// ValidateRequiredString checks a required string field.
func (b *BaseHandler) ValidateRequiredString(w http.ResponseWriter, r *http.Request, value, fieldName string) bool {
	if strings.TrimSpace(value) == "" {
		restapi.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, fieldName+" is required"))
		return false
	}
	return true
}

// ExcludePersonal reports whether the request opts out of personal-workspace
// results via the exclude_personal query parameter. Integration surfaces that
// republish items into shared contexts (e.g. document embeds) set this so the
// caller's own personal items never leak into pages other people read.
func ExcludePersonal(r *http.Request) bool {
	v := r.URL.Query().Get("exclude_personal")
	return v == "true" || v == "1"
}
