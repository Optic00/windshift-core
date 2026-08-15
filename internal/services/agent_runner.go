package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"windshift/internal/llm"
	"windshift/internal/models"
)

// AgentRunner runs a JSONL subprocess. It sends the initial prompt, forwards
// JSON events, and aborts on idle or context cancellation. Command and Args
// support the production container and test fixtures alike.
type AgentRunner struct {
	// Command and Args build the subprocess invocation.
	Command string
	Args    []string

	// Env is merged with per-run environment at spawn time.
	Env map[string]string

	// InitialPrompt is written immediately after startup.
	InitialPrompt string

	// IdleEventType triggers graceful shutdown; defaults to "session_idle".
	IdleEventType string

	// ShutdownGrace bounds graceful shutdown before the context kills the process.
	ShutdownGrace time.Duration
}

const (
	defaultAgentIdleEventType = "session_idle"
	defaultAgentShutdownGrace = 10 * time.Second
	maxAgentLine              = 1 << 20 // 1 MiB; matches docker_runner
)

// Run implements Runner. See the type comment for the lifecycle.
func (r *AgentRunner) Run(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
	if r.Command == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "agent runner: Command is required"}
	}
	if r.InitialPrompt == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "agent runner: InitialPrompt is required"}
	}
	idleEvent := r.IdleEventType
	if idleEvent == "" {
		idleEvent = defaultAgentIdleEventType
	}
	grace := r.ShutdownGrace
	if grace <= 0 {
		grace = defaultAgentShutdownGrace
	}

	// Subprocess ctx is independent of the orchestrator ctx so we can
	// run a controlled shutdown before the kill. cancelCmd is what fires
	// the SIGTERM after grace expires.
	cmdCtx, cancelCmd := context.WithCancel(context.Background())
	defer cancelCmd()

	// All values reaching args come from operator config (AgentRunner
	// fields) or orchestrator-managed env keys — no user-supplied data
	// hits the command line.
	cmd := exec.CommandContext(cmdCtx, r.Command, r.Args...) //nolint:gosec // G204: see comment above.
	cmd.Env = buildAgentEnv(r.Env, input.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("agent stdin pipe: %v", err)}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("agent stdout pipe: %v", err)}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("agent stderr pipe: %v", err)}
	}
	if err := cmd.Start(); err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("agent start: %v", err)}
	}

	// Stderr drain — same JSON-or-wrap convention as docker_runner.
	var wgStderr sync.WaitGroup
	var stderrDrainErr error
	wgStderr.Add(1)
	go func() {
		defer wgStderr.Done()
		stderrDrainErr = drainPipe(stderr, "stderr", emit)
	}()

	// Send the initial prompt. The RPC protocol takes JSONL commands;
	// each line is one JSON object. Errors here are fatal — without a
	// prompt the agent has nothing to do.
	promptCmd := map[string]any{"type": "prompt", "message": r.InitialPrompt}
	if err := writeJSONLine(stdin, promptCmd); err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("agent write prompt: %v", err)}
	}

	// Stream events. We need both "the subprocess emitted an idle
	// event" and "ctx was canceled" to converge on the same shutdown
	// path, so the read loop runs in its own goroutine and signals via
	// a channel.
	sawIdle := make(chan struct{}, 1)
	streamDone := make(chan struct{})
	var sawContractEvent atomic.Bool
	var streamErr error
	var outcome agentOutcome // written only by the drain goroutine; read after <-streamDone
	go func() {
		defer close(streamDone)
		streamErr = drainAgentStdout(stdout, idleEvent, emit, sawIdle, &sawContractEvent, &outcome)
	}()

	// Shutdown trigger: whichever fires first — idle from the
	// subprocess or cancel from the orchestrator — drives the abort.
	var canceledByCtx bool
	select {
	case <-sawIdle:
	case <-ctx.Done():
		canceledByCtx = true
	case <-streamDone:
		// Subprocess closed stdout on its own (likely crashed or
		// finished without an idle event). Fall through to the wait.
	}

	// Best-effort abort + stdin close. If the read loop is still
	// running, this gives it a clean shutdown signal.
	if canceledByCtx || !isClosed(streamDone) {
		_ = writeJSONLine(stdin, map[string]any{"type": "abort"})
	}
	_ = stdin.Close()

	// Wait up to grace for the subprocess to exit on its own; past
	// that, kill via the cmdCtx. All pipe readers must finish BEFORE
	// cmd.Wait — os/exec closes the stdout/stderr pipes inside Wait,
	// which would race the drain goroutines and could drop trailing
	// output (same ordering DockerRunner uses). The grace timer
	// therefore runs independently of Wait: a subprocess that ignores
	// the abort gets killed by cancelCmd, the pipes hit EOF, the drains
	// return, and only then do we reap it.
	graceTimer := time.NewTimer(grace)
	defer graceTimer.Stop()
	waitDone := make(chan struct{})
	go func() {
		select {
		case <-graceTimer.C:
			cancelCmd()
		case <-waitDone:
		}
	}()
	wgStderr.Wait()
	<-streamDone
	waitErr := cmd.Wait()
	close(waitDone)

	// Recovery-aware review flag: a high-signal, unrecovered tool misuse
	// (invented tool / unsatisfiable schema) correlates with hallucination and
	// is worth a human glance, even when the run otherwise "succeeded". Emitted
	// once, as an orthogonal annotation — it deliberately does NOT change the
	// run status below.
	if verdict := llm.EvaluateReview(outcome.toolCalls, llm.DefaultReviewFlagConfig()); verdict.Flagged {
		classes := make([]string, len(verdict.UnrecoveredClasses))
		for i, c := range verdict.UnrecoveredClasses {
			classes[i] = string(c)
		}
		if payload, err := json.Marshal(map[string]any{
			"reasons":    verdict.Reasons,
			"classes":    classes,
			"tool_calls": len(outcome.toolCalls),
		}); err == nil {
			_ = emit("review_flagged", string(payload))
		}
	}

	switch {
	case canceledByCtx:
		return RunnerResult{Status: models.AgentRunStatusCanceled, Error: ctx.Err().Error()}
	case waitErr == nil && !sawContractEvent.Load():
		// Exit 0 without one valid JSONL contract event means the subprocess
		// never spoke the protocol — almost certainly the wrong image (WI-312:
		// the ws-carrier's help text once reported as a successful run).
		return RunnerResult{
			Status: models.AgentRunStatusFailed,
			Error:  "agent subprocess exited without emitting a single JSONL contract event — is the configured agent image actually the windshift-agent image?",
		}
	case waitErr == nil && (streamErr != nil || stderrDrainErr != nil):
		// The subprocess exited 0 but a drain died mid-stream (e.g. an
		// oversized line tripped bufio.ErrTooLong) — events after that
		// point were discarded, so the stream is degraded and the run
		// must not be reported as a clean success.
		return RunnerResult{
			Status: models.AgentRunStatusFailed,
			Error:  fmt.Sprintf("agent event stream degraded — output drain failed mid-run, trailing events discarded: %v", errors.Join(streamErr, stderrDrainErr)),
		}
	case waitErr == nil && outcome.finishOutcome == "blocked":
		// The agent declared itself blocked via the finish tool — the run
		// did not deliver and must surface as failed, with the agent's own
		// summary as the reason.
		return RunnerResult{
			Status: models.AgentRunStatusFailed,
			Error:  "agent blocked: " + outcome.finishSummary,
		}
	case waitErr == nil && outcome.lastError != "" && outcome.finishOutcome == "":
		// The agent emitted an error event (unrecovered stream error, max
		// turns, …) and never reached a finish: exit 0 notwithstanding, the
		// run died mid-task. Before this mapping, a broker stream break was
		// recorded as a clean no_changes success (WI-395).
		return RunnerResult{
			Status: models.AgentRunStatusFailed,
			Error:  "agent error: " + outcome.lastError,
		}
	case waitErr == nil:
		// completed and needs_info both count as a successful run: the agent
		// either delivered or correctly handed the item back to a human. The
		// agent's own finish summary rides along as the PR note (WI-400).
		return RunnerResult{Status: models.AgentRunStatusSucceeded, Summary: outcome.finishSummary}
	}

	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	return RunnerResult{
		Status: models.AgentRunStatusFailed,
		Error:  fmt.Sprintf("agent subprocess exited with code %d: %v", exitCode, waitErr),
	}
}

