// Command windshift-triage is the runner-private repo-preparation helper. It is
// the only thing that touches git and the bare-clone cache, invoked as a
// subprocess by both the local in-process worker and the remote windshift-
// runner so local and remote prepare repos byte-identically (WI-205 / page 35).
//
// Two subcommands, both emitting a single JSON object on stdout:
//
//	windshift-triage prepare \
//	  --root <cache-root> --workspace-id <n> --repo owner/name \
//	  --remote-url <tokenless-url> --base-ref main --run-id <n> \
//	  [--token-file <path>] [--git-transport askpass|proxy]
//	  -> {"checkout_path":"...","branch":"agent-runs/run-<n>","base_commit":"<sha>"}
//
//	windshift-triage push \
//	  --dest <checkout> --branch agent-runs/run-<n> \
//	  [--token-file <path>] [--git-transport askpass|proxy] [--proxy-url <url>]
//	  [--skip-if-head <base-sha>]
//	  -> {"head_sha":"<sha>","skipped":false}
//	     ({"head_sha":"","skipped":true} when the head still equals
//	     --skip-if-head: a commit-less run pushes nothing)
//
// Privilege separation is the point: the agent container never execs this and
// never sees the cache or SCM credentials; the orchestrator never inlines its
// filesystem reach.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
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
	return emit(map[string]string{
		"checkout_path": pr.Path,
		"branch":        pr.Branch,
		"base_commit":   pr.BaseCommit,
	})
}

// chownCheckoutForAgent hands the prepared checkout to the pinned agent uid:
// every job container runs --user=1000:1000 (baselineSandboxArgs), but the
// checkout is created with the runner process's own uid — root on a
// production runner host — so without this the tree is unwritable from
// inside the container and the agent's first git operation fails (WI-388).
// A non-root runner (local dev) can't chown and doesn't need to: the run
// container is spawned by the same uid that owns the checkout there — that
// case is skipped, every other failure is fatal so the run fails fast with a
// clear error instead of an opaque in-container EACCES.
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
