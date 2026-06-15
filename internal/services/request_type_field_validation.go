package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"windshift/internal/database"
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
	ItemTypeID         *int
	VirtualFieldValues map[string]interface{}
	CustomFieldValues  map[string]interface{}
	// TitleFieldInForm is true when the request type's field config includes
	// the default "title" field — meaning the submitter saw a title input on
	// the form. Callers that need a title (every item create) use this to
	// decide between trusting submission.Title vs. rendering a title template.
	TitleFieldInForm bool
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
	err := db.QueryRowContext(ctx, `SELECT id, name, item_type_id FROM request_types WHERE id = ? AND is_active = true`, *requestTypeID).Scan(
		&rtID, &rtName, &itemTypeID,
	)
	if err != nil {
		return nil, fmt.Errorf("invalid request type (ID: %d): %w", *requestTypeID, err)
	}
	result.ItemTypeID = &itemTypeID

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

	return result, nil
}

// StoreCustomFieldValues stores custom field values for an item.
// The component parameter is used for log attribution (e.g. "forms", "portal").
func StoreCustomFieldValues(ctx context.Context, db database.Database, component string, itemID int64, customFields map[string]interface{}) {
	if len(customFields) == 0 {
		return
	}

	// Forms/portal submissions carry raw anonymous-user text; bound
	// text/textarea values before they reach either persistence target
	// below. Failure to load field definitions only skips sanitization
	// (logged), matching this function's tolerant store-what-we-can style.
	if err := validation.SanitizeCustomFieldTextValues(db, customFields); err != nil {
		slog.Warn("failed to sanitize custom field values", slog.String("component", component), slog.Int64("item_id", itemID), slog.Any("error", err))
	}

	now := time.Now()
	for fieldIDStr, value := range customFields {
		if value == nil || value == "" {
			continue
		}

		var valueStr string
		switch v := value.(type) {
		case string:
			valueStr = v
		case float64:
			valueStr = fmt.Sprintf("%v", v)
		case bool:
			valueStr = fmt.Sprintf("%v", v)
		default:
			valueBytes, err := json.Marshal(v)
			if err == nil {
				valueStr = string(valueBytes)
			}
		}

		if valueStr != "" {
			if _, err := db.ExecWriteContext(ctx, `
				INSERT INTO custom_field_values (item_id, custom_field_id, value, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(item_id, custom_field_id) DO UPDATE SET value = ?, updated_at = ?
			`, itemID, fieldIDStr, valueStr, now, now, valueStr, now); err != nil {
				slog.Warn("failed to save custom field value", slog.String("component", component), slog.Int64("item_id", itemID), slog.String("field_id", fieldIDStr), slog.Any("error", err))
			}
		}
	}

	customFieldsJSON, err := json.Marshal(customFields)
	if err == nil {
		if err := repository.NewItemRepository(db).SetCustomFieldValuesRaw(ctx, int(itemID), string(customFieldsJSON)); err != nil {
			slog.Warn("failed to update item custom_field_values", slog.String("component", component), slog.Int64("item_id", itemID), slog.Any("error", err))
		}
	}
}

// StoreVirtualFieldValues stores virtual field values for an item.
// The component parameter is used for log attribution (e.g. "forms", "portal").
func StoreVirtualFieldValues(ctx context.Context, db database.Database, component string, itemID int64, virtualFields map[string]interface{}) {
	if len(virtualFields) == 0 {
		return
	}

	virtualFieldsJSON, err := json.Marshal(virtualFields)
	if err != nil {
		slog.Warn("failed to marshal virtual field values", slog.String("component", component), slog.Int64("item_id", itemID), slog.Any("error", err))
		return
	}

	if err := repository.NewItemRepository(db).SetVirtualFieldDataRaw(ctx, int(itemID), string(virtualFieldsJSON)); err != nil {
		slog.Warn("failed to update item virtual_field_data", slog.String("component", component), slog.Int64("item_id", itemID), slog.Any("error", err))
	}
}