// DockerAgentRunner spawns the runner image via `docker run` and delegates
// the JSONL stdio orchestration to AgentRunner. The wrapper exists because
// docker args (env, volume mounts, sandbox flags) depend on RunInput,
// while AgentRunner.Args is static — splitting concerns keeps the JSONL
// logic independent of the container layer.
//
// The sandbox flags baked by buildDockerArgs are not configurable from
// outside this file; operator-tunable knobs (network, pids-limit,
// memory, cpus) are exposed through the named fields below, but flags
// like --cap-drop=ALL or --security-opt=no-new-privileges are part of
// the contract and cannot be turned off.
type DockerAgentRunner struct {
	Image               string
	DockerBinary        string
	AllowedImages       []string
	AllowUnlabeledImage bool
	Env                 map[string]string
	ExtraArgs           []string
	InitialPrompt       string
	IdleEventType       string
	ShutdownGrace       time.Duration

	// Sandbox tunables. Empty / zero values fall back to the safe
	// defaults declared by sandboxDefaults below.
	Network   string // docker --network value
	PidsLimit int    // docker --pids-limit
	Memory    string // docker --memory + --memory-swap
	CPUs      string // docker --cpus
}

const (
	// AgentContractLabel identifies images that implement the coding-agent JSONL contract.
	AgentContractLabel = "org.windshift.agent-contract"
	// AgentContractVersion is the contract version spoken by this runner.
	AgentContractVersion = "v1"
)

