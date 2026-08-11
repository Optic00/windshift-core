package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"windshift/internal/database"
)

// OAuthClientRepository persists OAuth client registrations.
type OAuthClientRepository struct {
	db database.Database
}

// NewOAuthClientRepository constructs an OAuth client repository.
func NewOAuthClientRepository(db database.Database) *OAuthClientRepository {
	return &OAuthClientRepository{db: db}
}

// EnabledByID returns ErrNotFound when the client does not exist.
func (r *OAuthClientRepository) EnabledByID(id int) (bool, error) {
	var enabled bool
	err := r.db.QueryRow("SELECT enabled FROM oauth_clients WHERE id = ?", id).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("get OAuth client %d: %w", id, err)
	}
	return enabled, nil
}

// CreateDynamicPublicClient inserts a dynamically registered public OAuth
// client. A generated client-ID collision is returned as ErrDuplicateEntry.
func (r *OAuthClientRepository) CreateDynamicPublicClient(
	slug, displayName, clientID, redirectsJSON, scopesJSON, resourceURI string,
) error {
	_, err := r.db.ExecWrite(`
		INSERT INTO oauth_clients (
			slug, display_name, client_id, client_secret_hash, client_type,
			redirect_uris, allowed_scopes, resource_uri, enabled, created_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, slug, displayName, clientID, sql.NullString{}, "public", redirectsJSON,
		scopesJSON, resourceURI, true, nil)
	if err != nil {
		if database.IsUniqueConstraintError(err) {
			return ErrDuplicateEntry
		}
		return fmt.Errorf("insert dynamic OAuth client: %w", err)
	}
	return nil
}
