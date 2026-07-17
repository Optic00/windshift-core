package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"windshift/internal/database"
)

type portalSectionReferenceKind string

const (
	portalRequestTypeReference portalSectionReferenceKind = "request_type"
	portalAssetReportReference portalSectionReferenceKind = "asset_report"
)

func removePortalSectionReference(ctx context.Context, tx database.Tx, channelID, resourceID int, kind portalSectionReferenceKind) error {
	// Serialize against every other channel-config writer that takes the row
	// lock, and make SQLite acquire its write lock before reading the JSON we
	// will replace. Without this, a concurrent config save can be overwritten
	// by the read/modify/write cleanup below even though deletion uses a tx.
	result, err := tx.ExecContext(ctx, "UPDATE channels SET updated_at = updated_at WHERE id = ?", channelID)
	if err != nil {
		return fmt.Errorf("lock channel config for portal-section cleanup: %w", err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil {
		return fmt.Errorf("count locked channels for portal-section cleanup: %w", rowsErr)
	} else if rows == 0 {
		return ErrNotFound
	}

	var raw string
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(config, '') FROM channels WHERE id = ?", channelID).Scan(&raw); err != nil {
		return notFoundOrWrap(err, "load channel config for portal-section cleanup")
	}
	if raw == "" {
		return nil
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return fmt.Errorf("parse channel config for portal-section cleanup: %w", err)
	}
	if cfg == nil {
		return nil
	}
	rawSections, exists := cfg["portal_sections"]
	if !exists {
		return nil
	}
	var sections []map[string]json.RawMessage
	if err := json.Unmarshal(rawSections, &sections); err != nil {
		return fmt.Errorf("parse portal sections for cleanup: %w", err)
	}
	fieldName := "request_type_ids"
	if kind == portalAssetReportReference {
		fieldName = "asset_report_ids"
	}
	modified := false
	for i := range sections {
		rawIDs, ok := sections[i][fieldName]
		if !ok {
			continue
		}
		var source []int
		if err := json.Unmarshal(rawIDs, &source); err != nil {
			return fmt.Errorf("parse %s for portal-section cleanup: %w", fieldName, err)
		}
		filtered := make([]int, 0, len(source))
		for _, id := range source {
			if id == resourceID {
				modified = true
				continue
			}
			filtered = append(filtered, id)
		}
		if len(filtered) != len(source) {
			encodedIDs, err := json.Marshal(filtered)
			if err != nil {
				return fmt.Errorf("marshal %s for portal-section cleanup: %w", fieldName, err)
			}
			sections[i][fieldName] = encodedIDs
		}
	}
	if !modified {
		return nil
	}
	encodedSections, err := json.Marshal(sections)
	if err != nil {
		return fmt.Errorf("marshal portal sections for cleanup: %w", err)
	}
	cfg["portal_sections"] = encodedSections
	updated, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal channel config for portal-section cleanup: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE channels SET config = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", string(updated), channelID); err != nil {
		return fmt.Errorf("save portal-section cleanup: %w", err)
	}
	return nil
}
