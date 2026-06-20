package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/sanitize"
	"windshift/internal/scheduler"
	"windshift/internal/services"

	"github.com/teambition/rrule-go"
)

// RecurrenceHandler exposes the item-recurrence surface (RRULE rules +
// generated instances + RRULE preview) on the bearer-token v1 surface so
// MCP and the ws CLI can drive recurring tasks the same way the SPA does.
//
// It mirrors internal/handlers/recurrence.go (the cookie-auth handler) in
// logic but goes through bearer auth + the items:* token scopes, and gates
// on workspace view/edit. As with the rest of the items surface, a
// permission failure collapses to 404 (not 403) so item existence is never
// leaked through the recurrence routes.
type RecurrenceHandler struct {
	BaseHandler
	repo      *repository.RecurrenceRepository
	itemRepo  *repository.ItemRepository
	scheduler *scheduler.RecurrenceScheduler
}

// NewRecurrenceHandler constructs a v1 RecurrenceHandler. The
// RecurrenceScheduler is built from the DB + a fresh WorkflowService (the
// only inputs ForceGenerate needs) rather than the long-running instance
// the server starts for periodic generation — v1 only ever triggers
// on-demand generation for a single rule, so it never calls Start().
func NewRecurrenceHandler(db database.Database, permissionService *services.PermissionService) *RecurrenceHandler {
	return &RecurrenceHandler{
		BaseHandler: NewBaseHandler(db, permissionService),
		repo:        repository.NewRecurrenceRepository(db),
		itemRepo:    repository.NewItemRepository(db),
		scheduler:   scheduler.NewRecurrenceScheduler(db, services.NewWorkflowService(db)),
	}
}

// parseRecurrenceDate parses a date string in RFC3339 or YYYY-MM-DD form.
func parseRecurrenceDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

// requireItemEdit loads the {id} item and verifies the caller can edit it.
// Returns the item on success; on any failure it writes the response and
// returns (nil, false).
func (h *RecurrenceHandler) requireItemEdit(w http.ResponseWriter, r *http.Request) (*models.Item, *models.User, bool) {
	return h.requireItem(w, r, h.Perms.CanEditWorkspace)
}

// requireItem loads the {id} item and verifies the caller can view it.
func (h *RecurrenceHandler) requireItem(w http.ResponseWriter, r *http.Request, permCheck func(int, int) (bool, error)) (*models.Item, *models.User, bool) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return nil, nil, false
	}
	itemID, ok := h.ParsePathID(w, r, "id", "item ID")
	if !ok {
		return nil, nil, false
	}
	item, err := h.itemRepo.FindByID(itemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondError(w, r, restapi.ErrItemNotFound)
			return nil, nil, false
		}
		h.RespondInternalError(w, r)
		return nil, nil, false
	}
	allowed, err := permCheck(user.ID, item.WorkspaceID)
	if err != nil || !allowed {
		h.RespondError(w, r, restapi.ErrItemNotFound)
		return nil, nil, false
	}
	return item, user, true
}

// resolveRule loads the {id} item, checks permission, then loads the
// recurrence rule attached to it. The rule is required (404 when absent)
// for update/delete/instance/generate operations.
func (h *RecurrenceHandler) resolveRule(w http.ResponseWriter, r *http.Request, permCheck func(int, int) (bool, error)) (*models.RecurrenceRule, *models.User, bool) {
	item, user, ok := h.requireItem(w, r, permCheck)
	if !ok {
		return nil, nil, false
	}
	rule, err := h.repo.GetByTemplateItemID(item.ID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondError(w, r, restapi.ErrNotFound)
		return nil, nil, false
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return nil, nil, false
	}
	return rule, user, true
}

