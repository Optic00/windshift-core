package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"windshift/internal/auth"
	"windshift/internal/database"
	"windshift/internal/handlers"
	"windshift/internal/integrations/netbox"
	"windshift/internal/logger"
	"windshift/internal/middleware"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/router"
	"windshift/internal/routes"
	"windshift/internal/services"
)

type netBoxHTTPFixture struct {
	db                                                                                  database.Database
	service                                                                             *services.NetBoxService
	permissions                                                                         *services.PermissionService
	connection                                                                          *models.NetBoxConnection
	mux                                                                                 *http.ServeMux
	cookies                                                                             map[int]string
	admin, editor, viewer, outsider, workspace, otherWorkspace, item, otherItem, status int
	remoteStatus                                                                        int
	remoteCalls                                                                         int
}

func netBoxHTTPInsert(t *testing.T, db database.Database, query string, args ...any) int {
	t.Helper()
	result, err := db.ExecWrite(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}

func newNetBoxHTTPFixture(t *testing.T) *netBoxHTTPFixture {
	t.Helper()
	db, err := database.NewSQLiteDB(t.TempDir() + "/netbox-http.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}
	f := &netBoxHTTPFixture{db: db, mux: http.NewServeMux(), cookies: map[int]string{}, remoteStatus: http.StatusOK}
	users := []*int{&f.admin, &f.editor, &f.viewer, &f.outsider}
	for i, target := range users {
		*target = netBoxHTTPInsert(t, db, `INSERT INTO users(email,username,first_name,last_name) VALUES (?,?, 'NetBox','HTTP')`, fmt.Sprintf("nbhttp%d@example.test", i), fmt.Sprintf("nbhttp%d", i))
	}
	if _, err := db.ExecWrite(`INSERT INTO user_global_permissions(user_id,permission_id) SELECT ?,id FROM permissions WHERE permission_key='system.admin'`, f.admin); err != nil {
		t.Fatal(err)
	}
	f.workspace = netBoxHTTPInsert(t, db, "INSERT INTO workspaces(name,key) VALUES ('HTTP primary','NHP')")
	f.otherWorkspace = netBoxHTTPInsert(t, db, "INSERT INTO workspaces(name,key) VALUES ('HTTP other','NHO')")
	for _, grant := range []struct {
		user, workspace int
		role            string
	}{{f.viewer, f.workspace, "viewer"}, {f.editor, f.workspace, "editor"}, {f.admin, f.otherWorkspace, "viewer"}} {
		if _, err := db.ExecWrite(`INSERT INTO user_workspace_roles(user_id,workspace_id,role_id) SELECT ?,?,id FROM workspace_roles WHERE builtin_key=?`, grant.user, grant.workspace, grant.role); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow("SELECT id FROM statuses ORDER BY id LIMIT 1").Scan(&f.status); err != nil {
		t.Fatal(err)
	}
	f.item = netBoxHTTPInsert(t, db, `INSERT INTO items(workspace_id,workspace_item_number,title,description,frac_index,status_id,creator_id,last_active_at) VALUES (?,1,'HTTP item','','a0',?,?,CURRENT_TIMESTAMP)`, f.workspace, f.status, f.admin)
	f.otherItem = netBoxHTTPInsert(t, db, `INSERT INTO items(workspace_id,workspace_item_number,title,description,frac_index,status_id,creator_id,last_active_at) VALUES (?,2,'Other HTTP item','','a1',?,?,CURRENT_TIMESTAMP)`, f.workspace, f.status, f.admin)
	f.permissions, err = services.NewPermissionService(db, services.PermissionCacheConfig{TTL: time.Minute, MaxCacheSize: 8, BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.permissions.Close() })
	credentials := services.NewActionCredentialService(repository.NewActionCredentialRepository(db), "synthetic-netbox-http-secret")
	f.service = services.NewNetBoxService(db, repository.NewNetBoxRepository(db), credentials, f.permissions)
	f.service.SetTransportForTesting(netbox.TransportFunc(func(_ context.Context, method, target string, _ []byte, _ map[string]string) (*netbox.Response, error) {
		f.remoteCalls++
		if method != http.MethodGet {
			t.Fatal("HTTP route caused NetBox write")
		}
		body := `{"id":42,"name":"SNAPSHOT_PRIVATE_NAME","custom_fields":{"password":"RAW_REMOTE_SECRET"},"url":"https://untrusted.example.test"}`
		if strings.HasSuffix(strings.Split(target, "?")[0], "/devices/") {
			body = `{"count":1,"results":[` + body + `]}`
		}
		if f.remoteStatus != http.StatusOK {
			body = `{"error":"RAW_REMOTE_PRIVATE_ERROR","token":"nbt_key.synthetic"}`
		}
		return &netbox.Response{StatusCode: f.remoteStatus, Body: []byte(body)}, nil
	}))
	all := false
	f.connection, err = f.service.CreateConnection(models.CreateNetBoxConnectionRequest{Slug: "netbox-http", Name: "NetBox HTTP", BaseURL: "https://netbox.example.test", AuthScheme: models.NetBoxAuthBearer, APIToken: "nbt_key.synthetic", AppliesToAllWorkspaces: &all, WorkspaceIDs: []int{f.workspace}}, f.admin)
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionManager(db, false, false, nil, "synthetic-netbox-cookie-secret", "none")
	t.Cleanup(func() { _ = sessions.Close() })
	for _, user := range []int{f.admin, f.editor, f.viewer, f.outsider} {
		session, err := sessions.CreateSession(user, "192.0.2.1", "NetBox security test", false)
		if err != nil {
			t.Fatal(err)
		}
		f.cookies[user], err = sessions.EncodeSessionCookieValue(session.Token)
		if err != nil {
			t.Fatal(err)
		}
	}
	authMiddleware := middleware.NewAuthMiddleware(sessions, nil, db, false, nil, true)
	handler := handlers.NewNetBoxHandler(f.service, repository.NewItemRepository(db), f.permissions, logger.NewAuditor(db))
	deps := &routes.Deps{API: router.NewRouteGroup(f.mux, "/api", authMiddleware.OptionalAuth, middleware.CSRFProtection([]string{"https://windshift.example.test"})), Mux: f.mux, AuthMiddleware: authMiddleware, PermissionMiddleware: middleware.NewPermissionMiddleware(db, f.permissions), Integrations: routes.IntegrationHandlers{NetBox: handler}}
	routes.RegisterIntegrationRoutes(deps)
	return f
}

func (f *netBoxHTTPFixture) request(t *testing.T, method, path string, user int, body any, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.RemoteAddr = "192.0.2.1:4321"
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if user != 0 {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: f.cookies[user]})
	}
	result := httptest.NewRecorder()
	f.mux.ServeHTTP(result, req)
	return result
}

func (f *netBoxHTTPFixture) link(t *testing.T) *models.NetBoxItemLink {
	t.Helper()
	response := f.request(t, http.MethodPost, fmt.Sprintf("/api/items/%d/netbox-links", f.item), f.editor, models.CreateNetBoxItemLinkRequest{ConnectionID: f.connection.ProviderID, ObjectType: models.NetBoxDevice, ObjectID: 42}, "https://windshift.example.test")
	if response.Code != http.StatusCreated {
		t.Fatalf("link response=%d %s", response.Code, response.Body.String())
	}
	var link models.NetBoxItemLink
	if err := json.Unmarshal(response.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	return &link
}

func TestNetBoxHTTPRegisteredRoutesRequireSessionAdminAndCSRF(t *testing.T) {
	f := newNetBoxHTTPFixture(t)
	for _, test := range []struct {
		method, path string
		user         int
		origin       string
		want         int
	}{
		{http.MethodGet, "/api/admin/netbox-connections", 0, "", 401},
		{http.MethodGet, "/api/admin/netbox-connections", f.viewer, "", 403},
		{http.MethodGet, "/api/admin/netbox-connections", f.admin, "", 200},
		{http.MethodPost, "/api/admin/netbox-connections", f.admin, "", 403},
		{http.MethodPost, "/api/admin/netbox-connections", f.admin, "https://attacker.example.test", 403},
		{http.MethodGet, fmt.Sprintf("/api/items/%d/netbox-links", f.item), 0, "", 401},
		{http.MethodGet, fmt.Sprintf("/api/items/%d/netbox-links", f.item), f.viewer, "", 200},
		{http.MethodGet, fmt.Sprintf("/api/items/%d/netbox-links", f.item), f.outsider, "", 404},
		{http.MethodGet, fmt.Sprintf("/api/workspaces/%d/netbox-connections/%s/search?object_type=dcim.device&q=device", f.workspace, f.connection.ProviderID), f.viewer, "", 404},
		{http.MethodGet, fmt.Sprintf("/api/workspaces/%d/netbox-connections/%s/search?object_type=dcim.device&q=device", f.otherWorkspace, f.connection.ProviderID), f.admin, "", 404},
		{http.MethodGet, "/api/v2/admin/netbox-connections", f.admin, "", 404},
		{http.MethodGet, "/rest/api/v2/admin/netbox-connections", f.admin, "", 404},
	} {
		response := f.request(t, test.method, test.path, test.user, nil, test.origin)
		if response.Code != test.want {
			t.Errorf("%s %s actor=%d: status=%d want=%d body=%s", test.method, test.path, test.user, response.Code, test.want, response.Body.String())
		}
	}
	response := f.request(t, http.MethodPost, "/api/admin/netbox-connections", f.admin, map[string]any{"slug": "missing-scope", "name": "Missing scope", "base_url": "https://netbox.example.test", "api_token": "nbt_key.synthetic"}, "https://windshift.example.test")
	if response.Code != 400 || strings.Contains(response.Body.String(), "nbt_key.synthetic") {
		t.Fatalf("invalid scope response unsafe: %d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/api/admin/netbox-connections", fmt.Sprintf("/api/items/%d/netbox-links", f.item)} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer crw_synthetic_test_only")
		result := httptest.NewRecorder()
		f.mux.ServeHTTP(result, request)
		if result.Code != http.StatusUnauthorized {
			t.Fatalf("public API bearer authenticated on internal session route %s: %d %s", path, result.Code, result.Body.String())
		}
	}
	response = f.request(t, http.MethodPost, fmt.Sprintf("/api/items/%d/netbox-links", f.item), f.viewer,
		models.CreateNetBoxItemLinkRequest{ConnectionID: f.connection.ProviderID, ObjectType: models.NetBoxDevice, ObjectID: 42}, "https://windshift.example.test")
	if response.Code != http.StatusNotFound {
		t.Fatalf("read-only viewer linked an object: %d %s", response.Code, response.Body.String())
	}
}

func TestNetBoxHTTPNestedLinkIsolationSafeErrorsAndAudit(t *testing.T) {
	f := newNetBoxHTTPFixture(t)
	link := f.link(t)
	visible := f.request(t, http.MethodGet, fmt.Sprintf("/api/items/%d/netbox-links", f.item), f.viewer, nil, "")
	if visible.Code != http.StatusOK || !strings.Contains(visible.Body.String(), "SNAPSHOT_PRIVATE_NAME") {
		t.Fatalf("authorized viewer could not read selected snapshot: %d %s", visible.Code, visible.Body.String())
	}
	for _, method := range []string{http.MethodDelete, http.MethodPost} {
		path := fmt.Sprintf("/api/items/%d/netbox-links/%s", f.otherItem, link.ID)
		if method == http.MethodPost {
			path += "/refresh"
		}
		response := f.request(t, method, path, f.editor, nil, "https://windshift.example.test")
		if response.Code != 404 {
			t.Fatalf("cross-item operation accepted: %s status=%d %s", method, response.Code, response.Body.String())
		}
	}
	if _, err := repository.NewNetBoxRepository(f.db).GetVisibleLink(f.item, f.workspace, link.ID); err != nil {
		t.Fatalf("cross-item operation altered original link: %v", err)
	}
	f.remoteStatus = http.StatusForbidden
	response := f.request(t, http.MethodPost, fmt.Sprintf("/api/items/%d/netbox-links/%s/refresh", f.item, link.ID), f.editor, nil, "https://windshift.example.test")
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "RAW_REMOTE") || strings.Contains(response.Body.String(), "nbt_key") {
		t.Fatalf("remote error disclosure: %d %s", response.Code, response.Body.String())
	}
	rows, err := f.db.Query("SELECT COALESCE(details,''),COALESCE(resource_name,'') FROM audit_logs WHERE action_type LIKE 'netbox_%'")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var details, name string
		if err := rows.Scan(&details, &name); err != nil {
			t.Fatal(err)
		}
		count++
		if strings.Contains(details+name, "SNAPSHOT_PRIVATE_NAME") || strings.Contains(details+name, "RAW_REMOTE") || strings.Contains(details+name, "nbt_key") || strings.Contains(details, "url") {
			t.Fatalf("audit leaked snapshot/secret: %s %s", details, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("successful link audit count=%d", count)
	}
	_ = rows.Close()
	response = f.request(t, http.MethodDelete, fmt.Sprintf("/api/items/%d/netbox-links/%s", f.item, link.ID), f.editor, nil, "https://windshift.example.test")
	if response.Code != 204 || response.Body.Len() != 0 {
		t.Fatalf("unlink response=%d body=%q", response.Code, response.Body.String())
	}
}

func TestNetBoxHTTPRelinkReturnsExistingWithoutCreateAudit(t *testing.T) {
	f := newNetBoxHTTPFixture(t)
	original := f.link(t)
	response := f.request(t, http.MethodPost, fmt.Sprintf("/api/items/%d/netbox-links", f.item), f.editor,
		models.CreateNetBoxItemLinkRequest{ConnectionID: f.connection.ProviderID, ObjectType: models.NetBoxDevice, ObjectID: 42}, "https://windshift.example.test")
	if response.Code != http.StatusOK {
		t.Errorf("re-link status=%d, want 200 for existing link", response.Code)
	}
	var existing models.NetBoxItemLink
	if err := json.Unmarshal(response.Body.Bytes(), &existing); err != nil || existing.ID != original.ID || existing.Revision != original.Revision {
		t.Fatalf("re-link changed identity or snapshot: %#v %v", existing, err)
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM audit_logs WHERE action_type='netbox_link.create'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("re-link wrote an extra create audit: count=%d", count)
	}
}

func TestNetBoxHTTPApproverOnlyAccessDoesNotExposeSnapshots(t *testing.T) {
	f := newNetBoxHTTPFixture(t)
	f.link(t)
	workflow := netBoxHTTPInsert(t, f.db, "INSERT INTO workflows(name) VALUES ('NetBox approval isolation')")
	transition := netBoxHTTPInsert(t, f.db, "INSERT INTO workflow_transitions(workflow_id,to_status_id) VALUES (?,?)", workflow, f.status)
	set := netBoxHTTPInsert(t, f.db, "INSERT INTO approval_sets(name,workflow_id) VALUES ('NetBox approval',?)", workflow)
	setStatus := netBoxHTTPInsert(t, f.db, "INSERT INTO approval_set_statuses(approval_set_id,status_id,approve_transition_id,deny_transition_id) VALUES (?,?,?,?)", set, f.status, transition, transition)
	step := netBoxHTTPInsert(t, f.db, "INSERT INTO approval_steps(approval_set_status_id,name,approver_source) VALUES (?,'External approver','user')", setStatus)
	approval := netBoxHTTPInsert(t, f.db, "INSERT INTO approval_requests(item_id,approval_set_status_id,status_id,triggered_by_user_id) VALUES (?,?,?,?)", f.item, setStatus, f.status, f.admin)
	instance := netBoxHTTPInsert(t, f.db, "INSERT INTO approval_step_instances(approval_request_id,approval_step_id) VALUES (?,?)", approval, step)
	netBoxHTTPInsert(t, f.db, "INSERT INTO approval_step_approvers(approval_step_instance_id,user_id) VALUES (?,?)", instance, f.outsider)
	approvalService := services.NewApprovalService(f.db, nil, nil)
	allowed, err := approvalService.UserHasActivePoolMembershipOnItem(context.Background(), f.outsider, f.item, nil)
	if err != nil || !allowed {
		t.Fatalf("fixture lacks real active approval pool: allowed=%t err=%v", allowed, err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/items/approval-context", nil).WithContext(context.WithValue(context.Background(), middleware.ContextKeyUser, &models.User{ID: f.outsider}))
	if !handlers.CheckItemPermissionAsActor(httptest.NewRecorder(), request, repository.NewItemRepository(f.db), f.permissions, approvalService, f.item, models.PermissionItemView) {
		t.Fatal("fixture should permit ordinary approval-context item view")
	}
	response := f.request(t, http.MethodGet, fmt.Sprintf("/api/items/%d/netbox-links", f.item), f.outsider, nil, "")
	if response.Code != 404 || strings.Contains(response.Body.String(), "SNAPSHOT_PRIVATE_NAME") {
		t.Fatalf("approver-only access exposed NetBox snapshot: %d %s", response.Code, response.Body.String())
	}
}
