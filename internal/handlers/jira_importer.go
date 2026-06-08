package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"windshift/internal/logger"
	"windshift/internal/restapi"
	"windshift/internal/utils"

	"github.com/google/uuid"
)

// GetJobStatus handles GET /api/admin/jira-import/jobs/{jobId}
func (h *JiraImportHandler) GetJobStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobId")

	var status, phase, progressJSON, resultJSON, errorMessage sql.NullString
	var startedAt, completedAt sql.NullTime

	err := h.db.QueryRow(`
		SELECT status, phase, progress_json, result_json, error_message, started_at, completed_at
		FROM jira_import_jobs
		WHERE id = ?
	`, jobID).Scan(&status, &phase, &progressJSON, &resultJSON, &errorMessage, &startedAt, &completedAt)
	if err != nil {
		respondNotFound(w, r, "job")
		return
	}

	response := ImportJobStatus{
		JobID:  jobID,
		Status: status.String,
	}
	if phase.Valid {
		response.Phase = phase.String
	}
	if errorMessage.Valid {
		response.ErrorMessage = errorMessage.String
	}
	if startedAt.Valid {
		response.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		response.CompletedAt = &completedAt.Time
	}
	if progressJSON.Valid {
		var progress map[string]interface{}
		if err := json.Unmarshal([]byte(progressJSON.String), &progress); err == nil {
			response.Progress = progress
		}
	}

	respondJSONOK(w, response)
}

