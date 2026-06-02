package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
)

// AgentRunRepository persists coding-agent runs and their event streams.
// RunService is the only writer in normal operation; the admin
// "Agent runs" UI is the dominant reader.
type AgentRunRepository struct {
	db database.Database
}

// NewAgentRunRepository constructs a new repository.
func NewAgentRunRepository(db database.Database) *AgentRunRepository {
	return &AgentRunRepository{db: db}
}

// Insert creates a new agent_runs row in the queued state and returns the
// new ID. Fields managed by the DB (id, queued_at, created_at, updated_at)
// are populated from defaults; the caller is responsible for workspace_id,
// item_id, and binding_id.
func (r *AgentRunRepository) Insert(ctx context.Context, run *models.AgentRun) (int, error) {
	status := run.Status
	if status == "" {
		status = models.AgentRunStatusQueued
	}
	res, err := r.db.ExecWriteContext(ctx, `
		INSERT INTO agent_runs(workspace_id, item_id, binding_id, target_pool_id, status)
		VALUES (?, ?, ?, ?, ?)
	`,
		run.WorkspaceID, nullIntArg(run.ItemID), nullIntArg(run.BindingID),
		nullIntArg(run.TargetPoolID), status,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert agent_run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to read insert id: %w", err)
	}
	return int(id), nil
}

// Get loads a single run by ID. Returns sql.ErrNoRows if it does not exist.
func (r *AgentRunRepository) Get(ctx context.Context, id int) (*models.AgentRun, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, item_id, binding_id, status, queued_at, started_at, ended_at,
		       container_id, runner_id, target_pool_id, error, created_at, updated_at
		FROM agent_runs WHERE id = ?
	`, id)

	run := &models.AgentRun{}
	var itemID, bindingID, runnerID, targetPoolID sql.NullInt64
	var startedAt, endedAt sql.NullTime
	var containerID, errMsg sql.NullString

	if err := row.Scan(
		&run.ID, &run.WorkspaceID, &itemID, &bindingID, &run.Status,
		&run.QueuedAt, &startedAt, &endedAt,
		&containerID, &runnerID, &targetPoolID, &errMsg, &run.CreatedAt, &run.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if itemID.Valid {
		v := int(itemID.Int64)
		run.ItemID = &v
	}
	if bindingID.Valid {
		v := int(bindingID.Int64)
		run.BindingID = &v
	}
	if runnerID.Valid {
		v := int(runnerID.Int64)
		run.RunnerID = &v
	}
	if targetPoolID.Valid {
		v := int(targetPoolID.Int64)
		run.TargetPoolID = &v
	}
	if startedAt.Valid {
		run.StartedAt = &startedAt.Time
	}
	if endedAt.Valid {
		run.EndedAt = &endedAt.Time
	}
	if containerID.Valid {
		run.ContainerID = containerID.String
	}
	if errMsg.Valid {
		run.Error = errMsg.String
	}
	return run, nil
}

// CountForBindingSince returns the number of runs created against the
// given binding at or after `since`. Used to enforce a binding's
// max_runs_per_day budget before admitting a new run. Returns 0 when
// bindingID is 0 — callers that don't have a binding shouldn't be
// enforcing a per-binding budget in the first place.
func (r *AgentRunRepository) CountForBindingSince(ctx context.Context, bindingID int, since time.Time) (int, error) {
	if bindingID == 0 {
		return 0, nil
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_runs
		WHERE binding_id = ? AND created_at >= ?
	`, bindingID, since)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count runs for binding: %w", err)
	}
	return n, nil
}

// MarkRunning transitions a run from queued to running and stamps started_at.
// Callers must hold their admission-control slot before invoking this.
func (r *AgentRunRepository) MarkRunning(ctx context.Context, id int, containerID string, now time.Time) error {
	_, err := r.db.ExecWriteContext(ctx, `
		UPDATE agent_runs
		SET status = ?, started_at = ?, container_id = ?, updated_at = ?
		WHERE id = ? AND status = ?
	`,
		models.AgentRunStatusRunning, now, nullStringArg(containerID), now,
		id, models.AgentRunStatusQueued,
	)
	if err != nil {
		return fmt.Errorf("failed to mark agent_run running: %w", err)
	}
	return nil
}

