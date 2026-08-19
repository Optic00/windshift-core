package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

// AgentRunHandler hosts the HTTP surface the UI uses to list, inspect,
// poll, and cancel coding-agent runs (WI-91 / WI-83).
//
// Event streaming is intentionally polling-style for this slice — the
// UI calls GET /agent-runs/{id}/events?after_id=N every few seconds and
// trims its local store. SSE would be a follow-up; the polling shape
// matches what the existing audit-log streaming endpoint does.
type AgentRunHandler struct {
	repo              *repository.AgentRunRepository
	runs              *services.RunService
	permissionService *services.PermissionService
	items             *repository.ItemRepository
	bindings          *services.BindingService
	usage             *repository.LLMUsageRepository // optional; nil → usage endpoint reports zeros
}

// NewAgentRunHandler constructs the handler. runs and bindings may be nil when
// the harness is disabled (CodingAgent.Enabled off); in that case the cancel
// and re-run endpoints return 503 instead of silently dropping the request.
// items resolves an item's workspace for the item-scoped runs list.
func NewAgentRunHandler(
	repo *repository.AgentRunRepository,
	runs *services.RunService,
	permissionService *services.PermissionService,
	items *repository.ItemRepository,
	bindings *services.BindingService,
) *AgentRunHandler {
	return &AgentRunHandler{repo: repo, runs: runs, permissionService: permissionService, items: items, bindings: bindings}
}

// SetUsageRepository attaches the LLM usage repository so the per-run usage
// endpoint can report token + cost totals. Optional and nil-safe.
func (h *AgentRunHandler) SetUsageRepository(usage *repository.LLMUsageRepository) {
	h.usage = usage
}

type agentRunResponse struct {
	ID             int        `json:"id"`
	WorkspaceID    int        `json:"workspace_id"`
	ItemID         *int       `json:"item_id,omitempty"`
	BindingID      *int       `json:"binding_id,omitempty"`
	JobKind        string     `json:"job_kind,omitempty"`
	ProfileVersion int        `json:"profile_version,omitempty"`
	Status         string     `json:"status"`
	QueuedAt       time.Time  `json:"queued_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	ContainerID    string     `json:"container_id,omitempty"`
	Error          string     `json:"error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func toAgentRunResponse(r *models.AgentRun) agentRunResponse {
	return agentRunResponse{
		ID: r.ID, WorkspaceID: r.WorkspaceID, ItemID: r.ItemID, BindingID: r.BindingID,
		JobKind: r.JobKind, ProfileVersion: r.ProfileVersion, Status: r.Status,
		QueuedAt: r.QueuedAt, StartedAt: r.StartedAt, EndedAt: r.EndedAt,
		ContainerID: r.ContainerID, Error: r.Error,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (h *AgentRunHandler) requireRunRead(w http.ResponseWriter, r *http.Request, userID int, run *models.AgentRun) bool {
	permission := models.PermissionItemView
	if run.IsEphemeral {
		permission = models.PermissionWorkspaceAdmin
	}
	return RequireWorkspacePermission(w, r, userID, run.WorkspaceID, permission, h.permissionService)
}

type agentRunEventResponse struct {
	ID          int       `json:"id"`
	RunID       int       `json:"run_id"`
	Timestamp   time.Time `json:"ts"`
	Type        string    `json:"type"`
	PayloadJSON string    `json:"payload_json"`
}

// List returns the workspace's most-recent agent runs. ?before_id=N for
// cursor pagination, ?limit=N (capped at 200, defaults to 50).
func (h *AgentRunHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if !RequireWorkspacePermission(w, r, user.ID, workspaceID, models.PermissionItemView, h.permissionService) {
		return
	}
	limit := parseQueryInt(r, "limit", 50)
	beforeID := parseQueryInt(r, "before_id", 0)
	runs, err := h.repo.ListForWorkspace(r.Context(), workspaceID, limit, beforeID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	out := make([]agentRunResponse, 0, len(runs))
	for _, r := range runs {
		out = append(out, toAgentRunResponse(r))
	}
	respondJSON(w, http.StatusOK, out)
}

// ListForItem returns the runs triggered against one work item, newest
// first — the item detail "Agent log" tab (WI-260). Gated on item.view via
// the item's workspace; 404 on both a missing item and a missing permission
// so item existence never leaks.
func (h *AgentRunHandler) ListForItem(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "itemId")
	if !ok {
		return
	}
	if !CheckItemPermission(w, r, h.items, h.permissionService, itemID, models.PermissionItemView) {
		return
	}
	limit := parseQueryInt(r, "limit", 50)
	beforeID := parseQueryInt(r, "before_id", 0)
	runs, err := h.repo.ListForItem(r.Context(), itemID, limit, beforeID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	out := make([]agentRunResponse, 0, len(runs))
	for _, run := range runs {
		out = append(out, toAgentRunResponse(run))
	}
	respondJSON(w, http.StatusOK, out)
}

// Rerun manually re-triggers the agent that last worked an item — the
// "Re-run" button on the item detail "Agent log" tab. Gated on item.edit via
// the item's workspace (404 on a missing item or permission, so existence
// never leaks). The new run inherits the last run's binding configuration; the
// authenticated user becomes its SCM principal.
//
// Runs do not start synchronously — they are enqueued. To keep a caller from
// stacking duplicates, the service no-ops when a run is already queued/running
// for the item (returned as 200 {"started": false}); the UI additionally
// disables the button while any run is in flight.
func (h *AgentRunHandler) Rerun(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "itemId")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	if !CheckItemPermission(w, r, h.items, h.permissionService, itemID, models.PermissionItemEdit) {
		return
	}
	if h.bindings == nil {
		respondServiceUnavailable(w, r, "coding-agent harness is disabled on this server")
		return
	}
	started, err := h.bindings.RerunForItem(r.Context(), itemID, user.ID)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrRerunUnavailable):
			respondServiceUnavailable(w, r, "coding-agent harness is disabled on this server")
		case errors.Is(err, services.ErrRerunNoPriorRun), errors.Is(err, services.ErrRerunNoBinding):
			respondConflict(w, r, "this item has no agent run to re-run")
		case errors.Is(err, services.ErrBindingBudgetExceeded):
			respondConflict(w, r, "the agent has hit its daily run budget for this item — try again later")
		default:
			respondInternalError(w, r, err)
		}
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"started": started})
}

