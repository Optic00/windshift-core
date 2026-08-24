package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"windshift/internal/database"
	"windshift/internal/integrations/todoist"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/sso"

	"uuid"
)

// TodoistSyncHandler exposes the per-user Todoist personal-task sync settings
// and a manual "Sync now" trigger. It reuses the integration OAuth connection
// (encrypted token in user_integration_tokens) established by the connect flow.
type TodoistSyncHandler struct {
	encryption    *sso.SecretEncryption
	syncRepo      *repository.TodoistSyncRepository
	workspaceRepo *repository.WorkspaceRepository
	syncService   *services.TodoistSyncService
}

// NewTodoistSyncHandler constructs a TodoistSyncHandler.
func NewTodoistSyncHandler(db database.Database, encryption *sso.SecretEncryption) *TodoistSyncHandler {
	return &TodoistSyncHandler{
		encryption:    encryption,
		syncRepo:      repository.NewTodoistSyncRepository(db),
		workspaceRepo: repository.NewWorkspaceRepository(db),
		syncService:   services.NewTodoistSyncService(db, encryption),
	}
}

// todoistSyncStatus is the GET response: enough for the settings UI to render
// the connect prompt, the scope picker, and the last-sync state.
type todoistSyncStatus struct {
	Configured       bool       `json:"configured"` // an enabled Todoist provider exists
	Connected        bool       `json:"connected"`  // this user has a Todoist token
	Enabled          bool       `json:"enabled"`
	ScopeMode        string     `json:"scope_mode"`
	TodoistProjectID string     `json:"todoist_project_id"`
	LastSyncedAt     *time.Time `json:"last_synced_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}

// GetSync returns the user's Todoist sync configuration and connection state.
func (h *TodoistSyncHandler) GetSync(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	userID := fmt.Sprintf("%d", user.ID)

	providerID, err := h.todoistProviderID()
	if errors.Is(err, repository.ErrNotFound) {
		respondJSONOK(w, todoistSyncStatus{Configured: false, ScopeMode: string(models.TodoistScopeAll)})
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	status := todoistSyncStatus{Configured: true, ScopeMode: string(models.TodoistScopeAll)}
	status.Connected = h.userConnected(userID, providerID)

	cfg, err := h.syncRepo.GetConfig(userID, providerID)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		// No config yet: defaults (disabled, scope all).
	case err != nil:
		respondInternalError(w, r, err)
		return
	default:
		status.Enabled = cfg.Enabled
		status.ScopeMode = string(cfg.ScopeMode)
		status.TodoistProjectID = cfg.TodoistProjectID
		status.LastSyncedAt = cfg.LastSyncedAt
		status.LastError = cfg.LastError
	}
	respondJSONOK(w, status)
}

type todoistSyncUpdateRequest struct {
	Enabled          bool   `json:"enabled"`
	ScopeMode        string `json:"scope_mode"`
	TodoistProjectID string `json:"todoist_project_id"`
}

// UpdateSync creates or updates the user's sync configuration.
func (h *TodoistSyncHandler) UpdateSync(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	userID := fmt.Sprintf("%d", user.ID)

	req, ok := decodeJSON[todoistSyncUpdateRequest](w, r)
	if !ok {
		return
	}

	scope := models.TodoistSyncScopeMode(req.ScopeMode)
	if scope != models.TodoistScopeAll && scope != models.TodoistScopeProject {
		respondBadRequest(w, r, "scope_mode must be 'all' or 'project'")
		return
	}
	if scope == models.TodoistScopeProject && req.TodoistProjectID == "" {
		respondBadRequest(w, r, "todoist_project_id is required when scope_mode is 'project'")
		return
	}

	providerID, err := h.todoistProviderID()
	if errors.Is(err, repository.ErrNotFound) {
		respondBadRequest(w, r, "Todoist is not configured on this server")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Enabling sync requires a connected account and a personal workspace to
	// sync into.
	if req.Enabled && !h.userConnected(userID, providerID) {
		respondBadRequest(w, r, "Connect your Todoist account before enabling sync")
		return
	}

	workspaceID, err := h.workspaceRepo.GetActivePersonalWorkspaceID(user.ID)
	if errors.Is(err, repository.ErrNotFound) {
		respondBadRequest(w, r, "Open your personal task list once before enabling Todoist sync")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	cfgID := uuid.New().String()
	if existing, gErr := h.syncRepo.GetConfig(userID, providerID); gErr == nil {
		cfgID = existing.ID
	}

	if err := h.syncRepo.UpsertConfig(models.TodoistSyncConfig{
		ID:                    cfgID,
		UserID:                userID,
		IntegrationProviderID: providerID,
		PersonalWorkspaceID:   workspaceID,
		Enabled:               req.Enabled,
		ScopeMode:             scope,
		TodoistProjectID:      req.TodoistProjectID,
	}); err != nil {
		respondInternalError(w, r, err)
		return
	}

	h.GetSync(w, r)
}

// GetProjects lists the connected user's Todoist projects for the scope picker.
func (h *TodoistSyncHandler) GetProjects(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	userID := fmt.Sprintf("%d", user.ID)

	providerID, err := h.todoistProviderID()
	if errors.Is(err, repository.ErrNotFound) {
		respondBadRequest(w, r, "Todoist is not configured on this server")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	token, ok := h.decryptedToken(w, r, userID, providerID)
	if !ok {
		return
	}

	projects, err := todoist.NewClient(token).ListProjects()
	if err != nil {
		respondServiceUnavailable(w, r, "Could not reach Todoist")
		return
	}

	type projectView struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		IsInbox bool   `json:"is_inbox"`
	}
	out := make([]projectView, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectView{ID: p.ID, Name: p.Name, IsInbox: p.IsInbox})
	}
	respondJSONOK(w, out)
}

// RunSync triggers an immediate sync for the user and returns what changed.
func (h *TodoistSyncHandler) RunSync(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	userID := fmt.Sprintf("%d", user.ID)

	providerID, err := h.todoistProviderID()
	if errors.Is(err, repository.ErrNotFound) {
		respondBadRequest(w, r, "Todoist is not configured on this server")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	cfg, err := h.syncRepo.GetConfig(userID, providerID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && !cfg.Enabled) {
		respondBadRequest(w, r, "Todoist sync is not enabled")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	stats, syncErr := h.syncService.SyncConfig(*cfg)
	if errors.Is(syncErr, services.ErrTodoistSyncAlreadyRunning) {
		respondConflict(w, r, "A Todoist sync is already running for your account")
		return
	}
	resp := map[string]any{
		"ok":            syncErr == nil,
		"created_in_ws": stats.CreatedInWS,
		"updated_in_ws": stats.UpdatedInWS,
		"deleted_in_ws": stats.DeletedInWS,
		"created_in_td": stats.CreatedInTD,
		"updated_in_td": stats.UpdatedInTD,
		"deleted_in_td": stats.DeletedInTD,
	}
	if syncErr != nil {
		resp["error"] = syncErr.Error()
	}
	respondJSONOK(w, resp)
}

// todoistProviderID resolves the single enabled Todoist provider, or ErrNotFound.
func (h *TodoistSyncHandler) todoistProviderID() (string, error) {
	return h.syncRepo.GetEnabledTodoistProviderID()
}

func (h *TodoistSyncHandler) userConnected(userID, providerID string) bool {
	_, err := h.syncRepo.GetEncryptedToken(userID, providerID)
	return err == nil
}

// decryptedToken loads and decrypts the user's Todoist token, writing the
// appropriate error response and returning ok=false on failure.
func (h *TodoistSyncHandler) decryptedToken(w http.ResponseWriter, r *http.Request, userID, providerID string) (string, bool) {
	enc, err := h.syncRepo.GetEncryptedToken(userID, providerID)
	if errors.Is(err, repository.ErrNotFound) {
		respondBadRequest(w, r, "Connect your Todoist account first")
		return "", false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return "", false
	}
	token, err := h.encryption.Decrypt(enc)
	if err != nil {
		respondInternalError(w, r, err)
		return "", false
	}
	return token, true
}