// ClaimQueued atomically claims the oldest queued run targeted at the given
// pool, transitioning it queued→running and stamping the claiming runner +
// started_at. It is the DB-as-queue primitive a remote runner polls: the
// agent_runs table itself is the queue (Initiative WI-141). Returns
// (nil, nil) when no queued run is available for the pool.
//
// Atomicity uses the same status-guarded CAS as MarkRunning: pick a
// candidate, then UPDATE ... WHERE id=? AND status='queued'. If a racing
// runner won the row first, the guarded update affects zero rows and we
// retry with the next candidate. This needs no FOR UPDATE / SKIP LOCKED, so
// it behaves identically on SQLite and Postgres.
func (r *AgentRunRepository) ClaimQueued(ctx context.Context, poolID, runnerID int, now time.Time) (*models.AgentRun, error) {
	const maxAttempts = 16
	for attempt := 0; attempt < maxAttempts; attempt++ {
		row := r.db.QueryRowContext(ctx, `
			SELECT id FROM agent_runs
			WHERE status = ? AND target_pool_id = ?
			ORDER BY queued_at ASC
			LIMIT 1
		`, models.AgentRunStatusQueued, poolID)
		var id int
		switch err := row.Scan(&id); err {
		case sql.ErrNoRows:
			return nil, nil
		case nil:
			// fall through to the guarded claim
		default:
			return nil, fmt.Errorf("claim queued: select candidate: %w", err)
		}

		res, err := r.db.ExecWriteContext(ctx, `
			UPDATE agent_runs
			SET status = ?, runner_id = ?, started_at = ?, updated_at = ?
			WHERE id = ? AND status = ?
		`,
			models.AgentRunStatusRunning, runnerID, now, now,
			id, models.AgentRunStatusQueued,
		)
		if err != nil {
			return nil, fmt.Errorf("claim queued: mark running: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("claim queued: rows affected: %w", err)
		}
		if n == 1 {
			return r.Get(ctx, id)
		}
		// Lost the race for this candidate; try the next queued run.
	}
	// Heavy contention exhausted the retry budget; the caller polls again.
	return nil, nil
}

// CountQueuedForPool returns the number of queued runs targeted at the given
// pool — the per-pool queue depth an autoscaler scales on (WI-141).
func (r *AgentRunRepository) CountQueuedForPool(ctx context.Context, poolID int) (int, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_runs WHERE status = ? AND target_pool_id = ?
	`, models.AgentRunStatusQueued, poolID)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count queued for pool: %w", err)
	}
	return n, nil
}

// CountRunningForPool returns how many runs are currently running on the
// given pool — used to enforce the pool's max-concurrency quota (WI-147)
// before handing out another claim.
func (r *AgentRunRepository) CountRunningForPool(ctx context.Context, poolID int) (int, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_runs WHERE status = ? AND target_pool_id = ?
	`, models.AgentRunStatusRunning, poolID)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count running for pool: %w", err)
	}
	return n, nil
}

// RequestCancel flags a running run for cancellation. The runner that owns
// the run learns via its heartbeat and aborts. Idempotent no-op (zero rows)
// when the run is not running or is already flagged.
func (r *AgentRunRepository) RequestCancel(ctx context.Context, runID int, now time.Time) error {
	_, err := r.db.ExecWriteContext(ctx, `
		UPDATE agent_runs SET cancel_requested_at = ?, updated_at = ?
		WHERE id = ? AND status = ? AND cancel_requested_at IS NULL
	`, now, now, runID, models.AgentRunStatusRunning)
	if err != nil {
		return fmt.Errorf("request cancel: %w", err)
	}
	return nil
}

