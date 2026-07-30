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

	setNames := jiraAssetSetNames(schemas)
	var pendingReferences []jiraPendingAssetReference
	for _, schema := range schemas {
		setID, ok := h.ensureJiraAssetSet(jobID, schema, setNames[schema.ID], createdByUserID)
		if !ok {
			continue
		}
		typeMap := h.ensureJiraAssetTypes(ctx, jobID, client, setID, schema)
		for objectTypeID, importedType := range typeMap {
			pendingReferences = append(pendingReferences, h.importJiraAssetObjectsForType(
				ctx,
				jobID,
				client,
				setID,
				importedType.AssetTypeID,
				importedType.CategoryID,
				schema.ID,
				objectTypeID,
				importedType.Attributes,
				importedType.AttributeFieldIDs,
			)...)
		}
	}
	h.resolveJiraAssetReferences(jobID, pendingReferences)
}

func jiraAssetSetNames(schemas []jira.AssetObjectSchema) map[string]string {
	baseNameCounts := make(map[string]int, len(schemas))
	for _, schema := range schemas {
		baseNameCounts[jiraAssetSchemaBaseName(schema)]++
	}

	names := make(map[string]string, len(schemas))
	usedNames := make(map[string]struct{}, len(schemas))
	for _, schema := range schemas {
		baseName := jiraAssetSchemaBaseName(schema)
		if baseNameCounts[baseName] > 1 {
			identity := strings.TrimSpace(schema.ObjectSchemaKey)
			if identity == "" {
				identity = schema.ID
			}
			baseName += " (" + identity + ")"
		}
		name := "Jira Assets: " + baseName
		if _, exists := usedNames[name]; exists {
			name += " [" + schema.ID + "]"
		}
		usedNames[name] = struct{}{}
		names[schema.ID] = name
	}
	return names
}

func jiraAssetSchemaBaseName(schema jira.AssetObjectSchema) string {
	if name := strings.TrimSpace(schema.Name); name != "" {
		return name
	}
	if key := strings.TrimSpace(schema.ObjectSchemaKey); key != "" {
		return key
	}
	return "Jira Assets " + schema.ID
}

func (h *JiraImportHandler) ensureJiraAssetSet(jobID string, schema jira.AssetObjectSchema, name string, createdByUserID int) (int, bool) {
	if strings.TrimSpace(name) == "" {
		name = "Jira Assets: " + jiraAssetSchemaBaseName(schema)
	}

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
	CategoryID        int
	Attributes        map[string]jira.AssetObjectAttribute
	AttributeFieldIDs map[string]int
}

type jiraPendingAssetReference struct {
	AssetID     int
	SetID       int
	FieldID     int
	AttributeID string
	Multiple    bool
	Values      []jira.AssetAttributeValue
}

type jiraIssueAssetReference struct {
	AssetID  int
	SetID    int
	Title    string
	AssetTag string
}

func (h *JiraImportHandler) importedJiraAssetSetID(jobID, schemaID string) (int, bool) {
	var setID int
	err := h.db.QueryRow(`
		SELECT windshift_id
		FROM jira_import_id_mappings
		WHERE job_id = ? AND entity_type = 'asset_set' AND jira_id = ?
		ORDER BY id
		LIMIT 1
	`, jobID, strings.TrimSpace(schemaID)).Scan(&setID)
	return setID, err == nil && setID > 0
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
				// Cloud issue payloads include both id="<workspace UUID>:<id>"
				// and objectId="<id>". Asset import mappings use the latter.
				ID:    firstStringKey(x, "objectId", "objectID", "id"),
				Key:   firstStringKey(x, "objectKey", "key", "globalId"),
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

	categoryIDs := h.ensureJiraAssetTypeCategories(jobID, setID, objectTypes)
	for _, objectType := range objectTypes {
		if objectType.AbstractObjectType {
			continue
		}
		assetTypeID, ok := h.ensureJiraAssetType(jobID, setID, objectType)
		if !ok {
			continue
		}
		attrFieldIDs := make(map[string]int)
		attrsByID := make(map[string]jira.AssetObjectAttribute)

		attrs, err := client.GetObjectTypeAttributes(ctx, objectType.ID)
		if err != nil {
			slog.Warn("Failed to load Jira asset object type attributes", slog.String("component", "jira"), slog.String("objectTypeID", objectType.ID), slog.Any("error", err))
			continue
		}
		for _, attr := range attrs {
			attrsByID[attr.ID] = attr
			fieldID, ok := h.ensureJiraAssetAttributeField(jobID, setID, objectType, attr)
			if ok {
				h.linkJiraAssetTypeField(assetTypeID, fieldID, attr)
				attrFieldIDs[attr.ID] = fieldID
			}
		}
		result[objectType.ID] = jiraAssetTypeImport{
			AssetTypeID:       assetTypeID,
			CategoryID:        categoryIDs[objectType.ID],
			Attributes:        attrsByID,
			AttributeFieldIDs: attrFieldIDs,
		}
	}
	return result
}

