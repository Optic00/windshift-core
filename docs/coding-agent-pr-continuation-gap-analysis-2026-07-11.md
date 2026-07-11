# Coding-agent PR continuation and review-response gap analysis

Date: 2026-07-11

## Implementation update

The remediation described below was implemented for milestone `0.8.2` under WI-587/WI-588:

- WI-592: durable `agent_pr_review_events` inbox/outbox, repository-level commenter authorization, live first-sight protection, bounded retry, and webhook/poll admission deduplication;
- WI-591: marker-protected acknowledgements and terminal replies for changed, no-change, failed, canceled, rejected, and deferred outcomes;
- WI-589: per-user OAuth polling for agent-owned PRs, stable `agent_pr_ownerships`, compatible-repository selection, workspace isolation, and exact fork-head refusal;
- WI-590: normalized top-level comments, submitted reviews, inline comments, and thread replies with structured path/line/thread prompt context for GitHub and Gitea/Codeberg.

The five-minute poll remains the active deployment transport because Windshift has no configured inbound SCM webhook endpoint. `SyncService.IngestPRReviewEvent` is the common webhook/poll admission seam, so a future authenticated provider webhook adapter will deduplicate through the same durable event key without changing execution semantics.

### Provider/event matrix for 0.8.2

| Provider | Top-level PR comment | Submitted review body | Inline review/thread comment | Authorization |
|---|---:|---:|---:|---|
| GitHub | Yes | Yes | Yes | author association, mapped workspace identity, or collaborator permission |
| Gitea/Codeberg | Yes | Yes, where provider API supports reviews | Yes, where provider API supports reviews | mapped workspace identity or collaborator permission |

All supported trigger surfaces require the explicit whole-word token `@agent`. Review-surface API failure degrades to top-level conversation polling rather than disabling the existing trigger.

## Executive conclusion

The continuation mechanism exists and is wired through the production server, but the complete product promise is not yet in place.

- **Windshift item comment continuation:** substantially implemented. A new comment that mentions a bound coding-agent user starts another run, resolves an open linked PR, checks out that PR's head branch, pushes back to it, and avoids opening a duplicate PR.
- **Manual Re-run continuation:** implemented through the same open-PR resolver.
- **PR comment response:** partially implemented. A new top-level PR conversation comment containing the literal token `@agent` can start a continuation run, and a successful run that pushes commits posts a progress comment.
- **General review-loop behavior:** not complete. OAuth-backed repositories are excluded from the background poller; inline review comments and submitted reviews are not read; first-seen and concurrent comments can be dropped; unauthorized SCM users can trigger runs; and failed, skipped, or no-change runs do not reply.

The correct readiness assessment is therefore **partial / not production-complete**. The happy path works for a PAT or GitHub App repository after the PR cursor has been initialized, but the system does not yet guarantee that an eligible review request receives either a fix or an explanatory response.

## Intended behavior and observed status

| Scenario | Expected outcome | Current status |
|---|---|---|
| Assign an item to a coding agent for the first time | New run, branch, and PR | Implemented |
| Mention the same agent in a new Windshift item comment while its PR is open | New run on the existing PR head branch | Implemented with qualifications |
| Click Re-run while the PR is open | New run on the existing PR head branch | Implemented |
| Add a top-level PR comment with `@agent` | Continue the agent's existing PR | Partially implemented |
| Add a normal PR comment without `@agent` | Agent reacts automatically | Not implemented by design |
| Add an inline code-review comment with `@agent` | Agent fixes or replies | Not implemented |
| Submit a review body with `@agent` | Agent fixes or replies | Not implemented |
| Agent succeeds and pushes commits | Existing PR grows and receives a progress comment | Implemented |
| Agent decides no code change is needed | Agent explains that in the PR | Not implemented |
| Agent run fails or cannot start | PR receives an error/explanation | Not implemented |
| Agent is busy or over budget | PR receives an acknowledgement or deferral | Not implemented |
| Repository uses per-user OAuth credentials | Item mention and PR comment continuation work | Not reliably implemented |

## What is implemented

### 1. Windshift comment mentions start a bound agent run

`MaybeStartRunsForMentions` resolves mentioned users to workspace agent bindings, deduplicates active runs for the same binding/item, persists the comment as the run instruction, and dispatches a run. It is explicitly limited to comment creation; editing an existing comment does not trigger it.

Evidence: `internal/services/binding_service.go:939-1007`.

### 2. Item mentions resolve and persist a continuation target

Before starting a mention-triggered run, `applyContinuation` asks for an open PR linked to the item. If the PR repository is among the binding's repositories, it stores the PR number, repository slug, and head branch in `agent_runs.trigger_json`.

Evidence:

