# Security Review — Windshift Core

**Date:** 2026-06-09
**Scope:** Full-codebase pass — authorization/IDOR, the coding-agent/runner control plane + brokers, XSS, SSRF, auth/session/token lifecycle, secrets/redaction, SQL injection, and dependencies. Go backend (`internal/`), Svelte frontend (`frontend/src`), and the runner subsystem.
**Method:** Multi-agent static review — 8 parallel attack-surface reviewers, each finding independently verified by 2 adversarial skeptics (majority "real" to survive) — plus `govulncheck ./...` and `npm audit`.
**Reviewer:** Claude (automated)

## Executive summary

The codebase remains in strong shape. The May 6 review's twelve findings are
resolved and its dependency fixes are merged (Go 1.26.4, goxmldsig 1.6.0,
x/image 0.39.0). The large new attack surface added since — the coding-agent /
runner control plane, brokers, repoprep, and the `redact` package — holds up
well under adversarial review: per-run tokens are scoped + bound to grants and
re-validated each request, the broker swaps real secrets server-side and never
exposes them to the container, git push runs host-side in a fresh temp repo
that never consults agent-mutated `.git/config`/hooks, the container sandbox
drops all caps + `no-new-privileges` + non-root + read-only + restricted
egress, git-receive-pack ref-gating checks every ref update against the grant,
and `ProxyHTTP` already uses an SSRF-safe transport.

**No critical or high-severity issues were confirmed.** Of 15 raw findings, 10
survived adversarial verification: **1 medium**, **6 low**, **3 informational**.
5 were refuted (a claimed `javascript:` XSS that CSP neutralizes, three
SSRF/auth notes blocked by existing controls, and an email-template SSTI that
is system-admin-only and uses no eval).

The one actionable cross-cutting theme: several **outbound HTTP clients bypass
the project's own SSRF-safe dialer** (`utils.SafeNetDialer`) — plugin
`http_fetch`, the Jira import client, the SCM (GitHub/Gitea) providers, and the
LLM broker proxy. All are admin/system-admin-gated today, so each is
defense-in-depth rather than a live non-admin SSRF, but routing them through
the existing dialer is cheap and closes the gap (Phase 8 of the agent-runner
fix plan already flags the LLM-proxy one).

## Dependency scan results

- **`govulncheck ./...`**: 0 vulnerabilities affecting first-party code. 2
  advisories in a required-but-uncalled module (`golang.org/x/image`
  GO-2026-5031 / GO-2026-5032, fixed in v0.41.0). Bumped as a free win since
  the package *is* reachable via attachment thumbnail decoding.
- **`npm audit`** (`frontend/`): **0 vulnerabilities** (the two moderate
  Excalidraw advisories left open in May are now gone).

## Findings table

| # | Sev | Area | File:line | Summary | Status |
|---|-----|------|-----------|---------|--------|
| F-1 | Medium | Authz / info-disclosure | `internal/handlers/asset_link_handlers.go:70,218` | `GetAssetLinks` returns linked item/asset/test-case titles with no per-target view check; `CreateAssetLink` lets an asset editor plant a link to any cross-workspace entity, then read its title back | ✅ Fixed |
| F-2 | Low | SSRF (defense-in-depth) | `internal/plugins/host_functions.go:118` + `manager.go:84` | Plugin `http_fetch` uses a plain `http.Client`; no SSRF dialer | ✅ Fixed |
| F-3 | Low | SSRF (defense-in-depth) | `internal/jira/client.go:138` | Jira import client uses a plain `http.Client` for the admin-supplied instance URL; Basic-auth creds could reach an internal host | ✅ Fixed |
| F-4 | Low | SSRF (defense-in-depth) | `internal/scm/github.go:68`, `internal/scm/gitea.go:61` | SCM providers use a plain `http.Client` for the admin-set base URL; OAuth client-secret exchange could reach an internal host | ✅ Fixed |
| F-5 | Low | Auth (secret-at-rest) | `internal/services/magic_link.go:88` | Portal magic-link tokens stored plaintext; a read-only DB leak yields usable, unexpired sign-in links | ✅ Fixed |
| F-6 | Low | Secrets (process listing) | `internal/services/container_service.go:53` | `ContainerService` passes capability env vars via `-e KEY=VALUE` argv; secrets visible in `/proc/<pid>/cmdline` + `docker inspect` | ✅ Fixed |
| F-7 | Low | Headers (XSS-in-depth) | `internal/handlers/plugins.go:222` | Plugin assets served unauthenticated, inline, extension-typed, with no `nosniff`/CSP/`Content-Disposition` | ✅ Fixed |
| F-8 | Info | SSRF (defense-in-depth) | `internal/handlers/runner_broker.go:181` | LLM broker proxy uses the default transport, not `ssrfSafeTransport()` like `ProxyHTTP` | ✅ Fixed |
| F-9 | Info | XSS (latent) | `frontend/src/lib/features/actions/shared/BaseActionFlowEditor.svelte:336` | `{@html tip}` unsanitized; `tip` is static i18n today, would become stored-XSS if wired to user/server data | ✅ Fixed |
| F-10 | Info | Auth (latency) | `internal/auth/tokens.go:386` | API-token validation cache serves stale permissions/role for up to 30s after an in-place edit (revocation/deactivation evict correctly) | ⏭ Documented |