// ValidateAgentContractImage verifies the selected coding-agent image before
// execution. It pulls an image that is not yet present locally, then inspects
// the label again.
func ValidateAgentContractImage(ctx context.Context, dockerBin, image string, allowUnlabeled bool) error {
	inspect := func() (string, error) {
		out, err := exec.CommandContext(ctx, dockerBin, "image", "inspect", //nolint:gosec // argv execution does not invoke a shell
			"--format", `{{index .Config.Labels "`+AgentContractLabel+`"}}`, image).Output()
		return strings.TrimSpace(string(out)), err
	}

	label, err := inspect()
	if err != nil {
		if out, pullErr := exec.CommandContext(ctx, dockerBin, "pull", image).CombinedOutput(); pullErr != nil { //nolint:gosec // argv execution does not invoke a shell
			return fmt.Errorf("pull image %s for agent-contract validation: %w: %s", image, pullErr, strings.TrimSpace(string(out)))
		}
		label, err = inspect()
		if err != nil {
			return fmt.Errorf("inspect image %s after pull for agent-contract validation: %w", image, err)
		}
	}

	switch {
	case label == AgentContractVersion:
		return nil
	case label == "" && allowUnlabeled:
		return nil
	case label == "":
		return fmt.Errorf("image %s does not carry the %s label", image, AgentContractLabel)
	default:
		return fmt.Errorf("image %s carries %s=%q, but the runner requires %q", image, AgentContractLabel, label, AgentContractVersion)
	}
}

// sandboxDefaults are the hardened defaults applied when the caller
// has not overridden a tunable. Network defaults to a name the operator
// is expected to have created with egress restrictions (see
// deploy/coding-agent/README.md). The runner (windshift-runner) may set
// the Network field to "bridge" to opt into host egress, loudly.
var sandboxDefaults = struct {
	Network   string
	PidsLimit int
	Memory    string
	CPUs      string
}{
	Network:   "coding-agent-egress",
	PidsLimit: 512,
	Memory:    "4g",
	CPUs:      "2",
}

// sandboxConfig carries the operator-tunable resource knobs that layer onto the
// non-negotiable baseline sandbox flags. Empty / zero fields fall back to
// sandboxDefaults.
type sandboxConfig struct {
	Network   string
	PidsLimit int
	Memory    string
	CPUs      string
}

// baselineSandboxArgs returns the non-negotiable hardening flags plus the
// resolved resource limits applied to EVERY job kind — the coding agent and the
// action_container / ci_task plain-container path alike (WI-238 security Phase
// 2). A job kind may add its own flags/mounts/image around these, but cannot
// remove them. It does NOT include `run`, `-i`, the env-file, the workspace
// mount, ExtraArgs, or the image; callers append those.
func baselineSandboxArgs(cfg sandboxConfig) []string {
	args := make([]string, 0, 10)
	args = append(args,
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--user=1000:1000", // non-root; matches the agent uid pinned in the agent image
		"--read-only",
		// mode=1777: a Docker tmpfs is created root-owned 0750 by default, which
		// the non-root agent (uid 1000) can't write — and with --read-only the
		// tmpfs mounts are the only writable paths. Sticky world-writable (like a
		// real /tmp) lets the agent write; the mount is per-run and discarded.
		"--tmpfs=/tmp:rw,nosuid,nodev,size=256m,mode=1777",
	)

	network := cfg.Network
	if network == "" {
		network = sandboxDefaults.Network
	}
	args = append(args, "--network="+network)

	pids := cfg.PidsLimit
	if pids <= 0 {
		pids = sandboxDefaults.PidsLimit
	}
	args = append(args, fmt.Sprintf("--pids-limit=%d", pids))

	memory := cfg.Memory
	if memory == "" {
		memory = sandboxDefaults.Memory
	}
	// --memory-swap matches --memory so the container can't swap past
	// its memory cap (docker default: swap = 2*memory).
	args = append(args, "--memory="+memory, "--memory-swap="+memory)

	cpus := cfg.CPUs
	if cpus == "" {
		cpus = sandboxDefaults.CPUs
	}
	args = append(args, "--cpus="+cpus)

	return args
}

