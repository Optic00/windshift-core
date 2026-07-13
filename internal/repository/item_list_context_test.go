package repository

import (
	"context"
	"errors"
	"testing"

	"windshift/internal/database"
)

func TestFindAllWithDetailsContextHonorsCancellation(t *testing.T) {
	db := newItemListTestDB(t, "item-list-context")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := NewItemRepository(db).FindAllWithDetailsContext(ctx, ItemListParams{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestFindAllWithDetailsUsesLeanCountQuery(t *testing.T) {
	db := newItemListTestDB(t, "item-list-lean-count")

	items, total, err := NewItemRepository(db).FindAllWithDetailsContext(context.Background(), ItemListParams{})
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if total != len(items) {
		t.Fatalf("total = %d, items = %d", total, len(items))
	}
}

func newItemListTestDB(t *testing.T, name string) database.Database {
	t.Helper()
	db, err := database.NewSQLiteDBWithPoolSizes("file:"+name+"?mode=memory&cache=shared", 2, 1)
	if err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	return db
}
