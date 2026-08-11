package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
)

// UserSCMTokenRepository contains persistence helpers for user-level SCM OAuth tokens.
type UserSCMTokenRepository struct {
	db database.Database
}

func NewUserSCMTokenRepository(db database.Database) *UserSCMTokenRepository {
	return &UserSCMTokenRepository{db: db}
}

// UserSCMConnection represents a user's connected SCM account.
type UserSCMConnection struct {
	ID           int                    `json:"id"`
	ProviderID   int                    `json:"provider_id"`
	ProviderName string                 `json:"provider_name"`
	ProviderType models.SCMProviderType `json:"provider_type"`
	ProviderSlug string                 `json:"provider_slug"`
	AuthMethod   models.SCMAuthMethod   `json:"auth_method"`
	SCMUsername  string                 `json:"scm_username,omitempty"`
	SCMAvatarURL string                 `json:"scm_avatar_url,omitempty"`
	ConnectedAt  time.Time              `json:"connected_at"`
	LastUsedAt   *time.Time             `json:"last_used_at,omitempty"`
}

// UserSCMProviderInfo is provider metadata returned when a user is not connected.
type UserSCMProviderInfo struct {
	ProviderName string                 `json:"provider_name"`
	ProviderType models.SCMProviderType `json:"provider_type"`
	ProviderSlug string                 `json:"provider_slug"`
	AuthMethod   models.SCMAuthMethod   `json:"auth_method"`
}

// UserSCMProviderStatus represents an OAuth SCM provider plus the current user's connection state.
type UserSCMProviderStatus struct {
	ID           int                    `json:"id"`
	Name         string                 `json:"name"`
	ProviderType models.SCMProviderType `json:"provider_type"`
	Slug         string                 `json:"slug"`
	AuthMethod   models.SCMAuthMethod   `json:"auth_method"`
	IsConnected  bool                   `json:"is_connected"`
	SCMUsername  string                 `json:"scm_username,omitempty"`
	SCMAvatarURL string                 `json:"scm_avatar_url,omitempty"`
	ConnectedAt  *time.Time             `json:"connected_at,omitempty"`
}

// UserSCMRemoteRevokeInfo contains encrypted token material and provider metadata for best-effort remote revocation.
type UserSCMRemoteRevokeInfo struct {
	EncryptedAccessToken  string
	ProviderType          models.SCMProviderType
	OAuthClientID         string
	EncryptedClientSecret string
	BaseURL               string
}

