package handlers

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"windshift/internal/auth"
	"windshift/internal/llm"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// Broker request-body caps (WI-238 security Phase 7): bound how much a runner
// can push through a broker endpoint so a compromised container can't use the
// broker as an unbounded-upload amplifier. LLM/HTTP requests are small; git
// packfiles can be large, so its cap is generous but still finite.
const (
	maxLLMBrokerBody  = 16 << 20 // 16 MiB
	maxHTTPBrokerBody = 16 << 20 // 16 MiB
	maxGitBrokerBody  = 2 << 30  // 2 GiB

	// egressResponseHeaderTimeout bounds time-to-first-header for arbitrary
	// HTTP/git egress, where a slow upstream is treated as a fault.
	egressResponseHeaderTimeout = 30 * time.Second
	// llmResponseHeaderTimeout is intentionally generous: an OpenAI-compatible
	// chat completion can spend minutes on prompt prefill (long context,
	// reasoning) before committing the SSE response headers. A 30s bound aborts
	// those slow-but-healthy calls with "timeout awaiting response headers",
	// which the coding agent surfaces as a 503 and retries into failure. Still
	// bounded so a genuinely hung upstream cannot pin a goroutine forever.
	llmResponseHeaderTimeout = 5 * time.Minute
)

// RunnerBrokerHandler is the secretless access layer's server side
// (Initiative WI-141 / WI-144): the broker endpoints a running job calls to
// reach credentials it is granted, without those credentials ever living on
// the runner host. This file hosts the secrets broker; the git and LLM
// proxies join it in WI-164/WI-165.
//
// Authentication is the per-run token (the WS_TOKEN minted at claim). A
// request is authorized only when (a) the presented token is exactly the
// token bound to the run (agent_runs.run_token_id), (b) the run is still
// running, and (c) the requested resource is in the run's grants. So a
// leaked run-A token cannot reach run-B's resources, and a token cannot
// reach a credential the run was not granted.
type RunnerBrokerHandler struct {
	tokens   *auth.TokenManager
	runs     *repository.AgentRunRepository
	creds    *services.ActionCredentialService
	llmConns *llm.ConnectionManager
	scm      services.SCMCredentialResolver
	usage    *repository.LLMUsageRepository // optional; nil disables metering
}

// NewRunnerBrokerHandler constructs the handler. Any nil dependency disables
// the corresponding broker (503), e.g. when the harness is not configured.
func NewRunnerBrokerHandler(tokens *auth.TokenManager, runs *repository.AgentRunRepository, creds *services.ActionCredentialService, llmConns *llm.ConnectionManager, scm services.SCMCredentialResolver) *RunnerBrokerHandler {
	return &RunnerBrokerHandler{tokens: tokens, runs: runs, creds: creds, llmConns: llmConns, scm: scm}
}

// SetUsageRepository attaches the LLM usage repository so ProxyLLM meters token
// usage + cost per call. Optional and nil-safe: without it, proxying works
// unchanged but no usage is recorded.
func (h *RunnerBrokerHandler) SetUsageRepository(usage *repository.LLMUsageRepository) {
	h.usage = usage
}

// runFromToken authenticates the per-run token and authorizes it for the run
// in the URL: the token must be the one bound to the run, and the run must be
// running. Returns the run's grants + workspace, or writes a 401/403/404 and
// returns ok=false.
func (h *RunnerBrokerHandler) runFromToken(w http.ResponseWriter, r *http.Request, runID int) (grants *models.RunGrants, workspaceID int, ok bool) {
	token := bearerCredential(r)
	if token == "" {
		respondUnauthorized(w, r)
		return nil, 0, false
	}
	_, apiToken, err := h.tokens.ValidateToken(token)
	if err != nil || apiToken == nil {
		respondUnauthorized(w, r)
		return nil, 0, false
	}
	boundTokenID, ws, g, status, err := h.runs.GetRunAuthz(r.Context(), runID)
	if err != nil {
		respondNotFound(w, r, "agent run")
		return nil, 0, false
	}
	if apiToken.ID != boundTokenID || status != models.AgentRunStatusRunning {
		respondForbidden(w, r)
		return nil, 0, false
	}
	return g, ws, true
}

