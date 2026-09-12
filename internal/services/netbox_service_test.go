package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"windshift/internal/database"
	"windshift/internal/integrations/netbox"
	"windshift/internal/models"
	"windshift/internal/repository"
)

type netBoxTestPermission struct {
	allow bool
	hook  func(int, int, string)
}

func (p *netBoxTestPermission) HasWorkspacePermission(actor, workspace int, permission string) (bool, error) {
	if p.hook != nil {
		p.hook(actor, workspace, permission)
	}
	return p.allow, nil
}

type netBoxServiceFixture struct {
	db                                                       database.Database
	service                                                  *NetBoxService
	repo                                                     *repository.NetBoxRepository
	credentials                                              *ActionCredentialService
	permission                                               *netBoxTestPermission
	connection                                               *models.NetBoxConnection
	admin, actor, workspace, otherWorkspace, item, otherItem int
	requests                                                 int
	status                                                   int
	name                                                     string
	afterGET                                                 func()
}

func netBoxServiceInsert(t *testing.T, db database.Database, query string, args ...any) int {
	t.Helper()
	var id int
	if err := db.QueryRow(query+" RETURNING id", args...).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func newNetBoxServiceTestDB(t *testing.T, driver string) database.Database {
	t.Helper()
	if driver != "postgres" {
		db, err := database.NewSQLiteDB(t.TempDir() + "/netbox.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	dsn := os.Getenv("WINDSHIFT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WINDSHIFT_TEST_POSTGRES_DSN is not set")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("WINDSHIFT_TEST_POSTGRES_DSN must be a PostgreSQL URI")
	}
	admin, err := database.NewPostgresDB(dsn, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("netbox_service_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("remove isolated NetBox service schema: %v", err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := database.NewPostgresDB(parsed.String(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newNetBoxServiceFixture(t *testing.T, drivers ...string) *netBoxServiceFixture {
	t.Helper()
	driver := "sqlite"
	if len(drivers) > 0 {
		driver = drivers[0]
	}
	db := newNetBoxServiceTestDB(t, driver)
	var err error
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}
	f := &netBoxServiceFixture{db: db, repo: repository.NewNetBoxRepository(db), permission: &netBoxTestPermission{allow: true}, status: http.StatusOK, name: "Selected device"}
	f.admin = netBoxServiceInsert(t, db, `INSERT INTO users(email,username,first_name,last_name) VALUES ('netbox-admin@example.test','netbox-admin','NetBox','Admin')`)
	f.actor = netBoxServiceInsert(t, db, `INSERT INTO users(email,username,first_name,last_name) VALUES ('netbox-editor@example.test','netbox-editor','NetBox','Editor')`)
	if _, err := db.ExecWrite(`INSERT INTO user_global_permissions(user_id,permission_id) SELECT ?,id FROM permissions WHERE permission_key='system.admin'`, f.admin); err != nil {
		t.Fatal(err)
	}
	f.workspace = netBoxServiceInsert(t, db, `INSERT INTO workspaces(name,key) VALUES ('NetBox Primary','NBP')`)
	f.otherWorkspace = netBoxServiceInsert(t, db, `INSERT INTO workspaces(name,key) VALUES ('NetBox Other','NBO')`)
	var status int
	if err := db.QueryRow("SELECT id FROM statuses ORDER BY id LIMIT 1").Scan(&status); err != nil {
		t.Fatal(err)
	}
	f.item = netBoxServiceInsert(t, db, `INSERT INTO items(workspace_id,workspace_item_number,title,description,frac_index,status_id,creator_id,last_active_at) VALUES (?,1,'NetBox item','','a0',?,?,CURRENT_TIMESTAMP)`, f.workspace, status, f.admin)
	f.otherItem = netBoxServiceInsert(t, db, `INSERT INTO items(workspace_id,workspace_item_number,title,description,frac_index,status_id,creator_id,last_active_at) VALUES (?,1,'Other item','','a1',?,?,CURRENT_TIMESTAMP)`, f.otherWorkspace, status, f.admin)
	f.credentials = NewActionCredentialService(repository.NewActionCredentialRepository(db), "synthetic-netbox-encryption-secret")
	f.service = NewNetBoxService(db, f.repo, f.credentials, f.permission)
	f.service.SetTransportForTesting(netbox.TransportFunc(func(ctx context.Context, method, target string, body []byte, headers map[string]string) (*netbox.Response, error) {
		f.requests++
		if method != http.MethodGet || len(body) != 0 {
			t.Fatalf("NetBox made a remote write: method=%s", method)
		}
		if !strings.HasPrefix(headers["Authorization"], "Bearer nbt_key.") {
			t.Fatal("missing synthetic bearer authorization")
		}
		parsed, err := url.Parse(target)
		if err != nil {
			return nil, err
		}
		id, parseErr := strconv.ParseInt(strings.TrimSuffix(parsed.Path, "/")[strings.LastIndex(strings.TrimSuffix(parsed.Path, "/"), "/")+1:], 10, 64)
		if parseErr != nil {
			id = 42
		}
		object := map[string]any{"id": id, "name": f.name, "url": "https://untrusted.example.test/secret", "status": map[string]any{"value": "active", "label": "Active"}, "site": map[string]any{"id": 9, "name": "Selected site"}, "custom_fields": map[string]any{"private": "UNSELECTED_REMOTE_SECRET"}, "config_context": map[string]any{"secret": "UNSELECTED_REMOTE_SECRET"}}
		var payload any = object
		if parseErr != nil {
			payload = map[string]any{"count": 1, "next": "https://untrusted.example.test/next", "results": []any{object}}
		}
		if f.status != http.StatusOK {
			payload = map[string]any{"error": "RAW_REMOTE_PRIVATE_ERROR"}
		}
		encoded, err := json.Marshal(payload)
		if f.afterGET != nil {
			f.afterGET()
		}
		return &netbox.Response{StatusCode: f.status, Body: encoded}, err
	}))
	f.connection, err = f.service.CreateConnection(f.request(), f.admin)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *netBoxServiceFixture) request() models.CreateNetBoxConnectionRequest {
	return models.CreateNetBoxConnectionRequest{Slug: "netbox-main", Name: "NetBox Main", BaseURL: "https://netbox.example.test", AuthScheme: models.NetBoxAuthBearer, APIToken: "nbt_key.synthetic", AppliesToAllWorkspaces: boolPointer(false), WorkspaceIDs: []int{f.workspace}}
}

func (f *netBoxServiceFixture) link(t *testing.T, kind models.NetBoxObjectType, id int64) *models.NetBoxItemLink {
	t.Helper()
	link, _, err := f.service.LinkObject(context.Background(), f.item, f.actor, models.CreateNetBoxItemLinkRequest{ConnectionID: f.connection.ProviderID, ObjectType: kind, ObjectID: id})
	if err != nil {
		t.Fatal(err)
	}
	return link
}

func TestNetBoxServiceExplicitScopeAndManagedSecrets(t *testing.T) {
	f := newNetBoxServiceFixture(t)
	for _, missing := range []bool{true, false} {
		req := f.request()
		req.Slug = "invalid-scope"
		req.WorkspaceIDs = nil
		if missing {
			req.AppliesToAllWorkspaces = nil
		}
		_, err := f.service.CreateConnection(req, f.admin)
		var invalid *NetBoxValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("invalid scope accepted: %v", err)
		}
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM action_credentials").Scan(&count); err != nil || count != 1 {
		t.Fatalf("validation left orphan credential: count=%d err=%v", count, err)
	}
	var encrypted string
	if err := f.db.QueryRow("SELECT encrypted_secret FROM action_credentials WHERE id=?", f.connection.CredentialID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted == "" || strings.Contains(encrypted, "nbt_key.synthetic") {
		t.Fatal("token was not encrypted at rest")
	}
	for _, list := range []func() ([]*models.ActionCredential, error){f.credentials.ListAll, f.credentials.ListGlobal, func() ([]*models.ActionCredential, error) { return f.credentials.ListForWorkspace(f.workspace) }} {
		credentials, err := list()
		if err != nil || len(credentials) != 0 {
			t.Fatalf("managed token appeared in generic credential listing: count=%d err=%v", len(credentials), err)
		}
	}
	encoded, _ := json.Marshal(f.connection)
	if strings.Contains(string(encoded), "synthetic") || strings.Contains(string(encoded), "credential_id") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("connection response disclosed credentials: %s", encoded)
	}
}

func TestNetBoxServiceRejectsUnauthorizedAndInactiveActors(t *testing.T) {
	for _, state := range []string{"denied", "inactive", "offboarded"} {
		t.Run(state, func(t *testing.T) {
			f := newNetBoxServiceFixture(t)
			if state == "denied" {
				f.permission.allow = false
			} else {
				query := "UPDATE users SET is_active=false WHERE id=?"
				if state == "offboarded" {
					query = "UPDATE users SET offboarded_at=CURRENT_TIMESTAMP WHERE id=?"
				}
				if _, err := f.db.ExecWrite(query, f.actor); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.service.GetItemLinks(f.item, f.actor); !errors.Is(err, ErrNetBoxPermission) {
				t.Fatalf("actor read accepted: %v", err)
			}
			if _, err := f.service.Search(context.Background(), f.workspace, f.actor, f.connection.ProviderID, models.NetBoxDevice, "device", 20, 0); !errors.Is(err, ErrNetBoxPermission) {
				t.Fatalf("actor search accepted: %v", err)
			}
			if _, _, err := f.service.LinkObject(context.Background(), f.item, f.actor, models.CreateNetBoxItemLinkRequest{ConnectionID: f.connection.ProviderID, ObjectType: models.NetBoxDevice, ObjectID: 42}); !errors.Is(err, ErrNetBoxPermission) {
				t.Fatalf("actor link accepted: %v", err)
			}
			if f.requests != 0 {
				t.Fatal("denied actor reached NetBox")
			}
		})
	}
	f := newNetBoxServiceFixture(t)
	if _, err := f.service.CreateConnection(f.request(), f.actor); !errors.Is(err, ErrNetBoxPermission) {
		t.Fatalf("non-admin created connection: %v", err)
	}
	if _, err := f.service.Search(context.Background(), f.otherWorkspace, f.actor, f.connection.ProviderID, models.NetBoxDevice, "device", 20, 0); !errors.Is(err, ErrNetBoxUnavailable) {
		t.Fatalf("cross-workspace search accepted: %v", err)
	}
	if f.requests != 0 {
		t.Fatal("out-of-scope connection reached NetBox")
	}
}

func TestNetBoxServiceProjectsAndDeduplicatesTypedObjects(t *testing.T) {
	f := newNetBoxServiceFixture(t)
	var required string
	f.permission.hook = func(_, _ int, permission string) { required = permission }
	result, err := f.service.Search(context.Background(), f.workspace, f.actor, f.connection.ProviderID, models.NetBoxDevice, "device", 20, 0)
	if err != nil || len(result.Results) != 1 || required != models.PermissionItemEdit {
		t.Fatalf("search permission/projection result=%#v permission=%s err=%v", result, required, err)
	}
	device := f.link(t, models.NetBoxDevice, 42)
	duplicate := f.link(t, models.NetBoxDevice, 42)
	vm := f.link(t, models.NetBoxVirtualMachine, 42)
	if device.ID != duplicate.ID || device.ID == vm.ID || device.Object.ExternalID != "dcim.device:42" || vm.Object.ExternalID != "virtualization.virtualmachine:42" {
		t.Fatalf("typed idempotency failed: %#v %#v %#v", device, duplicate, vm)
	}
	links, err := f.service.GetItemLinks(f.item, f.actor)
	if err != nil || len(links) != 2 {
		t.Fatalf("saved links=%d err=%v", len(links), err)
	}
	encoded, _ := json.Marshal(links)
	if strings.Contains(string(encoded), "UNSELECTED_REMOTE_SECRET") || strings.Contains(string(encoded), "untrusted.example") || strings.Contains(string(encoded), "custom_fields") {
		t.Fatalf("raw remote fields escaped projection: %s", encoded)
	}
}

func TestNetBoxServiceConnectionUpdateFencesAndPreservesSecrets(t *testing.T) {
	f := newNetBoxServiceFixture(t)
	blank := ""
	updated, err := f.service.UpdateConnection(f.connection.ProviderID, models.UpdateNetBoxConnectionRequest{Revision: f.connection.ConfigRevision, APIToken: &blank}, f.admin)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := f.credentials.ResolveManaged(context.Background(), updated.CredentialID, f.workspace, "netbox", updated.ProviderID)
	if err != nil || secret != "nbt_key.synthetic" {
		t.Fatalf("blank token erased existing secret: %v", err)
	}
	replacement := "nbt_key.rotated"
	if _, err := f.service.UpdateConnection(updated.ProviderID, models.UpdateNetBoxConnectionRequest{Revision: f.connection.ConfigRevision, APIToken: &replacement}, f.admin); !errors.Is(err, repository.ErrConcurrentUpdate) {
		t.Fatalf("stale config changed credential: %v", err)
	}
	secret, _, err = f.credentials.ResolveManaged(context.Background(), updated.CredentialID, f.workspace, "netbox", updated.ProviderID)
	if err != nil || secret != "nbt_key.synthetic" {
		t.Fatalf("stale update rotated secret: %v", err)
	}
	origin := "https://other.example.test"
	scheme := models.NetBoxAuthToken
	for _, req := range []models.UpdateNetBoxConnectionRequest{{Revision: updated.ConfigRevision, BaseURL: &origin}, {Revision: updated.ConfigRevision, AuthScheme: &scheme}} {
		var invalid *NetBoxValidationError
		if _, err := f.service.UpdateConnection(updated.ProviderID, req, f.admin); !errors.As(err, &invalid) {
			t.Fatalf("immutable connection field changed: %v", err)
		}
	}
}

func TestNetBoxServiceFencesRemoteReadsAfterRevocation(t *testing.T) {
	for _, operation := range []string{"search", "link"} {
		for _, mutation := range []string{"token", "scope", "disabled", "permission", "inactive", "item_move"} {
			if operation == "search" && mutation == "item_move" {
				continue
			}
			t.Run(operation+"/"+mutation, func(t *testing.T) {
				f := newNetBoxServiceFixture(t)
				f.afterGET = func() {
					f.afterGET = nil
					var err error
					switch mutation {
					case "token", "scope", "disabled":
						req := models.UpdateNetBoxConnectionRequest{Revision: f.connection.ConfigRevision}
						if mutation == "token" {
							token := "nbt_key.rotated"
							req.APIToken = &token
						}
						if mutation == "scope" {
							ids := []int{f.otherWorkspace}
							req.WorkspaceIDs = &ids
						}
						if mutation == "disabled" {
							req.Enabled = boolPointer(false)
						}
						_, err = f.service.UpdateConnection(f.connection.ProviderID, req, f.admin)
					case "permission":
						f.permission.allow = false
					case "inactive":
						_, err = f.db.ExecWrite("UPDATE users SET is_active=false WHERE id=?", f.actor)
					case "item_move":
						_, err = f.db.ExecWrite("UPDATE items SET workspace_id=?,workspace_item_number=2 WHERE id=?", f.otherWorkspace, f.item)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if operation == "search" {
					_, err = f.service.Search(context.Background(), f.workspace, f.actor, f.connection.ProviderID, models.NetBoxDevice, "device", 20, 0)
				} else {
					_, _, err = f.service.LinkObject(context.Background(), f.item, f.actor, models.CreateNetBoxItemLinkRequest{ConnectionID: f.connection.ProviderID, ObjectType: models.NetBoxDevice, ObjectID: 42})
				}
				if err == nil {
					t.Fatal("revoked remote read was published or persisted")
				}
				var count int
				if err := f.db.QueryRow("SELECT COUNT(*) FROM netbox_item_links").Scan(&count); err != nil || count != 0 {
					t.Fatalf("revoked read persisted snapshot: count=%d err=%v", count, err)
				}
			})
		}
	}
}

func TestNetBoxServiceMovedItemCannotLeakOldAuthorizedRead(t *testing.T) {
	f := newNetBoxServiceFixture(t)
	f.link(t, models.NetBoxDevice, 42)
	if _, err := f.db.ExecWrite("UPDATE action_credentials SET applies_to_all_workspaces=true WHERE id=?", f.connection.CredentialID); err != nil {
		t.Fatal(err)
	}
	f.permission.hook = func(_, _ int, _ string) {
		f.permission.hook = nil
		if _, err := f.db.ExecWrite("UPDATE items SET workspace_id=?,workspace_item_number=2 WHERE id=?", f.otherWorkspace, f.item); err != nil {
			t.Fatal(err)
		}
	}
	links, err := f.service.GetItemLinks(f.item, f.actor)
	if err != nil || len(links) != 0 {
		t.Fatalf("moved item escaped expected-workspace guard: count=%d err=%v", len(links), err)
	}
}

func TestNetBoxServiceFailedRefreshRetainsSnapshot(t *testing.T) {
	for _, failure := range []string{"403", "404", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			f := newNetBoxServiceFixture(t)
			original := f.link(t, models.NetBoxDevice, 42)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.name = "Must not persist"
			if failure == "cancel" {
				f.afterGET = cancel
			} else {
				f.status, _ = strconv.Atoi(failure)
			}
			if _, err := f.service.RefreshLink(ctx, f.item, f.actor, original.ID); err == nil {
				t.Fatal("failed refresh succeeded")
			}
			stored, err := f.repo.GetVisibleLink(f.item, f.workspace, original.ID)
			if err != nil || stored.Revision != original.Revision || stored.Object.Name != original.Object.Name {
				t.Fatalf("failed refresh changed snapshot: %#v %v", stored, err)
			}
		})
	}
}

func TestNetBoxServiceRefreshCannotOverwriteNewerOrUnlinkedSnapshot(t *testing.T) {
	for _, mutation := range []string{"refresh", "unlink"} {
		t.Run(mutation, func(t *testing.T) {
			f := newNetBoxServiceFixture(t)
			original := f.link(t, models.NetBoxDevice, 42)
			f.name = "Older remote result"
			f.service.beforePersist = func() {
				f.service.beforePersist = nil
				if mutation == "unlink" {
					if err := f.service.Unlink(context.Background(), f.item, f.actor, original.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					f.name = "Newer remote result"
					if _, err := f.service.RefreshLink(context.Background(), f.item, f.actor, original.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := f.service.RefreshLink(context.Background(), f.item, f.actor, original.ID); !errors.Is(err, repository.ErrConcurrentUpdate) {
				t.Fatalf("late refresh was not fenced: %v", err)
			}
			stored, err := f.repo.GetVisibleLink(f.item, f.workspace, original.ID)
			if mutation == "unlink" {
				if !errors.Is(err, repository.ErrNotFound) {
					t.Fatalf("refresh recreated link: %#v %v", stored, err)
				}
			} else if err != nil || stored.Revision != 2 || stored.Object.Name != "Newer remote result" {
				t.Fatalf("late refresh overwrote newer snapshot: %#v %v", stored, err)
			}
		})
	}
}

func TestNetBoxServiceLinkLimitAndConnectionCleanup(t *testing.T) {
	f := newNetBoxServiceFixture(t)
	if err := database.WithTx(f.db, func(tx database.Tx) error {
		for i := int64(1); i <= 100; i++ {
			if _, err := f.repo.CreateLinkTx(tx, &models.NetBoxItemLink{ID: fmt.Sprintf("limit-%d", i), ItemID: f.item, ProviderID: f.connection.ProviderID, Object: models.NetBoxObject{ObjectType: models.NetBoxDevice, ObjectID: i}, SnapshotUpdatedAt: time.Now().UTC()}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var invalid *NetBoxValidationError
	if _, _, err := f.service.LinkObject(context.Background(), f.item, f.actor, models.CreateNetBoxItemLinkRequest{ConnectionID: f.connection.ProviderID, ObjectType: models.NetBoxDevice, ObjectID: 101}); !errors.As(err, &invalid) {
		t.Fatalf("link limit bypassed: %v", err)
	}
	if duplicate := f.link(t, models.NetBoxDevice, 42); duplicate.ID != "limit-42" {
		t.Fatalf("duplicate rejected or recreated at limit: %#v", duplicate)
	}
	if err := f.service.DeleteConnection(f.connection.ProviderID, f.admin); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"netbox_item_links", "netbox_connections", "action_credentials"} {
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("connection cleanup left %s count=%d err=%v", table, count, err)
		}
	}
}

func TestNetBoxServiceZeroScopeAfterWorkspaceDeletion(t *testing.T) {
	f := newNetBoxServiceFixture(t)
	if _, err := f.db.ExecWrite("DELETE FROM workspaces WHERE id=?", f.workspace); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.TestConnection(context.Background(), f.connection.ProviderID, f.admin)
	var invalid *NetBoxValidationError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "no allowed workspaces") {
		t.Fatalf("zero-scope test lacked useful validation: %v", err)
	}
	name := "Changed name"
	if _, err := f.service.UpdateConnection(f.connection.ProviderID, models.UpdateNetBoxConnectionRequest{Revision: f.connection.ConfigRevision, Name: &name}, f.admin); !errors.As(err, &invalid) {
		t.Fatalf("zero-scope edit accepted no workspace: %v", err)
	}
	if err := f.service.DeleteConnection(f.connection.ProviderID, f.admin); err != nil {
		t.Fatalf("zero-scope connection cannot be deleted: %v", err)
	}
	if f.requests != 0 {
		t.Fatal("zero-scope operations reached NetBox")
	}
}

func TestNetBoxServiceTestConnectionRejectsAdminRevokedDuringRead(t *testing.T) {
	f := newNetBoxServiceFixture(t)
	f.afterGET = func() {
		f.afterGET = nil
		if _, err := f.db.ExecWrite("DELETE FROM user_global_permissions WHERE user_id=?", f.admin); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.TestConnection(context.Background(), f.connection.ProviderID, f.admin); !errors.Is(err, ErrNetBoxPermission) {
		t.Fatalf("test result published after admin revocation: %v", err)
	}
}

func TestNetBoxServicePostgresCommitGuards(t *testing.T) {
	for _, mutation := range []string{"lifecycle", "rotation", "scope", "permission", "item_move"} {
		t.Run(mutation, func(t *testing.T) {
			f := newNetBoxServiceFixture(t, "postgres")
			if mutation == "lifecycle" {
				link := f.link(t, models.NetBoxDevice, 42)
				f.name = "Refreshed on PostgreSQL"
				updated, err := f.service.RefreshLink(context.Background(), f.item, f.actor, link.ID)
				if err != nil || updated.Revision != 2 {
					t.Fatalf("PG refresh failed: %#v %v", updated, err)
				}
				if err := f.service.Unlink(context.Background(), f.item, f.actor, link.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.repo.GetVisibleLink(f.item, f.workspace, link.ID); !errors.Is(err, repository.ErrNotFound) {
					t.Fatalf("PG unlink retained link: %v", err)
				}
				return
			}
			f.service.beforePersist = func() {
				f.service.beforePersist = nil
				var err error
				switch mutation {
				case "rotation":
					token := "nbt_key.pgrotated"
					_, err = f.service.UpdateConnection(f.connection.ProviderID, models.UpdateNetBoxConnectionRequest{Revision: f.connection.ConfigRevision, APIToken: &token}, f.admin)
				case "scope":
					ids := []int{f.otherWorkspace}
					_, err = f.service.UpdateConnection(f.connection.ProviderID, models.UpdateNetBoxConnectionRequest{Revision: f.connection.ConfigRevision, WorkspaceIDs: &ids}, f.admin)
				case "permission":
					f.permission.allow = false
				case "item_move":
					_, err = f.db.ExecWrite("UPDATE items SET workspace_id=?,workspace_item_number=2 WHERE id=?", f.otherWorkspace, f.item)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := f.service.LinkObject(context.Background(), f.item, f.actor, models.CreateNetBoxItemLinkRequest{ConnectionID: f.connection.ProviderID, ObjectType: models.NetBoxDevice, ObjectID: 42}); err == nil {
				t.Fatal("PG remote result escaped commit guard")
			}
			var count int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM netbox_item_links").Scan(&count); err != nil || count != 0 {
				t.Fatalf("PG guard retained snapshot: %d %v", count, err)
			}
		})
	}
}

func TestNetBoxServiceManagedCredentialCannotBeResolvedGenericallyOrCrossOwned(t *testing.T) {
	f := newNetBoxServiceFixture(t)
	if _, err := f.credentials.Get(f.connection.CredentialID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("generic get exposed managed credential: %v", err)
	}
	if _, _, err := f.credentials.Resolve(context.Background(), f.connection.CredentialID, f.workspace); err == nil {
		t.Fatal("generic action resolved NetBox token")
	}
	metadata, err := json.Marshal(managedCredentialMetadata{Marker: managedCredentialMetadataMarker, ManagedBy: "netbox", OwnerID: "different-provider"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecWrite("UPDATE action_credentials SET secret_metadata=? WHERE id=?", string(metadata), f.connection.CredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Search(context.Background(), f.workspace, f.actor, f.connection.ProviderID, models.NetBoxDevice, "device", 20, 0); !errors.Is(err, ErrCredentialPurposeMismatch) {
		t.Fatalf("different credential owner reached search: %v", err)
	}
	if f.requests != 0 {
		t.Fatal("wrong-owner credential was sent remotely")
	}
}

// The callback models cancellation while the final guarded SQL read completes,
// after beforeCommit has already checked the context. It is not a timer race.
type netBoxCancelGuardDB struct {
	database.Database
	cancel context.CancelFunc
}
type netBoxCancelGuardTx struct {
	database.Tx
	cancel context.CancelFunc
}

func (db netBoxCancelGuardDB) Begin() (database.Tx, error) {
	tx, err := db.Database.Begin()
	if err != nil {
		return nil, err
	}
	return netBoxCancelGuardTx{Tx: tx, cancel: db.cancel}, nil
}

func (tx netBoxCancelGuardTx) QueryRow(query string, args ...any) *sql.Row {
	row := tx.Tx.QueryRow(query, args...)
	if strings.HasPrefix(query, "SELECT EXISTS(") && strings.Contains(query, "FROM netbox_connections nc") {
		tx.cancel()
	}
	return row
}

func TestNetBoxServiceCancellationAfterGuardDoesNotWrite(t *testing.T) {
	for _, operation := range []string{"link", "refresh", "unlink"} {
		t.Run(operation, func(t *testing.T) {
			f := newNetBoxServiceFixture(t)
			var original *models.NetBoxItemLink
			if operation != "link" {
				original = f.link(t, models.NetBoxDevice, 42)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.service.db = netBoxCancelGuardDB{Database: f.db, cancel: cancel}
			var err error
			switch operation {
			case "link":
				_, _, err = f.service.LinkObject(ctx, f.item, f.actor, models.CreateNetBoxItemLinkRequest{ConnectionID: f.connection.ProviderID, ObjectType: models.NetBoxDevice, ObjectID: 42})
			case "refresh":
				f.name = "Cancelled snapshot"
				_, err = f.service.RefreshLink(ctx, f.item, f.actor, original.ID)
			case "unlink":
				err = f.service.Unlink(ctx, f.item, f.actor, original.ID)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("post-guard cancellation was ignored: %v", err)
			}
			links, err := f.repo.ListVisibleLinks(f.item, f.workspace)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "link" {
				if len(links) != 0 {
					t.Fatal("cancelled create persisted")
				}
			} else if len(links) != 1 || links[0].Revision != original.Revision || links[0].Object.Name != original.Object.Name {
				t.Fatalf("cancelled mutation changed snapshot: %#v", links)
			}
		})
	}
}