// GetImportJobs handles GET /api/admin/jira-import/jobs
func (h *JiraImportHandler) GetImportJobs(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT j.id, j.connection_id, c.instance_url, c.instance_name, j.status, j.phase, j.scope,
		       j.config_json, j.progress_json, j.result_json, j.error_message, j.created_at, j.started_at, j.completed_at
		FROM jira_import_jobs j
		LEFT JOIN jira_import_connections c ON j.connection_id = c.id
		ORDER BY j.created_at DESC
	`)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	defer func() { _ = rows.Close() }()

	jobs := make([]ImportJobInfo, 0)
	for rows.Next() {
		var job ImportJobInfo
		var instanceURL, instanceName, phase, configJSON, progressJSON, resultJSON, errorMessage sql.NullString
		var startedAt, completedAt sql.NullTime

		if err := rows.Scan(&job.ID, &job.ConnectionID, &instanceURL, &instanceName, &job.Status,
			&phase, &job.Scope, &configJSON, &progressJSON, &resultJSON, &errorMessage,
			&job.CreatedAt, &startedAt, &completedAt); err != nil {
			slog.Warn("Failed to scan job", slog.String("component", "jira"), slog.Any("error", err))
			continue
		}

		if instanceURL.Valid {
			job.InstanceURL = instanceURL.String
		}
		if instanceName.Valid {
			job.InstanceName = instanceName.String
		}
		if phase.Valid {
			job.Phase = phase.String
		}
		if errorMessage.Valid {
			job.ErrorMessage = errorMessage.String
		}
		if startedAt.Valid {
			job.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.Time
		}
		if progressJSON.Valid {
			var progress map[string]interface{}
			if err := json.Unmarshal([]byte(progressJSON.String), &progress); err == nil {
				job.Progress = progress
			}
		}
		if resultJSON.Valid {
			var result map[string]interface{}
			if err := json.Unmarshal([]byte(resultJSON.String), &result); err == nil {
				job.Result = result
			}
		}
		if configJSON.Valid {
			job.ProjectKeys = extractJiraImportProjectKeys(configJSON.String)
		}
		job.ImportedWorkspaceCount, job.ImportedItemCount = h.importJobEntityCounts(job.ID)
		job.ImportedWorkspaces = h.importJobWorkspaces(job.ID)

		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, jobs)
}

func (h *JiraImportHandler) importJobEntityCounts(jobID string) (workspaceCount, itemCount int) {
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM jira_import_id_mappings WHERE job_id = ? AND entity_type = 'workspace'`, jobID).Scan(&workspaceCount); err != nil {
		slog.Warn("Failed to count Jira import workspace mappings", slog.String("component", "jira"), slog.String("job_id", jobID), slog.Any("error", err))
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM jira_import_id_mappings WHERE job_id = ? AND entity_type = 'item'`, jobID).Scan(&itemCount); err != nil {
		slog.Warn("Failed to count Jira import item mappings", slog.String("component", "jira"), slog.String("job_id", jobID), slog.Any("error", err))
	}
	return workspaceCount, itemCount
}

func (h *JiraImportHandler) importJobWorkspaces(jobID string) []ImportedWorkspaceInfo {
	rows, err := h.db.Query(`
		SELECT DISTINCT w.id, w.key, w.name
		FROM jira_import_id_mappings m
		JOIN workspaces w ON w.id = m.windshift_id
		WHERE m.job_id = ? AND m.entity_type = 'workspace'
		ORDER BY w.key
	`, jobID)
	if err != nil {
		slog.Warn("Failed to load Jira import workspace mappings", slog.String("component", "jira"), slog.String("job_id", jobID), slog.Any("error", err))
		return nil
	}
	defer func() { _ = rows.Close() }()

	workspaces := []ImportedWorkspaceInfo{}
	for rows.Next() {
		var ws ImportedWorkspaceInfo
		if err := rows.Scan(&ws.ID, &ws.Key, &ws.Name); err != nil {
			slog.Warn("Failed to scan Jira import workspace mapping", slog.String("component", "jira"), slog.String("job_id", jobID), slog.Any("error", err))
			continue
		}
		workspaces = append(workspaces, ws)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("Failed to iterate Jira import workspace mappings", slog.String("component", "jira"), slog.String("job_id", jobID), slog.Any("error", err))
	}
	return workspaces
}

// StartImport handles POST /api/admin/jira-import/start
// Starts a background import job and returns immediately with the job ID
func (h *JiraImportHandler) StartImport(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeJSON[StartImportRequest](w, r)
	if !ok {
		return
	}

	if req.ConnectionID == "" || len(req.ProjectKeys) == 0 {
		respondValidationError(w, r, "connection_id and project_keys are required")
		return
	}

	if !req.ForceReimport {
		conflicts, err := h.findConflictingJiraImports(req)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if len(conflicts) > 0 {
			err := restapi.NewAPIError(http.StatusConflict, "JIRA_IMPORT_CONFLICT", "One or more selected Jira projects have already been imported. Delete the previous import data or explicitly force a re-import.").WithDetails(map[string]interface{}{
				"conflicting_imports": conflicts,
			})
			respondError(w, r, err)
			return
		}
	}

	// Get user ID from context
	userID := getUserIDFromContext(r)

	// Generate a new job ID
	jobID := generateUUID()

	// Store the configuration as JSON
	configJSON, err := json.Marshal(map[string]interface{}{
		"project_keys":     req.ProjectKeys,
		"open_issues_only": req.OpenIssuesOnly,
		"mappings":         req.Mappings,
		"force_reimport":   req.ForceReimport,
	})
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Create the import job in the database
	_, err = h.db.ExecWrite(`
		INSERT INTO jira_import_jobs (id, connection_id, status, scope, config_json, created_by)
		VALUES (?, ?, 'queued', 'work_items', ?, ?)
	`, jobID, req.ConnectionID, string(configJSON), userID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		_ = logger.LogAudit(h.db, logger.AuditEvent{
			UserID:       currentUser.ID,
			Username:     currentUser.Username,
			IPAddress:    utils.GetClientIP(r),
			UserAgent:    r.UserAgent(),
			ActionType:   logger.ActionJiraImport,
			ResourceType: logger.ResourceJiraImport,
			ResourceName: jobID,
			Details: map[string]interface{}{
				"connection_id": req.ConnectionID,
				"project_keys":  req.ProjectKeys,
			},
			Success: true,
		})
	}

	// Start the import in a background goroutine
	go h.executeImport(jobID, req)

	respondJSONOK(w, StartImportResponse{
		JobID:   jobID,
		Message: "Import started successfully",
	})
}

type jiraImportConflict struct {
	JobID       string     `json:"job_id"`
	Status      string     `json:"status"`
	ProjectKeys []string   `json:"project_keys"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

func (h *JiraImportHandler) findConflictingJiraImports(req StartImportRequest) ([]jiraImportConflict, error) {
	requested := projectKeySet(req.ProjectKeys)
	if len(requested) == 0 || req.ConnectionID == "" {
		return nil, nil
	}

	rows, err := h.db.Query(`
		SELECT id, status, config_json, created_at, completed_at
		FROM jira_import_jobs
		WHERE connection_id = ?
		  AND scope = 'work_items'
		  AND status <> 'data_deleted'
		ORDER BY created_at DESC
	`, req.ConnectionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var conflicts []jiraImportConflict
	for rows.Next() {
		var jobID, status, configJSON string
		var createdAt time.Time
		var completedAt sql.NullTime
		if err := rows.Scan(&jobID, &status, &configJSON, &createdAt, &completedAt); err != nil {
			return nil, err
		}
		projectKeys := extractJiraImportProjectKeys(configJSON)
		if !projectKeysOverlap(requested, projectKeys) {
			continue
		}
		conflict := jiraImportConflict{
			JobID:       jobID,
			Status:      status,
			ProjectKeys: projectKeys,
			CreatedAt:   createdAt,
		}
		if completedAt.Valid {
			conflict.CompletedAt = &completedAt.Time
		}
		conflicts = append(conflicts, conflict)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return conflicts, nil
}

func extractJiraImportProjectKeys(configJSON string) []string {
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil
	}
	rawKeys, ok := config["project_keys"].([]interface{})
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(rawKeys))
	seen := make(map[string]struct{}, len(rawKeys))
	for _, raw := range rawKeys {
		key, ok := raw.(string)
		if !ok {
			continue
		}
		normalized := normalizeJiraProjectKey(key)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		keys = append(keys, normalized)
		seen[normalized] = struct{}{}
	}
	return keys
}

func projectKeysOverlap(requested map[string]struct{}, existing []string) bool {
	for _, key := range existing {
		if _, ok := requested[normalizeJiraProjectKey(key)]; ok {
			return true
		}
	}
	return false
}

func projectKeySet(keys []string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if normalized := normalizeJiraProjectKey(key); normalized != "" {
			set[normalized] = struct{}{}
		}
	}
	return set
}

