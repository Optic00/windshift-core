package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

// maxSkillBodyLen caps a skill's markdown body. Skills are loaded into the
// agent's context wholesale via `ws skill get`, so an unbounded body is a
// context-window footgun; 64 KiB comfortably fits any reasonable SKILL.md.
const maxSkillBodyLen = 64 * 1024

// AgentSkillHandler exposes the workspace-admin CRUD for the agent-skills
// library (WI-258). Skills are markdown knowledge packs attachable to agent
// bindings; the run prompt indexes them and the agent fetches bodies through
// the bearer-token surface (`ws skill get`).
type AgentSkillHandler struct {
	repo              *repository.WorkspaceAgentSkillRepository
	permissionService *services.PermissionService
	auditor           *logger.Auditor
}

// NewAgentSkillHandler constructs the handler.
func NewAgentSkillHandler(repo *repository.WorkspaceAgentSkillRepository, permissionService *services.PermissionService, auditor *logger.Auditor) *AgentSkillHandler {
	return &AgentSkillHandler{repo: repo, permissionService: permissionService, auditor: auditor}
}

type skillBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

func (b skillBody) validate() string {
	name := strings.TrimSpace(b.Name)
	switch {
	case name == "":
		return "name is required"
	case len(name) > 120:
		return "name must be at most 120 characters"
	case len(b.Description) > 500:
		return "description must be at most 500 characters (it is the prompt-index trigger, not the content)"
	case len(b.Body) > maxSkillBodyLen:
		return "body must be at most 64 KiB"
	}
	return ""
}

func (h *AgentSkillHandler) requireAdmin(w http.ResponseWriter, r *http.Request) (workspaceID int, user *models.User, ok bool) {
	workspaceID, ok = requireIDParam(w, r, "workspaceId")
	if !ok {
		return 0, nil, false
	}
	user, ok = RequireAuth(w, r)
	if !ok {
		return 0, nil, false
	}
	if !RequireWorkspacePermission(w, r, user.ID, workspaceID, models.PermissionWorkspaceAdmin, h.permissionService) {
		return 0, nil, false
	}
	return workspaceID, user, true
}

// List returns the workspace's skill library (bodies included — the admin
// UI edits them in place).
func (h *AgentSkillHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID, _, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	skills, err := h.repo.ListForWorkspace(r.Context(), workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if skills == nil {
		skills = []*models.WorkspaceAgentSkill{}
	}
	respondJSON(w, http.StatusOK, skills)
}

// Create adds a skill to the workspace library.
func (h *AgentSkillHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var body skillBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	if msg := body.validate(); msg != "" {
		respondBadRequest(w, r, msg)
		return
	}
	skill := &models.WorkspaceAgentSkill{
		WorkspaceID: workspaceID,
		Name:        strings.TrimSpace(body.Name),
		Description: strings.TrimSpace(body.Description),
		Body:        body.Body,
		Enabled:     body.Enabled == nil || *body.Enabled,
	}
	uid := user.ID
	skill.CreatedByUserID = &uid
	id, err := h.repo.Insert(r.Context(), skill)
	if err != nil {
		if errors.Is(err, repository.ErrSkillDuplicateName) {
			respondConflict(w, r, err.Error())
			return
		}
		respondInternalError(w, r, err)
		return
	}
	skill.ID = id
	h.auditor.LogWithDetails(r, user, "agent_skill.create", "workspace_agent_skill", &id, "", map[string]interface{}{
		"workspace_id": workspaceID,
		"name":         skill.Name,
	})
	respondJSON(w, http.StatusCreated, skill)
}

// Update rewrites a skill's fields.
func (h *AgentSkillHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		respondBadRequest(w, r, "id path param must be a positive integer")
		return
	}
	var body skillBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	if msg := body.validate(); msg != "" {
		respondBadRequest(w, r, msg)
		return
	}
	skill := &models.WorkspaceAgentSkill{
		ID:          id,
		WorkspaceID: workspaceID,
		Name:        strings.TrimSpace(body.Name),
		Description: strings.TrimSpace(body.Description),
		Body:        body.Body,
		Enabled:     body.Enabled == nil || *body.Enabled,
	}
	n, err := h.repo.Update(r.Context(), skill)
	if err != nil {
		if errors.Is(err, repository.ErrSkillDuplicateName) {
			respondConflict(w, r, err.Error())
			return
		}
		respondInternalError(w, r, err)
		return
	}
	if n == 0 {
		respondNotFound(w, r, "agent skill")
		return
	}
	h.auditor.LogWithDetails(r, user, "agent_skill.update", "workspace_agent_skill", &id, "", map[string]interface{}{
		"workspace_id": workspaceID,
		"name":         skill.Name,
	})
	respondJSON(w, http.StatusOK, skill)
}

// Delete removes a skill; binding attachments cascade away.
func (h *AgentSkillHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		respondBadRequest(w, r, "id path param must be a positive integer")
		return
	}
	n, err := h.repo.Delete(r.Context(), id, workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if n == 0 {
		respondNotFound(w, r, "agent skill")
		return
	}
	h.auditor.LogWithDetails(r, user, "agent_skill.delete", "workspace_agent_skill", &id, "", map[string]interface{}{
		"workspace_id": workspaceID,
	})
	respondJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// Get returns a single skill (admin surface; the agent-facing read lives on
// the bearer-token REST v1 surface).
func (h *AgentSkillHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID, _, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		respondBadRequest(w, r, "id path param must be a positive integer")
		return
	}
	skill, err := h.repo.Get(r.Context(), id, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		respondNotFound(w, r, "agent skill")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSON(w, http.StatusOK, skill)
}
