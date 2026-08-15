# windshift-runner — host setup

`windshift-runner` is the worker that executes coding-agent jobs on a host you
control. It self-registers with the orchestrator, then loops: **claim** a queued
run → **prepare** its source → **run** the agent in a throwaway container →
**push** the result branch → **report**. Many hosts can join the same pool; the
orchestrator load-balances claims across them.

This directory provides an installer (`install.sh`), a systemd unit, and an env
template for standing up a runner host.

> **Fastest path — hosted install script (WI-313).** The orchestrator serves a
> container-flavor installer at `GET <WS_API_URL>/runner-install.sh` with its
> own public URL and version-matched image tags baked in; the mint-token dialog
> in Admin → Runner Pools emits the complete one-liner:
>
> ```bash
> curl -fsSL https://windshift.example.com/api/runner-install.sh | sudo bash -s -- --token wsrt_...
> ```
>
> It handles SELinux (`label=disable`), data dirs, the env file, and waits for
> registration. Use the systemd flavor below when you prefer host binaries
> over the runner container.

## How it fits together

```
orchestrator (windshift core)            runner host
  RunService, control plane                windshift-runner  (this)
  brokers: git-proxy . llm-proxy            ├─ execs windshift-triage  (git: clone/push)
        ▲  claim / report / heartbeat       ├─ docker run ─► agent container
        │  broker calls (HTTPS)             │                 windshift-agent (edits code)
        └───────────────────────────────────┘                 git . ws CLI . no creds
```

Three trust tiers: the **orchestrator** is trusted; the **runner host** is
untrusted-but-credential-light (it holds a per-instance credential and per-run
tokens, never raw SCM/LLM secrets); the **agent container** is fully untrusted
(no credentials, sandboxed by docker). git reads and the final push flow through
the orchestrator's **git-proxy**, which injects the real SCM credential
server-side and gates the push to the run's single granted ref.

## Prerequisites

- A Linux host with **systemd**.
- **Docker Engine** — the runner spawns one container per job. (The runner's
  user needs docker access, which is **root-equivalent** on the host; isolate
  runner hosts accordingly.)
- **git** on `PATH` — `windshift-triage` shells out to it for clone/fetch/push.
- **Outbound HTTPS** to your orchestrator's `WS_API_URL`.
- The **agent image** (`WSRUNNER_IMAGE`) must be pullable by the local daemon.
- A **pool registration token** (`wsrt_…`) from a workspace admin (see below).

The runner and triage binaries are static Go binaries; the host needs no Go
toolchain unless you build with `--core`.

## Get a registration token

A workspace admin mints a pool registration token in Windshift (the runner-pool
admin surface). It is a one-time bootstrap secret: on first start the runner
exchanges it for a per-instance credential and heartbeats with that thereafter.
One token can register many instances into its pool.

## Install

Build the binaries on a build machine (or in CI):

```bash
cd core
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o dist/windshift-runner  ./windshift-runner
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o dist/windshift-triage ./cmd/windshift-triage
```

Then on the runner host, either copy this `deploy/windshift-runner/` directory
plus the two binaries and run:

```bash
# prebuilt binaries sitting next to install.sh (or pass --bin-dir DIR)
sudo ./install.sh --bin-dir /path/to/dist

# …or build from a core checkout present on the host (needs Go):
sudo ./install.sh --core /path/to/core
```

`install.sh` checks docker/git, installs both binaries to `/usr/local/bin`,
creates the `windshift-runner` system user (added to the `docker` group),
creates the cache dir, and installs the env file + systemd unit. It is
idempotent — re-run it to upgrade binaries; it never overwrites an existing
`runner.env`.

Then configure and start:

```bash
sudo "$EDITOR" /etc/windshift-runner/runner.env   # set WS_API_URL, token, image
docker pull <WSRUNNER_IMAGE>
sudo systemctl start windshift-runner
journalctl -u windshift-runner -f                 # watch it register + claim
```

### Run as a container instead

You can skip the install script and run the runner as a container — it drives
the host Docker daemon through the mounted socket (Docker-out-of-Docker). See
`runner-compose.yml` and `Dockerfile` in this directory. Key points:

- Mount the Docker socket: `/var/run/docker.sock:/var/run/docker.sock`.
- Mount the cache root at the **same path** on host and container
  (`/var/lib/windshift-runner:/var/lib/windshift-runner`) — the host daemon
  bind-mounts each prepared checkout into the agent container, so the path must
  resolve identically on the host.
- Podman: works with the rootful socket (`/run/podman/podman.sock`); rootless
  Podman needs UID-mapping care on the checkout mount.

```bash
docker compose -f runner-compose.yml up -d
```

### Same host as Windshift — one compose file, no firewall rules

When the runner lives on the same host as the orchestrator, skip the separate
compose file and the egress-firewall scripts entirely: `deploy/docker-compose.yml`
ships a commented `windshift-runner` service plus a shared `coding-agent-egress`
network declared with `internal: true`. Windshift joins that network, the runner
and every spawned agent container live on it, and Docker's own isolation does
the egress filtering — containers on an internal network can reach each other
(i.e. Windshift) but nothing else. No firewalld policy, no iptables rules, no
re-resolving the API host's IP when it changes.

