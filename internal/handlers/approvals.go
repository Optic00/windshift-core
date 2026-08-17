package handlers

import (
	"errors"
	"net/http"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

// ApprovalHandler handles runtime approval endpoints — viewing, deciding, and
// canceling in-flight approval requests. Approval-set CRUD is in
// ApprovalSetHandler; this handler is the user-facing decision surface.
type ApprovalHandler struct {
	permService     *services.PermissionService
	approvalService *services.ApprovalService
	itemRepo        *repository.ItemRepository
	auditor         *logger.Auditor
}

// NewApprovalHandler constructs the runtime approval handler.
func NewApprovalHandler(permService *services.PermissionService, approvalService *services.ApprovalService, itemRepo *repository.ItemRepository, auditor *logger.Auditor) *ApprovalHandler {
	return &ApprovalHandler{
		permService:     permService,
		approvalService: approvalService,
		itemRepo:        itemRepo,
		auditor:         auditor,
	}
}

// GetForItem returns the full approval timeline for an item: every request
// (pending, approved, rejected, canceled) with its step instances and
// auditable decisions.
//
// GET /api/items/{id}/approvals
// last review: ser, 260504
func (h *ApprovalHandler) GetForItem(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if !CheckItemPermissionAsActor(w, r, h.itemRepo, h.permService, h.approvalService, itemID, models.PermissionItemView) {
		return
	}

	requests, err := h.approvalService.GetTimelineForItem(r.Context(), itemID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if requests == nil {
		requests = []*models.ApprovalRequest{}
	}
	respondJSONOK(w, requests)
}

// Get returns a single approval request with full audit log.
//
// GET /api/approvals/{id}
// last review: ser, 040526
func (h *ApprovalHandler) Get(w http.ResponseWriter, r *http.Request) {
	requestID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	req, err := h.approvalService.GetRequest(r.Context(), requestID)
	if err != nil {
		if errors.Is(err, services.ErrApprovalNotFound) {
			respondNotFound(w, r, "Approval request")
			return
		}
		respondInternalError(w, r, err)
		return
	}
	if !h.userCanViewRequest(user, req) {
		respondNotFound(w, r, "Approval request")
		return
	}
	respondJSONOK(w, req)
}

// MyPending lists approval requests where the calling user is in the active
// approver pool of any pending step. ?status= filters by request status
// ("pending" by default).
//
// GET /api/approvals/mine?status=pending
func (h *ApprovalHandler) MyPending(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	requests, err := h.approvalService.GetForUser(r.Context(), user.ID, status)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if requests == nil {
		requests = []*models.ApprovalRequest{}
	}
	respondJSONOK(w, requests)
}

// decideRequestBody is the JSON payload for POST /api/approvals/{id}/decide.
type decideRequestBody struct {
	Decision string `json:"decision"` // 'approve' | 'reject' | 'comment'
	Comment  string `json:"comment,omitempty"`
}

// Decide records a decision against the active step the actor is in.
//
// POST /api/approvals/{id}/decide
func (h *ApprovalHandler) Decide(w http.ResponseWriter, r *http.Request) {
	requestID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	var body decideRequestBody
	if err := newJSONDecoder(w, r).Decode(&body); err != nil {
		respondValidationError(w, r, "Invalid request body")
		return
	}
	// Approval comments are user-supplied reasoning that surfaces on the
	// approval timeline and audit log; RichText scrubs HTML while keeping
	// the multi-line markdown body intact.
	warnings := sanitize.ApplyAllWithWarnings(
		sanitize.Pair{Target: &body.Comment, Policy: sanitize.RichText, Label: "Comment"},
	)
	switch body.Decision {
	case models.ApprovalDecisionApprove, models.ApprovalDecisionReject, models.ApprovalDecisionComment:
		// ok
	default:
		respondValidationError(w, r, "decision must be 'approve', 'reject', or 'comment'")
		return
	}

	// Authorization: the active-pool snapshot is the permission gate. We deliberately
	// do NOT require item.view here — internal users without workspace access
	// (e.g. a finance reviewer reachable only via portal customer link) must be
	// able to decide on approvals they're explicitly added to. ApprovalService
	// returns a clean "user is not an active approver" error if they're not in
	// the pool, which surfaces as 4xx via respondValidationError below.
	//
	// Light existence check first so unknown request IDs return 404 cleanly
	// rather than the more expensive service path.
	if _, ok := h.requestItemIDOrNotFound(w, r, requestID); !ok {
		return
	}

	decision, req, err := h.approvalService.Decide(r.Context(), requestID, user.ID, body.Decision, body.Comment, services.DecideOptions{})
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	h.auditor.Log(r, user, logger.ActionApprovalDecide, logger.ResourceApprovalRequest, &requestID, body.Decision)

	resp := map[string]any{
		"decision": decision,
		"request":  req,
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	respondJSONOK(w, resp)
}

// cancelRequestBody is the JSON payload for POST /api/approvals/{id}/cancel.
type cancelRequestBody struct {
	Comment string `json:"comment,omitempty"`
}

// Cancel manually cancels a pending approval. Only the requestor (the user who
// triggered the entry) or an item-edit-permitted user (admin path) may cancel.
//
// POST /api/approvals/{id}/cancel
func (h *ApprovalHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	requestID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	var body cancelRequestBody
	if r.ContentLength > 0 {
		if err := newJSONDecoder(w, r).Decode(&body); err != nil {
			respondValidationError(w, r, "Invalid request body")
			return
		}
	}
	warnings := sanitize.ApplyAllWithWarnings(
		sanitize.Pair{Target: &body.Comment, Policy: sanitize.RichText, Label: "Comment"},
	)

	req, err := h.approvalService.GetRequest(r.Context(), requestID)
	if err != nil {
		if errors.Is(err, services.ErrApprovalNotFound) {
			respondNotFound(w, r, "Approval request")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	// Authorization MUST run before the state check — otherwise the 409
	// "not pending" response leaks the existence and lifecycle state of the
	// request to an unauthorized caller. Consistent with the 404-not-403
	// policy in MEMORY.md: unauthorized → 404, never 409.
	authorized := req.TriggeredByUserID == user.ID
	if !authorized {
		workspaceID, err := h.itemRepo.GetWorkspaceID(req.ItemID)
		if err != nil {
			respondNotFound(w, r, "Approval request")
			return
		}
		hasEdit, err := h.permService.HasWorkspacePermission(user.ID, workspaceID, models.PermissionItemEdit)
		if err != nil || !hasEdit {
			respondNotFound(w, r, "Approval request")
			return
		}
		authorized = true
	}
	if !authorized {
		respondNotFound(w, r, "Approval request")
		return
	}

	// Now that we know the caller is allowed to see the request, surface the
	// state-conflict response if it isn't pending.
	if req.Status != models.ApprovalRequestStatusPending {
		respondConflict(w, r, "Approval request is not pending")
		return
	}

	if err := h.approvalService.Cancel(r.Context(), requestID, user.ID, body.Comment, "manual"); err != nil {
		respondInternalError(w, r, err)
		return
	}
	h.auditor.Log(r, user, logger.ActionApprovalCancel, logger.ResourceApprovalRequest, &requestID, "")

	out, err := h.approvalService.GetRequest(r.Context(), requestID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, struct {
		*models.ApprovalRequest
		Warnings []string `json:"warnings,omitempty"`
	}{out, warnings})
}

// delegateRequestBody is the JSON payload for POST /api/approvals/{id}/delegate.
type delegateRequestBody struct {
	ToUserID int    `json:"to_user_id"`
	Comment  string `json:"comment,omitempty"`
}

// Delegate hands the actor's seat in the active step pool to another user.
//
// POST /api/approvals/{id}/delegate
func (h *ApprovalHandler) Delegate(w http.ResponseWriter, r *http.Request) {
	requestID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	var body delegateRequestBody
	if err := newJSONDecoder(w, r).Decode(&body); err != nil {
		respondValidationError(w, r, "Invalid request body")
		return
	}
	warnings := sanitize.ApplyAllWithWarnings(
		sanitize.Pair{Target: &body.Comment, Policy: sanitize.RichText, Label: "Comment"},
	)
	if body.ToUserID == 0 {
		respondValidationError(w, r, "to_user_id is required")
		return
	}

	itemID, ok := h.requestItemIDOrNotFound(w, r, requestID)
	if !ok {
		return
	}
	// Allow approvers without workspace item.view to delegate their seat — they
	// already passed the active-pool gate at request creation, and delegation
	// is a strictly approver-scoped action.
	if !CheckItemPermissionAsActor(w, r, h.itemRepo, h.permService, h.approvalService, itemID, models.PermissionItemView) {
		return
	}

	if err := h.approvalService.Delegate(r.Context(), requestID, user.ID, body.ToUserID, body.Comment); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	h.auditor.Log(r, user, logger.ActionApprovalDelegate, logger.ResourceApprovalRequest, &requestID, "")

	out, err := h.approvalService.GetRequest(r.Context(), requestID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, struct {
		*models.ApprovalRequest
		Warnings []string `json:"warnings,omitempty"`
	}{out, warnings})
}

// RefreshApprovers re-resolves the configured approver_source for a pending
// step (admin only — gated by item.edit). Useful when a source field changed
// mid-flow and the admin wants the new value to take effect.
//
// POST /api/approvals/{id}/steps/{step_id}/refresh-approvers
func (h *ApprovalHandler) RefreshApprovers(w http.ResponseWriter, r *http.Request) {
	requestID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	stepInstanceID, ok := requireIDParam(w, r, "step_id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	itemID, ok := h.requestItemIDOrNotFound(w, r, requestID)
	if !ok {
		return
	}
	if !CheckItemPermission(w, r, h.itemRepo, h.permService, itemID, models.PermissionItemEdit) {
		return
	}

	// Belt-and-braces: confirm the step instance belongs to this request.
	belongs, err := h.approvalService.StepInstanceBelongsToRequest(r.Context(), stepInstanceID, requestID)
	if err != nil || !belongs {
		respondNotFound(w, r, "Approval step")
		return
	}

	type refreshApproversReq struct {
		Comment string `json:"comment,omitempty"`
	}
	body, ok := decodeOptionalJSON[refreshApproversReq](w, r)
	if !ok {
		return
	}
	warnings := sanitize.ApplyAllWithWarnings(
		sanitize.Pair{Target: &body.Comment, Policy: sanitize.RichText, Label: "Comment"},
	)

	if err := h.approvalService.RefreshApprovers(r.Context(), stepInstanceID, user.ID, body.Comment); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	h.auditor.Log(r, user, logger.ActionApprovalRefresh, logger.ResourceApprovalRequest, &requestID, "")

	out, err := h.approvalService.GetRequest(r.Context(), requestID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, struct {
		*models.ApprovalRequest
		Warnings []string `json:"warnings,omitempty"`
	}{out, warnings})
}

// EscalateNow runs the configured escalation policy for a pending step
// immediately (ignores escalation_due_at). Workspace admin only.
//
// POST /api/approvals/{id}/steps/{step_id}/escalate
func (h *ApprovalHandler) EscalateNow(w http.ResponseWriter, r *http.Request) {
	requestID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	stepInstanceID, ok := requireIDParam(w, r, "step_id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	itemID, ok := h.requestItemIDOrNotFound(w, r, requestID)
	if !ok {
		return
	}
	if !CheckItemPermission(w, r, h.itemRepo, h.permService, itemID, models.PermissionWorkspaceAdmin) {
		return
	}

	belongs, err := h.approvalService.StepInstanceBelongsToRequest(r.Context(), stepInstanceID, requestID)
	if err != nil || !belongs {
		respondNotFound(w, r, "Approval step")
		return
	}

	if err := h.approvalService.Escalate(r.Context(), stepInstanceID, user.ID, "manual"); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	h.auditor.Log(r, user, logger.ActionApprovalEscalate, logger.ResourceApprovalRequest, &requestID, "manual")

	out, err := h.approvalService.GetRequest(r.Context(), requestID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, out)
}

// --- helpers ---

// userCanViewRequest returns true if the user has item.view on the request's
// workspace, OR if they're an approver of any step. The latter handles the
// case where a user has been added as an approver via a custom-field reference
// without otherwise having a workspace role.
func (h *ApprovalHandler) userCanViewRequest(user *models.User, req *models.ApprovalRequest) bool {
	workspaceID, err := h.itemRepo.GetWorkspaceID(req.ItemID)
	if err == nil {
		hasView, _ := h.permService.HasWorkspacePermission(user.ID, workspaceID, models.PermissionItemView)
		if hasView {
			return true
		}
	}
	for _, si := range req.StepInstances {
		for _, app := range si.Approvers {
			if app.UserID != nil && *app.UserID == user.ID {
				return true
			}
		}
	}
	return req.TriggeredByUserID == user.ID
}

// requestItemIDOrNotFound looks up the item id for an approval request id; if
// not found it writes a 404 and returns ok=false.
func (h *ApprovalHandler) requestItemIDOrNotFound(w http.ResponseWriter, r *http.Request, requestID int) (int, bool) {
	itemID, err := h.approvalService.GetItemIDForRequest(r.Context(), requestID)
	if err != nil {
		if errors.Is(err, services.ErrApprovalNotFound) {
			respondNotFound(w, r, "Approval request")
		} else {
			respondInternalError(w, r, err)
		}
		return 0, false
	}
	return itemID, true
}
