package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"windshift/internal/database"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/services"
)

// newPageHandler wires a fully-initialized SQLite DB, the real
// PermissionService, and the page services into a PageHandler suitable for
// integration tests.
func newPageHandler(t *testing.T) (*PageHandler, database.Database, *services.PermissionService) {
	t.Helper()
	db := newNegativeTestDB(t)
	perm := newNegativeTestPermissionService(t, db)
	svc := services.NewPageService(db)
	auth := services.NewPagePermissionService(db, perm)
	h := NewPageHandler(svc, auth, logger.NewAuditor(db))
	return h, db, perm
}

// seedWorkspaceWithRole inserts a workspace and assigns userID the named
// role, plus a second admin user so the workspace is in gated mode (memory:
// open-by-default workspaces don't exercise the 404-not-403 invariant).
func seedWorkspaceWithRole(t *testing.T, db database.Database, workspaceID, userID int, role string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, key, active) VALUES (?, 'WS', ?, 1)`, workspaceID, "WS"+strconv.Itoa(workspaceID)); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	var roleID int
	if err := db.QueryRow(`SELECT id FROM workspace_roles WHERE name = ?`, role).Scan(&roleID); err != nil {
		t.Fatalf("look up role %s: %v", role, err)
	}
	var adminRoleID int
	if err := db.QueryRow(`SELECT id FROM workspace_roles WHERE name = 'Administrator'`).Scan(&adminRoleID); err != nil {
		t.Fatalf("look up Administrator role: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO user_workspace_roles (user_id, workspace_id, role_id, granted_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, userID, workspaceID, roleID); err != nil {
		t.Fatalf("assign workspace role: %v", err)
	}
	// Ensure the workspace is in gated mode by guaranteeing at least one
	// Administrator membership distinct from userID. Seed a stable phantom
	// admin (uid 999) — the only thing that matters is the workspace_roles
	// table sees a non-empty role grant.
	if userID != 999 {
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO user_workspace_roles (user_id, workspace_id, role_id, granted_at)
			VALUES (999, ?, ?, CURRENT_TIMESTAMP)
		`, workspaceID, adminRoleID); err != nil {
			t.Fatalf("assign phantom admin: %v", err)
		}
	}
}

func setPath(req *http.Request, kv map[string]string) {
	for k, v := range kv {
		req.SetPathValue(k, v)
	}
}

