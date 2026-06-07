package llm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/sso"
)

// ErrFeatureDisabled is returned when an AI feature has been disabled by admin.
var ErrFeatureDisabled = errors.New("this AI feature is disabled by your administrator")

// ConnectionInfo represents an LLM connection without sensitive fields.
type ConnectionInfo struct {
	ID           int          `json:"id"`
	Name         string       `json:"name"`
	ProviderType ProviderType `json:"provider_type"`
	Model        string       `json:"model"`
	HasAPIKey    bool         `json:"has_api_key"`
	BaseURL      string       `json:"base_url,omitempty"`
	IsDefault    bool         `json:"is_default"`
	IsEnabled    bool         `json:"is_enabled"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// ConnectionManager bridges the database and the LLM client layer.
type ConnectionManager struct {
	db                  database.Database
	encryption          *sso.SecretEncryption
	fallback            Client
	allowedPrivateCIDRs []*net.IPNet
}

// NewConnectionManager creates a new connection manager.
func NewConnectionManager(db database.Database, encryption *sso.SecretEncryption, fallback Client, allowedPrivateCIDRs []*net.IPNet) *ConnectionManager {
	return &ConnectionManager{
		db:                  db,
		encryption:          encryption,
		fallback:            fallback,
		allowedPrivateCIDRs: allowedPrivateCIDRs,
	}
}

// Resolve returns a Client for the given connection ID.
// If connectionID > 0, uses that specific enabled connection.
// Otherwise, picks the default enabled connection (or the first enabled one).
// Falls back to the env-var-based client if no DB connections exist.
func (m *ConnectionManager) Resolve(connectionID int) (Client, error) {
	var row *sql.Row
	if connectionID > 0 {
		row = m.db.QueryRow(
			`SELECT id, provider_type, model, api_key_encrypted, base_url
			 FROM llm_connections
			 WHERE id = ? AND is_enabled = true`,
			connectionID,
		)
	} else {
		row = m.db.QueryRow(
			`SELECT id, provider_type, model, api_key_encrypted, base_url
			 FROM llm_connections
			 WHERE is_enabled = true
			 ORDER BY is_default DESC, id ASC
			 LIMIT 1`,
		)
	}

	var id int
	var providerType, model string
	var apiKeyEncrypted, baseURL sql.NullString
	err := row.Scan(&id, &providerType, &model, &apiKeyEncrypted, &baseURL)
	if errors.Is(err, sql.ErrNoRows) {
		if connectionID > 0 {
			return nil, fmt.Errorf("LLM connection %d not found or disabled", connectionID)
		}
		// No DB connections configured — fall back to the env-var client
		return m.fallback, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query connection: %w", err)
	}

	var apiKey string
	if apiKeyEncrypted.Valid && apiKeyEncrypted.String != "" {
		apiKey, err = m.encryption.Decrypt(apiKeyEncrypted.String)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt API key: %w", err)
		}
	}

	return NewProviderClient(ConnectionConfig{
		ProviderType:        ProviderType(providerType),
		Model:               model,
		APIKey:              apiKey,
		BaseURL:             baseURL.String,
		AllowedPrivateCIDRs: m.allowedPrivateCIDRs,
	}), nil
}

// ConnectionRuntimeConfig contains the decrypted runtime fields needed to
// resolve the admin-selected provider for a coding-agent run (the model id for
// the agent container; the key + base URL stay server-side in the llm-proxy).
type ConnectionRuntimeConfig struct {
	ProviderType string
	APIFormat    string
	Model        string
	APIKey       string
	BaseURL      string
}

// ConnectionRuntime returns the runtime config for one enabled connection. It
// is intentionally narrower than GetConnection and is only used after callers
// have already authorized access to the selected connection.
func (m *ConnectionManager) ConnectionRuntime(ctx context.Context, connectionID int) (*ConnectionRuntimeConfig, error) {
	cfg := &ConnectionRuntimeConfig{}
	var apiKeyEncrypted, baseURLNull sql.NullString
	err := m.db.QueryRowContext(ctx,
		`SELECT provider_type, model, api_key_encrypted, base_url
		 FROM llm_connections
		 WHERE id = ? AND is_enabled = true`,
		connectionID,
	).Scan(&cfg.ProviderType, &cfg.Model, &apiKeyEncrypted, &baseURLNull)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("LLM connection %d not found or disabled", connectionID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query connection runtime: %w", err)
	}
	provider := GetProvider(ProviderType(cfg.ProviderType))
	if provider == nil {
		return nil, fmt.Errorf("unknown LLM provider type %q", cfg.ProviderType)
	}
	cfg.APIFormat = provider.APIFormat
	if apiKeyEncrypted.Valid && apiKeyEncrypted.String != "" {
		cfg.APIKey, err = m.encryption.Decrypt(apiKeyEncrypted.String)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt API key: %w", err)
		}
	}
	if baseURLNull.Valid {
		cfg.BaseURL = baseURLNull.String
	}
	return cfg, nil
}

// ListConnections returns all connections (without secrets) for admin listing.
func (m *ConnectionManager) ListConnections() ([]ConnectionInfo, error) {
	rows, err := m.db.Query(
		`SELECT id, name, provider_type, model, api_key_encrypted, base_url, is_default, is_enabled, created_at, updated_at
		 FROM llm_connections ORDER BY is_default DESC, name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list connections: %w", err)
	}
	defer rows.Close()

	return scanConnections(rows)
}