// GetSecret resolves a named credential for a run that is granted it, and
// returns the plaintext. GET /secrets/{run}/{credentialId}.
func (h *RunnerBrokerHandler) GetSecret(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil || h.runs == nil || h.creds == nil {
		respondServiceUnavailable(w, r, "coding-agent harness is disabled on this server")
		return
	}
	runID, ok := requireIDParam(w, r, "run")
	if !ok {
		return
	}
	credID, ok := requireIDParam(w, r, "credentialId")
	if !ok {
		return
	}

	grants, workspaceID, ok := h.runFromToken(w, r, runID)
	if !ok {
		return
	}
	if !grants.AllowsSecret(credID) {
		respondForbidden(w, r)
		return
	}

	plaintext, _, err := h.creds.Resolve(r.Context(), credID, workspaceID)
	if err != nil {
		respondNotFound(w, r, "credential")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"value": plaintext})
}

// ProxyLLM reverse-proxies a running job's model API calls to the LLM
// connection it is granted, injecting the real provider credential
// server-side so the key never reaches the runner. /llm-proxy/{run}/{path...}.
//
// Token-quota metering (grants.LLM.QuotaTokens) is a follow-up; this slice
// establishes key-injecting, run-scoped proxying. Only anthropic and
// openai-compatible auth conventions are handled; other providers need an
// added case.
// unbindStreamDeadlines lifts the server's per-request read/write deadlines for
// a long-lived proxied stream. The broker's http.Server sets ReadTimeout (15s)
// and WriteTimeout (30s) as slow-loris protection for ordinary JSON endpoints,
// but those are hard wall-clock deadlines that do NOT reset on streaming
// activity: a 30s WriteTimeout severs any LLM SSE response — or a large git
// transfer — the moment it runs past 30s, which the coding agent observes as an
// HTTP/2 "INTERNAL_ERROR; received from peer" mid-stream. Streaming proxy
// handlers opt out here; the upstream transport's ResponseHeaderTimeout and the
// request context still bound the work. Best-effort: SetWriteDeadline returns
// http.ErrNotSupported on a ResponseWriter that cannot carry a deadline, which
// is harmless (the broker's writer can).
func unbindStreamDeadlines(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
}

