package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"windshift/internal/models"
)

// PiRunner drives a pi-coding-agent subprocess in RPC mode (or any other
// JSONL stdin/stdout process — the fake-pi fixture used by the tests is
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
// PiRunner doesn't know whether the subprocess is `pi --mode rpc`, a
// shell script, or a docker invocation that wraps either — Command +
// Args carry the whole story. Production wires `docker run -i --rm
// <image>` with the right env; tests wire a Go-binary fake-pi.
type PiRunner struct {
	// Command + Args build the subprocess invocation. Command is the
	// executable (e.g. "docker" or "/path/to/fake-pi"); Args are the
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
	// "session_idle". The pi RPC docs spell out the event vocabulary;
	// tests can override this to match the fake-pi script.
	IdleEventType string

	// ShutdownGrace bounds how long the runner waits for the subprocess
	// to exit after abort+stdin-close. Defaults to 10 seconds. Past
	// that, exec.CommandContext takes over and kills the process.
	ShutdownGrace time.Duration
}

const (
	defaultPiIdleEventType = "session_idle"
	defaultPiShutdownGrace = 10 * time.Second
	maxPiLine              = 1 << 20 // 1 MiB; matches docker_runner
)

// Run implements Runner. See the type comment for the lifecycle.
func (r *PiRunner) Run(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
	if r.Command == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "pi runner: Command is required"}
	}
	if r.InitialPrompt == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "pi runner: InitialPrompt is required"}
	}
	idleEvent := r.IdleEventType
	if idleEvent == "" {
		idleEvent = defaultPiIdleEventType
	}
	grace := r.ShutdownGrace
	if grace <= 0 {
		grace = defaultPiShutdownGrace
	}

	// Subprocess ctx is independent of the orchestrator ctx so we can
	// run a controlled shutdown before the kill. cancelCmd is what fires
	// the SIGTERM after grace expires.
	cmdCtx, cancelCmd := context.WithCancel(context.Background())
	defer cancelCmd()

	// All values reaching args come from operator config (PiRunner
	// fields) or orchestrator-managed env keys — no user-supplied data
	// hits the command line.
	cmd := exec.CommandContext(cmdCtx, r.Command, r.Args...) //nolint:gosec // G204: see comment above.
	cmd.Env = buildPiEnv(r.Env, input.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("pi stdin pipe: %v", err)}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("pi stdout pipe: %v", err)}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("pi stderr pipe: %v", err)}
	}
	if err := cmd.Start(); err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("pi start: %v", err)}
	}

	// Stderr drain — same JSON-or-wrap convention as docker_runner.
	var wgStderr sync.WaitGroup
	wgStderr.Add(1)
	go func() {
		defer wgStderr.Done()
		drainPipe(stderr, "stderr", emit)
	}()

	// Send the initial prompt. The RPC protocol takes JSONL commands;
	// each line is one JSON object. Errors here are fatal — without a
	// prompt the agent has nothing to do.
	promptCmd := map[string]any{"type": "prompt", "message": r.InitialPrompt}
	if err := writeJSONLine(stdin, promptCmd); err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("pi write prompt: %v", err)}
	}

	// Stream events. We need both "the subprocess emitted an idle
	// event" and "ctx was canceled" to converge on the same shutdown
	// path, so the read loop runs in its own goroutine and signals via
	// a channel.
	sawIdle := make(chan struct{}, 1)
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		drainPiStdout(stdout, idleEvent, emit, sawIdle)
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
	// that, kill via the cmdCtx.
	graceTimer := time.NewTimer(grace)
	defer graceTimer.Stop()
	doneWait := make(chan error, 1)
	go func() { doneWait <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-doneWait:
	case <-graceTimer.C:
		cancelCmd()
		waitErr = <-doneWait
	}

	// Make sure the stderr drain completes before we return — losing
	// trailing stderr to a goroutine leak shows up as missing context
	// in failure modes.
	wgStderr.Wait()
	<-streamDone

	switch {
	case canceledByCtx:
		return RunnerResult{Status: models.AgentRunStatusCanceled, Error: ctx.Err().Error()}
	case waitErr == nil:
		return RunnerResult{Status: models.AgentRunStatusSucceeded}
	}

	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	return RunnerResult{
		Status: models.AgentRunStatusFailed,
		Error:  fmt.Sprintf("pi subprocess exited with code %d: %v", exitCode, waitErr),
	}
}

// DockerPiRunner spawns the runner image via `docker run -i --rm ...` and
// delegates the JSONL stdio orchestration to PiRunner. The wrapper exists
// because docker args (env, volume mounts) depend on RunInput, while
// PiRunner.Args is static — splitting concerns keeps the JSONL logic
// independent of the container layer.
type DockerPiRunner struct {
	Image         string
	DockerBinary  string
	Env           map[string]string
	ExtraArgs     []string
	InitialPrompt string
	IdleEventType string
	ShutdownGrace time.Duration
}

// Run implements Runner. Builds docker args from the runner's static
// config + RunInput.Env, then dispatches through PiRunner.
func (r *DockerPiRunner) Run(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
	if r.Image == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "docker pi runner: Image is required"}
	}
	bin := r.DockerBinary
	if bin == "" {
		bin = "docker"
	}

	args := []string{"run", "-i", "--rm"}
	mergedEnv := make(map[string]string, len(r.Env)+len(input.Env))
	for k, v := range r.Env {
		mergedEnv[k] = v
	}
	for k, v := range input.Env {
		mergedEnv[k] = v
	}
	for k, v := range mergedEnv {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, "-e", fmt.Sprintf("AGENT_RUN_ID=%d", input.RunID))
	if input.WorkspacePath != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/workspace", input.WorkspacePath))
	}
	args = append(args, r.ExtraArgs...)
	args = append(args, r.Image)

	inner := &PiRunner{
		Command:       bin,
		Args:          args,
		InitialPrompt: r.InitialPrompt,
		IdleEventType: r.IdleEventType,
		ShutdownGrace: r.ShutdownGrace,
	}
	return inner.Run(ctx, RunInput{RunID: input.RunID}, emit)
}

// buildPiEnv composes a "key=value" slice from the runner's static Env
// plus per-run env carried in RunInput. Per-run wins on conflict, same
// as DockerRunner.
func buildPiEnv(static, perRun map[string]string) []string {
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

// drainPiStdout reads NDJSON events from stdout and forwards each to the
// sink. JSON-parseable lines pass through verbatim; non-JSON lines are
// wrapped as {"line": "<raw>"}. When the parsed event's "type" matches
// idleEvent, sawIdle is signaled (non-blocking — only the first idle
// event needs to wake the orchestrator).
func drainPiStdout(rd io.Reader, idleEvent string, emit EventSink, sawIdle chan<- struct{}) {
	scanner := bufio.NewScanner(rd)
	scanner.Buffer(make([]byte, 64*1024), maxPiLine)
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
		if t, _ := parsed["type"].(string); t == idleEvent {
			select {
			case sawIdle <- struct{}{}:
			default:
			}
		}
	}
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