func TestPageHandler_GetTree_FiltersToWorkspace(t *testing.T) {
	h, db, _ := newPageHandler(t)
	const userID = 1
	seedNegativeTestUser(t, db, userID)
	seedNegativeTestUser(t, db, 999)
	seedWorkspaceWithRole(t, db, 1, userID, "Editor")
	seedWorkspaceWithRole(t, db, 2, 999, "Administrator")

	// Seed a page in workspace 1 and one in workspace 2.
	if _, err := h.service.Create(userID, services.CreatePageInput{WorkspaceID: 1, Title: "Hello"}); err != nil {
		t.Fatalf("create page ws1: %v", err)
	}
	if _, err := h.service.Create(999, services.CreatePageInput{WorkspaceID: 2, Title: "Other"}); err != nil {
		t.Fatalf("create page ws2: %v", err)
	}

	req := authedRequest(http.MethodGet, "/workspaces/1/pages/tree", userID, nil)
	setPath(req, map[string]string{"workspaceId": "1"})
	rr := httptest.NewRecorder()
	h.GetTree(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp pageTreeResponse
	decodeJSONBody(t, rr, &resp)
	if len(resp.Pages) != 1 || resp.Pages[0].Title != "Hello" {
		t.Errorf("expected exactly the ws1 page, got %+v", resp.Pages)
	}
}

func TestPageHandler_Get_404WhenCrossWorkspace(t *testing.T) {
	h, db, _ := newPageHandler(t)
	const userID = 1
	seedNegativeTestUser(t, db, userID)
	seedNegativeTestUser(t, db, 999)
	seedWorkspaceWithRole(t, db, 1, userID, "Viewer")
	seedWorkspaceWithRole(t, db, 2, 999, "Administrator")

	other, err := h.service.Create(999, services.CreatePageInput{WorkspaceID: 2, Title: "Secret"})
	if err != nil {
		t.Fatalf("seed secret page: %v", err)
	}

	req := authedRequest(http.MethodGet, "/workspaces/1/pages/"+strconv.Itoa(other.ID), userID, nil)
	setPath(req, map[string]string{"workspaceId": "1", "pageId": strconv.Itoa(other.ID)})
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace get: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPageHandler_Create_RejectsViewer(t *testing.T) {
	h, db, _ := newPageHandler(t)
	const userID = 1
	seedNegativeTestUser(t, db, userID)
	seedNegativeTestUser(t, db, 999)
	seedWorkspaceWithRole(t, db, 1, userID, "Viewer")

	body := map[string]interface{}{"title": "ShouldFail"}
	req := authedRequest(http.MethodPost, "/workspaces/1/pages", userID, body)
	setPath(req, map[string]string{"workspaceId": "1"})
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("viewer create: want 404 (no leak), got %d body=%s", rr.Code, rr.Body.String())
	}

	pages, _ := h.service.ListTree(1, false)
	if len(pages) != 0 {
		t.Errorf("page should not have been created by a viewer, got %d", len(pages))
	}
}

func TestPageHandler_Create_AllowsEditor(t *testing.T) {
	h, db, _ := newPageHandler(t)
	const userID = 1
	seedNegativeTestUser(t, db, userID)
	seedNegativeTestUser(t, db, 999)
	seedWorkspaceWithRole(t, db, 1, userID, "Editor")

	body := map[string]interface{}{"title": "Onboarding", "content": "# hi"}
	req := authedRequest(http.MethodPost, "/workspaces/1/pages", userID, body)
	setPath(req, map[string]string{"workspaceId": "1"})
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("editor create: want 201, got %d body=%s", rr.Code, rr.Body.String())
	}
	var got models.Page
	decodeJSONBody(t, rr, &got)
	if got.Title != "Onboarding" {
		t.Errorf("title: want Onboarding, got %q", got.Title)
	}
}