// buildDockerArgs creates security-critical docker args without a daemon. An
// env file keeps values such as WS_TOKEN out of argv; tests may omit it.
func (r *DockerAgentRunner) buildDockerArgs(input RunInput, envFilePath string) []string {
	args := []string{"run", "-i", "--rm"}
	args = append(args, baselineSandboxArgs(sandboxConfig{
		Network:   r.Network,
		PidsLimit: r.PidsLimit,
		Memory:    r.Memory,
		CPUs:      r.CPUs,
	})...)
	// The coding-agent home needs a uid-1000-writable tmpfs under --read-only.
	args = append(args, "--tmpfs=/home/agent:rw,nosuid,nodev,size=512m,mode=1777")

	if envFilePath != "" {
		args = append(args, "--env-file", envFilePath)
	}
	if input.WorkspacePath != "" {
		args = append(args, "-v", workspaceMountSpec(input.WorkspacePath))
	}
	args = append(args, r.ExtraArgs...)
	args = append(args, r.agentImage(input))
	return args
}

// agentImage selects the container image for this run: a per-run override
// (input.Image, set from a binding's runner_image for pool runs — WI-450) when
// present, else the runner's statically configured default image. The override
// is still a coding-agent image; only the image name changes, never the
// baseline sandbox flags or the JSONL contract.
func (r *DockerAgentRunner) agentImage(input RunInput) string {
	if input.Image != "" {
		return input.Image
	}
	return r.Image
}

func (r *DockerAgentRunner) imageAllowed(image string) bool {
	if image == r.Image {
		return true
	}
	for _, allowed := range r.AllowedImages {
		if image == allowed {
			return true
		}
	}
	return false
}

// workspaceMountSpec renders the bind-mount argument for the per-run
// checkout. The :Z suffix privately relabels the tree on SELinux-enforcing
// hosts (WI-388): without it the container process gets EACCES on every read
// even though DAC permits — the same denial the runner's own credential-volume
// preflight diagnoses. The checkout is per-run and throwaway, so a private
// label is safe; on hosts without SELinux the flag is a no-op.
func workspaceMountSpec(hostPath string) string {
	return fmt.Sprintf("%s:/workspace:Z", hostPath)
}

// Run implements Runner. Builds docker args from the runner's static
// config + RunInput.Env, then dispatches through AgentRunner.
func (r *DockerAgentRunner) Run(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
	image := r.agentImage(input)
	if image == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "docker agent runner: Image is required"}
	}
	if !r.imageAllowed(image) {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("docker agent runner: image %s is not in the runner image allowlist", image)}
	}
	bin := r.DockerBinary
	if bin == "" {
		bin = "docker"
	}
	if err := ValidateAgentContractImage(ctx, bin, image, r.AllowUnlabeledImage); err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "docker agent runner: " + err.Error()}
	}

	// Write env to a 0600 file passed via --env-file. Env values (which
	// can include WS_TOKEN + SCM tokens forwarded by the orchestrator)
	// must never reach docker argv where they'd be visible via
	// /proc/<pid>/cmdline and `docker inspect`.
	envFile, cleanup, err := writeDockerEnvFile(r.Env, input.Env, input.RunID)
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("docker agent runner: env file: %v", err)}
	}
	defer cleanup()

	initialPrompt := input.InitialPrompt
	if initialPrompt == "" {
		initialPrompt = r.InitialPrompt
	}
	inner := &AgentRunner{
		Command:       bin,
		Args:          r.buildDockerArgs(input, envFile),
		InitialPrompt: initialPrompt,
		IdleEventType: r.IdleEventType,
		ShutdownGrace: r.ShutdownGrace,
	}
	return inner.Run(ctx, RunInput{RunID: input.RunID}, emit)
}

