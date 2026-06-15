package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"windshift/internal/jira"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

const jiraAssetsPageSize = 100

func (h *JiraImportHandler) importJiraAssets(ctx context.Context, jobID string, client jira.Client, createdByUserID int) {
	schemas, err := client.ListObjectSchemas(ctx)
	if err != nil {
		if errors.Is(err, jira.ErrAssetsNotAvailable) {
			slog.Debug("Jira Assets API not available; skipping Assets import", slog.String("component", "jira"))
			return
		}
		slog.Warn("Failed to list Jira Assets schemas", slog.String("component", "jira"), slog.Any("error", err))
		return
	}

	for _, schema := range schemas {
		setID, ok := h.ensureJiraAssetSet(jobID, schema, createdByUserID)
		if !ok {
			continue
		}
		typeMap := h.ensureJiraAssetTypes(ctx, jobID, client, setID, schema)
		for objectTypeID, importedType := range typeMap {
			h.importJiraAssetObjectsForType(ctx, jobID, client, setID, importedType.AssetTypeID, schema.ID, objectTypeID, importedType.AttributeFieldIDs)
		}
	}
}

func (h *JiraImportHandler) ensureJiraAssetSet(jobID string, schema jira.AssetObjectSchema, createdByUserID int) (int, bool) {
	name := strings.TrimSpace(schema.Name)
	if name == "" {
		name = strings.TrimSpace(schema.ObjectSchemaKey)
	}
	if name == "" {
		name = "Jira Assets " + schema.ID
	}
	name = "Jira Assets: " + name

	action := "reuse_existing"
	var setID int
	err := h.db.QueryRow(`SELECT id FROM asset_management_sets WHERE name = ?`, name).Scan(&setID)
	if errors.Is(err, sql.ErrNoRows) {
		var createdBy any
		if createdByUserID > 0 {
			createdBy = createdByUserID
		}
		var newID int64
		err = h.db.QueryRow(`
			INSERT INTO asset_management_sets (name, description, is_default, created_by, created_at, updated_at)
			VALUES (?, ?, false, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
		`, name, strings.TrimSpace(schema.Description), createdBy).Scan(&newID)
		setID = int(newID)
		if err == nil {
			action = "create"
			h.ensureJiraAssetDefaultStatus(setID)
			h.grantJiraAssetSetAdmin(setID, createdByUserID)
		}
	}
	if err != nil {
		slog.Warn("Failed to ensure Jira asset set", slog.String("component", "jira"), slog.String("schemaID", schema.ID), slog.Any("error", err))
		return 0, false
	}

	h.recordMapping(jobID, "asset_set", schema.ID, schema.ObjectSchemaKey, setID, map[string]any{
		"schema_name":  schema.Name,
		"object_count": schema.ObjectCount,
		"action":       action,
	})
	return setID, true
}

func (h *JiraImportHandler) ensureJiraAssetDefaultStatus(setID int) int {
	var statusID int
	err := h.db.QueryRow(`SELECT id FROM asset_statuses WHERE set_id = ? AND is_default = true ORDER BY id LIMIT 1`, setID).Scan(&statusID)
	if err == nil {
		return statusID
	}
	if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("Failed to load default asset status", slog.String("component", "jira"), slog.Int("setID", setID), slog.Any("error", err))
		return 0
	}
	var newID int64
	if err := h.db.QueryRow(`
		INSERT INTO asset_statuses (set_id, name, color, description, is_default, display_order, created_at, updated_at)
		VALUES (?, 'Active', '#22c55e', 'Default status for imported Jira Assets', true, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
	`, setID).Scan(&newID); err != nil {
		slog.Warn("Failed to create default asset status", slog.String("component", "jira"), slog.Int("setID", setID), slog.Any("error", err))
		return 0
	}
	return int(newID)
}

