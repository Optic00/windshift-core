package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"windshift/internal/models"
)

// TriageRunner is the remote runner's repo-prep wrapper (WI-215). It owns git
// on the runner host so the untrusted agent container never does: for a run
// carrying a JobRepo it execs the windshift-triage binary to prepare a per-run
// checkout, runs the inner Runner against it, then pushes the run branch back
// through the git-proxy — all without the agent ever holding an SCM credential.
//
// Reads (clone/fetch) and the final push both go through the git-proxy at
// {APIBase}/git-proxy/{ws}/{owner}/{repo}, authenticated by the per-run token
// (WS_TOKEN). The proxy injects the real SCM credential server-side and gates
// the push to the single granted ref (WI-168).
//
// For a run WITHOUT a JobRepo (local runs, where the orchestrator already
// prepared WorkspacePath, and non-repo container jobs) TriageRunner is a pass-
// through to the inner Runner.
type TriageRunner struct {
	Inner     Runner // the real execution (DockerAgentRunner)
	TriageBin string // path to the windshift-triage binary
	CacheRoot string // --root for the bare-clone cache
	APIBase   string // orchestrator base incl. API prefix (for the git-proxy URL)
	Logger    *log.Logger
}

type triagePrepareOut struct {
	CheckoutPath string `json:"checkout_path"`
	Branch       string `json:"branch"`
	BaseCommit   string `json:"base_commit"`
}

// Run implements Runner.
func (t *TriageRunner) Run(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
	if input.Repo == nil {
		return t.Inner.Run(ctx, input, emit)
	}
	if t.TriageBin == "" || t.CacheRoot == "" || t.APIBase == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "triage runner: TriageBin, CacheRoot and APIBase are required for repo runs"}
	}
	token := input.Env["WS_TOKEN"]
	if token == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "triage runner: no WS_TOKEN for git-proxy auth"}
	}
	owner, repo, ok := splitSlug(input.Repo.Slug)
	if !ok {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("triage runner: bad repo slug %q", input.Repo.Slug)}
	}

	// The git-proxy URL is the tokenless remote both clone and push target;
	// the token authenticates via askpass/Basic and the proxy injects the SCM
	// credential. {ws} is cosmetic to the proxy (it authorizes by token) but
	// the route requires it.
	proxyURL := fmt.Sprintf("%s/git-proxy/%d/%s/%s",
		strings.TrimRight(t.APIBase, "/"), input.Repo.WorkspaceID, owner, repo)

	tokenFile, cleanupToken, err := writeTokenFile(token, input.RunID)
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("triage runner: token file: %v", err)}
	}
	defer cleanupToken()

	prep, err := t.prepare(ctx, input, proxyURL, tokenFile)
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("triage prepare: %v", err)}
	}
	// The checkout is throwaway; reclaim it whatever happens.
	defer func() {
		if rmErr := os.RemoveAll(prep.CheckoutPath); rmErr != nil {
			t.logf("triage runner: cleanup %s: %v", prep.CheckoutPath, rmErr)
		}
	}()

	input.WorkspacePath = prep.CheckoutPath
	_ = emit("lifecycle", fmt.Sprintf(
		`{"phase":"worktree_ready","path":%q,"branch":%q,"base_commit":%q}`,
		prep.CheckoutPath, prep.Branch, prep.BaseCommit))

	result := t.Inner.Run(ctx, input, emit)

	// Push the agent's commits only on success. A run that didn't succeed has
	// nothing the post-run PR hook should act on, and pushing a half-built
	// branch through the ref-gated proxy adds no value.
	if result.Status == models.AgentRunStatusSucceeded {
		if head, perr := t.push(ctx, prep, proxyURL, tokenFile); perr != nil {
			_ = emit("lifecycle", fmt.Sprintf(`{"phase":"push_failed","error":%q}`, RedactString(perr.Error())))
			return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("triage push: %v", perr)}
		} else {
			_ = emit("lifecycle", fmt.Sprintf(`{"phase":"pushed","branch":%q,"head":%q}`, prep.Branch, head))
		}
	}
	return result
}

func (t *TriageRunner) prepare(ctx context.Context, input RunInput, proxyURL, tokenFile string) (triagePrepareOut, error) {
	out, err := t.execTriage(ctx, "prepare",
		"--root", t.CacheRoot,
		"--workspace-id", fmt.Sprintf("%d", input.Repo.WorkspaceID),
		"--repo", input.Repo.Slug,
		"--remote-url", proxyURL,
		"--base-ref", input.Repo.BaseRef,
		"--run-id", fmt.Sprintf("%d", input.RunID),
		"--token-file", tokenFile,
	)
	if err != nil {
		return triagePrepareOut{}, err
	}
	var p triagePrepareOut
	if jerr := json.Unmarshal(out, &p); jerr != nil {
		return triagePrepareOut{}, fmt.Errorf("decode prepare output: %w", jerr)
	}
	if p.CheckoutPath == "" || p.Branch == "" {
		return triagePrepareOut{}, fmt.Errorf("prepare returned incomplete output: %s", string(out))
	}
	return p, nil
}

func (t *TriageRunner) push(ctx context.Context, prep triagePrepareOut, proxyURL, tokenFile string) (string, error) {
	out, err := t.execTriage(ctx, "push",
		"--dest", prep.CheckoutPath,
		"--branch", prep.Branch,
		"--git-transport", "proxy",
		"--proxy-url", proxyURL,
		"--token-file", tokenFile,
	)
	if err != nil {
		return "", err
	}
	var p struct {
		HeadSHA string `json:"head_sha"`
	}
	_ = json.Unmarshal(out, &p)
	return p.HeadSHA, nil
}

// execTriage runs the triage binary and returns its stdout. stderr is folded
// into the error (scrubbed) so a credential can't leak into logs.
func (t *TriageRunner) execTriage(ctx context.Context, sub string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, t.TriageBin, append([]string{sub}, args...)...) //nolint:gosec // G204: args are orchestrator-derived
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, RedactString(strings.TrimSpace(stderr.String())))
	}
	return []byte(stdout.String()), nil
}

func (t *TriageRunner) logf(format string, args ...any) {
	if t.Logger != nil {
		t.Logger.Printf(format, args...)
	}
}

// splitSlug splits "owner/repo" into its parts. Both must be non-empty and
// neither may contain a slash (a multi-segment path is rejected).
func splitSlug(slug string) (owner, repo string, ok bool) {
	owner, repo, found := strings.Cut(slug, "/")
	if !found || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", false
	}
	return owner, repo, true
}

// writeTokenFile writes the per-run token to a 0600 temp file for triage's
// --token-file, returning the path plus a cleanup func.
func writeTokenFile(token string, runID int) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", fmt.Sprintf("windshift-runtoken-%d-*", runID))
	if err != nil {
		return "", func() {}, err
	}
	path = f.Name()
	cleanup = func() { _ = os.Remove(path) }
	if err = f.Chmod(0o600); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if _, err = f.WriteString(token); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err = f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}