// GetRecurrence handles GET /rest/api/v1/items/{id}/recurrence
//
// Absence is a normal state for most items, so (matching the cookie
// surface) this returns 200 with a JSON null body instead of a 404 that
// would force callers to distinguish "no recurrence" from "real error".
//
// @Summary      Get the recurrence rule on an item
// @Description  Returns the item's recurrence rule, or JSON null when the item has no recurrence configured.
// @Tags         recurrence
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Item ID"
// @Success      200  {object}  models.RecurrenceRule  "null when no recurrence is configured"
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid item ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      403  {object}  handlers.ErrorResponse  "Token lacks the items:read scope"
// @Failure      404  {object}  handlers.ErrorResponse  "Item not found or not visible to caller"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /items/{id}/recurrence [get]
func (h *RecurrenceHandler) GetRecurrence(w http.ResponseWriter, r *http.Request) {
	item, _, ok := h.requireItem(w, r, h.Perms.CanViewWorkspace)
	if !ok {
		return
	}
	rule, err := h.repo.GetByTemplateItemID(item.ID)
	if errors.Is(err, repository.ErrNotFound) {
		// Absence is the normal state — return JSON null, not 404.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("null"))
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, rule)
}

// CreateRecurrence handles POST /rest/api/v1/items/{id}/recurrence
//
// @Summary      Create a recurrence rule on an item
// @Description  Creates a new recurrence rule for the item. Returns 409 if a rule already exists.
// @Tags         recurrence
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id    path      int                              true  "Item ID"
// @Param        body  body      models.CreateRecurrenceRequest   true  "Recurrence rule to create"
// @Success      201   {object}  models.RecurrenceRule
// @Failure      400   {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401   {object}  handlers.ErrorResponse
// @Failure      403   {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404   {object}  handlers.ErrorResponse  "Item not found or not visible to caller"
// @Failure      409   {object}  handlers.ErrorResponse  "A recurrence rule already exists for this item"
// @Failure      500   {object}  handlers.ErrorResponse
// @Router       /items/{id}/recurrence [post]
func (h *RecurrenceHandler) CreateRecurrence(w http.ResponseWriter, r *http.Request) {
	item, user, ok := h.requireItemEdit(w, r)
	if !ok {
		return
	}

	// Reject if a rule already exists — use PUT to update.
	if existing, err := h.repo.GetByTemplateItemID(item.ID); err == nil && existing != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusConflict, restapi.ErrCodeConflict, "Recurrence rule already exists for this item"))
		return
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		h.RespondInternalError(w, r)
		return
	}

	req, ok := decodeRecurrenceCreateRequest(h, w, r)
	if !ok {
		return
	}

	rule := buildRecurrenceRule(item.ID, item.WorkspaceID, req, user.ID)
	ruleID, err := h.repo.Create(rule)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	createdRule, err := h.repo.GetByID(ruleID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondCreated(w, createdRule)
}

// UpdateRecurrence handles PUT /rest/api/v1/items/{id}/recurrence
//
// @Summary      Update a recurrence rule
// @Description  Partial update of the recurrence rule attached to the item. Only fields present in the body are changed.
// @Tags         recurrence
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        id    path      int                              true  "Item ID"
// @Param        body  body      models.UpdateRecurrenceRequest   true  "Fields to update"
// @Success      200   {object}  models.RecurrenceRule
// @Failure      400   {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401   {object}  handlers.ErrorResponse
// @Failure      403   {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404   {object}  handlers.ErrorResponse  "Item or recurrence rule not found"
// @Failure      500   {object}  handlers.ErrorResponse
// @Router       /items/{id}/recurrence [put]
func (h *RecurrenceHandler) UpdateRecurrence(w http.ResponseWriter, r *http.Request) {
	rule, _, ok := h.resolveRule(w, r, h.Perms.CanEditWorkspace)
	if !ok {
		return
	}

	req, ok := decodeRecurrenceUpdateRequest(h, w, r)
	if !ok {
		return
	}

	if err := applyRecurrenceUpdate(rule, req); err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, err.Error()))
		return
	}

	if err := h.repo.Update(rule); err != nil {
		h.RespondInternalError(w, r)
		return
	}

	updatedRule, err := h.repo.GetByID(rule.ID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, updatedRule)
}

