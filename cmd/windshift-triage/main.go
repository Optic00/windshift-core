// Command windshift-triage prepares and pushes runner checkouts.
// It emits JSON for both subcommands and is the only process that accesses the
// clone cache or SCM credentials; agent containers never execute it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"windshift/internal/repoprep"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: windshift-triage <prepare|push> [flags]")
	}
	var err error
	switch os.Args[1] {
	case "prepare":
		err = runPrepare(os.Args[2:])
	case "push":
		err = runPush(os.Args[2:])
	default:
		fail(fmt.Sprintf("unknown subcommand %q (want prepare|push)", os.Args[1]))
	}
	if err != nil {
		fail(err.Error())
	}
}

func runPrepare(args []string) error {
	fs := flag.NewFlagSet("prepare", flag.ExitOnError)
	root := fs.String("root", "", "cache root directory (absolute)")
	wsID := fs.Int("workspace-id", 0, "workspace id")
	repo := fs.String("repo", "", "repo slug owner/name")
	remoteURL := fs.String("remote-url", "", "tokenless remote URL")
	baseRef := fs.String("base-ref", "main", "base ref to branch from")
	continueBranch := fs.String("continue-branch", "", "existing PR head branch to continue (overrides base-ref; pushes back to it)")
	destDir := fs.String("dest-dir", "", "place the checkout here instead of the default per-run location (WI-449 multi-repo sibling layout)")
	runID := fs.Int("run-id", 0, "run id")
	tokenFile := fs.String("token-file", "", "file holding the SCM token (askpass)")
	transport := fs.String("git-transport", "askpass", "askpass|proxy")
	allowFileURL := fs.Bool("allow-file-url", false, "permit file:// remotes (tests only)")
	_ = fs.Parse(args)

	if err := requireTransport(*transport); err != nil {
		return err
	}
	token, err := readToken(*tokenFile)
	if err != nil {
		return err
	}

	prep, err := repoprep.New(repoprep.Options{RootDir: *root, AllowFileURL: *allowFileURL})
	if err != nil {
		return err
	}
	pr, err := prep.Prepare(context.Background(), repoprep.RepoSpec{
		WorkspaceID:    *wsID,
		RepoSlug:       *repo,
		RemoteURL:      *remoteURL,
		BaseRef:        *baseRef,
		ContinueBranch: *continueBranch,
		DestDir:        *destDir,
		Token:          token,
	}, *runID)
	if err != nil {
		return err
	}
	if err := chownCheckoutForAgent(pr.Path); err != nil {
		return fmt.Errorf("chown checkout for agent uid: %w", err)
	}
	// Multi-repo runs also need their workspace parent owned by the agent.
	if *destDir != "" {
		if err := chownDirForAgent(filepath.Dir(*destDir)); err != nil {
			return fmt.Errorf("chown workspace parent for agent uid: %w", err)
		}
	}
	return emit(map[string]string{
		"checkout_path": pr.Path,
		"branch":        pr.Branch,
		"base_commit":   pr.BaseCommit,
	})
}

// chownDirForAgent makes a workspace directory traversable by the agent user.
// Local non-root runners keep ownership because their agent uses the same UID.
func chownDirForAgent(dir string) error {
	const agentUID, agentGID = 1000, 1000
	if err := os.Lchown(dir, agentUID, agentGID); err != nil {
		if errors.Is(err, iofs.ErrPermission) {
			fmt.Fprintf(os.Stderr, "windshift-triage: skipping workspace-parent chown (not root): %v\n", err)
			return nil
		}
		return err
	}
	return nil
}

func chownCheckoutForAgent(checkout string) error {
	const agentUID, agentGID = 1000, 1000
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Lchown(".", agentUID, agentGID); err != nil {
		if errors.Is(err, iofs.ErrPermission) {
			if verr := verifyCheckoutReadable(checkout, agentUID, agentGID); verr != nil {
				return verr
			}
			fmt.Fprintf(os.Stderr, "windshift-triage: skipping checkout chown (not root): %v\n", err)
			return nil
		}
		return err
	}
	return iofs.WalkDir(root.FS(), ".", func(p string, _ iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return root.Lchown(p, agentUID, agentGID)
	})
}

