package models

import (
	"net/url"
	"strings"
)

// RunGrants is the set of brokered resources a single agent run may reach
// through the secretless access layer (Initiative WI-141 / WI-144). It is
// snapshotted onto the run at claim time (derived from the binding) and
// stored as agent_runs.grants_json; the broker endpoints (git / llm /
// secrets / http) authorize each request against it. The run's minted token
// (agent_runs.run_token_id) is what binds a presented credential to these
// grants — so a leaked token for run A cannot reach run B's resources.
type RunGrants struct {
	Git     *GitGrant `json:"git,omitempty"`
	LLM     *LLMGrant `json:"llm,omitempty"`
	Secrets []int     `json:"secrets,omitempty"` // ActionCredential ids the run may fetch
	HTTP    []string  `json:"http,omitempty"`    // allowed outbound URL prefixes
}

// GitGrant scopes a run's git access to a single repo and the single ref it
// may push (the agent's run branch). Empty Ref means no push is authorized.
// ConnectionID is the SCM connection whose credential the git broker injects
// server-side when proxying to the provider. UserID is the credential
// principal: on OAuth connections the broker injects this user's personal
// token (the run's triggering user, WI-275); 0 means the connection-level
// credential (PAT / GitHub App connections, and legacy runs).
type GitGrant struct {
	Repo         string `json:"repo"`          // "owner/repo"
	Ref          string `json:"ref,omitempty"` // the branch the run may push
	ConnectionID int    `json:"connection_id"` // SCM connection for credential injection
	UserID       int    `json:"user_id,omitempty"`
}

// LLMGrant scopes a run's model access to one connection with an optional
// per-run output-token quota (0 = unlimited).
type LLMGrant struct {
	ConnectionID int `json:"connection_id"`
	QuotaTokens  int `json:"quota_tokens,omitempty"`
}

// AllowsGitRepo reports whether the run may access the given owner/repo.
func (g *RunGrants) AllowsGitRepo(repo string) bool {
	return g != nil && g.Git != nil && g.Git.Repo == repo
}

// AllowsGitPush reports whether the run may push the given ref to repo. The
// push is gated to the single branch named in the grant. git-receive-pack
// always sends the fully-qualified ref ("refs/heads/agent-runs/run-7"), while
// the grant may store that branch either short ("agent-runs/run-7", as the run
// service mints it) or already qualified — so both sides are normalized to
// refs/heads/<branch> before the exact match. A tag or any other ref class
// never collapses onto a branch grant, so this stays a single-branch gate.
func (g *RunGrants) AllowsGitPush(repo, ref string) bool {
	return g.AllowsGitRepo(repo) && g.Git.Ref != "" &&
		qualifyBranchRef(g.Git.Ref) == qualifyBranchRef(ref)
}

// qualifyBranchRef returns ref in fully-qualified form, treating a bare name as
// a branch (refs/heads/<name>). An already-qualified ref (refs/heads/, refs/tags/,
// any refs/*) is returned unchanged, so a branch grant never matches a tag.
func qualifyBranchRef(ref string) string {
	if ref == "" || strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + ref
}

// AllowsSecret reports whether the run may fetch the credential with the
// given id.
func (g *RunGrants) AllowsSecret(id int) bool {
	if g == nil {
		return false
	}
	for _, s := range g.Secrets {
		if s == id {
			return true
		}
	}
	return false
}

// AllowsHTTP reports whether rawURL matches one of the run's allowed grants.
// Deny-by-default: a nil grant or empty pattern never matches.
//
// Matching is on URL-component boundaries, not raw string prefix (WI-168):
// scheme and host:port must be equal and the target path must be the grant
// path or a "/"-delimited sub-path of it. This prevents a grant such as
// "https://api.example.com" from also permitting "https://api.example.com.evil/"
// or "https://api.example.com@169.254.169.254/". A target carrying userinfo
// (user:pass@host) is always rejected.
func (g *RunGrants) AllowsHTTP(rawURL string) bool {
	if g == nil {
		return false
	}
	target, err := url.Parse(rawURL)
	if err != nil || target.Host == "" || target.User != nil {
		return false
	}
	for _, p := range g.HTTP {
		if p == "" {
			continue
		}
		pat, err := url.Parse(p)
		if err != nil || pat.Host == "" {
			continue
		}
		if !strings.EqualFold(target.Scheme, pat.Scheme) {
			continue
		}
		if !strings.EqualFold(target.Host, pat.Host) { // Host includes :port
			continue
		}
		if pathWithinGrant(target.EscapedPath(), pat.EscapedPath()) {
			return true
		}
	}
	return false
}

// pathWithinGrant reports whether targetPath is the grant path or a
// slash-delimited descendant of it. An empty or "/" grant path matches any
// target path (host-level grant).
func pathWithinGrant(targetPath, grantPath string) bool {
	grantPath = strings.TrimSuffix(grantPath, "/")
	if grantPath == "" {
		return true
	}
	if targetPath == grantPath {
		return true
	}
	return strings.HasPrefix(targetPath, grantPath+"/")
}
