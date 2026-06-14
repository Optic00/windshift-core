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

	"windshift/internal/models"
)

// AgentRunner drives the windshift-agent subprocess in JSONL RPC mode (or any other
// JSONL stdin/stdout process — the fake-agent fixture used by the tests is
// the same shape). Lifecycle for one run:
//
//  1. Start the subprocess via exec.CommandContext.
//  2. Write the initial prompt as one JSONL command on stdin.
//  3. Stream NDJSON events from stdout through the EventSink. Each line
//     is forwarded verbatim if it's valid JSON, otherwise wrapped as
//     {"line": "<raw>"} so consumers can rely on JSON-shaped payloads.
//  4. When an event with type == IdleEventType arrives (default
//     "session_idle"), the runner sends an abort command and closes
//     stdin so the subprocess exits cleanly.
//  5. On ctx cancel mid-stream, the same abort+close path runs and the
//     run is recorded as canceled. If the subprocess hasn't exited after
//     ShutdownGrace, the context's CommandContext kills it.
//
// AgentRunner doesn't know whether the subprocess is the windshift-agent binary, a
// shell script, or a docker invocation that wraps either — Command +
// Args carry the whole story. Production wires `docker run -i --rm
// <image>` with the right env; tests wire a Go-binary fake-agent.
type AgentRunner struct {
	// Command + Args build the subprocess invocation. Command is the
	// executable (e.g. "docker" or "/path/to/fake-agent"); Args are the
	// arguments. Both are required.
	Command string
	Args    []string

	// Env is forwarded into the subprocess. Production layers in
	// per-run env (LLM API keys, GH_TOKEN) via RunInput.Env; the runner
	// merges those over Env at spawn time.
	Env map[string]string

	// InitialPrompt is the user message the runner writes to stdin
	// immediately after the subprocess starts. Required for the run to
	// make any progress; an empty value is a configuration bug.
	InitialPrompt string

	// IdleEventType is the event-type string the runner looks for to
	// know the agent has finished and it should send abort. Defaults to
	// "session_idle". The JSONL contract spells out the event vocabulary;
	// tests can override this to match the fake-agent script.
	IdleEventType string

	// ShutdownGrace bounds how long the runner waits for the subprocess
	// to exit after abort+stdin-close. Defaults to 10 seconds. Past
	// that, exec.CommandContext takes over and kills the process.
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
	Image         string
	DockerBinary  string
	Env           map[string]string
	ExtraArgs     []string
	InitialPrompt string
	IdleEventType string
	ShutdownGrace time.Duration

	// Sandbox tunables. Empty / zero values fall back to the safe
	// defaults declared by sandboxDefaults below.
	Network   string // docker --network value
	PidsLimit int    // docker --pids-limit
	Memory    string // docker --memory + --memory-swap
	CPUs      string // docker --cpus
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

// buildDockerArgs assembles the full docker-run argv for a single agent
// run. Pure function over the runner config + RunInput so it can be
// unit-tested without a live docker daemon. The flags it emits are
// security-critical; the unit tests assert their presence.
//
// envFilePath, when non-empty, is added as `--env-file <path>` so env
// values (which may include WS_TOKEN) do not appear in the docker
// argv. Run() always supplies a real path; tests may pass "" to assert
// the conditional.
func (r *DockerAgentRunner) buildDockerArgs(input RunInput, envFilePath string) []string {
	args := []string{"run", "-i", "--rm"}
	args = append(args, baselineSandboxArgs(sandboxConfig{
		Network:   r.Network,
		PidsLimit: r.PidsLimit,
		Memory:    r.Memory,
		CPUs:      r.CPUs,
	})...)
	// /home/agent is the agent user's home; agent + ws state lives there at
	// runtime. Specific to the coding-agent image, so it is added on top of the
	// shared baseline rather than inside it. mode=1777 for the same reason as
	// /tmp above: the default root-owned 0750 tmpfs is unwritable by uid 1000,
	// so $HOME (e.g. ~/.config) couldn't be created under --read-only.
	args = append(args, "--tmpfs=/home/agent:rw,nosuid,nodev,size=512m,mode=1777")

	if envFilePath != "" {
		args = append(args, "--env-file", envFilePath)
	}
	if input.WorkspacePath != "" {
		args = append(args, "-v", workspaceMountSpec(input.WorkspacePath))
	}
	args = append(args, r.ExtraArgs...)
	args = append(args, r.Image)
	return args
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
	if r.Image == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "docker agent runner: Image is required"}
	}
	bin := r.DockerBinary
	if bin == "" {
		bin = "docker"
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
	}
}

// drainAgentStdout reads NDJSON events from stdout and forwards each to the
// sink. JSON-parseable lines pass through verbatim; non-JSON lines are
// wrapped as {"line": "<raw>"}. When the parsed event's "type" matches
// idleEvent, sawIdle is signaled (non-blocking — only the first idle
// event needs to wake the orchestrator). sawContractEvent records whether
// at least one typed JSONL event arrived — the discriminator between a real
// agent and a wrong image that just prints text and exits (WI-312).
//
// Returns nil on a clean scan to EOF. If the scanner stops early (oversized
// line / read error), the rest of the stream is still drained to EOF so the
// pipe never backs up, and the error is returned so the runner can mark the
// event stream as degraded instead of silently completing.
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
		// The line scanner is dead, so the idle event can no longer be
		// observed — wake the orchestrator now so it runs the abort path
		// (otherwise it would sit in its select until ctx cancel while we
		// block below draining toward an EOF that needs the subprocess to
		// exit first).
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
