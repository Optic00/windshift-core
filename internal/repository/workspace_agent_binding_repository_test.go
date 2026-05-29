package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"windshift/internal/database"
	"windshift/internal/models"
)

func openBindingTestDB(t *testing.T) (database.Database, int, int, int) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s/bindings_repo.db?mode=memory&cache=shared", t.TempDir())
	db, err := database.NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces(id, name, key, active) VALUES (1, 'WS', 'WS', 1)`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	mkUser := func(email, username string, isAgent bool) int {
		res, err := db.Exec(
			`INSERT INTO users(email, username, first_name, last_name, is_agent) VALUES (?, ?, '', '', ?)`,
			email, username, isAgent,
		)
		if err != nil {
			t.Fatalf("seed user %s: %v", username, err)
		}
		id, _ := res.LastInsertId()
		return int(id)
	}
	admin := mkUser("admin@example.com", "admin", false)
	agentA := mkUser("agent-a@agents.local", "agent-a", true)
	agentB := mkUser("agent-b@agents.local", "agent-b", true)
	return db, admin, agentA, agentB
}

func TestWorkspaceAgentBindingRepository_InsertGetList(t *testing.T) {
	ctx := context.Background()
	db, admin, agentA, agentB := openBindingTestDB(t)
	repo := NewWorkspaceAgentBindingRepository(db)

	id, err := repo.Insert(ctx, &models.WorkspaceAgentBinding{
		WorkspaceID:     1,
		ActingUserID:    agentA,
		ActingUserKind:  "agent",
		RepoSlug:        "acme/widget",
		RepoRemoteURL:   "https://github.com/acme/widget",
		RepoBaseRef:     "main",
		TokenScopes:     []string{"items:read", "items:write"},
		TokenTTLMinutes: 30,
		CreatedByUserID: admin,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RepoSlug != "acme/widget" {
		t.Errorf("RepoSlug: want acme/widget, got %q", got.RepoSlug)
	}
	if got.TokenTTLMinutes != 30 {
		t.Errorf("TTL: want 30, got %d", got.TokenTTLMinutes)
	}
	if len(got.TokenScopes) != 2 {
		t.Errorf("scopes count: want 2, got %d", len(got.TokenScopes))
	}

	// Second binding in same workspace for a different acting user is OK.
	if _, err := repo.Insert(ctx, &models.WorkspaceAgentBinding{
		WorkspaceID: 1, ActingUserID: agentB, ActingUserKind: "agent", CreatedByUserID: admin,
	}); err != nil {
		t.Fatalf("insert second: %v", err)
	}

	bindings, err := repo.ListForWorkspace(ctx, 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(bindings) != 2 {
		t.Fatalf("list count: want 2, got %d", len(bindings))
	}
}

func TestWorkspaceAgentBindingRepository_InsertRejectsDuplicate(t *testing.T) {
	ctx := context.Background()
	db, admin, agentA, _ := openBindingTestDB(t)
	repo := NewWorkspaceAgentBindingRepository(db)

	if _, err := repo.Insert(ctx, &models.WorkspaceAgentBinding{
		WorkspaceID: 1, ActingUserID: agentA, ActingUserKind: "agent", CreatedByUserID: admin,
	}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err := repo.Insert(ctx, &models.WorkspaceAgentBinding{
		WorkspaceID: 1, ActingUserID: agentA, ActingUserKind: "agent", CreatedByUserID: admin,
	})
	if !errors.Is(err, ErrBindingDuplicate) {
		t.Errorf("err: want ErrBindingDuplicate, got %v", err)
	}
}

func TestWorkspaceAgentBindingRepository_FindByActingUser(t *testing.T) {
	ctx := context.Background()
	db, admin, agentA, _ := openBindingTestDB(t)
	repo := NewWorkspaceAgentBindingRepository(db)

	missing, err := repo.FindByActingUser(ctx, 1, agentA)
	if err != nil {
		t.Fatalf("find missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil, got %+v", missing)
	}

	if _, err := repo.Insert(ctx, &models.WorkspaceAgentBinding{
		WorkspaceID: 1, ActingUserID: agentA, ActingUserKind: "agent", CreatedByUserID: admin,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	got, err := repo.FindByActingUser(ctx, 1, agentA)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got == nil || got.ActingUserID != agentA {
		t.Fatalf("expected binding for actingUser=%d, got %+v", agentA, got)
	}
}

func TestWorkspaceAgentBindingRepository_Delete(t *testing.T) {
	ctx := context.Background()
	db, admin, agentA, _ := openBindingTestDB(t)
	repo := NewWorkspaceAgentBindingRepository(db)

	id, err := repo.Insert(ctx, &models.WorkspaceAgentBinding{
		WorkspaceID: 1, ActingUserID: agentA, ActingUserKind: "agent", CreatedByUserID: admin,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	n, err := repo.Delete(ctx, id)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n != 1 {
		t.Errorf("rows: want 1, got %d", n)
	}
	n, _ = repo.Delete(ctx, id)
	if n != 0 {
		t.Errorf("idempotent rowcount: want 0, got %d", n)
	}
}