func (h *RunnerBrokerHandler) ProxyLLM(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil || h.runs == nil || h.llmConns == nil {
		respondServiceUnavailable(w, r, "coding-agent harness is disabled on this server")
		return
	}
	runID, ok := requireIDParam(w, r, "run")
	if !ok {
		return
	}
	grants, _, ok := h.runFromToken(w, r, runID)
	if !ok {
		return
	}
	if grants == nil || grants.LLM == nil {
		respondForbidden(w, r)
		return
	}
	cfg, err := h.llmConns.ConnectionRuntime(r.Context(), grants.LLM.ConnectionID)
	if err != nil {
		respondServiceUnavailable(w, r, "llm connection unavailable")
		return
	}
	provider := llm.GetProvider(llm.ProviderType(cfg.ProviderType))
	base := cfg.BaseURL
	if base == "" && provider != nil {
		base = provider.BaseURL
	}
	target, err := url.Parse(base)
	if err != nil || target.Host == "" {
		respondServiceUnavailable(w, r, "llm connection has no base url")
		return
	}
	upstreamPath := r.PathValue("path")
	providerType := strings.ToLower(cfg.ProviderType)
	// The Go agent speaks OpenAI-compatible /v1/chat/completions. Until the
	// broker implements OpenAI→Anthropic translation, reject Anthropic bindings
	// clearly instead of forwarding an incompatible request shape.
	if providerType == "anthropic" {
		respondServiceUnavailable(w, r, "anthropic coding-agent bindings require broker translation that is not enabled")
		return
	}
	// Restrict the run token to the exact inference API surface the Go agent
	// needs. A leaked run token must not be usable to drive files, fine-tuning,
	// batch, or arbitrary provider APIs through the injected key.
	if !allowedLLMProxyEndpoint(r.Method, upstreamPath) {
		respondForbidden(w, r)
		return
	}
	// Bound the request body (WI-238 security Phase 7).
	r.Body = http.MaxBytesReader(w, r.Body, maxLLMBrokerBody)
	// Read the chat body once when we need to merge provider_config OR meter
	// usage (image parts are priced from the request, not the SSE tail).
	imageCount := 0
	isChat := strings.TrimPrefix(upstreamPath, "/") == "v1/chat/completions"
	hasProviderConfig := strings.TrimSpace(cfg.ProviderConfig) != ""
	if isChat && (hasProviderConfig || h.usage != nil) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			respondBadRequest(w, r, "invalid llm request body")
			return
		}
		imageCount = countImageParts(body)
		out := body
		if hasProviderConfig {
			merged, mergeErr := llm.MergeProviderConfigJSON(body, cfg.ProviderConfig)
			if mergeErr != nil {
				respondServiceUnavailable(w, r, services.RedactString(mergeErr.Error()))
				return
			}
			out = merged
		}
		r.Body = io.NopCloser(bytes.NewReader(out))
		r.ContentLength = int64(len(out))
		r.Header.Set("Content-Length", fmt.Sprintf("%d", len(out)))
	}
	apiKey := cfg.APIKey
	providerChatPath := "/v1/chat/completions"
	if provider != nil && provider.ChatPath != "" {
		providerChatPath = provider.ChatPath
	}
	proxy := &httputil.ReverseProxy{
		// Match ProxyHTTP: refuse to dial private/loopback/link-local/metadata
		// targets even if an LLM connection's base URL is misconfigured or
		// rebinds between write-time validation and use (Phase 8 of the
		// agent-runner security fix plan).
		Transport: ssrfSafeTransport(llmResponseHeaderTimeout),
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			outPath := upstreamPath
			if strings.TrimPrefix(upstreamPath, "/") == "v1/chat/completions" {
				outPath = strings.TrimPrefix(providerChatPath, "/")
			}
			req.URL.Path = singleJoiningSlash(target.Path, outPath)
			req.URL.RawPath = ""
			// Replace the run-token with the real provider credential.
			req.Header.Del("Authorization")
			req.Header.Del("X-Api-Key")
			switch providerType {
			case "anthropic":
				req.Header.Set("x-api-key", apiKey)
				if req.Header.Get("anthropic-version") == "" {
					req.Header.Set("anthropic-version", "2023-06-01")
				}
			default: // openai-compatible
				req.Header.Set("Authorization", "Bearer "+apiKey)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			respondServiceUnavailable(w, r, services.RedactString(err.Error()))
		},
		// Meter token usage + cost from the SSE usage tail (WI-493). The agent is
		// untrusted, so the broker is the trustworthy capture point.
		ModifyResponse: h.meterLLMResponse(runID, cfg.Model, h.llmConns.ModelPricing(llm.ProviderType(cfg.ProviderType), cfg.Model), imageCount),
	}
	// The chat completion streams as SSE and routinely runs past the server's
	// 30s WriteTimeout; lift the deadline so the stream is not severed mid-run.
	unbindStreamDeadlines(w)
	proxy.ServeHTTP(w, r)
}

// allowedGitProxyPath reports whether the {gitpath...} tail is one of the three
// endpoints a git smart-HTTP client uses. Anything else (notably a "../"
// traversal tail) is rejected so the proxied upstream path cannot escape the
// grant-validated owner/repo. The tail is the decoded PathValue, so an encoded
// "%2e%2e" already appears here as "..".
// gitProxyBaseURL resolves the clone base URL the proxy targets. GitHub-cloud
// connections commonly store no base_url (the API layer defaults it
// internally), but the proxy needs the clone host — default it exactly like
// deriveCloneURL does for local runs, otherwise a remote run 503s on a
// connection that works fine in-process. Gitea has no well-known default; an
// empty base URL there stays a config error (caught by the Host=="" check).
func gitProxyBaseURL(providerType, stored string) string {
	if stored == "" && providerType == "github" {
		return "https://github.com"
	}
	return stored
}

