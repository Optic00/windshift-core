package repository

import (
	"context"
	"errors"
	"testing"

	"windshift/internal/database"
)

func TestFindAllWithDetailsContextHonorsCancellation(t *testing.T) {
	db, err := database.NewSQLiteDBWithPoolSizes("file:item-list-context?mode=memory&cache=shared", 2, 1)
	if err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize database: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err = NewItemRepository(db).FindAllWithDetailsContext(ctx, ItemListParams{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