// verifyCheckoutReadable guards the not-root chown skip: skipping is only
// safe when the agent uid can still traverse the checkout (same-uid local
// dev, or world-readable modes from a normal umask). A non-root runner with
// a restrictive umask would otherwise hand the agent a tree it cannot even
// read, and the run burns its budget flailing on in-container EACCES
// instead of failing here with one clear line.
func verifyCheckoutReadable(checkout string, uid, gid int) error {
	info, err := os.Stat(checkout)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil // non-unix stat: nothing to verify
	}
	mode := info.Mode().Perm()
	switch {
	case int(st.Uid) == uid && mode&0o500 == 0o500:
		return nil
	case int(st.Gid) == gid && mode&0o050 == 0o050:
		return nil
	case mode&0o005 == 0o005:
		return nil
	}
	return fmt.Errorf("checkout %s (uid %d gid %d mode %#o) is unreadable by the agent uid %d and this non-root runner cannot chown it — run the runner as root or relax its umask", checkout, st.Uid, st.Gid, mode, uid)
}

func runPush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	dest := fs.String("dest", "", "per-run checkout directory")
	branch := fs.String("branch", "", "run branch to push")
	tokenFile := fs.String("token-file", "", "file holding the SCM token (askpass)")
	transport := fs.String("git-transport", "askpass", "askpass|proxy")
	proxyURL := fs.String("proxy-url", "", "git-proxy URL (proxy transport)")
	remoteURL := fs.String("remote-url", "", "trusted push URL (askpass transport)")
	allowFileURL := fs.Bool("allow-file-url", false, "permit file:// remotes (tests only)")
	skipIfHead := fs.String("skip-if-head", "", "base SHA: skip the push when the branch head still equals it (commit-less run)")
	_ = fs.Parse(args)

	if err := requireTransport(*transport); err != nil {
		return err
	}
	token, err := readToken(*tokenFile)
	if err != nil {
		return err
	}

	// PushBranch never trusts origin from the agent-mutated checkout; every
	// transport must provide a trusted target URL. Proxy transport uses the
	// git-proxy, which enforces the single granted ref server-side.
	remoteOverride := *remoteURL
	if *transport == "proxy" {
		if *proxyURL == "" {
			return fmt.Errorf("--proxy-url is required for --git-transport=proxy")
		}
		remoteOverride = *proxyURL
	}
	if remoteOverride == "" {
		return fmt.Errorf("--remote-url is required for --git-transport=askpass")
	}

	head, err := repoprep.PushBranch(context.Background(), repoprep.PushOptions{
		Dest:             *dest,
		Branch:           *branch,
		RemoteURL:        remoteOverride,
		Token:            token,
		AllowFileURL:     *allowFileURL,
		SkipIfHeadEquals: *skipIfHead,
	})
	if errors.Is(err, repoprep.ErrNoNewCommits) {
		// A commit-less run is a success with nothing to deliver — report
		// the skip instead of failing, so the runner can finish the run
		// without a branch (and without a PR).
		return emit(map[string]any{"head_sha": "", "skipped": true})
	}
	if err != nil {
		return err
	}
	return emit(map[string]any{"head_sha": head, "skipped": false})
}

func requireTransport(t string) error {
	if t != "askpass" && t != "proxy" {
		return fmt.Errorf("--git-transport must be askpass or proxy, got %q", t)
	}
	return nil
}

// readToken reads and trims the token file. An empty path yields no token
// (ambient/none) — never an error, so callers can omit it.
func readToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	// The token-file path is supplied by the orchestrator/runner, not by any
	// user-controlled input, and is read once into memory.
	b, err := os.ReadFile(path) //nolint:gosec // G304: operator/orchestrator-supplied path
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func emit(v any) error {
	enc := json.NewEncoder(os.Stdout)
	return enc.Encode(v)
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "windshift-triage:", msg)
	os.Exit(1)
}
