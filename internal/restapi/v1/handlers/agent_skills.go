package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	repo  *repository.WorkspaceAgentSkillRepository
	pages *repository.PageRepository
}

// NewAgentSkillHandler constructs a v1 AgentSkillHandler.
func NewAgentSkillHandler(db database.Database, permissionService *services.PermissionService) *AgentSkillHandler {
	return &AgentSkillHandler{
		BaseHandler: NewBaseHandler(db, permissionService),
		repo:        repository.NewWorkspaceAgentSkillRepository(db),
		pages:       repository.NewPageRepository(db),
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
	// Inline referenced workspace pages (WI-517): a skill can be built out of
	// living workspace docs. Rather than make the agent chase a second fetch,
	// the referenced pages' current markdown is appended to the body it
	// receives here — the moment of disclosure. The curator's act of
	// referencing a page is the authorization (equivalent to pasting its text
	// into the body), so per-page ACLs are not re-checked; we do re-scope to
	// the skill's workspace defensively.
	if err := h.inlineReferencedPages(r, skill, wsID); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	// Disabled skills stay readable by id: a run whose prompt indexed the
	// skill before an admin toggled it off should not get a confusing 404
	// mid-run.
	h.RespondOK(w, skill)
}

// inlineReferencedPages appends the markdown of each page referenced by the
// skill (WI-517) to skill.Body under a "Referenced pages" section, in the
// skill's page order, and records the refs on skill.Pages. A reference to a
// page outside wsID (which the write path forbids) is skipped defensively.
func (h *AgentSkillHandler) inlineReferencedPages(r *http.Request, skill *models.WorkspaceAgentSkill, wsID int) error {
	refs, err := h.repo.PageRefsForSkill(r.Context(), skill.ID)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	ids := make([]int, len(refs))
	for i, ref := range refs {
		ids[i] = ref.ID
	}
	pages, err := h.pages.GetByIDs(ids)
	if err != nil {
		return err
	}
	byID := make(map[int]models.Page, len(pages))
	for _, p := range pages {
		if p.WorkspaceID == wsID {
			byID[p.ID] = p
		}
	}
	var b strings.Builder
	b.WriteString(skill.Body)
	b.WriteString("\n\n---\n\n## Referenced pages\n\nWorkspace pages attached to this skill; their current content is inlined below.\n")
	for _, ref := range refs {
		p, ok := byID[ref.ID]
		if !ok {
			continue
		}
		title := p.Title
		if strings.TrimSpace(title) == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", title, p.Content)
	}
	skill.Body = b.String()
	skill.Pages = refs
	return nil
}