func (h *JiraImportHandler) ensureJiraAssetTypeCategories(
	jobID string,
	setID int,
	objectTypes []jira.AssetObjectType,
) map[string]int {
	result := make(map[string]int, len(objectTypes))
	pending := append([]jira.AssetObjectType{}, objectTypes...)
	for len(pending) > 0 {
		next := make([]jira.AssetObjectType, 0, len(pending))
		progress := false
		for _, objectType := range pending {
			parentID := 0
			if objectType.ParentObjectTypeID != "" {
				var parentReady bool
				parentID, parentReady = result[objectType.ParentObjectTypeID]
				if !parentReady {
					next = append(next, objectType)
					continue
				}
			}
			categoryID, ok := h.ensureJiraAssetTypeCategory(jobID, setID, parentID, objectType)
			if ok {
				result[objectType.ID] = categoryID
			}
			progress = true
		}
		if progress {
			pending = next
			continue
		}
		// A missing or cyclic Jira parent must not drop the remaining object
		// types. Preserve them as roots and retain the unresolved parent ID in
		// mapping metadata for the fidelity report.
		for _, objectType := range next {
			categoryID, ok := h.ensureJiraAssetTypeCategory(jobID, setID, 0, objectType)
			if ok {
				result[objectType.ID] = categoryID
			}
		}
		break
	}
	return result
}

