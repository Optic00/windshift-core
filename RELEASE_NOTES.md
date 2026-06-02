# Windshift v0.7.3 — "Shiplaunch"

Windshift 0.7.3 is a focused corrective release for the coding-agent harness introduced in 0.7.2.

## Highlights

### Coding-agent runner now actually runs

The coding-agent container entrypoint now prepares the per-run environment and execs `pi --mode rpc` instead of emitting placeholder lifecycle events and exiting successfully. Runs now stream real pi RPC output back through the existing agent-run event pipeline.

### Runner image is now shipped

CI now builds and publishes the pi-based runner image as:

```text
ghcr.io/windshiftapp/coding-agent-runner
```

For local development, the image can be built with:

```bash
make coding-agent-image
```

or via the Docker Compose `coding-agent-image` profile.

### Agent runs receive the Windshift context they need

Run containers now get the environment required for the bundled `ws` CLI and the initial coding-agent prompt:

- `WS_API_URL`
- `WS_WORKSPACE_KEY`
- `WS_WORKSPACE_ID`
- `WINDSHIFT_ITEM_ID` / `WINDSHIFT_ITEM_KEY`
- `WINDSHIFT_ITEM_DB_ID`
- `WS_ITEM_NUMBER`

`CODING_AGENT_WS_API_URL` was added for deployments where browser-facing `BASE_URL` is not reachable from runner containers.

### Per-binding AI connections are honored

Workspace agent bindings now resolve their configured `llm_connection_id` at run time and inject the selected provider, model, API key, and optional base URL into the runner. Custom/local endpoints are translated into a per-run pi `models.json`; built-in providers use pi auth config without putting API keys on the Docker command line.

### Git push auth works for agent branches

The runner now receives SCM credentials through Docker's env-file path and turns them into a per-container `GIT_ASKPASS` helper. Agents can push their run branch without embedding credentials in `.git/config` or process arguments, allowing the existing post-run PR creation hook to find the branch it expects.

## Bug fixes

- Rejected unknown AI connection provider types at create/update time instead of saving connections that resolve to a noop client at runtime.
- Documented production deployment requirements for coding agents, including Docker socket access and same-path worktree mounts when Windshift itself runs in Docker.
- Updated the runner Dockerfile comments and deployment docs to reflect that the image is no longer a skeleton.

## Upgrade notes

- Set `CODING_AGENT_RUNNER_IMAGE=ghcr.io/windshiftapp/coding-agent-runner:<tag>` to enable the shipped runner image.
 `CODING_AGENT_WORKTREE_ROOT` must be a host path Docker can bind-mount into runner containers. If Windshift runs in Docker using the host Docker socket, mount that same absolute host path into the Windshift container.
- Set `CODING_AGENT_WS_API_URL` when containers cannot reach `BASE_URL` directly, for example when `BASE_URL` is `http://localhost:...` from a browser perspective.
