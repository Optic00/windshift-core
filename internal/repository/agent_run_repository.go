package repository

import (
	"context"
	"database/sql"
	"fmt"
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
		INSERT INTO agent_runs(workspace_id, item_id, binding_id, status)
		VALUES (?, ?, ?, ?)
	`,
		run.WorkspaceID, nullIntArg(run.ItemID), nullIntArg(run.BindingID), status,
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
		       container_id, error, created_at, updated_at
		FROM agent_runs WHERE id = ?
	`, id)

	run := &models.AgentRun{}
	var itemID, bindingID sql.NullInt64
	var startedAt, endedAt sql.NullTime
	var containerID, errMsg sql.NullString

	if err := row.Scan(
		&run.ID, &run.WorkspaceID, &itemID, &bindingID, &run.Status,
		&run.QueuedAt, &startedAt, &endedAt,
		&containerID, &errMsg, &run.CreatedAt, &run.UpdatedAt,
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
func (r *AgentRunRepository) AppendEvent(ctx context.Context, runID int, eventType, payloadJSON string) error {
	if payloadJSON == "" {
		payloadJSON = "{}"
	}
	_, err := r.db.ExecWriteContext(ctx, `
		INSERT INTO agent_run_events(run_id, type, payload_json)
		VALUES (?, ?, ?)
	`, runID, eventType, payloadJSON)
	if err != nil {
		return fmt.Errorf("failed to append agent_run_event: %w", err)
	}
	return nil
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