// requireRunForRead resolves the id path param, loads the run, and enforces
// the read gate shared by the per-run endpoints.
func (h *AgentRunHandler) requireRunForRead(w http.ResponseWriter, r *http.Request) (*models.AgentRun, bool) {
	runID, ok := requireIDParam(w, r, "id")
	if !ok {
		return nil, false
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return nil, false
	}
	run, err := h.repo.Get(r.Context(), runID)
	if err != nil {
		respondNotFound(w, r, "agent run")
		return nil, false
	}
	if !h.requireRunRead(w, r, user.ID, run) {
		return nil, false
	}
	return run, true
}

// Get returns a single run. Workspace permission gates access by way of
// the run row's workspace_id.
func (h *AgentRunHandler) Get(w http.ResponseWriter, r *http.Request) {
	run, ok := h.requireRunForRead(w, r)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, toAgentRunResponse(run))
}

// Usage returns the run's metered LLM token + cost totals (WI-494).
// GET /agent-runs/{id}/usage. Same view-permission gate as Get.
func (h *AgentRunHandler) Usage(w http.ResponseWriter, r *http.Request) {
	run, ok := h.requireRunForRead(w, r)
	if !ok {
		return
	}
	if h.usage == nil {
		respondJSON(w, http.StatusOK, repository.RunUsageTotals{})
		return
	}
	totals, err := h.usage.TotalsForRun(r.Context(), run.ID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSON(w, http.StatusOK, totals)
}

// Events returns the run's event stream. ?after_id=N for "give me
// everything after id N"; the UI polls this every few seconds.
func (h *AgentRunHandler) Events(w http.ResponseWriter, r *http.Request) {
	run, ok := h.requireRunForRead(w, r)
	if !ok {
		return
	}
	afterID := parseQueryInt(r, "after_id", 0)
	limit := parseQueryInt(r, "limit", 200)
	events, err := h.repo.ListEventsAfter(r.Context(), run.ID, afterID, limit)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	out := make([]agentRunEventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, agentRunEventResponse{
			ID: e.ID, RunID: e.RunID, Timestamp: e.Timestamp, Type: e.Type, PayloadJSON: e.PayloadJSON,
		})
	}
	respondJSON(w, http.StatusOK, out)
}

