package jiraimport

import (
	"database/sql"
	"errors"
	"fmt"

	"windshift/internal/repository"
)

type ScreenField struct {
	Type       string
	Identifier string
	Required   bool
}

func (s *Service) EnsureConfiguredScreen(name, description string, fields []ScreenField) (screenID int, created bool, err error) {
	err = s.db.QueryRow(`SELECT id FROM screens WHERE name = ?`, name).Scan(&screenID)
	if err == nil {
		return screenID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	err = s.db.QueryRow(`
		INSERT INTO screens (name, description, created_at, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) RETURNING id
	`, name, description).Scan(&screenID)
	if err != nil {
		return 0, false, err
	}
	for index, field := range fields {
		if _, err = s.db.ExecWrite(`
			INSERT INTO screen_fields (screen_id, field_type, field_identifier, display_order, is_required, field_width)
			VALUES (?, ?, ?, ?, ?, 'full')
		`, screenID, field.Type, field.Identifier, index+1, field.Required); err != nil {
			return 0, false, fmt.Errorf("add field to imported Jira screen %q: %w", name, err)
		}
		if field.Type == "system" {
			if _, err = s.db.ExecWrite(`
				INSERT INTO screen_system_fields (screen_id, field_name)
				VALUES (?, ?) ON CONFLICT(screen_id, field_name) DO NOTHING
			`, screenID, field.Identifier); err != nil {
				return 0, false, err
			}
		}
	}
	return screenID, true, nil
}

func (s *Service) ChoiceFieldOptions(fieldID int) (fieldType, options string, err error) {
	var rawOptions sql.NullString
	err = s.db.QueryRow(`
		SELECT field_type, options FROM custom_field_definitions WHERE id = ?
	`, fieldID).Scan(&fieldType, &rawOptions)
	return fieldType, rawOptions.String, err
}

func (s *Service) UpdateChoiceFieldOptions(fieldID int, options string) error {
	_, err := s.db.ExecWrite(`
		UPDATE custom_field_definitions SET options = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?
	`, options, fieldID)
	return err
}

func (s *Service) EnsureIssueKeyField(names []string, description, fieldType string) (fieldID int, name string, created bool, err error) {
	for _, candidate := range names {
		var existingType string
		err = s.db.QueryRow(`
			SELECT id, field_type FROM custom_field_definitions WHERE LOWER(name) = LOWER(?)
		`, candidate).Scan(&fieldID, &existingType)
		if err == nil {
			if existingType == fieldType {
				return fieldID, candidate, false, nil
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, "", false, err
		}
		err = s.db.QueryRow(`
			INSERT INTO custom_field_definitions
				(name, field_type, description, required, options, display_order,
				 applies_to_portal_customers, applies_to_customer_organisations, created_at, updated_at)
			VALUES (?, ?, ?, false, '', 0, false, false, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			RETURNING id
		`, candidate, fieldType, description).Scan(&fieldID)
		return fieldID, candidate, err == nil, err
	}
	return 0, "", false, repository.ErrNotFound
}

func (s *Service) CustomFieldType(fieldID int) string {
	field, err := s.customFields.FindByID(fieldID)
	if err != nil {
		return ""
	}
	return field.FieldType
}

func (s *Service) BindFieldsToWorkspace(workspaceID int, projectKey string, fieldIDs []int) error {
	return s.screens.BindImportedFields(workspaceID, projectKey, fieldIDs)
}