Differences from the standalone setup:

- `WS_API_URL: http://windshift:8080/api` with `WSRUNNER_ALLOW_INSECURE: "1"` —
  plaintext is acceptable only because the traffic never leaves the host's
  docker bridge.
- The orchestrator sets `CODING_AGENT_WS_API_URL=http://windshift:8080/api` so the
  per-run broker URLs (llm-proxy, git-proxy, `ws` API) handed to agent
  containers resolve on the internal network.
- Bootstrap order: start Windshift, mint the pool registration token in
  Admin → Runner Pools, put it in `.env`, then bring up the runner service.

### install.sh options

| Flag | Default | Meaning |
|------|---------|---------|
| `--core DIR` | — | build the binaries from a core checkout (needs Go) |
| `--bin-dir DIR` | script dir | use prebuilt `windshift-runner` + `windshift-triage` from DIR |
| `--prefix DIR` | `/usr/local/bin` | where to install the binaries |
| `--cache-dir DIR` | `/var/lib/windshift-runner/cache` | bare-clone cache root |
| `--user NAME` | `windshift-runner` | service user |
| `--no-enable` | — | install without enabling the unit |

## Configuration

All config is environment-only, in `/etc/windshift-runner/runner.env`
(see `runner.env.example`).

| Variable | Req | Default | Purpose |
|----------|-----|---------|---------|
| `WS_API_URL` | ✅ | — | orchestrator base URL ending in **`/api`** (e.g. `https://host/api`) — the runner control plane + brokers live there (`/api/runner/register`, `/api/git-proxy/...`). **Not** the v1 REST API (`/rest/api/v1`) and not the bare host. |
| `WSRUNNER_REGISTRATION_TOKEN` | ✅ | — | pool registration token (`wsrt_…`); exchanged for a per-instance credential on first start |
| `WSRUNNER_IMAGE` | ✅ | — | `ghcr.io/windshiftapp/windshift-agent` image to spawn per coding-agent job |
| `WSRUNNER_ALLOWED_IMAGES` | — | — | Comma-separated exact image references permitted as per-run overrides; the default image is always allowed |
| `WSRUNNER_NAME` | | hostname | runner display name |
| `WSRUNNER_DOCKER` | | `docker` | docker binary |
| `WSRUNNER_TRIAGE_BIN` | | `windshift-triage` | path to the triage binary the runner execs for git prep/push |
| `WSRUNNER_CACHE_ROOT` | | `/var/lib/windshift-runner/cache` | host-local bare-clone cache (fetch accelerator; never mounted into a container) |
| `WSRUNNER_POLL_INTERVAL` | | `2s` | claim poll interval when idle |
| `WSRUNNER_HEARTBEAT_INTERVAL` | | `30s` | lease heartbeat interval |
| `WSRUNNER_INITIAL_PROMPT` | | generic | fallback initial prompt |

## Verify

```bash
systemctl status windshift-runner
journalctl -u windshift-runner -f
```

You should see `registered as instance N in pool M` then `worker started`.
Trigger a run for the pool from Windshift and watch it claim, prepare, run, and
report.

## Security notes

- **No SCM/LLM secrets on the host.** The runner authenticates with a
  per-instance credential and per-run tokens. git and LLM access flow through
  the orchestrator's brokers; raw provider credentials never leave the
  orchestrator.
- **Push is ref-gated.** A run may push only its single granted ref
  (`agent-runs/run-<id>`), enforced by the git-proxy regardless of what the
  runner claims.
- **The agent container has no credentials** and is sandboxed (cap-drop,
  read-only rootfs, non-root uid, restricted egress).
- **docker group is root-equivalent.** Anyone who can reach this runner's docker
  socket effectively controls the host. Run runner hosts as dedicated,
  disposable machines.
- The **bare-clone cache** is host-local and never bind-mounted into a
  container; each run gets its own private object-store clone.

## Upgrading

Re-run `install.sh` with the new binaries (or `--core` against an updated
checkout), then `systemctl restart windshift-runner`. The unit drains the
in-flight job on stop (up to `TimeoutStopSec`).

## Uninstall

```bash
sudo systemctl disable --now windshift-runner
sudo rm /etc/systemd/system/windshift-runner.service /usr/local/bin/windshift-{runner,triage}
sudo rm -rf /etc/windshift-runner /var/lib/windshift-runner
sudo systemctl daemon-reload
# optionally: sudo userdel windshift-runner
```

## Troubleshooting

- **`required environment variable WS_API_URL is not set`** — edit `runner.env`.
- **Registers but jobs fail immediately** — check `WSRUNNER_IMAGE` is pulled and
  the daemon can run it; `docker run --rm <image> windshift-agent` should print a
  config error, not an exec error.
- **git prep/push errors** — confirm `git` is installed and `WSRUNNER_TRIAGE_BIN`
  points at the installed `windshift-triage`; check outbound HTTPS to the
  git-proxy under `WS_API_URL`.
- **Permission denied talking to docker** — the runner user must be in the
  `docker` group (re-run `install.sh` or `usermod -aG docker windshift-runner`,
  then restart the service).
