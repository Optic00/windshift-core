package handlers

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"windshift/internal/database"
)

func setupRequiredAssetImportTest(t *testing.T) (*AssetHandler, database.Database, int, int, int, int, int) {
	t.Helper()
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "asset-import-required.db"))
	if err != nil {
		t.Fatalf("NewSQLiteDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	insertID := func(label, query string, args ...interface{}) int {
		t.Helper()
		var id int
		if err := db.QueryRow(query, args...).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", label, err)
		}
		return id
	}

	userID := insertID("user", `
		INSERT INTO users (email, username, first_name, last_name)
		VALUES ('asset-import@example.test', 'asset-import', 'Asset', 'Importer') RETURNING id`)
	setID := insertID("asset set", `
		INSERT INTO asset_management_sets (name, created_by)
		VALUES ('Required import fields', ?) RETURNING id`, userID)
	typeID := insertID("asset type", `
		INSERT INTO asset_types (set_id, name)
		VALUES (?, 'Server') RETURNING id`, setID)
	ownerFieldID := insertID("owner field", `
		INSERT INTO custom_field_definitions (name, field_type)
		VALUES ('Import owner', 'text') RETURNING id`)
	regionFieldID := insertID("region field", `
		INSERT INTO custom_field_definitions (name, field_type)
		VALUES ('Import region', 'text') RETURNING id`)
	if _, err := db.ExecWrite(`
		INSERT INTO asset_type_fields (asset_type_id, custom_field_id, is_required, display_order)
		VALUES (?, ?, true, 0), (?, ?, true, 1)
	`, typeID, ownerFieldID, typeID, regionFieldID); err != nil {
		t.Fatalf("attach required fields: %v", err)
	}

	return NewAssetHandler(db, nil, ""), db, setID, typeID, ownerFieldID, regionFieldID, userID
}

func TestImportCSVRowAlwaysEnforcesRequiredCustomFields(t *testing.T) {
	handler, db, setID, typeID, ownerFieldID, regionFieldID, userID := setupRequiredAssetImportTest(t)
	baseMappings := AssetImportMappings{
		Title:       0,
		Description: -1,
		AssetTag:    -1,
		CategoryID:  -1,
		StatusID:    -1,
	}

	tests := []struct {
		name         string
		record       []string
		customFields map[string]int
	}{
		{
			name:         "no custom mapping",
			record:       []string{"No mapping"},
			customFields: nil,
		},
		{
			name:         "all mapped cells blank",
			record:       []string{"Blank mapping", "", ""},
			customFields: map[string]int{importFieldKey(ownerFieldID): 1, importFieldKey(regionFieldID): 2},
		},
		{
			name:         "partial required values",
			record:       []string{"Partial mapping", "alice", ""},
			customFields: map[string]int{importFieldKey(ownerFieldID): 1, importFieldKey(regionFieldID): 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mappings := baseMappings
			mappings.CustomFields = tc.customFields
			req := StartAssetImportRequest{AssetTypeID: typeID, Mappings: mappings}
			if err := handler.importCSVRow(tc.record, setID, req, userID, nil, "required-fields-test"); err == nil {
				t.Fatal("importCSVRow succeeded, want required-field error")
			}
		})
	}

	var invalidCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assets`).Scan(&invalidCount); err != nil {
		t.Fatalf("count invalid assets: %v", err)
	}
	if invalidCount != 0 {
		t.Fatalf("invalid assets inserted = %d, want 0", invalidCount)
	}

	validMappings := baseMappings
	validMappings.CustomFields = map[string]int{
		importFieldKey(ownerFieldID):  1,
		importFieldKey(regionFieldID): 2,
	}
	validReq := StartAssetImportRequest{AssetTypeID: typeID, Mappings: validMappings}
	if err := handler.importCSVRow(
		[]string{"Valid mapping", "alice", "eu-central"},
		setID,
		validReq,
		userID,
		nil,
		"required-fields-test",
	); err != nil {
		t.Fatalf("valid importCSVRow: %v", err)
	}

	var rawValues string
	if err := db.QueryRow(`SELECT custom_field_values FROM assets WHERE title = 'Valid mapping'`).Scan(&rawValues); err != nil {
		t.Fatalf("load valid asset: %v", err)
	}
	var values map[string]interface{}
	if err := json.Unmarshal([]byte(rawValues), &values); err != nil {
		t.Fatalf("decode custom fields: %v", err)
	}
	if values[importFieldKey(ownerFieldID)] != "alice" || values[importFieldKey(regionFieldID)] != "eu-central" {
		t.Fatalf("custom fields = %#v", values)
	}
}

func importFieldKey(id int) string {
	return fmt.Sprintf("%d", id)
}
