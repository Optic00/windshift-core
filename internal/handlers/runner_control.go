package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
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
const runnerControlMaxBodyBytes = 1 << 20

const (
	// Event delivery is synchronous but can burst while an agent streams output.
	// Keep the steady limit generous and isolate it per registered runner.
	runnerControlRequestsPerSecond = 20
	runnerControlBurst             = 100
	runnerLimiterEntryTTL          = 10 * time.Minute
)

type runnerRateLimitEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type runnerInstanceRateLimiter struct {
	mu        sync.Mutex
	entries   map[int]*runnerRateLimitEntry
	rate      rate.Limit
	burst     int
	now       func() time.Time
	lastSweep time.Time
}

func newRunnerInstanceRateLimiter(r rate.Limit, burst int) *runnerInstanceRateLimiter {
	now := time.Now
	return &runnerInstanceRateLimiter{
		entries: make(map[int]*runnerRateLimitEntry),
		rate:    r,
		burst:   burst,
		now:     now,
	}
}

func (l *runnerInstanceRateLimiter) Allow(instanceID int) bool {
	if l == nil || l.burst <= 0 {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= runnerLimiterEntryTTL {
		for id, entry := range l.entries {
			if now.Sub(entry.lastSeen) > runnerLimiterEntryTTL {
				delete(l.entries, id)
			}
		}
		l.lastSweep = now
	}
	entry := l.entries[instanceID]
	if entry == nil {
		entry = &runnerRateLimitEntry{limiter: rate.NewLimiter(l.rate, l.burst)}
		l.entries[instanceID] = entry
	}
	entry.lastSeen = now
	return entry.limiter.Allow()
}

type RunnerControlHandler struct {
	registry *services.RunnerRegistryService
	runs     *repository.AgentRunRepository
	runSvc   *services.RunService
	caps     *repository.ActionRepository
	limiter  *runnerInstanceRateLimiter
	now      func() time.Time
	// baseURL is the resolved public base URL (without /api); used to render
	// the copy-paste install command returned alongside a minted registration
	// token (WI-309).
	baseURL string
}

// NewRunnerControlHandler constructs the handler. registry/runs may be nil
// when the coding-agent harness is disabled, in which case endpoints return
// 503 rather than panicking.
func NewRunnerControlHandler(registry *services.RunnerRegistryService, runs *repository.AgentRunRepository, runSvc *services.RunService, caps *repository.ActionRepository, now func() time.Time, baseURL string) *RunnerControlHandler {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RunnerControlHandler{
		registry: registry,
		runs:     runs,
		runSvc:   runSvc,
		caps:     caps,
		limiter:  newRunnerInstanceRateLimiter(runnerControlRequestsPerSecond, runnerControlBurst),
		now:      now,
		baseURL:  baseURL,
	}
}

// poolMaxConcurrent reads an enabled runner_pool capability's quota. Pool
// deletion/disable/type changes fail closed so a stale instance credential
// cannot claim new work after its pool has been revoked.
func (h *RunnerControlHandler) poolMaxConcurrent(poolID int) (int, error) {
	if h.caps == nil {
		return 0, fmt.Errorf("runner capability repository is not configured")
	}
	capRow, err := h.caps.GetCapabilityByID(poolID)
	if errors.Is(err, repository.ErrNotFound) || capRow == nil {
		return 0, services.ErrRunnerPoolUnavailable
	}
	if err != nil {
		return 0, fmt.Errorf("load runner pool capability: %w", err)
	}
	if capRow.CapabilityType != models.CapabilityRunnerPool || !capRow.IsEnabled {
		return 0, services.ErrRunnerPoolUnavailable
	}
	var cfg models.RunnerPoolConfig
	if err := json.Unmarshal([]byte(capRow.Config), &cfg); err != nil {
		return 0, fmt.Errorf("decode runner pool capability config: %w", err)
	}
	if cfg.MaxConcurrentRuns < 0 {
		return 0, fmt.Errorf("runner pool capability has negative max_concurrent_runs")
	}
	return cfg.MaxConcurrentRuns, nil
}

// Register exchanges a pool registration token for a per-instance runner
// credential. Unauthenticated: the registration token in the body is the
// credential. POST /runner/register.
func (h *RunnerControlHandler) Register(w http.ResponseWriter, r *http.Request) {
	if h.registry == nil {
		respondServiceUnavailable(w, r, "coding-agent harness is disabled on this server")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, runnerControlMaxBodyBytes)
	var req services.RegisterRequest
	if err := newJSONDecoder(w, r).Decode(&req); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	// Name renders in the pool's instance list + claim logs. The
	// registration token is a machine credential and stays untouched.
	sanitize.Apply(&req.Name, sanitize.PlainTextField)
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
	maxRuns, err := h.poolMaxConcurrent(inst.PoolCapabilityID)
	if errors.Is(err, services.ErrRunnerPoolUnavailable) {
		respondForbidden(w, r)
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	// Treat a successful claim poll as liveness too (WI-545). This prevents a
	// runner whose heartbeat loop was delayed by an idle/server outage window
	// from claiming work with an old last_heartbeat_at and then having that work
	// reaped before the next heartbeat tick.
	if err := h.registry.Heartbeat(r.Context(), inst.ID); err != nil {
		respondInternalError(w, r, err)
		return
	}
	// Soft pool cap: concurrent count and claim can briefly overshoot.
	if maxRuns > 0 {
		running, err := h.runs.CountRunningForPool(r.Context(), inst.PoolCapabilityID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if running >= maxRuns {
			respondJSONOK(w, services.ClaimResponse{Job: nil}) // pool at capacity
			return
		}
	}
	// Server-side round robin prevents a fast or newly registered poller from
	// monopolizing work; out-of-turn runners receive job=null.
	next, err := h.registry.NextRunner(r.Context(), inst.PoolCapabilityID)
	if err != nil {
		if errors.Is(err, services.ErrNoLiveRunner) {
			// Do not assign work to a runner the reaper may revoke.
			respondJSONOK(w, services.ClaimResponse{Job: nil})
			return
		}
		respondInternalError(w, r, err)
		return
	}
	var run *models.AgentRun
	if next.ID == inst.ID {
		// The turn is consumed even if another claimer drained the queue.
		run, err = h.runs.ClaimQueuedForRunner(r.Context(), inst.PoolCapabilityID, inst.ID, h.now())
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
	}
	if run == nil {
		respondJSONOK(w, services.ClaimResponse{Job: nil})
		return
	}
	// Log and event together make queued-to-running transitions traceable.
	slog.Info("runner claimed agent run",
		slog.Int("run_id", run.ID),
		slog.Int("pool_id", inst.PoolCapabilityID),
		slog.Int("runner_id", inst.ID),
		slog.String("runner_name", inst.Name),
	)
	claimedPayload, _ := json.Marshal(map[string]any{
		"phase": "claimed", "runner_id": inst.ID, "runner_name": inst.Name,
	})
	if err := h.runs.AppendEvent(r.Context(), run.ID, "lifecycle", string(claimedPayload)); err != nil {
		slog.Warn("append claimed event", slog.Int("run_id", run.ID), slog.Any("error", err))
	}
	// Enrich the claim: a binding-backed coding-agent run gets its per-run
	// token minted, grants persisted, and runner context env populated, the
	// same preamble the local path runs (WI-195). Runs with no binding (e.g.
	// action_container) come back with just {RunID, Kind, Image}.
	if h.runSvc != nil {
		spec, err := h.runSvc.PrepareRemoteClaim(r.Context(), run)
		if err != nil {
			// ClaimQueuedForRunner already moved the run to running; if enrichment fails
			// we must not leave it stranded there with no token/grants holding a
			// pool slot. Fail it so the slot frees up (WI-238 Phase 8).
			h.runSvc.FailRemoteClaim(r.Context(), run.ID, err.Error())
			respondInternalError(w, r, err)
			return
		}
		respondJSONOK(w, services.ClaimResponse{Job: &spec})
		return
	}
	respondJSONOK(w, services.ClaimResponse{Job: &services.JobSpec{RunID: run.ID, Kind: run.JobKind, Image: run.JobImage}})
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
	r.Body = http.MaxBytesReader(w, r.Body, runnerControlMaxBodyBytes)
	var req services.EmitRequest
	if err := newJSONDecoder(w, r).Decode(&req); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	sanitize.Apply(&req.Type, sanitize.ShortIdentifier)
	// PayloadJSON is a JSON blob, so HTML stripping would corrupt valid
	// payloads — bound it by size and require well-formed JSON instead.
	if len(req.PayloadJSON) > sanitize.LongTextMaxBytes {
		respondBadRequest(w, r, "payload_json exceeds the maximum size")
		return
	}
	payload := req.PayloadJSON
	if payload == "" {
		payload = "{}"
	}
	if !json.Valid([]byte(payload)) {
		respondBadRequest(w, r, "payload_json must be valid JSON")
		return
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
	r.Body = http.MaxBytesReader(w, r.Body, runnerControlMaxBodyBytes)
	var req services.ReportRequest
	if err := newJSONDecoder(w, r).Decode(&req); err != nil {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	// Error and Summary render as prose (Summary becomes the PR note, WI-400);
	// the rest are identifier-shaped (container id, branch name, commit sha).
	// Summary is agent-generated, so RichText strips HTML and caps its length
	// before it reaches an SCM PR body. Status is enum-validated below.
	sanitize.ApplyAll(
		sanitize.Pair{Target: &req.Error, Policy: sanitize.RichText},
		sanitize.Pair{Target: &req.Summary, Policy: sanitize.RichText},
		sanitize.Pair{Target: &req.ContainerID, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &req.Branch, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &req.BaseCommit, Policy: sanitize.ShortIdentifier},
	)
	// Sanitize repository identifiers before the PR hook consumes them.
	var resultRepos []services.RunnerRepoResult
	for i := range req.Repos {
		sanitize.ApplyAll(
			sanitize.Pair{Target: &req.Repos[i].RepoSlug, Policy: sanitize.ShortIdentifier},
			sanitize.Pair{Target: &req.Repos[i].Branch, Policy: sanitize.ShortIdentifier},
			sanitize.Pair{Target: &req.Repos[i].BaseCommit, Policy: sanitize.ShortIdentifier},
		)
		resultRepos = append(resultRepos, services.RunnerRepoResult{
			RepoSlug:   req.Repos[i].RepoSlug,
			Branch:     req.Repos[i].Branch,
			BaseCommit: req.Repos[i].BaseCommit,
		})
	}
	status := req.Status
	if !models.IsAgentRunTerminal(status) {
		respondBadRequest(w, r, "status must be a terminal agent-run state")
		return
	}
	// RunService preserves normal finalization and post-run-hook behavior.
	if h.runSvc != nil {
		if err := h.runSvc.FinalizeRemote(r.Context(), runID, services.RunnerResult{
			Status:      status,
			Error:       req.Error,
			ContainerID: req.ContainerID,
			Branch:      req.Branch,
			BaseCommit:  req.BaseCommit,
			Summary:     req.Summary,
			Repos:       resultRepos,
		}, req.Branch, req.BaseCommit); err != nil {
			respondInternalError(w, r, err)
			return
		}
		respondJSONOK(w, map[string]any{"ok": true})
		return
	}
	if req.ContainerID != "" {
		if err := h.runs.SetContainerID(r.Context(), runID, req.ContainerID); err != nil {
			respondInternalError(w, r, err)
			return
		}
	}
	// Finalize only a running run so late results cannot rewrite history.
	transitioned, err := h.runs.FinalizeRunning(r.Context(), runID, status, services.RedactString(req.Error), h.now())
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !transitioned {
		respondConflict(w, r, "agent run is not running")
		return
	}
	_ = h.runs.AppendEvent(r.Context(), runID, "lifecycle", `{"phase":"`+status+`"}`)
	respondJSONOK(w, map[string]any{"ok": true})
}

// Heartbeat renews a runner lease and returns abort IDs and queue depth.
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

// requireRunner authenticates the per-instance runner credential and applies
// its isolated request budget. It writes the error response on failure.
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
	if !h.limiter.Allow(inst.ID) {
		w.Header().Set("Retry-After", "1")
		respondTooManyRequests(w, r, "Runner request rate exceeded. Retry shortly.")
		return nil, false
	}
	return inst, true
}

// ownsRun requires a running run claimed by this runner, preventing historical
// runs from receiving events or revised verdicts.
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
	if run.Status != models.AgentRunStatusRunning {
		respondConflict(w, r, "agent run is not running")
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
