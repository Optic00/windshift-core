# Windshift v0.7.3 — "Shiplaunch"

Windshift 0.7.3 turns the coding-agent harness introduced in 0.7.2 from a single-host feature into a distributed execution substrate. Agent runs can now be claimed and executed by **remote runner pools**, and the credentials a run needs — SCM tokens, LLM keys, named secrets — are **brokered server-side** so they never touch a runner host. The local in-process path still works exactly as before; it is now just one runner against the same protocol every remote runner speaks.

Alongside the substrate, the agent surface itself grew authoring controls: admins can shape **what an agent knows** (a per-workspace skills library), **who it is** (per-binding instructions), and **how it is triggered** (an @mention in a comment now starts a run).

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

**Per-user SCM identity (WI-275).** A run's code operations — clone, branch push, draft-PR creation — now ride the **triggering user's own credential** rather than the workspace connection's. On OAuth connections that means the user who fired the trigger (the assigner, the commenter, or the admin who started a test run), persisted on `agent_runs.triggered_by_user_id`; PAT and GitHub App connections keep using the impersonal connection credential.

### Shaping agents: skills, instructions, and @mention triggers

Admins can now tailor what a bound agent knows, who it is, and what sets it off — all per binding:

- **Skills library (WI-258)** — a per-workspace library of markdown knowledge packs (Anthropic "Agent Skills" shape) attached to bindings many-to-many. Delivery is progressive disclosure through the `ws` CLI: a run's initial prompt only **indexes** its attached, enabled skills (name + description + id), and the agent reads a body with `ws skill get <id>` when it is actually relevant — so skill bodies cost no context until needed. A new `agent-skills:read` scope is in the default run-scope set and is auto-appended when a binding has skills attached.
- **Per-binding instructions (WI-258)** — a persona appended to the run's standard prompt as a `## Your role` section (capped at 8 KiB). It augments, never replaces, the operational prompt, so the commit / comment / no-push rules survive any persona.
- **@mention starts a run (WI-264)** — @mentioning a bound agent's acting user in a comment enqueues a run on that item. It is a lighter trigger than assignment: no assignee change, no status change. Self-mentions are skipped, repeated mentions of the same agent in one comment dedup, and an agent already queued or running on the item is not stacked with a second run (later comments re-trigger once the run ends).

### Multi-kind substrate (`container_run`)

The substrate is now job-kind aware (`coding_agent` and `action_container`), so plain container jobs ride the same rails as agent runs. A `container_run` action node with a pool dispatches the image (from its `docker_environment` capability) to a remote runner, falling back to the local container path when no pool is set.

### Remote post-run hook

Remote runs get the same post-run hook as local ones — draft PR creation plus `ItemSCMLink` writeback — fired against the run's workspace/item/binding using the runner-reported branch and base commit.

### Serving under a context path

The server can now be served under a base path, for reverse-proxy subpath deployments.

## Local coding-agent harness

The single-host path that shipped in 0.7.2 is carried forward — and has converged onto the same secretless model as remote runs: no provider key and no SCM credential ever enters the run container.

- The runner entrypoint prepares the per-run environment and drives the **windshift-agent** binary over the `AgentRunner` JSONL RPC contract: start the subprocess, write the initial prompt as one JSONL command on stdin, and stream the agent's JSONL events through the agent-run event pipeline.
- The agent is the **windshift-agent** image (a node-free coding-agent harness, WI-204), published by CI as `ghcr.io/windshiftapp/windshift-agent`. The in-process runner spawns it out of the box — `CODING_AGENT_RUNNER_IMAGE` and `CODING_AGENT_WORKTREE_ROOT` both ship with defaults — so the assignee-change trigger fires real runs without extra wiring.
- Run containers receive the Windshift context the bundled `ws` CLI and the initial prompt need: `WS_API_URL`, `WS_WORKSPACE_KEY`, `WS_WORKSPACE_ID`, `WINDSHIFT_ITEM_ID`/`WINDSHIFT_ITEM_KEY`, `WINDSHIFT_ITEM_DB_ID`, `WS_ITEM_NUMBER`, plus `CODING_AGENT_WS_API_URL` for deployments where the browser-facing `BASE_URL` is not reachable from runner containers.
- **The model is reached only through the run-scoped `llm-proxy` broker.** No provider, key, or model selection is injected into the container; it needs only an `LLM_BASE_URL` (the per-run proxy) and a model id. Local runs now use the same broker the remote path does, instead of writing provider keys into the container.
- **SCM credentials never enter the container.** The run branch is pushed **host-side** (WI-238) using the triggering user's resolved credential, so the agent can publish its work without ever holding a token.

