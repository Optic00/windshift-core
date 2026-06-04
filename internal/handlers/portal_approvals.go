package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"windshift/internal/models"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

// Portal-side approval endpoints. Portal customers and internal users can
// decide on approvals where they're in the active pool. Authentication is
// handled upstream by RequirePortalAuth; these handlers pull the active actor
// via getAuthFromContext.
//
// The active-pool snapshot is the gate (just like /api/approvals/{id}/decide
// after slice 4 loosened item.view): if the actor isn't in the pool,
// ApprovalService returns "actor is not an active approver" → 4xx.

// GetMyApprovals lists pending approvals where the authenticated portal actor
// is in the active pool.
//
// GET /portal/{slug}/approvals/mine
func (h *PortalHandler) GetMyApprovals(w http.ResponseWriter, r *http.Request) {
	if h.approvalService == nil {
		respondServiceUnavailable(w, r, "approvals not configured")
		return
	}

	actor, ok := h.requirePortalApprovalActor(w, r)
	if !ok {
		return
	}

	status := r.URL.Query().Get("status")
	requests, err := h.getApprovalsForPortalActor(actor, status)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if requests == nil {
		requests = []*models.ApprovalRequest{}
	}
	respondJSONOK(w, requests)
}

// GetApproval returns a single approval request for the authenticated portal
// actor. Visibility gate: the actor must be in the snapshot pool of any step on
// the request.
//
// GET /portal/{slug}/approvals/{id}
func (h *PortalHandler) GetApproval(w http.ResponseWriter, r *http.Request) {
	if h.approvalService == nil {
		respondServiceUnavailable(w, r, "approvals not configured")
		return
	}

	actor, ok := h.requirePortalApprovalActor(w, r)
	if !ok {
		return
	}
	requestID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	req, err := h.approvalService.GetRequest(requestID)
	if err != nil {
		respondNotFound(w, r, "Approval request")
		return
	}
	if !portalActorCanViewRequest(actor, req) {
		respondNotFound(w, r, "Approval request")
		return
	}
	respondJSONOK(w, req)
}

// portalDecideRequest is the JSON payload for the portal-side decide.
type portalDecideRequest struct {
	Decision string `json:"decision"`
	Comment  string `json:"comment,omitempty"`
}

// DecideAsPortalCustomer records a decision from a portal actor.
//
// POST /portal/{slug}/approvals/{id}/decide
func (h *PortalHandler) DecideAsPortalCustomer(w http.ResponseWriter, r *http.Request) {
	if h.approvalService == nil {
		respondServiceUnavailable(w, r, "approvals not configured")
		return
	}

	actor, ok := h.requirePortalApprovalActor(w, r)
	if !ok {
		return
	}
	requestID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	var body portalDecideRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondValidationError(w, r, "Invalid request body")
		return
	}
	// Portal decision comments surface on the same approval timeline as
	// the internal Decide path. Mirror the policy.
	warnings := sanitize.ApplyAllWithWarnings(
		sanitize.Pair{Target: &body.Comment, Policy: sanitize.RichText, Label: "Comment"},
	)
	switch body.Decision {
	case models.ApprovalDecisionApprove, models.ApprovalDecisionReject, models.ApprovalDecisionComment:
	default:
		respondValidationError(w, r, "decision must be 'approve', 'reject', or 'comment'")
		return
	}

	var (
		decision *models.ApprovalDecision
		req      *models.ApprovalRequest
		err      error
	)
	switch {
	case actor.userID != nil && portalActorCanActAsUser(actor, h.approvalService, requestID):
		decision, req, err = h.approvalService.Decide(r.Context(), requestID, *actor.userID, body.Decision, body.Comment, services.DecideOptions{})
	case actor.customerID != nil:
		decision, req, err = h.approvalService.DecideAsCustomer(r.Context(), requestID, *actor.customerID, body.Decision, body.Comment, services.DecideOptions{})
	case actor.userID != nil:
		decision, req, err = h.approvalService.Decide(r.Context(), requestID, *actor.userID, body.Decision, body.Comment, services.DecideOptions{})
	}
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	resp := map[string]any{
		"decision": decision,
		"request":  req,
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	respondJSONOK(w, resp)
}

type portalApprovalActor struct {
	userID     *int
	customerID *int
}

// requirePortalApprovalActor resolves every approval identity available to the
// active portal request. Internal users are valid portal approval actors on
// their own; if they also have a linked portal-customer row, both identities are
// kept so pooled approvals addressed to either row appear in the portal view.
func (h *PortalHandler) requirePortalApprovalActor(w http.ResponseWriter, r *http.Request) (portalApprovalActor, bool) {
	internalUserID, customerID := h.getAuthFromContext(r)
	actor := portalApprovalActor{userID: internalUserID, customerID: customerID}
	if internalUserID != nil && customerID == nil {
		cid, err := h.portalService.GetCustomerIDForUser(r.Context(), *internalUserID)
		if err == nil && cid > 0 {
			actor.customerID = &cid
		} else if err != nil && !errors.Is(err, services.ErrPortalCustomerNotFound) {
			respondInternalError(w, r, err)
			return portalApprovalActor{}, false
		}
	}
	if actor.userID == nil && actor.customerID == nil {
		respondUnauthorized(w, r)
		return portalApprovalActor{}, false
	}
	return actor, true
}

func (h *PortalHandler) getApprovalsForPortalActor(actor portalApprovalActor, status string) ([]*models.ApprovalRequest, error) {
	byID := map[int]*models.ApprovalRequest{}
	if actor.customerID != nil {
		requests, err := h.approvalService.GetForPortalCustomer(*actor.customerID, status)
		if err != nil {
			return nil, err
		}
		for _, req := range requests {
			byID[req.ID] = req
		}
	}
	if actor.userID != nil {
		requests, err := h.approvalService.GetForUser(*actor.userID, status)
		if err != nil {
			return nil, err
		}
		for _, req := range requests {
			byID[req.ID] = req
		}
	}
	out := make([]*models.ApprovalRequest, 0, len(byID))
	for _, req := range byID {
		out = append(out, req)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// portalActorCanViewRequest returns true if either actor identity is in any
// step's approver pool — same gate as the internal userCanViewRequest helper.
func portalActorCanViewRequest(actor portalApprovalActor, req *models.ApprovalRequest) bool {
	for _, si := range req.StepInstances {
		for _, app := range si.Approvers {
			if portalActorMatchesApprover(actor, app) {
				return true
			}
		}
	}
	return false
}

func portalActorCanActAsUser(actor portalApprovalActor, svc *services.ApprovalService, requestID int) bool {
	if actor.userID == nil {
		return false
	}
	req, err := svc.GetRequest(requestID)
	if err != nil {
		return false
	}
	for _, si := range req.StepInstances {
		if si.Status != models.ApprovalStepStatusPending || si.StartedAt == nil {
			continue
		}
		for _, app := range si.Approvers {
			if app.IsActive && app.UserID != nil && *app.UserID == *actor.userID {
				return true
			}
		}
	}
	return false
}

func portalActorMatchesApprover(actor portalApprovalActor, app models.ApprovalStepApprover) bool {
	if actor.customerID != nil && app.PortalCustomerID != nil && *app.PortalCustomerID == *actor.customerID {
		return true
	}
	return actor.userID != nil && app.UserID != nil && *app.UserID == *actor.userID
}