### Refuted (raised, then rejected on verification)

| Claim | Why refuted |
|-------|-------------|
| CLI-authorize `javascript:` callback XSS (High) | Sink is blocked by the global strict CSP (`script-src 'self' 'nonce-…'`, no `unsafe-inline`; nonces do not authorize `javascript:` URIs). |
| Email/IMAP channel-manager SSRF (Info) | The dial path already goes through `utils.SafeNetDialer`, which blocks loopback/RFC1918/link-local/metadata/CGNAT. |
| OAuth deny / no-PKCE consent binding (Info) | The cross-site POST is blocked by the authoritative `Sec-Fetch-Site` CSRF check; not a usable path. |
| CSRF `Sec-Fetch-Site` fallback gap (Info) | "Reviewed, no gap" — the cookie-auth path requires the same-origin check; conclusion correct on re-verification. |
| Email-template SSTI (Info) | `html/template` `Parse`+`Execute` with no custom `FuncMap`/eval, and the template source is system-admin-gated. Not exploitable. |

---

## F-1 — Asset link listing leaks cross-workspace entity titles (Medium)

**Where**
- Read: `internal/handlers/asset_link_handlers.go:70-208` (`GetAssetLinks`)
- Write: `internal/handlers/asset_link_handlers.go:218-295` (`CreateAssetLink`)

**What**
`GetAssetLinks` checks only that the caller can *view the source asset's set*,
then hydrates and returns the title of every linked target (items via
`ItemRepository.GetTitles`, assets/test cases via SQL subquery) with **no view
check on those targets**. `CreateAssetLink` checks only edit on the source
asset's set and never validates the target, so an asset editor can insert a
link whose `target_id`/`target_type` point at an arbitrary entity in any other
workspace.

The filtered link path (`ItemLinkService.ListLinksForEntityWithChecks`, which
applies `FilterLinksByAccess` + `FilterPageLinksByACL`) rejects `asset`
entities at its entry point, so asset link listing has no filtered path and
goes exclusively through this unfiltered handler. The sibling
`GetAssetRelationshipGraph` *does* gate every neighbor via `canAccessEntity` —
the leak is specific to the listing handler.

**Attack**
1. Authenticate as a normal asset editor (edit on ≥1 asset set).
2. `POST /api/assets/{ownAssetId}/links` with `{link_type_id, target_type:"item", target_id:<victim item in another workspace>}` — inserted with no target check.
3. `GET /api/assets/{ownAssetId}/links` returns the victim item's title.
4. Increment `target_id` to enumerate titles of items/assets/test cases workspace-wide.

Discloses titles only (not full bodies), hence Medium.