func (h *JiraImportHandler) ensureJiraAssetTypeCategory(
	jobID string,
	setID, parentID int,
	objectType jira.AssetObjectType,
) (int, bool) {
	name := strings.TrimSpace(objectType.Name)
	if name == "" {
		name = "Jira Object Type " + objectType.ID
	}
	var parent any
	if parentID > 0 {
		parent = parentID
	}
	action := "reuse_existing"
	var categoryID int
	err := h.db.QueryRow(`
		SELECT id FROM asset_categories
		WHERE set_id = ? AND name = ?
		  AND ((parent_id IS NULL AND ? IS NULL) OR parent_id = ?)
		ORDER BY id LIMIT 1
	`, setID, name, parent, parent).Scan(&categoryID)
	if errors.Is(err, sql.ErrNoRows) {
		repo := repository.NewAssetRepository(h.db)
		var parentPtr *int
		if parentID > 0 {
			parentPtr = &parentID
		}
		categoryID, _, err = repo.CreateAssetCategory(repository.CreateAssetCategoryInput{
			SetID:       setID,
			Name:        name,
			Description: strings.TrimSpace(objectType.Description),
			ParentID:    parentPtr,
		})
		if err == nil {
			action = "create"
		}
	}
	if err != nil {
		slog.Warn("Failed to ensure Jira asset type category",
			slog.String("component", "jira"),
			slog.String("objectTypeID", objectType.ID),
			slog.Any("error", err))
		return 0, false
	}
	h.recordMapping(jobID, "asset_category", objectType.ID, name, categoryID, map[string]any{
		"asset_set_id":               setID,
		"jira_parent_object_type_id": objectType.ParentObjectTypeID,
		"abstract":                   objectType.AbstractObjectType,
		"action":                     action,
	})
	return categoryID, true
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

func (h *JiraImportHandler) ensureJiraAssetAttributeField(jobID string, setID int, objectType jira.AssetObjectType, attr jira.AssetObjectAttribute) (int, bool) {
	name := strings.TrimSpace(attr.Name)
	if name == "" || attr.Hidden {
		return 0, false
	}
	fieldName := fmt.Sprintf("Jira Assets %s: %s", strings.TrimSpace(objectType.Name), name)
	fieldType := jiraAssetAttributeFieldType(attr)
	options := ""
	if fieldType == "asset" {
		optionsBytes, _ := json.Marshal(map[string]any{
			"asset_set_id": setID,
			"multi":        attr.MaximumCardinality != 1,
		})
		options = string(optionsBytes)
	}

	action := "reuse_existing"
	var fieldID int
	err := h.db.QueryRow(`SELECT id FROM custom_field_definitions WHERE LOWER(name) = LOWER(?) AND field_type = ?`, fieldName, fieldType).Scan(&fieldID)
	if errors.Is(err, sql.ErrNoRows) {
		var newID int64
		err = h.db.QueryRow(`
			INSERT INTO custom_field_definitions (name, field_type, description, required, options, display_order,
			                                      applies_to_portal_customers, applies_to_customer_organisations,
			                                      created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, false, false, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
		`, fieldName, fieldType, strings.TrimSpace(attr.Description), attr.MinimumCardinality > 0, options, attr.Position).Scan(&newID)
		fieldID = int(newID)
		if err == nil {
			action = "create"
		}
	}
	if err != nil {
		slog.Warn("Failed to ensure Jira asset attribute field", slog.String("component", "jira"), slog.String("attributeID", attr.ID), slog.Any("error", err))
		return 0, false
	}
	h.recordMapping(jobID, "custom_field", attr.ID, fieldName, fieldID, map[string]any{
		"asset_attribute": true,
		"asset_set_id":    setID,
		"object_type_id":  objectType.ID,
		"jira_type":       attr.Type,
		"action":          action,
	})
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

func (h *JiraImportHandler) importJiraAssetObjectsForType(
	ctx context.Context,
	jobID string,
	client jira.Client,
	setID, assetTypeID, categoryID int,
	schemaID, objectTypeID string,
	attributes map[string]jira.AssetObjectAttribute,
	attributeFields map[string]int,
) []jiraPendingAssetReference {
	var pending []jiraPendingAssetReference
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
			return pending
		}
		if result == nil || len(result.ObjectEntries) == 0 {
			return pending
		}
		for _, object := range result.ObjectEntries {
			pending = append(pending, h.importJiraAssetObject(
				ctx,
				jobID,
				client,
				setID,
				assetTypeID,
				categoryID,
				attributes,
				attributeFields,
				object,
			)...)
		}
		if result.IsLast || len(result.ObjectEntries) < jiraAssetsPageSize {
			return pending
		}
	}
}