// Cancel requests cancellation of an in-flight run. Returns 503 when the
// harness's RunService isn't wired (CodingAgent.Enabled off).
// Returns 200 even when the run is already terminal; cancellation is
// idempotent from the API's point of view.
func (h *AgentRunHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	runID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	run, err := h.repo.Get(r.Context(), runID)
	if err != nil {
		respondNotFound(w, r, "agent run")
		return
	}
	if !RequireWorkspacePermission(w, r, user.ID, run.WorkspaceID, models.PermissionWorkspaceAdmin, h.permissionService) {
		return
	}
	// Queued runs have no owner yet — neither the remote heartbeat flag nor
	// the local in-process registry knows them — so cancel them with a
	// queued→canceled CAS on the row itself (WI-341). ClaimQueuedForRunner
	// and the in-process consumer both CAS on status='queued', so whichever side wins
	// this race the run executes at most once: either it is terminal before
	// any claim, or a claim won first and we fall through to the claimed-run
	// paths below.
	if run.Status == models.AgentRunStatusQueued {
		transitioned, err := h.repo.CancelQueued(r.Context(), runID, time.Now().UTC())
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if transitioned {
			// Terminal lifecycle event, best-effort like the other emitters.
			_ = h.repo.AppendEvent(r.Context(), runID, "lifecycle",
				`{"phase":"canceled","reason":"canceled while queued"}`)
			respondJSON(w, http.StatusOK, map[string]any{"canceled": true})
			return
		}
		// Lost the race with a claim (or another terminal transition):
		// reload and dispatch on the run's current shape.
		run, err = h.repo.Get(r.Context(), runID)
		if err != nil {
			respondNotFound(w, r, "agent run")
			return
		}
	}
	// force (admin) terminates the row directly even when the cooperative cancel
	// can't reach the worker — the phantom-run escape hatch (WI-512).
	force := r.URL.Query().Get("force") == "true"
	now := time.Now().UTC()
	// Remote runs (claimed by a runner) cancel via a flag the owning runner
	// observes on its next heartbeat; independent of the local harness.
	if run.RunnerID != nil {
		if err := h.repo.RequestCancel(r.Context(), runID, now); err != nil {
			respondInternalError(w, r, err)
			return
		}
		forced := false
		if force {
			// A runner that lost its terminal report never observes the flag, so
			// force the row terminal instead of waiting for the 8h backstop.
			forced, err = h.repo.ForceCancelRunning(r.Context(), runID, now)
			if err != nil {
				respondInternalError(w, r, err)
				return
			}
			if forced {
				_ = h.repo.AppendEvent(r.Context(), runID, "lifecycle",
					`{"phase":"canceled","reason":"force-canceled by admin"}`)
			}
		}
		respondJSON(w, http.StatusOK, map[string]any{"canceled": true, "remote": true, "forced": forced})
		return
	}
	// Local in-process run: cancel via the RunService registry.
	if h.runs == nil {
		respondServiceUnavailable(w, r, "coding-agent harness is disabled on this server")
		return
	}
	canceled := h.runs.Cancel(runID)
	forced := false
	if force && !canceled {
		// Not in the in-process registry (e.g. orphaned by an orchestrator
		// restart) — force the row terminal directly.
		forced, err = h.repo.ForceCancelRunning(r.Context(), runID, now)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if forced {
			_ = h.repo.AppendEvent(r.Context(), runID, "lifecycle",
				`{"phase":"canceled","reason":"force-canceled by admin"}`)
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"canceled":     canceled || forced,
		"already_done": !canceled && !forced,
		"forced":       forced,
	})
}

func parseQueryInt(r *http.Request, key string, fallback int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return fallback
	}
	return v
}
