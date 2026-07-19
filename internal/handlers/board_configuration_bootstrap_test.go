package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"windshift/internal/contextkeys"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

func TestBoardConfigurationBootstrapCombinesCollectionConfigAndWorkspaceStatuses(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "board-configuration-bootstrap.db"))
	if err != nil {
		t.Fatalf("NewSQLiteDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	insertID := func(label, query string, args ...interface{}) int {
		t.Helper()
		result, err := db.ExecWrite(query, args...)
		if err != nil {
			t.Fatalf("insert %s: %v", label, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("LastInsertId for %s: %v", label, err)
		}
		return int(id)
	}

	userID := insertID("user", `INSERT INTO users (email, username, first_name, last_name) VALUES ('board@example.test', 'board-editor', 'Board', 'Editor')`)
	grant, err := db.ExecWrite(`
		INSERT INTO user_global_permissions (user_id, permission_id)
		SELECT ?, id FROM permissions WHERE permission_key = ?
	`, userID, models.PermissionItemView)
	if err != nil {
		t.Fatalf("grant item.view: %v", err)
	}
	if affected, _ := grant.RowsAffected(); affected != 1 {
		t.Fatalf("item.view grant rows = %d, want 1", affected)
	}

	categoryID := insertID("category", `INSERT INTO status_categories (name, color) VALUES ('Board bootstrap', '#abcdef')`)
	statusA := insertID("status A", `INSERT INTO statuses (name, category_id) VALUES ('Bootstrap A', ?)`, categoryID)
	statusB := insertID("status B", `INSERT INTO statuses (name, category_id) VALUES ('Bootstrap B', ?)`, categoryID)
	statusC := insertID("status C", `INSERT INTO statuses (name, category_id) VALUES ('Bootstrap C', ?)`, categoryID)
	statusD := insertID("status D", `INSERT INTO statuses (name, category_id) VALUES ('Bootstrap D', ?)`, categoryID)
	workflowOne := insertID("workflow one", `INSERT INTO workflows (name) VALUES ('Bootstrap workflow one')`)
	workflowTwo := insertID("workflow two", `INSERT INTO workflows (name) VALUES ('Bootstrap workflow two')`)
	if _, err := db.ExecWrite(
		`INSERT INTO workflow_transitions (workflow_id, from_status_id, to_status_id) VALUES (?, ?, ?), (?, ?, ?)`,
		workflowOne, statusA, statusB, workflowTwo, statusC, statusD,
	); err != nil {
		t.Fatalf("insert transitions: %v", err)
	}
	configSetOne := insertID("configuration set one", `INSERT INTO configuration_sets (name, workflow_id) VALUES ('Bootstrap set one', ?)`, workflowOne)
	configSetTwo := insertID("configuration set two", `INSERT INTO configuration_sets (name, workflow_id) VALUES ('Bootstrap set two', ?)`, workflowTwo)
	workspaceOne := insertID("workspace one", `INSERT INTO workspaces (name, key) VALUES ('Bootstrap workspace one', 'BC1')`)
	workspaceTwo := insertID("workspace two", `INSERT INTO workspaces (name, key) VALUES ('Bootstrap workspace two', 'BC2')`)
	if _, err := db.ExecWrite(
		`INSERT INTO workspace_configuration_sets (workspace_id, configuration_set_id) VALUES (?, ?), (?, ?)`,
		workspaceOne, configSetOne, workspaceTwo, configSetTwo,
	); err != nil {
		t.Fatalf("assign configuration sets: %v", err)
	}
	for number, workspaceID := range []int{workspaceOne, workspaceTwo} {
		if _, err := db.ExecWrite(
			`INSERT INTO items (workspace_id, workspace_item_number, title) VALUES (?, ?, 'Bootstrap board item')`,
			workspaceID, number+1,
		); err != nil {
			t.Fatalf("insert item for workspace %d: %v", workspaceID, err)
		}
	}
	collectionID := insertID(
		"collection",
		`INSERT INTO collections (name, ql_query, is_public, created_by) VALUES ('Bootstrap collection', 'title ~ "Bootstrap board"', false, ?)`,
		userID,
	)
	boardRepo := repository.NewBoardConfigurationRepository(db)
	boardConfigID, err := boardRepo.Create(&collectionID, nil, &models.BoardConfigurationRequest{
		Columns: []models.BoardColumnRequest{{Name: "Ready", StatusIDs: []int{statusA}}},
	})
	if err != nil {
		t.Fatalf("create board configuration: %v", err)
	}

	permissionService, err := services.NewPermissionService(db, services.PermissionCacheConfig{
		TTL: 0, MaxCacheSize: 8, WarmupOnStartup: false, PreWarmActive: false, BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("NewPermissionService: %v", err)
	}
	t.Cleanup(func() { _ = permissionService.Close() })
	handler := NewBoardConfigurationHandler(
		boardRepo,
		repository.NewCollectionRepository(db),
		permissionService,
		services.NewItemCRUDService(db),
		services.NewWorkspaceService(db),
	)
	request := httptest.NewRequest(
		http.MethodGet,
		fmt.Sprintf("/api/collections/%d/board-configuration/bootstrap?workspace_id=%d", collectionID, workspaceOne),
		nil,
	)
	request.SetPathValue("id", fmt.Sprintf("%d", collectionID))
	request = request.WithContext(context.WithValue(request.Context(), contextkeys.User, &models.User{ID: userID}))
	recorder := httptest.NewRecorder()

	handler.GetBootstrap(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var response boardConfigurationBootstrapResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Collection == nil || response.Collection.ID != collectionID || response.Collection.QLQuery == "" {
		t.Fatalf("collection = %+v, want complete collection metadata", response.Collection)
	}
	if response.BoardConfiguration == nil || response.BoardConfiguration.ID != boardConfigID || len(response.BoardConfiguration.Columns) != 1 {
		t.Fatalf("board configuration = %+v, want config %d with its column", response.BoardConfiguration, boardConfigID)
	}
	if len(response.ReferencedWorkspaceIDs) != 2 || response.ReferencedWorkspaceIDs[0] != workspaceOne || response.ReferencedWorkspaceIDs[1] != workspaceTwo {
		t.Fatalf("referenced workspaces = %v, want [%d %d]", response.ReferencedWorkspaceIDs, workspaceOne, workspaceTwo)
	}
	statusIDs := make(map[int]bool, len(response.Statuses))
	for _, status := range response.Statuses {
		statusIDs[status.ID] = true
	}
	for _, statusID := range []int{statusA, statusB, statusC, statusD} {
		if !statusIDs[statusID] {
			t.Fatalf("statuses = %+v, missing %d", response.Statuses, statusID)
		}
	}
}
