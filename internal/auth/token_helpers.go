package auth

import (
	"database/sql"
	"fmt"
	"strings"

	"windshift/internal/models"

	"golang.org/x/crypto/bcrypt"
)

// scanner is the interface satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// checkTokenFormat validates that a raw token has the expected prefix and minimum length.
func checkTokenFormat(token, prefix string, minLength int) error {
	if !strings.HasPrefix(token, prefix) || len(token) < minLength {
		return fmt.Errorf("invalid token format")
	}
	return nil
}

// verifyTokenHash compares a bcrypt hash against a raw token.
// Returns nil on match, an error otherwise.
func verifyTokenHash(hash, rawToken string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(rawToken))
}

// scanAPITokenListRow scans a row that selects:
//
//	t.id, t.user_id, t.name, t.token_prefix, t.permissions, t.is_temporary,
//	t.expires_at, t.last_used_at, t.created_at, t.updated_at,
//	u.email, u.username
//
// This is the "list" projection used by GetUserTokens, ListAllTokens, and GetTokenByID.
// last review: ser, 210426
func scanAPITokenListRow(s scanner) (models.APIToken, error) {
	var token models.APIToken
	var expiresAt, lastUsedAt sql.NullTime

	err := s.Scan(
		&token.ID, &token.UserID, &token.Name, &token.TokenPrefix, &token.Permissions, &token.IsTemporary,
		&expiresAt, &lastUsedAt, &token.CreatedAt, &token.UpdatedAt,
		&token.UserEmail, &token.UserName,
	)
	if err != nil {
		return token, err
	}

	if expiresAt.Valid {
		token.ExpiresAt = &expiresAt.Time
	}
	if lastUsedAt.Valid {
		token.LastUsedAt = &lastUsedAt.Time
	}

	return token, nil
}

// scanSCIMTokenRow scans the GetTokenByID/ListTokens projection.
func scanSCIMTokenRow(s scanner) (models.SCIMToken, error) {
	var token models.SCIMToken
	var createdBy sql.NullInt64
	var expiresAt, lastUsedAt sql.NullTime

	err := s.Scan(
		&token.ID, &token.Name, &token.TokenPrefix, &token.IsActive,
		&createdBy, &expiresAt, &lastUsedAt, &token.CreatedAt, &token.UpdatedAt,
		&token.CreatedByName,
	)
	if err != nil {
		return token, err
	}

	if createdBy.Valid {
		id := int(createdBy.Int64)
		token.CreatedBy = &id
	}
	if expiresAt.Valid {
		token.ExpiresAt = &expiresAt.Time
	}
	if lastUsedAt.Valid {
		token.LastUsedAt = &lastUsedAt.Time
	}

	return token, nil
}

// scanSCIMTokenValidateRow returns ValidateToken data and its bcrypt hash.
func scanSCIMTokenValidateRow(s scanner) (models.SCIMToken, string, error) {
	var token models.SCIMToken
	var tokenHash string
	var createdBy sql.NullInt64
	var expiresAt, lastUsedAt sql.NullTime

	err := s.Scan(
		&token.ID, &token.Name, &tokenHash, &token.TokenPrefix,
		&token.IsActive, &createdBy, &expiresAt, &lastUsedAt,
		&token.CreatedAt, &token.UpdatedAt, &token.CreatedByName,
	)
	if err != nil {
		return token, "", err
	}

	if createdBy.Valid {
		id := int(createdBy.Int64)
		token.CreatedBy = &id
	}
	if expiresAt.Valid {
		token.ExpiresAt = &expiresAt.Time
	}
	if lastUsedAt.Valid {
		token.LastUsedAt = &lastUsedAt.Time
	}

	return token, tokenHash, nil
}
