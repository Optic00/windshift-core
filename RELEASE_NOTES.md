# Windshift v0.8.0 — "Shiplaunch"

Windshift 0.8.0 takes the coding agents introduced in 0.7.2 beyond a single host. Agent runs can now be executed by **remote runner pools** — fleets of machines you register with one command — while every credential a run needs (Git access, model API keys, secrets) stays on the server and is never copied to a runner. You can also shape each agent: give it knowledge with a skills library, a persona with custom instructions, and trigger it simply by @mentioning it in a comment.

## Highlights

### Run agents anywhere with runner pools

Until now, agent runs executed on the Windshift host itself. With 0.8.0 you can register pools of runner machines and Windshift dispatches work to them:

- **One-command install.** Mint a registration token and the dialog hands you a copy-paste command that installs and starts the runner on any machine with Docker. The runner registers itself, picks up work, streams progress back, and finishes cleanly when you shut it down.
- **Built for fleets.** Each pool has a concurrency limit, reports its queue depth (useful as an autoscaling signal), and runs can be cancelled remotely. If a runner machine dies mid-run, Windshift notices, marks the affected runs as failed, and removes the dead runner — nothing hangs forever.
- **Nothing changes if you don't use it.** The built-in local execution path still works out of the box; it's simply the first member of the same system.

### Your credentials never leave the server

Runner machines — and the agents running on them — never receive your actual credentials. When a run starts, Windshift works out exactly what that run is allowed to touch, and every sensitive operation goes through the server:

- Git access is proxied: the agent can fetch its repository and push its work branch, and nothing else, without ever holding a Git token.
- Model API calls are proxied with the provider key added server-side — no key ever enters the agent's container.
- Secrets and outbound web requests are brokered and allow-listed the same way.

A run's Git activity now also carries the right identity: when someone triggers an agent on a workspace connected via OAuth, the branch and draft pull request are created as **that person**, not as a shared service identity.

### Shape your agents

- **Skills library.** Each workspace can maintain a library of markdown knowledge packs and attach them to agents. Agents see a short index of their skills and fetch the full text only when relevant — so a large library doesn't slow anything down.
- **Custom instructions.** Give each agent binding a persona or extra ground rules. These are added on top of the standard operating rules, never instead of them.
- **@mention to start a run.** Mentioning a bound agent in a comment starts a run on that item — lighter than assignment, with no assignee or status change. Duplicate mentions and self-mentions are handled sensibly, and an agent already working on the item won't be started twice.

### See what your agents are doing

- A live **Agent log** tab on the work item shows the run's progress as it happens.
- Assignment pickers now show whether an agent is actually **available** to take work.
- A **runner pool health panel** in admin Diagnostics shows each pool's runners, capacity, and queue.
- Configuration problems surface early: Windshift checks an agent's Git and model access at trigger time and fails visibly with a clear error instead of starting a run that was never going to work, and warns at startup if the host is missing what local runs need.

### Safer networking out of the box

Agent containers are attached to a dedicated egress network. Windshift now creates it automatically (with a loud warning if its traffic is unfiltered), and ships a helper for setting up a firewall allowlist so agents can only reach the destinations you choose.

### More than coding agents

The same dispatch system now also runs plain **container jobs** from action automations, so scheduled or triggered containers can execute on a runner pool too.

### Subpath deployments

Windshift can now be served under a path prefix (for example `https://example.com/windshift/`) behind a reverse proxy.

## Security

This release includes fixes from two dedicated security reviews — one focused on the remote runner system, one across the whole codebase:

- Runner registration tokens are single-use, expire by default, and require HTTPS; each runner gets its own credential.
- A finished run can't be tampered with afterwards: late events and repeated finalization are rejected, and Git pushes are checked branch-by-branch against what the run was granted.
- Server-side request forgery (SSRF) protections now cover all outbound traffic, with one explicit operator switch (`--allow-local-connections`) for deployments that genuinely need to reach private addresses.
- Portal magic-link tokens are stored hashed, secrets no longer appear in the host process table, and several cross-workspace information leaks were closed.
- User input is consistently sanitized across both API surfaces, and anything the server had to clean up is reported back in the response.

## Also in 0.8.0

- **Asset management API & CLI** — assets get full REST endpoints, token scopes, CSV import, and new `ws asset` commands (deleting via API is opt-in).
- **Jira import grows** — the importer now covers Jira/Insight assets, boards and sprints, and custom fields, and can be re-run safely without creating duplicates.
- **Time tracking integrity** — one active timer per user is now enforced, worklogs require permission to view the item, and several permission gaps in projects and categories were closed.

## Bug fixes

- Agent runs that produce no commits no longer push an empty branch or open an empty pull request.
- Expiring Git OAuth tokens are refreshed automatically, so long-lived agent setups don't fail on a stale token.
- Pressing Enter now confirms the page move dialog, and notification toasts appear above modal dialogs.
- Fresh installs and upgraded databases now end up with exactly the same schema.

## Upgrade notes

### Remote runner pools

Optional — nothing to do if you only use local runs. To add a pool: create it under Admin → Capabilities, mint a registration token, and run the install command shown in the dialog on each runner machine (Docker required). The runner's only required settings are the Windshift URL, the registration token, and the agent image; polling and heartbeat intervals are tunable but have sensible defaults.

### Local agent runs

The local harness is **on by default** in 0.8.0 with a published default agent image and worktree location, so the assignment trigger fires real runs without extra configuration.

- If Windshift itself runs in Docker, the worktree directory must be a real host path mounted at the same path inside the Windshift container.
- If agent containers can't reach your public `BASE_URL` (for example a `localhost` URL), set `CODING_AGENT_WS_API_URL` to an address they can reach.
- Agent containers join the `coding-agent-egress` Docker network; filter its traffic with the bundled firewall helper, or point the network setting somewhere else to apply your own policy.
