package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// RepoSpec identifies a source repository the orchestrator should prepare
// for a run. The combination (WorkspaceID, RepoSlug) is what scopes the
// bare-clone-per-(workspace,repo) cache; bindings (WI-88) surface these
// values from configuration. RemoteURL is the tokenless fetch target;
// Token (if non-empty) is injected via a per-clone GIT_ASKPASS helper
// so it never appears in argv or .git/config.
type RepoSpec struct {
	WorkspaceID int
	RepoSlug    string // e.g. "owner/name" — must not contain ".." or absolute paths
	RemoteURL   string // tokenless HTTPS URL
	BaseRef     string // default "main"
	Token       string // optional OAuth/PAT; injected via askpass, never embedded in URL
}

// PreparedWorktree is what WorktreeManager.Prepare returns. The Path field
// is the host directory the runner will bind-mount as /workspace; the
// Branch is the run-local branch the agent's commits land on; BaseCommit
// is the SHA the branch was created at, useful for diffing later.
type PreparedWorktree struct {
	Path       string
	Branch     string
	BaseCommit string

	// internal: kept so Cleanup can return the bare repo and the
	// worktree name without re-computing.
	bareDir string
	repoKey string
}

// WorktreeManager owns the on-disk worktree root and per-repo fetch/gc
// serialization. One bare clone per (workspace, repo); per-run worktrees
// branched off it. The Phase 1 walking skeleton stayed mount-less; this
// is what Phase 2 (WI-85) adds so the per-run container has actual repo
// content under /workspace.
type WorktreeManager struct {
	rootDir   string
	gitBinary string
	logger    *log.Logger

	mu        sync.Mutex
	repoLocks map[string]*sync.Mutex
}

// WorktreeManagerOptions controls construction. RootDir is the only
// required value; everything else is defaulted.
type WorktreeManagerOptions struct {
	RootDir   string
	GitBinary string
	Logger    *log.Logger
}

