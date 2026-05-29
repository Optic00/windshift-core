package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
)

func openAgentRunTestDB(t *testing.T) database.Database {
	t.Helper()
	dsn := fmt.Sprintf("file:%s/agent_run_repo.db?mode=memory&cache=shared", t.TempDir())
	db, err := database.NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("initialize db: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces(id, name, key, active) VALUES (1, 'ws', 'WS', 1)`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return db
}

func TestAgentRunRepository_Lifecycle(t *testing.T) {
	ctx := context.Background()
	db := openAgentRunTestDB(t)
	repo := NewAgentRunRepository(db)

	id, err := repo.Insert(ctx, &models.AgentRun{WorkspaceID: 1})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.AgentRunStatusQueued {
		t.Fatalf("status: want queued, got %q", got.Status)
	}
	if got.StartedAt != nil {
		t.Fatalf("started_at: want nil before MarkRunning, got %v", got.StartedAt)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := repo.MarkRunning(ctx, id, "container-abc", now); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	got, err = repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("get after mark: %v", err)
	}
	if got.Status != models.AgentRunStatusRunning {
		t.Fatalf("status after mark: want running, got %q", got.Status)
	}
	if got.ContainerID != "container-abc" {
		t.Fatalf("container_id: want container-abc, got %q", got.ContainerID)
	}

	if err := repo.AppendEvent(ctx, id, "stdout", `{"line":"hello"}`); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if err := repo.AppendEvent(ctx, id, "lifecycle", `{"phase":"running"}`); err != nil {
		t.Fatalf("append event 2: %v", err)
	}

	events, err := repo.ListEvents(ctx, id)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("event count: want 2, got %d", len(events))
	}
	if events[0].Type != "stdout" || events[1].Type != "lifecycle" {
		t.Fatalf("event order off: %+v", events)
	}

	if err := repo.Finalize(ctx, id, models.AgentRunStatusSucceeded, "", now.Add(time.Second)); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	got, err = repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("get after finalize: %v", err)
	}
	if got.Status != models.AgentRunStatusSucceeded {
		t.Fatalf("status after finalize: want succeeded, got %q", got.Status)
	}
	if got.EndedAt == nil {
		t.Fatal("ended_at must be set after finalize")
	}
}

func TestAgentRunRepository_ListForWorkspaceAndEventsAfter(t *testing.T) {
	ctx := context.Background()
	db := openAgentRunTestDB(t)
	repo := NewAgentRunRepository(db)

	ids := make([]int, 3)
	for i := range ids {
		id, err := repo.Insert(ctx, &models.AgentRun{WorkspaceID: 1})
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		ids[i] = id
	}
	// Newest-first ordering.
	got, err := repo.ListForWorkspace(ctx, 1, 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("count: want 3, got %d", len(got))
	}
	if got[0].ID != ids[2] || got[2].ID != ids[0] {
		t.Errorf("order: want %d..%d desc, got %d..%d", ids[2], ids[0], got[0].ID, got[2].ID)
	}
	// Cursor pagination — pass beforeID and we get the older slice.
	older, err := repo.ListForWorkspace(ctx, 1, 50, ids[2])
	if err != nil {
		t.Fatalf("list older: %v", err)
	}
	if len(older) != 2 || older[0].ID != ids[1] {
		t.Errorf("cursor pagination off: got %+v", older)
	}

	// Events: 4 on the first run, query "after id=0", "after id=2".
	for i := 0; i < 4; i++ {
		if err := repo.AppendEvent(ctx, ids[0], "stdout", fmt.Sprintf(`{"i":%d}`, i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	events, err := repo.ListEventsAfter(ctx, ids[0], 0, 50)
	if err != nil {
		t.Fatalf("list events all: %v", err)
	}
	if len(events) != 4 {
		t.Errorf("first call: want 4, got %d", len(events))
	}
	tail, err := repo.ListEventsAfter(ctx, ids[0], events[1].ID, 50)
	if err != nil {
		t.Fatalf("list events tail: %v", err)
	}
	if len(tail) != 2 {
		t.Errorf("tail: want 2, got %d", len(tail))
	}
}

func TestAgentRunRepository_FinalizeRejectsNonTerminalStatus(t *testing.T) {
	ctx := context.Background()
	db := openAgentRunTestDB(t)
	repo := NewAgentRunRepository(db)

	id, err := repo.Insert(ctx, &models.AgentRun{WorkspaceID: 1})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	err = repo.Finalize(ctx, id, models.AgentRunStatusRunning, "", time.Now())
	if err == nil {
		t.Fatal("expected error finalizing with non-terminal status, got nil")
	}
}
