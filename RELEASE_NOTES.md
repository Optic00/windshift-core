# Windshift v0.7.3 — "Shiplaunch"

Windshift 0.7.3 turns the coding-agent harness introduced in 0.7.2 from a single-host feature into a distributed execution substrate. Agent runs can now be claimed and executed by **remote runner pools**, and the credentials a run needs — SCM tokens, LLM keys, named secrets — are **brokered server-side** so they never touch a runner host. The local in-process path still works exactly as before; it is now just one runner against the same protocol every remote runner speaks.

## Highlights

### Remote runner pools

`agent_runs` is now the queue. A run carries a target pool (`NULL` = the local in-process pool), and a new **`runner_pool`** capability type describes each remote pool, including its max concurrency and whether it is ephemeral. Pool eligibility reuses the existing capability workspace-scoping, so dispatch is gated the same way as every other capability.

A standalone **`windshift-runner`** binary self-registers with a pool registration token, exchanges it for a per-instance credential, then runs the shared claim → execute → report loop over HTTPS: claim a queued run for its pool, run it, stream events and the result back. It heartbeats on an interval and drains in-flight work on `SIGINT`/`SIGTERM`.

Local and remote runs share one execution path — `RunService` is simply the in-process transport for the same `OrchestratorClient` protocol (`Claim`/`Emit`/`Report`/`Heartbeat`) that the HTTPS client implements.

### Autoscaling signals, quotas, and self-healing

- **Per-pool concurrency quota** — `Claim` refuses work when a pool is at its configured `MaxConcurrentRuns` (soft back-pressure cap).
- **Queue-depth + remote cancel** — each heartbeat returns the pool's queue depth (the autoscaling signal) and the list of in-flight runs the orchestrator wants aborted.
- **Lease reaping** — a runner that dies mid-run no longer hangs its runs in `running` forever. A reaper fails the orphaned runs and evicts the dead instance after ~3 missed heartbeats.
- **Worktree-cache eviction** — idle per-(workspace, repo) bare clones are swept on age so the runner host stays clean.

### Secretless access layer

Remote runs never receive raw credentials. At claim time the binding's repo/SCM connection and LLM connection are resolved into a **`RunGrants`** snapshot, persisted on the run and bound to a minted per-run token. Brokers authorize each request against that snapshot (deny-by-default) and inject the real credential server-side:

- **Git smart-HTTP proxy** — SCM traffic is reverse-proxied to the granted repo with the credential injected server-side. Pushes are parsed per-ref and gated against the run's single-ref grant; clone/fetch stays repo-scoped.
- **LLM proxy** — model API calls are reverse-proxied to the granted connection with the provider key injected server-side.
- **Secrets broker** — a run fetches a named `ActionCredential` it is granted; the plaintext is resolved and decrypted server-side and never lands on the runner.
- **HTTP proxy** — allow-listed outbound requests only.

### Multi-kind substrate (`container_run`)

The substrate is now job-kind aware (`coding_agent` and `action_container`), so plain container jobs ride the same rails as agent runs. A `container_run` action node with a pool dispatches the image (from its `docker_environment` capability) to a remote runner, falling back to the local container path when no pool is set.

### Remote post-run hook

Remote runs get the same post-run hook as local ones — draft PR creation plus `ItemSCMLink` writeback — fired against the run's workspace/item/binding using the runner-reported branch and base commit.

### Serving under a context path

The server can now be served under a base path, for reverse-proxy subpath deployments.

## Local coding-agent harness

The single-host path that shipped in 0.7.2 is carried forward intact:

- The runner entrypoint prepares the per-run environment and execs `pi --mode rpc`, streaming real pi RPC output through the agent-run event pipeline.
- CI builds and publishes the pi-based runner image as `ghcr.io/windshiftapp/coding-agent-runner` (locally: `make coding-agent-image`, or the Docker Compose `coding-agent-image` profile).
- Run containers receive the Windshift context the bundled `ws` CLI and the initial prompt need: `WS_API_URL`, `WS_WORKSPACE_KEY`, `WS_WORKSPACE_ID`, `WINDSHIFT_ITEM_ID`/`WINDSHIFT_ITEM_KEY`, `WINDSHIFT_ITEM_DB_ID`, `WS_ITEM_NUMBER`, plus `CODING_AGENT_WS_API_URL` for deployments where the browser-facing `BASE_URL` is not reachable from runner containers.
- Workspace agent bindings resolve their configured `llm_connection_id` at run time and inject the selected provider, model, key, and optional base URL. Custom/local endpoints become a per-run pi `models.json`; built-in providers use pi auth config without putting keys on the Docker command line.
- For local runs, SCM credentials are injected via a per-container `GIT_ASKPASS` helper (passed through Docker's env-file path), so agents can push their run branch without embedding credentials in `.git/config` or argv.

## Security hardening

Findings from the runner remote security review (WI-168):

- **Pool authorization** — `container_run` now resolves its pool id through `resolveCapability` (enabled / correct type / workspace-scoped) before dispatch, so an action can no longer target an arbitrary, disabled, or cross-workspace runner pool by number.
- **Finalize/events CAS** — run ownership now also requires `running` status, and finalization is a compare-and-swap. A runner credential can no longer inject late events, rewrite the terminal status of an already-finalized run, or re-fire the post-run hook (duplicate PR creation).
- **Git push ref-gating** — the git broker parses the receive-pack command list and enforces the per-ref push grant on every pushed ref, instead of injecting the credential after only a repo-equality check.
- **HTTP allowlist + SSRF guard** — the HTTP broker enforces a URL-boundary allowlist and guards against SSRF.

## Bug fixes

- Rejected unknown AI connection provider types at create/update time instead of saving connections that resolve to a noop client at runtime.
- Closed schema drift between fresh installs and the upgrade path so both converge on the same shape (email tracking direction, active-timer ownership, screen-field ordering, boolean defaults).
- Documented production deployment requirements for coding agents, including Docker socket access and same-path worktree mounts when Windshift itself runs in Docker; updated the runner Dockerfile and deployment docs now that the image is no longer a skeleton.

## Also in 0.7.3

- **Asset management v1 API** — REST endpoints, token scopes, and `ws asset`/`asset-set`/`asset-type` CLI for assets (`assets:delete` is opt-in only).
- **Input sanitization hardening** — decoded string fields are bounded through the consolidated `internal/sanitize` package across the cookie-auth and v1 surfaces, and mutations are surfaced back to the caller as response warnings.

## Upgrade notes

### Enabling remote runner pools

- Create a `runner_pool` capability for the pool and mint a registration token for it.
- Run the standalone runner with at least:
  - `WS_API_URL` — orchestrator base URL including the API prefix
  - `WSRUNNER_REGISTRATION_TOKEN` — the pool registration token (`wsrt_…`)
  - `WSRUNNER_IMAGE` — the coding-agent container image (jobs fail until set)
  - optional: `WSRUNNER_NAME`, `WSRUNNER_DOCKER`, `WSRUNNER_POLL_INTERVAL` (default 2s), `WSRUNNER_HEARTBEAT_INTERVAL` (default 30s), `WSRUNNER_INITIAL_PROMPT`
- The runner needs access to a Docker daemon to launch job containers.

### Local in-process runner

- Set `CODING_AGENT_RUNNER_IMAGE=ghcr.io/windshiftapp/coding-agent-runner:<tag>` to enable the shipped runner image.
- `CODING_AGENT_WORKTREE_ROOT` must be a host path Docker can bind-mount into runner containers. If Windshift runs in Docker using the host Docker socket, mount that same absolute host path into the Windshift container.
- Set `CODING_AGENT_WS_API_URL` when containers cannot reach `BASE_URL` directly (for example when `BASE_URL` is `http://localhost:…` from a browser perspective).