// ListAbortableRuns returns the ids of runs the given runner is executing
// that have been flagged for cancellation, so the heartbeat handler can tell
// the runner which jobs to abort.
func (r *AgentRunRepository) ListAbortableRuns(ctx context.Context, runnerInstanceID int) ([]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id FROM agent_runs
		WHERE runner_id = ? AND status = ? AND cancel_requested_at IS NOT NULL
	`, runnerInstanceID, models.AgentRunStatusRunning)
	if err != nil {
		return nil, fmt.Errorf("list abortable runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan abortable run: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ReapStaleRuns fails any running run whose owning runner has gone stale —
// revoked, or with a heartbeat older than staleBefore (or never seen and
// registered before staleBefore). It is the liveness backstop for remote
// runs whose runner died mid-execution (WI-141). Returns the number reaped.
func (r *AgentRunRepository) ReapStaleRuns(ctx context.Context, staleBefore, now time.Time) (int, error) {
	res, err := r.db.ExecWriteContext(ctx, `
		UPDATE agent_runs
		SET status = ?, error = ?, ended_at = ?, updated_at = ?
		WHERE status = ?
		  AND runner_id IS NOT NULL
		  AND runner_id IN (
		    SELECT id FROM runner_instances
		    WHERE status = ?
		       OR (last_heartbeat_at IS NOT NULL AND last_heartbeat_at < ?)
		       OR (last_heartbeat_at IS NULL AND registered_at < ?)
		  )
	`,
		models.AgentRunStatusFailed, "runner lease expired (missed heartbeat)", now, now,
		models.AgentRunStatusRunning,
		models.RunnerInstanceStatusRevoked, staleBefore, staleBefore,
	)
	if err != nil {
		return 0, fmt.Errorf("reap stale runs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reap stale runs: rows affected: %w", err)
	}
	return int(n), nil
}

// SetGrants snapshots a run's access-layer grants and binds the run to the
// minted run-token that authorizes them (WI-144). Called from the claim path
// once the grants are derived from the binding.
func (r *AgentRunRepository) SetGrants(ctx context.Context, runID, tokenID int, grants *models.RunGrants, now time.Time) error {
	b, err := json.Marshal(grants)
	if err != nil {
		return fmt.Errorf("set grants: marshal: %w", err)
	}
	if _, err := r.db.ExecWriteContext(ctx, `
		UPDATE agent_runs SET grants_json = ?, run_token_id = ?, updated_at = ?
		WHERE id = ?
	`, string(b), tokenID, now, runID); err != nil {
		return fmt.Errorf("set grants: %w", err)
	}
	return nil
}

// GetRunAuthz returns what a broker needs to authorize a request for a run:
// the id of the token bound to the run (0 if none), the run's grants (nil if
// unset), and the run's current status. Brokers verify the presented token's
// id matches, the status is running, and the resource is in the grants.
func (r *AgentRunRepository) GetRunAuthz(ctx context.Context, runID int) (tokenID, workspaceID int, grants *models.RunGrants, status string, err error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT run_token_id, workspace_id, grants_json, status FROM agent_runs WHERE id = ?
	`, runID)
	var tid sql.NullInt64
	var grantsJSON sql.NullString
	if err := row.Scan(&tid, &workspaceID, &grantsJSON, &status); err != nil {
		return 0, 0, nil, "", err
	}
	if grantsJSON.Valid && grantsJSON.String != "" {
		grants = &models.RunGrants{}
		if err := json.Unmarshal([]byte(grantsJSON.String), grants); err != nil {
			return 0, 0, nil, "", fmt.Errorf("get run authz: unmarshal grants: %w", err)
		}
	}
	return int(tid.Int64), workspaceID, grants, status, nil
}