func allowedGitProxyPath(path string) bool {
	switch strings.TrimPrefix(path, "/") {
	case "info/refs", "git-upload-pack", "git-receive-pack":
		return true
	default:
		return false
	}
}

func allowedLLMProxyEndpoint(method, path string) bool {
	path = strings.TrimPrefix(path, "/")
	switch {
	case method == http.MethodPost && path == "v1/chat/completions":
		return true
	case method == http.MethodGet && path == "v1/models":
		return true
	default:
		return false
	}
}

// ProxyGit reverse-proxies a running job's git smart-HTTP traffic to its
// granted repo on the SCM provider, injecting the real SCM credential
// server-side so the token never reaches the runner. The clone URL is stable
// and repo-scoped (/git-proxy/{ws}/{owner}/{repo}/...), so the presented
// per-run token (git Basic-auth password) is what identifies the run.
//
// Authorization is repo-level (the run's git grant must name owner/repo);
// ref-level push gating (grant.Git.Ref) is a follow-up since the pushed ref
// lives in the git-receive-pack payload.
func (h *RunnerBrokerHandler) ProxyGit(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil || h.runs == nil || h.scm == nil {
		respondServiceUnavailable(w, r, "coding-agent harness is disabled on this server")
		return
	}
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	gitPath := r.PathValue("gitpath")

	// git presents the per-run token via HTTP Basic auth (token as the
	// password, dummy username); fall back to Bearer. A 401 with
	// WWW-Authenticate prompts git to (re)send credentials.
	token := ""
	if u, p, ok := r.BasicAuth(); ok {
		if token = p; token == "" {
			token = u
		}
	}
	if token == "" {
		token = bearerCredential(r)
	}
	if token == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="windshift-git-proxy"`)
		respondUnauthorized(w, r)
		return
	}
	_, apiToken, err := h.tokens.ValidateToken(token)
	if err != nil || apiToken == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="windshift-git-proxy"`)
		respondUnauthorized(w, r)
		return
	}
	_, _, grants, status, err := h.runs.GetRunByTokenID(r.Context(), apiToken.ID)
	if err != nil {
		respondNotFound(w, r, "agent run")
		return
	}
	repoName := owner + "/" + strings.TrimSuffix(repo, ".git")
	// Select the grant authorizing THIS repo (WI-449: a run may bind several).
	// Deny-by-default: no matching grant → forbidden.
	gitGrant := grants.GitGrantFor(repoName)
	if status != models.AgentRunStatusRunning || gitGrant == nil {
		respondForbidden(w, r)
		return
	}

	// Restrict the git tail to the three git smart-HTTP endpoints (WI-238).
	// The repo grant is enforced via owner/repo above, but gitPath is appended
	// raw into the upstream URL path, and a URL-encoded "%2e%2e" tail decodes to
	// "../" *after* ServeMux's cleanPath redirect runs — so a tail like
	// "%2e%2e/%2e%2e/other/repo/git-upload-pack" keeps owner/repo (and thus the
	// grant check) intact while making the proxied path traverse to a different
	// repo on the SCM host, abusing the injected credential. Allow-listing the
	// exact endpoints closes that traversal regardless of encoding.
	if !allowedGitProxyPath(gitPath) {
		respondForbidden(w, r)
		return
	}

	// Bound the request body (WI-238 security Phase 7). Generous for git
	// packfiles but finite, so a push can't stream unboundedly through the proxy.
	r.Body = http.MaxBytesReader(w, r.Body, maxGitBrokerBody)

	// Ref-level push gating (WI-168): the repo grant authorizes reads (clone /
	// fetch via git-upload-pack), but a push (git-receive-pack) may only touch
	// the single ref named in grants.Git.Ref. The injected SCM credential can
	// write any ref, so we parse the pushed ref-update commands and reject the
	// request unless every update is for the granted ref. The packfile is left
	// un-buffered: we replay the consumed pkt-line prefix ahead of the rest of
	// the body when proxying.
	if r.Method == http.MethodPost && strings.TrimPrefix(gitPath, "/") == "git-receive-pack" {
		replay, perr := h.authorizeGitPush(r, grants, repoName)
		if perr != nil {
			respondForbidden(w, r)
			return
		}
		r.Body = replay
	}

	// The grant's UserID is the credential principal (WI-275): on OAuth
	// connections the proxy injects that user's personal token (the run's
	// triggering user). 0 — legacy runs queued before the field existed —
	// keeps the connection-level credential.
	var scmToken, scmProviderType, scmBase string
	if gitGrant.UserID > 0 {
		scmToken, scmProviderType, scmBase, err = h.scm.ResolveForRunAsUser(r.Context(), gitGrant.ConnectionID, gitGrant.UserID)
	} else {
		scmToken, scmProviderType, scmBase, err = h.scm.ResolveForRun(r.Context(), gitGrant.ConnectionID)
	}
	if err != nil {
		respondServiceUnavailable(w, r, "scm credential unavailable")
		return
	}
	target, err := url.Parse(gitProxyBaseURL(scmProviderType, scmBase))
	if err != nil || target.Host == "" {
		respondServiceUnavailable(w, r, "scm connection has no base url")
		return
	}
	upstreamPath := singleJoiningSlash(target.Path, owner+"/"+repo+"/"+gitPath)
	proxy := &httputil.ReverseProxy{
		// Resolve and reject non-public destinations at connection time. The
		// transport intentionally ignores proxy environment variables so they
		// cannot bypass the checked dialer.
		Transport: ssrfSafeTransport(egressResponseHeaderTimeout),
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.URL.Path = upstreamPath
			req.URL.RawPath = ""
			// Swap the run-token for the real SCM credential (provider-
			// agnostic oauth2:<token> Basic form).
			req.Header.Del("Authorization")
			req.SetBasicAuth("oauth2", scmToken)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			respondServiceUnavailable(w, r, services.RedactString(err.Error()))
		},
	}
	// Git smart-HTTP clones/fetches of a large repo stream well past 30s in
	// both directions; lift the read/write deadlines for the transfer.
	unbindStreamDeadlines(w)
	proxy.ServeHTTP(w, r)
}

