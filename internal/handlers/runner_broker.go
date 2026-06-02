package handlers

import (
	"net/http"

	"windshift/internal/auth"
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
	tokens *auth.TokenManager
	runs   *repository.AgentRunRepository
	creds  *services.ActionCredentialService
}

// NewRunnerBrokerHandler constructs the handler. Any nil dependency disables
// the broker (503), e.g. when the harness is not configured.
func NewRunnerBrokerHandler(tokens *auth.TokenManager, runs *repository.AgentRunRepository, creds *services.ActionCredentialService) *RunnerBrokerHandler {
	return &RunnerBrokerHandler{tokens: tokens, runs: runs, creds: creds}
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

	// Authenticate the per-run token.
	token := bearerCredential(r)
	if token == "" {
		respondUnauthorized(w, r)
		return
	}
	_, apiToken, err := h.tokens.ValidateToken(token)
	if err != nil || apiToken == nil {
		respondUnauthorized(w, r)
		return
	}

	// Authorize against the run: the token must be the one bound to the run,
	// the run must be running, and the credential must be granted.
	boundTokenID, workspaceID, grants, status, err := h.runs.GetRunAuthz(r.Context(), runID)
	if err != nil {
		respondNotFound(w, r, "agent run")
		return
	}
	if apiToken.ID != boundTokenID || status != models.AgentRunStatusRunning || !grants.AllowsSecret(credID) {
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