// DeleteRecurrence handles DELETE /rest/api/v1/items/{id}/recurrence
//
// @Summary      Delete a recurrence rule
// @Tags         recurrence
// @Security     BearerAuth
// @Param        id   path  int  true  "Item ID"
// @Success      204  "Recurrence rule deleted"
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid item ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      403  {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404  {object}  handlers.ErrorResponse  "Item or recurrence rule not found"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /items/{id}/recurrence [delete]
func (h *RecurrenceHandler) DeleteRecurrence(w http.ResponseWriter, r *http.Request) {
	rule, _, ok := h.resolveRule(w, r, h.Perms.CanEditWorkspace)
	if !ok {
		return
	}
	if err := h.repo.Delete(rule.ID); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondNoContent(w)
}

type recurrenceInstanceListResponse struct {
	Items      []models.RecurrenceInstance `json:"instances"`
	Pagination restapi.PaginationMeta      `json:"pagination"`
}

// ListInstances handles GET /rest/api/v1/items/{id}/recurrence/instances
//
// @Summary      List generated instances for a recurrence rule
// @Description  Paginated list of instances generated from the item's recurrence rule. `limit` (1-100, default 20) and `offset` (default 0) query parameters control paging.
// @Tags         recurrence
// @Produce      json
// @Security     BearerAuth
// @Param        id      path   int  true   "Item ID"
// @Param        limit   query  int  false  "Page size (1-100, default 20)"
// @Param        offset  query  int  false  "Offset (default 0)"
// @Success      200  {object}  handlers.recurrenceInstanceListResponse
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid item ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      403  {object}  handlers.ErrorResponse  "Token lacks the items:read scope"
// @Failure      404  {object}  handlers.ErrorResponse  "Item or recurrence rule not found"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /items/{id}/recurrence/instances [get]
func (h *RecurrenceHandler) ListInstances(w http.ResponseWriter, r *http.Request) {
	rule, _, ok := h.resolveRule(w, r, h.Perms.CanViewWorkspace)
	if !ok {
		return
	}

	limit := 20
	offset := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		if l, err := strconv.Atoi(s); err == nil && l > 0 {
			limit = l
			if limit > 100 {
				limit = 100
			}
		}
	}
	if s := r.URL.Query().Get("offset"); s != "" {
		if o, err := strconv.Atoi(s); err == nil && o >= 0 {
			offset = o
		}
	}

	instances, err := h.repo.GetInstancesByRuleID(rule.ID, limit, offset)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	total, err := h.repo.CountInstancesByRuleID(rule.ID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}

	instancesOut := make([]models.RecurrenceInstance, 0, len(instances))
	for _, inst := range instances {
		instancesOut = append(instancesOut, *inst)
	}
	h.RespondOK(w, recurrenceInstanceListResponse{
		Items: instancesOut,
		Pagination: restapi.NewPaginationMeta(restapi.PaginationParams{Page: 0, Limit: limit}, total),
	})
}

type recurrenceForceGenerateResponse struct {
	InstancesGenerated int `json:"instances_generated"`
}

// ForceGenerate handles POST /rest/api/v1/items/{id}/recurrence/generate
//
// @Summary      Force-generate instances for a recurrence rule
// @Description  Triggers immediate generation of recurrence instances due up to the rule's lead-time horizon, bypassing the periodic scheduler.
// @Tags         recurrence
// @Produce      json
// @Security     BearerAuth
// @Param        id   path  int  true  "Item ID"
// @Success      200  {object}  handlers.recurrenceForceGenerateResponse
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid item ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      403  {object}  handlers.ErrorResponse  "Token lacks the items:write scope"
// @Failure      404  {object}  handlers.ErrorResponse  "Item or recurrence rule not found"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /items/{id}/recurrence/generate [post]
func (h *RecurrenceHandler) ForceGenerate(w http.ResponseWriter, r *http.Request) {
	rule, _, ok := h.resolveRule(w, r, h.Perms.CanEditWorkspace)
	if !ok {
		return
	}
	count, err := h.scheduler.ForceGenerate(rule.ID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, recurrenceForceGenerateResponse{InstancesGenerated: count})
}

type rrulePreviewRequest struct {
	RRule   string `json:"rrule"`
	DtStart string `json:"dtstart"`
	Count   int    `json:"count,omitempty"`
}

