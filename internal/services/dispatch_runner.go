package services

import (
	"context"

	"windshift/internal/models"
)

// KindDispatchRunner is the single Runner the worker pool drives; it routes a
// claimed job to the right execution mode by its kind (WI-146), keeping the
// substrate (claim / brokers / quota / reaping) kind-agnostic:
//
//   - coding_agent (default) → CodingAgent runner: the windshift-agent harness on the
//     fixed runner image.
//   - action_container / ci_task → Container runner: the job's admin image
//     run as a plain container.
type KindDispatchRunner struct {
	CodingAgent Runner
	Container   Runner
}

// Run implements Runner.
func (r *KindDispatchRunner) Run(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
	switch input.Kind {
	case "", models.JobKindCodingAgent:
		if r.CodingAgent == nil {
			return RunnerResult{Status: models.AgentRunStatusFailed, Error: "no coding-agent runner configured"}
		}
		return r.CodingAgent.Run(ctx, input, emit)
	default:
		if r.Container == nil {
			return RunnerResult{Status: models.AgentRunStatusFailed, Error: "no container runner configured for job kind " + input.Kind}
		}
		return r.Container.Run(ctx, input, emit)
	}
}

// ContainerImageRunner runs RunInput.Image as a plain container (no JSONL RPC),
// streaming its output and reporting its exit — the execution mode for
// action_container / ci_task jobs. The image is per-job; static Env / ExtraArgs
// are operator sandbox defaults merged under the job's env. It delegates to
// DockerRunner, which applies the same baseline sandbox flags as the coding
// agent (WI-238 security Phase 2) — a job kind cannot opt out of the baseline.
type ContainerImageRunner struct {
	DockerBinary string
	Env          map[string]string
	ExtraArgs    []string

	// Sandbox tunables forwarded to DockerRunner; empty / zero fall back to
	// sandboxDefaults.
	Network   string
	PidsLimit int
	Memory    string
	CPUs      string
}

// Run implements Runner.
func (r *ContainerImageRunner) Run(ctx context.Context, input RunInput, emit EventSink) RunnerResult {
	if input.Image == "" {
		return RunnerResult{Status: models.AgentRunStatusFailed, Error: "container job has no image"}
	}
	dr := &DockerRunner{
		Image:        input.Image,
		DockerBinary: r.DockerBinary,
		Env:          r.Env, // DockerRunner merges r.Env under input.Env
		ExtraArgs:    r.ExtraArgs,
		Network:      r.Network,
		PidsLimit:    r.PidsLimit,
		Memory:       r.Memory,
		CPUs:         r.CPUs,
	}
	return dr.Run(ctx, input, emit)
}