// ProxyHTTP forwards a running job's outbound HTTP request to an allow-listed
// external URL (the run's http grant), so egress is governed centrally
// (WI-145). The target is given in the X-Windshift-Target header and checked
// against grants.HTTP; the run-token is stripped before forwarding.
// Per-target credential injection is a follow-up — this slice is allow-listed
// egress.
func (h *RunnerBrokerHandler) ProxyHTTP(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil || h.runs == nil {
		respondServiceUnavailable(w, r, "coding-agent harness is disabled on this server")
		return
	}
	runID, ok := requireIDParam(w, r, "run")
	if !ok {
		return
	}
	grants, _, ok := h.runFromToken(w, r, runID)
	if !ok {
		return
	}
	target := r.Header.Get("X-Windshift-Target")
	if target == "" {
		respondBadRequest(w, r, "X-Windshift-Target header is required")
		return
	}
	if grants == nil || !grants.AllowsHTTP(target) {
		respondForbidden(w, r)
		return
	}
	// Bound the request body (WI-238 security Phase 7).
	r.Body = http.MaxBytesReader(w, r.Body, maxHTTPBrokerBody)
	tu, err := url.Parse(target)
	if err != nil || tu.Host == "" {
		respondBadRequest(w, r, "invalid target url")
		return
	}
	proxy := &httputil.ReverseProxy{
		// Block egress to private/loopback/link-local addresses (SSRF guard,
		// WI-168) so the broker can't be coerced into reaching the cloud
		// metadata endpoint or internal services. The dialer re-resolves and
		// re-checks at connect time, which also defends against DNS rebinding.
		Transport: ssrfSafeTransport(egressResponseHeaderTimeout),
		Director: func(req *http.Request) {
			req.URL = tu
			req.Host = tu.Host
			req.Header.Del("X-Windshift-Target")
			req.Header.Del("Authorization") // strip the run-token
		},
	}
	// Generic upstream proxy may stream an arbitrarily long response; lift the
	// deadlines so a slow/large transfer is not cut at the 30s WriteTimeout.
	unbindStreamDeadlines(w)
	proxy.ServeHTTP(w, r)
}