- `internal/services/binding_service.go:998-1003`
- `internal/services/binding_service.go:1072-1104`
- `internal/models/agent_run.go:104-133`

The production server wires `itemPRContinuationResolver` into the binding service. The resolver chooses the most recently updated open PR link and verifies its state and head branch against the SCM provider.

Evidence: `internal/server/server.go:777-790` and `internal/server/server.go:2279-2345`.

### 3. Remote execution checks out and pushes the existing branch

The continuation branch is carried into the runner job specification and into the per-repository git grant. Repository preparation fetches the existing branch, checks it out under the same name, and therefore pushes commits back to that PR branch rather than to `agent-runs/run-{id}`.

Evidence:

- `internal/services/binding_service.go:1263-1268` and `internal/services/binding_service.go:1590-1601`
- `internal/services/run_service.go:855-912`
- `internal/repoprep/repoprep.go:182-227`

### 4. A continuation does not open a duplicate PR

The post-run hook recognizes the persisted continuation trigger. For the continued repository, it skips PR creation and posts a progress comment instead. Other changed repositories in a multi-repository run may still receive new PRs.

Evidence: `internal/services/agent_pr_service.go:122-165` and `internal/services/agent_pr_service.go:376-418`.

### 5. Top-level PR comments have a polling trigger

The SCM repository sync polls issue/PR conversation comments on open linked PRs. It recognizes the case-insensitive whole-word token `@agent`, ignores agent-marked comments to prevent loops, resolves the most recently active binding for the linked item, and starts a continuation on the PR head branch.

Evidence:

- Wiring: `internal/server/server.go:969-981`
- Polling: `internal/scm/sync.go:510-605`
- Binding selection and run start: `internal/services/binding_service.go:1339-1415`
- GitHub and Gitea top-level issue-comment APIs: `internal/scm/github.go:917-975` and `internal/scm/gitea.go:374-439`

The poller runs as part of the five-minute repository sync, not from a webhook.

Evidence: `internal/server/server.go:1956-1974`.

## Detailed gaps

### P0 — OAuth-backed repositories are outside the PR-comment poller

The repository-sync query explicitly excludes every provider whose `auth_method` is `oauth` (`internal/scm/sync.go:184-197`). The `@agent` PR-comment poller only runs inside that repository-sync path. Consequently, top-level PR comments on OAuth-backed repositories are never discovered by the scheduled poller.

This matters because coding-agent git and PR operations explicitly support per-user OAuth credentials. The implementation can create and update a PR through a user's credential while the background component responsible for hearing review comments never visits that repository.

The Windshift-item mention path has a related weakness: `itemPRContinuationResolver` reads PR details with `GetCredentialsByConnectionID`, not the mentioning user's credentials (`internal/server/server.go:2314-2333`). A per-user-only OAuth connection can therefore fail resolution; `applyContinuation` logs the error and deliberately starts a fresh run, which can create the duplicate PR this feature is intended to prevent.