func (r *UserSCMTokenRepository) ListUserConnections(userID int) ([]UserSCMConnection, error) {
	rows, err := r.db.Query(`
		SELECT
			ut.id, ut.scm_provider_id, sp.name, sp.provider_type, sp.slug, sp.auth_method,
			ut.scm_username, ut.scm_avatar_url, ut.connected_at, ut.last_used_at
		FROM user_scm_oauth_tokens ut
		JOIN scm_providers sp ON sp.id = ut.scm_provider_id
		WHERE ut.user_id = ? AND sp.enabled = true
		ORDER BY ut.connected_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	connections := []UserSCMConnection{}
	for rows.Next() {
		conn, err := scanUserSCMConnection(rows)
		if err != nil {
			return nil, err
		}
		connections = append(connections, conn)
	}
	return connections, rows.Err()
}

func (r *UserSCMTokenRepository) GetUserConnection(userID, providerID int) (*UserSCMConnection, error) {
	row := r.db.QueryRow(`
		SELECT
			ut.id, ut.scm_provider_id, sp.name, sp.provider_type, sp.slug, sp.auth_method,
			ut.scm_username, ut.scm_avatar_url, ut.connected_at, ut.last_used_at
		FROM user_scm_oauth_tokens ut
		JOIN scm_providers sp ON sp.id = ut.scm_provider_id
		WHERE ut.user_id = ? AND ut.scm_provider_id = ?
	`, userID, providerID)
	conn, err := scanUserSCMConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &conn, nil
}

func (r *UserSCMTokenRepository) GetEnabledProviderInfo(providerID int) (*UserSCMProviderInfo, error) {
	var p UserSCMProviderInfo
	err := r.db.QueryRow(`
		SELECT name, provider_type, slug, auth_method
		FROM scm_providers WHERE id = ? AND enabled = true
	`, providerID).Scan(&p.ProviderName, &p.ProviderType, &p.ProviderSlug, &p.AuthMethod)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *UserSCMTokenRepository) DeleteUserConnection(userID, providerID int) (bool, error) {
	result, err := r.db.ExecWrite(`
		DELETE FROM user_scm_oauth_tokens
		WHERE user_id = ? AND scm_provider_id = ?
	`, userID, providerID)
	if err != nil {
		return false, err
	}
	rowsAffected, _ := result.RowsAffected()
	return rowsAffected > 0, nil
}

func (r *UserSCMTokenRepository) GetRemoteRevokeInfo(ctx context.Context, userID, providerID int) (*UserSCMRemoteRevokeInfo, error) {
	var encAccessToken sql.NullString
	var clientID, clientSecretEnc, baseURL sql.NullString
	var out UserSCMRemoteRevokeInfo
	err := r.db.QueryRowContext(ctx, `
		SELECT ut.oauth_access_token_encrypted,
		       sp.provider_type, sp.oauth_client_id, sp.oauth_client_secret_encrypted, sp.base_url
		FROM user_scm_oauth_tokens ut
		JOIN scm_providers sp ON sp.id = ut.scm_provider_id
		WHERE ut.user_id = ? AND ut.scm_provider_id = ?
	`, userID, providerID).Scan(&encAccessToken, &out.ProviderType, &clientID, &clientSecretEnc, &baseURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !encAccessToken.Valid || encAccessToken.String == "" || !clientID.Valid || !clientSecretEnc.Valid {
		return nil, ErrNotFound
	}
	out.EncryptedAccessToken = encAccessToken.String
	out.OAuthClientID = clientID.String
	out.EncryptedClientSecret = clientSecretEnc.String
	out.BaseURL = baseURL.String
	return &out, nil
}

func (r *UserSCMTokenRepository) ListAvailableOAuthProviders(userID int) ([]UserSCMProviderStatus, error) {
	rows, err := r.db.Query(`
		SELECT
			sp.id, sp.name, sp.provider_type, sp.slug, sp.auth_method,
			CASE WHEN ut.id IS NOT NULL THEN 1 ELSE 0 END as is_connected,
			ut.scm_username, ut.scm_avatar_url, ut.connected_at
		FROM scm_providers sp
		LEFT JOIN user_scm_oauth_tokens ut ON ut.scm_provider_id = sp.id AND ut.user_id = ?
		WHERE sp.enabled = true
		  AND sp.auth_method = 'oauth'
		  AND (
			COALESCE(sp.workspace_restriction_mode, 'unrestricted') = 'unrestricted'
			OR EXISTS (
				SELECT 1
				FROM scm_provider_workspace_allowlist al
				JOIN user_workspace_roles uwr
				  ON uwr.workspace_id = al.workspace_id
				 AND uwr.user_id = ?
				WHERE al.provider_id = sp.id
			)
		  )
		ORDER BY sp.name
	`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	providers := []UserSCMProviderStatus{}
	for rows.Next() {
		var p UserSCMProviderStatus
		var isConnected int
		var scmUsername, scmAvatarURL sql.NullString
		var connectedAt sql.NullTime
		if err := rows.Scan(
			&p.ID, &p.Name, &p.ProviderType, &p.Slug, &p.AuthMethod,
			&isConnected, &scmUsername, &scmAvatarURL, &connectedAt,
		); err != nil {
			return nil, err
		}
		p.IsConnected = isConnected == 1
		p.SCMUsername = scmUsername.String
		p.SCMAvatarURL = scmAvatarURL.String
		if connectedAt.Valid {
			p.ConnectedAt = &connectedAt.Time
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

type userSCMConnectionScanner interface {
	Scan(dest ...any) error
}

func scanUserSCMConnection(scanner userSCMConnectionScanner) (UserSCMConnection, error) {
	var conn UserSCMConnection
	var scmUsername, scmAvatarURL sql.NullString
	var lastUsedAt sql.NullTime
	if err := scanner.Scan(
		&conn.ID, &conn.ProviderID, &conn.ProviderName, &conn.ProviderType,
		&conn.ProviderSlug, &conn.AuthMethod,
		&scmUsername, &scmAvatarURL, &conn.ConnectedAt, &lastUsedAt,
	); err != nil {
		return conn, err
	}
	conn.SCMUsername = scmUsername.String
	conn.SCMAvatarURL = scmAvatarURL.String
	if lastUsedAt.Valid {
		conn.LastUsedAt = &lastUsedAt.Time
	}
	return conn, nil
}
