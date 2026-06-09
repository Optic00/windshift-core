package services

import (
	"context"
	"errors"
	"fmt"
	"os"

	"windshift/internal/repoprep"
)

// RepoEntry is one top-level entry in a prepared worktree's project root.
type RepoEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

// ErrNoPreparer is returned by InspectRepoRoot when the run service was built
// without a repoprep.Preparer — i.e. the coding-agent harness is disabled (no
// RunnerImage / WorktreeRoot configured), so no worktree can be materialized.
var ErrNoPreparer = errors.New("run service: no repo preparer configured")

// InspectRepoRoot prepares a throwaway worktree for spec exactly the way a real
// run does — reusing the same repoprep.Preparer that backs agent runs — lists
// up to max entries from its project root, then removes the checkout. It powers
// the agent-binding "test" button: a bare LLM prompt proves the model is
// reachable, but only an actual clone proves the SCM connection decrypts, the
// clone URL resolves, and the binding points at the right repository.
//
// The checkout uses a unique negative run id so its runs/<id> directory and
// agent-runs/run-<id> branch never collide with a real run or another
// concurrent test, and it is always cleaned up before returning. Nothing is
// pushed — this is read-only.
func (s *RunService) InspectRepoRoot(ctx context.Context, spec repoprep.RepoSpec, maxEntries int) ([]RepoEntry, error) {
	if s.preparer == nil {
		return nil, ErrNoPreparer
	}
	if maxEntries <= 0 {
		maxEntries = 5
	}

	runID := int(s.testCheckoutSeq.Add(-1))
	prepared, err := s.preparer.Prepare(ctx, spec, runID)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Background context: the checkout must be removed even if the request
		// context that drove Prepare has since been canceled.
		if cerr := s.preparer.Cleanup(context.Background(), prepared); cerr != nil {
			s.logger.Printf("run service: cleanup test checkout run=%d: %v", runID, cerr)
		}
	}()

	entries, err := os.ReadDir(prepared.Path)
	if err != nil {
		return nil, fmt.Errorf("read worktree root: %w", err)
	}
	out := make([]RepoEntry, 0, maxEntries)
	for _, e := range entries {
		if e.Name() == ".git" {
			continue // the clone's own git dir is plumbing, not project content
		}
		out = append(out, RepoEntry{Name: e.Name(), IsDir: e.IsDir()})
		if len(out) >= maxEntries {
			break
		}
	}
	return out, nil
}
