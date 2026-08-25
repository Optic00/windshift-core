package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/models"
	"windshift/internal/restapi"
	"windshift/internal/services"
)

// LinkHandler is the bearer-auth thin HTTP shim around
// services.ItemLinkService. All real logic (permission checks, dup
// detection, page-ACL filtering, notifications) lives in the service so
// this handler can stay an HTTP-only adapter. Constructed by passing
// the already-wired service from the legacy cookie-auth handler (see
// router.go) — that way both auth paths share one fully-set-up service
// (asset checker, page checker, notification emitter, action emitter).
type LinkHandler struct {
	BaseHandler
	svc   *services.ItemLinkService
	items *services.ItemCRUDService
}

// NewLinkHandler wires the v1 link surface against a service supplied
// by the caller. Pass the same *services.ItemLinkService that the
// cookie-auth handler uses so notifications, action events, asset and
// page permission checks all behave identically across surfaces.
func NewLinkHandler(base BaseHandler, svc *services.ItemLinkService) *LinkHandler {
	return &LinkHandler{
		BaseHandler: base,
		svc:         svc,
		items:       services.NewItemCRUDService(base.DB),
	}
}

// --- request payloads ---

type linkCreateRequest struct {
	LinkTypeID int    `json:"link_type_id"`
	SourceType string `json:"source_type"`
	SourceID   int    `json:"source_id"`
	TargetType string `json:"target_type"`
	TargetID   int    `json:"target_id"`
}

const maxBatchLinkItems = 100

type batchItemLinksResponse struct {
	ItemID          int               `json:"item_id"`
	Outgoing        []models.ItemLink `json:"outgoing"`
	Incoming        []models.ItemLink `json:"incoming"`
	HasMoreLinks    bool              `json:"has_more_links"`
	NextAfterLinkID int               `json:"next_after_link_id,omitempty"`
}

// --- endpoints ---

// ListLinkTypes handles GET /rest/api/v1/link-types
//
// ListLinkTypes returns the active link-type catalog. Scope: items:read.
//
// @Summary      List active link types
// @Description  Returns the active link-type catalog (e.g. Relates To, Page, Tests). Used to validate `link_type_id` on link creation.
// @Tags         links
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   models.LinkType
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /link-types [get]
func (h *LinkHandler) ListLinkTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.RequireAuth(w, r); !ok {
		return
	}
	types, err := h.svc.ListLinkTypes(false)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, types)
}

