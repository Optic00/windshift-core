// Package repoprep prepares a per-run source checkout for the coding-agent
// runner and pushes the run branch back. It is the single repo-preparation
// component both the local in-process worker and the standalone triage binary
// invoke, so local and remote prepare repos identically (WI-205 / page 35).
//
// The isolation primitive is a per-run CLONE WITH ITS OWN OBJECT STORE, not a
// git worktree sharing the cache's objects. The host-local bare clone per
// (workspace, repo) is only a fetch accelerator the preparer clones from — it
// is never bind-mounted or aliased (no alternates, no hardlinks) into a
// container. A compromised agent can therefore corrupt only its own throwaway
// checkout, and in-container git still works because the run owns its objects.
package repoprep

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"windshift/internal/redact"
)

// RepoSpec identifies a source repository to prepare. (WorkspaceID, RepoSlug)
// scopes the bare-clone cache; bindings surface these from configuration.
// RemoteURL is the tokenless fetch/push target; Token, when set, is injected
// via a per-invocation GIT_ASKPASS helper so it never lands in argv or
// .git/config.
type RepoSpec struct {
	WorkspaceID int
	RepoSlug    string // "owner/name" — must not contain ".." or be absolute
	RemoteURL   string // tokenless HTTPS URL
	BaseRef     string // default "main"
	Token       string // optional OAuth/PAT; askpass-injected, never embedded
}

// Prepared is the result of Prepare. Path is the host directory the runner
// bind-mounts as /workspace — a full, self-contained git repo. Branch is the
// run-local branch the agent commits on; BaseCommit is the SHA it was cut at.
type Prepared struct {
	Path       string
	Branch     string
	BaseCommit string
	RemoteURL  string

	// internal: retained so Push/Cleanup need not recompute.
	cacheDir string
	repoKey  string
}

// Preparer owns the on-disk root and per-repo fetch serialization. One bare
// cache per (workspace, repo); one independent clone per run beneath it.
type Preparer struct {
	rootDir      string
	gitBinary    string
	logger       *log.Logger
	allowFileURL bool

	mu        sync.Mutex
	repoLocks map[string]*sync.Mutex
}

// Options controls construction. RootDir (absolute, writable) is required.
type Options struct {
	RootDir   string
	GitBinary string
	Logger    *log.Logger
	// AllowFileURL relaxes the production ban on file:// (and bare local
	// path) remotes. Only unit tests set it, to seed an on-disk origin
	// instead of an HTTPS server. Production never does.
	AllowFileURL bool
}

