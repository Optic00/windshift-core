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

// DockerRunner runs a plain container image to completion: it shells out to
// the `docker` CLI, pipes stdout/stderr back through the EventSink as NDJSON
// events, and reports the exit code as a terminal agent_run status. It is the
// execution mode for action_container / ci_task jobs (an admin-chosen image
// with no agent RPC), driven through ContainerImageRunner.
//
// Every spawn gets the same baseline sandbox flags as the coding agent
// (baselineSandboxArgs, WI-238 security Phase 2) and passes secrets via an
// --env-file rather than -e KEY=VALUE argv, so tokens never appear in
// /proc/<pid>/cmdline or `docker inspect`.
type DockerRunner struct {
	// Image is the container image to spawn. Required.
	Image string

	// DockerBinary is the path to the docker CLI. Defaults to "docker"
	// from $PATH.
	DockerBinary string

	// Env are environment variables forwarded into the container via an
	// --env-file (0600), merged under per-run RunInput.Env.
	Env map[string]string

	// ExtraArgs are appended to the docker-run command line before the
	// image name, on top of (never replacing) the baseline sandbox flags.
	ExtraArgs []string

	// Sandbox tunables. Empty / zero values fall back to sandboxDefaults.
	Network   string // docker --network value
	PidsLimit int    // docker --pids-limit
	Memory    string // docker --memory + --memory-swap
	CPUs      string // docker --cpus

	// ShutdownGrace bounds how long a canceled run may linger before the
	// docker CLI process is force-killed. Defaults to 10 seconds, same as
	// AgentRunner.ShutdownGrace.
	ShutdownGrace time.Duration
}

// buildDockerArgs assembles the docker-run argv for a plain container job:
// `run --rm` + the shared baseline sandbox flags + the env-file + optional
// workspace mount + ExtraArgs + image. Pure function so the baseline can be
// unit-tested without a live docker daemon.
func (r *DockerRunner) buildDockerArgs(input RunInput, envFilePath string) []string {
	args := []string{"run", "--rm"}
	args = append(args, baselineSandboxArgs(sandboxConfig{
		Network:   r.Network,
		PidsLimit: r.PidsLimit,
		Memory:    r.Memory,
		CPUs:      r.CPUs,
	})...)
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

// Run implements Runner. Each stdout line becomes a "stdout" event; each
// stderr line becomes a "stderr" event. Lines that parse as JSON are
// stored as-is in payload_json; lines that don't are wrapped in
// {"line": "<raw>"} so consumers can rely on payload_json being a JSON
// document.
func (r *DockerRunner) Run(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
	if r.Image == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "docker runner: image is required"}
	}
	bin := r.DockerBinary
	if bin == "" {
		bin = "docker"
	}

	// Secrets (per-run env may include WS_TOKEN / brokered tokens) go through a
	// 0600 --env-file, never -e KEY=VALUE argv where they'd be visible via
	// /proc/<pid>/cmdline and `docker inspect`. writeDockerEnvFile merges
	// r.Env (static) under input.Env (per-run wins) and stamps AGENT_RUN_ID.
	envFile, cleanup, err := writeDockerEnvFile(r.Env, input.Env, input.RunID)
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("docker runner: env file: %v", err)}
	}
	defer cleanup()
	args := r.buildDockerArgs(input, envFile)

	// The docker binary path is config-controlled (DockerBinary); args contain
	// only the baseline sandbox flags, the env-file path, ExtraArgs the service
	// vets at construction time, and the job image. No user-supplied data
	// reaches the command line.
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: see comment above.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("docker stdout pipe: %v", err)}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("docker stderr pipe: %v", err)}
	}
	if err := cmd.Start(); err != nil {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: fmt.Sprintf("docker start: %v", err)}
	}

	var wg sync.WaitGroup
	var stdoutDrainErr, stderrDrainErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		stdoutDrainErr = drainPipe(stdout, "stdout", emit)
	}()
	go func() {
		defer wg.Done()
		stderrDrainErr = drainPipe(stderr, "stderr", emit)
	}()

	// Pipes must be read to EOF before cmd.Wait — os/exec closes them
	// inside Wait, which would race the drain goroutines. drainPipe now
	// guarantees it consumes its pipe to EOF even when the line scanner
	// errors out, so the only way wg.Wait can hang is the container never
	// exiting. Guard that with a kill path consistent with AgentRunner's
	// ShutdownGrace: CommandContext already SIGKILLs the docker CLI when
	// ctx is canceled; the watchdog force-kills again after the grace
	// period in case that signal is lost, so a hung container cannot
	// wedge the worker forever.
	grace := r.ShutdownGrace
	if grace <= 0 {
		grace = defaultAgentShutdownGrace
	}
	watchdogDone := make(chan struct{})
	go func() {
		select {
		case <-watchdogDone:
			return
		case <-ctx.Done():
		}
		t := time.NewTimer(grace)
		defer t.Stop()
		select {
		case <-watchdogDone:
		case <-t.C:
			_ = cmd.Process.Kill()
		}
	}()
	wg.Wait()

	waitErr := cmd.Wait()
	close(watchdogDone)

	// docker-run with --rm leaves us no easy way to get the container id
	// after the fact (the CLI wraps a create+start+wait+rm). Phase 6's
	// long-lived RPC pipe path captures the id via `docker create` →
	// stamps it on the row before streaming starts. Skeleton stays
	// container_id-less.
	drainErr := errors.Join(stdoutDrainErr, stderrDrainErr)
	switch {
	case ctx.Err() != nil:
		return RunnerResult{Status: models.AgentRunStatusCanceled, Error: ctx.Err().Error()}
	case waitErr == nil && drainErr != nil:
		// The container exited 0 but part of its output never made it into
		// events. Reporting success here would silently hide the missing
		// output, so the run fails with the drain diagnostics instead.
		return RunnerResult{
			Status: models.AgentRunStatusFailed,
			Error:  fmt.Sprintf("docker run exited 0 but output drain failed (events truncated): %v", drainErr),
		}
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
		Error:  fmt.Sprintf("docker run exited with code %d: %v", exitCode, waitErr),
	}
}

