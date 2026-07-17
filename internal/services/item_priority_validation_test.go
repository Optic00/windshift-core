package services

import (
	"path/filepath"
	"testing"

	"windshift/internal/database"
)

func TestIsPriorityAllowedInWorkspaceHonorsConfigurationSet(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "priority-validation.db"))
	if err != nil {
		t.Fatalf("NewSQLiteDB: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var unrestrictedWorkspaceID, restrictedWorkspaceID int
	if err := db.QueryRow(`INSERT INTO workspaces (name, key) VALUES ('Unrestricted', 'UPR') RETURNING id`).Scan(&unrestrictedWorkspaceID); err != nil {
		t.Fatalf("insert unrestricted workspace: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO workspaces (name, key) VALUES ('Restricted', 'RPR') RETURNING id`).Scan(&restrictedWorkspaceID); err != nil {
		t.Fatalf("insert restricted workspace: %v", err)
	}

	var configSetID, allowedPriorityID, blockedPriorityID int
	if err := db.QueryRow(`INSERT INTO configuration_sets (name) VALUES ('Priority test') RETURNING id`).Scan(&configSetID); err != nil {
		t.Fatalf("insert config set: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO priorities (name) VALUES ('Allowed priority') RETURNING id`).Scan(&allowedPriorityID); err != nil {
		t.Fatalf("insert allowed priority: %v", err)
	}
	if err := db.QueryRow(`INSERT INTO priorities (name) VALUES ('Blocked priority') RETURNING id`).Scan(&blockedPriorityID); err != nil {
		t.Fatalf("insert blocked priority: %v", err)
	}
	if _, err := db.ExecWrite(`INSERT INTO workspace_configuration_sets (workspace_id, configuration_set_id) VALUES (?, ?)`, restrictedWorkspaceID, configSetID); err != nil {
		t.Fatalf("attach config set: %v", err)
	}
	if _, err := db.ExecWrite(`INSERT INTO configuration_set_priorities (configuration_set_id, priority_id) VALUES (?, ?)`, configSetID, allowedPriorityID); err != nil {
		t.Fatalf("attach priority: %v", err)
	}

	assertAllowed := func(workspaceID, priorityID int, want bool) {
		t.Helper()
		got, err := IsPriorityAllowedInWorkspace(db, workspaceID, priorityID)
		if err != nil {
			t.Fatalf("IsPriorityAllowedInWorkspace(%d, %d): %v", workspaceID, priorityID, err)
		}
		if got != want {
			t.Fatalf("IsPriorityAllowedInWorkspace(%d, %d) = %v, want %v", workspaceID, priorityID, got, want)
		}
	}

	assertAllowed(unrestrictedWorkspaceID, blockedPriorityID, true)
	assertAllowed(restrictedWorkspaceID, allowedPriorityID, true)
	assertAllowed(restrictedWorkspaceID, blockedPriorityID, false)
	assertAllowed(restrictedWorkspaceID, 1_000_000, false)
}
