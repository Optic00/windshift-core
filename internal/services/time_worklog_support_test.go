package services

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
)

func TestParseWorklogTimesStrictClockAndDurationBounds(t *testing.T) {
	date := time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC)

	for _, input := range []WorklogTimeInput{
		{StartTime: "25:00", EndTime: "26:00"},
		{StartTime: "12:60", EndTime: "13:00"},
		{DurationMinutes: MaxWorklogDurationMinutes + 1},
		{DurationMinutes: int(^uint(0) >> 1)},
	} {
		if _, _, _, err := ParseWorklogTimes(date, input); err == nil {
			t.Fatalf("ParseWorklogTimes(%+v) succeeded, want validation error", input)
		}
	}

	minutes, start, end, err := ParseWorklogTimes(date, WorklogTimeInput{
		StartTime: "09:15",
		EndTime:   "10:45",
	})
	if err != nil {
		t.Fatalf("ParseWorklogTimes(valid): %v", err)
	}
	if minutes != 90 || end-start != int64(90*time.Minute/time.Second) {
		t.Fatalf("parsed result = (%d, %d seconds), want (90, 5400)", minutes, end-start)
	}
}

func TestRedactInaccessibleWorklogItemsFailsClosedAndCachesWorkspaceChecks(t *testing.T) {
	item1, item2, workspaceID := 1, 2, 7
	worklogs := []models.Worklog{
		{ItemID: &item1, ItemTitle: "secret one", WorkspaceID: &workspaceID, WorkspaceKey: "SEC", WorkspaceItemNumber: 1},
		{ItemID: &item2, ItemTitle: "secret two", WorkspaceID: &workspaceID, WorkspaceKey: "SEC", WorkspaceItemNumber: 2},
	}
	checks := 0
	redacted := RedactInaccessibleWorklogItems(worklogs, func(int) (bool, error) {
		checks++
		return false, errors.New("permission backend unavailable")
	})

	if checks != 1 {
		t.Fatalf("workspace checks = %d, want 1", checks)
	}
	for i, worklog := range redacted {
		if worklog.ItemID != nil || worklog.WorkspaceID != nil || worklog.ItemTitle != "" || worklog.WorkspaceKey != "" || worklog.WorkspaceItemNumber != 0 {
			t.Fatalf("worklog %d retained item metadata: %+v", i, worklog)
		}
	}
	if worklogs[0].ItemID == nil || worklogs[0].ItemTitle == "" {
		t.Fatal("input slice was mutated")
	}
}

func TestResolveAccessibleWorklogItemUsesSameGateForIDAndKey(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "worklogs.db"))
	if err != nil {
		t.Fatalf("NewSQLiteDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	workspaceResult, err := db.ExecWrite(`INSERT INTO workspaces (name, key, description, active) VALUES ('Secret', 'SEC', '', true)`)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	workspaceID, err := workspaceResult.LastInsertId()
	if err != nil {
		t.Fatalf("workspace LastInsertId: %v", err)
	}
	itemResult, err := db.ExecWrite(`
		INSERT INTO items (workspace_id, workspace_item_number, title, description)
		VALUES (?, 42, 'Restricted', '')
	`, workspaceID)
	if err != nil {
		t.Fatalf("insert item: %v", err)
	}
	itemID64, err := itemResult.LastInsertId()
	if err != nil {
		t.Fatalf("item LastInsertId: %v", err)
	}
	itemID := int(itemID64)

	deny := func(int) (bool, error) { return false, nil }
	if _, err := ResolveAccessibleWorklogItem(db, itemID, "", deny); !errors.Is(err, ErrWorklogItemNotFound) {
		t.Fatalf("numeric denied error = %v, want ErrWorklogItemNotFound", err)
	}
	if _, err := ResolveAccessibleWorklogItem(db, 0, "SEC-42", deny); !errors.Is(err, ErrWorklogItemNotFound) {
		t.Fatalf("key denied error = %v, want ErrWorklogItemNotFound", err)
	}

	allow := func(id int) (bool, error) { return id == int(workspaceID), nil }
	for _, reference := range []struct {
		id  int
		key string
	}{{id: itemID}, {key: "SEC-42"}} {
		resolved, err := ResolveAccessibleWorklogItem(db, reference.id, reference.key, allow)
		if err != nil {
			t.Fatalf("ResolveAccessibleWorklogItem(%+v): %v", reference, err)
		}
		if resolved != itemID {
			t.Fatalf("resolved ID = %d, want %d", resolved, itemID)
		}
	}
}
