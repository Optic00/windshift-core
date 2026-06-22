package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// OpenPRRequest is what AgentPRService hands to the OpenPRFn adapter.
type OpenPRRequest struct {
	ConnectionID int
	// UserID is the credential principal: on OAuth connections the PR is
	// opened with this user's personal token (the run's triggering user,
	// WI-275). 0 = connection-level credential (PAT / GitHub App, and
	// legacy runs with no recorded triggering user).
	UserID     int
	Owner      string
	Repo       string
	HeadBranch string
	BaseBranch string
	Title      string
	Body       string
	Draft      bool
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

// PRCommentRequest is what AgentPRService hands to the CommentPRFn adapter to
// post a comment on an existing PR — a continuation run's "pushed updates" note.
type PRCommentRequest struct {
	ConnectionID int
	UserID       int // credential principal, as OpenPRRequest.UserID
	Owner        string
	Repo         string
	Number       int // PR number
	Body         string
}

// CommentPRFn is the seam to whatever SCM driver posts a PR comment. Production
// wires it to a closure that builds a scm.Provider and calls
// IssueProvider.CreateIssueComment (a PR is an issue on both GitHub and Gitea).
// Optional: when nil, a continuation run still skips opening a duplicate PR, it
// just posts no progress comment.
type CommentPRFn func(ctx context.Context, req PRCommentRequest) error

// AgentPRService is the WI-90 post-run hook: on a successful run that
// produced a pushed branch, it opens a draft pull request via the
// OpenPRFn adapter and writes an item_scm_links row of type=pull_request
// so the PR shows on the bound item. Works against both GitHub and
// Gitea because the production adapter routes through scm.Provider —
// the service itself has no provider-specific knowledge.
type AgentPRService struct {
	bindings  *repository.WorkspaceAgentBindingRepository
	openPR    OpenPRFn
	commentPR CommentPRFn
	db        database.Database
	logger    *log.Logger
}

// AgentPRServiceOptions wires the service.
type AgentPRServiceOptions struct {
	Bindings *repository.WorkspaceAgentBindingRepository
	OpenPR   OpenPRFn
	// CommentPR posts a comment on an existing PR for continuation runs.
	// Optional — see CommentPRFn.
	CommentPR CommentPRFn
	DB        database.Database
	Logger    *log.Logger
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
		bindings:  opts.Bindings,
		openPR:    opts.OpenPR,
		commentPR: opts.CommentPR,
		db:        opts.DB,
		logger:    logger,
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
	// Need a binding and at least one delivered branch — either the scalar
	// primary branch (legacy/single-repo) or any per-repo branch (WI-449).
	if info.BindingID == 0 {
		return
	}
	if info.Branch == "" && !hasPushedRepo(info.Repos) {
		return
	}
	binding, err := s.bindings.Get(ctx, info.BindingID)
	if err != nil {
		s.logger.Printf("agent pr: load binding=%d for run=%d: %v", info.BindingID, info.RunID, err)
		return
	}

	// One PR per changed repo (WI-449). Each repo the agent committed to gets
	// its own branch + PR; only the PRIMARY repo's PR is linked to the work
	// item. A repo with no new commits has an empty branch and is skipped.
	for _, pr := range s.prReposFor(info, binding) {
		owner, repo, ok := splitRepoSlug(pr.slug)
		if !ok {
			s.logger.Printf("agent pr: unparseable repo_slug %q for binding=%d", pr.slug, binding.ID)
			continue
		}
		// Continuation run: the runner already pushed commits onto the existing
		// PR's head branch (this repo's branch), so the PR grew in place —
		// opening another PR would duplicate it. Comment on the continued PR
		// instead. Sibling repos changed in the same run still open fresh PRs.
		if info.Trigger.IsContinuation() && pr.slug == info.Trigger.ContinueRepoSlug {
			s.afterContinuationRun(ctx, info, binding, pr.connID, owner, repo)
			continue
		}
		s.openRepoPR(ctx, info, binding, pr, owner, repo)
	}
}

// hasPushedRepo reports whether any per-repo result carries a delivered branch.
func hasPushedRepo(repos []PostRunRepo) bool {
	for _, r := range repos {
		if r.Branch != "" {
			return true
		}
	}
	return false
}

// prRepo is one repo's push result enriched with the binding metadata the PR
// hook needs: which SCM connection to authenticate with and whether it's the
// primary (work-item-linked) repo.
type prRepo struct {
	slug       string
	branch     string
	baseCommit string
	baseRef    string
	connID     int
	primary    bool
}

// prReposFor resolves the repos to open PRs for: info.Repos (WI-449) when the
// run reported per-repo results, else the legacy single primary repo derived
// from info.Branch. Each is enriched with its SCM connection + primary flag by
// matching the binding's repos by slug. Repos with no branch (no_changes) or no
// SCM connection are dropped.
func (s *AgentPRService) prReposFor(info PostRunInfo, binding *models.WorkspaceAgentBinding) []prRepo {
	byslug := make(map[string]models.BindingRepo, len(binding.Repos))
	for _, br := range binding.Repos {
		byslug[br.RepoSlug] = br
	}
	resolve := func(slug, branch, baseCommit string) (prRepo, bool) {
		if branch == "" {
			return prRepo{}, false
		}
		br, ok := byslug[slug]
		if !ok {
			// Fall back to the binding's mirrored primary fields for a repo not
			// found in the child table (pre-migration / legacy single repo).
			if binding.SCMConnectionID == nil || binding.RepoSlug != slug {
				return prRepo{}, false
			}
			return prRepo{slug: slug, branch: branch, baseCommit: baseCommit, baseRef: binding.RepoBaseRef, connID: *binding.SCMConnectionID, primary: true}, true
		}
		if br.SCMConnectionID == nil {
			return prRepo{}, false
		}
		return prRepo{slug: slug, branch: branch, baseCommit: baseCommit, baseRef: br.RepoBaseRef, connID: *br.SCMConnectionID, primary: br.IsPrimary}, true
	}

	var out []prRepo
	if len(info.Repos) > 0 {
		for _, r := range info.Repos {
			if pr, ok := resolve(r.RepoSlug, r.Branch, r.BaseCommit); ok {
				out = append(out, pr)
			}
		}
		return out
	}
	// Legacy single-repo run: one repo from the scalar branch fields.
	if pr, ok := resolve(binding.RepoSlug, info.Branch, info.BaseCommit); ok {
		out = append(out, pr)
	}
	return out
}

// openRepoPR opens a draft PR for one changed repo and, when it is the primary
// repo, links it to the work item.
func (s *AgentPRService) openRepoPR(ctx context.Context, info PostRunInfo, binding *models.WorkspaceAgentBinding, pr prRepo, owner, repo string) {
	base := pr.baseRef
	if base == "" {
		base = "main"
	}
	title := fmt.Sprintf("agent: run %d", info.RunID)
	if info.ItemID != nil {
		// Prefer a human-readable title derived from the bound work item
		// ("WI-595: <item title>"), matching the manual open-PR path
		// (scm_item_links.go). Fall back to the generic numeric form only
		// when the item can't be loaded or has no title.
		if itemTitle := s.itemPRTitle(ctx, *info.ItemID); itemTitle != "" {
			title = itemTitle
		} else {
			title = fmt.Sprintf("agent: work item %d (run %d)", *info.ItemID, info.RunID)
		}
	}
	// The agent's finish summary (WI-400), when present, leads the body as the
	// PR note; the harness footer (run id / base / branch) follows a rule.
	body := fmt.Sprintf(
		"Opened by the Windshift coding-agent harness.\n\nRun id: %d\nBase commit: %s\nBranch: %s\n",
		info.RunID, pr.baseCommit, pr.branch,
	)
	if note := boundPRNote(info.Summary); note != "" {
		body = note + "\n\n---\n\n" + body
	}

	opened, err := s.openPRWithRetry(ctx, info.RunID, OpenPRRequest{
		ConnectionID: pr.connID,
		UserID:       info.TriggeredByUserID,
		Owner:        owner,
		Repo:         repo,
		HeadBranch:   pr.branch,
		BaseBranch:   base,
		Title:        title,
		Body:         body,
		Draft:        true,
	})
	if err != nil {
		s.logger.Printf("agent pr: open pr run=%d %s/%s: %v", info.RunID, owner, repo, err)
		return
	}
	s.logger.Printf("agent pr: opened PR %s for run=%d (binding=%d, %s/%s, primary=%t)", opened.URL, info.RunID, binding.ID, owner, repo, pr.primary)

	// Only the primary repo's PR represents the work item; secondary repos open
	// PRs but are not linked back to the item.
	if !pr.primary || info.ItemID == nil {
		return
	}
	if err := s.upsertItemSCMLink(ctx, *info.ItemID, pr.connID, pr.slug, opened); err != nil {
		s.logger.Printf("agent pr: upsert item_scm_link run=%d: %v", info.RunID, err)
	}
}

// openPRRetryAttempts bounds how many times AfterRun re-tries the OpenPR
// adapter on a transient failure. SCM PR creation occasionally times out or
// 5xxes when the upstream (Codeberg/Gitea, GitHub) is slow; without a retry the
// run's branch is pushed but no PR exists, forcing a human to open it by hand.
// Three attempts with backoff turn a transient blip into a non-event.
const openPRRetryAttempts = 3

// openPRAttemptTimeout caps a single OpenPR attempt so one hung POST can't
// swallow the whole post-run budget, leaving room for a retry. It sits under
// the SCM HTTP client's own 30s timeout — whichever fires first aborts the
// attempt and the loop backs off. A var (not a const) so tests can shrink it.
var openPRAttemptTimeout = 20 * time.Second

// openPRRetryBackoff is the base delay between OpenPR attempts; it doubles each
// retry (2s, then 4s) to give a struggling upstream room to recover. A var (not
// a const) so tests can shrink it.
var openPRRetryBackoff = 2 * time.Second

// openPRWithRetry calls the OpenPR adapter, retrying transient failures so a
// flaky SCM API (a Codeberg/Gitea timeout, a 5xx, a dropped connection) doesn't
// leave the run's pushed branch without a PR. Permanent failures — bad
// credentials, repo not found, a PR that already exists — are surfaced
// immediately (the production adapter classifies them via NewPermanentOpenPRError);
// retrying them only burns the post-run budget. Each attempt runs under its own
// bounded sub-context so a single hung request can't consume the whole window,
// and the backoff aborts the moment the parent context is canceled.
func (s *AgentPRService) openPRWithRetry(ctx context.Context, runID int, req OpenPRRequest) (*OpenedPR, error) {
	var lastErr error
	for attempt := 1; attempt <= openPRRetryAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, openPRAttemptTimeout)
		pr, err := s.openPR(attemptCtx, req)
		cancel()
		if err == nil {
			return pr, nil
		}
		lastErr = err
		// Parent budget exhausted/canceled, or a permanent provider error: a
		// retry can't help, so surface it now.
		if ctx.Err() != nil || IsPermanentOpenPRError(err) {
			return nil, err
		}
		if attempt == openPRRetryAttempts {
			break
		}
		backoff := openPRRetryBackoff << (attempt - 1)
		s.logger.Printf("agent pr: open pr run=%d attempt %d/%d failed: %v; retrying in %s",
			runID, attempt, openPRRetryAttempts, err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// permanentOpenPRError marks an OpenPRFn failure that must not be retried — a
// bad credential, a missing repo, or a PR that already exists. The production
// OpenPR adapter classifies the scm package's sentinel errors into this so the
// retry loop in AfterRun can decide retryability without importing scm (which
// would form a services→scm→services import cycle).
type permanentOpenPRError struct{ err error }

func (e *permanentOpenPRError) Error() string { return e.err.Error() }
func (e *permanentOpenPRError) Unwrap() error { return e.err }

// NewPermanentOpenPRError wraps err so AfterRun's retry loop treats it as
// terminal. A nil err is returned unchanged.
func NewPermanentOpenPRError(err error) error {
	if err == nil {
		return nil
	}
	return &permanentOpenPRError{err: err}
}

// IsPermanentOpenPRError reports whether err was wrapped by NewPermanentOpenPRError.
func IsPermanentOpenPRError(err error) bool {
	var p *permanentOpenPRError
	return errors.As(err, &p)
}

// afterContinuationRun posts a progress comment on the PR a continuation run
// just pushed commits to. It never opens a PR (the PR already exists) and never
// writes a new link row (the PR was linked when it was first opened/detected).
// A nil commentPR seam degrades to a log line — the commits are already on the
// PR regardless.
func (s *AgentPRService) afterContinuationRun(ctx context.Context, info PostRunInfo, binding *models.WorkspaceAgentBinding, connID int, owner, repo string) {
	number := info.Trigger.ContinuePRNumber
	s.logger.Printf("agent pr: continuation run=%d pushed to %s/%s PR #%d (binding=%d)", info.RunID, owner, repo, number, binding.ID)
	if s.commentPR == nil || number <= 0 {
		return
	}
	if err := s.commentPR(ctx, PRCommentRequest{
		ConnectionID: connID,
		UserID:       info.TriggeredByUserID,
		Owner:        owner,
		Repo:         repo,
		Number:       number,
		Body:         continuationComment(info.Summary),
	}); err != nil {
		s.logger.Printf("agent pr: comment continuation run=%d %s/%s PR #%d: %v", info.RunID, owner, repo, number, err)
	}
}

// triggerTokenRE matches the literal agent trigger token case-insensitively so
// it can be stripped from any agent-authored comment body. Stripping the token
// from the agent's own output is loop-guard layer 2: even if the marker layer
// failed, the agent's comment carries no token to re-fire the poller.
var triggerTokenRE = regexp.MustCompile("(?i)" + regexp.QuoteMeta(models.DefaultAgentTriggerToken))

// stripTriggerToken removes every occurrence of the trigger token from s.
func stripTriggerToken(s string) string {
	return triggerTokenRE.ReplaceAllString(s, "")
}

// continuationComment builds the PR comment body for a continuation run: the
// hidden agent marker (loop-guard layer 1) followed by a short note and, when
// present, the agent's finish summary — token-stripped (layer 2) and bounded.
func continuationComment(summary string) string {
	body := models.AgentCommentMarker + "\n\nThe Windshift coding agent pushed updates to this pull request."
	if note := strings.TrimSpace(stripTriggerToken(summary)); note != "" {
		body += "\n\n---\n\n" + boundPRNote(note)
	}
	return body
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

	// The canonical external_id for a pull_request link is the PR *number*
	// (the per-repo sequence number), not the provider's global database ID.
	// Both the SCM sync detection path and CreateItemSCMLink key on the
	// number, and RefreshItemSCMLink calls GetPullRequest(owner, repo,
	// number) — so a link keyed on the global ID 404s on refresh.
	state := strings.ToLower(pr.State)
	externalID := strconv.Itoa(pr.Number)

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
	if err == nil {
		// Live-update publish (WI-484): the coding agent opened/updated a PR link
		// on the item; refresh its SCM-links section.
		PublishItemChange(itemID, ItemChangeLink)
	}
	return err
}

// itemPRTitle builds a human-readable PR title from the bound work item —
// "<KEY>: <title>" (e.g. "WI-595: Add recently-viewed work items sub-palette"),
// the same shape the manual open-PR path uses (scm_item_links.go). Returns ""
// when the item can't be loaded or carries no title, so the caller falls back
// to the generic "agent: work item N (run M)" form.
func (s *AgentPRService) itemPRTitle(ctx context.Context, itemID int) string {
	var workspaceKey, itemTitle string
	var itemNumber int
	err := s.db.QueryRowContext(ctx, `
		SELECT w.key, i.workspace_item_number, i.title
		FROM items i
		JOIN workspaces w ON i.workspace_id = w.id
		WHERE i.id = ?
	`, itemID).Scan(&workspaceKey, &itemNumber, &itemTitle)
	if err != nil {
		s.logger.Printf("agent pr: load item=%d for PR title: %v", itemID, err)
		return ""
	}
	itemTitle = strings.TrimSpace(itemTitle)
	if itemTitle == "" {
		return ""
	}
	return boundPRTitle(fmt.Sprintf("%s-%d: %s", workspaceKey, itemNumber, itemTitle))
}

// maxPRTitleBytes caps the derived PR title well under common SCM limits
// (GitHub allows 256 chars) so a long item title can't 422 the create.
const maxPRTitleBytes = 200

// boundPRTitle caps the title at maxPRTitleBytes on a rune boundary, marking a
// truncation with an ellipsis so a clipped title never reads as the whole thing.
func boundPRTitle(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxPRTitleBytes {
		return s
	}
	cut := maxPRTitleBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

// maxPRNoteBytes bounds the agent-supplied PR note (WI-400) well under common
// SCM pull-request body limits (GitHub caps the body at 65536 chars), leaving
// room for the harness footer. The note is already HTML-stripped + sanitized
// upstream (RichText at the result handler); this is the final length guard
// before it reaches the provider, so a runaway summary can't 422 the create.
const maxPRNoteBytes = 16384

// boundPRNote trims the note and caps it at maxPRNoteBytes on a rune boundary,
// flagging a truncation so a clipped note never reads as the whole story.
func boundPRNote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxPRNoteBytes {
		return s
	}
	cut := maxPRNoteBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "\n\n…(truncated)"
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