func (h *JiraImportHandler) importJiraAssetObject(
	ctx context.Context,
	jobID string,
	client jira.Client,
	setID, assetTypeID, categoryID int,
	attributes map[string]jira.AssetObjectAttribute,
	attributeFields map[string]int,
	object jira.AssetObject,
) []jiraPendingAssetReference {
	if object.ID == "" {
		return nil
	}
	if existingID := h.existingImportedJiraAsset(jobID, object.ID); existingID > 0 {
		h.recordMapping(jobID, "asset", object.ID, object.ObjectKey, existingID, map[string]any{"action": "reuse_existing_mapping"})
		return nil
	}

	customValues := make(map[string]any)
	statusID := h.ensureJiraAssetDefaultStatus(setID)
	userMap := h.ensureJiraAssetAttributeUsers(ctx, jobID, client, object)
	var pendingAttributes []jiraPendingAssetReference
	for _, attr := range object.Attributes {
		fieldID := attributeFields[attr.ObjectTypeAttributeID]
		if fieldID == 0 {
			continue
		}
		definition := attributes[attr.ObjectTypeAttributeID]
		for _, raw := range attr.ObjectAttributeValues {
			if raw.Status != nil {
				if mappedStatusID := h.ensureJiraAssetStatus(jobID, setID, *raw.Status); mappedStatusID > 0 {
					statusID = mappedStatusID
				}
				break
			}
		}
		switch jiraAssetAttributeFieldType(definition) {
		case "asset":
			if rawValue, ok := jiraAssetAttributeValue(attr); ok {
				customValues["_jira_asset_attribute_"+attr.ObjectTypeAttributeID] = rawValue
			}
			pendingAttributes = append(pendingAttributes, jiraPendingAssetReference{
				SetID:       setID,
				FieldID:     fieldID,
				AttributeID: attr.ObjectTypeAttributeID,
				Multiple:    definition.MaximumCardinality != 1,
				Values:      attr.ObjectAttributeValues,
			})
			continue
		case "user":
			if value, ok := jiraAssetUserAttributeValue(attr, userMap); ok {
				customValues[strconv.Itoa(fieldID)] = value
			} else if rawValue, rawOK := jiraAssetAttributeValue(attr); rawOK {
				customValues["_jira_asset_attribute_"+attr.ObjectTypeAttributeID] = rawValue
			}
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
		return nil
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
		INSERT INTO assets (set_id, asset_type_id, category_id, status_id, title, description, asset_tag, custom_field_values, import_job_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, CURRENT_TIMESTAMP), COALESCE(?, CURRENT_TIMESTAMP)) RETURNING id
	`, setID, assetTypeID, nullablePositiveInt(categoryID), status, title, description, assetTag, customJSON, jobID, createdAt, updatedAt).Scan(&assetID)
	if err != nil {
		slog.Warn("Failed to import Jira asset object", slog.String("component", "jira"), slog.String("objectID", object.ID), slog.String("objectKey", object.ObjectKey), slog.Any("error", err))
		return nil
	}

	h.recordMapping(jobID, "asset", object.ID, object.ObjectKey, int(assetID), map[string]any{
		"asset_set_id":  setID,
		"asset_type_id": assetTypeID,
		"category_id":   categoryID,
		"label":         object.Label,
	})
	for idx := range pendingAttributes {
		pendingAttributes[idx].AssetID = int(assetID)
	}
	return pendingAttributes
}

func nullablePositiveInt(value int) any {
	if value > 0 {
		return value
	}
	return nil
}

func (h *JiraImportHandler) ensureJiraAssetAttributeUsers(
	ctx context.Context,
	jobID string,
	client jira.Client,
	object jira.AssetObject,
) map[string]int {
	var users []JiraUserSummary
	seen := make(map[string]bool)
	for _, attr := range object.Attributes {
		for _, raw := range attr.ObjectAttributeValues {
			addJiraUserSummaryFromUser(raw.User, nil, &users, seen)
		}
	}
	if len(users) == 0 {
		return nil
	}
	userMap, _, err := h.ensureUsers(ctx, jobID, users, client)
	if err != nil {
		slog.Warn("Failed to ensure Jira Assets user attributes",
			slog.String("component", "jira"),
			slog.String("objectID", object.ID),
			slog.Any("error", err))
		return nil
	}
	return userMap
}

func jiraAssetUserAttributeValue(attr jira.AssetObjectAttributeValue, userMap map[string]int) (any, bool) {
	for _, raw := range attr.ObjectAttributeValues {
		if raw.User == nil {
			continue
		}
		if userID := userMap[raw.User.GetIdentifier()]; userID > 0 {
			return userID, true
		}
	}
	return nil, false
}

func (h *JiraImportHandler) ensureJiraAssetStatus(jobID string, setID int, status jira.AssetStatus) int {
	name := strings.TrimSpace(status.Name)
	if name == "" {
		return 0
	}
	action := "reuse_existing"
	var statusID int
	err := h.db.QueryRow(`
		SELECT id FROM asset_statuses WHERE set_id = ? AND LOWER(name) = LOWER(?)
		ORDER BY id LIMIT 1
	`, setID, name).Scan(&statusID)
	if errors.Is(err, sql.ErrNoRows) {
		color := "#22c55e"
		switch status.Category {
		case 0:
			color = "#ef4444"
		case 2:
			color = "#f59e0b"
		}
		var newID int64
		err = h.db.QueryRow(`
			INSERT INTO asset_statuses
			       (set_id, name, color, description, is_default, display_order, created_at, updated_at)
			VALUES (?, ?, ?, ?, false, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			RETURNING id
		`, setID, name, color, strings.TrimSpace(status.Description), status.Category).Scan(&newID)
		statusID = int(newID)
		if err == nil {
			action = "create"
		}
	}
	if err != nil {
		slog.Warn("Failed to ensure Jira asset status",
			slog.String("component", "jira"),
			slog.String("jiraStatusID", status.ID),
			slog.Any("error", err))
		return 0
	}
	jiraStatusID := status.ID
	if jiraStatusID == "" {
		jiraStatusID = name
	}
	h.recordMapping(jobID, "asset_status", jiraStatusID, name, statusID, map[string]any{
		"asset_set_id": setID,
		"category":     status.Category,
		"action":       action,
	})
	return statusID
}

func (h *JiraImportHandler) resolveJiraAssetReferences(
	jobID string,
	pending []jiraPendingAssetReference,
) {
	for _, reference := range pending {
		refs := make([]jiraIssueAssetReference, 0, len(reference.Values))
		seen := make(map[int]struct{}, len(reference.Values))
		for _, raw := range reference.Values {
			candidate := jiraAssetAttributeReferenceCandidate(raw)
			resolved, ok := h.resolveJiraIssueAssetReference(jobID, candidate)
			if !ok || resolved.SetID != reference.SetID {
				continue
			}
			if _, exists := seen[resolved.AssetID]; exists {
				continue
			}
			seen[resolved.AssetID] = struct{}{}
			refs = append(refs, resolved)
		}
		if len(refs) < len(reference.Values) {
			h.recordMapping(
				jobID,
				"fidelity_finding",
				fmt.Sprintf("asset:%d:attribute:%s", reference.AssetID, reference.AttributeID),
				"Unresolved Jira Assets references",
				reference.AssetID,
				map[string]any{
					"code":           "jira_asset_reference_unresolved",
					"severity":       "warning",
					"disposition":    "preserved_raw",
					"source_count":   len(reference.Values),
					"resolved_count": len(refs),
					"asset_id":       reference.AssetID,
					"attribute_id":   reference.AttributeID,
					"was_created":    false,
				},
			)
		}
		if len(refs) == 0 {
			continue
		}
		var rawJSON string
		if err := h.db.QueryRow(`
			SELECT COALESCE(custom_field_values, '{}') FROM assets WHERE id = ?
		`, reference.AssetID).Scan(&rawJSON); err != nil {
			continue
		}
		values := make(map[string]any)
		if err := json.Unmarshal([]byte(rawJSON), &values); err != nil {
			values = make(map[string]any)
		}
		fieldKey := strconv.Itoa(reference.FieldID)
		if reference.Multiple {
			mapped := make([]map[string]any, 0, len(refs))
			for _, ref := range refs {
				mapped = append(mapped, assetCustomFieldValue(ref))
			}
			values[fieldKey] = mapped
		} else {
			values[fieldKey] = assetCustomFieldValue(refs[0])
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			continue
		}
		if _, err := h.db.ExecWrite(`
			UPDATE assets SET custom_field_values = ?, updated_at = updated_at WHERE id = ?
		`, string(encoded), reference.AssetID); err != nil {
			slog.Warn("Failed to resolve Jira Assets object reference",
				slog.String("component", "jira"),
				slog.Int("assetID", reference.AssetID),
				slog.String("attributeID", reference.AttributeID),
				slog.Any("error", err))
		}
	}
}

func jiraAssetAttributeReferenceCandidate(raw jira.AssetAttributeValue) jiraIssueAssetCandidate {
	candidate := jiraIssueAssetCandidate{
		Key:   strings.TrimSpace(raw.SearchValue),
		Label: strings.TrimSpace(raw.DisplayValue),
	}
	switch value := raw.Value.(type) {
	case map[string]interface{}:
		candidate.ID = firstStringKey(value, "objectId", "objectID", "id")
		if key := firstStringKey(value, "objectKey", "key", "globalId"); key != "" {
			candidate.Key = key
		}
		if label := firstStringKey(value, "label", "name", "displayValue", "value"); label != "" {
			candidate.Label = label
		}
	case string:
		candidate.ID = strings.TrimSpace(value)
	case float64:
		candidate.ID = strconv.FormatInt(int64(value), 10)
	case int:
		candidate.ID = strconv.Itoa(value)
	case int64:
		candidate.ID = strconv.FormatInt(value, 10)
	}
	return candidate
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
	switch attr.Type {
	case 1:
		return "asset"
	case 2:
		return "user"
	case 0:
		// Continue below for Jira's built-in scalar types.
	default:
		return "textarea"
	}
	switch attr.DefaultTypeID {
	case 1, 3:
		return "number"
	case 2:
		return "boolean"
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
