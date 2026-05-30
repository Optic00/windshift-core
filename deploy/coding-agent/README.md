# Coding-agent runner deployment

The coding-agent harness spawns one ephemeral container per run via
`docker run`. The container image lives in this directory
(`Dockerfile`); the orchestrator (`internal/services/pi_runner.go`)
assembles a hardened `docker run` argv around it.

## Hardening baked into every run

`DockerPiRunner.buildDockerArgs` (in `internal/services/pi_runner.go`)
emits these flags for every spawn. They are **not** configurable from
outside the file — operator-tunable knobs (network, CPU/memory/pids
budgets) are separate fields:

| Flag                             | Purpose                                                                |
|----------------------------------|------------------------------------------------------------------------|
| `--cap-drop=ALL`                 | Container starts with no Linux capabilities.                           |
| `--security-opt=no-new-privileges` | Prevents the container from gaining additional capabilities mid-run. |
| `--user=1000:1000`               | Runs as the unprivileged `agent` user pinned in the Dockerfile.        |
| `--read-only`                    | Root filesystem is read-only. Writable paths come from tmpfs mounts.   |
| `--tmpfs=/tmp` / `/home/agent`   | Per-run writable scratch space; size-capped, `nosuid,nodev`.           |

## Operator-tunable knobs

Set these env vars before starting the windshift server. They layer
**on top of** the hardened defaults — there is no opt-out for the
flags in the table above.

| Env var                       | Default              | Effect                                                   |
|-------------------------------|----------------------|----------------------------------------------------------|
| `CODING_AGENT_RUNNER_IMAGE`   | (unset → disabled)   | Required to enable the harness at all.                   |
| `CODING_AGENT_DOCKER_BINARY`  | `docker`             | Path to the docker CLI to invoke.                        |
| `CODING_AGENT_WORKTREE_ROOT`  | (required)           | Host directory where per-run worktrees are created.      |
| `CODING_AGENT_NETWORK`        | `coding-agent-egress` | docker `--network`. See "Egress network" below.          |
| `CODING_AGENT_PIDS_LIMIT`     | `512`                | docker `--pids-limit`.                                   |
| `CODING_AGENT_MEMORY`         | `4g`                 | docker `--memory` (also applied to `--memory-swap`).     |
| `CODING_AGENT_CPUS`           | `2`                  | docker `--cpus`.                                         |

## Egress network

The default `--network` value is `coding-agent-egress`, a **user-defined
bridge network the operator must create before starting the server**.
Picking a name distinct from `bridge` is intentional: it forces the
operator to think about egress filtering rather than inheriting the
host's default outbound posture.

A minimal setup with restrictive iptables (Linux host):

```bash
# 1. Create the network.
docker network create \
  --driver bridge \
  --subnet 10.100.0.0/24 \
  coding-agent-egress

# 2. Apply egress restrictions on the bridge interface (example):
#    only allow the LLM provider, the SCM provider, and DNS.
#    Adjust to your environment's outbound allowlist.
BRIDGE_IF="$(docker network inspect coding-agent-egress -f '{{(index .Options "com.docker.network.bridge.name")}}')"
sudo iptables -I DOCKER-USER -i "$BRIDGE_IF" -j DROP
sudo iptables -I DOCKER-USER -i "$BRIDGE_IF" -p udp --dport 53 -j ACCEPT
sudo iptables -I DOCKER-USER -i "$BRIDGE_IF" -d api.anthropic.com -j ACCEPT
sudo iptables -I DOCKER-USER -i "$BRIDGE_IF" -d api.github.com    -j ACCEPT
# …add gitea.your-company.com, etc.
```

A sidecar HTTP proxy with an allowlist (mitmproxy, envoy, squid) is the
other common shape — point `coding-agent-egress` at the proxy and
firewall everything else.

### Opting out (NOT recommended)

If you knowingly want the container to inherit host egress, set the env
var explicitly so the choice is loud in your config:

```bash
CODING_AGENT_NETWORK=bridge
```

Without this override the orchestrator will try to launch on
`coding-agent-egress`; docker fails fast if the network doesn't exist.