func normalizeJiraProjectKey(key string) string {
	return strings.ToUpper(strings.TrimSpace(key))
}

func (h *JiraImportHandler) deleteImportedAttachment(attachmentID int) bool {
	var filePath string
	var thumbnailPath sql.NullString
	if err := h.db.QueryRow(`SELECT file_path, thumbnail_path FROM attachments WHERE id = ?`, attachmentID).Scan(&filePath, &thumbnailPath); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("Failed to load imported attachment before deletion", slog.String("component", "jira"), slog.Int("attachmentID", attachmentID), slog.Any("error", err))
		}
		return false
	}

	result, err := h.db.ExecWrite(`DELETE FROM attachments WHERE id = ?`, attachmentID)
	if err != nil {
		slog.Error("Failed to delete imported attachment row", slog.String("component", "jira"), slog.Int("attachmentID", attachmentID), slog.Any("error", err))
		return false
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil || rowsAffected == 0 {
		if err != nil {
			slog.Warn("Failed to verify imported attachment deletion", slog.String("component", "jira"), slog.Int("attachmentID", attachmentID), slog.Any("error", err))
		}
		return false
	}

	h.removeImportedAttachmentFile(filePath)
	if thumbnailPath.Valid && strings.TrimSpace(thumbnailPath.String) != "" {
		h.removeImportedAttachmentFile(thumbnailPath.String)
	}
	return true
}

