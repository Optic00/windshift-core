package handlers

import (
	"errors"
	"net/http"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/restapi/v1/middleware"
	"windshift/internal/services"
)

// AgentSkillHandler exposes the read-only agent-skills surface on the
// bearer-token v1 API (WI-258). This is what a coding-agent run's `ws skill
// ls` / `ws skill get` hit: the run prompt indexes the binding's attached
// skills and the agent fetches a body on demand (progressive disclosure).
// Authoring stays on the cookie-auth admin surface.
type AgentSkillHandler struct {
	BaseHandler
	runs *repository.AgentRunRepository
}

// NewAgentSkillHandler constructs a v1 AgentSkillHandler.
func NewAgentSkillHandler(db database.Database, permissionService *services.PermissionService) *AgentSkillHandler {
	return &AgentSkillHandler{
		BaseHandler: NewBaseHandler(db, permissionService),
		runs:        repository.NewAgentRunRepository(db),
	}
}

// runSkills resolves the immutable skill grants bound to the presented run
// token. Workspace permissions alone never disclose binding-confidential skills.
func (h *AgentSkillHandler) runSkills(w http.ResponseWriter, r *http.Request, workspaceID int) ([]models.SkillGrant, bool) {
	token := middleware.GetAPIToken(r.Context())
	if token == nil {
		h.RespondNotFound(w, r)
		return nil, false
	}
	_, runWorkspaceID, grants, status, err := h.runs.GetRunByTokenID(r.Context(), token.ID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return nil, false
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return nil, false
	}
	if runWorkspaceID != workspaceID || status != models.AgentRunStatusRunning || grants == nil {
		h.RespondNotFound(w, r)
		return nil, false
	}
	return grants.Skills, true
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
// List returns only the skills snapshotted into the presented run token.
//
// @Summary      List agent skills in a workspace
// @Tags         agents
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Workspace ID"
// @Success      200  {object}  handlers.agentSkillListResponse
// @Failure      400  {object}  handlers.ErrorResponse  "Invalid workspace ID"
// @Failure      401  {object}  handlers.ErrorResponse
// @Failure      404  {object}  handlers.ErrorResponse  "No active run snapshot for this token and workspace"
// @Failure      500  {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/agent-skills [get]
func (h *AgentSkillHandler) List(w http.ResponseWriter, r *http.Request) {
	_, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	wsID, ok := h.ParsePathID(w, r, "id", "workspace ID")
	if !ok {
		return
	}
	skills, ok := h.runSkills(w, r, wsID)
	if !ok {
		return
	}
	items := make([]agentSkillSummary, 0, len(skills))
	for _, s := range skills {
		items = append(items, agentSkillSummary{ID: s.ID, Name: s.Name, Description: s.Description, Enabled: true})
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
// @Failure      404      {object}  handlers.ErrorResponse  "Skill not present in this run snapshot"
// @Failure      422      {object}  handlers.ErrorResponse  "Saved activation exceeds the supported budget"
// @Failure      500      {object}  handlers.ErrorResponse
// @Router       /workspaces/{id}/agent-skills/{skillId} [get]
func (h *AgentSkillHandler) Get(w http.ResponseWriter, r *http.Request) {
	_, ok := h.RequireAuth(w, r)
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
	skills, ok := h.runSkills(w, r, wsID)
	if !ok {
		return
	}
	for _, skill := range skills {
		if skill.ID == skillID {
			if skill.Error != "" {
				h.RespondError(w, r, restapi.NewAPIError(
					http.StatusUnprocessableEntity,
					"SKILL_ACTIVATION_TOO_LARGE",
					"The saved skill exceeds the supported activation budget. Ask a workspace administrator to reduce and resave it.",
				))
				return
			}
			h.RespondOK(w, models.WorkspaceAgentSkill{
				ID: skill.ID, WorkspaceID: wsID, Name: skill.Name,
				Description: skill.Description, Body: skill.Body, Enabled: true,
			})
			return
		}
	}
	h.RespondNotFound(w, r)
}