func (h *JiraImportHandler) grantJiraAssetSetAdmin(setID, userID int) {
	if userID <= 0 {
		return
	}
	var roleID int
	if err := h.db.QueryRow(`SELECT id FROM asset_roles WHERE name = 'Administrator'`).Scan(&roleID); err != nil {
		return
	}
	_, err := h.db.ExecWrite(`
		INSERT INTO user_asset_set_roles (user_id, set_id, role_id, granted_by)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, set_id) DO NOTHING
	`, userID, setID, roleID, userID)
	if err != nil {
		slog.Warn("Failed to grant Jira asset set admin role", slog.String("component", "jira"), slog.Int("setID", setID), slog.Int("userID", userID), slog.Any("error", err))
	}
}

type jiraAssetTypeImport struct {
	AssetTypeID       int
	AttributeFieldIDs map[string]int
}

type jiraIssueAssetReference struct {
	AssetID  int
	SetID    int
	Title    string
	AssetTag string
}

func (h *JiraImportHandler) singleImportedJiraAssetSetID(jobID string) (int, bool) {
	rows, err := h.db.Query(`
		SELECT DISTINCT windshift_id
		FROM jira_import_id_mappings
		WHERE job_id = ? AND entity_type = 'asset_set'
	`, jobID)
	if err != nil {
		return 0, false
	}
	defer func() { _ = rows.Close() }()

	setID := 0
	count := 0
	for rows.Next() {
		if err := rows.Scan(&setID); err != nil {
			return 0, false
		}
		count++
		if count > 1 {
			return 0, false
		}
	}
	if rows.Err() != nil || count != 1 {
		return 0, false
	}
	return setID, true
}