func (h *JiraImportHandler) removeImportedAttachmentFile(storedPath string) {
	resolvedPath, err := h.resolveImportedAttachmentPath(storedPath)
	if err != nil {
		slog.Warn("Refusing to delete imported attachment file outside storage root", slog.String("component", "jira"), slog.String("filePath", storedPath), slog.Any("error", err))
		return
	}
	if err := os.Remove(resolvedPath); err != nil && !os.IsNotExist(err) { //nolint:gosec // path is validated by resolveImportedAttachmentPath
		slog.Warn("Failed to delete imported attachment file", slog.String("component", "jira"), slog.String("filePath", resolvedPath), slog.Any("error", err))
	}
}

func (h *JiraImportHandler) resolveImportedAttachmentPath(storedPath string) (string, error) {
	root, err := h.currentAttachmentRoot()
	if err != nil {
		return "", err
	}
	return resolvePathWithinRoot(root, storedPath)
}

func (h *JiraImportHandler) currentAttachmentRoot() (string, error) {
	var root string
	err := h.db.QueryRow(`SELECT attachment_path FROM attachment_settings WHERE enabled = true LIMIT 1`).Scan(&root)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(root) == "" {
		return "", errAttachmentPathOutsideRoot
	}
	return root, nil
}

func resolvePathWithinRoot(root, storedPath string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	isInsideRoot := func(candidate string) (string, bool, error) {
		absPath, err := filepath.Abs(candidate)
		if err != nil {
			return "", false, err
		}
		inside := absPath == absRoot || strings.HasPrefix(absPath, absRoot+string(os.PathSeparator))
		return absPath, inside, nil
	}

	if filepath.IsAbs(storedPath) {
		absPath, inside, err := isInsideRoot(storedPath)
		if err != nil {
			return "", err
		}
		if !inside {
			return "", errAttachmentPathOutsideRoot
		}
		return absPath, nil
	}
	if absPath, inside, err := isInsideRoot(storedPath); err != nil {
		return "", err
	} else if inside {
		return absPath, nil
	}
	absPath, inside, err := isInsideRoot(filepath.Join(root, storedPath))
	if err != nil {
		return "", err
	}
	if !inside {
		return "", errAttachmentPathOutsideRoot
	}
	return absPath, nil
}

func shouldSkipReusedJiraImportEntityDelete(db interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}, jobID, entityType string, windshiftID int) bool {
	var metadataJSON sql.NullString
	err := db.QueryRow(`
		SELECT metadata_json
		FROM jira_import_id_mappings
		WHERE job_id = ? AND entity_type = ? AND windshift_id = ?
		LIMIT 1
	`, jobID, entityType, windshiftID).Scan(&metadataJSON)
	if err != nil || !metadataJSON.Valid {
		return false
	}
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(metadataJSON.String), &meta); err != nil {
		return false
	}
	action, _ := meta["action"].(string)
	return action == "reuse_existing"
}

func shouldSkipJiraImportTimeProjectDelete(db interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}, jobID string, timeProjectID int) bool {
	var metadataJSON sql.NullString
	err := db.QueryRow(`
		SELECT metadata_json
		FROM jira_import_id_mappings
		WHERE job_id = ? AND entity_type = 'time_project' AND windshift_id = ?
		LIMIT 1
	`, jobID, timeProjectID).Scan(&metadataJSON)
	if err != nil || !metadataJSON.Valid {
		return false
	}
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(metadataJSON.String), &meta); err != nil {
		return false
	}
	action, _ := meta["action"].(string)
	return action == "reuse_workspace_default"
}

type deleteImportedDataRequest struct {
	ConfirmJobID              string `json:"confirm_job_id"`
	ConfirmWorkspaceCount     int    `json:"confirm_workspace_count"`
	ConfirmDeleteImportedData bool   `json:"confirm_delete_imported_data"`
}

