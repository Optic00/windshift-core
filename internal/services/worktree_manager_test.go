package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// seedOriginRepo creates a non-bare git repo on disk with one commit on
// the given branch and returns its path; the path is what we hand to
// RepoSpec.RemoteURL so the WorktreeManager can clone --bare from it.
func seedOriginRepo(t *testing.T, branch string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	// Use a sub-path so the temp dir cleanup doesn't trip over the
	// orientation of the bare clone we'll create elsewhere.
	repo := filepath.Join(dir, "origin")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir origin: %v", err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v (out=%s)", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--initial-branch="+branch)
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial")
	return repo
}

func newTestWorktreeManager(t *testing.T) *WorktreeManager {
	t.Helper()
	mgr, err := NewWorktreeManager(WorktreeManagerOptions{
		RootDir: t.TempDir(),
		Logger:  silentLogger(t),
	})
	if err != nil {
		t.Fatalf("new worktree manager: %v", err)
	}
	return mgr
}

func TestWorktreeManager_PrepareCreatesBareAndWorktree(t *testing.T) {
	ctx := context.Background()
	origin := seedOriginRepo(t, "main")
	mgr := newTestWorktreeManager(t)

	pw, err := mgr.Prepare(ctx, RepoSpec{
		WorkspaceID: 1,
		RepoSlug:    "acme/widget",
		RemoteURL:   origin,
		BaseRef:     "main",
	}, 42)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer func() { _ = mgr.Cleanup(ctx, pw) }()

	// Bare clone lives one level above the worktree's runs/ tree.
	expectedBare := filepath.Join(mgr.rootDir, "1", "acme/widget", ".bare")
	if _, err := os.Stat(filepath.Join(expectedBare, "HEAD")); err != nil {
		t.Errorf("bare clone missing HEAD at %s: %v", expectedBare, err)
	}
	expectedWT := filepath.Join(mgr.rootDir, "1", "acme/widget", "runs", "42")
	if pw.Path != expectedWT {
		t.Errorf("worktree path: want %s, got %s", expectedWT, pw.Path)
	}
	if _, err := os.Stat(filepath.Join(pw.Path, "README.md")); err != nil {
		t.Errorf("worktree must contain README.md: %v", err)
	}
	if pw.Branch != "agent-runs/run-42" {
		t.Errorf("branch: want agent-runs/run-42, got %s", pw.Branch)
	}
	if len(pw.BaseCommit) < 40 {
		t.Errorf("BaseCommit must be a full SHA, got %q", pw.BaseCommit)
	}

	// gc.auto must be disabled on the bare clone.
	cmd := exec.Command("git", "config", "--get", "gc.auto")
	cmd.Dir = expectedBare
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("read gc.auto: %v (out=%s)", err, out)
	}
	if strings.TrimSpace(string(out)) != "0" {
		t.Errorf("gc.auto: want 0, got %q", string(out))
	}
}

func TestWorktreeManager_CleanupRemovesWorktreeAndBranch(t *testing.T) {
	ctx := context.Background()
	origin := seedOriginRepo(t, "main")
	mgr := newTestWorktreeManager(t)

	pw, err := mgr.Prepare(ctx, RepoSpec{
		WorkspaceID: 1,
		RepoSlug:    "acme/widget",
		RemoteURL:   origin,
		BaseRef:     "main",
	}, 7)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := mgr.Cleanup(ctx, pw); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(pw.Path); !os.IsNotExist(err) {
		t.Errorf("worktree dir must be gone after cleanup, stat err=%v", err)
	}
	// Branch should be gone too (best-effort).
	cmd := exec.Command("git", "branch", "--list", pw.Branch)
	cmd.Dir = pw.bareDir
	out, _ := cmd.CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("branch %s still present after cleanup: %s", pw.Branch, out)
	}
}

func TestWorktreeManager_ConcurrentPrepareSameRepoSerializes(t *testing.T) {
	ctx := context.Background()
	origin := seedOriginRepo(t, "main")
	mgr := newTestWorktreeManager(t)

	const N = 5
	results := make(chan *PreparedWorktree, N)
	errs := make(chan error, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(runID int) {
			defer wg.Done()
			pw, err := mgr.Prepare(ctx, RepoSpec{
				WorkspaceID: 1,
				RepoSlug:    "acme/widget",
				RemoteURL:   origin,
				BaseRef:     "main",
			}, runID)
			if err != nil {
				errs <- err
				return
			}
			results <- pw
		}(100 + i)
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Errorf("concurrent prepare: %v", err)
	}

	seen := make(map[string]bool)
	for pw := range results {
		if seen[pw.Path] {
			t.Errorf("duplicate worktree path: %s", pw.Path)
		}
		seen[pw.Path] = true
		_ = mgr.Cleanup(ctx, pw)
	}
	if len(seen) != N {
		t.Errorf("want %d distinct worktrees, got %d", N, len(seen))
	}
}

func TestWorktreeManager_RejectsBadSlug(t *testing.T) {
	ctx := context.Background()
	mgr := newTestWorktreeManager(t)
	cases := []string{"../escape", "/abs/path", "trailing/", ""}
	for _, slug := range cases {
		t.Run(slug, func(t *testing.T) {
			_, err := mgr.Prepare(ctx, RepoSpec{
				WorkspaceID: 1,
				RepoSlug:    slug,
				RemoteURL:   "ignored",
			}, 1)
			if err == nil {
				t.Fatalf("expected error for slug %q, got nil", slug)
			}
		})
	}
}