// New constructs a Preparer. RootDir is not created up front (the operator
// deploys the layout) but per-repo subdirs are made lazily as runs arrive.
func New(opts Options) (*Preparer, error) {
	if opts.RootDir == "" {
		return nil, errors.New("repoprep: RootDir is required")
	}
	if !filepath.IsAbs(opts.RootDir) {
		return nil, fmt.Errorf("repoprep: RootDir must be absolute, got %q", opts.RootDir)
	}
	gitBin := opts.GitBinary
	if gitBin == "" {
		gitBin = "git"
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	return &Preparer{
		rootDir:      opts.RootDir,
		gitBinary:    gitBin,
		logger:       logger,
		allowFileURL: opts.AllowFileURL,
		repoLocks:    make(map[string]*sync.Mutex),
	}, nil
}

func (p *Preparer) lockFor(key string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.repoLocks[key]; ok {
		return l
	}
	l := &sync.Mutex{}
	p.repoLocks[key] = l
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

// Prepare ensures the (workspace,repo) bare cache exists, fetches the base ref
// into it, then clones the cache into a per-run checkout with COPIED objects
// (no hardlinks, no alternates) and checks out the run branch at the base
// commit. Concurrent Prepares for the same repo serialize on a per-repo mutex
// so fetch and clone never race. The returned checkout's origin is reset to the
// real (tokenless) RemoteURL so it looks like a normal clone and Push targets
// the real remote, not the cache.
func (p *Preparer) Prepare(ctx context.Context, spec RepoSpec, runID int) (*Prepared, error) {
	if spec.WorkspaceID == 0 {
		return nil, errors.New("repoprep: WorkspaceID is required")
	}
	if err := validateRepoSlug(spec.RepoSlug); err != nil {
		return nil, fmt.Errorf("repoprep: %w", err)
	}
	if spec.RemoteURL == "" {
		return nil, errors.New("repoprep: RemoteURL is required")
	}
	baseRef := spec.BaseRef
	if baseRef == "" {
		baseRef = "main"
	}

	repoKey := fmt.Sprintf("%d:%s", spec.WorkspaceID, spec.RepoSlug)
	repoLock := p.lockFor(repoKey)
	repoLock.Lock()
	defer repoLock.Unlock()

	repoRoot := filepath.Join(p.rootDir, fmt.Sprintf("%d", spec.WorkspaceID), spec.RepoSlug)
	cacheDir := filepath.Join(repoRoot, ".bare")
	if err := p.ensureBare(ctx, cacheDir, spec.RemoteURL, spec.Token); err != nil {
		return nil, fmt.Errorf("ensure bare cache: %w", err)
	}
	if err := p.fetchRef(ctx, cacheDir, baseRef, spec.Token); err != nil {
		return nil, fmt.Errorf("fetch %s: %w", baseRef, err)
	}
	baseCommit, err := p.revParse(ctx, cacheDir, baseRef)
	if err != nil {
		return nil, fmt.Errorf("rev-parse %s: %w", baseRef, err)
	}

	dest := filepath.Join(repoRoot, "runs", fmt.Sprintf("%d", runID))
	branch := fmt.Sprintf("agent-runs/run-%d", runID)

	// Retry safety: a previous attempt may have left a partial checkout.
	if err := os.RemoveAll(dest); err != nil {
		return nil, fmt.Errorf("clear stale checkout: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return nil, fmt.Errorf("mkdir runs dir: %w", err)
	}

	// --no-hardlinks forces git to COPY objects out of the cache rather than
	// hardlink them, so the run owns an independent object store. A local
	// clone never uses alternates (only --shared would), so nothing in dest
	// references the cache after this returns.
	//
	// The source is the trusted host-local bare cache, so this clone uses git's
	// "file" transport — which the default https-only hardening blocks. Permit
	// file for this one local-source clone (ext/tar stay disabled; the remote
	// clone/fetch above still run https-only), mirroring PushBranch's local
	// fetch from the per-run checkout.
	if err := p.runGitLocalSource(ctx, "", "clone", "--no-hardlinks", cacheDir, dest); err != nil {
		return nil, fmt.Errorf("clone cache -> checkout: %w", err)
	}
	// Cut the run branch at the fetched base commit.
	if err := p.runGit(ctx, dest, "checkout", "-B", branch, baseCommit); err != nil {
		return nil, fmt.Errorf("checkout run branch: %w", err)
	}
	// Point origin at the real remote (tokenless) so the checkout looks
	// normal and Push targets it, not the host-local cache.
	if err := p.runGit(ctx, dest, "remote", "set-url", "origin", spec.RemoteURL); err != nil {
		return nil, fmt.Errorf("reset origin url: %w", err)
	}

	return &Prepared{
		Path:       dest,
		Branch:     branch,
		BaseCommit: baseCommit,
		RemoteURL:  spec.RemoteURL,
		cacheDir:   cacheDir,
		repoKey:    repoKey,
	}, nil
}

// Push pushes the run branch from the per-run checkout to origin (the real
// remote). The token is injected via askpass; an empty token assumes ambient
// credentials (or, later, a git-proxy that needs none). It pushes exactly the
// single run branch — never anything else. It delegates to PushBranch so the
// in-process and separate-process (triage binary) push paths are identical.
func (p *Preparer) Push(ctx context.Context, pr *Prepared, token string) error {
	if pr == nil {
		return errors.New("repoprep: nil prepared checkout")
	}
	_, err := PushBranch(ctx, PushOptions{
		Dest:         pr.Path,
		Branch:       pr.Branch,
		RemoteURL:    pr.RemoteURL,
		Token:        token,
		GitBinary:    p.gitBinary,
		AllowFileURL: p.allowFileURL,
		// A run in which the agent committed nothing has nothing worth
		// delivering: skip the push instead of littering the remote with a
		// branch identical to base (and a doomed PR-create call after it).
		SkipIfHeadEquals: pr.BaseCommit,
		TempRoot:         p.tempRoot(),
	})
	return err
}

// ErrNoNewCommits is returned by PushBranch when the run branch's head still
// equals SkipIfHeadEquals — the agent finished without committing (e.g. it
// answered a question via a work-item comment instead of changing code), so
// there is nothing to deliver: no branch on the remote, no PR.
var ErrNoNewCommits = errors.New("repoprep: branch head equals base — no new commits to push")

// PushOptions configures a stateless push of a single branch from an existing
// checkout — what the triage binary's `push` subcommand needs, since prepare
// and push run as separate processes that share no in-memory state.
type PushOptions struct {
	Dest         string // the per-run checkout directory
	Branch       string // run branch to push (e.g. agent-runs/run-123)
	RemoteURL    string // optional: rewrite origin before pushing (proxy transport)
	Token        string // optional: askpass token
	GitBinary    string // default "git"
	AllowFileURL bool
	// SkipIfHeadEquals, when set, short-circuits with ErrNoNewCommits if the
	// branch head resolves to this SHA (the base commit the run branch was
	// cut at): a commit-less run pushes nothing.
	SkipIfHeadEquals string
	// TempRoot, when set, is the preferred location for the sanitized push
	// repo and the askpass dir, with the system temp dir as fallback — in a
	// scratch deploy /tmp is absent or mounted noexec, and the askpass helper
	// must be executable. The in-process Preparer sets it to a subdir of its
	// worktree root; the triage binary leaves it empty because the runner
	// container always mounts an exec-capable /tmp tmpfs.
	TempRoot string
}

// PushBranch pushes exactly Branch from Dest to RemoteURL and returns the
// pushed head SHA. The push itself is performed from a fresh host-owned
// temporary repository, never from the agent-mutated checkout. That keeps
// agent-written .git/config (url.*.insteadOf, remotes, credential helpers,
// includeIf, fsmonitor, hooks, etc.) out of the credentialed push path; the
// only target used is the server-derived RemoteURL supplied by the caller.
func PushBranch(ctx context.Context, opts PushOptions) (string, error) {
	if opts.Dest == "" || opts.Branch == "" {
		return "", errors.New("repoprep: Dest and Branch are required")
	}
	if opts.RemoteURL == "" {
		return "", errors.New("repoprep: RemoteURL is required for sanitized push")
	}
	gitBin := opts.GitBinary
	if gitBin == "" {
		gitBin = "git"
	}
	destAbs, err := filepath.Abs(opts.Dest)
	if err != nil {
		return "", fmt.Errorf("abs dest: %w", err)
	}
	shaOut, err := gitOutputEnv(ctx, gitBin, opts.AllowFileURL, destAbs, nil, "rev-parse", "refs/heads/"+opts.Branch+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", opts.Branch, err)
	}
	sha := strings.TrimSpace(shaOut)
	if opts.SkipIfHeadEquals != "" && sha == opts.SkipIfHeadEquals {
		return "", ErrNoNewCommits
	}

	tmp, err := mkdirTempPreferring(opts.TempRoot, "windshift-sanitized-push-*")
	if err != nil {
		return "", fmt.Errorf("temp push repo: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if _, err := gitOutputEnv(ctx, gitBin, opts.AllowFileURL, tmp, nil, "init", "."); err != nil {
		return "", fmt.Errorf("init sanitized push repo: %w", err)
	}
	fetchSpec := fmt.Sprintf("+refs/heads/%s:refs/heads/%s", opts.Branch, opts.Branch)
	if _, err := gitOutputEnv(ctx, gitBin, true, tmp, nil, "fetch", "--no-tags", "--no-recurse-submodules", destAbs, fetchSpec); err != nil {
		return "", fmt.Errorf("fetch branch into sanitized push repo: %w", err)
	}
	fetchedOut, err := gitOutputEnv(ctx, gitBin, opts.AllowFileURL, tmp, nil, "rev-parse", "refs/heads/"+opts.Branch+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("rev-parse sanitized %s: %w", opts.Branch, err)
	}
	if fetched := strings.TrimSpace(fetchedOut); fetched != sha {
		return "", fmt.Errorf("sanitized push repo fetched %s, expected %s", fetched, sha)
	}

	refspec := fmt.Sprintf("%s:refs/heads/%s", sha, opts.Branch)
	if err := gitWithToken(ctx, gitBin, opts.AllowFileURL, tmp, opts.Token, opts.TempRoot, "push", opts.RemoteURL, refspec); err != nil {
		return "", fmt.Errorf("push %s: %w", opts.Branch, err)
	}
	return sha, nil
}

// Cleanup removes the per-run checkout. Best-effort: a stale checkout is wasted
// disk, not data loss. Unlike a worktree there is no git registration to
// unwind — the clone is self-contained — so this is a plain recursive remove.
func (p *Preparer) Cleanup(_ context.Context, pr *Prepared) error {
	if pr == nil {
		return nil
	}
	repoLock := p.lockFor(pr.repoKey)
	repoLock.Lock()
	defer repoLock.Unlock()
	if err := os.RemoveAll(pr.Path); err != nil {
		p.logger.Printf("repoprep: cleanup %s: %v", pr.Path, err)
	}
	return nil
}

// EvictIdle removes cached bare clones (and their repo trees) that have no
// active per-run checkout and whose last fetch is older than maxAge — the
// disk-hygiene backstop for the cache. Eviction takes the per-repo lock so it
// never races a Prepare. Returns the number evicted.
func (p *Preparer) EvictIdle(maxAge time.Duration, now time.Time) (int, error) {
	if p.rootDir == "" {
		return 0, nil
	}
	type bareEntry struct{ repoRoot, repoKey, barePath string }
	var found []bareEntry
	walkErr := filepath.WalkDir(p.rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil //nolint:nilerr // skip unreadable entries and keep walking
		}
		if !d.IsDir() || d.Name() != ".bare" {
			return nil
		}
		repoRoot := filepath.Dir(path)
		rel, rerr := filepath.Rel(p.rootDir, repoRoot)
		if rerr != nil {
			return fs.SkipDir
		}
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) == 2 {
			found = append(found, bareEntry{repoRoot: repoRoot, repoKey: parts[0] + ":" + parts[1], barePath: path})
		}
		return fs.SkipDir // don't descend into .bare internals
	})

	evicted := 0
	for _, e := range found {
		lock := p.lockFor(e.repoKey)
		lock.Lock()
		if entries, _ := os.ReadDir(filepath.Join(e.repoRoot, "runs")); len(entries) == 0 {
			info, serr := os.Stat(filepath.Join(e.barePath, "FETCH_HEAD"))
			if serr != nil {
				info, serr = os.Stat(e.barePath)
			}
			if serr == nil && now.Sub(info.ModTime()) > maxAge {
				if rmErr := os.RemoveAll(e.repoRoot); rmErr != nil {
					p.logger.Printf("repoprep: evict %s: %v", e.repoRoot, rmErr)
				} else {
					evicted++
				}
			}
		}
		lock.Unlock()
	}
	return evicted, walkErr
}

func (p *Preparer) ensureBare(ctx context.Context, cacheDir, remoteURL, token string) error {
	if _, err := os.Stat(filepath.Join(cacheDir, "HEAD")); err == nil {
		return nil // cache already initialized
	}
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0o750); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	if err := p.runGitWithToken(ctx, "", token, "clone", "--bare", remoteURL, cacheDir); err != nil {
		return fmt.Errorf("git clone --bare: %w", err)
	}
	// Disable auto-gc; manual gc is scheduled when no runs reference the repo.
	if err := p.runGit(ctx, cacheDir, "config", "gc.auto", "0"); err != nil {
		return fmt.Errorf("config gc.auto 0: %w", err)
	}
	return nil
}