// NewWorktreeManager constructs a manager. RootDir must be writable; the
// constructor does not create it (let the operator deploy the layout) but
// will mkdir per-repo subdirs lazily as runs come in.
func NewWorktreeManager(opts WorktreeManagerOptions) (*WorktreeManager, error) {
	if opts.RootDir == "" {
		return nil, errors.New("worktree manager: RootDir is required")
	}
	if !filepath.IsAbs(opts.RootDir) {
		return nil, fmt.Errorf("worktree manager: RootDir must be absolute, got %q", opts.RootDir)
	}
	gitBin := opts.GitBinary
	if gitBin == "" {
		gitBin = "git"
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &WorktreeManager{
		rootDir:   opts.RootDir,
		gitBinary: gitBin,
		logger:    logger,
		repoLocks: make(map[string]*sync.Mutex),
	}, nil
}

func (m *WorktreeManager) lockFor(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.repoLocks[key]; ok {
		return l
	}
	l := &sync.Mutex{}
	m.repoLocks[key] = l
	return l
}

func validateRepoSlug(slug string) error {
	if slug == "" {
		return errors.New("repo slug is required")
	}
	if filepath.IsAbs(slug) {
		return fmt.Errorf("repo slug must be relative, got %q", slug)
	}
	clean := filepath.Clean(slug)
	if clean != slug || strings.Contains(slug, "..") {
		return fmt.Errorf("repo slug must not contain .. or trailing slashes, got %q", slug)
	}
	return nil
}

// Prepare ensures the (workspace,repo) bare clone exists, fetches the
// requested base ref, and creates a per-run worktree branched off it.
// Concurrent Prepares for the same repo serialize on a per-repo mutex so
// fetch / gc / worktree-add never race each other.
func (m *WorktreeManager) Prepare(ctx context.Context, spec RepoSpec, runID int) (*PreparedWorktree, error) {
	if spec.WorkspaceID == 0 {
		return nil, errors.New("worktree manager: WorkspaceID is required")
	}
	if err := validateRepoSlug(spec.RepoSlug); err != nil {
		return nil, fmt.Errorf("worktree manager: %w", err)
	}
	if spec.RemoteURL == "" {
		return nil, errors.New("worktree manager: RemoteURL is required")
	}
	baseRef := spec.BaseRef
	if baseRef == "" {
		baseRef = "main"
	}

	repoKey := fmt.Sprintf("%d:%s", spec.WorkspaceID, spec.RepoSlug)
	repoLock := m.lockFor(repoKey)
	repoLock.Lock()
	defer repoLock.Unlock()

	repoRoot := filepath.Join(m.rootDir, fmt.Sprintf("%d", spec.WorkspaceID), spec.RepoSlug)
	bareDir := filepath.Join(repoRoot, ".bare")
	if err := m.ensureBare(ctx, bareDir, spec.RemoteURL, spec.Token); err != nil {
		return nil, fmt.Errorf("ensure bare clone: %w", err)
	}
	if err := m.fetchRef(ctx, bareDir, baseRef, spec.Token); err != nil {
		return nil, fmt.Errorf("fetch %s: %w", baseRef, err)
	}

	baseCommit, err := m.revParse(ctx, bareDir, baseRef)
	if err != nil {
		return nil, fmt.Errorf("rev-parse %s: %w", baseRef, err)
	}

	wtPath := filepath.Join(repoRoot, "runs", fmt.Sprintf("%d", runID))
	branch := fmt.Sprintf("agent-runs/run-%d", runID)

	// `git worktree add -B` (capital B) reuses or recreates the branch
	// instead of erroring if it already exists. Defensive on retries.
	if err := m.runGit(ctx, bareDir, "worktree", "add", "-B", branch, wtPath, baseCommit); err != nil {
		return nil, fmt.Errorf("worktree add: %w", err)
	}

	return &PreparedWorktree{
		Path:       wtPath,
		Branch:     branch,
		BaseCommit: baseCommit,
		bareDir:    bareDir,
		repoKey:    repoKey,
	}, nil
}

// Cleanup removes the per-run worktree directory and its git registration.
// Best-effort: logs but doesn't fail if individual steps don't succeed,
// because a stale worktree is wasted disk, not data loss.
func (m *WorktreeManager) Cleanup(ctx context.Context, pw *PreparedWorktree) error {
	if pw == nil {
		return nil
	}
	repoLock := m.lockFor(pw.repoKey)
	repoLock.Lock()
	defer repoLock.Unlock()

	// `git worktree remove --force` handles a dirty tree. Fall back to a
	// raw rm + prune if the worktree registration is already broken.
	if err := m.runGit(ctx, pw.bareDir, "worktree", "remove", "--force", pw.Path); err != nil {
		m.logger.Printf("worktree manager: worktree remove %s: %v (falling back to rm + prune)", pw.Path, err)
		if rmErr := os.RemoveAll(pw.Path); rmErr != nil {
			m.logger.Printf("worktree manager: rm %s: %v", pw.Path, rmErr)
		}
		if pruneErr := m.runGit(ctx, pw.bareDir, "worktree", "prune"); pruneErr != nil {
			m.logger.Printf("worktree manager: worktree prune: %v", pruneErr)
		}
	}
	// Drop the run branch so a subsequent run with the same id (test
	// reruns mostly) gets a clean slate.
	if err := m.runGit(ctx, pw.bareDir, "branch", "-D", pw.Branch); err != nil {
		m.logger.Printf("worktree manager: branch -D %s: %v", pw.Branch, err)
	}
	return nil
}

func (m *WorktreeManager) ensureBare(ctx context.Context, bareDir, remoteURL, token string) error {
	if _, err := os.Stat(filepath.Join(bareDir, "HEAD")); err == nil {
		return nil // bare clone already initialized
	}
	// The per-(workspace,repo) tree lives under the orchestrator's
	// worktree root and is owned by the windshift process user only; 0750
	// keeps it readable by group members for ops introspection without
	// leaking to other workers.
	if err := os.MkdirAll(filepath.Dir(bareDir), 0o750); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	// Use --bare without a working tree. Run with `clone` so the remote
	// is configured automatically (we'd otherwise need `git init --bare`
	// + `git remote add origin` + `git fetch`).
	if err := m.runGitWithToken(ctx, "", token, "clone", "--bare", remoteURL, bareDir); err != nil {
		return fmt.Errorf("git clone --bare: %w", err)
	}
	// Disable auto-gc on the bare clone. Auto-gc under concurrent
	// worktree access has been a footgun in similar setups; we'll
	// schedule manual gc from a sweeper once no runs reference the repo.
	if err := m.runGit(ctx, bareDir, "config", "gc.auto", "0"); err != nil {
		return fmt.Errorf("config gc.auto 0: %w", err)
	}
	return nil
}

func (m *WorktreeManager) fetchRef(ctx context.Context, bareDir, ref, token string) error {
	// `+ref:ref` forces fast-forward; on bare repos this updates the
	// local ref to track the remote's tip.
	spec := fmt.Sprintf("+%s:%s", ref, ref)
	return m.runGitWithToken(ctx, bareDir, token, "fetch", "--prune", "origin", spec)
}

func (m *WorktreeManager) revParse(ctx context.Context, bareDir, ref string) (string, error) {
	out, err := m.runGitOutput(ctx, bareDir, "rev-parse", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (m *WorktreeManager) runGit(ctx context.Context, dir string, args ...string) error {
	_, err := m.runGitOutput(ctx, dir, args...)
	return err
}

// runGitWithToken runs git with the given token injected via a
// per-invocation GIT_ASKPASS helper. The token is passed to the helper
// via env var, never via argv, and never persisted to .git/config —
// git's credential.helper is not invoked at all because askpass
// answers the prompt directly. When token is empty the call behaves
// exactly like runGit (no askpass setup).
func (m *WorktreeManager) runGitWithToken(ctx context.Context, dir, token string, args ...string) error {
	if token == "" {
		return m.runGit(ctx, dir, args...)
	}
	dirPath, askpassPath, err := writeAskpassHelper()
	if err != nil {
		return fmt.Errorf("setup askpass: %w", err)
	}
	defer func() { _ = os.RemoveAll(dirPath) }()
	_, err = m.runGitOutputEnv(ctx, dir, []string{
		"GIT_ASKPASS=" + askpassPath,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"AGENT_GIT_TOKEN=" + token,
	}, args...)
	return err
}

func (m *WorktreeManager) runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	return m.runGitOutputEnv(ctx, dir, nil, args...)
}

func (m *WorktreeManager) runGitOutputEnv(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	// Prepend defense-in-depth git protocol restrictions to every
	// invocation. Even though the clone URL itself is now derived from
	// a trusted SCM connection record (WI-136), turning off ext::,
	// file://, and tar:// remote helpers at the git level removes a
	// whole class of injection paths if any future caller accidentally
	// hands worktree_manager a URL it shouldn't.
	prefixed := append([]string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=never",
		"-c", "protocol.tar.allow=never",
	}, args...)
	// All values reaching args are operator-controlled or
	// orchestrator-derived; there is no user-supplied data in scope.
	cmd := exec.CommandContext(ctx, m.gitBinary, prefixed...) //nolint:gosec // G204: see comment above.
	if dir != "" {
		cmd.Dir = dir
	}
	// GIT_ALLOW_PROTOCOL bounds the protocols git itself will dial out
	// over (defense-in-depth alongside the protocol.*.allow config).
	cmd.Env = append(cmd.Environ(), "GIT_ALLOW_PROTOCOL=https")
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Run the error string through RedactString so an embedded
		// credential (in a remote URL, a git error message, a stray
		// env echo) never reaches the caller — which writes it to
		// agent_runs.error and the SSE event stream.
		joined := strings.Join(args, " ")
		return RedactString(string(out)), fmt.Errorf("git %s: %w (out=%q)", joined, err, RedactString(strings.TrimSpace(string(out))))
	}
	return string(out), nil
}

