package services

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

func newPRServiceTestStack(t *testing.T) (
	*AgentPRService,
	*repository.WorkspaceAgentBindingRepository,
	database.Database,
	int, // bindingID for a binding with SCM connection
	int, // workspaceRepositoryID
	int, // itemID
	*int32, // openPR call count
	func() OpenPRRequest, // last captured request
) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s/pr_service.db?mode=memory&cache=shared", t.TempDir())
	db, err := database.NewSQLiteDBWithPoolSizes(dsn, 4, 1)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces(id, name, key, active) VALUES (1, 'WS', 'WS', 1)`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(email, username, first_name, last_name, is_agent) VALUES ('admin@example.com','admin','A','',0)`); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(email, username, first_name, last_name, is_agent) VALUES ('agent@agents.local','agent','Ag','Ent',1)`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	var adminID, agentID int
	_ = db.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&adminID)
	_ = db.QueryRow(`SELECT id FROM users WHERE username='agent'`).Scan(&agentID)

	if _, err := db.Exec(`INSERT INTO scm_providers(slug, name, provider_type, auth_method, base_url) VALUES ('gitea1','Gitea','gitea','oauth','https://gitea.example.com')`); err != nil {
		t.Fatalf("seed scm provider: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_scm_connections(workspace_id, scm_provider_id) VALUES (1, 1)`); err != nil {
		t.Fatalf("seed scm connection: %v", err)
	}
	var connID int
	_ = db.QueryRow(`SELECT id FROM workspace_scm_connections LIMIT 1`).Scan(&connID)

	if _, err := db.Exec(
		`INSERT INTO workspace_repositories(workspace_scm_connection_id, repository_external_id, repository_name, repository_url) VALUES (?, ?, ?, ?)`,
		connID, "rep-1", "acme/widget", "https://gitea.example.com/acme/widget",
	); err != nil {
		t.Fatalf("seed workspace_repository: %v", err)
	}
	var wsRepoID int
	_ = db.QueryRow(`SELECT id FROM workspace_repositories LIMIT 1`).Scan(&wsRepoID)

	if _, err := db.Exec(`INSERT INTO items(workspace_id, title) VALUES (1, 'demo')`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	var itemID int
	_ = db.QueryRow(`SELECT id FROM items LIMIT 1`).Scan(&itemID)

	bindingsRepo := repository.NewWorkspaceAgentBindingRepository(db)
	bindingID, err := bindingsRepo.Insert(context.Background(), &models.WorkspaceAgentBinding{
		WorkspaceID:     1,
		ActingUserID:    agentID,
		ActingUserKind:  ActingIdentityKindAgent,
		RepoSlug:        "acme/widget",
		RepoRemoteURL:   "https://gitea.example.com/acme/widget.git",
		RepoBaseRef:     "main",
		SCMConnectionID: &connID,
		CreatedByUserID: adminID,
	})
	if err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	var calls int32
	var lastReq OpenPRRequest
	openPR := func(_ context.Context, req OpenPRRequest) (*OpenedPR, error) {
		atomic.AddInt32(&calls, 1)
		lastReq = req
		return &OpenedPR{
			ID:     "42",
			Number: 42,
			URL:    "https://gitea.example.com/acme/widget/pulls/42",
			Title:  req.Title,
			State:  "Open",
			Author: "agent",
		}, nil
	}

	svc, err := NewAgentPRService(AgentPRServiceOptions{
		Bindings: bindingsRepo,
		OpenPR:   openPR,
		DB:       db,
		Logger:   silentLogger(t),
	})
	if err != nil {
		t.Fatalf("new svc: %v", err)
	}
	return svc, bindingsRepo, db, bindingID, wsRepoID, itemID, &calls, func() OpenPRRequest { return lastReq }
}