**Fix (applied)**
`GetAssetLinks` now builds the same accessible-workspace set + cached
set-view checks the graph endpoint uses and drops any link whose *other*
endpoint the caller cannot view (via `canAccessEntity`), so neither the title
nor the link's existence leaks. `CreateAssetLink` now validates the target is
viewable (which also rejects nonexistent targets) before inserting, returning
404 on failure to avoid existence leakage.

---

## F-2 / F-3 / F-4 / F-8 — Outbound HTTP clients bypass the SSRF-safe dialer (Low / Info)

**Where**
- `internal/plugins/manager.go:84` (default client for plugin `http_fetch` at `host_functions.go:118`)
- `internal/jira/client.go:138`
- `internal/scm/github.go:68`, `internal/scm/gitea.go:61`
- `internal/handlers/runner_broker.go:181` (`ProxyLLM` ReverseProxy)

**What**
Each builds a plain `&http.Client{Timeout: …}` (or default ReverseProxy
transport) for a host that is operator/admin-configured, with no
loopback/RFC1918/link-local/CGNAT/metadata blocking and no redirect
re-validation. The project already ships `utils.SafeNetDialer` (checks the
resolved IP pre-handshake via `ControlContext`, robust against DNS rebinding,
re-checked on every redirect hop) and uses it for webhook/OIDC/portal/HTTP-broker
egress. These four paths simply don't opt in.

All are admin/system-admin-gated today (plugin install, Jira/SCM provider CRUD,
LLM-connection config), so none is a live non-admin SSRF — they are
defense-in-depth gaps and a credential-to-internal-host exposure (the Jira
Basic-auth header and the SCM OAuth client secret travel to the chosen host).
Phase 8 of `docs/agent-runner-security-fix-plan-2026-06-07.md` already lists the
LLM-proxy item.

**Fix (applied)**
- Plugin manager default client, Jira client, and SCM `baseProvider` now use
  `&http.Client{Timeout: …, Transport: &http.Transport{DialContext: utils.SafeNetDialer(…).DialContext}}`
  — preserving redirect-following while blocking private targets on every hop.
- `ProxyLLM` now sets `Transport: ssrfSafeTransport()`, matching `ProxyHTTP`.

**Self-hosted / local escape hatch.** SCM (Gitea / GitHub Enterprise), Jira Data
Center, and local LLM gateways legitimately run on private networks or
localhost, which the dialer would otherwise block. The previous per-endpoint
CIDR allowlists (`--llm-allowed-private-cidrs`, `--oidc-allowed-private-cidrs`)
were removed in favor of a single global switch — `--allow-local-connections` /
`ALLOW_LOCAL_CONNECTIONS` (off by default, mirrors `--no-csrf`) — which relaxes
every SSRF-safe dialer/client at once for these deployments. Wired in
`internal/utils/dialer.go` (consulted by `IsBlockedSSRFAddrWithAllowedCIDRs`,
`NewSSRFSafeHTTPClient`, `ValidateExternalURL`) and set once at startup from
config; logs a warning when enabled.

---

## F-5 — Portal magic-link tokens stored in plaintext (Low)

**Where** `internal/services/magic_link.go:88` (write), `:156` (lookup)

**What**
`GenerateMagicLink` inserts the raw 32-byte token into
`portal_customer_magic_links.token`; `ValidateMagicLink` looks it up with
`WHERE ml.token = ?`. The token is high-entropy + single-use + TTL'd, so this
is not a brute-force/timing issue — but storing the bearer secret in plaintext
means a read-only DB compromise (leaked backup, read replica, blind SQLi
elsewhere) yields directly usable, unexpired sign-in links for any portal
customer whose link hasn't been consumed. Every other token type except
sessions is hashed.

**Fix (applied)**
Store `SHA-256(token)` in the DB and compare the hash of the presented token on
validation. The token is already high-entropy, so a fast hash is sufficient and
keeps the deterministic indexed lookup. The plaintext appears only in the
emailed URL. (In-flight links minted before deploy stop validating; acceptable
given the 30-min / 24-h TTL and the recovery-resend UX.)

---

## F-6 — Container env vars passed via argv instead of an env-file (Low)