// GetRunByTokenID resolves the run bound to a given run-token id — used by
// the git broker, where the run id is not in the URL (the clone URL is
// stable/repo-scoped) so the presented token is what identifies the run.
func (r *AgentRunRepository) GetRunByTokenID(ctx context.Context, tokenID int) (runID, workspaceID int, grants *models.RunGrants, status string, err error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, grants_json, status FROM agent_runs WHERE run_token_id = ?
	`, tokenID)
	var grantsJSON sql.NullString
	if err := row.Scan(&runID, &workspaceID, &grantsJSON, &status); err != nil {
		return 0, 0, nil, "", err
	}
	if grantsJSON.Valid && grantsJSON.String != "" {
		grants = &models.RunGrants{}
		if err := json.Unmarshal([]byte(grantsJSON.String), grants); err != nil {
			return 0, 0, nil, "", fmt.Errorf("get run by token: unmarshal grants: %w", err)
		}
	}
	return runID, workspaceID, grants, status, nil
}

// SetContainerID records the spawned container id on an existing run row.
// MarkRunning's queued→running transition is intentionally guarded by
// status, so the runner uses this separate path once it actually has a
// container handle to stamp (which may be after MarkRunning has already
// flipped the status).
func (r *AgentRunRepository) SetContainerID(ctx context.Context, id int, containerID string) error {
	if containerID == "" {
		return nil
	}
	_, err := r.db.ExecWriteContext(ctx, `
		UPDATE agent_runs
		SET container_id = ?, updated_at = ?
		WHERE id = ?
	`, containerID, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to set agent_run container_id: %w", err)
	}
	return nil
}

// Finalize stamps a terminal status + ended_at + error message. Status must
// be one of the terminal values (see IsAgentRunTerminal). errMsg is stored
// verbatim; pass "" for successful runs.
func (r *AgentRunRepository) Finalize(ctx context.Context, id int, status, errMsg string, now time.Time) error {
	if !models.IsAgentRunTerminal(status) {
		return fmt.Errorf("agent_run finalize: %q is not a terminal status", status)
	}
	_, err := r.db.ExecWriteContext(ctx, `
		UPDATE agent_runs
		SET status = ?, ended_at = ?, error = ?, updated_at = ?
		WHERE id = ?
	`,
		status, now, nullStringArg(errMsg), now, id,
	)
	if err != nil {
		return fmt.Errorf("failed to finalize agent_run: %w", err)
	}
	return nil
}

// AppendEvent records one entry on the run's event stream. payloadJSON must
// be a JSON document (valid object, array, or scalar); the column type is
// JSONB on Postgres and TEXT on SQLite, but we treat it as opaque here.
//
// payloadJSON is scrubbed via redactURLCredentials before persistence so
// any token-bearing URL fragment (e.g. an upstream git error containing
// https://oauth2:<token>@host/...) cannot leak into the event stream
// that's visible to every item viewer in the workspace.
func (r *AgentRunRepository) AppendEvent(ctx context.Context, runID int, eventType, payloadJSON string) error {
	if payloadJSON == "" {
		payloadJSON = "{}"
	}
	_, err := r.db.ExecWriteContext(ctx, `
		INSERT INTO agent_run_events(run_id, type, payload_json)
		VALUES (?, ?, ?)
	`, runID, eventType, redactURLCredentials(payloadJSON))
	if err != nil {
		return fmt.Errorf("failed to append agent_run_event: %w", err)
	}
	return nil
}

// redactURLCredentials is the package-local mirror of
// services.RedactString. Kept here rather than imported because the
// repository layer must not depend on services (layer guard); the
// pattern is short enough to duplicate.
var urlCredRE = regexp.MustCompile(`(https?://)[^@/\s:]+:[^@/\s]+@`)

func redactURLCredentials(s string) string {
	if s == "" {
		return s
	}
	return urlCredRE.ReplaceAllString(s, "${1}[REDACTED]@")
}

// ListForWorkspace returns the most recent N runs in the workspace,
// newest first. Used by the workspace-admin runs list. beforeID is for
// cursor pagination ("give me runs with id < beforeID"); pass 0 for the
// first page. Empty result is not an error.
func (r *AgentRunRepository) ListForWorkspace(ctx context.Context, workspaceID, limit, beforeID int) ([]*models.AgentRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `
		SELECT id, workspace_id, item_id, binding_id, status, queued_at, started_at, ended_at,
		       container_id, error, created_at, updated_at
		FROM agent_runs
		WHERE workspace_id = ?
	`
	args := []any{workspaceID}
	if beforeID > 0 {
		query += " AND id < ?"
		args = append(args, beforeID)
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs for workspace: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*models.AgentRun
	for rows.Next() {
		run := &models.AgentRun{}
		var itemID, bindingID sql.NullInt64
		var startedAt, endedAt sql.NullTime
		var containerID, errMsg sql.NullString
		if err := rows.Scan(
			&run.ID, &run.WorkspaceID, &itemID, &bindingID, &run.Status,
			&run.QueuedAt, &startedAt, &endedAt,
			&containerID, &errMsg, &run.CreatedAt, &run.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan run row: %w", err)
		}
		if itemID.Valid {
			v := int(itemID.Int64)
			run.ItemID = &v
		}
		if bindingID.Valid {
			v := int(bindingID.Int64)
			run.BindingID = &v
		}
		if startedAt.Valid {
			run.StartedAt = &startedAt.Time
		}
		if endedAt.Valid {
			run.EndedAt = &endedAt.Time
		}
		if containerID.Valid {
			run.ContainerID = containerID.String
		}
		if errMsg.Valid {
			run.Error = errMsg.String
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// ListEventsAfter is the polling-style stream the UI uses to tail a
// run's event log: returns up to `limit` events with id > afterID,
// ordered by id ASC (insertion order). Empty result means "no new
// events since afterID."
func (r *AgentRunRepository) ListEventsAfter(ctx context.Context, runID, afterID, limit int) ([]*models.AgentRunEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, run_id, ts, type, payload_json
		FROM agent_run_events
		WHERE run_id = ? AND id > ?
		ORDER BY id ASC
		LIMIT ?
	`, runID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list events after: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*models.AgentRunEvent
	for rows.Next() {
		ev := &models.AgentRunEvent{}
		if err := rows.Scan(&ev.ID, &ev.RunID, &ev.Timestamp, &ev.Type, &ev.PayloadJSON); err != nil {
			return nil, fmt.Errorf("scan event row: %w", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ListEvents returns events for a run ordered chronologically (by id ASC,
// which matches insertion order in both backends). Used by the SSE backfill
// when a client connects mid-run.
func (r *AgentRunRepository) ListEvents(ctx context.Context, runID int) ([]*models.AgentRunEvent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, run_id, ts, type, payload_json
		FROM agent_run_events
		WHERE run_id = ?
		ORDER BY id ASC
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to query agent_run_events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*models.AgentRunEvent
	for rows.Next() {
		ev := &models.AgentRunEvent{}
		if err := rows.Scan(&ev.ID, &ev.RunID, &ev.Timestamp, &ev.Type, &ev.PayloadJSON); err != nil {
			return nil, fmt.Errorf("failed to scan agent_run_event: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate agent_run_events: %w", err)
	}
	return out, nil
}
