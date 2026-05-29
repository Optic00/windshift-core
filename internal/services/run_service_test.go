package services

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

func newRunServiceTestDB(t *testing.T) database.Database {
	t.Helper()
	dsn := fmt.Sprintf("file:%s/run_service.db?mode=memory&cache=shared", t.TempDir())
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

func silentLogger(t *testing.T) *log.Logger {
	t.Helper()
	return log.New(&strings.Builder{}, "", 0)
}

// TestRunService_SkeletonHappyPath verifies that Start kicks off a run, the
// stub runner gets invoked, lifecycle + runner-emitted events land in the
// agent_run_events stream, and the run row is finalized as succeeded with
// the container_id stamped.
func TestRunService_SkeletonHappyPath(t *testing.T) {
	ctx := context.Background()
	db := newRunServiceTestDB(t)
	repo := repository.NewAgentRunRepository(db)

	runner := RunnerFunc(func(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
		_ = emit("stdout", `{"line":"hello from stub"}`)
		_ = emit("stdout", `{"line":"second line"}`)
		return RunnerResult{
			ContainerID: "fake-container-xyz",
			Status:      models.AgentRunStatusSucceeded,
		}
	})

	svc, err := NewRunService(repo, RunServiceOptions{
		GlobalCap: 4,
		Runner:    runner,
		Logger:    silentLogger(t),
	})
	if err != nil {
		t.Fatalf("new run service: %v", err)
	}

	runID, err := svc.Start(ctx, RunRequest{WorkspaceID: 1})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	svc.Wait()

	got, err := repo.Get(ctx, runID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.AgentRunStatusSucceeded {
		t.Fatalf("status: want succeeded, got %q (err=%q)", got.Status, got.Error)
	}
	if got.ContainerID != "fake-container-xyz" {
		t.Fatalf("container_id: want fake-container-xyz, got %q", got.ContainerID)
	}
	if got.StartedAt == nil || got.EndedAt == nil {
		t.Fatalf("started_at and ended_at must be set; got started=%v ended=%v", got.StartedAt, got.EndedAt)
	}

	events, err := repo.ListEvents(ctx, runID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	// Expect: lifecycle/queued, lifecycle/running, stdout, stdout, lifecycle/succeeded
	wantTypes := []string{"lifecycle", "lifecycle", "stdout", "stdout", "lifecycle"}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count: want %d, got %d (%+v)", len(wantTypes), len(events), events)
	}
	for i, ev := range events {
		if ev.Type != wantTypes[i] {
			t.Errorf("event[%d].type: want %q, got %q (payload=%s)", i, wantTypes[i], ev.Type, ev.PayloadJSON)
		}
	}
	last := events[len(events)-1].PayloadJSON
	if !strings.Contains(last, `"succeeded"`) {
		t.Errorf("terminal lifecycle payload: want succeeded marker, got %q", last)
	}
}

// TestRunService_NonTerminalRunnerStatusBecomesFailed pins the invariant
// that a runner returning a non-terminal status (e.g. "running") gets
// normalized to failed with a descriptive error rather than leaving the
// run row in an inconsistent state.
func TestRunService_NonTerminalRunnerStatusBecomesFailed(t *testing.T) {
	ctx := context.Background()
	db := newRunServiceTestDB(t)
	repo := repository.NewAgentRunRepository(db)

	runner := RunnerFunc(func(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
		return RunnerResult{Status: "totally-bogus"}
	})

	svc, err := NewRunService(repo, RunServiceOptions{Runner: runner, Logger: silentLogger(t)})
	if err != nil {
		t.Fatalf("new run service: %v", err)
	}
	runID, err := svc.Start(ctx, RunRequest{WorkspaceID: 1})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	svc.Wait()

	got, err := repo.Get(ctx, runID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.AgentRunStatusFailed {
		t.Fatalf("status: want failed, got %q", got.Status)
	}
	if !strings.Contains(got.Error, "totally-bogus") {
		t.Errorf("error must mention bogus status, got %q", got.Error)
	}
}

// TestRunService_AdmissionCapsConcurrency ensures the global semaphore
// actually caps in-flight runs. With a cap of 2 and 5 launches, the
// runner should never see more than 2 concurrent executions.
func TestRunService_AdmissionCapsConcurrency(t *testing.T) {
	ctx := context.Background()
	db := newRunServiceTestDB(t)
	repo := repository.NewAgentRunRepository(db)

	const cap = 2
	const total = 5

	var inflight int32
	var peak int32
	gate := make(chan struct{})
	entered := make(chan struct{}, cap)

	runner := RunnerFunc(func(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
		n := atomic.AddInt32(&inflight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		// Best-effort latch: the first `cap` runners signal they're in.
		// Later runners are still gated behind the semaphore.
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
		atomic.AddInt32(&inflight, -1)
		return RunnerResult{Status: models.AgentRunStatusSucceeded}
	})

	svc, err := NewRunService(repo, RunServiceOptions{
		GlobalCap: cap,
		Runner:    runner,
		Logger:    silentLogger(t),
	})
	if err != nil {
		t.Fatalf("new run service: %v", err)
	}

	for i := 0; i < total; i++ {
		if _, err := svc.Start(ctx, RunRequest{WorkspaceID: 1}); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}

	// Wait until `cap` runners are inside the runner body.
	timeout := time.After(2 * time.Second)
	for i := 0; i < cap; i++ {
		select {
		case <-entered:
		case <-timeout:
			t.Fatalf("timed out waiting for %d runners to enter admission (got %d)", cap, i)
		}
	}

	close(gate)
	svc.Wait()

	if peak > cap {
		t.Fatalf("peak concurrency exceeded cap: got %d, want <= %d", peak, cap)
	}
}

// TestRunService_WithRepoPreparesWorktree threads a RepoSpec through Start
// and asserts (a) the runner sees a populated WorkspacePath, (b) a
// "worktree_ready" lifecycle event is emitted with branch + base commit
// data, and (c) the worktree is cleaned up after the run finishes.
func TestRunService_WithRepoPreparesWorktree(t *testing.T) {
	ctx := context.Background()
	db := newRunServiceTestDB(t)
	repoDB := repository.NewAgentRunRepository(db)

	origin := seedOriginRepo(t, "main")
	wm := newTestWorktreeManager(t)

	var observedPath string
	runner := RunnerFunc(func(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
		observedPath = input.WorkspacePath
		return RunnerResult{Status: models.AgentRunStatusSucceeded}
	})

	svc, err := NewRunService(repoDB, RunServiceOptions{
		Runner:    runner,
		Worktrees: wm,
		Logger:    silentLogger(t),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	runID, err := svc.Start(ctx, RunRequest{
		WorkspaceID: 1,
		Repo: &RepoSpec{
			WorkspaceID: 1,
			RepoSlug:    "acme/widget",
			RemoteURL:   origin,
			BaseRef:     "main",
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	svc.Wait()

	if observedPath == "" {
		t.Fatal("runner saw empty WorkspacePath; worktree prep didn't flow through")
	}
	if _, err := os.Stat(observedPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir must be cleaned up after run, stat err=%v", err)
	}

	events, err := repoDB.ListEvents(ctx, runID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	foundReady := false
	for _, ev := range events {
		if ev.Type == "lifecycle" && strings.Contains(ev.PayloadJSON, "worktree_ready") {
			foundReady = true
			if !strings.Contains(ev.PayloadJSON, `"branch":"agent-runs/run-`) {
				t.Errorf("worktree_ready payload missing branch info: %s", ev.PayloadJSON)
			}
		}
	}
	if !foundReady {
		t.Errorf("expected a worktree_ready lifecycle event, got events=%+v", events)
	}
}

// TestRunService_RepoWithoutManagerErrors verifies that asking for a Repo
// without configuring a WorktreeManager fails fast at Start time — better
// to surface the misconfiguration synchronously than write a queued row
// that will never advance.
func TestRunService_RepoWithoutManagerErrors(t *testing.T) {
	ctx := context.Background()
	db := newRunServiceTestDB(t)
	repoDB := repository.NewAgentRunRepository(db)

	runner := RunnerFunc(func(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
		return RunnerResult{Status: models.AgentRunStatusSucceeded}
	})
	svc, err := NewRunService(repoDB, RunServiceOptions{Runner: runner, Logger: silentLogger(t)})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	_, err = svc.Start(ctx, RunRequest{
		WorkspaceID: 1,
		Repo:        &RepoSpec{WorkspaceID: 1, RepoSlug: "acme/widget", RemoteURL: "ignored"},
	})
	if err == nil {
		t.Fatal("expected error when Repo is set without WorktreeManager, got nil")
	}
}

// TestRunService_ShutdownRejectsNewWork confirms Start returns
// ErrShuttingDown after Shutdown has been initiated.
func TestRunService_ShutdownRejectsNewWork(t *testing.T) {
	ctx := context.Background()
	db := newRunServiceTestDB(t)
	repo := repository.NewAgentRunRepository(db)

	runner := RunnerFunc(func(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
		return RunnerResult{Status: models.AgentRunStatusSucceeded}
	})
	svc, err := NewRunService(repo, RunServiceOptions{Runner: runner, Logger: silentLogger(t)})
	if err != nil {
		t.Fatalf("new run service: %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := svc.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if _, err := svc.Start(ctx, RunRequest{WorkspaceID: 1}); err == nil {
		t.Fatal("expected Start to fail after Shutdown, got nil")
	}
}