// GetLinksBatch handles GET /rest/api/v1/links/batch.
//
// @Summary      List direct links for a batch of items
// @Description  Selects up to 100 visible anchor items by CQL or explicit ids and returns at most 50 direct item-to-item links per anchor. The operation is one hop only. Supply exactly one of `ql` or `ids`. When an item has more links, request that single item with `ids` and `after_id` to continue.
// @Tags         links
// @Produce      json
// @Security     BearerAuth
// @Param        ql                     query     string  false  "CQL selecting anchor items"
// @Param        ids                    query     string  false  "Comma-separated anchor item ids (max 100)"
// @Param        page                   query     int     false  "CQL item page (1-based)"
// @Param        limit                  query     int     false  "CQL items per page (max 100)"
// @Param        sort                   query     string  false  "CQL item sort field"
// @Param        order                  query     string  false  "CQL item sort order: asc or desc"
// @Param        after_id               query     int     false  "Exclusive link cursor; valid only with one explicit item id"
// @Param        include_custom_fields  query     bool    false  "Include links managed by custom fields"
// @Success      200  {object}  handlers.PaginatedResponse{data=[]handlers.batchItemLinksResponse}
// @Failure      400  {object}  handlers.ErrorResponse
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      403  {object}  handlers.ErrorResponse
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /links/batch [get]
func (h *LinkHandler) GetLinksBatch(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}

	query := r.URL.Query()
	ql := strings.TrimSpace(query.Get("ql"))
	rawIDs := strings.TrimSpace(query.Get("ids"))
	if (ql == "") == (rawIDs == "") {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "exactly one of ql or ids is required"))
		return
	}

	pagination := h.ParsePagination(r)
	itemIDs := []int{}
	total := 0
	if ql != "" {
		accessibleWorkspaceIDs, err := h.Perms.GetAccessibleWorkspaceIDs(user.ID)
		if err != nil {
			h.RespondInternalError(w, r)
			return
		}
		page, err := h.items.ListIDsWithQLPageContext(r.Context(), services.ListWithQLParams{
			QLQuery:      ql,
			WorkspaceIDs: accessibleWorkspaceIDs,
			UserID:       user.ID,
			Pagination: services.PaginationParams{
				Limit:  pagination.Limit,
				Offset: pagination.Offset,
			},
			SortBy:  pagination.SortBy,
			SortAsc: pagination.SortAsc,
		})
		if errors.Is(err, services.ErrQLQuery) {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, err.Error()))
			return
		}
		if err != nil {
			h.RespondInternalError(w, r)
			return
		}
		itemIDs = page.IDs
		total = page.Total
	} else {
		seen := make(map[int]struct{})
		for _, id := range parseIDList(rawIDs) {
			if id <= 0 {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			itemIDs = append(itemIDs, id)
		}
		if len(itemIDs) == 0 {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "ids must contain at least one positive item id"))
			return
		}
		if len(itemIDs) > maxBatchLinkItems {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "too many ids (max 100)"))
			return
		}
		pagination.Page = 1
		pagination.Limit = maxBatchLinkItems
		pagination.Offset = 0
		total = len(itemIDs)
	}

	afterID := 0
	if rawAfterID := strings.TrimSpace(query.Get("after_id")); rawAfterID != "" {
		parsed, err := strconv.Atoi(rawAfterID)
		if err != nil || parsed < 0 {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "after_id must be a non-negative integer"))
			return
		}
		if ql != "" || len(itemIDs) != 1 {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "after_id requires exactly one explicit item id"))
			return
		}
		afterID = parsed
	}

	groups, err := h.svc.ListOneHopItemLinksPageWithChecks(
		r.Context(),
		user.ID,
		itemIDs,
		afterID,
		services.MaxOneHopLinksPerItem,
		query.Get("include_custom_fields") == "true",
	)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	response := make([]batchItemLinksResponse, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		group := groups[itemID]
		entry := batchItemLinksResponse{
			ItemID:       itemID,
			Outgoing:     group.Outgoing,
			Incoming:     group.Incoming,
			HasMoreLinks: group.HasMore,
		}
		if group.HasMore {
			entry.NextAfterLinkID = group.NextAfterID
		}
		response = append(response, entry)
	}
	h.RespondPaginated(w, response, pagination, total)
}

// CreateLink handles POST /rest/api/v1/links
//
// CreateLink creates a cross-entity link. Scope: items:write. The
// service handles permission gating on source (edit) and target (view),
// the same-workspace constraint for page links, link-type / entity-type
// compatibility, and duplicate detection.
//
// @Summary      Create a cross-entity link
// @Description  Creates a link between two entities (item/page/test_case). The link type must allow the given entity types; pages must share a workspace with the source item.
// @Tags         links
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      handlers.linkCreateRequest  true  "Link to create"
// @Success      201   {object}  models.ItemLink
// @Failure      400   {object}  handlers.ErrorResponse  "Invalid request body, missing required field, or entity types not allowed for the link type"
// @Failure      401   {object}  handlers.ErrorResponse
// @Failure      404   {object}  handlers.ErrorResponse  "Source or target entity not found / not accessible to caller"
// @Failure      409   {object}  handlers.ErrorResponse  "A link between these entities already exists"
// @Failure      500   {object}  handlers.ErrorResponse
// @Router       /links [post]
func (h *LinkHandler) CreateLink(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	var req linkCreateRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	if req.LinkTypeID == 0 || req.SourceType == "" || req.SourceID == 0 || req.TargetType == "" || req.TargetID == 0 {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "link_type_id, source_type, source_id, target_type, and target_id are required"))
		return
	}

	link, err := h.svc.CreateLinkWithChecks(user.ID, services.CreateItemLinkParams{
		LinkTypeID: req.LinkTypeID,
		SourceType: req.SourceType,
		SourceID:   req.SourceID,
		TargetType: req.TargetType,
		TargetID:   req.TargetID,
	})
	if err != nil {
		h.respondLinkServiceError(w, r, req.SourceType, err)
		return
	}
	h.RespondCreated(w, link)
}

