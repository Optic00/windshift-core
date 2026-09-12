package repository

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"

	"github.com/lib/pq"
)

type netBoxRepositoryFixture struct {
	db         database.Database
	repo       *NetBoxRepository
	connection *models.NetBoxConnection
}

func newNetBoxRepositoryFixture(t *testing.T, driver string) *netBoxRepositoryFixture {
	t.Helper()
	var base *zammadLeaseFixture
	if driver == "postgres" {
		base = newZammadPostgresReviewFixture(t)
	} else {
		base = newZammadLeaseFixture(t)
	}
	f := &netBoxRepositoryFixture{db: base.db, repo: NewNetBoxRepository(base.db)}
	// Keep the common schema/users/items fixture, but no Zammad lifecycle
	// guards: these tests must prove NetBox's own FK behavior independently.
	for _, query := range []string{
		"DELETE FROM integration_providers WHERE id = 'zammad-test'",
		"INSERT INTO workspaces (id, name, key) VALUES (2, 'Other NetBox workspace', 'NB2')",
		"UPDATE action_credentials SET applies_to_all_workspaces = false WHERE id = 1",
		"INSERT INTO action_credential_workspaces (credential_id, workspace_id) VALUES (1, 1)",
	} {
		if _, err := f.db.ExecWrite(query); err != nil {
			t.Fatal(err)
		}
	}
	actor := 1
	f.connection = &models.NetBoxConnection{
		ProviderID: "netbox-test", Slug: "netbox-test", Name: "NetBox test", Enabled: true,
		BaseURL: "https://netbox.example.test", AuthScheme: models.NetBoxAuthBearer,
		CredentialID: 1, CreatedBy: &actor,
	}
	if err := f.repo.CreateConnection(f.connection); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *netBoxRepositoryFixture) link(t *testing.T, id string, kind models.NetBoxObjectType, objectID int64) *models.NetBoxItemLink {
	t.Helper()
	actor := 1
	link := &models.NetBoxItemLink{ID: id, ItemID: 1, ProviderID: f.connection.ProviderID,
		Object:    models.NetBoxObject{ObjectType: kind, ObjectID: objectID, Name: "Selected snapshot", URL: "https://netbox.example.test/dcim/devices/42/"},
		CreatedBy: &actor, SnapshotUpdatedAt: time.Now().UTC(),
	}
	if err := database.WithTx(f.db, func(tx database.Tx) error {
		if err := f.repo.LockConnectionTx(tx, link.ProviderID); err != nil {
			return err
		}
		if _, err := NewItemRepository(f.db).FindByIDForUpdate(tx, link.ItemID); err != nil {
			return err
		}
		created, err := f.repo.CreateLinkTx(tx, link)
		if err == nil && !created {
			return fmt.Errorf("expected a new NetBox link")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return link
}

func TestNetBoxRepositoryTypedIdentityAndCAS(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			f := newNetBoxRepositoryFixture(t, driver)
			device := f.link(t, "device", models.NetBoxDevice, 42)
			f.link(t, "vm", models.NetBoxVirtualMachine, 42)
			if err := database.WithTx(f.db, func(tx database.Tx) error {
				duplicate := *device
				duplicate.ID = "duplicate-device"
				created, err := f.repo.CreateLinkTx(tx, &duplicate)
				if err != nil || created {
					return fmt.Errorf("duplicate result created=%t err=%v", created, err)
				}
				count, err := f.repo.CountLinksForItemTx(tx, 1)
				if err != nil || count != 2 {
					return fmt.Errorf("typed identity count=%d err=%v", count, err)
				}
				found, err := f.repo.GetLinkByObjectTx(tx, 1, f.connection.ProviderID, models.NetBoxDevice, 42)
				if err != nil || found.ID != "device" || found.Object.ExternalID != "dcim.device:42" {
					return fmt.Errorf("typed lookup failed: %#v %v", found, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			stale := *device
			device.Object.Name = "Newer snapshot"
			if err := database.WithTx(f.db, func(tx database.Tx) error { return f.repo.UpdateLinkTx(tx, device) }); err != nil {
				t.Fatal(err)
			}
			if device.Revision != 2 {
				t.Fatalf("link revision=%d", device.Revision)
			}
			if err := database.WithTx(f.db, func(tx database.Tx) error { return f.repo.UpdateLinkTx(tx, &stale) }); !errors.Is(err, ErrConcurrentUpdate) {
				t.Fatalf("stale link update was not rejected: %v", err)
			}
			if err := database.WithTx(f.db, func(tx database.Tx) error { return f.repo.DeleteLinkTx(tx, 1, device.ID) }); err != nil {
				t.Fatal(err)
			}
			if err := database.WithTx(f.db, func(tx database.Tx) error { return f.repo.UpdateLinkTx(tx, device) }); !errors.Is(err, ErrConcurrentUpdate) {
				t.Fatalf("refresh recreated an unlinked row: %v", err)
			}
			connection, err := f.repo.GetConnection(f.connection.ProviderID)
			if err != nil || len(connection.WorkspaceIDs) != 1 || connection.WorkspaceIDs[0] != 1 || !connection.HasAPIToken {
				t.Fatalf("connection scope/token metadata: %#v %v", connection, err)
			}
			oldConnection := *connection
			connection.Name = "Updated NetBox"
			if err := database.WithTx(f.db, func(tx database.Tx) error { return f.repo.UpdateConnectionTx(tx, connection) }); err != nil {
				t.Fatal(err)
			}
			if connection.ConfigRevision != 2 {
				t.Fatalf("connection revision=%d", connection.ConfigRevision)
			}
			if err := database.WithTx(f.db, func(tx database.Tx) error { return f.repo.UpdateConnectionTx(tx, &oldConnection) }); !errors.Is(err, ErrConcurrentUpdate) {
				t.Fatalf("stale connection update accepted: %v", err)
			}
			connection.BaseURL = "https://changed.example.test"
			if err := database.WithTx(f.db, func(tx database.Tx) error { return f.repo.UpdateConnectionTx(tx, connection) }); !errors.Is(err, ErrConcurrentUpdate) {
				t.Fatalf("immutable connection origin changed: %v", err)
			}
		})
	}
}

func TestNetBoxRepositoryVisibilityUsesCurrentScopeAndWorkspace(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			f := newNetBoxRepositoryFixture(t, driver)
			f.link(t, "device", models.NetBoxDevice, 42)
			assertVisible := func(want int, workspaceID int) {
				t.Helper()
				links, err := f.repo.ListVisibleLinks(1, workspaceID)
				if err != nil || len(links) != want {
					t.Fatalf("visible links=%d want=%d err=%v", len(links), want, err)
				}
				link, err := f.repo.GetVisibleLink(1, workspaceID, "device")
				if want == 0 && (!errors.Is(err, ErrNotFound) || link != nil) {
					t.Fatalf("hidden detail exposed: %#v %v", link, err)
				}
				if want == 1 && (err != nil || link.Object.Name != "Selected snapshot") {
					t.Fatalf("visible snapshot unavailable: %#v %v", link, err)
				}
			}
			assertVisible(1, 1)
			for _, mutation := range []struct{ disable, enable string }{
				{"UPDATE integration_providers SET enabled = false WHERE id = 'netbox-test'", "UPDATE integration_providers SET enabled = true WHERE id = 'netbox-test'"},
				{"UPDATE action_credentials SET is_enabled = false WHERE id = 1", "UPDATE action_credentials SET is_enabled = true WHERE id = 1"},
				{"DELETE FROM action_credential_workspaces WHERE credential_id = 1", "INSERT INTO action_credential_workspaces (credential_id, workspace_id) VALUES (1, 1)"},
			} {
				if _, err := f.db.ExecWrite(mutation.disable); err != nil {
					t.Fatal(err)
				}
				assertVisible(0, 1)
				available, err := f.repo.IsConnectionAvailableToWorkspace(f.connection.ProviderID, 1)
				if err != nil || available {
					t.Fatalf("revoked connection still available: %t %v", available, err)
				}
				connections, err := f.repo.ListConnectionsForWorkspace(1)
				if err != nil || len(connections) != 0 {
					t.Fatalf("revoked connection still listed: %d %v", len(connections), err)
				}
				raw, err := f.repo.GetLink(1, "device")
				if err != nil || raw.Object.Name != "" || raw.Object.URL != "" {
					t.Fatalf("identity lookup leaked hidden snapshot: %#v %v", raw, err)
				}
				if _, err := f.db.ExecWrite(mutation.enable); err != nil {
					t.Fatal(err)
				}
				assertVisible(1, 1)
			}
			if _, err := f.db.ExecWrite("UPDATE items SET workspace_id = 2 WHERE id = 1"); err != nil {
				t.Fatal(err)
			}
			assertVisible(0, 1)
			assertVisible(0, 2)
			if _, err := f.db.ExecWrite("UPDATE action_credentials SET applies_to_all_workspaces = true WHERE id = 1"); err != nil {
				t.Fatal(err)
			}
			assertVisible(0, 1) // old authorization must remain invalid after move
			assertVisible(1, 2)
			if _, err := f.repo.GetVisibleLink(2, 2, "device"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("wrong item exposed link: %v", err)
			}
		})
	}
}

func TestNetBoxRepositoryCascadesOnlyLocalLinks(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		for _, target := range []string{"item", "workspace", "provider"} {
			t.Run(driver+"/"+target, func(t *testing.T) {
				f := newNetBoxRepositoryFixture(t, driver)
				f.link(t, "device", models.NetBoxDevice, 42)
				query := map[string]string{"item": "DELETE FROM items WHERE id = 1", "workspace": "DELETE FROM workspaces WHERE id = 1"}[target]
				if target == "provider" {
					if err := database.WithTx(f.db, func(tx database.Tx) error {
						if err := f.repo.LockConnectionTx(tx, f.connection.ProviderID); err != nil {
							return err
						}
						return f.repo.DeleteConnectionTx(tx, f.connection.ProviderID)
					}); err != nil {
						t.Fatal(err)
					}
				} else if _, err := f.db.ExecWrite(query); err != nil {
					t.Fatal(err)
				}
				var links, credentials int
				if err := f.db.QueryRow("SELECT COUNT(*) FROM netbox_item_links").Scan(&links); err != nil || links != 0 {
					t.Fatalf("retained links=%d err=%v", links, err)
				}
				if err := f.db.QueryRow("SELECT COUNT(*) FROM action_credentials WHERE id = 1").Scan(&credentials); err != nil {
					t.Fatal(err)
				}
				if (target == "provider" && credentials != 0) || (target != "provider" && credentials != 1) {
					t.Fatalf("unexpected credential lifetime: target=%s count=%d", target, credentials)
				}
			})
		}
	}
}