// ssrfSafeTransport is an http.RoundTripper whose dialer rejects connections
// to non-public IPs. It reuses utils.SafeNetDialer, which checks the resolved
// address post-resolution but pre-handshake (via ControlContext), so the guard
// is robust against DNS rebinding. The blocklist is the conservative SSRF
// superset (loopback, RFC1918, CGNAT 100.64/10, link-local, multicast and the
// unspecified 0.0.0.0/:: addresses that route to localhost) — broader than the
// plain IsPrivateIP check, which misses several localhost-reachable ranges.
// Used by the HTTP egress broker.
func ssrfSafeTransport(responseHeaderTimeout time.Duration) http.RoundTripper {
	return &http.Transport{
		// No ProxyFromEnvironment (WI-238 security Phase 7): an env-configured
		// HTTP(S)_PROXY would be dialed directly, bypassing the post-resolution
		// blocklist below and reopening the SSRF hole the dialer closes. The
		// broker always connects to the validated target itself.
		Proxy:                 nil,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		DialContext:           utils.SafeNetDialer(10 * time.Second).DialContext,
	}
}

// authorizeGitPush parses the pushed ref-update commands from a
// git-receive-pack request and verifies every update targets the run's
// granted ref. It returns a replayable body (the consumed pkt-line prefix
// followed by the still-unread remainder) so the proxy can forward the
// packfile without it being buffered, or an error if the push is
// unauthorized or unparseable (callers must fail closed).
//
// The body is captured through a TeeReader, so whether the client sent the
// commands gzip-compressed or not, the exact original bytes are replayed
// upstream — only the decompressed view is used for parsing.
func (h *RunnerBrokerHandler) authorizeGitPush(r *http.Request, grants *models.RunGrants, repoName string) (io.ReadCloser, error) {
	var captured bytes.Buffer
	tee := io.TeeReader(r.Body, &captured)

	parseSrc := tee // io.Reader; swapped to a gzip reader below when needed
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(tee)
		if err != nil {
			return nil, fmt.Errorf("git push: invalid gzip body: %w", err)
		}
		defer func() { _ = gz.Close() }()
		parseSrc = gz
	}

	cmds, err := parseReceivePackCommands(parseSrc)
	if err != nil {
		return nil, err
	}
	for _, c := range cmds {
		if !grants.AllowsGitPush(repoName, c.Ref) {
			return nil, fmt.Errorf("git push: ref %q is not in the run's grant", c.Ref)
		}
	}

	// captured holds every raw byte already pulled from r.Body; the rest is
	// still on r.Body. Concatenated, they are the untouched original body.
	body := io.MultiReader(bytes.NewReader(captured.Bytes()), r.Body)
	return &replayBody{r: body, orig: r.Body}, nil
}

// replayBody adapts a reconstructed (prefix + remainder) reader back into an
// io.ReadCloser whose Close closes the original request body.
type replayBody struct {
	r    io.Reader
	orig io.ReadCloser
}

func (b *replayBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *replayBody) Close() error               { return b.orig.Close() }

// singleJoiningSlash joins two URL path segments with exactly one slash.
func singleJoiningSlash(a, b string) string {
	a = strings.TrimSuffix(a, "/")
	b = strings.TrimPrefix(b, "/")
	if b == "" {
		return a
	}
	return a + "/" + b
}
