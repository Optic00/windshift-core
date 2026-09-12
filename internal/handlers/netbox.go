package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"windshift/internal/integrations/netbox"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/services"
)

type NetBoxHandler struct {
	service    *services.NetBoxService
	items      *repository.ItemRepository
	permission *services.PermissionService
	auditor    *logger.Auditor
}

func NewNetBoxHandler(service *services.NetBoxService, items *repository.ItemRepository, permission *services.PermissionService, auditor *logger.Auditor) *NetBoxHandler {
	return &NetBoxHandler{service: service, items: items, permission: permission, auditor: auditor}
}

func (h *NetBoxHandler) adminActor(w http.ResponseWriter, r *http.Request) (*models.User, bool) {
	user, ok := RequireAuth(w, r)
	if !ok || !RequireSystemAdmin(w, r, user.ID, h.permission) {
		return nil, false
	}
	return user, true
}

func (h *NetBoxHandler) ListConnections(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.adminActor(w, r); !ok {
		return
	}
	connections, err := h.service.ListConnections()
	if h.respondError(w, r, err) {
		respondJSONOK(w, connections)
	}
}

func (h *NetBoxHandler) GetConnection(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.adminActor(w, r); !ok {
		return
	}
	connection, err := h.service.GetConnection(r.PathValue("id"))
	if h.respondError(w, r, err) {
		respondJSONOK(w, connection)
	}
}

func (h *NetBoxHandler) CreateConnection(w http.ResponseWriter, r *http.Request) {
	user, ok := h.adminActor(w, r)
	if !ok {
		return
	}
	req, ok := decodeJSON[models.CreateNetBoxConnectionRequest](w, r)
	if !ok {
		return
	}
	connection, err := h.service.CreateConnection(req, user.ID)
	if h.respondError(w, r, err) {
		h.audit(r, "netbox_connection.create", map[string]any{"connection_id": connection.ProviderID})
		respondJSONCreated(w, connection)
	}
}

func (h *NetBoxHandler) UpdateConnection(w http.ResponseWriter, r *http.Request) {
	user, ok := h.adminActor(w, r)
	if !ok {
		return
	}
	req, ok := decodeJSON[models.UpdateNetBoxConnectionRequest](w, r)
	if !ok {
		return
	}
	connection, err := h.service.UpdateConnection(r.PathValue("id"), req, user.ID)
	if h.respondError(w, r, err) {
		h.audit(r, "netbox_connection.update", map[string]any{"connection_id": connection.ProviderID})
		respondJSONOK(w, connection)
	}
}

func (h *NetBoxHandler) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	user, ok := h.adminActor(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if h.respondError(w, r, h.service.DeleteConnection(id, user.ID)) {
		h.audit(r, "netbox_connection.delete", map[string]any{"connection_id": id})
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *NetBoxHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	user, ok := h.adminActor(w, r)
	if !ok {
		return
	}
	result, err := h.service.TestConnection(r.Context(), r.PathValue("id"), user.ID)
	if h.respondError(w, r, err) {
		h.audit(r, "netbox_connection.test", map[string]any{"connection_id": r.PathValue("id")})
		respondJSONOK(w, result)
	}
}

func (h *NetBoxHandler) workspaceActor(w http.ResponseWriter, r *http.Request, permission string) (*models.User, int, bool) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return nil, 0, false
	}
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok || !RequireWorkspacePermission(w, r, user.ID, workspaceID, permission, h.permission) {
		return nil, 0, false
	}
	return user, workspaceID, true
}

func (h *NetBoxHandler) ListWorkspaceConnections(w http.ResponseWriter, r *http.Request) {
	user, workspaceID, ok := h.workspaceActor(w, r, models.PermissionItemView)
	if !ok {
		return
	}
	connections, err := h.service.ListWorkspaceConnections(workspaceID, user.ID)
	if h.respondError(w, r, err) {
		respondJSONOK(w, connections)
	}
}

func (h *NetBoxHandler) Search(w http.ResponseWriter, r *http.Request) {
	user, workspaceID, ok := h.workspaceActor(w, r, models.PermissionItemEdit)
	if !ok {
		return
	}
	limit, offset := 20, 0
	for name, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if value := r.URL.Query().Get(name); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				respondBadRequest(w, r, name+" must be an integer")
				return
			}
			*target = parsed
		}
	}
	result, err := h.service.Search(r.Context(), workspaceID, user.ID, r.PathValue("id"),
		models.NetBoxObjectType(r.URL.Query().Get("object_type")), r.URL.Query().Get("q"), limit, offset)
	if h.respondError(w, r, err) {
		respondJSONOK(w, result)
	}
}

func (h *NetBoxHandler) itemActor(w http.ResponseWriter, r *http.Request, permission string) (*models.User, int, bool) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return nil, 0, false
	}
	itemID, ok := requireIDParam(w, r, "id")
	// No approver-only item.view exception: NetBox snapshots require actual
	// workspace access, even if the user can see a pending approval's item.
	if !ok || !CheckItemPermission(w, r, h.items, h.permission, itemID, permission) {
		return nil, 0, false
	}
	return user, itemID, true
}

