package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"windshift/internal/models"
	"windshift/internal/repository"
)

// RunnerRegistryService owns the remote-runner control-plane credential
// lifecycle (Initiative WI-141): minting pool registration tokens, exchanging
// them for per-instance runner credentials, authenticating runner calls, and
// revocation.
//
// Hashing note: registration tokens and runner credentials are high-entropy
// (256-bit) machine secrets, not user passwords, so they are stored as a
// plain SHA-256 hash and looked up deterministically by that hash. bcrypt
// (used elsewhere for low-entropy user tokens) would add cost without
// security here and would force a prefix-then-verify scan; SHA-256 matches
// the UNIQUE hash columns the schema declares. Plaintext is returned to its
// holder exactly once at creation.
type RunnerRegistryService struct {
	repo *repository.RunnerRepository
	now  func() time.Time

	// nextRunner cursor: pool → offset into the live-runner list. Guards the
	// round-robin assignment (WI-514) so every live runner gets a turn before
	// any is offered work twice. Read/modified under nextRunnerMu.
	nextRunnerMu sync.Mutex
	nextRunner   map[int]int
}

// NewRunnerRegistryService constructs the service.
func NewRunnerRegistryService(repo *repository.RunnerRepository, now func() time.Time) *RunnerRegistryService {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RunnerRegistryService{repo: repo, now: now, nextRunner: map[int]int{}}
}

const (
	runnerRegistrationTokenPrefix = "wsrt_" // windshift runner registration token
	runnerCredentialPrefix        = "wsrc_" // windshift runner credential
	runnerTokenBodyBytes          = 32      // 256 bits of entropy
)

// ErrInvalidRegistrationToken is returned when a presented registration token
// is malformed, unknown, revoked, or expired.
var ErrInvalidRegistrationToken = errors.New("runner registry: invalid or expired registration token")

// ErrRunnerPoolUnavailable means the token/instance points at a pool that was
// deleted, disabled, or is no longer a runner_pool capability.
var ErrRunnerPoolUnavailable = errors.New("runner registry: runner pool is unavailable")

// ErrRunnerUnauthenticated is returned when a presented runner credential
// does not match an active runner instance.
var ErrRunnerUnauthenticated = errors.New("runner registry: unauthenticated runner")

// MintRegistrationToken creates a single-use, pool-scoped registration token
// (consumed on the first successful registration; see Register). The plaintext
// is returned exactly once; only its hash is persisted. ttl<=0 mints a
// non-expiring token, but the handler applies a default TTL so tokens expire by
// default (WI-238 security Phase 6).
func (s *RunnerRegistryService) MintRegistrationToken(ctx context.Context, poolID int, createdBy *int, description string, ttl time.Duration) (string, *models.RunnerRegistrationToken, error) {
	enabled, err := s.repo.IsEnabledRunnerPool(ctx, poolID)
	if err != nil {
		return "", nil, err
	}
	if !enabled {
		return "", nil, ErrRunnerPoolUnavailable
	}

	full, hash, prefix, err := generateRunnerToken(runnerRegistrationTokenPrefix)
	if err != nil {
		return "", nil, err
	}
	var expiresAt *time.Time
	if ttl > 0 {
		t := s.now().Add(ttl)
		expiresAt = &t
	}
	id, err := s.repo.InsertRegistrationToken(ctx, poolID, hash, prefix, description, createdBy, expiresAt)
	if err != nil {
		return "", nil, err
	}
	return full, &models.RunnerRegistrationToken{
		ID:               id,
		PoolCapabilityID: poolID,
		TokenPrefix:      prefix,
		Description:      description,
		CreatedByUserID:  createdBy,
		CreatedAt:        s.now(),
		ExpiresAt:        expiresAt,
	}, nil
}

// Register exchanges a valid registration token for a fresh per-instance
// runner credential bound to the token's pool. The plaintext credential is
// returned exactly once. Returns ErrInvalidRegistrationToken if the token is
// not currently valid.
//
// Registration tokens are single-use (WI-238 security Phase 6): the token is
// consumed (revoked) atomically as part of registering, so a leaked or shared
// token cannot be replayed to register additional instances. A runner reuses
// its per-instance credential across restarts, so it never needs the token
// again; scaling a fleet means one token per runner, or injecting credentials
// directly.
func (s *RunnerRegistryService) Register(ctx context.Context, registrationToken, name string) (string, *models.RunnerInstance, error) {
	if !strings.HasPrefix(registrationToken, runnerRegistrationTokenPrefix) {
		return "", nil, ErrInvalidRegistrationToken
	}
	tok, err := s.repo.GetActiveRegistrationTokenByHash(ctx, hashRunnerToken(registrationToken), s.now())
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrInvalidRegistrationToken
	}
	if err != nil {
		return "", nil, fmt.Errorf("register runner: lookup token: %w", err)
	}
	enabled, err := s.repo.IsEnabledRunnerPool(ctx, tok.PoolCapabilityID)
	if err != nil {
		return "", nil, fmt.Errorf("register runner: validate pool: %w", err)
	}
	if !enabled {
		return "", nil, ErrRunnerPoolUnavailable
	}

	// Claim the token single-use before minting anything. If we didn't win the
	// claim (already used / concurrent registration), reject — the token is
	// spent.
	consumed, err := s.repo.ConsumeRegistrationToken(ctx, tok.ID, s.now())
	if err != nil {
		return "", nil, fmt.Errorf("register runner: consume token: %w", err)
	}
	if !consumed {
		return "", nil, ErrInvalidRegistrationToken
	}

	cred, credHash, _, err := generateRunnerToken(runnerCredentialPrefix)
	if err != nil {
		return "", nil, err
	}
	now := s.now()
	id, err := s.repo.InsertInstance(ctx, tok.PoolCapabilityID, name, credHash, now)
	if err != nil {
		return "", nil, fmt.Errorf("register runner: insert instance: %w", err)
	}
	return cred, &models.RunnerInstance{
		ID:               id,
		PoolCapabilityID: tok.PoolCapabilityID,
		Name:             name,
		Status:           models.RunnerInstanceStatusActive,
		RegisteredAt:     now,
	}, nil
}

