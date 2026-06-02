package handlers

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"windshift/internal/auth"
	"windshift/internal/llm"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
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
}

// NewRunnerBrokerHandler constructs the handler. Any nil dependency disables
// the corresponding broker (503), e.g. when the harness is not configured.
func NewRunnerBrokerHandler(tokens *auth.TokenManager, runs *repository.AgentRunRepository, creds *services.ActionCredentialService, llmConns *llm.ConnectionManager, scm services.SCMCredentialResolver) *RunnerBrokerHandler {
	return &RunnerBrokerHandler{tokens: tokens, runs: runs, creds: creds, llmConns: llmConns, scm: scm}
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
	base := cfg.BaseURL
	if base == "" {
		if p := llm.GetProvider(llm.ProviderType(cfg.ProviderType)); p != nil {
			base = p.BaseURL
		}
	}
	target, err := url.Parse(base)
	if err != nil || target.Host == "" {
		respondServiceUnavailable(w, r, "llm connection has no base url")
		return
	}
	upstreamPath := r.PathValue("path")
	apiKey := cfg.APIKey
	providerType := strings.ToLower(cfg.ProviderType)
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.URL.Path = singleJoiningSlash(target.Path, upstreamPath)
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
	}
	proxy.ServeHTTP(w, r)
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
	if status != models.AgentRunStatusRunning || grants == nil || grants.Git == nil || grants.Git.Repo != repoName {
		respondForbidden(w, r)
		return
	}

	scmToken, _, scmBase, err := h.scm.ResolveForRun(r.Context(), grants.Git.ConnectionID)
	if err != nil {
		respondServiceUnavailable(w, r, "scm credential unavailable")
		return
	}
	target, err := url.Parse(scmBase)
	if err != nil || target.Host == "" {
		respondServiceUnavailable(w, r, "scm connection has no base url")
		return
	}
	upstreamPath := singleJoiningSlash(target.Path, owner+"/"+repo+"/"+gitPath)
	proxy := &httputil.ReverseProxy{
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
	}
	proxy.ServeHTTP(w, r)
}

// singleJoiningSlash joins two URL path segments with exactly one slash.
func singleJoiningSlash(a, b string) string {
	a = strings.TrimSuffix(a, "/")
	b = strings.TrimPrefix(b, "/")
	if b == "" {
		return a
	}
	return a + "/" + b
}
