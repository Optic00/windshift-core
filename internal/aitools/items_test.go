package aitools

import (
	"fmt"
	"strings"
	"testing"

	"windshift/internal/database"
)

func TestResolveStatusNameUsesGlobalStatuses(t *testing.T) {
	dsn := fmt.Sprintf("file:aitools-resolve-status-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := database.NewSQLiteDB(dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize sqlite: %v", err)
	}

	var want int
	if err := db.QueryRow("SELECT id FROM statuses WHERE name = ?", "Done").Scan(&want); err != nil {
		t.Fatalf("lookup fixture status: %v", err)
	}

	got, err := resolveStatusName(db, "done", 999)
	if err != nil {
		t.Fatalf("resolve status name: %v", err)
	}
	if got != want {
		t.Fatalf("resolve status name returned %d, want %d", got, want)
	}
}