type rrulePreviewResponse struct {
	RRule       string   `json:"rrule"`
	DtStart     string   `json:"dtstart"`
	Occurrences []string `json:"occurrences"`
}

// PreviewRRule handles POST /rest/api/v1/recurrence-rules/preview
//
// @Summary      Preview RRULE occurrences
// @Description  Validates an iCalendar RRULE and returns the first N occurrences (default 10, max 50) from a given dtstart. Pure validation — no rule or item is created.
// @Tags         recurrence
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        body  body      handlers.rrulePreviewRequest  true  "RRULE to preview"
// @Success      200   {object}  handlers.rrulePreviewResponse
// @Failure      400   {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401   {object}  handlers.ErrorResponse
// @Failure      403   {object}  handlers.ErrorResponse  "Token lacks the items:read scope"
// @Failure      500   {object}  handlers.ErrorResponse
// @Router       /recurrence-rules/preview [post]
func (h *RecurrenceHandler) PreviewRRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.RequireAuth(w, r); !ok {
		return
	}

	var req rrulePreviewRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	sanitize.ApplyAll(
		sanitize.Pair{Target: &req.RRule, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &req.DtStart, Policy: sanitize.ShortIdentifier},
	)

	if req.RRule == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "rrule is required"))
		return
	}
	if req.DtStart == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "dtstart is required"))
		return
	}

	dtstart, err := parseRecurrenceDate(req.DtStart)
	if err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Invalid dtstart format (use RFC3339 or YYYY-MM-DD)"))
		return
	}

	ruleOpt, err := rrule.StrToROption(req.RRule)
	if err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Invalid RRULE format: "+err.Error()))
		return
	}
	ruleOpt.Dtstart = dtstart

	rule, err := rrule.NewRRule(*ruleOpt)
	if err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Failed to create RRULE: "+err.Error()))
		return
	}

	count := 10
	if req.Count > 0 && req.Count <= 50 {
		count = req.Count
	}

	occurrences := rule.All()
	if len(occurrences) > count {
		occurrences = occurrences[:count]
	}

	dates := make([]string, len(occurrences))
	for i, t := range occurrences {
		dates[i] = t.Format(time.RFC3339)
	}

	h.RespondOK(w, rrulePreviewResponse{
		RRule:       req.RRule,
		DtStart:     dtstart.Format(time.RFC3339),
		Occurrences: dates,
	})
}

// decodeRecurrenceCreateRequest decodes + sanitizes + validates a create
// request body. Returns ok=false (response already written) on failure.
func decodeRecurrenceCreateRequest(h *RecurrenceHandler, w http.ResponseWriter, r *http.Request) (models.CreateRecurrenceRequest, bool) {
	var req models.CreateRecurrenceRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return req, false
	}
	// Sanitize the identifier-shaped inputs before validation so bogus
	// parse-error strings never reach user-facing messages.
	sanitize.ApplyAll(
		sanitize.Pair{Target: &req.RRule, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &req.Timezone, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &req.DtStart, Policy: sanitize.ShortIdentifier},
	)
	if req.DtEnd != nil {
		sanitize.Apply(req.DtEnd, sanitize.ShortIdentifier)
	}

	if req.RRule == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "rrule is required"))
		return req, false
	}
	if _, err := rrule.StrToROption(req.RRule); err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Invalid RRULE format: "+err.Error()))
		return req, false
	}
	if req.DtStart == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeMissingField, "dtstart is required"))
		return req, false
	}
	if _, err := parseRecurrenceDate(req.DtStart); err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Invalid dtstart format (use RFC3339 or YYYY-MM-DD)"))
		return req, false
	}
	if req.DtEnd != nil && *req.DtEnd != "" {
		if _, err := parseRecurrenceDate(*req.DtEnd); err != nil {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Invalid dtend format"))
			return req, false
		}
	}
	return req, true
}

