package llm

import (
	"database/sql"
	"fmt"
)

// scanConnections scans rows from an llm_connections query into a slice of ConnectionInfo.
// The rows must select: id, name, provider_type, model, api_key_encrypted, base_url, provider_config, is_default, is_enabled, created_at, updated_at.
func scanConnections(rows *sql.Rows) ([]ConnectionInfo, error) {
	var connections []ConnectionInfo
	for rows.Next() {
		var c ConnectionInfo
		var apiKeyEncrypted, baseURL, providerConfig sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &c.ProviderType, &c.Model, &apiKeyEncrypted, &baseURL, &providerConfig, &c.IsDefault, &c.IsEnabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan connection: %w", err)
		}
		c.HasAPIKey = apiKeyEncrypted.Valid && apiKeyEncrypted.String != ""
		if baseURL.Valid {
			c.BaseURL = baseURL.String
		}
		if providerConfig.Valid {
			c.ProviderConfig = providerConfig.String
		}
		connections = append(connections, c)
	}
	if connections == nil {
		connections = []ConnectionInfo{}
	}
	return connections, nil
}