func TestNetBoxRepositorySnapshotBoundsAndCredentialUniqueness(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			f := newNetBoxRepositoryFixture(t, driver)
			duplicate := *f.connection
			duplicate.ProviderID, duplicate.Slug = "other-netbox", "other-netbox"
			if err := f.repo.CreateConnection(&duplicate); !errors.Is(err, ErrDuplicateEntry) {
				t.Fatalf("credential reused across connections: %v", err)
			}
			var orphan int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM integration_providers WHERE id = 'other-netbox'").Scan(&orphan); err != nil || orphan != 0 {
				t.Fatalf("failed connection left provider=%d err=%v", orphan, err)
			}
			link := &models.NetBoxItemLink{ID: "large", ItemID: 1, ProviderID: f.connection.ProviderID,
				Object: models.NetBoxObject{ObjectType: models.NetBoxDevice, ObjectID: 1, Name: strings.Repeat("é", netBoxSnapshotMaxBytes)}, SnapshotUpdatedAt: time.Now().UTC()}
			if err := database.WithTx(f.db, func(tx database.Tx) error { _, err := f.repo.CreateLinkTx(tx, link); return err }); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("oversized snapshot accepted: %v", err)
			}
			for _, query := range []string{
				"UPDATE netbox_connections SET auth_scheme = 'basic' WHERE provider_id = 'netbox-test'",
				"UPDATE netbox_connections SET config_revision = 0 WHERE provider_id = 'netbox-test'",
			} {
				if _, err := f.db.ExecWrite(query); err == nil {
					t.Fatalf("constraint accepted invalid mutation: %s", query)
				}
			}
			f.link(t, "valid", models.NetBoxDevice, 42)
			for _, query := range []string{
				"UPDATE netbox_item_links SET object_type = 'unknown' WHERE id = 'valid'",
				"UPDATE netbox_item_links SET object_id = 0 WHERE id = 'valid'",
			} {
				if _, err := f.db.ExecWrite(query); err == nil {
					t.Fatalf("constraint accepted invalid mutation: %s", query)
				}
			}
			if _, err := f.db.ExecWrite("UPDATE netbox_item_links SET snapshot_json = ? WHERE id = 'valid'", strings.Repeat("é", netBoxSnapshotMaxBytes)); err == nil {
				t.Fatal("database accepted oversized UTF-8 snapshot")
			}
		})
	}
}