func TestPageHandler_Delete_RequiresPageDelete(t *testing.T) {
	h, db, _ := newPageHandler(t)
	const userID = 1
	seedNegativeTestUser(t, db, userID)
	seedNegativeTestUser(t, db, 999)
	seedWorkspaceWithRole(t, db, 1, userID, "Editor")
	// Editor has page.view/create/edit but not page.delete or page.admin.

	page, err := h.service.Create(userID, services.CreatePageInput{WorkspaceID: 1, Title: "Doomed"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	req := authedRequest(http.MethodDelete, "/workspaces/1/pages/"+strconv.Itoa(page.ID), userID, nil)
	setPath(req, map[string]string{"workspaceId": "1", "pageId": strconv.Itoa(page.ID)})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("editor delete: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}

	got, _ := h.service.GetByID(page.ID)
	if got.ArchivedAt != nil {
		t.Error("page should not be archived after editor delete attempt")
	}
}

func TestPageHandler_Update_BumpsRevisionAndAllowsEditor(t *testing.T) {
	h, db, _ := newPageHandler(t)
	const userID = 1
	seedNegativeTestUser(t, db, userID)
	seedNegativeTestUser(t, db, 999)
	seedWorkspaceWithRole(t, db, 1, userID, "Editor")

	page, err := h.service.Create(userID, services.CreatePageInput{WorkspaceID: 1, Title: "T", Content: "v1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	body := map[string]interface{}{"title": "T", "content": "v2"}
	req := authedRequest(http.MethodPut, "/workspaces/1/pages/"+strconv.Itoa(page.ID), userID, body)
	setPath(req, map[string]string{"workspaceId": "1", "pageId": strconv.Itoa(page.ID)})
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("editor update: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	revs, _ := h.service.ListRevisions(page.ID, 0, 0)
	if len(revs) != 2 {
		t.Errorf("expected 2 revisions after update, got %d", len(revs))
	}
}

func TestPageHandler_History_404OnCrossPageRevision(t *testing.T) {
	h, db, _ := newPageHandler(t)
	const userID = 1
	seedNegativeTestUser(t, db, userID)
	seedNegativeTestUser(t, db, 999)
	seedWorkspaceWithRole(t, db, 1, userID, "Editor")

	a, _ := h.service.Create(userID, services.CreatePageInput{WorkspaceID: 1, Title: "A"})
	b, _ := h.service.Create(userID, services.CreatePageInput{WorkspaceID: 1, Title: "B"})

	bRevs, _ := h.service.ListRevisions(b.ID, 0, 0)
	if len(bRevs) == 0 {
		t.Fatal("B has no revisions")
	}
	otherRevID := bRevs[0].ID

	req := authedRequest(http.MethodGet, "/workspaces/1/pages/"+strconv.Itoa(a.ID)+"/history/"+strconv.Itoa(otherRevID), userID, nil)
	setPath(req, map[string]string{"workspaceId": "1", "pageId": strconv.Itoa(a.ID), "revisionId": strconv.Itoa(otherRevID)})
	rr := httptest.NewRecorder()
	h.GetRevision(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-page revision get: want 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPageHandler_Permissions_ReturnsEffectiveLevel(t *testing.T) {
	h, db, _ := newPageHandler(t)
	const userID = 1
	seedNegativeTestUser(t, db, userID)
	seedNegativeTestUser(t, db, 999)
	seedWorkspaceWithRole(t, db, 1, userID, "Editor")

	page, err := h.service.Create(userID, services.CreatePageInput{WorkspaceID: 1, Title: "T"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	req := authedRequest(http.MethodGet, "/workspaces/1/pages/"+strconv.Itoa(page.ID)+"/permissions", userID, nil)
	setPath(req, map[string]string{"workspaceId": "1", "pageId": strconv.Itoa(page.ID)})
	rr := httptest.NewRecorder()
	h.GetPermissions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("perms: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp pageEffectivePermissionsResponse
	decodeJSONBody(t, rr, &resp)
	if resp.EffectiveLevel != services.PageOpEdit {
		t.Errorf("effective level: want edit, got %q", resp.EffectiveLevel)
	}
	if !resp.InheritPermissions {
		t.Error("inherit_permissions should default to true on new page")
	}
}

// Ensure JSON encoding stays stable across the handler boundary so the
// frontend can rely on these fields. A pure structural check.
func TestPageHandler_GetTree_JSONShape(t *testing.T) {
	h, db, _ := newPageHandler(t)
	const userID = 1
	seedNegativeTestUser(t, db, userID)
	seedNegativeTestUser(t, db, 999)
	seedWorkspaceWithRole(t, db, 1, userID, "Editor")

	parent, _ := h.service.Create(userID, services.CreatePageInput{WorkspaceID: 1, Title: "Parent"})
	_, _ = h.service.Create(userID, services.CreatePageInput{WorkspaceID: 1, ParentID: &parent.ID, Title: "Child"})

	req := authedRequest(http.MethodGet, "/workspaces/1/pages/tree", userID, nil)
	setPath(req, map[string]string{"workspaceId": "1"})
	rr := httptest.NewRecorder()
	h.GetTree(rr, req)

	var raw map[string]json.RawMessage
	decodeJSONBody(t, rr, &raw)
	if _, ok := raw["pages"]; !ok {
		t.Errorf("response missing pages field, got %v", raw)
	}
	if _, ok := raw["tree"]; !ok {
		t.Errorf("response missing tree field, got %v", raw)
	}
}
