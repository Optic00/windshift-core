package services

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"windshift/internal/database"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/utils"
)

// AuditActor carries the actor + transport context an audit event needs.
// Both the cookie-auth and bearer-auth surfaces build this from their
// *http.Request before calling into AssetService so the service layer
// stays HTTP-agnostic and the two surfaces produce identical audit rows
// for equivalent operations.
type AuditActor struct {
	UserID    int
	Username  string
	IPAddress string
	UserAgent string
}

// NewAuditActorFromRequest extracts the audit fields from a request +
// authenticated user. Convenience shared by both surfaces.
func NewAuditActorFromRequest(r *http.Request, user *models.User) AuditActor {
	if user == nil {
		return AuditActor{IPAddress: utils.GetClientIP(r), UserAgent: r.UserAgent()}
	}
	return AuditActor{
		UserID:    user.ID,
		Username:  user.Username,
		IPAddress: utils.GetClientIP(r),
		UserAgent: r.UserAgent(),
	}
}

// AssetValidationError signals a user-facing validation failure (400 at
// the HTTP layer) — as opposed to repo / IO errors which the caller
// renders as 500. Handlers use errors.As to switch on it.
type AssetValidationError struct{ Msg string }

func (e *AssetValidationError) Error() string { return e.Msg }

// IsAssetValidationError reports whether err is (or wraps) an
// AssetValidationError. Handlers use this when rendering 400 vs 500.
func IsAssetValidationError(err error) (*AssetValidationError, bool) {
	var ve *AssetValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}

// AssetService owns the asset mutation pipeline: repo writes, audit
// emission, automation-event emission, and custom-field schema
// validation. Both /api/assets (cookie auth) and /rest/api/v1/assets
// (bearer auth) flow through here so a single audit row + a single
// automation event is produced per mutation, regardless of which
// surface drove it.
type AssetService struct {
	db   database.Database
	repo *repository.AssetRepository
	// actionService is set lazily via SetActionService after the asset
	// action service is constructed (its dependencies — EventCoordinator,
	// NotificationService — aren't available at startup-init time). Nil
	// means automation events are silently skipped, which is intentional
	// for very early boot and tests that don't exercise automation.
	actionService atomic.Pointer[AssetActionService]
}

// NewAssetService constructs an AssetService backed by the given asset
// repository. The asset action service can be attached later via
// SetActionService.
func NewAssetService(db database.Database, repo *repository.AssetRepository) *AssetService {
	return &AssetService{db: db, repo: repo}
}

// SetActionService attaches an AssetActionService for automation event
// emission. Safe to call once after boot; subsequent calls overwrite.
func (s *AssetService) SetActionService(a *AssetActionService) {
	s.actionService.Store(a)
}

func (s *AssetService) actions() *AssetActionService {
	return s.actionService.Load()
}

// ValidateCustomFieldsSchema rejects keys in values that aren't declared
// on the asset type. Accepts both legacy field-id-string keys (the UI
// surface uses these) and field-name keys (CSV import normalizes to
// these). Type/option enforcement is deliberately out of scope for this
// pass — name validation is the minimum bar called for by the asset API
// v1 security review (finding 4).
func (s *AssetService) ValidateCustomFieldsSchema(assetTypeID int, values map[string]interface{}) error {
	if len(values) == 0 {
		return nil
	}
	fields, err := s.repo.FindAssetTypeFields(assetTypeID)
	if err != nil {
		return fmt.Errorf("load asset type fields: %w", err)
	}
	allowed := make(map[string]bool, len(fields)*2)
	for _, f := range fields {
		allowed[fmt.Sprintf("%d", f.CustomFieldID)] = true
		allowed[strings.ToLower(f.FieldName)] = true
	}
	var unknown []string
	for k := range values {
		if allowed[k] {
			continue
		}
		if allowed[strings.ToLower(k)] {
			continue
		}
		unknown = append(unknown, k)
	}
	if len(unknown) > 0 {
		return &AssetValidationError{
			Msg: "custom_field_values contains key(s) not declared on the asset type: " + strings.Join(unknown, ", "),
		}
	}
	return nil
}

// CreateAsset writes the asset, validates custom field schema, emits the
// audit event, and emits an asset_created automation event when an
// action service is wired. Returns the freshly-loaded row.
func (s *AssetService) CreateAsset(actor AuditActor, in repository.CreateAssetInput, customFieldValues map[string]interface{}) (*models.Asset, error) {
	if err := s.ValidateCustomFieldsSchema(in.AssetTypeID, customFieldValues); err != nil {
		return nil, err
	}
	assetID, err := s.repo.CreateAsset(in)
	if err != nil {
		return nil, fmt.Errorf("create asset: %w", err)
	}
	s.emitAudit(actor, logger.ActionAssetCreate, &assetID, in.Title, nil)
	if a := s.actions(); a != nil {
		a.EmitAssetActionEvent(&models.AssetActionEvent{
			EventType:   models.AssetTriggerAssetCreated,
			SetID:       in.SetID,
			AssetID:     assetID,
			ActorUserID: actor.UserID,
			NewValues: map[string]interface{}{
				"title":         in.Title,
				"asset_type_id": in.AssetTypeID,
				"status_id":     in.StatusID,
			},
		})
	}
	row, err := s.repo.FindAssetFullByID(assetID)
	if err != nil {
		return nil, fmt.Errorf("reload after create: %w", err)
	}
	m := repository.AssetRowToModel(*row)
	return &m, nil
}