// DeleteImportedData handles DELETE /api/admin/jira-import/jobs/{jobId}/data
// Deletes all entities created during an import job for re-import purposes
func (h *JiraImportHandler) DeleteImportedData(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobId")
	if jobID == "" {
		respondInvalidID(w, r, "jobId")
		return
	}

	req, ok := decodeJSON[deleteImportedDataRequest](w, r)
	if !ok {
		return
	}
	if req.ConfirmJobID != jobID || !req.ConfirmDeleteImportedData {
		respondValidationError(w, r, "Deleting imported Jira data requires confirm_job_id to match the job path and confirm_delete_imported_data=true.")
		return
	}

	var status string
	if err := h.db.QueryRow(`SELECT status FROM jira_import_jobs WHERE id = ?`, jobID).Scan(&status); err != nil {
		respondNotFound(w, r, "job")
		return
	}
	if status == "queued" || status == "running" {
		respondConflict(w, r, "Cannot delete imported data while the import job is queued or running.")
		return
	}

	currentWorkspaceCount, _ := h.importJobEntityCounts(jobID)
	if req.ConfirmWorkspaceCount != currentWorkspaceCount {
		respondValidationError(w, r, fmt.Sprintf("Workspace confirmation count mismatch: request confirmed %d workspace(s), but this import currently maps %d workspace(s). Refresh and try again.", req.ConfirmWorkspaceCount, currentWorkspaceCount))
		return
	}

	// Get all mappings for this job, ordered for proper deletion
	rows, err := h.db.Query(`
		SELECT entity_type, windshift_id
		FROM jira_import_id_mappings
		WHERE job_id = ?
		ORDER BY
			CASE entity_type
				WHEN 'link' THEN 1
				WHEN 'worklog' THEN 2
				WHEN 'comment' THEN 3
				WHEN 'attachment' THEN 4
				WHEN 'item' THEN 5
				WHEN 'asset' THEN 6
				WHEN 'asset_type' THEN 7
				WHEN 'asset_set' THEN 8
				WHEN 'board_configuration' THEN 9
				WHEN 'collection' THEN 10
				WHEN 'iteration' THEN 11
				WHEN 'milestone' THEN 12
				WHEN 'configuration_set' THEN 13
				WHEN 'workflow' THEN 14
				WHEN 'custom_field' THEN 15
				WHEN 'status' THEN 16
				WHEN 'item_type' THEN 17
				WHEN 'time_project' THEN 18
				WHEN 'workspace' THEN 19
				ELSE 20
			END
	`, jobID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	defer func() { _ = rows.Close() }()

	type mapping struct {
		entityType  string
		windshiftID int
	}
	var mappings []mapping
	for rows.Next() {
		var m mapping
		if err = rows.Scan(&m.entityType, &m.windshiftID); err != nil {
			slog.Warn("Failed to scan mapping", slog.String("component", "jira"), slog.Any("error", err))
			continue
		}
		mappings = append(mappings, m)
	}
	if err := rows.Err(); err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Delete entities in order (most dependent first)
	deleted := make(map[string]int)
	for _, m := range mappings {
		var tableName string
		switch m.entityType {
		case "item":
			tableName = "items"
		case "workspace":
			tableName = "workspaces"
		case "asset":
			tableName = "assets"
		case "asset_type":
			if shouldSkipReusedJiraImportEntityDelete(h.db, jobID, "asset_type", m.windshiftID) {
				continue
			}
			tableName = "asset_types"
		case "asset_set":
			if shouldSkipReusedJiraImportEntityDelete(h.db, jobID, "asset_set", m.windshiftID) {
				continue
			}
			tableName = "asset_management_sets"
		case "status":
			tableName = "statuses"
		case "item_type":
			tableName = "item_types"
		case "milestone":
			tableName = "milestones"
		case "custom_field":
			tableName = "custom_fields"
		case "board_configuration":
			tableName = "board_configurations"
		case "collection":
			tableName = "collections"
		case "attachment":
			if h.deleteImportedAttachment(m.windshiftID) {
				deleted[m.entityType]++
			}
			continue
		case "comment":
			tableName = "comments"
		case "link":
			tableName = "item_links"
		case "worklog":
			tableName = "time_worklogs"
		case "iteration":
			tableName = "iterations"
		case "time_project":
			if shouldSkipJiraImportTimeProjectDelete(h.db, jobID, m.windshiftID) {
				continue
			}
			_, _ = h.db.ExecWrite("UPDATE workspaces SET time_project_id = NULL WHERE time_project_id = ?", m.windshiftID)
			tableName = "time_projects"
		case "configuration_set":
			// Delete dependent rows first
			_, _ = h.db.ExecWrite("DELETE FROM workspace_configuration_sets WHERE configuration_set_id = ?", m.windshiftID)
			_, _ = h.db.ExecWrite("DELETE FROM configuration_set_item_types WHERE configuration_set_id = ?", m.windshiftID)
			_, _ = h.db.ExecWrite("DELETE FROM configuration_set_screens WHERE configuration_set_id = ?", m.windshiftID)
			_, _ = h.db.ExecWrite("DELETE FROM configuration_set_priorities WHERE configuration_set_id = ?", m.windshiftID)
			tableName = "configuration_sets"
		case "workflow":
			// Wrap the transition delete in a tx so we can cancel approval
			// requests pinned to the doomed transitions first — otherwise the
			// CASCADE chain from workflow_transitions → approval_set_statuses
			// trips the RESTRICT-FK on approval_requests (SQLite 1811).
			if wfTx, txErr := h.db.Begin(); txErr != nil {
				slog.Error("Failed to begin tx for workflow transitions cleanup", slog.String("component", "jira"), slog.Int("windshiftID", m.windshiftID), slog.Any("error", txErr))
			} else {
				wfTxOK := true
				transitionIDs := []int{}
				if tRows, qErr := wfTx.Query("SELECT id FROM workflow_transitions WHERE workflow_id = ?", m.windshiftID); qErr != nil {
					slog.Error("Failed to load workflow transition ids", slog.String("component", "jira"), slog.Int("windshiftID", m.windshiftID), slog.Any("error", qErr))
					wfTxOK = false
				} else {
					for tRows.Next() {
						var tid int
						if sErr := tRows.Scan(&tid); sErr != nil {
							slog.Error("Failed to scan transition id", slog.String("component", "jira"), slog.Any("error", sErr))
							wfTxOK = false
							break
						}
						transitionIDs = append(transitionIDs, tid)
					}
					if rErr := tRows.Err(); rErr != nil {
						slog.Error("Failed to iterate transition ids", slog.String("component", "jira"), slog.Any("error", rErr))
						wfTxOK = false
					}
					_ = tRows.Close()
				}
				if wfTxOK {
					if _, cancelErr := cancelApprovalRequestsForTransitions(wfTx, transitionIDs); cancelErr != nil {
						slog.Error("Failed to cancel blocking approval_requests", slog.String("component", "jira"), slog.Int("windshiftID", m.windshiftID), slog.Any("error", cancelErr))
						wfTxOK = false
					}
				}
				if wfTxOK {
					if _, delErr := wfTx.Exec("DELETE FROM workflow_transitions WHERE workflow_id = ?", m.windshiftID); delErr != nil {
						slog.Error("Failed to delete workflow_transitions", slog.String("component", "jira"), slog.Int("windshiftID", m.windshiftID), slog.Any("error", delErr))
						wfTxOK = false
					}
				}
				if wfTxOK {
					if cErr := wfTx.Commit(); cErr != nil {
						slog.Error("Failed to commit workflow transitions cleanup", slog.String("component", "jira"), slog.Int("windshiftID", m.windshiftID), slog.Any("error", cErr))
					}
				} else {
					_ = wfTx.Rollback()
				}
			}
			tableName = "workflows"
		default:
			slog.Warn("Unknown entity type", slog.String("component", "jira"), slog.String("entityType", m.entityType))
			continue
		}

		_, err = h.db.ExecWrite(fmt.Sprintf("DELETE FROM %s WHERE id = ?", tableName), m.windshiftID) //nolint:gosec // G201: tableName is from the hardcoded whitelist switch above
		if err != nil {
			slog.Error("Failed to delete entity", slog.String("component", "jira"), slog.String("entityType", m.entityType), slog.Int("windshiftID", m.windshiftID), slog.Any("error", err))
		} else {
			deleted[m.entityType]++
		}
	}

	// Clear the mappings for this job
	_, err = h.db.ExecWrite(`DELETE FROM jira_import_id_mappings WHERE job_id = ?`, jobID)
	if err != nil {
		slog.Error("Failed to delete mappings", slog.String("component", "jira"), slog.Any("error", err))
	}

	// Update job status to indicate data was deleted
	if _, err := h.db.ExecWrite(`
		UPDATE jira_import_jobs
		SET status = 'data_deleted', result_json = ?
		WHERE id = ?
	`, fmt.Sprintf(`{"deleted": %v}`, deleted), jobID); err != nil {
		slog.Warn("failed to update job status after data deletion", slog.String("component", "jira"), slog.String("job_id", jobID), slog.Any("error", err))
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		_ = logger.LogAudit(h.db, logger.AuditEvent{
			UserID:       currentUser.ID,
			Username:     currentUser.Username,
			IPAddress:    utils.GetClientIP(r),
			UserAgent:    r.UserAgent(),
			ActionType:   logger.ActionJiraImportDeleteData,
			ResourceType: logger.ResourceJiraImport,
			ResourceName: jobID,
			Details: map[string]interface{}{
				"job_id":  jobID,
				"deleted": deleted,
			},
			Success: true,
		})
	}

	respondJSONOK(w, map[string]interface{}{
		"success": true,
		"deleted": deleted,
	})
}

// GetPreviousImports handles GET /api/admin/jira-import/previous-imports
// Returns previous imports for the same projects to enable re-import
func (h *JiraImportHandler) GetPreviousImports(w http.ResponseWriter, r *http.Request) {
	projectKeys := r.URL.Query()["project_key"]
	if len(projectKeys) == 0 {
		respondValidationError(w, r, "At least one project_key is required")
		return
	}

	// Query all completed imports and filter by project keys
	rows, err := h.db.Query(`
		SELECT j.id, j.connection_id, j.status, j.config_json, j.created_at, j.completed_at,
		       (SELECT COUNT(*) FROM jira_import_id_mappings m WHERE m.job_id = j.id AND m.entity_type = 'workspace') as workspace_count,
		       (SELECT COUNT(*) FROM jira_import_id_mappings m WHERE m.job_id = j.id AND m.entity_type = 'item') as item_count
		FROM jira_import_jobs j
		WHERE j.status = 'completed'
		ORDER BY j.completed_at DESC
		LIMIT 10
	`)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	defer func() { _ = rows.Close() }()

	type previousImport struct {
		JobID          string     `json:"job_id"`
		ConnectionID   string     `json:"connection_id"`
		Status         string     `json:"status"`
		ProjectKeys    []string   `json:"project_keys"`
		WorkspaceCount int        `json:"workspace_count"`
		ItemCount      int        `json:"item_count"`
		CreatedAt      time.Time  `json:"created_at"`
		CompletedAt    *time.Time `json:"completed_at,omitempty"`
	}

	imports := make([]previousImport, 0)
	for rows.Next() {
		var pi previousImport
		var configJSON string
		var completedAt sql.NullTime

		if err := rows.Scan(&pi.JobID, &pi.ConnectionID, &pi.Status, &configJSON,
			&pi.CreatedAt, &completedAt, &pi.WorkspaceCount, &pi.ItemCount); err != nil {
			slog.Warn("Failed to scan import", slog.String("component", "jira"), slog.Any("error", err))
			continue
		}

		if completedAt.Valid {
			pi.CompletedAt = &completedAt.Time
		}

		// Extract project keys from config
		var config map[string]interface{}
		if err := json.Unmarshal([]byte(configJSON), &config); err == nil {
			if keys, ok := config["project_keys"].([]interface{}); ok {
				for _, k := range keys {
					if str, ok := k.(string); ok {
						pi.ProjectKeys = append(pi.ProjectKeys, str)
					}
				}
			}
		}

		// Check if this import matches any of the requested project keys
		for _, requestedKey := range projectKeys {
			for _, importedKey := range pi.ProjectKeys {
				if requestedKey == importedKey {
					imports = append(imports, pi)
					goto nextRow
				}
			}
		}
	nextRow:
	}
	if err := rows.Err(); err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, imports)
}

