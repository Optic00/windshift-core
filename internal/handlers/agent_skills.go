package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/agentskills"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

// maxSkillBodyLen caps a skill's markdown body. Skills are loaded into the
// agent's context wholesale via `ws skill get`, so an unbounded body is a
// context-window footgun; 64 KiB comfortably fits any reasonable SKILL.md.
const maxSkillBodyLen = agentskills.MaxBodyBytes

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

// maxSkillPages caps how many workspace pages a skill may snapshot. The
// aggregate activation budget applies after all snapshots are rendered.
const maxSkillPages = 25

type skillBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Enabled     *bool  `json:"enabled,omitempty"`
	// PageIDs are snapshotted on every save. Full replace: the supplied set
	// becomes the skill's references.
	PageIDs []int `json:"page_ids"`
}

func (b *skillBody) sanitize() {
	sanitize.ApplyAll(
		sanitize.Pair{Target: &b.Name, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &b.Description, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &b.Body, Policy: sanitize.LongDocument},
	)
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
	case len(b.PageIDs) > maxSkillPages:
		return "a skill may reference at most 25 pages"
	}
	if err := agentskills.ValidateMetadata(b.Name, b.Description); err != nil {
		return err.Error()
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
	// The editor renders each skill's referenced-page chips, so include them.
	// Skill libraries are small (per-workspace, admin-curated), so the
	// per-skill lookup is fine.
	for _, s := range skills {
		refs, err := h.repo.PageRefsForSkill(r.Context(), s.ID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		s.Pages = refs
		s.Usage = activationUsage(s.Body, refs)
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
	if err := newJSONDecoder(w, r).Decode(&body); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	if err := agentskills.ValidateMetadata(body.Name, body.Description); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}
	body.sanitize()
	if msg := body.validate(); msg != "" {
		respondBadRequest(w, r, msg)
		return
	}
	pages, usage, ok := h.preparePageSnapshots(w, r, workspaceID, body.Body, body.PageIDs)
	if !ok {
		return
	}
	skill := &models.WorkspaceAgentSkill{
		WorkspaceID: workspaceID,
		Name:        strings.TrimSpace(body.Name),
		Description: strings.TrimSpace(body.Description),
		Body:        body.Body,
		Enabled:     body.Enabled == nil || *body.Enabled,
		Usage:       usage,
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
	if !h.setPageRefs(w, r, skill, pages) {
		return
	}
	h.auditor.LogWithDetails(r, user, "agent_skill.create", "workspace_agent_skill", &id, "", map[string]any{
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
	if err := newJSONDecoder(w, r).Decode(&body); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	if err := agentskills.ValidateMetadata(body.Name, body.Description); err != nil {
		respondBadRequest(w, r, err.Error())
		return
	}
	body.sanitize()
	if msg := body.validate(); msg != "" {
		respondBadRequest(w, r, msg)
		return
	}
	pages, usage, ok := h.preparePageSnapshots(w, r, workspaceID, body.Body, body.PageIDs)
	if !ok {
		return
	}
	skill := &models.WorkspaceAgentSkill{
		ID:          id,
		WorkspaceID: workspaceID,
		Name:        strings.TrimSpace(body.Name),
		Description: strings.TrimSpace(body.Description),
		Body:        body.Body,
		Enabled:     body.Enabled == nil || *body.Enabled,
		Usage:       usage,
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
	if !h.setPageRefs(w, r, skill, pages) {
		return
	}
	h.auditor.LogWithDetails(r, user, "agent_skill.update", "workspace_agent_skill", &id, "", map[string]any{
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
	h.auditor.LogWithDetails(r, user, "agent_skill.delete", "workspace_agent_skill", &id, "", map[string]any{
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
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "agent skill")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	refs, err := h.repo.PageRefsForSkill(r.Context(), skill.ID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	skill.Pages = refs
	skill.Usage = activationUsage(skill.Body, refs)
	respondJSON(w, http.StatusOK, skill)
}

// setPageRefs replaces the skill's referenced pages (full-replace) and
// populates skill.Pages for the response. A page id that is not a page in the
// skill's workspace is a client error (400); anything else is a 500. Returns
// false when it has already written an error response.
func (h *AgentSkillHandler) setPageRefs(w http.ResponseWriter, r *http.Request, skill *models.WorkspaceAgentSkill, pages []models.SkillPageReference) bool {
	if err := h.repo.ReplaceSkillPageSnapshots(r.Context(), skill.ID, pages); err != nil {
		respondInternalError(w, r, err)
		return false
	}
	refs, err := h.repo.PageRefsForSkill(r.Context(), skill.ID)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	skill.Pages = refs
	skill.Usage = activationUsage(skill.Body, refs)
	return true
}

func (h *AgentSkillHandler) preparePageSnapshots(w http.ResponseWriter, r *http.Request, workspaceID int, body string, pageIDs []int) ([]models.SkillPageReference, *models.SkillActivationUsage, bool) {
	pages, err := h.repo.ResolveSkillPageSnapshots(r.Context(), workspaceID, pageIDs)
	if errors.Is(err, repository.ErrSkillPageNotInWorkspace) {
		respondBadRequest(w, r, err.Error())
		return nil, nil, false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return nil, nil, false
	}
	_, usage, err := agentskills.RenderActivation(body, pages)
	if errors.Is(err, agentskills.ErrActivationTooLarge) {
		respondBadRequest(w, r, "skill body and referenced page snapshots must fit within 256 KiB and an estimated 64K tokens")
		return nil, nil, false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return nil, nil, false
	}
	return pages, usageModel(usage), true
}

func activationUsage(body string, pages []models.SkillPageReference) *models.SkillActivationUsage {
	_, usage, _ := agentskills.RenderActivation(body, pages)
	model := usageModel(usage)
	populatePageUsage(pages, model)
	return model
}

func usageModel(usage agentskills.Usage) *models.SkillActivationUsage {
	return &models.SkillActivationUsage{
		Bytes: usage.Bytes, EstimatedTokens: usage.EstimatedTokens,
		MaxBytes: usage.MaxBytes, MaxTokens: usage.MaxTokens,
	}
}

func populatePageUsage(pages []models.SkillPageReference, usage *models.SkillActivationUsage) {
	for i := range pages {
		bytes, runes, prefixBytes, prefixRunes := agentskills.PageSnapshotUsage(pages[i])
		pages[i].ActivationBytes = bytes
		pages[i].ActivationRunes = runes
		usage.PagePrefixBytes = prefixBytes
		usage.PagePrefixRunes = prefixRunes
	}
}
