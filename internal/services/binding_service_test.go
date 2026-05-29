package services

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// seedItem inserts a minimal items row tied to the given workspace and
// returns the new id. The binding-service trigger tests need a real item
// row so the agent_runs.item_id FK resolves.
func seedItem(t *testing.T, db database.Database, workspaceID int) int {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO items(workspace_id, title) VALUES (?, ?)`,
		workspaceID, "test item",
	)
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}

// bindingTestStack assembles a fresh DB, identity service, binding repo,
// binding service, and (optionally) a wired RunService driven by a stub
// runner. The stub runner records every Run call's input via the supplied
// observer so tests can assert what the trigger passed through.
type bindingTestStack struct {
	BS        *BindingService
	Bindings  *repository.WorkspaceAgentBindingRepository
	DB        database.Database
	AdminID   int
	AgentID   int
	SvcUserID int
	RunCalls  *int32
	LastInput func() RunInput
}

func newBindingTestStack(t *testing.T, withRunService bool) *bindingTestStack {
	t.Helper()
	db, sec := openIdentityTestDB(t)
	identitySvc, err := NewAgentActingIdentityService(db, sec)
	if err != nil {
		t.Fatalf("identity svc: %v", err)
	}
	bindings := repository.NewWorkspaceAgentBindingRepository(db)

	admin := seedIdentityUser(t, db, "alice@example.com", "alice", "Alice", "Hu", false, nil, true)
	agent := seedIdentityUser(t, db, "alice-agent@agents.local", "alice-agent", "Alice", "Agent", true, &admin, true)
	svcUser := seedIdentityUser(t, db, "svc@agents.local", "svc", "Svc", "One", true, nil, true)

	opts := BindingServiceOptions{
		Repo:     bindings,
		Identity: identitySvc,
		Logger:   silentLogger(t),
	}

	var (
		calls  int32
		lastIn RunInput
	)
	if withRunService {
		runRepo := repository.NewAgentRunRepository(db)
		runner := RunnerFunc(func(ctx context.Context, in RunInput, _ EventSink) RunnerResult {
			atomic.AddInt32(&calls, 1)
			lastIn = in
			return RunnerResult{Status: models.AgentRunStatusSucceeded}
		})
		runSvc, err := NewRunService(runRepo, RunServiceOptions{
			Runner: runner,
			Logger: silentLogger(t),
		})
		if err != nil {
			t.Fatalf("run service: %v", err)
		}
		opts.Runs = runSvc
		t.Cleanup(func() { runSvc.Wait() })
	}

	bs, err := NewBindingService(opts)
	if err != nil {
		t.Fatalf("binding svc: %v", err)
	}
	return &bindingTestStack{
		BS:        bs,
		Bindings:  bindings,
		DB:        db,
		AdminID:   admin,
		AgentID:   agent,
		SvcUserID: svcUser,
		RunCalls:  &calls,
		LastInput: func() RunInput { return lastIn },
	}
}

func TestBindingService_CreateOwnedAgentPersistsAgentKind(t *testing.T) {
	ctx := context.Background()
	st := newBindingTestStack(t, false)

	binding, err := st.BS.Create(ctx, CreateBindingRequest{
		WorkspaceID:     1,
		ActingUserID:    st.AgentID,
		CreatedByUserID: st.AdminID,
		RepoSlug:        "acme/widget",
		RepoRemoteURL:   "https://github.com/acme/widget",
		TokenScopes:     []string{"items:read"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if binding.ActingUserKind != ActingIdentityKindAgent {
		t.Errorf("kind: want %q, got %q", ActingIdentityKindAgent, binding.ActingUserKind)
	}
	if !binding.HasRepo() {
		t.Errorf("HasRepo should be true; got %+v", binding)
	}
}

func TestBindingService_CreateRejectsBlockedIdentity(t *testing.T) {
	ctx := context.Background()
	st := newBindingTestStack(t, false)

	// Master flag is off → centralized service user is blocked.
	_, err := st.BS.Create(ctx, CreateBindingRequest{
		WorkspaceID:     1,
		ActingUserID:    st.SvcUserID,
		CreatedByUserID: st.AdminID,
	})
	if !errors.Is(err, ErrActingIdentityCentralizedGated) {
		t.Errorf("err: want ErrActingIdentityCentralizedGated, got %v", err)
	}
}

func TestBindingService_MaybeStartRun_FiresWhenAssigneeMatchesBinding(t *testing.T) {
	ctx := context.Background()
	st := newBindingTestStack(t, true /* withRunService */)

	// Binding with no Repo fields — the threading-through-of-RepoSpec is
	// covered by RunService's worktree tests; here we just verify the
	// trigger dispatched.
	if _, err := st.Bindings.Insert(ctx, &models.WorkspaceAgentBinding{
		WorkspaceID:     1,
		ActingUserID:    st.AgentID,
		ActingUserKind:  ActingIdentityKindAgent,
		TokenScopes:     []string{"items:read"},
		TokenTTLMinutes: 15,
		CreatedByUserID: st.AdminID,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	// Seed an item so the agent_runs FK to items resolves.
	itemID := seedItem(t, st.DB, 1)
	newAssignee := st.AgentID
	if err := st.BS.MaybeStartRunForAssignee(ctx, 1, itemID, nil, &newAssignee); err != nil {
		t.Fatalf("maybe start: %v", err)
	}
	// MaybeStartRunForAssignee returns once the run is dispatched (the
	// goroutine inside RunService.execute does the real work). Wait for it.
	st.BS.runs.Wait()

	if got := atomic.LoadInt32(st.RunCalls); got != 1 {
		t.Fatalf("expected 1 runner invocation, got %d", got)
	}
	in := st.LastInput()
	if in.RunID == 0 {
		t.Errorf("RunInput.RunID should be set; got %+v", in)
	}
}

func TestBindingService_MaybeStartRun_NoOpWhenAssigneeUnchanged(t *testing.T) {
	ctx := context.Background()
	st := newBindingTestStack(t, true)
	if _, err := st.Bindings.Insert(ctx, &models.WorkspaceAgentBinding{
		WorkspaceID: 1, ActingUserID: st.AgentID, ActingUserKind: ActingIdentityKindAgent, CreatedByUserID: st.AdminID,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	old := st.AgentID
	newVal := st.AgentID
	if err := st.BS.MaybeStartRunForAssignee(ctx, 1, 7, &old, &newVal); err != nil {
		t.Fatalf("maybe start: %v", err)
	}
	if got := atomic.LoadInt32(st.RunCalls); got != 0 {
		t.Fatalf("expected 0 runner invocations (assignee unchanged), got %d", got)
	}
}

func TestBindingService_MaybeStartRun_NoOpWhenNoBindingMatches(t *testing.T) {
	ctx := context.Background()
	st := newBindingTestStack(t, true)
	newVal := st.AgentID // no binding configured
	if err := st.BS.MaybeStartRunForAssignee(ctx, 1, 7, nil, &newVal); err != nil {
		t.Fatalf("maybe start: %v", err)
	}
	if got := atomic.LoadInt32(st.RunCalls); got != 0 {
		t.Fatalf("expected 0 runner invocations (no binding), got %d", got)
	}
}
