package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"windshift/internal/database"
	"windshift/internal/redact"
)

const (
	SCMHealthOperationRepositorySync = "repository_sync"
	SCMHealthOperationPRLinkRefresh  = "pull_request_refresh"

	SCMHealthStateHealthy      = "healthy"
	SCMHealthStateUnhealthy    = "unhealthy"
	SCMHealthStateDisabled     = "disabled"
	SCMHealthStateNeverChecked = "never_checked"

	maxSCMHealthErrorRunes = 2000
)

var scmHealthOperations = []string{
	SCMHealthOperationRepositorySync,
	SCMHealthOperationPRLinkRefresh,
}

// SCMHealthResult is one aggregate scheduled-operation result for a connection.
type SCMHealthResult struct {
	ConnectionID     int
	Operation        string
	AttemptedAt      time.Time
	CheckedResources int
	FailedResources  int
	LastError        string
}

// SCMHealthTransition describes a state change worth surfacing in logs.
type SCMHealthTransition struct {
	BecameUnhealthy bool
	ErrorChanged    bool
	Recovered       bool
}

// SCMOperationDiagnostic is the durable status of one background operation.
type SCMOperationDiagnostic struct {
	Operation           string     `json:"operation"`
	State               string     `json:"state"`
	Healthy             bool       `json:"healthy"`
	LastAttemptAt       *time.Time `json:"last_attempt_at"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	LastFailureAt       *time.Time `json:"last_failure_at"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	CheckedResources    int        `json:"checked_resources"`
	FailedResources     int        `json:"failed_resources"`
	LastError           string     `json:"last_error"`
}

// SCMRepositoryDiagnostic identifies a repository attached to a connection.
type SCMRepositoryDiagnostic struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// SCMConnectionDiagnostic combines connection metadata and operation health.
type SCMConnectionDiagnostic struct {
	ID                    int                       `json:"id"`
	WorkspaceID           int                       `json:"workspace_id"`
	WorkspaceName         string                    `json:"workspace_name"`
	WorkspaceKey          string                    `json:"workspace_key"`
	ProviderName          string                    `json:"provider_name"`
	ProviderSlug          string                    `json:"provider_slug"`
	ProviderBaseURL       string                    `json:"provider_base_url"`
	ProviderType          string                    `json:"provider_type"`
	AuthMethod            string                    `json:"auth_method"`
	Enabled               bool                      `json:"enabled"`
	RepositoryCount       int                       `json:"repository_count"`
	ActiveRepositoryCount int                       `json:"active_repository_count"`
	Repositories          []SCMRepositoryDiagnostic `json:"repositories"`
	State                 string                    `json:"state"`
	Healthy               bool                      `json:"healthy"`
	Operations            []SCMOperationDiagnostic  `json:"operations"`
}

// SCMHealthRepository persists bounded connection health snapshots.
type SCMHealthRepository struct {
	db database.Database
}

func NewSCMHealthRepository(db database.Database) *SCMHealthRepository {
	return &SCMHealthRepository{db: db}
}