func (h *NetBoxHandler) GetItemLinks(w http.ResponseWriter, r *http.Request) {
	user, itemID, ok := h.itemActor(w, r, models.PermissionItemView)
	if !ok {
		return
	}
	links, err := h.service.GetItemLinks(itemID, user.ID)
	if h.respondError(w, r, err) {
		respondJSONOK(w, links)
	}
}

func (h *NetBoxHandler) LinkObject(w http.ResponseWriter, r *http.Request) {
	user, itemID, ok := h.itemActor(w, r, models.PermissionItemEdit)
	if !ok {
		return
	}
	req, ok := decodeJSON[models.CreateNetBoxItemLinkRequest](w, r)
	if !ok {
		return
	}
	link, created, err := h.service.LinkObject(r.Context(), itemID, user.ID, req)
	if h.respondError(w, r, err) {
		if created {
			h.auditLink(r, "netbox_link.create", link)
			respondJSONCreated(w, link)
		} else {
			respondJSONOK(w, link)
		}
	}
}

func (h *NetBoxHandler) RefreshLink(w http.ResponseWriter, r *http.Request) {
	user, itemID, ok := h.itemActor(w, r, models.PermissionItemEdit)
	if !ok {
		return
	}
	link, err := h.service.RefreshLink(r.Context(), itemID, user.ID, r.PathValue("linkId"))
	if h.respondError(w, r, err) {
		h.auditLink(r, "netbox_link.refresh", link)
		respondJSONOK(w, link)
	}
}

func (h *NetBoxHandler) Unlink(w http.ResponseWriter, r *http.Request) {
	user, itemID, ok := h.itemActor(w, r, models.PermissionItemEdit)
	if !ok {
		return
	}
	linkID := r.PathValue("linkId")
	if h.respondError(w, r, h.service.Unlink(r.Context(), itemID, user.ID, linkID)) {
		h.audit(r, "netbox_link.delete", map[string]any{"item_id": itemID, "link_id": linkID})
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *NetBoxHandler) auditLink(r *http.Request, action string, link *models.NetBoxItemLink) {
	h.audit(r, action, map[string]any{"item_id": link.ItemID, "connection_id": link.ProviderID,
		"link_id": link.ID, "object_type": link.Object.ObjectType, "object_id": link.Object.ObjectID})
}

func (h *NetBoxHandler) audit(r *http.Request, action string, details map[string]any) {
	if h.auditor != nil && currentUser(r) != nil {
		// Never copy snapshot fields, query strings or remote error bodies into
		// a store whose visibility cannot follow connection scope revocation.
		h.auditor.LogWithDetails(r, currentUser(r), action, logger.ResourceIntegrationProvider, nil, "", details)
	}
}

func (h *NetBoxHandler) respondError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return true
	}
	var validation *services.NetBoxValidationError
	var input *netbox.ValidationError
	var upstream *netbox.UpstreamError
	var remote *netbox.APIError
	switch {
	case errors.Is(err, repository.ErrNotFound), errors.Is(err, services.ErrNetBoxUnavailable),
		errors.Is(err, services.ErrCredentialScopeMismatch), errors.Is(err, services.ErrCredentialPurposeMismatch):
		respondNotFound(w, r, "netbox_connection_or_link")
	case errors.Is(err, services.ErrNetBoxPermission):
		respondForbidden(w, r)
	case errors.Is(err, repository.ErrConcurrentUpdate):
		respondConflict(w, r, "The NetBox connection, item or snapshot changed. Reload and retry.")
	case errors.Is(err, repository.ErrDuplicateEntry):
		respondConflict(w, r, "A NetBox connection with these identifiers already exists")
	case errors.As(err, &validation):
		respondBadRequest(w, r, validation.Error())
	case errors.As(err, &input):
		respondBadRequest(w, r, input.Error())
	case errors.Is(err, services.ErrCredentialDisabled):
		respondServiceUnavailable(w, r, "NetBox credentials are disabled")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		respondError(w, r, restapi.NewAPIError(http.StatusGatewayTimeout, "NETBOX_REQUEST_CANCELED", "NetBox request was canceled or timed out"))
	case errors.As(err, &remote) && remote.StatusCode == http.StatusNotFound:
		respondError(w, r, restapi.NewAPIError(http.StatusNotFound, "NETBOX_OBJECT_UNAVAILABLE", "NetBox object was not found or is not accessible"))
	case errors.As(err, &remote), errors.As(err, &upstream):
		respondError(w, r, restapi.NewAPIError(http.StatusBadGateway, "NETBOX_UPSTREAM_ERROR", "NetBox could not complete the read request"))
	default:
		respondInternalError(w, r, err)
	}
	return false
}