func TestNetBoxPostgresCreateLocksSerializeDeletion(t *testing.T) {
	for _, target := range []string{"provider", "workspace"} {
		t.Run(target, func(t *testing.T) {
			f := newNetBoxRepositoryFixture(t, "postgres")
			tx, err := f.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if err := f.repo.LockConnectionTx(tx, f.connection.ProviderID); err != nil {
				t.Fatal(err)
			}
			if _, err := NewItemRepository(f.db).FindByIDForUpdate(tx, 1); err != nil {
				t.Fatal(err)
			}
			deleteTx, err := f.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = deleteTx.Rollback() }()
			if _, err := deleteTx.Exec("SET LOCAL lock_timeout = '100ms'"); err != nil {
				t.Fatal(err)
			}
			query := "DELETE FROM integration_providers WHERE id = 'netbox-test'"
			if target == "workspace" {
				query = "DELETE FROM workspaces WHERE id = 1"
			}
			_, err = deleteTx.Exec(query)
			var locked *pq.Error
			if !errors.As(err, &locked) || locked.Code != "55P03" {
				t.Fatalf("delete bypassed create lock: %v", err)
			}
			if err := deleteTx.Rollback(); err != nil {
				t.Fatal(err)
			}
			created, err := f.repo.CreateLinkTx(tx, &models.NetBoxItemLink{ID: "concurrent", ItemID: 1, ProviderID: f.connection.ProviderID,
				Object: models.NetBoxObject{ObjectType: models.NetBoxDevice, ObjectID: 42}, SnapshotUpdatedAt: time.Now().UTC()})
			if err != nil || !created {
				t.Fatalf("locked create failed: %t %v", created, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.ExecWrite(query); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM netbox_item_links").Scan(&count); err != nil || count != 0 {
				t.Fatalf("delete left concurrent link: count=%d err=%v", count, err)
			}
		})
	}
}