func (h *JiraImportHandler) resolveJiraIssueAssetReferences(jobID string, value any) []jiraIssueAssetReference {
	candidates := jiraIssueAssetCandidates(value)
	if len(candidates) == 0 {
		return nil
	}
	refs := make([]jiraIssueAssetReference, 0, len(candidates))
	seen := make(map[int]struct{}, len(candidates))
	for _, candidate := range candidates {
		ref, ok := h.resolveJiraIssueAssetReference(jobID, candidate)
		if !ok {
			continue
		}
		if _, exists := seen[ref.AssetID]; exists {
			continue
		}
		seen[ref.AssetID] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

type jiraIssueAssetCandidate struct {
	ID    string
	Key   string
	Label string
}

func jiraIssueAssetCandidates(value any) []jiraIssueAssetCandidate {
	var candidates []jiraIssueAssetCandidate
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []interface{}:
			for _, entry := range x {
				walk(entry)
			}
		case map[string]interface{}:
			candidate := jiraIssueAssetCandidate{
				ID:    firstStringKey(x, "id", "objectId", "objectID"),
				Key:   firstStringKey(x, "objectKey", "key", "globalId", "workspaceId"),
				Label: firstStringKey(x, "label", "name", "displayValue", "value"),
			}
			if candidate.ID != "" || candidate.Key != "" {
				candidates = append(candidates, candidate)
				return
			}
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(value)
	return candidates
}

func (h *JiraImportHandler) resolveJiraIssueAssetReference(jobID string, candidate jiraIssueAssetCandidate) (jiraIssueAssetReference, bool) {
	if candidate.ID == "" && candidate.Key == "" {
		return jiraIssueAssetReference{}, false
	}

	args := []any{jobID}
	where := "job_id = ? AND entity_type = 'asset' AND ("
	if candidate.ID != "" {
		where += "jira_id = ?"
		args = append(args, candidate.ID)
	}
	if candidate.Key != "" {
		if candidate.ID != "" {
			where += " OR "
		}
		where += "jira_key = ?"
		args = append(args, candidate.Key)
	}
	where += ")"

	query := fmt.Sprintf(`
		SELECT m.windshift_id, COALESCE(m.metadata_json, ''), a.title, COALESCE(a.asset_tag, '')
		FROM jira_import_id_mappings m
		JOIN assets a ON a.id = m.windshift_id
		WHERE %s
		ORDER BY CASE WHEN m.jira_id = ? THEN 0 ELSE 1 END, m.id
		LIMIT 1
	`, where)
	args = append(args, candidate.ID)

	var ref jiraIssueAssetReference
	var metadataJSON string
	if err := h.db.QueryRow(query, args...).Scan(&ref.AssetID, &metadataJSON, &ref.Title, &ref.AssetTag); err != nil {
		return jiraIssueAssetReference{}, false
	}
	if strings.TrimSpace(ref.Title) == "" {
		ref.Title = candidate.Label
	}
	if metadataJSON != "" {
		var meta map[string]any
		if json.Unmarshal([]byte(metadataJSON), &meta) == nil {
			if setID, ok := numericMetadataInt(meta["asset_set_id"]); ok {
				ref.SetID = setID
			}
		}
	}
	if ref.SetID == 0 {
		_ = h.db.QueryRow(`SELECT set_id FROM assets WHERE id = ?`, ref.AssetID).Scan(&ref.SetID)
	}
	return ref, ref.AssetID > 0
}

func numericMetadataInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (h *JiraImportHandler) ensureJiraAssetTypes(ctx context.Context, jobID string, client jira.Client, setID int, schema jira.AssetObjectSchema) map[string]jiraAssetTypeImport {
	result := make(map[string]jiraAssetTypeImport)
	objectTypes, err := client.ListObjectTypes(ctx, schema.ID)
	if err != nil {
		slog.Warn("Failed to list Jira asset object types", slog.String("component", "jira"), slog.String("schemaID", schema.ID), slog.Any("error", err))
		return result
	}

	for _, objectType := range objectTypes {
		if objectType.AbstractObjectType {
			continue
		}
		assetTypeID, ok := h.ensureJiraAssetType(jobID, setID, objectType)
		if !ok {
			continue
		}
		attrFieldIDs := make(map[string]int)

		attrs, err := client.GetObjectTypeAttributes(ctx, objectType.ID)
		if err != nil {
			slog.Warn("Failed to load Jira asset object type attributes", slog.String("component", "jira"), slog.String("objectTypeID", objectType.ID), slog.Any("error", err))
			continue
		}
		for _, attr := range attrs {
			fieldID, ok := h.ensureJiraAssetAttributeField(objectType, attr)
			if ok {
				h.linkJiraAssetTypeField(assetTypeID, fieldID, attr)
				attrFieldIDs[attr.ID] = fieldID
			}
		}
		result[objectType.ID] = jiraAssetTypeImport{AssetTypeID: assetTypeID, AttributeFieldIDs: attrFieldIDs}
	}
	return result
}

func (h *JiraImportHandler) ensureJiraAssetType(jobID string, setID int, objectType jira.AssetObjectType) (int, bool) {
	name := strings.TrimSpace(objectType.Name)
	if name == "" {
		name = "Jira Object Type " + objectType.ID
	}
	action := "reuse_existing"
	var typeID int
	err := h.db.QueryRow(`SELECT id FROM asset_types WHERE set_id = ? AND name = ?`, setID, name).Scan(&typeID)
	if errors.Is(err, sql.ErrNoRows) {
		var newID int64
		err = h.db.QueryRow(`
			INSERT INTO asset_types (set_id, name, description, icon, color, display_order, is_active, created_at, updated_at)
			VALUES (?, ?, ?, 'Box', '#3b82f6', ?, true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
		`, setID, name, strings.TrimSpace(objectType.Description), objectType.Position).Scan(&newID)
		typeID = int(newID)
		if err == nil {
			action = "create"
		}
	}
	if err != nil {
		slog.Warn("Failed to ensure Jira asset type", slog.String("component", "jira"), slog.String("objectTypeID", objectType.ID), slog.Any("error", err))
		return 0, false
	}
	h.recordMapping(jobID, "asset_type", objectType.ID, name, typeID, map[string]any{"asset_set_id": setID, "action": action})
	return typeID, true
}

func (h *JiraImportHandler) ensureJiraAssetAttributeField(objectType jira.AssetObjectType, attr jira.AssetObjectAttribute) (int, bool) {
	name := strings.TrimSpace(attr.Name)
	if name == "" || attr.Hidden {
		return 0, false
	}
	fieldName := fmt.Sprintf("Jira Assets %s: %s", strings.TrimSpace(objectType.Name), name)
	fieldType := jiraAssetAttributeFieldType(attr)

	var fieldID int
	err := h.db.QueryRow(`SELECT id FROM custom_field_definitions WHERE LOWER(name) = LOWER(?) AND field_type = ?`, fieldName, fieldType).Scan(&fieldID)
	if errors.Is(err, sql.ErrNoRows) {
		var newID int64
		err = h.db.QueryRow(`
			INSERT INTO custom_field_definitions (name, field_type, description, required, options, display_order,
			                                      applies_to_portal_customers, applies_to_customer_organisations,
			                                      created_at, updated_at)
			VALUES (?, ?, ?, ?, '', ?, false, false, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
		`, fieldName, fieldType, strings.TrimSpace(attr.Description), attr.MinimumCardinality > 0, attr.Position).Scan(&newID)
		fieldID = int(newID)
	}
	if err != nil {
		slog.Warn("Failed to ensure Jira asset attribute field", slog.String("component", "jira"), slog.String("attributeID", attr.ID), slog.Any("error", err))
		return 0, false
	}
	return fieldID, true
}

func (h *JiraImportHandler) linkJiraAssetTypeField(assetTypeID, fieldID int, attr jira.AssetObjectAttribute) {
	_, err := h.db.ExecWrite(`
		INSERT INTO asset_type_fields (asset_type_id, custom_field_id, is_required, display_order)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(asset_type_id, custom_field_id) DO UPDATE SET
			is_required = excluded.is_required,
			display_order = excluded.display_order
	`, assetTypeID, fieldID, attr.MinimumCardinality > 0, attr.Position)
	if err != nil {
		slog.Warn("Failed to link Jira asset type field", slog.String("component", "jira"), slog.Int("assetTypeID", assetTypeID), slog.Int("fieldID", fieldID), slog.Any("error", err))
	}
}

func (h *JiraImportHandler) importJiraAssetObjectsForType(ctx context.Context, jobID string, client jira.Client, setID, assetTypeID int, schemaID, objectTypeID string, attributeFields map[string]int) {
	statusID := h.ensureJiraAssetDefaultStatus(setID)
	for page := 1; ; page++ {
		result, err := client.SearchObjects(ctx, jira.ObjectSearchOptions{
			ObjectSchemaID:    schemaID,
			ObjectTypeID:      objectTypeID,
			Page:              page,
			PageSize:          jiraAssetsPageSize,
			IncludeAttributes: true,
		})
		if err != nil {
			slog.Warn("Failed to search Jira asset objects", slog.String("component", "jira"), slog.String("schemaID", schemaID), slog.String("objectTypeID", objectTypeID), slog.Int("page", page), slog.Any("error", err))
			return
		}
		if result == nil || len(result.ObjectEntries) == 0 {
			return
		}
		for _, object := range result.ObjectEntries {
			h.importJiraAssetObject(jobID, setID, assetTypeID, statusID, attributeFields, object)
		}
		if result.IsLast || len(result.ObjectEntries) < jiraAssetsPageSize {
			return
		}
	}
}

func (h *JiraImportHandler) importJiraAssetObject(jobID string, setID, assetTypeID, statusID int, attributeFields map[string]int, object jira.AssetObject) {
	if object.ID == "" {
		return
	}
	if existingID := h.existingImportedJiraAsset(jobID, object.ID); existingID > 0 {
		h.recordMapping(jobID, "asset", object.ID, object.ObjectKey, existingID, map[string]any{"action": "reuse_existing_mapping"})
		return
	}

	customValues := make(map[string]any)
	for _, attr := range object.Attributes {
		fieldID := attributeFields[attr.ObjectTypeAttributeID]
		if fieldID == 0 {
			continue
		}
		if value, ok := jiraAssetAttributeValue(attr); ok {
			customValues[strconv.Itoa(fieldID)] = value
		}
	}

	// External Jira attribute display values are untrusted — every string
	// gets at least the rich-text strip + length cap, then the asset CF
	// text pass (the same one CreateAsset/UpdateAsset apply, WI-319) lays
	// the rendering-matched policy over text/textarea-typed fields. Both
	// passes are idempotent no-ops on plain text.
	for key, v := range customValues {
		if s, ok := v.(string); ok {
			customValues[key] = sanitize.RichText.Sanitize(s)
		}
	}
	if err := services.NewAssetService(h.db, repository.NewAssetRepository(h.db)).SanitizeCustomFieldTextValues(assetTypeID, customValues); err != nil {
		slog.Warn("Failed to sanitize Jira asset custom field values", slog.String("component", "jira"), slog.String("objectID", object.ID), slog.Any("error", err))
		return
	}

	customJSON := "{}"
	if len(customValues) > 0 {
		if b, err := json.Marshal(customValues); err == nil {
			customJSON = string(b)
		}
	}

	// object.Label / ObjectKey are external display strings — apply the
	// same per-column policies sanitizeAssetText runs on the normal asset
	// create path (PlainTextField title, RichText description,
	// ShortIdentifier asset_tag) before they reach the INSERT.
	title := sanitize.PlainTextField.Sanitize(object.Label)
	if title == "" {
		title = sanitize.PlainTextField.Sanitize(object.ObjectKey)
	}
	if title == "" {
		title = sanitize.PlainTextField.Sanitize("Jira Asset " + object.ID)
	}
	assetTag := sanitize.ShortIdentifier.Sanitize(object.ObjectKey)
	description := sanitize.RichText.Sanitize(fmt.Sprintf("Imported from Jira Assets object %s", object.ObjectKey))

	createdAt := nullableAssetTime(object.Created)
	updatedAt := nullableAssetTime(object.Updated)
	var status any
	if statusID > 0 {
		status = statusID
	}
	var assetID int64
	err := h.db.QueryRow(`
		INSERT INTO assets (set_id, asset_type_id, status_id, title, description, asset_tag, custom_field_values, import_job_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP), COALESCE(?, CURRENT_TIMESTAMP)) RETURNING id
	`, setID, assetTypeID, status, title, description, assetTag, customJSON, jobID, createdAt, updatedAt).Scan(&assetID)
	if err != nil {
		slog.Warn("Failed to import Jira asset object", slog.String("component", "jira"), slog.String("objectID", object.ID), slog.String("objectKey", object.ObjectKey), slog.Any("error", err))
		return
	}

	h.recordMapping(jobID, "asset", object.ID, object.ObjectKey, int(assetID), map[string]any{
		"asset_set_id":  setID,
		"asset_type_id": assetTypeID,
		"label":         object.Label,
	})
}

func (h *JiraImportHandler) existingImportedJiraAsset(jobID, objectID string) int {
	var id int
	if err := h.db.QueryRow(`
		SELECT windshift_id FROM jira_import_id_mappings
		WHERE job_id = ? AND entity_type = 'asset' AND jira_id = ?
	`, jobID, objectID).Scan(&id); err == nil {
		return id
	}
	return 0
}

func jiraAssetAttributeFieldType(attr jira.AssetObjectAttribute) string {
	if attr.Type != 0 {
		return "textarea"
	}
	switch attr.DefaultTypeID {
	case 1, 3:
		return "number"
	case 4, 5:
		return "date"
	default:
		return "textarea"
	}
}

func jiraAssetAttributeValue(attr jira.AssetObjectAttributeValue) (any, bool) {
	if len(attr.ObjectAttributeValues) == 0 {
		return nil, false
	}
	values := make([]string, 0, len(attr.ObjectAttributeValues))
	for _, raw := range attr.ObjectAttributeValues {
		if raw.DisplayValue != "" {
			values = append(values, raw.DisplayValue)
			continue
		}
		if raw.SearchValue != "" {
			values = append(values, raw.SearchValue)
			continue
		}
		if raw.User != nil && raw.User.DisplayName != "" {
			values = append(values, raw.User.DisplayName)
			continue
		}
		if raw.Status != nil && raw.Status.Name != "" {
			values = append(values, raw.Status.Name)
			continue
		}
		if raw.Value != nil {
			values = append(values, fmt.Sprint(raw.Value))
		}
	}
	if len(values) == 0 {
		return nil, false
	}
	return strings.Join(values, "\n"), true
}

func nullableAssetTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
