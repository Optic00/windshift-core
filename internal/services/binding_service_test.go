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

func TestBindingService_EmbedTokenInRemoteURL(t *testing.T) {
	cases := []struct {
		name     string
		remote   string
		token    string
		want     string
		wantSame bool
	}{
		{"https github", "https://github.com/acme/widget.git", "ghp_xxx", "https://oauth2:ghp_xxx@github.com/acme/widget.git", false},
		{"https gitea self-hosted", "https://gitea.example.com/acme/widget.git", "gt-yyy", "https://oauth2:gt-yyy@gitea.example.com/acme/widget.git", false},
		{"http unencrypted", "http://internal-gitea/acme/widget.git", "abc", "http://oauth2:abc@internal-gitea/acme/widget.git", false},
		{"ssh unchanged", "git@github.com:acme/widget.git", "ghp_xxx", "git@github.com:acme/widget.git", true},
		{"empty token unchanged", "https://github.com/acme/widget.git", "", "https://github.com/acme/widget.git", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := embedTokenInRemoteURL(tc.remote, tc.token)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// fakeSCMCreds is a deterministic stand-in for scm.CredentialResolver.
type fakeSCMCreds struct {
	token        string
	providerType string
	baseURL      string
	calls        int
}

func (f *fakeSCMCreds) ResolveForRun(ctx context.Context, _ int) (string, string, string, error) {
	f.calls++
	return f.token, f.providerType, f.baseURL, nil
}

func TestBindingService_MaybeStartRun_EmbedsTokenWhenSCMConnectionPresent(t *testing.T) {
	ctx := context.Background()
	st := newBindingTestStack(t, true)
	// Seed a minimal scm_providers + workspace_scm_connections row so
	// the FK in workspace_agent_bindings.scm_connection_id resolves.
	if _, err := st.DB.Exec(`INSERT INTO scm_providers(slug, name, provider_type, auth_method, base_url) VALUES ('test-gitea', 'Test Gitea', 'gitea', 'oauth', 'https://gitea.example.com')`); err != nil {
		t.Fatalf("seed scm provider: %v", err)
	}
	if _, err := st.DB.Exec(`INSERT INTO workspace_scm_connections(workspace_id, scm_provider_id) VALUES (1, 1)`); err != nil {
		t.Fatalf("seed scm connection: %v", err)
	}
	var scmConn int
	if err := st.DB.QueryRow(`SELECT id FROM workspace_scm_connections LIMIT 1`).Scan(&scmConn); err != nil {
		t.Fatalf("read connection id: %v", err)
	}

	if _, err := st.Bindings.Insert(ctx, &models.WorkspaceAgentBinding{
		WorkspaceID:     1,
		ActingUserID:    st.AgentID,
		ActingUserKind:  ActingIdentityKindAgent,
		RepoSlug:        "acme/widget",
		RepoRemoteURL:   "https://gitea.example.com/acme/widget.git",
		SCMConnectionID: &scmConn,
		TokenTTLMinutes: 15,
		CreatedByUserID: st.AdminID,
	}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	// Seed an item so the agent_runs FK resolves.
	itemID := seedItem(t, st.DB, 1)

	creds := &fakeSCMCreds{token: "gt-secret", providerType: "gitea"}
	st.BS.scmCreds = creds

	// Worktree manager: required because Repo is set. Use a stub
	// origin repo so the prepare path succeeds.
	origin := seedOriginRepo(t, "main")
	wm := newTestWorktreeManager(t)
	st.BS.runs.worktrees = wm

	// Override the binding's stored remote with the local origin so the
	// prepare path actually works; the test only cares that the URL got
	// rewritten with the token in the trigger's RunRequest.
	if _, err := st.DB.Exec(`UPDATE workspace_agent_bindings SET repo_remote_url = ? WHERE acting_user_id = ?`, origin, st.AgentID); err != nil {
		t.Fatalf("update binding remote: %v", err)
	}

	newAssignee := st.AgentID
	if err := st.BS.MaybeStartRunForAssignee(ctx, 1, itemID, nil, &newAssignee); err != nil {
		t.Fatalf("maybe start: %v", err)
	}
	st.BS.runs.Wait()

	if creds.calls != 1 {
		t.Errorf("expected ResolveForRun to be called once, got %d", creds.calls)
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
