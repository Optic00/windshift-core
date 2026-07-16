package repository

import (
	"context"
	"testing"
	"time"
)

func TestFindAllWithDetailsOrdersBubblePagesByEffectiveActivity(t *testing.T) {
	db := newItemListTestDB(t, "item-list-bubble-order")

	workspaceResult, err := db.ExecWrite(
		`INSERT INTO workspaces (name, key, description, active) VALUES (?, ?, '', true)`,
		"Bubble Test",
		"BUB",
	)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	workspaceID64, err := workspaceResult.LastInsertId()
	if err != nil {
		t.Fatalf("workspace LastInsertId: %v", err)
	}
	workspaceID := int(workspaceID64)

	base := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	type seededItem struct {
		title        string
		fracIndex    string
		updatedAt    time.Time
		lastActiveAt *time.Time
	}
	newest := base.Add(3 * time.Hour)
	itemsToSeed := []seededItem{
		{title: "Old rank-first item", fracIndex: "a0", updatedAt: base, lastActiveAt: timePointer(base)},
		{title: "Newest lower-ID item", fracIndex: "a1", updatedAt: newest, lastActiveAt: timePointer(newest)},
		{title: "Newest higher-ID item", fracIndex: "a2", updatedAt: newest, lastActiveAt: timePointer(newest)},
		{title: "Fallback activity item", fracIndex: "a3", updatedAt: base.Add(2 * time.Hour)},
	}

	for number, item := range itemsToSeed {
		var lastActiveAt any
		if item.lastActiveAt != nil {
			lastActiveAt = item.lastActiveAt.Format(time.RFC3339Nano)
		}
		if _, err := db.ExecWrite(`
			INSERT INTO items (
				workspace_id, workspace_item_number, title, description,
				frac_index, created_at, updated_at, last_active_at
			) VALUES (?, ?, ?, '', ?, ?, ?, ?)
		`,
			workspaceID,
			number+1,
			item.title,
			item.fracIndex,
			base.Format(time.RFC3339Nano),
			item.updatedAt.Format(time.RFC3339Nano),
			lastActiveAt,
		); err != nil {
			t.Fatalf("insert %q: %v", item.title, err)
		}
	}

	repo := NewItemRepository(db)
	listPage := func(offset int) []string {
		t.Helper()
		items, total, err := repo.FindAllWithDetailsContext(context.Background(), ItemListParams{
			WorkspaceIDs: []int{workspaceID},
			Pagination:   PaginationParams{Limit: 2, Offset: offset},
			SortBy:       "last_active_at",
		})
		if err != nil {
			t.Fatalf("list page at offset %d: %v", offset, err)
		}
		if total != len(itemsToSeed) {
			t.Fatalf("total = %d, want %d", total, len(itemsToSeed))
		}
		titles := make([]string, len(items))
		for i, item := range items {
			titles[i] = item.Title
		}
		return titles
	}

	firstPage := listPage(0)
	secondPage := listPage(2)
	expectTitles(t, firstPage, []string{"Newest higher-ID item", "Newest lower-ID item"})
	expectTitles(t, secondPage, []string{"Fallback activity item", "Old rank-first item"})
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func expectTitles(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("titles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("titles = %v, want %v", got, want)
		}
	}
}
