package services

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"windshift/internal/auth"
	"windshift/internal/database"
)

func newTokenTestDB(t *testing.T) (database.Database, int) {
	t.Helper()
	dsn := fmt.Sprintf("file:run-token-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := database.NewSQLiteDB(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("init db: %v", err)
	}
	res, err := db.Exec(`INSERT INTO users(email, username, first_name, last_name) VALUES (?, ?, ?, '')`,
		"agent@example.com", "agent", "Agent")
	if err != nil {
		t.Fatalf("insert acting user: %v", err)
	}
	id64, _ := res.LastInsertId()
	return db, int(id64)
}

func TestRunTokenService_MintDefaults(t *testing.T) {
	db, uid := newTokenTestDB(t)
	tm := auth.NewTokenManager(db, nil)
	svc, err := NewRunTokenService(tm)
	if err != nil {
		t.Fatalf("new svc: %v", err)
	}

	before := time.Now().UTC()
	res, err := svc.Mint(context.Background(), MintRequest{ActingUserID: uid})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if res.Token == "" {
		t.Fatal("token must not be empty")
	}
	if res.TokenID == 0 {
		t.Fatal("token id must be set")
	}
	if res.ExpiresAt.Before(before.Add(50 * time.Minute)) {
		t.Errorf("default TTL should be ~1h, got expiry %v (now=%v)", res.ExpiresAt, before)
	}

	// Verify the api_tokens row is marked temporary so it never surfaces
	// in user/admin token listings.
	var temporary bool
	if err := db.QueryRow(`SELECT is_temporary FROM api_tokens WHERE id = ?`, res.TokenID).Scan(&temporary); err != nil {
		t.Fatalf("read is_temporary: %v", err)
	}
	if !temporary {
		t.Error("minted token must have IsTemporary=true")
	}

	// Verify it carries the DefaultAgentScopes via the token-validation
	// path — round-tripping the bearer back to the TokenManager confirms
	// the scopes were persisted correctly.
	_, apiTok, err := tm.ValidateToken(res.Token)
	if err != nil {
		t.Fatalf("validate minted token: %v", err)
	}
	if !strings.Contains(apiTok.Permissions, "items:read") {
		t.Errorf("expected DefaultAgentScopes to include items:read; got %q", apiTok.Permissions)
	}
}

func TestRunTokenService_MintRejectsBadScopes(t *testing.T) {
	db, uid := newTokenTestDB(t)
	tm := auth.NewTokenManager(db, nil)
	svc, _ := NewRunTokenService(tm)

	_, err := svc.Mint(context.Background(), MintRequest{
		ActingUserID: uid,
		Scopes:       []string{"items:read", "nonsense:scope"},
	})
	if err == nil {
		t.Fatal("expected validation error for bogus scope")
	}
	if !strings.Contains(err.Error(), "invalid scopes") {
		t.Errorf("expected invalid-scopes error, got %v", err)
	}
}

func TestRunTokenService_MintRespectsTTL(t *testing.T) {
	db, uid := newTokenTestDB(t)
	tm := auth.NewTokenManager(db, nil)
	svc, _ := NewRunTokenService(tm)

	before := time.Now().UTC()
	res, err := svc.Mint(context.Background(), MintRequest{
		ActingUserID: uid,
		TTL:          5 * time.Minute,
		Name:         "agent-run:42",
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	delta := res.ExpiresAt.Sub(before)
	if delta < 4*time.Minute || delta > 6*time.Minute {
		t.Errorf("ExpiresAt outside ~5m window: delta=%v", delta)
	}
}