// UpdateAsset writes the (partial) update, validates the custom-field
// schema, emits the audit event, and emits asset_updated +
// asset_status_changed automation events when applicable. oldSnap (read
// from repo.GetAssetUpdateSnapshot before the call) is used to detect
// the status transition.
func (s *AssetService) UpdateAsset(actor AuditActor, assetID int, oldSnap repository.AssetUpdateSnapshot, in repository.UpdateAssetInput, customFieldValues map[string]interface{}) (*models.Asset, error) {
	if err := s.ValidateCustomFieldsSchema(in.AssetTypeID, customFieldValues); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateAsset(assetID, in); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("update asset: %w", err)
	}
	s.emitAudit(actor, logger.ActionAssetUpdate, &assetID, in.Title, nil)
	if a := s.actions(); a != nil {
		oldSID := 0
		if oldSnap.StatusID.Valid {
			oldSID = int(oldSnap.StatusID.Int64)
		}
		newSID := 0
		if in.StatusID != nil {
			newSID = *in.StatusID
		}
		if oldSID != newSID {
			a.EmitAssetActionEvent(&models.AssetActionEvent{
				EventType:   models.AssetTriggerAssetStatusChanged,
				SetID:       oldSnap.SetID,
				AssetID:     assetID,
				ActorUserID: actor.UserID,
				OldValues:   map[string]interface{}{"status_id": oldSID},
				NewValues:   map[string]interface{}{"status_id": newSID},
			})
		}
		a.EmitAssetActionEvent(&models.AssetActionEvent{
			EventType:   models.AssetTriggerAssetUpdated,
			SetID:       oldSnap.SetID,
			AssetID:     assetID,
			ActorUserID: actor.UserID,
			NewValues: map[string]interface{}{
				"title":         in.Title,
				"asset_type_id": in.AssetTypeID,
				"status_id":     in.StatusID,
			},
		})
	}
	row, err := s.repo.FindAssetFullByID(assetID)
	if err != nil {
		return nil, fmt.Errorf("reload after update: %w", err)
	}
	m := repository.AssetRowToModel(*row)
	return &m, nil
}

// DeleteAsset resolves the title via GetAssetSetAndTitle (so the audit
// row carries human-readable context post-delete), removes the asset +
// its item_links rows, and emits the audit event + an
// asset_deleted automation event.
func (s *AssetService) DeleteAsset(actor AuditActor, assetID int) error {
	setID, title, err := s.repo.GetAssetSetAndTitle(assetID)
	if errors.Is(err, repository.ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("load asset for delete: %w", err)
	}
	if err := s.repo.DeleteAssetWithLinks(assetID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return err
		}
		return fmt.Errorf("delete asset: %w", err)
	}
	s.emitAudit(actor, logger.ActionAssetDelete, &assetID, title, nil)
	if a := s.actions(); a != nil {
		a.EmitAssetActionEvent(&models.AssetActionEvent{
			EventType:   models.AssetTriggerAssetDeleted,
			SetID:       setID,
			AssetID:     assetID,
			ActorUserID: actor.UserID,
			OldValues:   map[string]interface{}{"title": title},
		})
	}
	return nil
}

// ImportCSVDefaults carries the optional column defaults that apply to
// every row of a sync CSV import.
type ImportCSVDefaults struct {
	StatusID   *int
	CategoryID *int
}

// ImportCSVSummary is the aggregate result of a sync CSV import.
type ImportCSVSummary struct {
	SetID         int
	AssetTypeID   int
	TotalRows     int
	ProcessedRows int
	CreatedRows   int
	ErrorRows     int
	Status        string
	ErrorMessage  string
	StartedAt     time.Time
	CompletedAt   time.Time
}