**Where** `internal/services/container_service.go:35-58` (`buildDockerRunArgs`)

**What**
`ContainerService` appends each `docker_environment` capability env var as a
separate `-e KEY=VALUE` argument. The sibling runners (`DockerRunner`,
`agent_runner`) deliberately route per-run secrets through a 0600 `--env-file`
precisely so tokens never appear on the command line. Here, any credential an
operator places in a capability's `env_vars` (a legitimate use — CI images need
tokens) becomes visible to anyone who can read `/proc/<pid>/cmdline`, run
`docker inspect` / `docker ps --no-trunc`, or capture host process listings.

**Fix (applied)**
`buildDockerRunArgs` now writes the env map to a 0600 temp file and passes
`--env-file <path>`, deferring cleanup, mirroring the hardened runners. Values
with newlines are rejected (docker `--env-file` is line-based), consistent with
`writeDockerEnvFile`.

---

## F-7 — Plugin static assets served without protective headers (Low)

**Where** `internal/handlers/plugins.go:210-233` (`GetAsset`), route at `internal/routes/admin.go:66`

**What**
`GET /api/plugins/{name}/assets/{asset...}` is unauthenticated and serves bytes
with an extension-derived `Content-Type` (which can be `text/html`,
`application/javascript`, …) but sets **no** `X-Content-Type-Options: nosniff`,
**no** CSP, and **no** `Content-Disposition`. Unlike the attachment / portal /
public-board download paths — which force-download + sandbox dangerous types —
this serves HTML/JS/SVG inline, same-origin, to anonymous callers. Plugins are
admin-installed, so the content is admin-trusted; this is a defense-in-depth gap
that would let a malicious/compromised plugin host same-origin script.

**Fix (applied)**
`GetAsset` now always sets `X-Content-Type-Options: nosniff` +
`X-Frame-Options: DENY`, and for non-image/non-css/non-font types sets
`Content-Security-Policy: default-src 'none'; sandbox` and
`Content-Disposition: attachment`, mirroring the attachment download hardening.

---

## F-9 — Unsanitized `{@html tip}` in action-flow editor (Info / latent)

**Where** `frontend/src/lib/features/actions/shared/BaseActionFlowEditor.svelte:336`

**What**
`{@html tip}` renders each `tips` entry as raw HTML with no `sanitizeHtml`.
Tracing the source: the only caller (`ActionFlowEditor.svelte`) supplies static
i18n strings and the default is a hardcoded array — all compile-time constants,
not user/server data. So it is not exploitable today, but it is an unsanitized
sink that would become stored-XSS the moment anyone wires user/server text into
`tips`.

**Fix (applied)**
Render tips as plain text (they contain no markup), removing the latent sink.

---

## F-10 — API-token cache serves stale permissions for ≤30s (Info / documented)

**Where** `internal/auth/tokens.go:386`

**What**
`ValidateToken` caches the `{User, APIToken}` snapshot for 30s. Explicit
revocation and deactivation are handled (reverse-lookup eviction; `is_active` +
expiry re-checked on hit). But changes that don't go through
`RevokeToken`/`InvalidateTokens` — editing a token's permissions in place, or
granting/revoking the owner's system-admin role — aren't reflected until the
entry expires (≤30s). This is a documented latency tradeoff, not a bypass; no
attacker can trigger or extend the window.

**Decision:** documented, not changed. If tighter propagation is wanted later,
invalidate the token cache on permission edits and role grants/revocations the
same way `RevokeToken` does.

---

## What was checked and is clean

Recorded so the next reviewer doesn't redo it.

- **Runner per-run tokens** — minted per run, bound to `run_token_id` +
  `grants_json`, re-validated each broker request, run must be `running`; a
  finished/foreign run's token cannot reach another run's secrets/git/llm.
- **Broker secret isolation** — `ProxyLLM`/`ProxyGit`/`GetSecret` swap the real
  provider credential server-side; the container only ever holds the per-run
  `WS_TOKEN`. Real SCM/LLM keys never enter the container.
