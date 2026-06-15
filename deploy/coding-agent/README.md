# Coding-agent runner deployment

The coding agent is the node-free **windshift-agent** (a stripped codehamr
fork, WI-204), built and published from the sibling `windshift-agent` repo as
`ghcr.io/windshiftapp/windshift-agent`. The **windshift-runner**
(`DockerAgentRunner` in `internal/services/agent_runner.go`) spawns one
ephemeral container per run via `docker run`, assembling a hardened argv around
that image.

The Windshift server itself only **orchestrates**: with
`CODING_AGENT_ENABLED=true` it queues runs and serves the runner-pool control
plane, but it never executes an agent on its own host. All execution happens on
one or more `windshift-runner` hosts that register with the pool and claim work
(there is no in-process / local-LLM-loop mode).

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

## Configuration

The two sides are configured separately:

**Windshift server (orchestration only):**

| Env var                  | Default    | Effect                                                                 |
|--------------------------|------------|-----------------------------------------------------------------------|
| `CODING_AGENT_ENABLED`   | `false`    | Enables the harness: queue runs, serve the runner pool, fire the PR hook. (Also `--enable-coding-agent`.) |
| `CODING_AGENT_WS_API_URL`| `BASE_URL` | API URL handed to runners/agents for the `ws` CLI and run-scoped `llm-proxy`. Override when `BASE_URL` is browser-facing (e.g. `localhost`) but not reachable from the runner. Must end in `/api`. |

**windshift-runner (executes the agents):** configured via `WSRUNNER_*` env —
`WS_API_URL`, `WSRUNNER_REGISTRATION_TOKEN`, `WSRUNNER_IMAGE`, `WSRUNNER_DOCKER`,
etc. See the package doc in `windshift-runner/main.go` for the full list.

The hardened `docker run` flags in the table above are baked into
`DockerAgentRunner` and applied on the runner host for every spawn; the sandbox
budgets (network, `--pids-limit`, `--memory`, `--cpus`) use the defaults below
and are not operator-overridable from outside the binary.

| Sandbox default | Value                 |
|-----------------|-----------------------|
| `--network`     | `coding-agent-egress` (see "Egress network") |
| `--pids-limit`  | `512`                 |
| `--memory`      | `4g` (also `--memory-swap`) |
| `--cpus`        | `2`                   |

## Egress network

The default `--network` value is `coding-agent-egress`, a **user-defined
bridge network the operator must create on the runner host before starting
windshift-runner**. Picking a name distinct from `bridge` is intentional: it
forces the operator to think about egress filtering rather than inheriting the
host's default outbound posture. (windshift-runner auto-creates a plain bridge
of this name on boot if it is missing, warning loudly that egress is
unfiltered.)

### Simplest: an internal network (runner + Windshift in docker on the same host)

Agents only need to reach the Windshift API host (LLM and git are brokered
through the orchestrator's proxies). When Windshift and the runner both run in
docker on the same host, create the network with `--internal` and attach both
Windshift and the runner to it — Docker's isolation then IS the egress policy
and no firewall rules are needed at all:

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

### The network name is fixed

The sandbox network name (`coding-agent-egress`) is a baked default in
`DockerAgentRunner` — there is no env opt-out. windshift-runner will launch
agent containers on `coding-agent-egress`, auto-creating it as a plain
(unfiltered) bridge if it does not already exist. To enforce egress filtering,
pre-create the network with the firewall/internal-network setup above so the
runner attaches to your hardened network instead of a fresh open one.