func (p *Preparer) fetchRef(ctx context.Context, cacheDir, ref, token string) error {
	spec := fmt.Sprintf("+%s:%s", ref, ref)
	return p.runGitWithToken(ctx, cacheDir, token, "fetch", "--prune", "origin", spec)
}

func (p *Preparer) revParse(ctx context.Context, dir, ref string) (string, error) {
	out, err := p.runGitOutput(ctx, dir, "rev-parse", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (p *Preparer) runGit(ctx context.Context, dir string, args ...string) error {
	_, err := gitOutputEnv(ctx, p.gitBinary, p.allowFileURL, dir, nil, args...)
	return err
}

// runGitLocalSource runs git for an operation whose source is a trusted
// host-local path (the per-run clone of the bare cache), permitting the "file"
// transport that the default https-only hardening blocks. Used only where the
// source path is orchestrator-derived — never a caller- or remote-supplied URL.
func (p *Preparer) runGitLocalSource(ctx context.Context, dir string, args ...string) error {
	_, err := gitOutputEnv(ctx, p.gitBinary, true, dir, nil, args...)
	return err
}

func (p *Preparer) runGitWithToken(ctx context.Context, dir, token string, args ...string) error {
	return gitWithToken(ctx, p.gitBinary, p.allowFileURL, dir, token, p.tempRoot(), args...)
}

// tempRoot is the preferred location for the orchestrator's git scratch dirs
// (askpass helper, sanitized push repo): the worktree root is a writable,
// exec-capable volume, while a scratch deploy's /tmp is absent or noexec. It
// sits beside the per-repo trees; ".tmp" is never matched by EvictIdle's
// ".bare" walk, and each temp dir inside it is removed by its creator after
// use.
func (p *Preparer) tempRoot() string {
	return filepath.Join(p.rootDir, ".tmp")
}

func (p *Preparer) runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	return gitOutputEnv(ctx, p.gitBinary, p.allowFileURL, dir, nil, args...)
}

// --- package-level git plumbing (shared by Preparer and PushBranch so the
// in-process and triage-binary paths run byte-identical git) ---

// gitWithToken runs git with token injected via a per-invocation GIT_ASKPASS
// helper. The token reaches the helper through an env var only — never argv,
// never .git/config. An empty token behaves exactly like a plain run.
// tempRoot seeds mkdirTempPreferring for the askpass dir.
func gitWithToken(ctx context.Context, gitBinary string, allowFileURL bool, dir, token, tempRoot string, args ...string) error {
	if token == "" {
		_, err := gitOutputEnv(ctx, gitBinary, allowFileURL, dir, nil, args...)
		return err
	}
	dirPath, askpassPath, err := writeAskpassHelper(tempRoot)
	if err != nil {
		return fmt.Errorf("setup askpass: %w", err)
	}
	defer func() { _ = os.RemoveAll(dirPath) }()
	// GIT_TERMINAL_PROMPT / GIT_CONFIG_NOSYSTEM and the config-hardening -c
	// flags are applied centrally by gitOutputEnv; here we add only the
	// per-invocation askpass that feeds the token to git over an env var.
	_, err = gitOutputEnv(ctx, gitBinary, allowFileURL, dir, []string{
		"GIT_ASKPASS=" + askpassPath,
		"AGENT_GIT_TOKEN=" + token,
	}, args...)
	return err
}

func gitOutputEnv(ctx context.Context, gitBinary string, allowFileURL bool, dir string, extraEnv []string, args ...string) (string, error) {
	// Defense-in-depth: disable ext::/file://(unless allowed)/tar:// remote
	// helpers on every invocation so a future caller can't smuggle in a URL
	// that reaches a dangerous transport.
	fileAllow := "never"
	allowedProtocols := "https"
	if allowFileURL {
		fileAllow = "always"
		allowedProtocols = "https:file"
	}
	prefixed := append([]string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=" + fileAllow,
		"-c", "protocol.tar.allow=never",
		// Neutralize agent-controllable repo-local config that can execute code
		// or hijack credentials when the host runs git inside the agent-mutated
		// checkout (security Phase 1, WI-238). Command-line `-c` has the highest
		// config precedence, so these override any value the agent wrote into
		// the checkout's .git/config:
		//   - core.hooksPath=/dev/null  → agent-written .git/hooks/* never run
		//     (pre-push, post-checkout, etc.).
		//   - credential.helper=        → reset the helper list to empty so no
		//     agent-named helper binary is executed (askpass still answers auth).
		//   - core.fsmonitor=           → no agent-named fsmonitor hook process.
		"-c", "core.hooksPath=/dev/null",
		"-c", "credential.helper=",
		"-c", "core.fsmonitor=",
	}, args...)
	// All args are operator-controlled or orchestrator-derived; no
	// user-supplied data is in scope.
	cmd := exec.CommandContext(ctx, gitBinary, prefixed...) //nolint:gosec // G204: see comment above.
	if dir != "" {
		cmd.Dir = dir
	}
	// Isolate from inherited and agent-mutated git config: ignore the system
	// config (NOSYSTEM) and pin the global config to /dev/null so neither the
	// host user's ~/.gitconfig nor an injected global is read. Repo-local config
	// is still read by git, but the dangerous keys are overridden above.
	cmd.Env = append(cmd.Environ(),
		"GIT_ALLOW_PROTOCOL="+allowedProtocols,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Scrub any embedded credential before it reaches the caller, which
		// may log or persist it.
		joined := strings.Join(args, " ")
		return redact.String(string(out)), fmt.Errorf("git %s: %w (out=%q)", joined, err, redact.String(strings.TrimSpace(string(out))))
	}
	return string(out), nil
}