func TestNetBoxRepositoryPreservesBigIntIdentityAndRevisions(t *testing.T) {
	const large = int64(1) << 40
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			f := newNetBoxRepositoryFixture(t, driver)
			f.link(t, "large-id", models.NetBoxDevice, large)
			for _, query := range []string{
				"UPDATE netbox_item_links SET revision = ? WHERE id = 'large-id'",
				"UPDATE netbox_connections SET config_revision = ? WHERE provider_id = 'netbox-test'",
			} {
				if _, err := f.db.ExecWrite(query, large); err != nil {
					t.Fatal(err)
				}
			}
			link, err := f.repo.GetVisibleLink(1, 1, "large-id")
			if err != nil || link.Object.ObjectID != large || link.Revision != large {
				t.Fatalf("int64 link fields lost: %#v %v", link, err)
			}
			if err := database.WithTx(f.db, func(tx database.Tx) error { return f.repo.UpdateLinkTx(tx, link) }); err != nil {
				t.Fatal(err)
			}
			if link.Revision != large+1 {
				t.Fatalf("link revision truncated: %d", link.Revision)
			}
			if err := database.WithTx(f.db, func(tx database.Tx) error {
				c, err := f.repo.GetConnectionTx(tx, f.connection.ProviderID)
				if err != nil {
					return err
				}
				if c.ConfigRevision != large {
					return fmt.Errorf("connection revision truncated: %d", c.ConfigRevision)
				}
				available, err := f.repo.IsConnectionAvailableToWorkspaceTx(tx, c.ProviderID, 1)
				if err != nil || !available {
					return fmt.Errorf("transaction scope result=%t err=%v", available, err)
				}
				return f.repo.UpdateConnectionTx(tx, c)
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNetBoxPostgresDeletionFirstRejectsNewCreate(t *testing.T) {
	for _, target := range []string{"provider", "workspace"} {
		t.Run(target, func(t *testing.T) {
			f := newNetBoxRepositoryFixture(t, "postgres")
			deletion, err := f.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = deletion.Rollback() }()
			query := "DELETE FROM integration_providers WHERE id = 'netbox-test'"
			if target == "workspace" {
				query = "DELETE FROM workspaces WHERE id = 1"
			}
			if _, err := deletion.Exec(query); err != nil {
				t.Fatal(err)
			}
			creation, err := f.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = creation.Rollback() }()
			if _, err := creation.Exec("SET LOCAL lock_timeout = '100ms'"); err != nil {
				t.Fatal(err)
			}
			err = f.repo.LockConnectionTx(creation, f.connection.ProviderID)
			if target == "workspace" && err == nil {
				_, err = NewItemRepository(f.db).FindByIDForUpdate(creation, 1)
			}
			var locked *pq.Error
			if !errors.As(err, &locked) || locked.Code != "55P03" {
				t.Fatalf("create did not wait for deletion: %v", err)
			}
			if err := creation.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := deletion.Commit(); err != nil {
				t.Fatal(err)
			}
			err = database.WithTx(f.db, func(tx database.Tx) error {
				if err := f.repo.LockConnectionTx(tx, f.connection.ProviderID); err != nil {
					return err
				}
				_, err := NewItemRepository(f.db).FindByIDForUpdate(tx, 1)
				return err
			})
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("create found its deleted target: %v", err)
			}
		})
	}
}
