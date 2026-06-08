package handlers

import (
	"path/filepath"
	"reflect"
	"testing"

	"windshift/internal/database"
	"windshift/internal/jira"
)

func TestJiraAssetAttributeFieldType(t *testing.T) {
	cases := []struct {
		name string
		attr jira.AssetObjectAttribute
		want string
	}{
		{name: "integer", attr: jira.AssetObjectAttribute{Type: 0, DefaultTypeID: 1}, want: "number"},
		{name: "date", attr: jira.AssetObjectAttribute{Type: 0, DefaultTypeID: 4}, want: "date"},
		{name: "object reference", attr: jira.AssetObjectAttribute{Type: 1}, want: "textarea"},
		{name: "text", attr: jira.AssetObjectAttribute{Type: 0, DefaultTypeID: 0}, want: "textarea"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jiraAssetAttributeFieldType(tc.attr); got != tc.want {
				t.Fatalf("jiraAssetAttributeFieldType() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJiraIssueAssetCandidatesExtractsIDsAndKeys(t *testing.T) {
	got := jiraIssueAssetCandidates([]interface{}{
		map[string]interface{}{"id": "100", "objectKey": "LAP-1", "label": "Laptop"},
		map[string]interface{}{"objectId": "200", "key": "MON-2", "name": "Monitor"},
	})
	want := []jiraIssueAssetCandidate{
		{ID: "100", Key: "LAP-1", Label: "Laptop"},
		{ID: "200", Key: "MON-2", Label: "Monitor"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("jiraIssueAssetCandidates() = %#v, want %#v", got, want)
	}
}

func TestResolveJiraIssueAssetReferencesUsesImportedAssetMappings(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "jira-asset-links.db"))
	if err != nil {
		t.Fatalf("create sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize db: %v", err)
	}
	insertJiraImportConnection(t, db, "conn-1")
	insertJiraImportJob(t, db, "job-assets", "conn-1", "running", []string{"ASFJ"})

	var setID int
	if err := db.QueryRow(`
		INSERT INTO asset_management_sets (name, description) VALUES ('Imported Assets', '') RETURNING id
	`).Scan(&setID); err != nil {
		t.Fatalf("insert asset set: %v", err)
	}
	var typeID int
	if err := db.QueryRow(`
		INSERT INTO asset_types (set_id, name) VALUES (?, 'Laptop') RETURNING id
	`, setID).Scan(&typeID); err != nil {
		t.Fatalf("insert asset type: %v", err)
	}
	var assetID int
	if err := db.QueryRow(`
		INSERT INTO assets (set_id, asset_type_id, title, asset_tag, custom_field_values)
		VALUES (?, ?, 'Laptop 1', 'LAP-1', '{}') RETURNING id
	`, setID, typeID).Scan(&assetID); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	if _, err := db.ExecWrite(`
		INSERT INTO jira_import_id_mappings (job_id, entity_type, jira_id, jira_key, windshift_id, metadata_json)
		VALUES ('job-assets', 'asset', '100', 'LAP-1', ?, '{}')
	`, assetID); err != nil {
		t.Fatalf("insert mapping: %v", err)
	}

	h := NewJiraImportHandler(db, "jira-import-asset-link-test-secret-32", "")
	refs := h.resolveJiraIssueAssetReferences("job-assets", []interface{}{map[string]interface{}{"objectId": "100", "label": "Laptop 1"}})
	if len(refs) != 1 {
		t.Fatalf("reference count = %d, want 1 (%#v)", len(refs), refs)
	}
	if refs[0].AssetID != assetID || refs[0].AssetTag != "LAP-1" || refs[0].Title != "Laptop 1" {
		t.Fatalf("resolved ref = %#v, want asset id/title/tag", refs[0])
	}
}

func TestJiraAssetAttributeValueUsesDisplayValues(t *testing.T) {
	got, ok := jiraAssetAttributeValue(jira.AssetObjectAttributeValue{
		ObjectAttributeValues: []jira.AssetAttributeValue{
			{DisplayValue: "Server A", SearchValue: "server-a"},
			{SearchValue: "fallback-search"},
		},
	})
	if !ok {
		t.Fatal("jiraAssetAttributeValue() did not return a value")
	}
	if got != "Server A\nfallback-search" {
		t.Fatalf("jiraAssetAttributeValue() = %#v, want newline-separated display/search values", got)
	}
}
