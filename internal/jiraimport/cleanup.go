package jiraimport

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"windshift/internal/repository"
)

var errAttachmentPathOutsideRoot = errors.New("attachment path is outside configured storage root")

type mapping struct {
	entityType   string
	windshiftID  int
	metadataJSON sql.NullString
}

// DeleteImportedData enforces the provenance boundary and removes only records
// this job explicitly owns. Unknown or malformed provenance never authorizes a
// destructive operation.
func (s *Service) DeleteImportedData(jobID string, confirmedWorkspaceCount int) (map[string]int, error) {
	var status string
	err := s.db.QueryRow(`SELECT status FROM jira_import_jobs WHERE id = ?`, jobID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrJobNotFound
	}
	if err != nil {
		return nil, err
	}
	if status == "queued" || status == "running" {
		return nil, ErrJobActive
	}
	currentWorkspaceCount, _ := s.EntityCounts(jobID)
	if confirmedWorkspaceCount != currentWorkspaceCount {
		return nil, &WorkspaceCountMismatchError{Confirmed: confirmedWorkspaceCount, Current: currentWorkspaceCount}
	}

	mappings, err := s.cleanupMappings(jobID)
	if err != nil {
		return nil, err
	}
	deleted := make(map[string]int)
	for _, item := range mappings {
		s.deleteMapping(jobID, item, deleted)
	}
	if _, err := s.db.ExecWrite(`DELETE FROM jira_import_id_mappings WHERE job_id = ?`, jobID); err != nil {
		return nil, err
	}
	resultJSON, err := json.Marshal(map[string]any{"deleted": deleted})
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecWrite(`
		UPDATE jira_import_jobs SET status = 'data_deleted', result_json = ? WHERE id = ?
	`, string(resultJSON), jobID); err != nil {
		return nil, err
	}
	return deleted, nil
}