// PublicConnectionInfo is the slim, user-facing view of an LLM connection.
// It deliberately omits admin-only fields (BaseURL, HasAPIKey, timestamps,
// IsEnabled) so the user dropdown endpoint can't leak infrastructure URLs
// — see bughunt8 finding 4.
type PublicConnectionInfo struct {
	ID           int          `json:"id"`
	Name         string       `json:"name"`
	ProviderType ProviderType `json:"provider_type"`
	Model        string       `json:"model"`
	IsDefault    bool         `json:"is_default"`
}

// ListEnabledPublic returns the slim, user-facing view of all enabled
// connections. It's the user-facing counterpart of ListConnections
// (which is admin-only and returns the full ConnectionInfo).
func (m *ConnectionManager) ListEnabledPublic() ([]PublicConnectionInfo, error) {
	rows, err := m.db.Query(
		`SELECT id, name, provider_type, model, is_default
		 FROM llm_connections
		 WHERE is_enabled = true
		 ORDER BY is_default DESC, name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list enabled connections: %w", err)
	}
	defer rows.Close()

	var out []PublicConnectionInfo
	for rows.Next() {
		var c PublicConnectionInfo
		var providerType string
		if err := rows.Scan(&c.ID, &c.Name, &providerType, &c.Model, &c.IsDefault); err != nil {
			return nil, fmt.Errorf("failed to scan connection: %w", err)
		}
		c.ProviderType = ProviderType(providerType)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connections: %w", err)
	}
	return out, nil
}

// GetConnection returns a single connection by ID.
func (m *ConnectionManager) GetConnection(id int) (*ConnectionInfo, error) {
	var c ConnectionInfo
	var apiKeyEncrypted, baseURL sql.NullString
	err := m.db.QueryRow(
		`SELECT id, name, provider_type, model, api_key_encrypted, base_url, is_default, is_enabled, created_at, updated_at
		 FROM llm_connections WHERE id = ?`, id,
	).Scan(&c.ID, &c.Name, &c.ProviderType, &c.Model, &apiKeyEncrypted, &baseURL, &c.IsDefault, &c.IsEnabled, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}
	c.HasAPIKey = apiKeyEncrypted.Valid && apiKeyEncrypted.String != ""
	if baseURL.Valid {
		c.BaseURL = baseURL.String
	}
	return &c, nil
}

// CreateConnectionRequest is the input for creating a connection.
type CreateConnectionRequest struct {
	Name         string       `json:"name"`
	ProviderType ProviderType `json:"provider_type"`
	Model        string       `json:"model"`
	APIKey       string       `json:"api_key,omitempty"`
	BaseURL      string       `json:"base_url,omitempty"`
	IsDefault    bool         `json:"is_default"`
	IsEnabled    bool         `json:"is_enabled"`
}

// CreateConnection creates a new LLM connection.
func (m *ConnectionManager) CreateConnection(req CreateConnectionRequest) (*ConnectionInfo, error) {
	var encryptedKey sql.NullString
	if req.APIKey != "" {
		encrypted, err := m.encryption.Encrypt(req.APIKey)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt API key: %w", err)
		}
		encryptedKey = sql.NullString{String: encrypted, Valid: true}
	}

	var baseURL sql.NullString
	if req.BaseURL != "" {
		baseURL = sql.NullString{String: req.BaseURL, Valid: true}
	}

	// If setting as default, clear existing defaults
	if req.IsDefault {
		if _, err := m.db.Exec("UPDATE llm_connections SET is_default = false WHERE is_default = true"); err != nil {
			return nil, fmt.Errorf("failed to clear existing defaults: %w", err)
		}
	}

	var id int64
	err := m.db.QueryRow(
		`INSERT INTO llm_connections (name, provider_type, model, api_key_encrypted, base_url, is_default, is_enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		req.Name, string(req.ProviderType), req.Model, encryptedKey, baseURL, req.IsDefault, req.IsEnabled,
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection: %w", err)
	}

	return m.GetConnection(int(id))
}

// UpdateConnectionRequest is the input for updating a connection.
type UpdateConnectionRequest struct {
	Name         string       `json:"name"`
	ProviderType ProviderType `json:"provider_type"`
	Model        string       `json:"model"`
	APIKey       string       `json:"api_key,omitempty"`
	BaseURL      string       `json:"base_url,omitempty"`
	IsDefault    bool         `json:"is_default"`
	IsEnabled    bool         `json:"is_enabled"`
}

// UpdateConnection updates an existing LLM connection.
func (m *ConnectionManager) UpdateConnection(id int, req UpdateConnectionRequest) (*ConnectionInfo, error) {
	// If setting as default, clear existing defaults
	if req.IsDefault {
		if _, err := m.db.Exec("UPDATE llm_connections SET is_default = false WHERE is_default = true AND id != ?", id); err != nil {
			return nil, fmt.Errorf("failed to clear existing defaults: %w", err)
		}
	}

	if req.APIKey != "" {
		encrypted, err := m.encryption.Encrypt(req.APIKey)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt API key: %w", err)
		}
		_, err = m.db.Exec(
			`UPDATE llm_connections SET name = ?, provider_type = ?, model = ?, api_key_encrypted = ?, base_url = ?, is_default = ?, is_enabled = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`,
			req.Name, string(req.ProviderType), req.Model, encrypted, req.BaseURL, req.IsDefault, req.IsEnabled, id,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to update connection: %w", err)
		}
	} else {
		// Don't overwrite API key if not provided
		_, err := m.db.Exec(
			`UPDATE llm_connections SET name = ?, provider_type = ?, model = ?, base_url = ?, is_default = ?, is_enabled = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`,
			req.Name, string(req.ProviderType), req.Model, req.BaseURL, req.IsDefault, req.IsEnabled, id,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to update connection: %w", err)
		}
	}

	return m.GetConnection(id)
}

