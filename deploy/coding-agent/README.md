# Coding-agent runner deployment

The coding agent is the node-free **windshift-agent** (a stripped codehamr
fork, WI-204), built and published from the sibling `windshift-agent` repo as
`ghcr.io/windshiftapp/windshift-agent`. The orchestrator
(`internal/services/agent_runner.go`) spawns one ephemeral container per run via
`docker run`, assembling a hardened argv around that image.

## What this directory builds

`Dockerfile` here builds only the thin **ws-carrier** image
(`ghcr.io/windshiftapp/ws-carrier`): it cross-compiles the `ws` CLI
(which lives in this repo) and ships it at `/usr/local/bin/ws`. The
windshift-agent image lifts `ws` from it via its `WS_IMAGE` build arg. It no
longer bakes the retired Node agent, Node/npm, the `windshift-guard` extension,
or an RPC entrypoint — the agent owns its own entrypoint and tool sandbox now.

For local development, build the ws-carrier with either:

```bash
make coding-agent-image
# or
docker build -f deploy/coding-agent/Dockerfile -t windshift/ws-carrier:local .
```

…then build the agent from the sibling repo, lifting `ws` from it:

```bash
cd ../windshift-agent && make image WS_IMAGE=windshift/ws-carrier:local
```

## Hardening baked into every run

`DockerAgentRunner.buildDockerArgs` (in `internal/services/agent_runner.go`)
emits these flags for every spawn, regardless of which agent image is
configured. They are **not** configurable from outside the file —
operator-tunable knobs (network, CPU/memory/pids budgets) are separate fields:

| Flag                             | Purpose                                                                |
|----------------------------------|------------------------------------------------------------------------|
| `--cap-drop=ALL`                 | Container starts with no Linux capabilities.                           |
| `--security-opt=no-new-privileges` | Prevents the container from gaining additional capabilities mid-run. |
| `--user=1000:1000`               | Runs as the unprivileged agent user pinned in the agent image.         |
| `--read-only`                    | Root filesystem is read-only. Writable paths come from tmpfs mounts.   |
| `--tmpfs=/tmp` / `/home/agent`   | Per-run writable scratch space; size-capped, `nosuid,nodev`.           |

## Operator-tunable knobs

Set these env vars before starting the windshift server. They layer
**on top of** the hardened defaults — there is no opt-out for the
flags in the table above.

| Env var                       | Default              | Effect                                                   |
|-------------------------------|----------------------|----------------------------------------------------------|
| `CODING_AGENT_RUNNER_IMAGE`   | (unset → disabled)   | Required to enable the harness at all, e.g. `ghcr.io/windshiftapp/windshift-agent:latest`. |
| `CODING_AGENT_DOCKER_BINARY`  | `docker`             | Path to the docker CLI to invoke.                        |
| `CODING_AGENT_WORKTREE_ROOT`  | (required)           | Host directory where per-run worktrees are created. If Windshift itself runs in Docker while using the host docker socket, mount this same absolute host path into the Windshift container. |
| `CODING_AGENT_WS_API_URL`     | `BASE_URL`           | URL the runner container uses for the `ws` CLI and the run-scoped `llm-proxy`. Override when `BASE_URL` is browser-facing (for example `localhost`) but not reachable from containers. Must end in `/api`. |
| `CODING_AGENT_LLM_MODEL`      | (unset)              | Fallback model id when a binding carries no `llm_connection_id`. |
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

### Simplest: an internal network (Windshift in docker on the same host)

Agents only need to reach the Windshift API host (LLM and git are brokered
through the orchestrator's proxies). When Windshift itself runs in docker on
the same host, create the network with `--internal` and attach Windshift to
it — Docker's isolation then IS the egress policy and no firewall rules are
needed at all:

```bash
docker network create --internal coding-agent-egress
docker network connect coding-agent-egress windshift
```

Point agents at the in-network address (`CODING_AGENT_WS_API_URL=http://windshift:8080/api`)
so the broker URLs handed to them resolve on that network. The deploy compose
file (`deploy/docker-compose.yml`) ships this wired up, including a co-located
`windshift-runner` service — see its commented blocks.

### firewalld / iptables (Windshift reachable only via a public URL)

On firewalld hosts (Fedora/RHEL) use the bundled helper — it encodes the
allowlist as a permanent firewalld policy keyed on the network's source
subnet, so it survives reboots and docker recreating the bridge (WI-315):

```bash
sudo ./deploy/coding-agent/egress-firewalld.sh --allow windshift.example.com
```

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

> Note: because the agent reaches the model through the orchestrator's
> `llm-proxy` and git through the `git-proxy`, the egress allowlist for the
> agent network only needs to reach the Windshift API host itself, not the LLM
> or SCM providers directly.

### Opting out (NOT recommended)

If you knowingly want the container to inherit host egress, set the env
var explicitly so the choice is loud in your config:

```bash
CODING_AGENT_NETWORK=bridge
```

Without this override the orchestrator will try to launch on
`coding-agent-egress`; docker fails fast if the network doesn't exist.
