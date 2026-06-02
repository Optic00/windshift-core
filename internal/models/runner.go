package models

import "time"

// Runner registration + instance models for the remote-runner control plane
// (Initiative WI-141). These are deliberately NOT api_tokens / ws tokens:
// they authenticate the runner control plane (register / claim / heartbeat /
// result), not the Windshift API. Only hashes are persisted; the plaintext
// secret is shown to its holder exactly once.

const (
	RunnerInstanceStatusActive  = "active"
	RunnerInstanceStatusRevoked = "revoked"
)

// RunnerRegistrationToken is a reusable, pool-scoped, revocable token an
// operator bakes into a runner deployment. A runner presents it to register
// and exchanges it for a per-instance RunnerInstance credential. Revoking it
// stops new registrations without evicting already-registered runners.
type RunnerRegistrationToken struct {
	ID               int        `json:"id"`
	PoolCapabilityID int        `json:"pool_capability_id"`
	TokenPrefix      string     `json:"token_prefix"` // display only; lookup is by hash
	Description      string     `json:"description,omitempty"`
	CreatedByUserID  *int       `json:"created_by_user_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"` // nil = no expiry
	RevokedAt        *time.Time `json:"revoked_at,omitempty"` // nil = active
}

// RunnerInstance is one registered runner: its pool, a per-instance
// control-plane credential (hashed), and heartbeat liveness. Revoking one
// instance evicts a single runner without touching the pool's registration
// token.
type RunnerInstance struct {
	ID               int        `json:"id"`
	PoolCapabilityID int        `json:"pool_capability_id"`
	Name             string     `json:"name,omitempty"`
	Status           string     `json:"status"`
	RegisteredAt     time.Time  `json:"registered_at"`
	LastHeartbeatAt  *time.Time `json:"last_heartbeat_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}