// recordMapping records an entity mapping in the database
func (h *JiraImportHandler) recordMapping(jobID, entityType, jiraID, jiraKey string, windshiftID int, metadata map[string]interface{}) {
	metadataJSON := "{}"
	if metadata != nil {
		if data, err := json.Marshal(metadata); err == nil {
			metadataJSON = string(data)
		}
	}

	_, err := h.db.ExecWrite(`
		INSERT INTO jira_import_id_mappings (job_id, entity_type, jira_id, jira_key, windshift_id, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (job_id, entity_type, jira_id) DO UPDATE SET
			windshift_id = excluded.windshift_id,
			metadata_json = excluded.metadata_json
	`, jobID, entityType, jiraID, jiraKey, windshiftID, metadataJSON)
	if err != nil {
		slog.Error("Failed to record mapping", slog.String("component", "jira"), slog.Any("error", err))
	}
}

// updateJobStatus updates the status of an import job
func (h *JiraImportHandler) updateJobStatus(jobID, status, phase string, progress *ImportProgress, errorMessage string) {
	progressJSON := "{}"
	if progress != nil {
		if data, err := json.Marshal(progress); err == nil {
			progressJSON = string(data)
		}
	}

	var query string
	var args []interface{}

	switch status {
	case "running":
		query = `UPDATE jira_import_jobs SET status = ?, phase = ?, progress_json = ?, started_at = CURRENT_TIMESTAMP WHERE id = ?`
		args = []interface{}{status, phase, progressJSON, jobID}
	case "completed", "failed":
		query = `UPDATE jira_import_jobs SET status = ?, phase = ?, progress_json = ?, error_message = ?, completed_at = CURRENT_TIMESTAMP WHERE id = ?`
		args = []interface{}{status, phase, progressJSON, errorMessage, jobID}
	default:
		query = `UPDATE jira_import_jobs SET status = ?, phase = ?, progress_json = ? WHERE id = ?`
		args = []interface{}{status, phase, progressJSON, jobID}
	}

	_, err := h.db.ExecWrite(query, args...)
	if err != nil {
		slog.Error("Failed to update job status", slog.String("component", "jira"), slog.Any("error", err))
	}
}

// updateJobProgress updates just the progress of a running job
func (h *JiraImportHandler) updateJobProgress(jobID string, progress *ImportProgress) {
	progressJSON := "{}"
	if progress != nil {
		if data, err := json.Marshal(progress); err == nil {
			progressJSON = string(data)
		}
	}

	_, err := h.db.ExecWrite(`
		UPDATE jira_import_jobs SET phase = ?, progress_json = ? WHERE id = ?
	`, progress.Phase, progressJSON, jobID)
	if err != nil {
		slog.Error("Failed to update job progress", slog.String("component", "jira"), slog.Any("error", err))
	}
}

// generateUUID generates a UUID for job IDs
func generateUUID() string {
	return uuid.New().String()
}
