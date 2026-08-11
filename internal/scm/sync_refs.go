package scm

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"windshift/internal/models"
)

// refShort normalizes canonical v<digit> tags and release/ branches for
// shared upsert keys; unusual refs pass through unchanged.
func refShort(refType, refName string) string {
	switch refType {
	case "tag":
		if len(refName) > 1 && (refName[0] == 'v' || refName[0] == 'V') && refName[1] >= '0' && refName[1] <= '9' {
			return refName[1:]
		}
		return refName
	case "branch":
		if strings.HasPrefix(refName, "release/") {
			return strings.TrimPrefix(refName, "release/")
		}
		return refName
	default:
		return refName
	}
}

// matchGlob treats empty or malformed patterns as no match.
func matchGlob(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	ok, err := filepath.Match(pattern, name)
	if err != nil {
		// Malformed patterns must not fire on a ref.
		return false
	}
	return ok
}

// loadMilestonePatterns reads the per-repo tag + branch glob from
// workspace_repositories. Missing columns (older installs that haven't
// hit the migration yet) yield the schema defaults.
func (s *SyncService) loadMilestonePatterns(ctx context.Context, repoID int) (tagPattern, branchPattern string, err error) {
	var tagNS, branchNS sql.NullString
	if err = s.db.QueryRowContext(ctx, `
		SELECT milestone_tag_pattern, milestone_branch_pattern
		FROM workspace_repositories
		WHERE id = ?
	`, repoID).Scan(&tagNS, &branchNS); err != nil {
		return "v*", "release/*", fmt.Errorf("load milestone patterns: %w", err)
	}
	tagPattern = "v*"
	if tagNS.Valid && tagNS.String != "" {
		tagPattern = tagNS.String
	}
	branchPattern = "release/*"
	if branchNS.Valid && branchNS.String != "" {
		branchPattern = branchNS.String
	}
	return tagPattern, branchPattern, nil
}

// isRefProcessed reports whether (repoID, refType, refName) is already
// in scm_processed_refs. A row there means the corresponding ActionEvent
// has been queued at least once; the action engine itself owns idempotency
// for downstream effects via the per-action upsert_key.
func (s *SyncService) isRefProcessed(ctx context.Context, repoID int, refType, refName string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM scm_processed_refs
		WHERE workspace_repository_id = ? AND ref_type = ? AND ref_name = ?
	`, repoID, refType, refName).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// markRefProcessed inserts the idempotency row. Caller invokes this only
// after a successful event emission so a failure leaves the ref eligible
// for retry on the next tick.
func (s *SyncService) markRefProcessed(ctx context.Context, repoID int, refType, refName, sha string) error {
	_, err := s.db.ExecWriteContext(ctx, `
		INSERT INTO scm_processed_refs(workspace_repository_id, ref_type, ref_name, sha, processed_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, repoID, refType, refName, sha)
	return err
}

// syncTagsAndReleases emits new matching tags with chronological prev_tag; providers without tag support no-op.
func (s *SyncService) syncTagsAndReleases(ctx context.Context, provider Provider, owner, repo string, repoID, workspaceID int) error {
	rp, ok := provider.(RefProvider)
	if !ok {
		return nil
	}
	tagPattern, _, err := s.loadMilestonePatterns(ctx, repoID)
	if err != nil {
		return err
	}

	tags, err := rp.ListTags(ctx, owner, repo, time.Time{})
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}

	// Keep only matching tags; preserve their chronological order so the
	// "previous tag" lookup is consistent. Tags from providers can come
	// back in any order; sort by CreatedAt ascending so prev = i-1.
	matched := make([]Tag, 0, len(tags))
	for _, t := range tags {
		if matchGlob(tagPattern, t.Name) {
			matched = append(matched, t)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].CreatedAt.Before(matched[j].CreatedAt)
	})

	for i, t := range matched {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen, err := s.isRefProcessed(ctx, repoID, "tag", t.Name)
		if err != nil {
			slog.Error("Failed to check processed ref", slog.String("component", "scm"), slog.Any("error", err))
			continue
		}
		if seen {
			continue
		}
		var prev string
		if i > 0 {
			prev = matched[i-1].Name
		}
		s.emitTagEvent(workspaceID, repoID, owner, repo, t, prev)
		if err := s.markRefProcessed(ctx, repoID, "tag", t.Name, t.SHA); err != nil {
			slog.Error("Failed to mark tag processed", slog.String("component", "scm"), slog.String("tag", t.Name), slog.Any("error", err))
		}
	}
	return nil
}