func TestAgentPRService_OpensPRAndWritesItemLink(t *testing.T) {
	svc, _, db, bindingID, wsRepoID, itemID, calls, lastReq := newPRServiceTestStack(t)

	itemIDPtr := itemID
	svc.AfterRun(context.Background(), PostRunInfo{
		RunID:       7,
		WorkspaceID: 1,
		ItemID:      &itemIDPtr,
		BindingID:   bindingID,
		Status:      models.AgentRunStatusSucceeded,
		Branch:      "agent-runs/run-7",
		BaseCommit:  "abc123",
	})
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("openPR calls: want 1, got %d", got)
	}
	req := lastReq()
	if req.Owner != "acme" || req.Repo != "widget" {
		t.Errorf("owner/repo: want acme/widget, got %s/%s", req.Owner, req.Repo)
	}
	if req.HeadBranch != "agent-runs/run-7" || req.BaseBranch != "main" {
		t.Errorf("branches: want head=agent-runs/run-7 base=main, got head=%s base=%s", req.HeadBranch, req.BaseBranch)
	}
	if !req.Draft {
		t.Errorf("expected draft=true; got %v", req.Draft)
	}

	// Verify the item_scm_links row landed.
	var (
		linkType, externalURL, state string
		linkedItemID, linkedRepoID   int
	)
	if err := db.QueryRow(`
		SELECT item_id, workspace_repository_id, link_type, external_url, state
		FROM item_scm_links
		WHERE link_type = 'pull_request' AND external_id = '42'
	`).Scan(&linkedItemID, &linkedRepoID, &linkType, &externalURL, &state); err != nil {
		t.Fatalf("read link: %v", err)
	}
	if linkedItemID != itemID {
		t.Errorf("item_id: want %d, got %d", itemID, linkedItemID)
	}
	if linkedRepoID != wsRepoID {
		t.Errorf("workspace_repository_id: want %d, got %d", wsRepoID, linkedRepoID)
	}
	if state != "open" {
		t.Errorf("state lowercased: want open, got %q", state)
	}
}

func TestAgentPRService_SkipsOnNonSuccessStatus(t *testing.T) {
	svc, _, _, bindingID, _, itemID, calls, _ := newPRServiceTestStack(t)
	itemIDPtr := itemID
	svc.AfterRun(context.Background(), PostRunInfo{
		RunID:     7,
		BindingID: bindingID,
		ItemID:    &itemIDPtr,
		Status:    models.AgentRunStatusFailed,
		Branch:    "agent-runs/run-7",
	})
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("openPR should not be called on failed status, got %d", got)
	}
}

func TestAgentPRService_SkipsWhenBindingHasNoSCMConnection(t *testing.T) {
	svc, bindingsRepo, db, _, _, itemID, calls, _ := newPRServiceTestStack(t)

	// Make a second binding with no SCM connection.
	if _, err := db.Exec(`INSERT INTO users(email, username, first_name, last_name, is_agent) VALUES ('agent2@agents.local','agent2','Ag2','',1)`); err != nil {
		t.Fatalf("seed agent2: %v", err)
	}
	var agent2ID, adminID int
	_ = db.QueryRow(`SELECT id FROM users WHERE username='agent2'`).Scan(&agent2ID)
	_ = db.QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&adminID)
	otherID, err := bindingsRepo.Insert(context.Background(), &models.WorkspaceAgentBinding{
		WorkspaceID: 1, ActingUserID: agent2ID, ActingUserKind: ActingIdentityKindAgent, CreatedByUserID: adminID,
	})
	if err != nil {
		t.Fatalf("seed binding 2: %v", err)
	}
	itemIDPtr := itemID
	svc.AfterRun(context.Background(), PostRunInfo{
		RunID:     7,
		BindingID: otherID,
		ItemID:    &itemIDPtr,
		Status:    models.AgentRunStatusSucceeded,
		Branch:    "agent-runs/run-7",
	})
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("openPR should not be called when SCM connection absent, got %d", got)
	}
}

func TestAgentPRService_OpenPRErrorIsContained(t *testing.T) {
	svc, _, _, bindingID, _, itemID, _, _ := newPRServiceTestStack(t)
	svc.openPR = func(context.Context, OpenPRRequest) (*OpenedPR, error) {
		return nil, errors.New("upstream 500")
	}
	itemIDPtr := itemID
	// Must not panic / propagate the error.
	svc.AfterRun(context.Background(), PostRunInfo{
		RunID:     7,
		BindingID: bindingID,
		ItemID:    &itemIDPtr,
		Status:    models.AgentRunStatusSucceeded,
		Branch:    "agent-runs/run-7",
	})
}

func TestAgentPRService_SplitRepoSlug(t *testing.T) {
	owner, repo, ok := splitRepoSlug("acme/widget")
	if !ok || owner != "acme" || repo != "widget" {
		t.Errorf("good slug: got owner=%q repo=%q ok=%v", owner, repo, ok)
	}
	if _, _, ok := splitRepoSlug("not-a-slug"); ok {
		t.Errorf("missing slash should fail")
	}
	if _, _, ok := splitRepoSlug("/widget"); ok {
		t.Errorf("missing owner should fail")
	}
}
