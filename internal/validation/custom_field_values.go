package validation

import (
	"fmt"
	"strconv"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/sanitize"
)

// ValidateAndNormalizeCustomFieldValues mutates CFV values to match field
// definitions. It validates choices, deduplicates multiselects, sanitizes text,
// and returns the first invalid value; unknown keys await async cleanup.
func ValidateAndNormalizeCustomFieldValues(db database.Database, cfv map[string]interface{}) error {
	if len(cfv) == 0 {
		return nil
	}

	// Load referenced fields in one query.
	fields, err := loadFieldsForCFV(db, cfv)
	if err != nil {
		return fmt.Errorf("load custom fields for validation: %w", err)
	}

	for fieldKey, raw := range cfv {
		def, ok := fields[fieldKey]
		if !ok {
			// Unknown keys are removed asynchronously.
			continue
		}
		switch def.FieldType {
		case "select":
			if err := validateSelectValue(fieldKey, def, raw); err != nil {
				return err
			}
		case "multiselect":
			normalized, err := validateAndDedupeMultiselect(fieldKey, def, raw)
			if err != nil {
				return err
			}
			cfv[fieldKey] = normalized
		case models.CustomFieldTypeBoolean, models.CustomFieldTypeCheckbox:
			if _, err := ValidateCheckboxValue(fieldKey, raw); err != nil {
				return &ValidationError{
					Field:   "custom_field_values." + fieldKey,
					Message: fmt.Sprintf("boolean value must be true, false, or empty; got %T", raw),
				}
			}
		case "text", "textarea":
			cfv[fieldKey] = sanitizeTextValue(def.FieldType, raw)
		}
	}
	return nil
}

// ValidateCheckboxValue enforces the asset-aligned boolean value contract. An
// empty value is valid, and a supplied value must be an actual Go/JSON boolean.
// Requiredness never changes boolean semantics: both true and false are valid.
func ValidateCheckboxValue(fieldID string, raw interface{}) (bool, error) {
	if raw == nil {
		return false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("field %s must be a boolean value", fieldID)
	}
	return value, nil
}

// SanitizeCustomFieldTextValues sanitizes text fields for prevalidated writes.
func SanitizeCustomFieldTextValues(db database.Database, cfv map[string]interface{}) error {
	if len(cfv) == 0 {
		return nil
	}
	fields, err := loadFieldsForCFV(db, cfv)
	if err != nil {
		return fmt.Errorf("load custom fields for sanitization: %w", err)
	}
	for fieldKey, raw := range cfv {
		def, ok := fields[fieldKey]
		if !ok {
			continue
		}
		switch def.FieldType {
		case "text", "textarea":
			cfv[fieldKey] = sanitizeTextValue(def.FieldType, raw)
		}
	}
	return nil
}

// CustomFieldTypes bulk-resolves field types for known numeric CFV keys.
func CustomFieldTypes(db database.Database, cfv map[string]interface{}) (map[string]string, error) {
	fields, err := loadFieldsForCFV(db, cfv)
	if err != nil {
		return nil, err
	}
	types := make(map[string]string, len(fields))
	for key, def := range fields {
		types[key] = def.FieldType
	}
	return types, nil
}

// sanitizeTextValue applies the matching text policy; non-strings pass through.
func sanitizeTextValue(fieldType string, raw interface{}) interface{} {
	s, ok := raw.(string)
	if !ok {
		return raw
	}
	if fieldType == "textarea" {
		return sanitize.RichText.Sanitize(s)
	}
	return sanitize.PlainTextField.Sanitize(s)
}

