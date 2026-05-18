package services

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"windshift/internal/database"
)

// TestCreateItem_MissingItemTypeRejected verifies that CreateItem returns
// ErrMissingItemType when the caller omits item_type_id and neither a
// workspace-scoped default (via configuration_sets) nor a global is_default
// item type can be resolved. Prevents tasks from being silently created
// with NULL item_type (WI-14, bug 2).
func TestCreateItem_MissingItemTypeRejected(t *testing.T) {
	dsn := fmt.Sprintf("file:create-item-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := database.NewSQLiteDB(dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, stmt := range []string{
		`CREATE TABLE workspaces (id INTEGER PRIMARY KEY, name TEXT, key TEXT)`,
		`CREATE TABLE item_types (id INTEGER PRIMARY KEY, name TEXT, is_default INTEGER DEFAULT 0)`,
		`CREATE TABLE configuration_sets (id INTEGER PRIMARY KEY, default_item_type_id INTEGER, is_default INTEGER DEFAULT 0)`,
		`CREATE TABLE workspace_configuration_sets (workspace_id INTEGER, configuration_set_id INTEGER)`,
		`INSERT INTO workspaces (id, name, key) VALUES (1, 'ws', 'WS')`,
	} {
		if _, execErr := db.Exec(stmt); execErr != nil {
			t.Fatalf("exec %q: %v", stmt, execErr)
		}
	}

	_, err = CreateItem(db, ItemCreationParams{
		WorkspaceID: 1,
		Title:       "no type",
	})
	if !errors.Is(err, ErrMissingItemType) {
		t.Fatalf("expected ErrMissingItemType, got %v", err)
	}
}