// writeAskpassHelper creates a private (0700) directory plus a shell
// script that answers git's prompts from the AGENT_GIT_TOKEN env var.
// Returns (dirPath, scriptPath, err); the caller is responsible for
// removing dirPath after the git invocation completes.
//
// Script body:
//
//	case "$1" in
//	  Username*) printf 'oauth2\n' ;;
//	  Password*) printf '%s\n' "$AGENT_GIT_TOKEN" ;;
//	esac
//
// The username "oauth2" works for both GitHub and Gitea — both accept
// any non-empty username when paired with a token in the password slot.
func writeAskpassHelper() (dirPath, scriptPath string, err error) {
	dirPath, err = os.MkdirTemp("", "windshift-askpass-*")
	if err != nil {
		return "", "", err
	}
	// 0700 (rwx for owner) is the minimum that allows the windshift
	// process to write the script into the directory and remove it
	// afterwards. gosec G302 wants 0600 but a directory without +x
	// can't be traversed.
	if err = os.Chmod(dirPath, 0o700); err != nil { //nolint:gosec // G302: see comment above
		_ = os.RemoveAll(dirPath)
		return "", "", err
	}
	scriptPath = filepath.Join(dirPath, "askpass.sh")
	body := "#!/bin/sh\ncase \"$1\" in\n  Username*) printf 'oauth2\\n' ;;\n  Password*) printf '%s\\n' \"$AGENT_GIT_TOKEN\" ;;\nesac\n"
	// 0700 because git executes the helper via GIT_ASKPASS; the
	// executable bit is required. gosec G306 wants 0600 but execute
	// is intentional here.
	if err = os.WriteFile(scriptPath, []byte(body), 0o700); err != nil { //nolint:gosec // G306: see comment above
		_ = os.RemoveAll(dirPath)
		return "", "", err
	}
	return dirPath, scriptPath, nil
}