func loadFieldsForCFV(db database.Database, cfv map[string]interface{}) (map[string]*models.CustomFieldDefinition, error) {
	if len(cfv) == 0 {
		return nil, nil
	}

	// Collect numeric ids from cfv keys; non-numeric keys are skipped.
	ids := make([]any, 0, len(cfv))
	for key := range cfv {
		if _, err := strconv.Atoi(key); err == nil {
			ids = append(ids, key)
		}
	}
	if len(ids) == 0 {
		return map[string]*models.CustomFieldDefinition{}, nil
	}

	placeholders := ""
	for i := range ids {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
	}

	// options is nullable for every non-choice field. Scan a stable empty value
	// so validating ordinary text/textarea fields does not fail before their
	// type-specific sanitizer runs.
	query := "SELECT id, field_type, COALESCE(options, '') FROM custom_field_definitions WHERE id IN (" + placeholders + ")"
	rows, err := db.Query(query, ids...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]*models.CustomFieldDefinition, len(ids))
	for rows.Next() {
		var def models.CustomFieldDefinition
		if err := rows.Scan(&def.ID, &def.FieldType, &def.Options); err != nil {
			return nil, err
		}
		out[strconv.Itoa(def.ID)] = &def
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// validateSelectValue accepts numeric or numeric-string values that match
// a known option id on the field. Anything else is a ValidationError.
func validateSelectValue(fieldKey string, def *models.CustomFieldDefinition, raw interface{}) error {
	if raw == nil {
		return nil
	}
	id, ok := coerceOptionID(raw)
	if !ok {
		return &ValidationError{
			Field:   "custom_field_values." + fieldKey,
			Message: fmt.Sprintf("select option id must be a number, got %T", raw),
		}
	}
	allowed, err := optionIDSet(def)
	if err != nil {
		return err
	}
	if !allowed[id] {
		return &ValidationError{
			Field:   "custom_field_values." + fieldKey,
			Message: fmt.Sprintf("option id %d is not in the field's option set", id),
		}
	}
	return nil
}

// validateAndDedupeMultiselect validates option IDs and preserves first-seen order.
func validateAndDedupeMultiselect(fieldKey string, def *models.CustomFieldDefinition, raw interface{}) ([]int, error) {
	if raw == nil {
		return nil, nil
	}
	values, err := coerceToSlice(raw)
	if err != nil {
		return nil, &ValidationError{
			Field:   "custom_field_values." + fieldKey,
			Message: fmt.Sprintf("multiselect value must be an array, got %T", raw),
		}
	}
	allowed, err := optionIDSet(def)
	if err != nil {
		return nil, err
	}

	seen := make(map[int]bool, len(values))
	out := make([]int, 0, len(values))
	for _, v := range values {
		id, ok := coerceOptionID(v)
		if !ok {
			return nil, &ValidationError{
				Field:   "custom_field_values." + fieldKey,
				Message: fmt.Sprintf("multiselect option id must be a number, got %T", v),
			}
		}
		if !allowed[id] {
			return nil, &ValidationError{
				Field:   "custom_field_values." + fieldKey,
				Message: fmt.Sprintf("option id %d is not in the field's option set", id),
			}
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// optionIDSet parses the field's options JSON once and returns the set of
// known option ids for fast membership checks.
func optionIDSet(def *models.CustomFieldDefinition) (map[int]bool, error) {
	opts, err := models.ParseSelectOptions(def.Options)
	if err != nil {
		return nil, fmt.Errorf("parse options for field %d: %w", def.ID, err)
	}
	set := make(map[int]bool, len(opts.Items))
	for _, item := range opts.Items {
		set[item.ID] = true
	}
	return set, nil
}

// coerceOptionID accepts JSON numbers, Go ints, and legacy numeric strings.
func coerceOptionID(v interface{}) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	case string:
		n, err := strconv.Atoi(x)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// coerceToSlice accepts JSON-decoded arrays and Go-side []int values.
func coerceToSlice(v interface{}) ([]interface{}, error) {
	switch x := v.(type) {
	case []interface{}:
		return x, nil
	case []int:
		out := make([]interface{}, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out, nil
	}
	return nil, fmt.Errorf("not an array")
}
