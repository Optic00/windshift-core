package repository

import (
	"database/sql"
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