// RecordResult upserts one aggregate result and reports meaningful state changes.
func (r *SCMHealthRepository) RecordResult(ctx context.Context, result SCMHealthResult) (SCMHealthTransition, error) {
	if result.ConnectionID <= 0 {
		return SCMHealthTransition{}, fmt.Errorf("SCM connection ID must be positive")
	}
	if !validSCMHealthOperation(result.Operation) {
		return SCMHealthTransition{}, fmt.Errorf("unsupported SCM health operation %q", result.Operation)
	}
	if result.CheckedResources < 0 || result.FailedResources < 0 || result.FailedResources > result.CheckedResources {
		return SCMHealthTransition{}, fmt.Errorf("invalid SCM resource counts: checked=%d failed=%d", result.CheckedResources, result.FailedResources)
	}
	if result.AttemptedAt.IsZero() {
		result.AttemptedAt = time.Now().UTC()
	} else {
		result.AttemptedAt = result.AttemptedAt.UTC()
	}

	var previousFailures int
	var previousError sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT consecutive_failures, last_error
		FROM scm_connection_health
		WHERE workspace_scm_connection_id = ? AND operation = ?
	`, result.ConnectionID, result.Operation).Scan(&previousFailures, &previousError)
	if err != nil && err != sql.ErrNoRows {
		return SCMHealthTransition{}, fmt.Errorf("load previous SCM health: %w", err)
	}

	sanitizedError := truncateRunes(redact.String(strings.TrimSpace(result.LastError)), maxSCMHealthErrorRunes)
	failed := result.FailedResources > 0
	var successAt, failureAt any
	if failed {
		failureAt = result.AttemptedAt
	} else {
		successAt = result.AttemptedAt
		sanitizedError = ""
	}

	_, err = r.db.ExecWriteContext(ctx, `
		INSERT INTO scm_connection_health (
			workspace_scm_connection_id, operation, last_attempt_at,
			last_success_at, last_failure_at, consecutive_failures,
			checked_resources, failed_resources, last_error, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_scm_connection_id, operation) DO UPDATE SET
			last_attempt_at = excluded.last_attempt_at,
			last_success_at = CASE
				WHEN excluded.failed_resources = 0 THEN excluded.last_attempt_at
				ELSE scm_connection_health.last_success_at
			END,
			last_failure_at = CASE
				WHEN excluded.failed_resources > 0 THEN excluded.last_attempt_at
				ELSE scm_connection_health.last_failure_at
			END,
			consecutive_failures = CASE
				WHEN excluded.failed_resources > 0 THEN scm_connection_health.consecutive_failures + 1
				ELSE 0
			END,
			checked_resources = excluded.checked_resources,
			failed_resources = excluded.failed_resources,
			last_error = CASE WHEN excluded.failed_resources > 0 THEN excluded.last_error ELSE NULL END,
			updated_at = excluded.updated_at
	`, result.ConnectionID, result.Operation, result.AttemptedAt, successAt, failureAt,
		boolCount(failed), result.CheckedResources, result.FailedResources, scmNullableString(sanitizedError), result.AttemptedAt)
	if err != nil {
		return SCMHealthTransition{}, fmt.Errorf("record SCM health: %w", err)
	}

	return SCMHealthTransition{
		BecameUnhealthy: failed && previousFailures == 0,
		ErrorChanged:    failed && previousFailures > 0 && previousError.String != sanitizedError,
		Recovered:       !failed && previousFailures > 0,
	}, nil
}

// ListConnectionDiagnostics returns every connection, including disabled and
// never-checked connections, so absence is not mistaken for health.
func (r *SCMHealthRepository) ListConnectionDiagnostics(ctx context.Context) ([]SCMConnectionDiagnostic, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT wsc.id, wsc.workspace_id, w.name, w.key, sp.name, sp.slug,
			sp.base_url, sp.provider_type, sp.auth_method, wsc.enabled
		FROM workspace_scm_connections wsc
		JOIN workspaces w ON w.id = wsc.workspace_id
		JOIN scm_providers sp ON sp.id = wsc.scm_provider_id
		ORDER BY w.name, sp.name, wsc.id
	`)
	if err != nil {
		return nil, fmt.Errorf("list SCM connections: %w", err)
	}
	defer func() { _ = rows.Close() }()

	connections := make([]SCMConnectionDiagnostic, 0)
	byID := make(map[int]int)
	for rows.Next() {
		var connection SCMConnectionDiagnostic
		var providerBaseURL sql.NullString
		if err := rows.Scan(
			&connection.ID, &connection.WorkspaceID, &connection.WorkspaceName,
			&connection.WorkspaceKey, &connection.ProviderName, &connection.ProviderSlug,
			&providerBaseURL, &connection.ProviderType, &connection.AuthMethod, &connection.Enabled,
		); err != nil {
			return nil, fmt.Errorf("scan SCM connection diagnostics: %w", err)
		}
		connection.ProviderBaseURL = providerBaseURL.String
		connection.Repositories = []SCMRepositoryDiagnostic{}
		connection.Operations = defaultSCMOperationDiagnostics()
		byID[connection.ID] = len(connections)
		connections = append(connections, connection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SCM connection diagnostics: %w", err)
	}
	_ = rows.Close()

	repositoryRows, err := r.db.QueryContext(ctx, `
		SELECT id, workspace_scm_connection_id, repository_name, repository_url, is_active
		FROM workspace_repositories
		ORDER BY workspace_scm_connection_id, is_active DESC, repository_name, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list SCM diagnostic repositories: %w", err)
	}
	defer func() { _ = repositoryRows.Close() }()
	for repositoryRows.Next() {
		var connectionID int
		var scmRepository SCMRepositoryDiagnostic
		if err := repositoryRows.Scan(
			&scmRepository.ID, &connectionID, &scmRepository.Name,
			&scmRepository.URL, &scmRepository.Active,
		); err != nil {
			return nil, fmt.Errorf("scan SCM diagnostic repository: %w", err)
		}
		connectionIndex, ok := byID[connectionID]
		if !ok {
			continue
		}
		connection := &connections[connectionIndex]
		connection.Repositories = append(connection.Repositories, scmRepository)
		connection.RepositoryCount++
		if scmRepository.Active {
			connection.ActiveRepositoryCount++
		}
	}
	if err := repositoryRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SCM diagnostic repositories: %w", err)
	}
	_ = repositoryRows.Close()

	healthRows, err := r.db.QueryContext(ctx, `
		SELECT workspace_scm_connection_id, operation, last_attempt_at,
			last_success_at, last_failure_at, consecutive_failures,
			checked_resources, failed_resources, last_error
		FROM scm_connection_health
	`)
	if err != nil {
		return nil, fmt.Errorf("list SCM operation health: %w", err)
	}
	defer func() { _ = healthRows.Close() }()
	for healthRows.Next() {
		var connectionID int
		var operation SCMOperationDiagnostic
		var attemptedAt, successAt, failureAt sql.NullTime
		var lastError sql.NullString
		if err := healthRows.Scan(
			&connectionID, &operation.Operation, &attemptedAt, &successAt,
			&failureAt, &operation.ConsecutiveFailures, &operation.CheckedResources,
			&operation.FailedResources, &lastError,
		); err != nil {
			return nil, fmt.Errorf("scan SCM operation health: %w", err)
		}
		connectionIndex, ok := byID[connectionID]
		if !ok || !validSCMHealthOperation(operation.Operation) {
			continue
		}
		operation.LastAttemptAt = scmTimePointer(attemptedAt)
		operation.LastSuccessAt = scmTimePointer(successAt)
		operation.LastFailureAt = scmTimePointer(failureAt)
		operation.LastError = lastError.String
		operation.Healthy = operation.ConsecutiveFailures == 0
		operation.State = SCMHealthStateHealthy
		if !operation.Healthy {
			operation.State = SCMHealthStateUnhealthy
		}
		for index := range connections[connectionIndex].Operations {
			if connections[connectionIndex].Operations[index].Operation == operation.Operation {
				connections[connectionIndex].Operations[index] = operation
				break
			}
		}
	}
	if err := healthRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SCM operation health: %w", err)
	}

	for index := range connections {
		connection := &connections[index]
		connection.State = connectionHealthState(*connection)
		connection.Healthy = connection.State == SCMHealthStateHealthy
	}
	sort.SliceStable(connections, func(i, j int) bool {
		iUnhealthy := connections[i].State == SCMHealthStateUnhealthy
		jUnhealthy := connections[j].State == SCMHealthStateUnhealthy
		return iUnhealthy && !jUnhealthy
	})
	return connections, nil
}

func validSCMHealthOperation(operation string) bool {
	for _, candidate := range scmHealthOperations {
		if candidate == operation {
			return true
		}
	}
	return false
}

func defaultSCMOperationDiagnostics() []SCMOperationDiagnostic {
	operations := make([]SCMOperationDiagnostic, 0, len(scmHealthOperations))
	for _, operation := range scmHealthOperations {
		operations = append(operations, SCMOperationDiagnostic{
			Operation: operation,
			State:     SCMHealthStateNeverChecked,
		})
	}
	return operations
}

func connectionHealthState(connection SCMConnectionDiagnostic) string {
	if !connection.Enabled {
		return SCMHealthStateDisabled
	}
	hasAttempt := false
	for _, operation := range connection.Operations {
		if operation.State == SCMHealthStateUnhealthy {
			return SCMHealthStateUnhealthy
		}
		hasAttempt = hasAttempt || operation.LastAttemptAt != nil
	}
	if !hasAttempt {
		return SCMHealthStateNeverChecked
	}
	return SCMHealthStateHealthy
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func scmNullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func scmTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	timestamp := value.Time.UTC()
	return &timestamp
}
