package services

import (
	"strings"
	"testing"
	"time"

	"windshift/internal/database"
)

// pagePermTestEnv spins up a fully-initialized DB with gated workspace 1
// (Viewer/Editor/Admin all assigned) so the evaluator can be exercised
// without the open-by-default mode masking permission bugs.
type pagePermTestEnv struct {
	db     database.Database
	perm   *PermissionService
	pages  *PageService
	auth   *PagePermissionService
	users  map[string]int
	roleID map[string]int
}

func newPagePermTestEnv(t *testing.T) *pagePermTestEnv {
	t.Helper()
	dsn := "file:permtest-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := database.NewSQLiteDB(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfg := DefaultPermissionCacheConfig()
	cfg.WarmupOnStartup = false
	cfg.TTL = 1 * time.Minute
	perm, err := NewPermissionService(db, cfg)
	if err != nil {
		t.Fatalf("perm: %v", err)
	}
	t.Cleanup(func() { perm.Close() })

	users := map[string]int{}
	for _, name := range []string{"alice", "bob", "carol", "phantom"} {
		var id int
		if err := db.QueryRow(
			`INSERT INTO users (email, username, first_name, last_name, password_hash, is_active)
			 VALUES (?, ?, ?, ?, 'h', 1) RETURNING id`,
			name+"@x", name, name, name,
		).Scan(&id); err != nil {
			t.Fatalf("seed user %s: %v", name, err)
		}
		users[name] = id
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, key, active) VALUES (1, 'WS', 'WS1', 1)`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	roleID := map[string]int{}
	for _, role := range []string{"Viewer", "Editor", "Administrator"} {
		var id int
		if err := db.QueryRow(`SELECT id FROM workspace_roles WHERE name = ?`, role).Scan(&id); err != nil {
			t.Fatalf("look up %s: %v", role, err)
		}
		roleID[role] = id
	}

	// Gate the workspace: alice→Editor, bob→Administrator, phantom→Viewer.
	// carol stays unassigned (true outsider).
	if _, err := db.Exec(`
		INSERT INTO user_workspace_roles (user_id, workspace_id, role_id, granted_at)
		VALUES (?, 1, ?, CURRENT_TIMESTAMP),
		       (?, 1, ?, CURRENT_TIMESTAMP),
		       (?, 1, ?, CURRENT_TIMESTAMP)
	`,
		users["alice"], roleID["Editor"],
		users["bob"], roleID["Administrator"],
		users["phantom"], roleID["Viewer"],
	); err != nil {
		t.Fatalf("seed user roles: %v", err)
	}

	return &pagePermTestEnv{
		db:     db,
		perm:   perm,
		pages:  NewPageService(db),
		auth:   NewPagePermissionService(db, perm),
		users:  users,
		roleID: roleID,
	}
}

func TestPagePermission_OpenPageFallsBackToWorkspaceRole(t *testing.T) {
	env := newPagePermTestEnv(t)
	page, err := env.pages.Create(env.users["alice"], CreatePageInput{WorkspaceID: 1, Title: "Open"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	can, err := env.auth.Can(env.users["alice"], 1, page.ID, PageOpEdit)
	if err != nil || !can {
		t.Errorf("editor alice should be able to edit: can=%v err=%v", can, err)
	}
	can, err = env.auth.Can(env.users["carol"], 1, page.ID, PageOpView)
	if err != nil || can {
		t.Errorf("outsider carol should NOT view the open page: can=%v err=%v", can, err)
	}
}

func TestPagePermission_InheritFalse_EmptyACL_IsAdminOnly(t *testing.T) {
	env := newPagePermTestEnv(t)
	page, _ := env.pages.Create(env.users["alice"], CreatePageInput{WorkspaceID: 1, Title: "Locked"})

	// Break inheritance with no ACL rows → admin-only fallback.
	if _, err := env.pages.SetInheritPermissions(env.users["bob"], page.ID, false); err != nil {
		t.Fatalf("set inherit=false: %v", err)
	}

	// Even alice (Editor with workspace page.view) must be denied now.
	can, err := env.auth.Can(env.users["alice"], 1, page.ID, PageOpView)
	if err != nil || can {
		t.Errorf("editor alice should be denied on locked page: can=%v err=%v", can, err)
	}
	// Admin still sees everything.
	can, err = env.auth.Can(env.users["bob"], 1, page.ID, PageOpView)
	if err != nil || !can {
		t.Errorf("admin bob must still view a locked page: can=%v err=%v", can, err)
	}
}

func TestPagePermission_InheritFalse_WithACL_GrantsAccess(t *testing.T) {
	env := newPagePermTestEnv(t)
	page, _ := env.pages.Create(env.users["alice"], CreatePageInput{WorkspaceID: 1, Title: "Restricted"})
	if _, err := env.pages.SetInheritPermissions(env.users["bob"], page.ID, false); err != nil {
		t.Fatalf("break inheritance: %v", err)
	}

	// Without ACL, carol can't see it. Add a user grant for carol.
	if _, err := env.pages.GrantPermission(env.users["bob"], page.ID, "user", env.users["carol"], "view"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	can, err := env.auth.Can(env.users["carol"], 1, page.ID, PageOpView)
	if err != nil || !can {
		t.Errorf("carol with explicit user grant must view: can=%v err=%v", can, err)
	}

	// Edit still denied — only view granted.
	can, err = env.auth.Can(env.users["carol"], 1, page.ID, PageOpEdit)
	if err != nil || can {
		t.Errorf("carol with view-only grant must NOT edit: can=%v err=%v", can, err)
	}
}

func TestPagePermission_RoleGrant_OnRestrictedPage(t *testing.T) {
	env := newPagePermTestEnv(t)
	page, _ := env.pages.Create(env.users["alice"], CreatePageInput{WorkspaceID: 1, Title: "RoleGated"})
	if _, err := env.pages.SetInheritPermissions(env.users["bob"], page.ID, false); err != nil {
		t.Fatalf("break: %v", err)
	}
	// Grant the Editor role view access on this page.
	if _, err := env.pages.GrantPermission(env.users["bob"], page.ID, "role", env.roleID["Editor"], "view"); err != nil {
		t.Fatalf("grant role: %v", err)
	}
	// Alice (Editor) gets in via the role.
	can, err := env.auth.Can(env.users["alice"], 1, page.ID, PageOpView)
	if err != nil || !can {
		t.Errorf("editor alice should view via role grant: can=%v err=%v", can, err)
	}
	// Phantom (Viewer) does not.
	can, err = env.auth.Can(env.users["phantom"], 1, page.ID, PageOpView)
	if err != nil || can {
		t.Errorf("viewer phantom must NOT see role-gated-to-editors page: can=%v err=%v", can, err)
	}
}

func TestPagePermission_InheritedACLFromAncestor(t *testing.T) {
	env := newPagePermTestEnv(t)
	parent, _ := env.pages.Create(env.users["alice"], CreatePageInput{WorkspaceID: 1, Title: "P"})
	if _, err := env.pages.SetInheritPermissions(env.users["bob"], parent.ID, false); err != nil {
		t.Fatalf("break parent inheritance: %v", err)
	}
	// Parent restricted but grants alice view.
	if _, err := env.pages.GrantPermission(env.users["bob"], parent.ID, "user", env.users["alice"], "view"); err != nil {
		t.Fatalf("grant on parent: %v", err)
	}
	// Child inherits.
	child, _ := env.pages.Create(env.users["alice"], CreatePageInput{WorkspaceID: 1, ParentID: &parent.ID, Title: "C"})

	can, err := env.auth.Can(env.users["alice"], 1, child.ID, PageOpView)
	if err != nil || !can {
		t.Errorf("alice should inherit parent grant for child: can=%v err=%v", can, err)
	}
	can, err = env.auth.Can(env.users["carol"], 1, child.ID, PageOpView)
	if err != nil || can {
		t.Errorf("carol (no grant) should NOT inherit parent restriction-passthrough: can=%v err=%v", can, err)
	}
}
