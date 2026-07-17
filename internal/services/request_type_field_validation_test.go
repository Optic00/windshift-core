package services

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"windshift/internal/database"
)

func TestValidateAndSeparateRequestFieldsRejectsInvalidSelectOption(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "request-fields.db"))
	if err != nil {
		t.Fatalf("NewSQLiteDB: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var channelID, workspaceID, itemTypeID, fieldID, requestTypeID int
	if err := db.QueryRow(`
		INSERT INTO channels (name, type, direction, status, config)
		VALUES ('form', 'form', 'inbound', 'enabled', '{}') RETURNING id
	`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO workspaces (name, key) VALUES ('Requests', 'REQX') RETURNING id`).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.ExecWrite(`UPDATE channels SET config = ? WHERE id = ?`, fmt.Sprintf(`{"form_workspace_ids":[%d]}`, workspaceID), channelID); err != nil {
		t.Fatalf("configure channel workspace: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO item_types (name) VALUES ('Request Field Test Type') RETURNING id`).Scan(&itemTypeID); err != nil {
		t.Fatalf("insert item type: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO custom_field_definitions (name, field_type, options)
		VALUES ('request-field-select', 'select', '{"next_id":3,"items":[{"id":1,"label":"One"},{"id":2,"label":"Two"}]}')
		RETURNING id
	`).Scan(&fieldID); err != nil {
		t.Fatalf("insert custom field: %v", err)
	}
	var configSetID, screenID int
	if err := db.QueryRow(`INSERT INTO configuration_sets (name) VALUES ('Request Field Test Config') RETURNING id`).Scan(&configSetID); err != nil {
		t.Fatalf("insert configuration set: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO screens (name) VALUES ('Request Field Test Screen') RETURNING id`).Scan(&screenID); err != nil {
		t.Fatalf("insert screen: %v", err)
	}
	if _, err := db.ExecWrite(`INSERT INTO workspace_configuration_sets (workspace_id, configuration_set_id) VALUES (?, ?)`, workspaceID, configSetID); err != nil {
		t.Fatalf("bind workspace configuration: %v", err)
	}
	if _, err := db.ExecWrite(`INSERT INTO configuration_set_item_types (configuration_set_id, item_type_id, create_screen_id) VALUES (?, ?, ?)`, configSetID, itemTypeID, screenID); err != nil {
		t.Fatalf("bind item type screen: %v", err)
	}
	if _, err := db.ExecWrite(`INSERT INTO screen_fields (screen_id, field_type, field_identifier) VALUES (?, 'custom', ?)`, screenID, strconv.Itoa(fieldID)); err != nil {
		t.Fatalf("insert screen field: %v", err)
	}
	if err := db.QueryRow(`
		INSERT INTO request_types (channel_id, name, item_type_id, workspace_id, is_active)
		VALUES (?, 'Request Field Validation', ?, ?, true) RETURNING id
	`, channelID, itemTypeID, workspaceID).Scan(&requestTypeID); err != nil {
		t.Fatalf("insert request type: %v", err)
	}
	if _, err := db.ExecWrite(`
		INSERT INTO request_type_fields
			(request_type_id, field_identifier, field_type, is_required, display_order)
		VALUES (?, ?, 'custom', true, 1)
	`, requestTypeID, strconv.Itoa(fieldID)); err != nil {
		t.Fatalf("insert request type field: %v", err)
	}

	_, err = ValidateAndSeparateRequestFields(context.Background(), db, &requestTypeID, "Title", "", map[string]interface{}{
		strconv.Itoa(fieldID): float64(999),
	})
	if err == nil {
		t.Fatal("invalid select option unexpectedly passed public request-field validation")
	}
}
