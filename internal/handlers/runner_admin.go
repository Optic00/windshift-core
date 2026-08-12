package handlers

import (
	"errors"
	"io"
	"net/http"
	"time"

	"windshift/internal/models"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

// Runner-pool admin lifecycle (WI-177). These endpoints hang off the
// runner_pool capability — /admin/action-capabilities/{capabilityId}/... — so
// registration tokens and runner instances are managed as child resources of
// the pool they belong to. They are gated by the admin middleware in routing
// (distinct from the per-instance runner credential the control plane uses);
// here we only resolve and scope to the pool capability.

// resolveRunnerPool validates that {capabilityId} names an existing
// runner_pool capability and returns its id. Writes 503/400/404 and returns
// ok=false otherwise.
func (h *RunnerControlHandler) resolveRunnerPool(w http.ResponseWriter, r *http.Request) (int, bool) {
	if h.registry == nil || h.caps == nil {
		respondServiceUnavailable(w, r, "coding-agent harness is disabled on this server")
		return 0, false
	}
	poolID, ok := requireIDParam(w, r, "capabilityId")
	if !ok {
		return 0, false
	}
	capRow, err := h.caps.GetCapabilityByID(poolID)
	if err != nil || capRow == nil || capRow.CapabilityType != models.CapabilityRunnerPool {
		// Don't distinguish "not found" from "wrong type" — both are "no such
		// runner pool" from the caller's perspective.
		respondNotFound(w, r, "runner pool")
		return 0, false
	}
	return poolID, true
}

type mintRunnerTokenRequest struct {
	Description string `json:"description"`
	// TTLHours bounds the token's validity. 0 (unspecified) applies
	// defaultRunnerTokenTTLHours so a token always expires by default (WI-238
	// security Phase 6); set a large value for a long-lived token.
	TTLHours int `json:"ttl_hours"`
}

// defaultRunnerTokenTTLHours is the validity applied to a registration token
// when the caller does not specify one — 30 days. Registration tokens should
// expire by default rather than live forever (WI-238 security Phase 6).
const defaultRunnerTokenTTLHours = 24 * 30

// mintRunnerTokenResponse returns the plaintext token exactly once, alongside
// the persisted (hash-only) metadata. InstallCommand is the complete
// copy-paste host-onboarding one-liner (WI-309): the hosted install script
// (WI-313) already bakes in the public WS_API_URL and version-matched image
// references, so token + script URL is everything a fresh host needs.
type mintRunnerTokenResponse struct {
	Token          string `json:"token"`
	InstallCommand string `json:"install_command"`
	*models.RunnerRegistrationToken
}

// MintRunnerToken mints a pool-scoped registration token a runner uses to
// self-register. POST /admin/action-capabilities/{capabilityId}/runner-tokens.
// The plaintext token is returned once and never again. The token is single-use
// (consumed on the first successful registration) and expires by default
// (WI-238 security Phase 6), so mint one token per runner or inject credentials
// directly for a fleet.
func (h *RunnerControlHandler) MintRunnerToken(w http.ResponseWriter, r *http.Request) {
	poolID, ok := h.resolveRunnerPool(w, r)
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	var req mintRunnerTokenRequest
	// Body is optional: a bare POST mints a non-expiring, undescribed token,
	// so an empty body (io.EOF) is not an error.
	if err := newJSONDecoder(w, r).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		respondBadRequest(w, r, "invalid request body")
		return
	}
	// Description renders in the pool's token list.
	sanitize.Apply(&req.Description, sanitize.PlainTextField)
	if req.TTLHours < 0 {
		respondBadRequest(w, r, "ttl_hours cannot be negative")
		return
	}
	ttlHours := req.TTLHours
	if ttlHours == 0 {
		ttlHours = defaultRunnerTokenTTLHours
	}
	createdBy := user.ID
	full, tok, err := h.registry.MintRegistrationToken(
		r.Context(), poolID, &createdBy, req.Description, time.Duration(ttlHours)*time.Hour,
	)
	if errors.Is(err, services.ErrRunnerPoolUnavailable) {
		respondConflict(w, r, "runner pool is disabled")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONCreated(w, mintRunnerTokenResponse{
		Token:                   full,
		InstallCommand:          runnerInstallCommand(apiBaseURLFor(h.baseURL, r), full),
		RunnerRegistrationToken: tok,
	})
}

// ListRunnerTokens lists every registration token for a pool (active, revoked,
// expired). GET /admin/action-capabilities/{capabilityId}/runner-tokens.
func (h *RunnerControlHandler) ListRunnerTokens(w http.ResponseWriter, r *http.Request) {
	poolID, ok := h.resolveRunnerPool(w, r)
	if !ok {
		return
	}
	tokens, err := h.registry.ListRegistrationTokens(r.Context(), poolID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if tokens == nil {
		tokens = []*models.RunnerRegistrationToken{}
	}
	respondJSONOK(w, tokens)
}

// RevokeRunnerToken revokes a registration token belonging to the pool.
// DELETE /admin/action-capabilities/{capabilityId}/runner-tokens/{tokenId}.
func (h *RunnerControlHandler) RevokeRunnerToken(w http.ResponseWriter, r *http.Request) {
	poolID, ok := h.resolveRunnerPool(w, r)
	if !ok {
		return
	}
	tokenID, ok := requireIDParam(w, r, "tokenId")
	if !ok {
		return
	}
	// Scope the revoke to the pool in the path so a token id from another pool
	// can't be revoked through this pool's URL.
	tokens, err := h.registry.ListRegistrationTokens(r.Context(), poolID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !containsID(tokenIDs(tokens), tokenID) {
		respondNotFound(w, r, "registration token")
		return
	}
	if err := h.registry.RevokeRegistrationToken(r.Context(), tokenID); err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, map[string]any{"id": tokenID, "revoked": true})
}

// ListRunnerInstances lists every runner instance for a pool (active and
// revoked). GET /admin/action-capabilities/{capabilityId}/runner-instances.
func (h *RunnerControlHandler) ListRunnerInstances(w http.ResponseWriter, r *http.Request) {
	poolID, ok := h.resolveRunnerPool(w, r)
	if !ok {
		return
	}
	instances, err := h.registry.ListInstances(r.Context(), poolID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if instances == nil {
		instances = []*models.RunnerInstance{}
	}
	respondJSONOK(w, instances)
}

// RevokeRunnerInstance evicts a single runner from the pool.
// DELETE /admin/action-capabilities/{capabilityId}/runner-instances/{instanceId}.
func (h *RunnerControlHandler) RevokeRunnerInstance(w http.ResponseWriter, r *http.Request) {
	poolID, ok := h.resolveRunnerPool(w, r)
	if !ok {
		return
	}
	instanceID, ok := requireIDParam(w, r, "instanceId")
	if !ok {
		return
	}
	instances, err := h.registry.ListInstances(r.Context(), poolID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !containsID(instanceIDs(instances), instanceID) {
		respondNotFound(w, r, "runner instance")
		return
	}
	if err := h.registry.RevokeInstance(r.Context(), instanceID); err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, map[string]any{"id": instanceID, "revoked": true})
}

func tokenIDs(tokens []*models.RunnerRegistrationToken) []int {
	ids := make([]int, len(tokens))
	for i, t := range tokens {
		ids[i] = t.ID
	}
	return ids
}

func instanceIDs(instances []*models.RunnerInstance) []int {
	ids := make([]int, len(instances))
	for i, inst := range instances {
		ids[i] = inst.ID
	}
	return ids
}

func containsID(ids []int, id int) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