// ImportAssetsCSV parses csvBody as a CSV with a header row, then creates
// one asset per data row. Header columns "title" / "description" /
// "asset_tag"|"tag" map to built-in fields; every other header is
// matched case-insensitively against the asset type's declared custom
// field names. Rows missing a non-empty title are counted as errors but
// don't abort the import.
//
// Emits one aggregate audit row at the end (mirroring the cookie-auth
// async-import pattern, which audits the job, not each inserted row).
// Per-row audit would balloon the trail without changing what an
// investigator can reconstruct.
func (s *AssetService) ImportAssetsCSV(actor AuditActor, setID, assetTypeID int, defaults ImportCSVDefaults, csvBody io.Reader, filename string) (*ImportCSVSummary, error) {
	fields, err := s.repo.FindAssetTypeFields(assetTypeID)
	if err != nil {
		return nil, fmt.Errorf("load asset type fields: %w", err)
	}
	fieldByName := make(map[string]string, len(fields))
	for _, f := range fields {
		fieldByName[strings.ToLower(f.FieldName)] = f.FieldName
	}

	reader := csv.NewReader(csvBody)
	reader.FieldsPerRecord = -1
	headers, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &AssetValidationError{Msg: "CSV is empty"}
		}
		return nil, &AssetValidationError{Msg: fmt.Sprintf("CSV parse error: %v", err)}
	}

	summary := &ImportCSVSummary{
		SetID:       setID,
		AssetTypeID: assetTypeID,
		Status:      "running",
		StartedAt:   time.Now().UTC(),
	}
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			summary.TotalRows++
			summary.ProcessedRows++
			summary.ErrorRows++
			continue
		}
		summary.TotalRows++
		summary.ProcessedRows++

		row := buildCSVRow(headers, record, fieldByName)
		title := strings.TrimSpace(row.title)
		if title == "" {
			summary.ErrorRows++
			continue
		}
		cfJSON, _ := encodeCustomFieldValuesJSON(row.customFields)
		if _, err := s.repo.CreateAsset(repository.CreateAssetInput{
			SetID:                 setID,
			AssetTypeID:           assetTypeID,
			CategoryID:            defaults.CategoryID,
			StatusID:              defaults.StatusID,
			Title:                 title,
			Description:           row.description,
			AssetTag:              row.assetTag,
			CustomFieldValuesJSON: cfJSON,
			CreatedBy:             actor.UserID,
			CreatedAt:             time.Now().UTC(),
		}); err != nil {
			summary.ErrorRows++
			continue
		}
		summary.CreatedRows++
	}
	summary.CompletedAt = time.Now().UTC()
	switch {
	case summary.TotalRows == 0:
		summary.Status = "empty"
		summary.ErrorMessage = "no data rows in CSV"
	case summary.ErrorRows == 0:
		summary.Status = "succeeded"
	case summary.CreatedRows == 0:
		summary.Status = "failed"
	default:
		summary.Status = "partial"
	}

	s.emitAudit(actor, logger.ActionAssetCreate, nil, "csv_import:"+filename, map[string]interface{}{
		"source":        "csv_import_sync",
		"set_id":        setID,
		"asset_type_id": assetTypeID,
		"total":         summary.TotalRows,
		"created":       summary.CreatedRows,
		"errors":        summary.ErrorRows,
		"status":        summary.Status,
	})
	return summary, nil
}

// emitAudit writes a success-row to the audit trail. Best-effort — the
// underlying logger.LogAudit already swallows + slog-warns marshal
// failures, and an audit-write failure should never fail the mutation
// it's recording.
func (s *AssetService) emitAudit(actor AuditActor, action string, resourceID *int, resourceName string, extra map[string]interface{}) {
	_ = logger.LogAudit(s.db, logger.AuditEvent{
		UserID:       actor.UserID,
		Username:     actor.Username,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		ActionType:   action,
		ResourceType: logger.ResourceAsset,
		ResourceID:   resourceID,
		ResourceName: resourceName,
		Details:      extra,
		Success:      true,
	})
}

// csvRow holds the field values for a single CSV row, split by where
// they route on the asset model.
type csvRow struct {
	title        string
	description  string
	assetTag     string
	customFields map[string]interface{}
}

// buildCSVRow walks the CSV record against its header row and routes
// each cell to either a built-in column or a custom field on the type,
// matched case-insensitively by header name.
func buildCSVRow(headers, record []string, customFieldByName map[string]string) csvRow {
	row := csvRow{customFields: map[string]interface{}{}}
	for i, h := range headers {
		if i >= len(record) {
			break
		}
		key := strings.ToLower(strings.TrimSpace(h))
		val := strings.TrimSpace(record[i])
		switch key {
		case "title":
			row.title = val
		case "description":
			row.description = val
		case "asset_tag", "tag":
			row.assetTag = val
		default:
			if canonical, ok := customFieldByName[key]; ok && val != "" {
				row.customFields[canonical] = val
			}
		}
	}
	return row
}

// encodeCustomFieldValuesJSON marshals the values map for storage.
// Returns nil for nil / empty maps so the column stores NULL rather
// than "null" or "{}".
func encodeCustomFieldValuesJSON(m map[string]interface{}) (*string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	s := string(b)
	return &s, nil
}