## Security hardening

### Runner remote security review (WI-168)

- **Pool authorization** — `container_run` now resolves its pool id through `resolveCapability` (enabled / correct type / workspace-scoped) before dispatch, so an action can no longer target an arbitrary, disabled, or cross-workspace runner pool by number.
- **Finalize/events CAS** — run ownership now also requires `running` status, and finalization is a compare-and-swap. A runner credential can no longer inject late events, rewrite the terminal status of an already-finalized run, or re-fire the post-run hook (duplicate PR creation).
- **Git push ref-gating** — the git broker parses the receive-pack command list and enforces the per-ref push grant on every pushed ref, instead of injecting the credential after only a repo-equality check.
- **HTTP allowlist + SSRF guard** — the HTTP broker enforces a URL-boundary allowlist and guards against SSRF.

### Full-codebase security review (2026-06-09)

- **Outbound SSRF, globally** — admin-configured outbound clients are routed through the SSRF-safe dialer, and outbound egress is now governed by a single global switch with an explicit escape hatch for operators who need loopback/private-range egress (the `--allow-local-connections` flag, or the `ALLOW_LOCAL_CONNECTIONS` env var).
- **Magic-link tokens hashed at rest** — portal magic-link tokens are now stored hashed, not in plaintext.
- **Container capability env via `--env-file`** — capability environment is passed to action containers through a `0600` env-file instead of `-e` argv, keeping secrets out of the process table.
- **Plugin asset hardening** — plugin asset response headers are tightened and a latent `{@html}` sink was removed.
- **Cross-workspace leak** — asset link listing no longer leaks titles of linked entities in workspaces the caller cannot see.
- **Dependency** — bumped `golang.org/x/image` to v0.41.0 (GO-2026-5031/5032).

## Bug fixes

- Runs now use the triggering user's SCM credential on OAuth connections, and `GetCredentialsForUser` honors workspace-level PATs (WI-274, WI-275).
- SCM OAuth tokens are refreshed inside credential resolution so a long-lived binding doesn't fail on an expired access token (WI-274).
- Rejected unknown AI connection provider types at create/update time instead of saving connections that resolve to a noop client at runtime.
- Closed schema drift between fresh installs and the upgrade path so both converge on the same shape (email tracking direction, active-timer ownership, screen-field ordering, boolean defaults).
- Documented production deployment requirements for coding agents, including Docker socket access and same-path worktree mounts when Windshift itself runs in Docker; updated the runner Dockerfile and deployment docs now that the image is a real windshift-agent build.

## Also in 0.7.3

- **Asset management v1 API** — REST endpoints, token scopes, and `ws asset`/`asset-set`/`asset-type` CLI for assets (`assets:delete` is opt-in only).
- **Jira import: assets & more (WI-225)** — importer now covers Jira/Insight assets, boards and sprints, and custom fields, with idempotent re-import.
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

The local harness is **on by default** in 0.7.3: `CODING_AGENT_RUNNER_IMAGE` defaults to `ghcr.io/windshiftapp/windshift-agent:latest` and `CODING_AGENT_WORKTREE_ROOT` defaults to `/data/worktrees`, so the assignee-change trigger fires real runs out of the box.

- Override `CODING_AGENT_RUNNER_IMAGE` to pin a custom agent build, e.g. `ghcr.io/windshiftapp/windshift-agent:<tag>`.
- `CODING_AGENT_WORKTREE_ROOT` must be a host path Docker can bind-mount into runner containers. If Windshift runs in Docker using the host Docker socket, mount that same absolute host path into the Windshift container.
- Set `CODING_AGENT_WS_API_URL` when containers cannot reach `BASE_URL` directly (for example when `BASE_URL` is `http://localhost:…` from a browser perspective).
- Run containers are attached to the `coding-agent-egress` Docker network by default (operator-created, egress-filtered); override with the `Network` knob for a different egress policy.