// drainPipe forwards JSON or wraps raw lines. Scanner failures still drain the
// stream, then return an error so missing output cannot report success.
func drainPipe(rd io.Reader, eventType string, emit EventSink) error {
	scanner := bufio.NewScanner(rd)
	// Permit tool output and stack traces without unbounded memory use.
	const maxLine = 1 << 20 // 1 MiB
	scanner.Buffer(make([]byte, 64*1024), maxLine)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var payload string
		if json.Valid([]byte(line)) {
			payload = line
		} else {
			b, _ := json.Marshal(map[string]string{"line": line})
			payload = string(b)
		}
		_ = emit(eventType, payload)
	}
	if err := scanner.Err(); err != nil {
		return drainRest(rd, eventType, err, emit)
	}
	return nil
}

// drainRest finishes a stream whose line scanner stopped with an error. It
// emits one diagnostic event (same {"line":...} payload shape as wrapped
// non-JSON output), then keeps consuming the stream in raw discarded chunks
// until EOF — the pipe must NEVER back up, or the child blocks on a full
// pipe buffer and the worker wedges. The returned error carries the scan
// error plus how many trailing bytes were discarded.
func drainRest(rd io.Reader, eventType string, scanErr error, emit EventSink) error {
	b, _ := json.Marshal(map[string]string{
		"line": fmt.Sprintf("[runner] %s scan aborted: %v — remaining output will be drained and discarded", eventType, scanErr),
	})
	_ = emit(eventType, string(b))
	discarded, _ := io.Copy(io.Discard, rd)
	return fmt.Errorf("%s scan: %w (%d trailing bytes discarded)", eventType, scanErr, discarded)
}
