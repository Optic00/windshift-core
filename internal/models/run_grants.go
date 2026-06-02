package models

import "strings"

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
	Secrets []string  `json:"secrets,omitempty"` // ActionCredential names the run may fetch
	HTTP    []string  `json:"http,omitempty"`    // allowed outbound URL prefixes
}

// GitGrant scopes a run's git access to a single repo and the single ref it
// may push (the agent's run branch). Empty Ref means no push is authorized.
type GitGrant struct {
	Repo string `json:"repo"`          // "owner/repo"
	Ref  string `json:"ref,omitempty"` // the branch the run may push
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

// AllowsGitPush reports whether the run may push the given ref (exact match
// against the single granted ref).
func (g *RunGrants) AllowsGitPush(repo, ref string) bool {
	return g.AllowsGitRepo(repo) && g.Git.Ref != "" && g.Git.Ref == ref
}

// AllowsSecret reports whether the run may fetch the named credential.
func (g *RunGrants) AllowsSecret(name string) bool {
	if g == nil {
		return false
	}
	for _, s := range g.Secrets {
		if s == name {
			return true
		}
	}
	return false
}

// AllowsHTTP reports whether rawURL matches one of the run's allowed
// prefixes. Deny-by-default: a nil grant or empty pattern never matches.
func (g *RunGrants) AllowsHTTP(rawURL string) bool {
	if g == nil {
		return false
	}
	for _, p := range g.HTTP {
		if p != "" && strings.HasPrefix(rawURL, p) {
			return true
		}
	}
	return false
}
