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
	"windshift/internal/sanitize"
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
	skills            *repository.WorkspaceAgentSkillRepository
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

// SetSkillsRepo wires the optional agent-skills repository (WI-258) so
// binding responses can include attached skill ids and the agent-config
// update endpoint can replace attachments.
func (h *WorkspaceAgentBindingHandler) SetSkillsRepo(repo *repository.WorkspaceAgentSkillRepository) {
	h.skills = repo
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
	TargetPoolID    *int     `json:"target_pool_id,omitempty"`
	TokenScopes     []string `json:"token_scopes,omitempty"`
	TokenTTLMinutes int      `json:"token_ttl_minutes"`
	MaxRunsPerDay   int      `json:"max_runs_per_day"`
	Instructions    string   `json:"instructions,omitempty"`
	SkillIDs        []int    `json:"skill_ids,omitempty"`
	// Repos is the binding's bound repositories (WI-449). The legacy scalar
	// RepoSlug/RepoBaseRef/SCMConnectionID above mirror the primary repo.
	Repos []bindingRepoResponse `json:"repos,omitempty"`
}

type bindingRepoResponse struct {
	RepoSlug        string `json:"repo_slug"`
	RepoBaseRef     string `json:"repo_base_ref,omitempty"`
	SCMConnectionID *int   `json:"scm_connection_id,omitempty"`
	IsPrimary       bool   `json:"is_primary"`
	Position        int    `json:"position"`
}

func toBindingResponse(b *models.WorkspaceAgentBinding) bindingResponse {
	resp := bindingResponse{
		ID:              b.ID,
		WorkspaceID:     b.WorkspaceID,
		ActingUserID:    b.ActingUserID,
		ActingUserKind:  b.ActingUserKind,
		RepoSlug:        b.RepoSlug,
		RepoBaseRef:     b.RepoBaseRef,
		LLMConnectionID: b.LLMConnectionID,
		SCMConnectionID: b.SCMConnectionID,
		TargetPoolID:    b.TargetPoolID,
		TokenScopes:     b.TokenScopes,
		TokenTTLMinutes: b.TokenTTLMinutes,
		MaxRunsPerDay:   b.MaxRunsPerDay,
		Instructions:    b.Instructions,
	}
	for _, rp := range b.Repos {
		resp.Repos = append(resp.Repos, bindingRepoResponse{
			RepoSlug:        rp.RepoSlug,
			RepoBaseRef:     rp.RepoBaseRef,
			SCMConnectionID: rp.SCMConnectionID,
			IsPrimary:       rp.IsPrimary,
			Position:        rp.Position,
		})
	}
	return resp
}

// withSkillIDs decorates a binding response with its attached skill ids.
// Best-effort: a skills lookup failure leaves the field empty rather than
// failing the listing.
func (h *WorkspaceAgentBindingHandler) withSkillIDs(r *http.Request, resp bindingResponse) bindingResponse {
	if h.skills == nil {
		return resp
	}
	ids, err := h.skills.SkillIDsForBinding(r.Context(), resp.ID)
	if err == nil {
		resp.SkillIDs = ids
	}
	return resp
}

type createBindingBody struct {
	ActingUserID int `json:"acting_user_id"`
	// Repos is the preferred multi-repo input (WI-449). When empty, the legacy
	// scalar repo_slug/repo_base_ref/scm_connection_id below are folded into a
	// single primary repo for old clients.
	Repos           []createBindingRepoBody `json:"repos,omitempty"`
	RepoSlug        string                  `json:"repo_slug,omitempty"`
	RepoBaseRef     string                  `json:"repo_base_ref,omitempty"`
	LLMConnectionID *int                    `json:"llm_connection_id,omitempty"`
	SCMConnectionID *int                    `json:"scm_connection_id,omitempty"`
	TargetPoolID    *int                    `json:"target_pool_id,omitempty"`
	TokenScopes     []string                `json:"token_scopes,omitempty"`
	TokenTTLMinutes int                     `json:"token_ttl_minutes,omitempty"`
	MaxRunsPerDay   int                     `json:"max_runs_per_day,omitempty"`
	Instructions    string                  `json:"instructions,omitempty"`
	SkillIDs        []int                   `json:"skill_ids,omitempty"`
}

