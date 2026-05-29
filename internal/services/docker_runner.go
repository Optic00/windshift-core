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

// DockerRunner is the Phase 1 walking-skeleton container runner: it shells
// out to the `docker` CLI to spawn the windshift/coding-agent image, pipes
// stdout/stderr back through the EventSink as NDJSON events, and reports
// the exit code as a terminal agent_run status.
//
// Phase 6 (WI-89) replaces this with a goroutine that drives pi's RPC mode
// directly over a long-lived stdin/stdout pipe (no docker-cli subshell, no
// per-call container churn for streaming events). DockerRunner stays as a
// reference + fallback path until that lands.
type DockerRunner struct {
	// Image is the runner image to spawn, e.g.
	// "windshift/coding-agent:wi-84-skeleton". Required.
	Image string

	// DockerBinary is the path to the docker CLI. Defaults to "docker"
	// from $PATH.
	DockerBinary string

	// Env are environment variables forwarded into the container as
	// -e KEY=VALUE arguments. Values are passed verbatim; the caller is
	// responsible for not leaking secrets to logs upstream.
	Env map[string]string

	// ExtraArgs are appended to the docker-run command line before the
	// image name. Use for --memory, --cpus, --network, --workdir, etc.
	// Phase 5 wires real cgroup caps in here; the skeleton leaves it
	// empty.
	ExtraArgs []string
}

// Run implements Runner. Each stdout line becomes a "stdout" event; each
// stderr line becomes a "stderr" event. Lines that parse as JSON are
// stored as-is in payload_json; lines that don't are wrapped in
// {"line": "<raw>"} so consumers can rely on payload_json being a JSON
// document.
func (r *DockerRunner) Run(ctx context.Context, runID int, emit EventSink) RunnerResult {
	if r.Image == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "docker runner: image is required"}
	}
	bin := r.DockerBinary
	if bin == "" {
		bin = "docker"
	}

	args := []string{"run", "--rm"}
	for k, v := range r.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	// Stamp the run id so a baked entrypoint can echo it back without the
	// orchestrator having to inject it from outside.
	args = append(args, "-e", fmt.Sprintf("AGENT_RUN_ID=%d", runID))
	args = append(args, r.ExtraArgs...)
	args = append(args, r.Image)

	// The docker binary path is config-controlled (DockerBinary), the
	// args contain only operator-set env keys/values + ExtraArgs the
	// service vets at construction time. There's no user-supplied data
	// reaching the command line.
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