Recommendation: make continuation resolution user-aware and provide an OAuth-capable comment-ingestion path. For polling, select a stable authorized principal (prefer an installation/service credential; otherwise the PR-owning run's triggering user) and refresh that user's OAuth token.

### P0 — Any SCM commenter can spend agent budget and cause pushes

The provider returns comment author information, but `pollPRCommentTriggers` passes only the comment ID and body downstream. There is no collaborator-permission check, workspace membership check, allowlist, or association check before a run starts (`internal/scm/sync.go:568-602`). The run then reuses the previous Windshift triggering user's credentials (`internal/services/binding_service.go:1367-1407`).

On a public repository, an arbitrary external commenter can write `@agent`, consume LLM/runner budget, and cause new commits to be pushed using another user's authorized SCM principal. `MaxRunsPerDay` is only a coarse optional cap and does not establish authorization.

Recommendation: carry provider author ID/login and author association through the normalized event. Require an explicit policy such as repository write/triage permission, mapped workspace membership, or a configured trusted-commenter allowlist before accepting the trigger. Record denied attempts without exposing sensitive details.

### P0 — The “fix or comment back” contract is not satisfied

`AgentPRService.AfterRun` exits immediately when the run is not successful and also exits when no pushed branch was reported (`internal/services/agent_pr_service.go:122-139`). A PR reply is posted only after a successful continuation that delivered a branch (`internal/services/agent_pr_service.go:156-162`, `internal/services/agent_pr_service.go:376-396`). Therefore these outcomes are silent on the PR:

- the agent concludes no change is needed;
- the runner or model fails;
- repository preparation or credential resolution fails;
- the run is canceled or killed;
- the binding is missing or no longer covers the repository;
- another run is active;
- the daily budget is exhausted;
- posting the success comment itself fails.

Some start failures are persisted in the Windshift agent-run log, but a reviewer working in the PR receives no response. This is the most direct mismatch with the requested behavior.

Recommendation: model PR-trigger processing as a request with a terminal response. Post an immediate acknowledgement containing the run reference, then post exactly one terminal result: commits pushed, no changes needed, failed with a safe reason, or rejected/deferred. Retrying a failed reply should be durable rather than log-only.

### P1 — First-seen comments are deliberately dropped

When a PR has no `pr_comment_cursors` row, the poller moves the cursor to the newest comment and fires nothing (`internal/scm/sync.go:555-566`). This prevents historical replay, but creates a live race:

1. the agent opens a PR;
2. a reviewer writes `@agent` before the first five-minute poll;
3. the first poll establishes the baseline and silently discards that request.

The same loss occurs after enabling the feature for an existing repository or if the cursor table is rebuilt.

Recommendation: initialize the cursor when Windshift creates or first links the PR, using a known creation-time boundary, or persist eligible comments as inbox events before advancing discovery state. Do not use “first sight” as both migration safety and live delivery semantics.

### P1 — The high-water cursor is not a durable processing ledger

After examining all new comments, the poller advances directly to the maximum comment ID even when a trigger was not started or returned an error (`internal/scm/sync.go:568-605`). This loses requests in several cases:

- two `@agent` comments arrive in one polling interval: the first starts a run, the second sees the active-run guard and is then permanently passed by the cursor;
- run start fails transiently;
- no binding is found at that moment;
- the binding is temporarily over budget or unavailable;
- the head branch cannot be resolved.

Conversely, starting a run and crashing before cursor persistence can run the same request twice after the first run becomes terminal. `RunTrigger.ContinueCommentID` is persisted, but no code queries it or enforces uniqueness; it is audit metadata, not actual idempotency.

Recommendation: add a durable trigger inbox/ledger with a uniqueness key such as `(workspace_repository_id, pr_number, provider_comment_id, event_kind)`. Give each row explicit `pending`, `running`, `succeeded`, `failed`, `ignored`, and `replied` states. The discovery cursor may advance after inbox insertion; processing and retries then operate from the ledger.

### P1 — Only top-level conversation comments are supported

Both providers use issue-comment endpoints. The implementation does not read:

- GitHub pull-request review comments (inline code comments);
- GitHub submitted review bodies;
- equivalent Gitea/Codeberg review and inline-comment surfaces;
- review-thread replies unless the provider also mirrors them as issue comments.

This means the normal code-review workflow is outside the trigger even if the reviewer writes `@agent`. The feature should be described narrowly as “top-level PR conversation `@agent` comments” until those event types exist.

Recommendation: define a provider-neutral review event interface and ingest issue comments, review submissions, inline review comments, and thread replies. Include file/path/line/thread context in the agent instruction.

### P1 — The agent identity is inferred indirectly

PR comments use “the binding from the most recent run on the linked item,” not the binding or run that created the PR (`internal/services/binding_service.go:1351-1384`). If multiple agents or multiple PRs have touched one item, an unrelated later run can change which agent receives a PR request. A PR linked to multiple items further depends on item iteration order and the first starter that returns `started=true`.

Recommendation: persist explicit PR ownership at creation/link time: originating `agent_run_id`, `binding_id`, triggering principal, repo, PR number, and head repository/ref. Resolve PR comments through that record, with a documented fallback only for legacy links.

### P1 — Test coverage proves components, not the complete contract

The private overlay tests cover:

- first-sight baseline, one new token comment, loop-marker suppression, and non-token comments;
- mention triggering and basic active-run dedup;
- direct `applyContinuation` field setting;
- manual Re-run consulting the continuation resolver;
- repository preparation from an existing branch;
- fresh-run PR creation and error retries.

There are no focused tests for `StartPRCommentContinuation`, continuation-result PR comments, failed/no-change replies, OAuth polling, author authorization, multiple comments in one poll, start-error retry, crash idempotency, explicit PR-to-binding ownership, inline reviews, or the full server-to-runner-to-push-to-reply path.

The existing poller fake always returns `started=true`, so it cannot reveal the cursor-loss behavior when the binding service returns `false`.

Recommendation: add unit tests for every state transition in the proposed inbox and integration tests for PAT, GitHub App, and per-user OAuth. Add provider contract tests for each supported comment/review surface and an end-to-end test that verifies the second run pushes to the original PR without creating a new one.

### P2 — Open-PR selection can choose the wrong candidate

The item mention resolver selects the single most recently updated open PR before considering which repositories the mentioned binding can write (`internal/server/server.go:2291-2299`). If the newest PR is from an unbound repository but an older open PR belongs to the agent's bound repository, `applyContinuation` rejects the newest candidate and starts fresh instead of selecting the older compatible PR.

Recommendation: pass the binding's allowed repository slugs into resolution and filter in the query. If more than one compatible open PR remains, use explicit PR ownership or return an ambiguity that is visible to the user rather than silently choosing.

### P2 — Latency and observability are weak

PR comments are noticed only during a five-minute poll, with no immediate first sync (`internal/server/server.go:1959-1966`). A four-minute global sync deadline, provider pagination, and the recent-PR cap can add further delay. Most skip/error cases are log-only, and there is no user-facing status connecting an SCM comment ID to an agent run and final reply.

Recommendation: prefer webhooks where deployability permits and retain polling as reconciliation. Expose comment-trigger state in the agent run log and metrics: discovered, authorized, queued, deduplicated, ignored, failed, replied, and reply-failed, labeled by provider without comment content.

### P2 — Fork PR heads are not represented fully

The continuation target records only the base repository slug and a head branch name. For a fork-based PR, the head repository may differ from the base repository. Fetching the branch from the bound base repository can fail or, if a same-named branch exists, target the wrong ref.

Recommendation: persist and validate head owner/repository/ref separately. Only continue when the configured credential and git proxy grant can write that exact head repository/ref; otherwise reply that the PR cannot be modified automatically.

## Proposed target design

1. **Discover:** receive provider webhooks and run a polling reconciler. Normalize top-level comments, reviews, inline comments, and thread replies into a common event.
2. **Authorize:** validate repository relationship and commenter policy before spending budget or using a stored principal.
3. **Persist:** insert an immutable trigger event under a provider-event uniqueness key. Discovery progress and processing progress must be separate.
4. **Resolve ownership:** map the PR directly to its originating binding/run and exact head repository/ref.
5. **Acknowledge:** post a marker-protected “queued” reply with a Windshift run reference, or a safe rejection/defer reason.
6. **Execute:** queue a continuation with the exact review context and a least-privilege push grant for the existing head ref.
7. **Respond:** post one terminal result for pushed changes, no changes, failure, or cancellation. Retry reply delivery durably.
8. **Reconcile:** periodically repair stuck events and verify that each accepted event has a terminal run and PR response.

## Recommended delivery sequence

### Phase 1: make the existing promise safe and reliable

- Add commenter authorization.
- Add the durable trigger inbox and idempotency constraint.
- Stop advancing past unpersisted/unclassified requests.
- Initialize live PR cursors without dropping early comments.
- Reply for queued, no-change, failed, skipped, and success outcomes.
- Add tests for multiple comments, transient errors, crashes, and reply retries.

### Phase 2: close credential and ownership gaps

- Support per-user OAuth in mention resolution and PR-comment discovery.
- Persist PR-to-binding/run ownership and triggering principal.
- Resolve among compatible open PRs instead of choosing before filtering.
- Track exact head repository/ref and handle forks explicitly.

### Phase 3: support real review workflows

- Ingest submitted reviews, inline comments, and review-thread replies for GitHub and Gitea/Codeberg.
- Pass structured path/line/thread context to the agent.
- Add webhook ingestion with polling reconciliation and an end-to-end provider test matrix.

## Acceptance criteria

The feature should be considered complete only when all of the following hold:

1. A second Windshift comment mention on an item with one compatible open agent PR pushes to the same PR and opens no new PR.
2. The same behavior works for PAT, GitHub App, and supported per-user OAuth connections.
3. Every authorized `@agent` request receives an acknowledgement and exactly one terminal response, including no-change and failure outcomes.
4. Unauthorized SCM users cannot start runs or cause pushes.
5. Two comments arriving in one polling interval are both durably represented and are processed or explicitly coalesced with a visible response.
6. A crash at any point between discovery, run creation, push, and reply does not lose a request or execute it twice.
7. A comment made before the first poll of a newly created PR is not dropped.
8. Inline comments, submitted reviews, and top-level comments behave according to a documented provider matrix.
9. The PR is mapped to the binding/run that owns it rather than inferred from the latest unrelated item run.
10. Fork PRs are either safely continued on their exact writable head ref or receive a clear refusal.

## Verification performed for this analysis

Focused overlay tests were run on 2026-07-11:

```text
./overlay.sh ../core -- -tags=test -count=1 \
  -run 'Test(Poll_|BindingService_(applyContinuation|MentionTrigger|Rerun_ContinuesOpenPR)|AgentPRService_)' \
  ./internal/scm ./internal/services ./internal/repoprep
```

All selected tests passed. That confirms the covered happy-path components; it does not negate the untested gaps above.