// decodeRecurrenceUpdateRequest decodes + sanitizes an update request body.
func decodeRecurrenceUpdateRequest(h *RecurrenceHandler, w http.ResponseWriter, r *http.Request) (models.UpdateRecurrenceRequest, bool) {
	var req models.UpdateRecurrenceRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return req, false
	}
	sanitize.ApplyAll(
		sanitize.Pair{Target: req.RRule, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: req.Timezone, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: req.DtStart, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: req.DtEnd, Policy: sanitize.ShortIdentifier},
	)
	return req, true
}

// buildRecurrenceRule materializes a RecurrenceRule from a create request,
// applying the same defaults as the cookie handler.
func buildRecurrenceRule(itemID, workspaceID int, req models.CreateRecurrenceRequest, userID int) *models.RecurrenceRule {
	dtstart, _ := parseRecurrenceDate(req.DtStart)
	var dtend *time.Time
	if req.DtEnd != nil && *req.DtEnd != "" {
		t, _ := parseRecurrenceDate(*req.DtEnd)
		dtend = &t
	}

	timezone := "UTC"
	if req.Timezone != "" {
		timezone = req.Timezone
	}

	leadTimeDays := 14
	if req.LeadTimeDays != nil {
		leadTimeDays = *req.LeadTimeDays
	}

	copyAssignee := true
	if req.CopyAssignee != nil {
		copyAssignee = *req.CopyAssignee
	}
	copyPriority := true
	if req.CopyPriority != nil {
		copyPriority = *req.CopyPriority
	}
	copyCustomFields := true
	if req.CopyCustomFields != nil {
		copyCustomFields = *req.CopyCustomFields
	}
	copyDescription := true
	if req.CopyDescription != nil {
		copyDescription = *req.CopyDescription
	}

	createdBy := userID
	return &models.RecurrenceRule{
		TemplateItemID:   itemID,
		WorkspaceID:      workspaceID,
		RRule:            req.RRule,
		DtStart:          dtstart,
		DtEnd:            dtend,
		Timezone:         timezone,
		LeadTimeDays:     leadTimeDays,
		CopyAssignee:     copyAssignee,
		CopyPriority:     copyPriority,
		CopyCustomFields: copyCustomFields,
		CopyDescription:  copyDescription,
		StatusOnCreate:   req.StatusOnCreate,
		IsActive:         true,
		CreatedBy:        &createdBy,
	}
}

// applyRecurrenceUpdate applies the non-nil fields of an update request to
// an existing rule in place, validating the parsed values. Returns an
// error describing the first validation problem (mirrors the cookie
// handler's per-field 400 responses).
func applyRecurrenceUpdate(rule *models.RecurrenceRule, req models.UpdateRecurrenceRequest) error {
	if req.RRule != nil {
		if _, err := rrule.StrToROption(*req.RRule); err != nil {
			return errors.New("Invalid RRULE format: " + err.Error())
		}
		rule.RRule = *req.RRule
	}
	if req.DtStart != nil {
		dtstart, err := parseRecurrenceDate(*req.DtStart)
		if err != nil {
			return errors.New("Invalid dtstart format")
		}
		rule.DtStart = dtstart
	}
	if req.DtEnd != nil {
		if *req.DtEnd == "" {
			rule.DtEnd = nil
		} else {
			t, err := parseRecurrenceDate(*req.DtEnd)
			if err != nil {
				return errors.New("Invalid dtend format")
			}
			rule.DtEnd = &t
		}
	}
	if req.Timezone != nil {
		rule.Timezone = *req.Timezone
	}
	if req.LeadTimeDays != nil {
		rule.LeadTimeDays = *req.LeadTimeDays
	}
	if req.CopyAssignee != nil {
		rule.CopyAssignee = *req.CopyAssignee
	}
	if req.CopyPriority != nil {
		rule.CopyPriority = *req.CopyPriority
	}
	if req.CopyCustomFields != nil {
		rule.CopyCustomFields = *req.CopyCustomFields
	}
	if req.CopyDescription != nil {
		rule.CopyDescription = *req.CopyDescription
	}
	if req.StatusOnCreate != nil {
		rule.StatusOnCreate = req.StatusOnCreate
	}
	if req.IsActive != nil {
		rule.IsActive = *req.IsActive
	}
	return nil
}
