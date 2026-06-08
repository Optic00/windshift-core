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
//	  -> {"head_sha":"<sha>"}
//
// Privilege separation is the point: the agent container never execs this and
// never sees the cache or SCM credentials; the orchestrator never inlines its
// filesystem reach.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

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
		WorkspaceID: *wsID,
		RepoSlug:    *repo,
		RemoteURL:   *remoteURL,
		BaseRef:     *baseRef,
		Token:       token,
	}, *runID)
	if err != nil {
		return err
	}
	return emit(map[string]string{
		"checkout_path": pr.Path,
		"branch":        pr.Branch,
		"base_commit":   pr.BaseCommit,
	})
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
		Dest:         *dest,
		Branch:       *branch,
		RemoteURL:    remoteOverride,
		Token:        token,
		AllowFileURL: *allowFileURL,
	})
	if err != nil {
		return err
	}
	return emit(map[string]string{"head_sha": head})
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