// DeleteConnection deletes an LLM connection.
func (m *ConnectionManager) DeleteConnection(id int) error {
	_, err := m.db.Exec("DELETE FROM llm_connections WHERE id = ?", id)
	return err
}

// GetAnyAPIKeyForProvider returns a decrypted API key from any enabled
// connection of the given provider type, or "" when none is configured.
//
// Used by the catalog-refresh handler: most provider /models endpoints require
// auth, so we borrow the key from an existing connection rather than asking
// admins to enter it twice. Returns ("", nil) when no enabled connection
// exists — the handler then decides whether the provider requires a key
// (everything except OpenRouter does today).
func (m *ConnectionManager) GetAnyAPIKeyForProvider(providerType ProviderType) (string, error) {
	runtime, err := m.GetCatalogRuntimeForProvider(providerType)
	if err != nil {
		return "", err
	}
	if runtime == nil {
		return "", nil
	}
	return runtime.APIKey, nil
}

// CatalogRuntime contains endpoint/auth material used to refresh a provider's
// model catalog. It intentionally excludes model names and other unrelated
// connection fields.
type CatalogRuntime struct {
	ConnectionID int
	APIKey       string
	BaseURL      string
}

// GetCatalogRuntimeForProvider returns auth/base URL from the preferred enabled
// connection for a provider. It returns nil when no enabled connection exists.
func (m *ConnectionManager) GetCatalogRuntimeForProvider(providerType ProviderType) (*CatalogRuntime, error) {
	return m.catalogRuntimeFromRow(m.db.QueryRow(
		`SELECT id, api_key_encrypted, base_url FROM llm_connections
		 WHERE provider_type = ? AND is_enabled = true
		 ORDER BY CASE WHEN api_key_encrypted IS NOT NULL AND api_key_encrypted <> '' THEN 0 ELSE 1 END,
		          is_default DESC, id ASC LIMIT 1`,
		string(providerType),
	), fmt.Sprintf("%q", providerType))
}

