package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"windshift/internal/database"
	"windshift/internal/jira"
	"windshift/internal/models"
)

func TestFindConflictingJiraImports(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "jira-import-conflicts.db"))
	if err != nil {
		t.Fatalf("create sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize db: %v", err)
	}

	insertJiraImportConnection(t, db, "conn-1")
	insertJiraImportConnection(t, db, "conn-2")
	insertJiraImportJob(t, db, "job-completed", "conn-1", "completed", []string{"ASFJ", "BILL"})
	insertJiraImportJob(t, db, "job-deleted", "conn-1", "data_deleted", []string{"CRM"})
	insertJiraImportJob(t, db, "job-other-connection", "conn-2", "completed", []string{"ASFJ"})

	h := NewJiraImportHandler(db, "jira-import-conflict-test-secret-32-bytes", "")
	conflicts, err := h.findConflictingJiraImports(StartImportRequest{
		ConnectionID: "conn-1",
		ProjectKeys:  []string{" bill "},
	})
	if err != nil {
		t.Fatalf("find conflicts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflict count = %d, want 1 (%#v)", len(conflicts), conflicts)
	}
	if conflicts[0].JobID != "job-completed" {
		t.Fatalf("conflict job = %q, want job-completed", conflicts[0].JobID)
	}
	if got, want := conflicts[0].ProjectKeys, []string{"ASFJ", "BILL"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("conflict project keys = %#v, want %#v", got, want)
	}

	conflicts, err = h.findConflictingJiraImports(StartImportRequest{
		ConnectionID: "conn-1",
		ProjectKeys:  []string{"CRM"},
	})
	if err != nil {
		t.Fatalf("find deleted-only conflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("deleted import conflicts = %#v, want none", conflicts)
	}
}

