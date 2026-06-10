package handlers

import (
	"errors"
	"net/http"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

// AgentSkillHandler exposes the read-only agent-skills surface on the
// bearer-token v1 API (WI-258). This is what a coding-agent run's `ws skill
// ls` / `ws skill get` hit: the run prompt indexes the binding's attached
// skills and the agent fetches a body on demand (progressive disclosure).
// Authoring stays on the cookie-auth admin surface.
type AgentSkillHandler struct {
	BaseHandler
	repo *repository.WorkspaceAgentSkillRepository
}

// NewAgentSkillHandler constructs a v1 AgentSkillHandler.
func NewAgentSkillHandler(db database.Database, permissionService *services.PermissionService) *AgentSkillHandler {
	return &AgentSkillHandler{
		BaseHandler: NewBaseHandler(db, permissionService),
		repo:        repository.NewWorkspaceAgentSkillRepository(db),
	}
}

// checkAgentSkillPerm gates the skills surface on workspace item.view —
// any member (and any run token's acting user) that can see the
// workspace's items may read its curated skills. 404 on both not-found
// and no-permission, per the workspace-permission disclosure policy.
func (h *AgentSkillHandler) checkAgentSkillPerm(w http.ResponseWriter, r *http.Request, userID, workspaceID int) bool {
	has, err := h.PermissionService.HasWorkspacePermission(userID, workspaceID, models.PermissionItemView)
	if err != nil {
		h.RespondInternalError(w, r)
		return false
	}
	if !has {
		h.RespondNotFound(w, r)
		return false
	}
	return true
}

// agentSkillSummary is the list shape: no body — `ws skill ls` is the
// index, `ws skill get` is the disclosure.
type agentSkillSummary struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

type agentSkillListResponse struct {
	Items []agentSkillSummary `json:"items"`
}

// List handles GET /rest/api/v1/workspaces/{id}/agent-skills
//
// List returns the workspace's enabled agent skills, names + descriptions
// only.
//
// @Summary      List agent skills in a workspace
// @Tags         agents
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Workspace ID"
// @Success      200  {object}  handlers.agentSkillListResponse
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid workspace ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      404  {object}  handlers.ErrorResponse  "Workspace not found or caller lacks item.view"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/agent-skills [get]
func (h *AgentSkillHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	wsID, ok := h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return
	}
	if !h.checkAgentSkillPerm(w, r, user.ID, wsID) {
		return
	}
	skills, err := h.repo.ListForWorkspace(r.Context(), wsID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	items := make([]agentSkillSummary, 0, len(skills))
	for _, s := range skills {
		if !s.Enabled {
			continue
		}
		items = append(items, agentSkillSummary{ID: s.ID, Name: s.Name, Description: s.Description, Enabled: s.Enabled})
	}
	h.RespondOK(w, agentSkillListResponse{Items: items})
}

// Get handles GET /rest/api/v1/workspaces/{id}/agent-skills/{skillId}
//
// Get returns one skill including its markdown body.
//
// @Summary      Get an agent skill by ID
// @Tags         agents
// @Produce      json
// @Security     BearerAuth
// @Param        id       path      int  true  "Workspace ID"
// @Param        skillId  path      int  true  "Skill ID"
// @Success      200      {object}  models.WorkspaceAgentSkill
// @Failure      400      {object}  handlers.ErrorResponse  "Invalid workspace or skill ID"
// @Failure      401      {object}  handlers.ErrorResponse
// @Failure      404      {object}  handlers.ErrorResponse  "Skill not found in this workspace or caller lacks item.view"
// @Failure      500      {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/agent-skills/{skillId} [get]
func (h *AgentSkillHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	wsID, ok := h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return
	}
	skillID, ok := h.ParsePathID(w, r, "skillId", "skill ID")
	if !ok {
		return
	}
	if !h.checkAgentSkillPerm(w, r, user.ID, wsID) {
		return
	}
	skill, err := h.repo.Get(r.Context(), skillID, wsID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	// Disabled skills stay readable by id: a run whose prompt indexed the
	// skill before an admin toggled it off should not get a confusing 404
	// mid-run.
	h.RespondOK(w, skill)
}