// GetCatalogRuntimeForConnection returns auth/base URL for one enabled
// connection, and the connection's provider type so callers can validate it
// against route parameters.
func (m *ConnectionManager) GetCatalogRuntimeForConnection(connectionID int) (ProviderType, *CatalogRuntime, error) {
	var providerType string
	row := m.db.QueryRow(
		`SELECT provider_type, id, api_key_encrypted, base_url FROM llm_connections
		 WHERE id = ? AND is_enabled = true`,
		connectionID,
	)
	var id int
	var apiKeyEncrypted, baseURL sql.NullString
	if err := row.Scan(&providerType, &id, &apiKeyEncrypted, &baseURL); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("lookup catalog runtime for connection %d: %w", connectionID, err)
	}
	runtime, err := m.decryptCatalogRuntime(id, apiKeyEncrypted, baseURL, fmt.Sprintf("connection %d", connectionID))
	return ProviderType(providerType), runtime, err
}

func (m *ConnectionManager) catalogRuntimeFromRow(row *sql.Row, label string) (*CatalogRuntime, error) {
	var id int
	var apiKeyEncrypted, baseURL sql.NullString
	if err := row.Scan(&id, &apiKeyEncrypted, &baseURL); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup catalog runtime for %s: %w", label, err)
	}
	return m.decryptCatalogRuntime(id, apiKeyEncrypted, baseURL, label)
}

func (m *ConnectionManager) decryptCatalogRuntime(id int, apiKeyEncrypted, baseURL sql.NullString, label string) (*CatalogRuntime, error) {
	runtime := &CatalogRuntime{ConnectionID: id}
	if apiKeyEncrypted.Valid && apiKeyEncrypted.String != "" {
		apiKey, err := m.encryption.Decrypt(apiKeyEncrypted.String)
		if err != nil {
			return nil, fmt.Errorf("decrypt api key for %s: %w", label, err)
		}
		runtime.APIKey = apiKey
	}
	if baseURL.Valid {
		runtime.BaseURL = baseURL.String
	}
	return runtime, nil
}

