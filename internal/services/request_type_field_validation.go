package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/validation"
)

// IsBlankSubmittedField reports whether a value submitted in a portal/form
// payload should be treated as "no value" by required-field validation. JSON
// unmarshalling produces []interface{} / map[string]interface{} for empty
// arrays and objects respectively, and the old `== nil || == ""` check let
// those slip through, allowing required multiselect/object fields to be
// satisfied by `[]` or `{}`. Scalars `false` and `0` (and `0.0`) are NOT
// blank — they're legitimate values.
func IsBlankSubmittedField(value interface{}) bool {
	if value == nil {
		return true
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return rv.Len() == 0
	default:
		return false
	}
}

// RequestTypeValidationResult contains the result of request type field validation.
type RequestTypeValidationResult struct {
	ItemTypeID *int
	// WorkspaceID is the request type's own target workspace, when set. It is
	// the source of truth for submission routing: callers create the item in
	// this workspace, falling back to the channel's first configured workspace
	// only when it is nil. Nullable in the schema, so a request type may not
	// pin a workspace.
	WorkspaceID        *int
	VirtualFieldValues map[string]interface{}
	CustomFieldValues  map[string]interface{}
	// TitleFieldInForm is true when the request type's field config includes
	// the default "title" field — meaning the submitter saw a title input on
	// the form. Callers that need a title (every item create) use this to
	// decide between trusting submission.Title vs. rendering a title template.
	TitleFieldInForm bool
}

// AllowedCreateScreenCustomFieldIdentifiers resolves the custom fields that
// may be used when creating an item of itemTypeID in workspaceID. Public form
// schemas and submissions both use this list so stale or forged field rows do
// not expose or write fields outside the effective create screen.
func AllowedCreateScreenCustomFieldIdentifiers(db database.Database, workspaceID, itemTypeID int) (map[string]struct{}, error) {
	allowed := make(map[string]struct{})
	if workspaceID <= 0 || itemTypeID <= 0 {
		return allowed, nil
	}
	itemTypeAllowed, err := IsItemTypeAllowedInWorkspace(db, workspaceID, itemTypeID)
	if err != nil {
		return nil, err
	}
	if !itemTypeAllowed {
		return allowed, nil
	}

	screenRepo := repository.NewScreenRepository(db)
	createScreenID, err := screenRepo.GetCreateScreenID(workspaceID, itemTypeID)
	if err != nil || createScreenID == nil {
		return allowed, err
	}
	fields, err := screenRepo.ListFields(*createScreenID)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		if field.FieldType == "custom" && field.FieldIdentifier != "" && field.FieldName != "" {
			allowed[field.FieldIdentifier] = struct{}{}
		}
	}
	return allowed, nil
}

func allowedRequestTypeCustomFieldIdentifiers(ctx context.Context, db database.Database, requestTypeID int) (map[string]struct{}, error) {
	var itemTypeID int
	var workspaceID sql.NullInt64
	var channelType, direction, configJSON string
	err := db.QueryRowContext(ctx, `
		SELECT rt.item_type_id, rt.workspace_id, c.type, c.direction, COALESCE(c.config, '{}')
		FROM request_types rt
		JOIN channels c ON c.id = rt.channel_id
		WHERE rt.id = ? AND rt.is_active = true
	`, requestTypeID).Scan(&itemTypeID, &workspaceID, &channelType, &direction, &configJSON)
	if err != nil {
		return nil, err
	}

	var config models.ChannelConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, fmt.Errorf("parse request type channel config: %w", err)
	}
	var servedWorkspaceIDs []int
	if direction == "inbound" {
		switch channelType {
		case "portal":
			servedWorkspaceIDs = config.PortalWorkspaceIDs
		case "form":
			servedWorkspaceIDs = config.FormWorkspaceIDs
		}
	}
	if len(servedWorkspaceIDs) == 0 {
		return map[string]struct{}{}, nil
	}

	effectiveWorkspaceID := servedWorkspaceIDs[0]
	if workspaceID.Valid {
		effectiveWorkspaceID = int(workspaceID.Int64)
		served := false
		for _, candidate := range servedWorkspaceIDs {
			if candidate == effectiveWorkspaceID {
				served = true
				break
			}
		}
		if !served {
			return map[string]struct{}{}, nil
		}
	}
	return AllowedCreateScreenCustomFieldIdentifiers(db, effectiveWorkspaceID, itemTypeID)
}

