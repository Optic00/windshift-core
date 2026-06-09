package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/services"
)

// WorkspaceAgentBindingHandler exposes the workspace-admin CRUD for the
// coding-agent harness bindings (WI-88). Every mutation goes through
// services.BindingService so the WI-87 acting-identity chokepoint always
// runs at create time. The Candidates endpoint surfaces the picker
// contents to the UI so admins can't see ineligible options.
type WorkspaceAgentBindingHandler struct {
	bindings          *services.BindingService
	identity          *services.AgentActingIdentityService
	permissionService *services.PermissionService
	auditor           *logger.Auditor
}

// NewWorkspaceAgentBindingHandler constructs the handler.
func NewWorkspaceAgentBindingHandler(
	bindings *services.BindingService,
	identity *services.AgentActingIdentityService,
	permissionService *services.PermissionService,
	auditor *logger.Auditor,
) *WorkspaceAgentBindingHandler {
	return &WorkspaceAgentBindingHandler{
		bindings:          bindings,
		identity:          identity,
		permissionService: permissionService,
		auditor:           auditor,
	}
}

// Candidates returns the acting-identity options the workspace admin
// may pick for a binding in this workspace: owned agents + allowlisted
// centralized service users (when the WI-87 master flag is on). The
// chokepoint still re-validates at create time — this endpoint is a UX
// shortcut, not the security boundary.
func (h *WorkspaceAgentBindingHandler) Candidates(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if !RequireWorkspacePermission(w, r, user.ID, workspaceID, models.PermissionWorkspaceAdmin, h.permissionService) {
		return
	}
	candidates, err := h.identity.ListCandidatesForBinding(r.Context(), user.ID, workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if candidates == nil {
		candidates = []services.CandidateActingIdentity{}
	}
	respondJSON(w, http.StatusOK, candidates)
}

type bindingResponse struct {
	ID              int      `json:"id"`
	WorkspaceID     int      `json:"workspace_id"`
	ActingUserID    int      `json:"acting_user_id"`
	ActingUserKind  string   `json:"acting_user_kind"`
	RepoSlug        string   `json:"repo_slug,omitempty"`
	RepoBaseRef     string   `json:"repo_base_ref,omitempty"`
	LLMConnectionID *int     `json:"llm_connection_id,omitempty"`
	SCMConnectionID *int     `json:"scm_connection_id,omitempty"`
	TokenScopes     []string `json:"token_scopes,omitempty"`
	TokenTTLMinutes int      `json:"token_ttl_minutes"`
	MaxRunsPerDay   int      `json:"max_runs_per_day"`
}

func toBindingResponse(b *models.WorkspaceAgentBinding) bindingResponse {
	return bindingResponse{
		ID:              b.ID,
		WorkspaceID:     b.WorkspaceID,
		ActingUserID:    b.ActingUserID,
		ActingUserKind:  b.ActingUserKind,
		RepoSlug:        b.RepoSlug,
		RepoBaseRef:     b.RepoBaseRef,
		LLMConnectionID: b.LLMConnectionID,
		SCMConnectionID: b.SCMConnectionID,
		TokenScopes:     b.TokenScopes,
		TokenTTLMinutes: b.TokenTTLMinutes,
		MaxRunsPerDay:   b.MaxRunsPerDay,
	}
}

type createBindingBody struct {
	ActingUserID    int      `json:"acting_user_id"`
	RepoSlug        string   `json:"repo_slug,omitempty"`
	RepoBaseRef     string   `json:"repo_base_ref,omitempty"`
	LLMConnectionID *int     `json:"llm_connection_id,omitempty"`
	SCMConnectionID *int     `json:"scm_connection_id,omitempty"`
	TokenScopes     []string `json:"token_scopes,omitempty"`
	TokenTTLMinutes int      `json:"token_ttl_minutes,omitempty"`
	MaxRunsPerDay   int      `json:"max_runs_per_day,omitempty"`
}

// List returns every binding configured in the workspace.
func (h *WorkspaceAgentBindingHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if !RequireWorkspacePermission(w, r, user.ID, workspaceID, models.PermissionWorkspaceAdmin, h.permissionService) {
		return
	}
	bindings, err := h.bindings.ListForWorkspace(r.Context(), workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	out := make([]bindingResponse, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, toBindingResponse(b))
	}
	respondJSON(w, http.StatusOK, out)
}

// Create persists a binding after validating the acting identity through
// the WI-87 chokepoint. Returns 409 Conflict when a binding already
// exists for (workspace, acting_user); the same surface (ApprError on a
// rejected identity) returns 403.
func (h *WorkspaceAgentBindingHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if !RequireWorkspacePermission(w, r, user.ID, workspaceID, models.PermissionWorkspaceAdmin, h.permissionService) {
		return
	}
	var body createBindingBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	if body.ActingUserID <= 0 {
		respondBadRequest(w, r, "acting_user_id is required")
		return
	}

	binding, err := h.bindings.Create(r.Context(), services.CreateBindingRequest{
		WorkspaceID:     workspaceID,
		ActingUserID:    body.ActingUserID,
		RepoSlug:        body.RepoSlug,
		RepoBaseRef:     body.RepoBaseRef,
		LLMConnectionID: body.LLMConnectionID,
		SCMConnectionID: body.SCMConnectionID,
		TokenScopes:     body.TokenScopes,
		TokenTTLMinutes: body.TokenTTLMinutes,
		MaxRunsPerDay:   body.MaxRunsPerDay,
		CreatedByUserID: user.ID,
	})
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrBindingDuplicate):
			respondConflict(w, r, err.Error())
		case errors.Is(err, services.ErrLLMConnectionRequired),
			errors.Is(err, services.ErrLLMConnectionInvalid):
			respondBadRequest(w, r, err.Error())
		case errors.Is(err, services.ErrBindingTokenTTLOverCap),
			errors.Is(err, services.ErrBindingRepoNeedsSCMConnection),
			errors.Is(err, services.ErrBindingInvalidRepoSlug):
			respondBadRequest(w, r, err.Error())
		case isAgentScopeError(err):
			respondBadRequest(w, r, err.Error())
		case isIdentityGateError(err):
			respondForbidden(w, r)
		default:
			respondInternalError(w, r, err)
		}
		return
	}
	h.auditor.LogWithDetails(r, user, "agent_binding.create", "workspace_agent_binding", &binding.ID, "", map[string]interface{}{
		"workspace_id":     workspaceID,
		"acting_user_id":   binding.ActingUserID,
		"acting_user_kind": binding.ActingUserKind,
	})
	respondJSON(w, http.StatusCreated, toBindingResponse(binding))
}

