package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

// RunnerControlHandler is the remote-runner control plane (Initiative
// WI-141): the HTTP surface a self-registered runner uses to register,
// claim work, stream events, report results, and heartbeat. It is the
// server-side counterpart of services.HTTPOrchestratorClient.
//
// All endpoints except Register authenticate with the per-instance runner
// credential (Bearer), resolved via RunnerRegistryService. A runner may only
// emit/report against a run it actually claimed (runner_id ownership check).
type RunnerControlHandler struct {
	registry *services.RunnerRegistryService
	runs     *repository.AgentRunRepository
	now      func() time.Time
}

// NewRunnerControlHandler constructs the handler. registry/runs may be nil
// when the coding-agent harness is disabled, in which case endpoints return
// 503 rather than panicking.
func NewRunnerControlHandler(registry *services.RunnerRegistryService, runs *repository.AgentRunRepository, now func() time.Time) *RunnerControlHandler {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RunnerControlHandler{registry: registry, runs: runs, now: now}
}

// Register exchanges a pool registration token for a per-instance runner
// credential. Unauthenticated: the registration token in the body is the
// credential. POST /runner/register.
func (h *RunnerControlHandler) Register(w http.ResponseWriter, r *http.Request) {
	if h.registry == nil {
		respondServiceUnavailable(w, r, "coding-agent harness is disabled on this server")
		return
	}
	var req services.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	cred, inst, err := h.registry.Register(r.Context(), req.RegistrationToken, req.Name)
	if err != nil {
		// Invalid/expired token is the only expected error; surface it as
		// 401 without distinguishing unknown vs revoked vs expired.
		respondUnauthorized(w, r)
		return
	}
	respondJSONCreated(w, services.RegisterResponse{
		Credential: cred,
		InstanceID: inst.ID,
		PoolID:     inst.PoolCapabilityID,
	})
}

// Claim atomically claims the next queued run for the runner's pool.
// POST /runner/claim. Responds {"job": null} (200) when no work is available.
func (h *RunnerControlHandler) Claim(w http.ResponseWriter, r *http.Request) {
	inst, ok := h.requireRunner(w, r)
	if !ok {
		return
	}
	run, err := h.runs.ClaimQueued(r.Context(), inst.PoolCapabilityID, inst.ID, h.now())
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if run == nil {
		respondJSONOK(w, services.ClaimResponse{Job: nil})
		return
	}
	// Env / worktree are enriched by the access layer (WI-144); for now the
	// runner receives the run id and prepares its own workspace.
	respondJSONOK(w, services.ClaimResponse{Job: &services.JobSpec{RunID: run.ID}})
}

// Events appends one event to a run the caller owns.
// POST /runner/runs/{id}/events.
func (h *RunnerControlHandler) Events(w http.ResponseWriter, r *http.Request) {
	inst, ok := h.requireRunner(w, r)
	if !ok {
		return
	}
	runID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if !h.ownsRun(w, r, inst, runID) {
		return
	}
	var req services.EmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	payload := req.PayloadJSON
	if payload == "" {
		payload = "{}"
	}
	if err := h.runs.AppendEvent(r.Context(), runID, req.Type, payload); err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, map[string]any{"ok": true})
}

// Result records a run's terminal verdict. POST /runner/runs/{id}/result.
// Worktree cleanup happens runner-side; the post-run hook (PR creation) for
// remote runs is wired with the access layer (WI-144/WI-161).
func (h *RunnerControlHandler) Result(w http.ResponseWriter, r *http.Request) {
	inst, ok := h.requireRunner(w, r)
	if !ok {
		return
	}
	runID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	if !h.ownsRun(w, r, inst, runID) {
		return
	}
	var req services.ReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	status := req.Status
	if !models.IsAgentRunTerminal(status) {
		respondBadRequest(w, r, "status must be a terminal agent-run state")
		return
	}
	if req.ContainerID != "" {
		if err := h.runs.SetContainerID(r.Context(), runID, req.ContainerID); err != nil {
			respondInternalError(w, r, err)
			return
		}
	}
	if err := h.runs.Finalize(r.Context(), runID, status, services.RedactString(req.Error), h.now()); err != nil {
		respondInternalError(w, r, err)
		return
	}
	_ = h.runs.AppendEvent(r.Context(), runID, "lifecycle", `{"phase":"`+status+`"}`)
	respondJSONOK(w, map[string]any{"ok": true})
}

// Heartbeat renews the runner's lease and returns control signals: the run
// ids the runner should abort (orchestrator-requested cancellations) and the
// runner pool's current queue depth (the autoscaling signal).
// POST /runner/heartbeat.
func (h *RunnerControlHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	inst, ok := h.requireRunner(w, r)
	if !ok {
		return
	}
	if err := h.registry.Heartbeat(r.Context(), inst.ID); err != nil {
		respondInternalError(w, r, err)
		return
	}
	abort, err := h.runs.ListAbortableRuns(r.Context(), inst.ID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	depth, err := h.runs.CountQueuedForPool(r.Context(), inst.PoolCapabilityID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, services.HeartbeatResponse{Abort: abort, QueueDepth: depth})
}

// requireRunner authenticates the per-instance runner credential. Writes a
// 401/503 and returns ok=false on failure.
func (h *RunnerControlHandler) requireRunner(w http.ResponseWriter, r *http.Request) (*models.RunnerInstance, bool) {
	if h.registry == nil || h.runs == nil {
		respondServiceUnavailable(w, r, "coding-agent harness is disabled on this server")
		return nil, false
	}
	cred := bearerCredential(r)
	if cred == "" {
		respondUnauthorized(w, r)
		return nil, false
	}
	inst, err := h.registry.Authenticate(r.Context(), cred)
	if err != nil {
		respondUnauthorized(w, r)
		return nil, false
	}
	return inst, true
}

// ownsRun enforces that the run exists and was claimed by this runner.
// Returns false (after writing a response) otherwise.
func (h *RunnerControlHandler) ownsRun(w http.ResponseWriter, r *http.Request, inst *models.RunnerInstance, runID int) bool {
	run, err := h.runs.Get(r.Context(), runID)
	if err != nil {
		respondNotFound(w, r, "agent run")
		return false
	}
	if run.RunnerID == nil || *run.RunnerID != inst.ID {
		// The run was not claimed by this runner; treat as forbidden.
		respondForbidden(w, r)
		return false
	}
	return true
}

// bearerCredential extracts a Bearer token from the Authorization header.
func bearerCredential(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