// ValidateAndSeparateRequestFields validates request type fields and separates virtual from custom fields.
func ValidateAndSeparateRequestFields(ctx context.Context, db database.Database, requestTypeID *int, title, description string, customFields map[string]interface{}) (*RequestTypeValidationResult, error) {
	result := &RequestTypeValidationResult{}

	if requestTypeID == nil {
		if title == "" {
			return nil, fmt.Errorf("title is required")
		}
		return result, nil
	}

	var rtID int
	var rtName string
	var itemTypeID int
	var workspaceID sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT id, name, item_type_id, workspace_id FROM request_types WHERE id = ? AND is_active = true`, *requestTypeID).Scan(
		&rtID, &rtName, &itemTypeID, &workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("invalid request type (ID: %d): %w", *requestTypeID, err)
	}
	result.ItemTypeID = &itemTypeID
	if workspaceID.Valid {
		wsID := int(workspaceID.Int64)
		result.WorkspaceID = &wsID
	}
	allowedCustomFieldIDs, err := allowedRequestTypeCustomFieldIdentifiers(ctx, db, *requestTypeID)
	if err != nil {
		return nil, fmt.Errorf("resolve request type create-screen fields: %w", err)
	}

	virtualFieldIDs := make(map[string]bool)
	configuredCustomFieldIDs := make(map[string]bool)
	rows, err := db.QueryContext(ctx, `SELECT field_identifier, field_type, is_required FROM request_type_fields WHERE request_type_id = ? ORDER BY display_order`, *requestTypeID)
	if err != nil {
		return nil, fmt.Errorf("failed to load request type fields: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var fieldID, fieldType string
		var isRequired bool
		if err := rows.Scan(&fieldID, &fieldType, &isRequired); err != nil {
			continue
		}
		if fieldType == "custom" {
			if _, allowed := allowedCustomFieldIDs[fieldID]; !allowed {
				continue
			}
		}

		switch fieldType {
		case "virtual":
			virtualFieldIDs[fieldID] = true
		case "custom":
			configuredCustomFieldIDs[fieldID] = true
		}

		// Title is always required when shown on the form, regardless of the
		// admin-set is_required flag — items.title is NOT NULL and the
		// portal's title-template fallback only applies when the field is
		// hidden entirely.
		if fieldType == "default" && fieldID == "title" {
			result.TitleFieldInForm = true
			if title == "" {
				return nil, fmt.Errorf("title is required")
			}
		}

		if isRequired {
			switch fieldType {
			case "default":
				if fieldID == "description" && description == "" {
					return nil, fmt.Errorf("description is required")
				}
			case "custom", "virtual":
				if customFields == nil || IsBlankSubmittedField(customFields[fieldID]) {
					return nil, fmt.Errorf("field %s is required", fieldID)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate request type fields: %w", err)
	}

	// Partition submitted fields. Keys that are neither configured custom fields
	// nor virtual fields are dropped silently — a 400 would act as an oracle
	// telling probers which field IDs exist on the request type.
	result.VirtualFieldValues = make(map[string]interface{})
	result.CustomFieldValues = make(map[string]interface{})
	for fieldID, value := range customFields {
		switch {
		case virtualFieldIDs[fieldID]:
			result.VirtualFieldValues[fieldID] = value
		case configuredCustomFieldIDs[fieldID]:
			result.CustomFieldValues[fieldID] = value
		}
	}
	if err := validation.ValidateAndNormalizeCustomFieldValues(db, result.CustomFieldValues); err != nil {
		return nil, err
	}

	return result, nil
}

// StoreCustomFieldValues stores custom field values for an item.
// The component parameter is used for log attribution (e.g. "forms", "portal").
func StoreCustomFieldValues(ctx context.Context, db database.Database, component string, itemID int64, customFields map[string]interface{}) error {
	_ = component
	if len(customFields) == 0 {
		return nil
	}
	if err := validation.ValidateAndNormalizeCustomFieldValues(db, customFields); err != nil {
		return err
	}

	customFieldsJSON, err := json.Marshal(customFields)
	if err != nil {
		return fmt.Errorf("marshal custom field values: %w", err)
	}
	return repository.NewItemRepository(db).SetCustomFieldValuesRaw(ctx, int(itemID), string(customFieldsJSON))
}

// StoreVirtualFieldValues stores virtual field values for an item.
// The component parameter is used for log attribution (e.g. "forms", "portal").
func StoreVirtualFieldValues(ctx context.Context, db database.Database, component string, itemID int64, virtualFields map[string]interface{}) error {
	_ = component
	if len(virtualFields) == 0 {
		return nil
	}

	virtualFieldsJSON, err := json.Marshal(virtualFields)
	if err != nil {
		return fmt.Errorf("marshal virtual field values: %w", err)
	}
	return repository.NewItemRepository(db).SetVirtualFieldDataRaw(ctx, int(itemID), string(virtualFieldsJSON))
}