// DeleteLink handles DELETE /rest/api/v1/links/{id}
//
// DeleteLink removes a link by id. Scope: items:write. The service
// enforces edit permission on the link's source entity.
//
// @Summary      Delete a link by ID
// @Tags         links
// @Security     BearerAuth
// @Param        id   path  int  true  "Link ID"
// @Success      204  "Link deleted"
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid link ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      404  {object}  handlers.ErrorResponse  "Link not found or caller lacks edit on the source entity"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /links/{id} [delete]
func (h *LinkHandler) DeleteLink(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "link ID")
	if !ok {
		return
	}
	if err := h.svc.DeleteLinkWithChecks(user.ID, id); err != nil {
		h.respondLinkServiceError(w, r, "link", err)
		return
	}
	h.RespondNoContent(w)
}

// GetLinksForEntity handles GET /rest/api/v1/items/{id}/links,
// GET /rest/api/v1/pages/{id}/links, and GET /rest/api/v1/test-cases/{id}/links.
//
// GetLinksForEntity dispatches to the entity-specific list endpoint. The
// caller routes /items/{id}/links, /pages/{id}/links, and
// /test-cases/{id}/links to this same handler — the URL prefix decides
// the entity type. Scope: caller-route decides (items:read /
// pages:read).
//
// @Summary      List links anchored on an entity
// @Description  Returns outgoing and incoming links for the given entity. The URL prefix (items / pages / test-cases) selects the entity type. Required scope depends on the route: items:read for items/test-cases, pages:read for pages.
// @Tags         links
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Entity ID (item / page / test case)"
// @Success      200  {object}  map[string]any  "Object with `outgoing` and `incoming` arrays of models.ItemLink"
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid entity ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      404  {object}  handlers.ErrorResponse  "Entity not found or not accessible to caller"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /items/{id}/links [get]
// @Router       /pages/{id}/links [get]
// @Router       /test-cases/{id}/links [get]
func (h *LinkHandler) GetLinksForEntity(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "entity ID")
	if !ok {
		return
	}
	entityType := "item"
	switch {
	case strings.Contains(r.URL.Path, "/test-cases/"):
		entityType = "test_case"
	case strings.Contains(r.URL.Path, "/pages/"):
		entityType = "page"
	}

	outgoing, incoming, err := h.svc.ListLinksForEntityWithChecks(user.ID, entityType, id)
	if err != nil {
		h.respondLinkServiceError(w, r, entityType, err)
		return
	}
	h.RespondOK(w, map[string]any{
		"outgoing": outgoing,
		"incoming": incoming,
	})
}

// respondLinkServiceError maps the typed errors that ItemLinkService
// returns onto v1's APIError shape. Mirrors the cookie-auth handler's
// handlers.respondLinkServiceError; kept v1-local so the two HTTP layers
// don't need to import each other.
func (h *LinkHandler) respondLinkServiceError(w http.ResponseWriter, r *http.Request, fallbackResource string, err error) {
	switch {
	case errors.Is(err, services.ErrLinkSelfReference),
		errors.Is(err, services.ErrLinkInvalidEntityType):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, err.Error()))
	case errors.Is(err, services.ErrInvalidLinkTypeForEntities):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "the selected link type does not allow these entity types"))
	case errors.Is(err, services.ErrLinkExists):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeValidationFailed, "a link between these entities already exists"))
	case errors.Is(err, services.ErrLinkNotFound),
		errors.Is(err, services.ErrLinkCrossWorkspacePage),
		services.IsEntityNotAccessible(err):
		// 404 covers "missing" + "no permission" + cross-workspace page —
		// matches the cookie-auth handler's existence-leak policy.
		h.RespondNotFound(w, r)
		_ = fallbackResource // for future per-type messaging when we surface one
	default:
		h.RespondInternalError(w, r)
	}
}