// mkdirTempPreferring creates a temp dir under preferredRoot when set, falling
// back to the system temp location. The orchestrator passes a subdir of the
// worktree root: that is always a writable, exec-capable volume when the
// runner is configured, whereas in a scratch deploy /tmp is either absent
// entirely or a tmpfs Docker mounted noexec by default — and the askpass
// helper must be executable by git. An empty preferredRoot keeps the plain
// os.MkdirTemp behavior.
func mkdirTempPreferring(preferredRoot, pattern string) (string, error) {
	if preferredRoot == "" {
		return os.MkdirTemp("", pattern)
	}
	rootErr := os.MkdirAll(preferredRoot, 0o700)
	if rootErr == nil {
		dir, err := os.MkdirTemp(preferredRoot, pattern)
		if err == nil {
			return dir, nil
		}
		rootErr = err
	}
	dir, sysErr := os.MkdirTemp("", pattern)
	if sysErr != nil {
		return "", errors.Join(rootErr, sysErr)
	}
	return dir, nil
}

// writeAskpassHelper creates a private (0700) directory plus a script that
// answers git's prompts from AGENT_GIT_TOKEN. The username "oauth2" works for
// both GitHub and Gitea (both accept any non-empty username with a token in the
// password slot). The caller removes dirPath after the git invocation.
func writeAskpassHelper(tempRoot string) (dirPath, scriptPath string, err error) {
	dirPath, err = mkdirTempPreferring(tempRoot, "windshift-askpass-*")
	if err != nil {
		return "", "", err
	}
	if err = os.Chmod(dirPath, 0o700); err != nil { //nolint:gosec // G302: dir needs +x to be traversed
		_ = os.RemoveAll(dirPath)
		return "", "", err
	}
	scriptPath = filepath.Join(dirPath, "askpass.sh")
	body := "#!/bin/sh\ncase \"$1\" in\n  Username*) printf 'oauth2\\n' ;;\n  Password*) printf '%s\\n' \"$AGENT_GIT_TOKEN\" ;;\nesac\n"
	if err = os.WriteFile(scriptPath, []byte(body), 0o700); err != nil { //nolint:gosec // G306: GIT_ASKPASS needs the exec bit
		_ = os.RemoveAll(dirPath)
		return "", "", err
	}
	return dirPath, scriptPath, nil
}