// Delete removes a binding by id. Returns 404 when the binding is absent.
func (h *WorkspaceAgentBindingHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if !RequireWorkspacePermission(w, r, user.ID, workspaceID, models.PermissionWorkspaceAdmin, h.permissionService) {
		return
	}
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		respondBadRequest(w, r, "id path param must be a positive integer")
		return
	}
	n, err := h.bindings.Delete(r.Context(), id, workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if n == 0 {
		respondNotFound(w, r, "agent binding")
		return
	}
	h.auditor.LogWithDetails(r, user, "agent_binding.delete", "workspace_agent_binding", &id, "", map[string]interface{}{
		"workspace_id": workspaceID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// testLLMRequest is the optional body for TestLLM. A blank/absent prompt
// falls back to the service default.
type testLLMRequest struct {
	Prompt string `json:"prompt,omitempty"`
}

// testLLMResponse carries the model's reply back to the admin.
type testLLMResponse struct {
	Prompt string `json:"prompt"`
	Answer string `json:"answer"`
}

// TestLLM round-trips a prompt through a binding's LLM connection and returns
// the model's reply, so a workspace admin can confirm the agent's model is
// reachable before assigning real work. Workspace-admin gated, like the other
// mutations. A provider/connection failure is surfaced as 502 so the admin
// sees the upstream message rather than an opaque 500.
func (h *WorkspaceAgentBindingHandler) TestLLM(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if !RequireWorkspacePermission(w, r, user.ID, workspaceID, models.PermissionWorkspaceAdmin, h.permissionService) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		respondBadRequest(w, r, "id path param must be a positive integer")
		return
	}
	body, ok := decodeOptionalJSON[testLLMRequest](w, r)
	if !ok {
		return
	}
	answer, err := h.bindings.TestLLM(r.Context(), id, workspaceID, body.Prompt)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrBindingNotFound):
			respondNotFound(w, r, "agent binding")
		case errors.Is(err, services.ErrLLMConnectionRequired):
			respondBadRequest(w, r, "this binding has no LLM connection — edit it to choose one")
		default:
			respondError(w, r, restapi.NewAPIError(http.StatusBadGateway, restapi.ErrCodeConnectionTestFailed,
				"LLM test failed: "+err.Error()))
		}
		return
	}
	prompt := body.Prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = services.DefaultLLMTestPrompt
	}
	respondJSONOK(w, testLLMResponse{Prompt: prompt, Answer: answer})
}

// isIdentityGateError reports whether the error came from the WI-87
// chokepoint. The handler maps all of them to 403 so a workspace admin
// cannot tell the difference between "user does not exist", "not your
// agent", and "centralized service users are gated" — the design plan
// calls this out specifically.
func isIdentityGateError(err error) bool {
	return errors.Is(err, services.ErrActingIdentityNotFound) ||
		errors.Is(err, services.ErrActingIdentityNotAgent) ||
		errors.Is(err, services.ErrActingIdentityInactive) ||
		errors.Is(err, services.ErrActingIdentityNotOwned) ||
		errors.Is(err, services.ErrActingIdentityCentralizedGated) ||
		errors.Is(err, services.ErrActingIdentityNotInAllowlist)
}

// isAgentScopeError reports whether the wrapped error came from
// auth.ValidateAgentScopes. The auth package returns a plain
// fmt.Errorf rather than a sentinel; matching by substring is ugly but
// localized and preferable to leaking the validation message via 500.
func isAgentScopeError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "scopes not permitted for coding-agent tokens")
}