type createBindingRepoBody struct {
	RepoSlug        string `json:"repo_slug"`
	RepoBaseRef     string `json:"repo_base_ref,omitempty"`
	SCMConnectionID *int   `json:"scm_connection_id,omitempty"`
	IsPrimary       bool   `json:"is_primary,omitempty"`
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
		out = append(out, h.withSkillIDs(r, toBindingResponse(b)))
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
	// RepoSlug/RepoBaseRef are identifier-shaped (owner/repo, git ref);
	// Instructions is free-form persona text rendered in the binding editor.
	sanitize.ApplyAll(
		sanitize.Pair{Target: &body.RepoSlug, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &body.RepoBaseRef, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &body.Instructions, Policy: sanitize.RichText},
	)
	repos := make([]services.RepoInput, 0, len(body.Repos))
	for i := range body.Repos {
		sanitize.ApplyAll(
			sanitize.Pair{Target: &body.Repos[i].RepoSlug, Policy: sanitize.ShortIdentifier},
			sanitize.Pair{Target: &body.Repos[i].RepoBaseRef, Policy: sanitize.ShortIdentifier},
		)
		repos = append(repos, services.RepoInput{
			RepoSlug:        body.Repos[i].RepoSlug,
			RepoBaseRef:     body.Repos[i].RepoBaseRef,
			SCMConnectionID: body.Repos[i].SCMConnectionID,
			IsPrimary:       body.Repos[i].IsPrimary,
		})
	}

	binding, err := h.bindings.Create(r.Context(), services.CreateBindingRequest{
		WorkspaceID:     workspaceID,
		ActingUserID:    body.ActingUserID,
		Repos:           repos,
		RepoSlug:        body.RepoSlug,
		RepoBaseRef:     body.RepoBaseRef,
		LLMConnectionID: body.LLMConnectionID,
		SCMConnectionID: body.SCMConnectionID,
		TargetPoolID:    body.TargetPoolID,
		TokenScopes:     body.TokenScopes,
		TokenTTLMinutes: body.TokenTTLMinutes,
		MaxRunsPerDay:   body.MaxRunsPerDay,
		Instructions:    body.Instructions,
		SkillIDs:        body.SkillIDs,
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
			errors.Is(err, services.ErrBindingInvalidRepoSlug),
			errors.Is(err, services.ErrBindingDuplicateRepoSlug),
			errors.Is(err, services.ErrBindingPrimaryRepoRequired),
			errors.Is(err, services.ErrBindingTooManyRepos),
			errors.Is(err, services.ErrBindingInvalidPool),
			errors.Is(err, services.ErrBindingInstructionsTooLong),
			isSkillAttachError(err):
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
	respondJSON(w, http.StatusCreated, h.withSkillIDs(r, toBindingResponse(binding)))
}

// isSkillAttachError reports whether the error came from skill-id
// validation during binding create/update (bad or foreign ids → 400).
func isSkillAttachError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "skill")
}

type updateAgentConfigBody struct {
	Instructions string `json:"instructions"`
	SkillIDs     []int  `json:"skill_ids"`
}

// UpdateAgentConfig rewrites the binding's prompt-shaping configuration —
// custom instructions + skill attachments (WI-258). Bindings stay
// create/delete-only for everything else; this narrow update lets admins
// iterate on personas without recreating the binding.
func (h *WorkspaceAgentBindingHandler) UpdateAgentConfig(w http.ResponseWriter, r *http.Request) {
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
	var body updateAgentConfigBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	sanitize.Apply(&body.Instructions, sanitize.RichText)
	if err := h.bindings.UpdateAgentConfig(r.Context(), workspaceID, id, body.Instructions, body.SkillIDs); err != nil {
		switch {
		case errors.Is(err, services.ErrBindingNotFound):
			respondNotFound(w, r, "agent binding")
		case errors.Is(err, services.ErrBindingInstructionsTooLong), isSkillAttachError(err):
			respondBadRequest(w, r, err.Error())
		default:
			respondInternalError(w, r, err)
		}
		return
	}
	h.auditor.LogWithDetails(r, user, "agent_binding.update_config", "workspace_agent_binding", &id, "", map[string]interface{}{
		"workspace_id": workspaceID,
		"skill_count":  len(body.SkillIDs),
	})
	respondJSON(w, http.StatusOK, map[string]any{"updated": true})
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

// testLLMResponse carries the model's reply back to the admin. It proves only
// that the binding's LLM connection is reachable; the full chain (repo checked
// out, agent can read its files) is exercised by the heavier TestRun.
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
	// Prompt is echoed back verbatim in the response.
	sanitize.Apply(&body.Prompt, sanitize.RichText)
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

// testRunResponse returns the id of the provisioned test run so the UI can
// watch it via the agent-runs events endpoints.
type testRunResponse struct {
	RunID int `json:"run_id"`
}

// TestRun provisions a real, ephemeral coding-agent container run for the
// binding (no work item, read-only prompt) so a workspace admin can confirm the
// full chain end-to-end: the model is reachable, the repo clones into a
// worktree, and the agent can read its files. Workspace-admin gated. The run
// executes asynchronously; the response carries its id for event polling.
//
// 404 when the binding is absent, 400 when it has no repo configured, and 409
// when the coding-agent runner isn't configured on this server or the binding
// targets a remote runner pool (test runs are local-runtime only).
func (h *WorkspaceAgentBindingHandler) TestRun(w http.ResponseWriter, r *http.Request) {
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
	runID, err := h.bindings.StartTestRun(r.Context(), id, workspaceID, user.ID)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrBindingNotFound):
			respondNotFound(w, r, "agent binding")
		case errors.Is(err, services.ErrBindingNoRepo):
			respondBadRequest(w, r, "this binding has no repo configured — a test run needs a repo to check out")
		case errors.Is(err, services.ErrBindingRunnerNotConfigured):
			respondConflict(w, r, "the coding-agent runner is not configured on this server")
		case errors.Is(err, services.ErrBindingTestRunRemotePool):
			respondConflict(w, r, "test runs execute on this server's local runtime and are not supported for bindings that target a remote runner pool — assign a real work item to verify the pool instead")
		case errors.Is(err, services.ErrTriggerUserSCMNotConnected):
			respondConflict(w, r, "you have no connected SCM account for this binding's OAuth connection — connect your GitHub/Gitea account under profile settings first")
		default:
			respondError(w, r, restapi.NewAPIError(http.StatusBadGateway, restapi.ErrCodeConnectionTestFailed,
				"failed to start test run: "+err.Error()))
		}
		return
	}
	h.auditor.LogWithDetails(r, user, "agent_binding.test_run", "workspace_agent_binding", &id, "", map[string]interface{}{
		"workspace_id": workspaceID,
		"run_id":       runID,
	})
	respondJSONOK(w, testRunResponse{RunID: runID})
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
