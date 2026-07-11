package webauthn

import (
	"path/filepath"
	"testing"

	webauthnlib "github.com/go-webauthn/webauthn/webauthn"

	"windshift/internal/database"
)

func TestAuthenticationSessionBinding(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "webauthn-sessions.db"))
	if err != nil {
		t.Fatalf("NewSQLiteDB: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	result, err := db.ExecWrite(`
		INSERT INTO users (email, username, first_name, last_name, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, "bound-session@example.com", "bound-session", "Bound", "Session")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID64, _ := result.LastInsertId()
	userID := int(userID64)

	store := NewSessionStore(db)
	data := &webauthnlib.SessionData{Challenge: "bound-challenge", UserID: []byte("1")}
	sessionID, err := store.SaveAuthenticationSessionBound(userID, 42, data)
	if err != nil {
		t.Fatalf("SaveAuthenticationSessionBound: %v", err)
	}
	if _, err := store.GetAuthenticationSession(sessionID); err == nil {
		t.Fatal("unbound lookup consumed a bound authentication challenge")
	}
	if _, err := store.GetAuthenticationSessionBound(sessionID, 41); err == nil {
		t.Fatal("different pending session consumed a bound authentication challenge")
	}
	got, err := store.GetAuthenticationSessionBound(sessionID, 42)
	if err != nil {
		t.Fatalf("GetAuthenticationSessionBound: %v", err)
	}
	if got.Challenge != data.Challenge {
		t.Fatalf("challenge = %q, want %q", got.Challenge, data.Challenge)
	}
	if _, err := store.GetAuthenticationSessionBound(sessionID, 42); err == nil {
		t.Fatal("bound authentication challenge was reusable")
	}
}
