package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// OpenPRRequest is what AgentPRService hands to the OpenPRFn adapter.
type OpenPRRequest struct {
	ConnectionID int
	Owner        string
	Repo         string
	HeadBranch   string
	BaseBranch   string
	Title        string
	Body         string
	Draft        bool
}

// OpenedPR is what the adapter returns. Mirrors the subset of
// scm.PullRequest the link writeback needs; keeping the type in this
// package avoids the services→scm import cycle (scm already imports
// services for milestone-attach).
type OpenedPR struct {
	ID     string
	Number int
	URL    string
	Title  string
	State  string
	Author string
}

// OpenPRFn is the orchestrator-level seam to whatever SCM driver opens
// the PR. Production wires this to a closure that builds a scm.Provider
// via scm.CredentialResolver and calls Provider.CreatePullRequest; tests
// pass a deterministic stand-in.
type OpenPRFn func(ctx context.Context, req OpenPRRequest) (*OpenedPR, error)

// AgentPRService is the WI-90 post-run hook: on a successful run that
// produced a pushed branch, it opens a draft pull request via the
// OpenPRFn adapter and writes an item_scm_links row of type=pull_request
// so the PR shows on the bound item. Works against both GitHub and
// Gitea because the production adapter routes through scm.Provider —
// the service itself has no provider-specific knowledge.
type AgentPRService struct {
	bindings *repository.WorkspaceAgentBindingRepository
	openPR   OpenPRFn
	db       database.Database
	logger   *log.Logger
}

// AgentPRServiceOptions wires the service.
type AgentPRServiceOptions struct {
	Bindings *repository.WorkspaceAgentBindingRepository
	OpenPR   OpenPRFn
	DB       database.Database
	Logger   *log.Logger
}

// NewAgentPRService constructs the service.
func NewAgentPRService(opts AgentPRServiceOptions) (*AgentPRService, error) {
	if opts.Bindings == nil {
		return nil, errors.New("agent pr service: bindings repo is required")
	}
	if opts.OpenPR == nil {
		return nil, errors.New("agent pr service: OpenPR is required")
	}
	if opts.DB == nil {
		return nil, errors.New("agent pr service: DB is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &AgentPRService{
		bindings: opts.Bindings,
		openPR:   opts.OpenPR,
		db:       opts.DB,
		logger:   logger,
	}, nil
}

// AfterRun implements PostRunHook. The full open-PR-and-link path only
// runs when (1) the run succeeded, (2) the binding still exists and
// carries an SCM connection, (3) a branch was prepared (i.e. the agent
// had a worktree to push from). Everything else is logged-and-skipped
// so a mis-configured binding can't take the run's terminal status
// down with it.
func (s *AgentPRService) AfterRun(ctx context.Context, info PostRunInfo) {
	if info.Status != models.AgentRunStatusSucceeded {
		return
	}
	if info.BindingID == 0 || info.Branch == "" {
		return
	}
	binding, err := s.bindings.Get(ctx, info.BindingID)
	if err != nil {
		s.logger.Printf("agent pr: load binding=%d for run=%d: %v", info.BindingID, info.RunID, err)
		return
	}
	if binding.SCMConnectionID == nil {
		return
	}
	if binding.RepoSlug == "" {
		s.logger.Printf("agent pr: binding=%d has no repo_slug; skipping PR for run=%d", binding.ID, info.RunID)
		return
	}
	owner, repo, ok := splitRepoSlug(binding.RepoSlug)
	if !ok {
		s.logger.Printf("agent pr: unparseable repo_slug %q for binding=%d", binding.RepoSlug, binding.ID)
		return
	}

	base := binding.RepoBaseRef
	if base == "" {
		base = "main"
	}
	title := fmt.Sprintf("agent: run %d", info.RunID)
	if info.ItemID != nil {
		title = fmt.Sprintf("agent: work item %d (run %d)", *info.ItemID, info.RunID)
	}
	body := fmt.Sprintf(
		"Opened by the Windshift coding-agent harness.\n\nRun id: %d\nBase commit: %s\nBranch: %s\n",
		info.RunID, info.BaseCommit, info.Branch,
	)

	pr, err := s.openPR(ctx, OpenPRRequest{
		ConnectionID: *binding.SCMConnectionID,
		Owner:        owner,
		Repo:         repo,
		HeadBranch:   info.Branch,
		BaseBranch:   base,
		Title:        title,
		Body:         body,
		Draft:        true,
	})
	if err != nil {
		s.logger.Printf("agent pr: open pr run=%d %s/%s: %v", info.RunID, owner, repo, err)
		return
	}
	s.logger.Printf("agent pr: opened PR %s for run=%d (binding=%d, %s/%s)", pr.URL, info.RunID, binding.ID, owner, repo)

	if info.ItemID == nil {
		return
	}
	if err := s.upsertItemSCMLink(ctx, *info.ItemID, *binding.SCMConnectionID, binding.RepoSlug, pr); err != nil {
		s.logger.Printf("agent pr: upsert item_scm_link run=%d: %v", info.RunID, err)
	}
}

// upsertItemSCMLink writes (or refreshes) the pull_request link row that
// surfaces the PR on the bound item. Resolves the workspace_repository
// row by (connection_id, repo_name); skips silently when no row exists
// rather than papering over the gap with a synthetic insert that the
// existing SCM sync code wouldn't know about.
func (s *AgentPRService) upsertItemSCMLink(ctx context.Context, itemID, connectionID int, repoSlug string, pr *OpenedPR) error {
	var workspaceRepoID int
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM workspace_repositories
		WHERE workspace_scm_connection_id = ? AND repository_name = ?
	`, connectionID, repoSlug).Scan(&workspaceRepoID)
	if err != nil {
		return fmt.Errorf("locate workspace_repositories row: %w", err)
	}

	state := strings.ToLower(pr.State)
	externalID := pr.ID
	if externalID == "" {
		externalID = fmt.Sprintf("%d", pr.Number)
	}

	_, err = s.db.ExecWriteContext(ctx, `
		INSERT INTO item_scm_links
			(item_id, workspace_repository_id, link_type, external_id, external_url,
			 title, state, author_name, detection_source)
		VALUES (?, ?, 'pull_request', ?, ?, ?, ?, ?, 'coding_agent')
		ON CONFLICT(item_id, workspace_repository_id, link_type, external_id)
		DO UPDATE SET
			external_url = excluded.external_url,
			title = excluded.title,
			state = excluded.state,
			author_name = excluded.author_name,
			updated_at = CURRENT_TIMESTAMP
	`,
		itemID, workspaceRepoID, externalID, pr.URL,
		pr.Title, state, pr.Author,
	)
	return err
}

// splitRepoSlug splits "owner/repo" into its parts. Returns ok=false
// when the input doesn't have exactly one slash separator.
func splitRepoSlug(slug string) (owner, repo string, ok bool) {
	parts := strings.SplitN(slug, "/", 3)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
