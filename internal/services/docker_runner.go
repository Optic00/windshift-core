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
		args = append(args, "-v", fmt.Sprintf("%s:/workspace", input.WorkspacePath))
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
	wg.Add(2)
	go func() {
		defer wg.Done()
		drainPipe(stdout, "stdout", emit)
	}()
	go func() {
		defer wg.Done()
		drainPipe(stderr, "stderr", emit)
	}()
	wg.Wait()

	waitErr := cmd.Wait()

	// docker-run with --rm leaves us no easy way to get the container id
	// after the fact (the CLI wraps a create+start+wait+rm). Phase 6's
	// long-lived RPC pipe path captures the id via `docker create` →
	// stamps it on the row before streaming starts. Skeleton stays
	// container_id-less.
	switch {
	case ctx.Err() != nil:
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
		Error:  fmt.Sprintf("docker run exited with code %d: %v", exitCode, waitErr),
	}
}

// drainPipe reads lines from r and pushes them onto the sink as the given
// event type. JSON-parseable lines pass through verbatim; non-JSON lines
// are wrapped as {"line":"<raw>"} so consumers always see a JSON document.
func drainPipe(rd io.Reader, eventType string, emit EventSink) {
	scanner := bufio.NewScanner(rd)
	// Containers can emit long lines (logged tool output, stack traces).
	// Bump the buffer well above the default 64KB but bound it so a
	// runaway producer can't pin memory.
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
}
