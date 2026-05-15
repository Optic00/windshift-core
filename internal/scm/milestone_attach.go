package scm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"windshift/internal/repository"
	"windshift/internal/services"
)

// MilestoneAttacher implements services.MilestoneCommitAttacher by
// composing SyncService's existing primitives: provider resolution,
// ItemKeyDetector, findItemByKey, plus a small idempotency check
// against scm_processed_commits.
//
// Lives in the scm package because the deps it composes (provider
// resolution, RefProvider.CompareCommits, ItemKeyDetector) all live
// here. services imports scm only via the interface, so no cycle.
type MilestoneAttacher struct {
	sync   *SyncService
	attach *repository.MilestoneAttachRepository
}

// NewMilestoneAttacher returns an attacher ready to register with the
// create_milestone action executor at startup. Both deps are required;
// AttachCommitIssues returns a clear error if called on a zero-value.
func NewMilestoneAttacher(sync *SyncService, attach *repository.MilestoneAttachRepository) *MilestoneAttacher {
	return &MilestoneAttacher{sync: sync, attach: attach}
}

// AttachCommitIssues walks commits in (base, head], extracts item keys,
// and attaches each matching item in this workspace to the milestone.
// Returns a summary the executor records in stepResult.Output.
//
// Errors fall into two buckets: hard failures (provider missing,
// CompareCommits API error) return non-nil; "this repo just doesn't
// have matching items" returns a zero-attachment result with err=nil
// so the create_milestone action can still report success.
func (m *MilestoneAttacher) AttachCommitIssues(ctx context.Context, in services.MilestoneCommitAttachInput) (services.MilestoneCommitAttachResult, error) {
	var result services.MilestoneCommitAttachResult
	if m.sync == nil || m.attach == nil {
		return result, errors.New("milestone attacher missing deps")
	}

	repoCtx, err := m.loadRepoContext(ctx, in.WorkspaceRepoID)
	if err != nil {
		return result, err
	}

	provider, err := m.sync.resolveProvider(ctx, repoCtx.connectionID)
	if err != nil {
		return result, fmt.Errorf("resolve provider: %w", err)
	}
	rp, ok := provider.(RefProvider)
	if !ok {
		return result, errors.New("provider does not implement RefProvider (CompareCommits)")
	}

	parts := strings.SplitN(repoCtx.repositoryName, "/", 2)
	if len(parts) != 2 {
		return result, fmt.Errorf("invalid repository_name %q", repoCtx.repositoryName)
	}
	owner, repo := parts[0], parts[1]

	commits, err := rp.CompareCommits(ctx, owner, repo, in.BaseRef, in.HeadRef)
	if err != nil {
		return result, fmt.Errorf("compare commits %s..%s: %w", in.BaseRef, in.HeadRef, err)
	}
	result.CommitsScanned = len(commits)

	// Walk in order; one item can be mentioned across multiple commits
	// — dedupe via a per-call set so we only attach once.
	attached := make(map[int]struct{})
	for _, c := range commits {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		processed, err := m.commitAlreadyProcessed(ctx, in.WorkspaceRepoID, c.SHA)
		if err != nil {
			slog.Error("milestone attach: check processed commit", slog.String("component", "scm"), slog.Any("error", err))
			continue
		}
		if processed {
			continue
		}
		keys := m.sync.detector.DetectFromCommit(&c, repoCtx.workspaceKey)
		for _, k := range keys {
			itemID, err := m.sync.findItemByKey(ctx, repoCtx.workspaceID, k.Prefix, k.Number)
			if err != nil || itemID == 0 {
				continue
			}
			if _, dup := attached[itemID]; dup {
				continue
			}
			if err := m.attach.AddItemMilestone(itemID, in.MilestoneID); err != nil &&
				!errors.Is(err, repository.ErrDuplicateEntry) {
				slog.Error("milestone attach: add item_milestones",
					slog.String("component", "scm"),
					slog.Int("item_id", itemID),
					slog.Int("milestone_id", in.MilestoneID),
					slog.Any("error", err))
				continue
			}
			attached[itemID] = struct{}{}
		}
		if err := m.markCommitProcessed(ctx, in.WorkspaceRepoID, c.SHA); err != nil {
			// Marking is non-fatal: a retry will re-detect the same keys
			// and AddItemMilestone returns ErrDuplicateEntry which we
			// ignore above. Worst case is some redundant work next tick.
			slog.Error("milestone attach: mark commit processed", slog.String("component", "scm"), slog.Any("error", err))
		}
	}

	for id := range attached {
		result.AttachedItemIDs = append(result.AttachedItemIDs, id)
	}
	return result, nil
}

// attachRepoContext is the per-repo metadata the attacher needs once: which
// workspace owns the repo, which SCM connection backs it, and the
// owner/repo string for provider calls.
type attachRepoContext struct {
	workspaceID    int
	workspaceKey   string
	connectionID   int
	repositoryName string
}

func (m *MilestoneAttacher) loadRepoContext(ctx context.Context, workspaceRepoID int) (*attachRepoContext, error) {
	var rc attachRepoContext
	err := m.sync.db.QueryRowContext(ctx, `
		SELECT wsc.workspace_id, w.key, wsc.id, wr.repository_name
		FROM workspace_repositories wr
		JOIN workspace_scm_connections wsc ON wsc.id = wr.workspace_scm_connection_id
		JOIN workspaces w ON w.id = wsc.workspace_id
		WHERE wr.id = ?
	`, workspaceRepoID).Scan(&rc.workspaceID, &rc.workspaceKey, &rc.connectionID, &rc.repositoryName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("workspace_repository %d not found", workspaceRepoID)
	}
	if err != nil {
		return nil, fmt.Errorf("load repo context: %w", err)
	}
	return &rc, nil
}

func (m *MilestoneAttacher) commitAlreadyProcessed(ctx context.Context, repoID int, sha string) (bool, error) {
	var n int
	err := m.sync.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM scm_processed_commits
		WHERE workspace_repository_id = ? AND commit_sha = ?
	`, repoID, sha).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (m *MilestoneAttacher) markCommitProcessed(ctx context.Context, repoID int, sha string) error {
	_, err := m.sync.db.ExecContext(ctx, `
		INSERT INTO scm_processed_commits(commit_sha, workspace_repository_id, processed_at, actions_applied)
		VALUES (?, ?, CURRENT_TIMESTAMP, 0)
		ON CONFLICT(commit_sha, workspace_repository_id) DO NOTHING
	`, sha, repoID)
	return err
}

// Compile-time check that MilestoneAttacher satisfies the services
// interface. Adding/removing methods on either side fails the build
// here rather than at runtime via a type assertion.
var _ services.MilestoneCommitAttacher = (*MilestoneAttacher)(nil)
