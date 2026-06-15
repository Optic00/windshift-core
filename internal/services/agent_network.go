package services

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// EnsureAgentNetwork makes sure the docker network agent containers are
// spawned into exists (WI-311). Before this, a fresh host failed every agent
// spawn with "network coding-agent-egress not found" until the operator
// hand-created it. Any process with docker access can create a plain bridge,
// so we do — but the network's whole point is egress filtering, which only the
// operator can apply, so creation comes with a loud unfiltered-egress warning
// pointing at the docs (deploy/coding-agent/README.md, "Egress network").
//
// network == "" resolves to the sandbox default; the built-in docker networks
// (bridge/host/none) are never created and only draw the warning, since
// choosing them is the documented loud opt-out from filtering. logf receives
// printf-style progress/warning lines (callers adapt log.Printf or slog).
func EnsureAgentNetwork(ctx context.Context, logf func(format string, args ...any), dockerBin, network string) {
	if dockerBin == "" {
		dockerBin = "docker"
	}
	if network == "" {
		network = sandboxDefaults.Network
	}
	switch network {
	case "bridge", "host", "none":
		logf("WARNING: agent containers run on the built-in %q docker network — egress is UNFILTERED (inherits host egress). See deploy/coding-agent/README.md (Egress network).", network)
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// dockerBin + network are operator config (env / runner defaults), not
	// user-supplied data — same posture as DockerAgentRunner.
	if err := exec.CommandContext(ctx, dockerBin, "network", "inspect", network).Run(); err == nil { //nolint:gosec // G204: operator-controlled argv
		return // exists; egress posture is the operator's, don't second-guess it
	}
	out, err := exec.CommandContext(ctx, dockerBin, "network", "create", "--driver", "bridge", network).CombinedOutput() //nolint:gosec // G204: operator-controlled argv
	if err != nil {
		// Lost a create race with a sibling runner? Then it exists now and
		// we're done; anything else (daemon down, no permission) stays the
		// operator's problem — agent spawns will fail with docker's error.
		if strings.Contains(string(out), "already exists") {
			return
		}
		logf("warning: could not create docker network %q: %v (%s) — agent container spawns will fail until it exists; create it manually: docker network create %s",
			network, err, strings.TrimSpace(string(out)), network)
		return
	}
	logf("WARNING: created docker network %q as a PLAIN bridge — agent egress is UNFILTERED. Agents only need DNS + the Windshift API host (LLM and git are brokered through the orchestrator); apply egress filtering per deploy/coding-agent/README.md (Egress network).", network)
}