// syncReleaseBranches lists branches matching the repo's branch pattern
// and emits an ActionEvent per newly observed one. Branches don't carry
// a usable creation timestamp from most providers, so there's no
// "prev_branch" concept — the create_milestone action's upsert_key is
// what pairs the branch with its later tag.
func (s *SyncService) syncReleaseBranches(ctx context.Context, provider Provider, owner, repo string, repoID, workspaceID int) error {
	_, branchPattern, err := s.loadMilestonePatterns(ctx, repoID)
	if err != nil {
		return err
	}
	branches, err := provider.ListBranches(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("list branches: %w", err)
	}
	for _, b := range branches {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !matchGlob(branchPattern, b.Name) {
			continue
		}
		seen, err := s.isRefProcessed(ctx, repoID, "branch", b.Name)
		if err != nil {
			slog.Error("Failed to check processed ref", slog.String("component", "scm"), slog.Any("error", err))
			continue
		}
		if seen {
			continue
		}
		s.emitBranchEvent(workspaceID, repoID, owner, repo, b)
		if err := s.markRefProcessed(ctx, repoID, "branch", b.Name, b.SHA); err != nil {
			slog.Error("Failed to mark branch processed", slog.String("component", "scm"), slog.String("branch", b.Name), slog.Any("error", err))
		}
	}
	return nil
}

// emitTagEvent constructs and dispatches an ActionEvent for a newly
// observed tag. The payload lives entirely in NewValues so the variable
// substitution layer (`{{ref.short}}` etc.) can read it without a new
// struct field on ActionEvent.
func (s *SyncService) emitTagEvent(workspaceID, repoID int, owner, repo string, t Tag, prevTagName string) {
	if s.actionEvents == nil {
		return
	}
	values := map[string]any{
		"ref.name":                     t.Name,
		"ref.short":                    refShort("tag", t.Name),
		"ref.sha":                      t.SHA,
		"ref.type":                     "tag",
		"ref.prev_name":                prevTagName,
		"repo.owner":                   owner,
		"repo.name":                    repo,
		"repo.full_name":               fmt.Sprintf("%s/%s", owner, repo),
		"repo.workspace_repository_id": repoID,
	}
	s.actionEvents.EmitActionEvent(&models.ActionEvent{
		EventType:   models.ActionTriggerSCMTagCreated,
		WorkspaceID: workspaceID,
		NewValues:   values,
	})
}

// emitBranchEvent: same as emitTagEvent but for the release-branch case.
// No prev_branch; the action's upsert_key is the pairing mechanism.
func (s *SyncService) emitBranchEvent(workspaceID, repoID int, owner, repo string, b Branch) {
	if s.actionEvents == nil {
		return
	}
	values := map[string]any{
		"ref.name":                     b.Name,
		"ref.short":                    refShort("branch", b.Name),
		"ref.sha":                      b.SHA,
		"ref.type":                     "branch",
		"repo.owner":                   owner,
		"repo.name":                    repo,
		"repo.full_name":               fmt.Sprintf("%s/%s", owner, repo),
		"repo.workspace_repository_id": repoID,
	}
	s.actionEvents.EmitActionEvent(&models.ActionEvent{
		EventType:   models.ActionTriggerSCMReleaseBranchCreated,
		WorkspaceID: workspaceID,
		NewValues:   values,
	})
}