// TestConnection tests a connection by creating a client and calling Health.
func (m *ConnectionManager) TestConnection(id int) error {
	var providerType, model string
	var apiKeyEncrypted, baseURL sql.NullString
	err := m.db.QueryRow(
		"SELECT provider_type, model, api_key_encrypted, base_url FROM llm_connections WHERE id = ?", id,
	).Scan(&providerType, &model, &apiKeyEncrypted, &baseURL)
	if err != nil {
		return fmt.Errorf("connection not found: %w", err)
	}

	var apiKey string
	if apiKeyEncrypted.Valid && apiKeyEncrypted.String != "" {
		apiKey, err = m.encryption.Decrypt(apiKeyEncrypted.String)
		if err != nil {
			return fmt.Errorf("failed to decrypt API key: %w", err)
		}
	}

	client := NewProviderClient(ConnectionConfig{
		ProviderType:        ProviderType(providerType),
		Model:               model,
		APIKey:              apiKey,
		BaseURL:             baseURL.String,
		Timeout:             30 * time.Second,
		AllowedPrivateCIDRs: m.allowedPrivateCIDRs,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return client.Health(ctx)
}

// LoadAIFeaturesConfig reads the per-feature AI configuration from system_settings.
func LoadAIFeaturesConfig(db database.Database) (models.AIFeaturesConfig, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM system_settings WHERE key = 'ai_feature_config'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return models.AIFeaturesConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load AI features config: %w", err)
	}
	var cfg models.AIFeaturesConfig
	if err := json.Unmarshal([]byte(value), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse AI features config: %w", err)
	}
	return cfg, nil
}

// SaveAIFeaturesConfig persists the per-feature AI configuration to system_settings.
func SaveAIFeaturesConfig(db database.Database, cfg models.AIFeaturesConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal AI features config: %w", err)
	}
	_, err = db.Exec(
		`UPDATE system_settings SET value = ?, updated_at = CURRENT_TIMESTAMP WHERE key = 'ai_feature_config'`,
		string(data),
	)
	return err
}

// ResolveForFeature resolves an LLM client respecting per-feature configuration.
func (m *ConnectionManager) ResolveForFeature(featureKey string) (Client, error) {
	return m.ResolveForFeatureWithOverride(featureKey, 0)
}

// ResolveForFeatureWithOverride resolves an LLM client respecting per-feature
// admin configuration, optionally honoring a user-supplied connection override.
//
// Policy:
//   - Mode == Disabled → returns ErrFeatureDisabled regardless of override.
//   - Mode == Specific → ignores override, returns the pinned connection.
//     This is the security-critical case: a user who supplies a different
//     connection_id MUST NOT be able to escape the admin's pin.
//   - Mode == Default (or no entry) → uses override if > 0, else the default
//     enabled connection.
func (m *ConnectionManager) ResolveForFeatureWithOverride(featureKey string, userOverrideConnectionID int) (Client, error) {
	cfg, err := LoadAIFeaturesConfig(m.db)
	if err != nil {
		return nil, err
	}
	decision := decideFeatureResolution(cfg[featureKey], userOverrideConnectionID)
	if decision.disabled {
		return nil, ErrFeatureDisabled
	}
	return m.Resolve(decision.connectionID)
}

// featureResolution is the outcome of applying the feature policy: either the
// feature is disabled, or we know which connection_id to pass to Resolve.
type featureResolution struct {
	disabled     bool
	connectionID int // 0 means "use the default enabled connection"
}

// decideFeatureResolution is the pure policy function. Extracted so the rules
// can be unit-tested without spinning up a database or a manager.
func decideFeatureResolution(fc models.AIFeatureConfig, userOverrideConnectionID int) featureResolution {
	switch fc.Mode {
	case models.AIFeatureModeDisabled:
		return featureResolution{disabled: true}
	case models.AIFeatureModeSpecific:
		// Admin pinned this feature to a specific connection — ignore the
		// caller's override so it cannot escape the pin.
		return featureResolution{connectionID: fc.ConnectionID}
	default: // AIFeatureModeDefault (or unrecognized — treat as default)
		if userOverrideConnectionID > 0 {
			return featureResolution{connectionID: userOverrideConnectionID}
		}
		return featureResolution{connectionID: 0}
	}
}