// writeDockerEnvFile writes the merged env map (static runner Env
// overridden by per-run RunInput.Env, plus AGENT_RUN_ID) to a
// 0600-permissioned temp file in docker's --env-file format
// (`KEY=value\n` per line). Returns the path plus a cleanup func the
// caller must defer.
func writeDockerEnvFile(static, perRun map[string]string, runID int) (path string, cleanup func(), err error) {
	merged := make(map[string]string, len(static)+len(perRun)+1)
	for k, v := range static {
		merged[k] = v
	}
	for k, v := range perRun {
		merged[k] = v
	}
	merged["AGENT_RUN_ID"] = fmt.Sprintf("%d", runID)

	f, ferr := os.CreateTemp("", "windshift-agent-env-*.env")
	if ferr != nil {
		return "", func() {}, ferr
	}
	path = f.Name()
	cleanup = func() { _ = os.Remove(path) }
	if err = f.Chmod(0o600); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	for k, v := range merged {
		// docker --env-file is line-based KEY=value; values cannot
		// contain newlines (docker rejects them). The orchestrator
		// only emits short alphanumeric tokens / config strings, so
		// this is asserted rather than escaped.
		line := k + "=" + v + "\n"
		if _, err = f.WriteString(line); err != nil {
			_ = f.Close()
			cleanup()
			return "", func() {}, err
		}
	}
	if err = f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

// buildAgentEnv composes a "key=value" slice from the runner's static Env
// plus per-run env carried in RunInput. Per-run wins on conflict, same
// as DockerRunner.
func buildAgentEnv(static, perRun map[string]string) []string {
	merged := make(map[string]string, len(static)+len(perRun))
	for k, v := range static {
		merged[k] = v
	}
	for k, v := range perRun {
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}

// writeJSONLine encodes obj and writes it as a single LF-terminated line.
// JSON encoding here intentionally does not buffer beyond a single message
// — RPC protocols rely on the producer flushing per record.
func writeJSONLine(w io.Writer, obj any) error {
	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// agentOutcome accumulates the run-determining events seen on the agent's
// stdout: the last {"type":"error"} message and the structured
// {"type":"finish"} outcome/summary. Written only by the drain goroutine and
// read by the runner after <-streamDone, so no locking is needed.
type agentOutcome struct {
	lastError     string
	finishOutcome string
	finishSummary string
	// toolCalls is the ordered classification of every tool_done event, used
	// after the stream closes to decide whether the run needs human review
	// (see EvaluateReview). Successful calls are recorded too, since a later
	// success is what recovers an earlier high-signal failure.
	toolCalls []llm.ToolCallOutcome
}

// observe inspects one parsed agent event. "retry" events are deliberately
// NOT recorded as errors — they are recovered hiccups the agent already
// handled; only an unrecovered failure arrives as type "error".
func (o *agentOutcome) observe(t string, parsed map[string]any) {
	switch t {
	case "error":
		if msg, ok := parsed["message"].(string); ok && msg != "" {
			o.lastError = msg
		} else {
			o.lastError = "agent reported an unspecified error"
		}
	case "finish":
		o.finishOutcome, _ = parsed["outcome"].(string)
		o.finishSummary, _ = parsed["summary"].(string)
	case "tool_done":
		tool, _ := parsed["tool"].(string)
		output, _ := parsed["output"].(string)
		o.toolCalls = append(o.toolCalls, llm.ToolCallOutcome{
			Tool:  tool,
			Class: llm.Classify(tool, output),
		})
	}
}

// drainAgentStdout forwards NDJSON or wraps raw lines, signals the first idle
// event, and records typed contract events to reject text-only images. Scanner
// failures still drain the pipe, then return an error for degraded handling.
func drainAgentStdout(rd io.Reader, idleEvent string, emit EventSink, sawIdle chan<- struct{}, sawContractEvent *atomic.Bool, outcome *agentOutcome) error {
	scanner := bufio.NewScanner(rd)
	scanner.Buffer(make([]byte, 64*1024), maxAgentLine)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var payload string
		var parsed map[string]any
		if json.Valid([]byte(line)) {
			payload = line
			_ = json.Unmarshal([]byte(line), &parsed)
		} else {
			b, _ := json.Marshal(map[string]string{"line": line})
			payload = string(b)
		}
		_ = emit("stdout", payload)
		t, _ := parsed["type"].(string)
		if t != "" {
			sawContractEvent.Store(true)
		}
		outcome.observe(t, parsed)
		if t == idleEvent {
			select {
			case sawIdle <- struct{}{}:
			default:
			}
		}
	}
	if err := scanner.Err(); err != nil {
		// Wake the abort path before draining after scanner failure.
		select {
		case sawIdle <- struct{}{}:
		default:
		}
		return drainRest(rd, "stdout", err, emit)
	}
	return nil
}

// isClosed reports whether a done-channel has already fired. Non-blocking;
// used to decide whether the read loop is still running before sending
// the redundant abort that an already-exited subprocess wouldn't care
// about.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