func (s *Service) cleanupMappings(jobID string) ([]mapping, error) {
	rows, err := s.db.Query(`
		SELECT entity_type, windshift_id, metadata_json
		FROM jira_import_id_mappings
		WHERE job_id = ?
		ORDER BY CASE entity_type
			WHEN 'link' THEN 1 WHEN 'external_issue_link' THEN 2 WHEN 'watch' THEN 3
			WHEN 'worklog' THEN 4 WHEN 'comment' THEN 5 WHEN 'attachment' THEN 6
			WHEN 'test_case' THEN 7 WHEN 'item' THEN 8
			WHEN 'portal_customer_channel' THEN 9 WHEN 'portal_customer_role' THEN 10
			WHEN 'request_type' THEN 11 WHEN 'portal_customer' THEN 12
			WHEN 'customer_organisation' THEN 13 WHEN 'portal' THEN 14
			WHEN 'asset' THEN 15 WHEN 'asset_category' THEN 16 WHEN 'asset_status' THEN 17
			WHEN 'asset_type' THEN 18 WHEN 'asset_set' THEN 19
			WHEN 'board_configuration' THEN 20 WHEN 'collection' THEN 21
			WHEN 'iteration' THEN 22 WHEN 'milestone' THEN 23
			WHEN 'configuration_set' THEN 24 WHEN 'screen' THEN 25 WHEN 'workflow' THEN 26
			WHEN 'custom_field' THEN 27 WHEN 'status' THEN 28 WHEN 'item_type' THEN 29
			WHEN 'time_project' THEN 30 WHEN 'workspace' THEN 31 ELSE 32 END
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []mapping
	for rows.Next() {
		var item mapping
		if err := rows.Scan(&item.entityType, &item.windshiftID, &item.metadataJSON); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) deleteMapping(jobID string, item mapping, deleted map[string]int) {
	if item.entityType != "portal_customer" && !MappingWasCreated(item.metadataJSON) {
		return
	}
	var tableName string
	switch item.entityType {
	case "item":
		tableName = "items"
	case "test_case":
		tableName = "test_cases"
	case "workspace":
		tableName = "workspaces"
	case "request_type":
		if s.reusedEntity(jobID, item.entityType, item.windshiftID) {
			return
		}
		tableName = "request_types"
	case "portal_customer_channel":
		channelID, ok := mappingMetadataInt(item.metadataJSON, "channel_id")
		if !ok {
			return
		}
		s.deletePair("portal_customer_channels", "portal_customer_id", item.windshiftID, "channel_id", channelID, item.entityType, deleted)
		return
	case "portal_customer_role":
		roleID, ok := mappingMetadataInt(item.metadataJSON, "contact_role_id")
		if !ok {
			return
		}
		s.deletePair("portal_customer_roles", "portal_customer_id", item.windshiftID, "contact_role_id", roleID, item.entityType, deleted)
		return
	case "portal_customer":
		if !MappingWasCreated(item.metadataJSON) {
			s.restorePortalCustomerOrganisation(item)
			return
		}
		tableName = "portal_customers"
	case "customer_organisation":
		if s.reusedEntity(jobID, item.entityType, item.windshiftID) {
			return
		}
		tableName = "customer_organisations"
	case "portal":
		if s.reusedEntity(jobID, item.entityType, item.windshiftID) {
			return
		}
		tableName = "channels"
	case "asset":
		tableName = "assets"
	case "asset_category":
		tableName = "asset_categories"
	case "asset_status":
		tableName = "asset_statuses"
	case "asset_type":
		if s.reusedEntity(jobID, item.entityType, item.windshiftID) {
			return
		}
		tableName = "asset_types"
	case "asset_set":
		if s.reusedEntity(jobID, item.entityType, item.windshiftID) {
			return
		}
		tableName = "asset_management_sets"
	case "status":
		tableName = "statuses"
	case "item_type":
		tableName = "item_types"
	case "milestone":
		tableName = "milestones"
	case "custom_field":
		tableName = "custom_field_definitions"
	case "board_configuration":
		tableName = "board_configurations"
	case "collection":
		tableName = "collections"
	case "attachment":
		if s.deleteAttachment(item.windshiftID) {
			deleted[item.entityType]++
		}
		return
	case "comment":
		tableName = "comments"
	case "link":
		tableName = "item_links"
	case "external_issue_link":
		linkID, ok := mappingMetadataString(item.metadataJSON, "integration_link_id")
		if !ok {
			return
		}
		if _, err := s.db.ExecWrite(`DELETE FROM item_integration_links WHERE id = ?`, linkID); err == nil {
			deleted[item.entityType]++
		}
		return
	case "watch":
		userID, ok := mappingMetadataInt(item.metadataJSON, "user_id")
		if !ok {
			return
		}
		if _, err := s.db.ExecWrite(`
			UPDATE item_watches SET is_active = false, updated_at = CURRENT_TIMESTAMP
			WHERE user_id = ? AND item_id = ?
		`, userID, item.windshiftID); err == nil {
			deleted[item.entityType]++
		}
		return
	case "worklog":
		tableName = "time_worklogs"
	case "iteration":
		tableName = "iterations"
	case "time_project":
		if s.reusedWorkspaceTimeProject(jobID, item.windshiftID) {
			return
		}
		_, _ = s.db.ExecWrite("UPDATE workspaces SET time_project_id = NULL WHERE time_project_id = ?", item.windshiftID)
		tableName = "time_projects"
	case "configuration_set":
		_, _ = s.db.ExecWrite("DELETE FROM workspace_configuration_sets WHERE configuration_set_id = ?", item.windshiftID)
		_, _ = s.db.ExecWrite("DELETE FROM configuration_set_item_types WHERE configuration_set_id = ?", item.windshiftID)
		_, _ = s.db.ExecWrite("DELETE FROM configuration_set_screens WHERE configuration_set_id = ?", item.windshiftID)
		_, _ = s.db.ExecWrite("DELETE FROM configuration_set_priorities WHERE configuration_set_id = ?", item.windshiftID)
		tableName = "configuration_sets"
	case "screen":
		tableName = "screens"
	case "workflow":
		s.deleteWorkflowTransitions(item.windshiftID)
		tableName = "workflows"
	default:
		slog.Warn("unknown Jira import mapping entity type", slog.String("entity_type", item.entityType))
		return
	}
	if _, err := s.db.ExecWrite(fmt.Sprintf("DELETE FROM %s WHERE id = ?", tableName), item.windshiftID); err != nil { //nolint:gosec // tableName is selected from the fixed whitelist above.
		slog.Error("failed to delete imported Jira entity", slog.String("entity_type", item.entityType), slog.Int("windshift_id", item.windshiftID), slog.Any("error", err))
		return
	}
	deleted[item.entityType]++
}

func (s *Service) deletePair(table, firstColumn string, firstID int, secondColumn string, secondID int, entityType string, deleted map[string]int) {
	query := fmt.Sprintf("DELETE FROM %s WHERE %s = ? AND %s = ?", table, firstColumn, secondColumn) //nolint:gosec // all identifiers are fixed callers.
	if _, err := s.db.ExecWrite(query, firstID, secondID); err == nil {
		deleted[entityType]++
	}
}

func (s *Service) restorePortalCustomerOrganisation(item mapping) {
	assigned, _ := mappingMetadataBool(item.metadataJSON, "organization_was_assigned")
	if !assigned {
		return
	}
	previousID, _ := mappingMetadataInt(item.metadataJSON, "previous_customer_organisation_id")
	if previousID > 0 {
		_, _ = s.db.ExecWrite(`
			UPDATE portal_customers SET customer_organisation_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?
		`, previousID, item.windshiftID)
		return
	}
	_, _ = s.db.ExecWrite(`
		UPDATE portal_customers SET customer_organisation_id = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?
	`, item.windshiftID)
}

func (s *Service) reusedEntity(jobID, entityType string, windshiftID int) bool {
	var metadata sql.NullString
	err := s.db.QueryRow(`
		SELECT metadata_json FROM jira_import_id_mappings
		WHERE job_id = ? AND entity_type = ? AND windshift_id = ? LIMIT 1
	`, jobID, entityType, windshiftID).Scan(&metadata)
	if err != nil {
		return false
	}
	action, _ := mappingMetadata(metadata)["action"].(string)
	return action == "reuse_existing"
}

func (s *Service) reusedWorkspaceTimeProject(jobID string, windshiftID int) bool {
	var metadata sql.NullString
	err := s.db.QueryRow(`
		SELECT metadata_json FROM jira_import_id_mappings
		WHERE job_id = ? AND entity_type = 'time_project' AND windshift_id = ? LIMIT 1
	`, jobID, windshiftID).Scan(&metadata)
	if err != nil {
		return false
	}
	action, _ := mappingMetadata(metadata)["action"].(string)
	return action == "reuse_workspace_default"
}

func (s *Service) deleteWorkflowTransitions(workflowID int) {
	tx, err := s.db.Begin()
	if err != nil {
		slog.Error("failed to begin Jira workflow cleanup", slog.Int("workflow_id", workflowID), slog.Any("error", err))
		return
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query("SELECT id FROM workflow_transitions WHERE workflow_id = ?", workflowID)
	if err != nil {
		return
	}
	var transitionIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return
		}
		transitionIDs = append(transitionIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return
	}
	_ = rows.Close()
	if _, err := repository.CancelApprovalRequestsForTransitions(tx, transitionIDs); err != nil {
		return
	}
	if _, err := tx.Exec("DELETE FROM workflow_transitions WHERE workflow_id = ?", workflowID); err != nil {
		return
	}
	if err := tx.Commit(); err != nil {
		slog.Error("failed to commit Jira workflow cleanup", slog.Int("workflow_id", workflowID), slog.Any("error", err))
	}
}

func (s *Service) deleteAttachment(attachmentID int) bool {
	var filePath string
	var thumbnailPath sql.NullString
	if err := s.db.QueryRow(`
		SELECT file_path, thumbnail_path FROM attachments WHERE id = ?
	`, attachmentID).Scan(&filePath, &thumbnailPath); err != nil {
		return false
	}
	result, err := s.db.ExecWrite(`DELETE FROM attachments WHERE id = ?`, attachmentID)
	if err != nil {
		return false
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false
	}
	s.removeAttachmentFile(filePath)
	if thumbnailPath.Valid && strings.TrimSpace(thumbnailPath.String) != "" {
		s.removeAttachmentFile(thumbnailPath.String)
	}
	return true
}

func (s *Service) removeAttachmentFile(storedPath string) {
	var root string
	if err := s.db.QueryRow(`
		SELECT attachment_path FROM attachment_settings WHERE enabled = true LIMIT 1
	`).Scan(&root); err != nil {
		return
	}
	path, err := resolvePathWithinRoot(root, storedPath)
	if err != nil {
		slog.Warn("refusing to delete imported attachment outside storage root", slog.String("path", storedPath), slog.Any("error", err))
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) { //nolint:gosec // path is confined to the configured attachment root.
		slog.Warn("failed to delete imported attachment file", slog.String("path", path), slog.Any("error", err))
	}
}

func resolvePathWithinRoot(root, storedPath string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errAttachmentPathOutsideRoot
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	inside := func(candidate string) (string, bool, error) {
		absPath, err := filepath.Abs(candidate)
		if err != nil {
			return "", false, err
		}
		return absPath, absPath == absRoot || strings.HasPrefix(absPath, absRoot+string(os.PathSeparator)), nil
	}
	if filepath.IsAbs(storedPath) {
		path, ok, err := inside(storedPath)
		if err != nil || !ok {
			return "", errAttachmentPathOutsideRoot
		}
		return path, nil
	}
	if path, ok, err := inside(storedPath); err == nil && ok {
		return path, nil
	}
	path, ok, err := inside(filepath.Join(root, storedPath))
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errAttachmentPathOutsideRoot
	}
	return path, nil
}
