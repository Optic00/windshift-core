package database

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newNetBoxSchemaTestDB(t *testing.T, driver string) Database {
	t.Helper()
	if driver == "sqlite" {
		db, err := NewSQLiteDB(filepath.Join(t.TempDir(), "netbox-schema.db"))
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
	admin, err := NewPostgresDB(dsn, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("netbox_schema_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("remove isolated NetBox test schema: %v", err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := NewPostgresDB(parsed.String(), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestNetBoxSchemaFreshDDLMatchesNewMigration(t *testing.T) {
	if !strings.Contains(integrationsSchema, strings.TrimSpace(netBoxSchemaSQLite)) {
		t.Fatal("SQLite fresh NetBox schema differs from migration DDL")
	}
	if !strings.Contains(integrationsSchemaPostgres, strings.TrimSpace(netBoxSchemaPostgres)) {
		t.Fatal("PostgreSQL fresh NetBox schema differs from migration DDL")
	}
}

func TestNetBoxSchemaInitializesUpgradesAndReinitializes(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			db := newNetBoxSchemaTestDB(t, driver)
			if err := db.Initialize(); err != nil {
				t.Fatalf("fresh initialization: %v", err)
			}
			assertSchema := func() {
				t.Helper()
				for _, table := range []string{"netbox_connections", "netbox_item_links"} {
					query := "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?"
					if driver == "postgres" {
						query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=?"
					}
					var count int
					if err := db.QueryRow(query, table).Scan(&count); err != nil || count != 1 {
						t.Fatalf("table %s missing: count=%d err=%v", table, count, err)
					}
				}
				var count int
				if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version='20260912_netbox_integration'").Scan(&count); err != nil || count != 1 {
					t.Fatalf("migration stamp count=%d err=%v", count, err)
				}
			}
			assertSchema()
			if err := db.Initialize(); err != nil {
				t.Fatalf("second initialization: %v", err)
			}
			assertSchema()
			// Recreate a pre-NetBox database in this owned fixture. Running only
			// pending migrations proves the new migration, not fresh-schema DDL.
			for _, query := range []string{
				"DROP TABLE netbox_item_links", "DROP TABLE netbox_connections",
				"DELETE FROM schema_migrations WHERE version='20260912_netbox_integration'",
			} {
				if _, err := db.ExecWrite(query); err != nil {
					t.Fatal(err)
				}
			}
			if err := runPendingMigrations(db, Catalog); err != nil {
				t.Fatalf("upgrade from pre-NetBox schema: %v", err)
			}
			assertSchema()
			if err := db.Initialize(); err != nil {
				t.Fatalf("initialize after upgrade: %v", err)
			}
			assertSchema()
			if driver == "postgres" {
				for _, field := range []struct{ table, column string }{{"netbox_connections", "config_revision"}, {"netbox_item_links", "revision"}, {"netbox_item_links", "object_id"}} {
					var dataType string
					if err := db.QueryRow(`SELECT data_type FROM information_schema.columns
						WHERE table_schema=current_schema() AND table_name=? AND column_name=?`, field.table, field.column).Scan(&dataType); err != nil || dataType != "bigint" {
						t.Fatalf("%s.%s must be BIGINT: type=%s err=%v", field.table, field.column, dataType, err)
					}
				}
			}
		})
	}
}