func TestDeleteImportedDataRequiresStrongConfirmation(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "jira-import-delete-guard.db"))
	if err != nil {
		t.Fatalf("create sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize db: %v", err)
	}

	insertJiraImportConnection(t, db, "conn-1")
	insertJiraImportJob(t, db, "job-delete", "conn-1", "completed", []string{"ASFJ"})
	if _, err := db.ExecWrite(`
		INSERT INTO jira_import_id_mappings (job_id, entity_type, jira_id, jira_key, windshift_id, metadata_json)
		VALUES ('job-delete', 'workspace', 'ASFJ', 'ASFJ', 12345, '{}')
	`); err != nil {
		t.Fatalf("insert workspace mapping: %v", err)
	}

	h := NewJiraImportHandler(db, "jira-import-delete-guard-test-secret-32", "")

	mismatchBody := `{"confirm_job_id":"job-delete","confirm_workspace_count":0,"confirm_delete_imported_data":true}`
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/jira-import/jobs/job-delete/data", strings.NewReader(mismatchBody))
	req.SetPathValue("jobId", "job-delete")
	w := httptest.NewRecorder()
	h.DeleteImportedData(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("count mismatch status = %d, want %d; body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}

	matchingBody := bytes.NewBufferString(`{"confirm_job_id":"job-delete","confirm_workspace_count":1,"confirm_delete_imported_data":true}`)
	req = httptest.NewRequest(http.MethodDelete, "/api/admin/jira-import/jobs/job-delete/data", matchingBody)
	req.SetPathValue("jobId", "wrong-job")
	w = httptest.NewRecorder()
	h.DeleteImportedData(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("job-id mismatch status = %d, want %d; body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestEnsureCustomFieldsCreatesJiraAssetFieldWhenSingleAssetSetImported(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "jira-import-asset-field.db"))
	if err != nil {
		t.Fatalf("create sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize db: %v", err)
	}

	insertJiraImportConnection(t, db, "conn-1")
	insertJiraImportJob(t, db, "job-asset-field", "conn-1", "running", []string{"ASFJ"})
	if _, err := db.ExecWrite(`
		INSERT INTO jira_import_id_mappings (job_id, entity_type, jira_id, jira_key, windshift_id, metadata_json)
		VALUES ('job-asset-field', 'asset_set', 'schema-1', 'ITSM', 77, '{}')
	`); err != nil {
		t.Fatalf("insert asset set mapping: %v", err)
	}

	h := NewJiraImportHandler(db, "jira-import-asset-field-test-secret", "")
	fieldMap, err := h.ensureCustomFields(t.Context(), "job-asset-field", []CustomFieldMapping{{
		JiraID:        "customfield_12345",
		JiraName:      "Affected hardware",
		JiraType:      "com.atlassian.jira.plugins.jira-servicedesk-cmdb-plugin:insight-object-field",
		WindshiftType: "asset",
		Action:        "create",
	}})
	if err != nil {
		t.Fatalf("ensure custom fields: %v", err)
	}
	fieldID := fieldMap["customfield_12345"]
	if fieldID == 0 {
		t.Fatalf("asset field not mapped: %#v", fieldMap)
	}
	var fieldType, options string
	if err := db.QueryRow(`SELECT field_type, options FROM custom_field_definitions WHERE id = ?`, fieldID).Scan(&fieldType, &options); err != nil {
		t.Fatalf("load custom field: %v", err)
	}
	if fieldType != "asset" {
		t.Fatalf("field_type = %q, want asset", fieldType)
	}
	if !strings.Contains(options, `"asset_set_id":77`) {
		t.Fatalf("options = %s, want asset_set_id 77", options)
	}
}

func TestEnsureAffectsVersionCustomFieldStoresAllVersionsAsMultiselect(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "jira-import-affects-version.db"))
	if err != nil {
		t.Fatalf("create sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize db: %v", err)
	}

	insertJiraImportConnection(t, db, "conn-1")
	insertJiraImportJob(t, db, "job-affects", "conn-1", "running", []string{"ASFJ"})

	h := NewJiraImportHandler(db, "jira-import-affects-test-secret-32", "")
	field, err := h.ensureAffectsVersionCustomField(t.Context(), "job-affects", []VersionMapping{
		{JiraID: "10000", JiraName: "1.0"},
		{JiraID: "10001", JiraName: "2.0"},
		{JiraID: "10002", JiraName: "1.0"},
	})
	if err != nil {
		t.Fatalf("ensureAffectsVersionCustomField: %v", err)
	}
	if field == nil {
		t.Fatal("field is nil")
	}
	if got := field.OptionIDsByJiraID["10000"]; got == 0 {
		t.Fatalf("version 10000 option ID missing: %#v", field.OptionIDsByJiraID)
	}
	if got, want := field.OptionIDsByJiraID["10002"], field.OptionIDsByJiraID["10000"]; got != want {
		t.Fatalf("duplicate version name option ID = %d, want shared ID %d", got, want)
	}

	var fieldType, options string
	if err := db.QueryRow(`SELECT field_type, options FROM custom_field_definitions WHERE id = ?`, field.FieldID).Scan(&fieldType, &options); err != nil {
		t.Fatalf("load custom field: %v", err)
	}
	if fieldType != "multiselect" {
		t.Fatalf("field_type = %q, want multiselect", fieldType)
	}
	if !strings.Contains(options, `"label":"1.0"`) || !strings.Contains(options, `"label":"2.0"`) {
		t.Fatalf("options do not contain affected version labels: %s", options)
	}
}

func TestDeleteImportedDataRemovesAttachmentFiles(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "jira-import-delete-attachments.db"))
	if err != nil {
		t.Fatalf("create sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize db: %v", err)
	}

	root := t.TempDir()
	filePath := filepath.Join(root, "jira-attachment.txt")
	if err := os.WriteFile(filePath, []byte("jira attachment"), 0o600); err != nil {
		t.Fatalf("write attachment file: %v", err)
	}
	if _, err := db.ExecWrite(`
		INSERT INTO attachment_settings (max_file_size, allowed_mime_types, attachment_path, enabled)
		VALUES (52428800, '', ?, true)
	`, root); err != nil {
		t.Fatalf("configure attachment settings: %v", err)
	}

	insertJiraImportConnection(t, db, "conn-1")
	insertJiraImportJob(t, db, "job-attachments", "conn-1", "completed", []string{"ASFJ"})
	var attachmentID int
	if err := db.QueryRow(`
		INSERT INTO attachments (item_id, entity_type, filename, original_filename, file_path, mime_type, file_size)
		VALUES (1, 'item', 'jira-attachment.txt', 'jira-attachment.txt', ?, 'text/plain', 15) RETURNING id
	`, filePath).Scan(&attachmentID); err != nil {
		t.Fatalf("insert attachment row: %v", err)
	}
	if _, err := db.ExecWrite(`
		INSERT INTO jira_import_id_mappings (job_id, entity_type, jira_id, jira_key, windshift_id, metadata_json)
		VALUES ('job-attachments', 'attachment', 'att-1', 'ASFJ-1', ?, '{}')
	`, attachmentID); err != nil {
		t.Fatalf("insert attachment mapping: %v", err)
	}

	h := NewJiraImportHandler(db, "jira-import-delete-file-test-secret-32", "")
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/jira-import/jobs/job-attachments/data", bytes.NewBufferString(`{"confirm_job_id":"job-attachments","confirm_workspace_count":0,"confirm_delete_imported_data":true}`))
	req.SetPathValue("jobId", "job-attachments")
	w := httptest.NewRecorder()
	h.DeleteImportedData(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete imported data status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("attachment file still exists or stat failed unexpectedly: %v", err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE id = ?`, attachmentID).Scan(&remaining); err != nil {
		t.Fatalf("count attachment rows: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("attachment rows remaining = %d, want 0", remaining)
	}
}

func insertJiraImportConnection(t *testing.T, db database.Database, id string) {
	t.Helper()
	if _, err := db.ExecWrite(`
		INSERT INTO jira_import_connections (id, instance_url, email, encrypted_credentials, instance_name, deployment_type)
		VALUES (?, 'https://example.invalid', 'redacted@example.invalid', '{}', 'Fixture Jira', 'cloud')
	`, id); err != nil {
		t.Fatalf("insert jira connection %s: %v", id, err)
	}
}

func insertJiraImportJob(t *testing.T, db database.Database, id, connectionID, status string, projectKeys []string) {
	t.Helper()
	configBytes, err := json.Marshal(map[string]interface{}{"project_keys": projectKeys})
	if err != nil {
		t.Fatalf("marshal jira import job config: %v", err)
	}
	if _, err := db.ExecWrite(`
		INSERT INTO jira_import_jobs (id, connection_id, status, scope, config_json, created_at, completed_at)
		VALUES (?, ?, ?, 'work_items', ?, ?, ?)
	`, id, connectionID, status, string(configBytes), time.Now(), time.Now()); err != nil {
		t.Fatalf("insert jira import job %s: %v", id, err)
	}
}

func TestTranslateJQLToWindshiftQLPreservesUnsupportedClauses(t *testing.T) {
	ql, unsupported := translateJQLToWindshiftQL(`project = ASFJ AND status IN ("To Do", Done) AND priority = Highest AND component = API ORDER BY created DESC`, 42)
	for _, want := range []string{
		`workspace_id = 42`,
		`status IN ("To Do", "Done")`,
		`priority = "Critical"`,
		`labels = "component:API"`,
	} {
		if !strings.Contains(ql, want) {
			t.Fatalf("ql = %q, want to contain %q", ql, want)
		}
	}
	if len(unsupported) != 1 || !strings.Contains(unsupported[0], "ORDER BY") {
		t.Fatalf("unsupported = %#v, want ORDER BY", unsupported)
	}
}

func TestJiraBoardColumnsMapsStatusesAndBacklog(t *testing.T) {
	h := &JiraImportHandler{}
	columns, backlog, unsupported := h.jiraBoardColumns(&jira.JiraBoardConfiguration{ColumnConfig: &jira.JiraBoardColumnConfig{Columns: []jira.JiraBoardConfigColumn{
		{Name: "Backlog", Statuses: []jira.JiraBoardColumnStatus{{ID: "10"}}},
		{Name: "Doing", Statuses: []jira.JiraBoardColumnStatus{{ID: "20"}, {ID: "missing"}}},
	}}}, map[string]int{"10": 1, "20": 2})
	if got, want := fmt.Sprint(backlog), "[1]"; got != want {
		t.Fatalf("backlog = %s, want %s", got, want)
	}
	if len(columns) != 1 || columns[0].Name != "Doing" || fmt.Sprint(columns[0].StatusIDs) != "[2]" {
		t.Fatalf("columns = %#v, want Doing with status 2", columns)
	}
	if got, want := fmt.Sprint(unsupported), "[missing]"; got != want {
		t.Fatalf("unsupported = %s, want %s", got, want)
	}
}

func TestEnsureJiraBoardConfigurationCreatesRowsAndMapping(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "jira-import-board-config.db"))
	if err != nil {
		t.Fatalf("create sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize db: %v", err)
	}
	insertJiraImportConnection(t, db, "conn-1")
	insertJiraImportJob(t, db, "job-board", "conn-1", "running", []string{"ASFJ"})

	var collectionID int
	if err := db.QueryRow(`INSERT INTO collections (name, ql_query) VALUES ('Jira Board: Team', 'workspace_id = 1') RETURNING id`).Scan(&collectionID); err != nil {
		t.Fatalf("insert collection: %v", err)
	}

	h := NewJiraImportHandler(db, "jira-import-board-test-secret", "")
	configID, ok := h.ensureJiraBoardConfiguration("job-board", jira.JiraBoard{ID: 77, Name: "Team", Type: "scrum"}, collectionID, []models.BoardColumnRequest{
		{Name: "To Do", StatusIDs: []int{1}, Color: "#64748b"},
		{Name: "Done", StatusIDs: []int{3}, Color: "#22c55e"},
	}, []int{2}, nil)
	if !ok || configID == 0 {
		t.Fatalf("ensureJiraBoardConfiguration = (%d, %v), want created", configID, ok)
	}
	var columnCount, statusCount, mappingCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM board_columns WHERE board_configuration_id = ?`, configID).Scan(&columnCount); err != nil {
		t.Fatalf("count columns: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM board_column_statuses bcs JOIN board_columns bc ON bc.id = bcs.board_column_id WHERE bc.board_configuration_id = ?`, configID).Scan(&statusCount); err != nil {
		t.Fatalf("count statuses: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM jira_import_id_mappings WHERE job_id = 'job-board' AND entity_type = 'board_configuration' AND jira_id = 'board:77' AND windshift_id = ?`, configID).Scan(&mappingCount); err != nil {
		t.Fatalf("count mapping: %v", err)
	}
	if columnCount != 2 || statusCount != 2 || mappingCount != 1 {
		t.Fatalf("columnCount=%d statusCount=%d mappingCount=%d, want 2/2/1", columnCount, statusCount, mappingCount)
	}
}