// Authenticate resolves a presented runner credential to its active instance,
// or returns ErrRunnerUnauthenticated. Callers use the returned instance's
// PoolCapabilityID to scope claims and the ID to stamp runs / heartbeats.
func (s *RunnerRegistryService) Authenticate(ctx context.Context, credential string) (*models.RunnerInstance, error) {
	if !strings.HasPrefix(credential, runnerCredentialPrefix) {
		return nil, ErrRunnerUnauthenticated
	}
	inst, err := s.repo.GetActiveInstanceByCredentialHash(ctx, hashRunnerToken(credential))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunnerUnauthenticated
	}
	if err != nil {
		return nil, fmt.Errorf("authenticate runner: %w", err)
	}
	return inst, nil
}

// Heartbeat records liveness for a runner instance.
func (s *RunnerRegistryService) Heartbeat(ctx context.Context, instanceID int) error {
	return s.repo.TouchHeartbeat(ctx, instanceID, s.now())
}

// ListRegistrationTokens returns every registration token for a pool (active,
// revoked, and expired), newest-first. For the admin lifecycle surface.
func (s *RunnerRegistryService) ListRegistrationTokens(ctx context.Context, poolID int) ([]*models.RunnerRegistrationToken, error) {
	return s.repo.ListRegistrationTokensForPool(ctx, poolID)
}

// ListInstances returns every runner instance registered against a pool
// (active and revoked), newest-first. For the admin lifecycle surface.
func (s *RunnerRegistryService) ListInstances(ctx context.Context, poolID int) ([]*models.RunnerInstance, error) {
	return s.repo.ListInstancesForPool(ctx, poolID)
}

// RevokeRegistrationToken disables a registration token (stops new
// registrations; does not evict already-registered runners).
func (s *RunnerRegistryService) RevokeRegistrationToken(ctx context.Context, id int) error {
	return s.repo.RevokeRegistrationToken(ctx, id, s.now())
}

// RevokeInstance evicts a single runner.
func (s *RunnerRegistryService) RevokeInstance(ctx context.Context, id int) error {
	return s.repo.RevokeInstance(ctx, id, s.now())
}

// generateRunnerToken mints a prefixed, high-entropy token and returns the
// full plaintext, its SHA-256 hash (for storage/lookup), and a short display
// prefix (for admin identification only).
func generateRunnerToken(prefix string) (full, hash, displayPrefix string, err error) {
	b := make([]byte, runnerTokenBodyBytes)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("generate runner token: %w", err)
	}
	full = prefix + hex.EncodeToString(b)
	hash = hashRunnerToken(full)
	displayPrefix = full[:len(prefix)+8]
	return full, hash, displayPrefix, nil
}

// hashRunnerToken returns the hex SHA-256 of a runner token/credential.
func hashRunnerToken(full string) string {
	sum := sha256.Sum256([]byte(full))
	return hex.EncodeToString(sum[:])
}

// NextRunner picks the live runner in poolID whose turn it is under round-robin
// (WI-514) and rotates the cursor so the next call yields a different one.
// Candidates are the pool's live runners (fresh heartbeat); offset indexes
// into them oldest-registered-first (ListLiveInstancesForPool), so the turn
// order is stable across calls: A, B, A, B, …. Returns ErrNoLiveRunner when no
// live runner is registered for the pool. The lock scope is deliberately
// tiny (query + cursor advance): it serializes cursor rotation, not the claim.
func (s *RunnerRegistryService) NextRunner(ctx context.Context, poolID int) (*models.RunnerInstance, error) {
	freshSince := s.now().Add(-models.RunnerLivenessWindow)
	live, err := s.repo.ListLiveInstancesForPool(ctx, poolID, freshSince)
	if err != nil {
		return nil, fmt.Errorf("next runner: list live: %w", err)
	}
	if len(live) == 0 {
		return nil, ErrNoLiveRunner
	}
	s.nextRunnerMu.Lock()
	offset := s.nextRunner[poolID] % len(live)
	s.nextRunner[poolID] = offset + 1
	s.nextRunnerMu.Unlock()
	return live[offset], nil
}

// ErrNoLiveRunner is returned by NextRunner when no live runner is
// registered for the pool (none with a heartbeat within the liveness window).
var ErrNoLiveRunner = errors.New("runner registry: no live runner for pool")