- **git-receive-pack ref-gating** — `parseReceivePackCommands` parses pkt-line
  commands and checks every ref update against `grants.Git.Ref` (exact match).
- **repoprep host-git isolation** — host-side push runs in a fresh `git init`
  temp repo, fetches the branch by SHA, and pushes that SHA; agent-mutated
  `.git/config`, hooks, remotes, and credential helpers are never consulted.
  `protocol.{ext,tar}.allow=never`, `core.hooksPath=/dev/null`,
  `GIT_CONFIG_NOSYSTEM=1`, empty global config, token via `GIT_ASKPASS` only.
- **Container sandbox** — `--cap-drop=ALL`, `--security-opt=no-new-privileges`,
  `--user=1000:1000`, `--read-only` + tmpfs, restricted egress network,
  pids/memory/cpu caps; per-run secrets via 0600 `--env-file`, never argv.
- **`ProxyHTTP` SSRF** — `ssrfSafeTransport()` with `Proxy: nil` (no env proxy),
  post-resolution blocklist, grant host/path matching that rejects `userinfo@`,
  trailing-dot, and prefix-confusion hosts.
- **`redact` package** — scrubs URL creds, `WS_TOKEN`/`LLM_API_KEY`/`AGENT_GIT_TOKEN`,
  bearer headers, `wsrt_`/`wsrc_`/`crw_` tokens, and JSON secret fields; applied
  at git error output, runner result, and remote-claim failure paths.
- **Registration tokens** — single-use (CAS consume), expiry, revocation;
  per-instance `wsrc_` credentials hashed at rest.
- **Item/page authz** — 404-not-403 invariant holds in sampled handlers; page
  ACL tree-walk + `inherit_permissions` + archived-page handling correct;
  v1 token scopes are not bypassed by system-admin.
- **SQL** — `fmt.Sprintf`-built SQL in repositories/migrations/CQL uses
  identifiers from code enums, values via `?` placeholders; no user string
  reaches SQL outside a placeholder (incl. the asset custom-field JSON-path
  builders, where the interpolated key is a numeric field id).
- **CSRF / CSP / sessions / SSRF dialer / SAML redirect validator / attachment
  download hardening** — confirmed unchanged and correct since the May review.
- **Frontend XSS sinks** — all `{@html}` except F-9 route through
  `sanitizeHtml` or a trusted renderer (marked+DOMPurify, Milkdown link
  sanitizer, mermaid `securityLevel:'strict'`); `redirect_url`/`auth_url`
  navigation is scheme-checked.
- **Email-template rendering** — `html/template`, no custom `FuncMap`/eval,
  template source system-admin-gated.

## Remediation status

| # | Status | How |
|---|--------|-----|
| F-1 | ✅ Fixed | `GetAssetLinks` filters both link directions by `canAccessEntity`; `CreateAssetLink` validates the target is viewable |
| F-2 | ✅ Fixed | Plugin manager default client uses `SafeNetDialer` transport |
| F-3 | ✅ Fixed | Jira client uses `SafeNetDialer` transport |
| F-4 | ✅ Fixed | SCM `baseProvider` uses `SafeNetDialer` transport |
| F-5 | ✅ Fixed | Magic-link tokens stored + compared as `SHA-256` |
| F-6 | ✅ Fixed | `ContainerService` uses a 0600 `--env-file` |
| F-7 | ✅ Fixed | Plugin `GetAsset` sets `nosniff` + CSP/sandbox + `Content-Disposition` for dangerous types |
| F-8 | ✅ Fixed | `ProxyLLM` uses `ssrfSafeTransport()` |
| F-9 | ✅ Fixed | Action-flow tips rendered as plain text |
| F-10 | ⏭ Documented | Latency tradeoff; no attacker-triggerable window |
| Deps | ✅ Fixed | `golang.org/x/image` → v0.41.0 (GO-2026-5031/5032) |

**Verification:** `go build ./...` clean, `govulncheck ./...` reports 0
first-party vulnerabilities, `npm audit` 0, targeted `go test` for touched
packages green (via the `core-tests` overlay).
