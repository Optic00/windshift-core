package jiraimport

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrConnectionNotFound   = errors.New("jira import connection not found")
	ErrConnectionHasHistory = errors.New("jira import connection has import history")
)

type Connection struct {
	ID             string     `json:"id"`
	InstanceURL    string     `json:"instance_url"`
	Email          string     `json:"email"`
	InstanceName   string     `json:"instance_name"`
	DeploymentType string     `json:"deployment_type"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
}

type NewConnection struct {
	ID                   string
	InstanceURL          string
	Email                string
	EncryptedCredentials string
	InstanceName         string
	DeploymentType       string
	CreatedBy            *int
}

type ConnectionCredentials struct {
	InstanceURL          string
	Email                string
	EncryptedCredentials string
	DeploymentType       string
}

func (s *Service) CreateConnection(input NewConnection) error {
	_, err := s.db.ExecWrite(`
		INSERT INTO jira_import_connections
			(id, instance_url, email, encrypted_credentials, instance_name, deployment_type, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, input.ID, input.InstanceURL, input.Email, input.EncryptedCredentials, input.InstanceName, input.DeploymentType, input.CreatedBy)
	return err
}

func (s *Service) ListConnections() ([]Connection, error) {
	rows, err := s.db.Query(`
		SELECT id, instance_url, email, instance_name, deployment_type, created_at, last_used_at
		FROM jira_import_connections ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	connections := make([]Connection, 0)
	for rows.Next() {
		var connection Connection
		var instanceName, deploymentType sql.NullString
		var lastUsedAt sql.NullTime
		if err := rows.Scan(&connection.ID, &connection.InstanceURL, &connection.Email,
			&instanceName, &deploymentType, &connection.CreatedAt, &lastUsedAt); err != nil {
			return nil, err
		}
		connection.InstanceName = instanceName.String
		connection.DeploymentType = deploymentType.String
		if connection.DeploymentType == "" {
			connection.DeploymentType = "cloud"
		}
		if lastUsedAt.Valid {
			connection.LastUsedAt = &lastUsedAt.Time
		}
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}

func (s *Service) DeleteConnection(connectionID string) error {
	hasHistory, err := s.HasImportHistory(connectionID)
	if err != nil {
		return err
	}
	if hasHistory {
		return ErrConnectionHasHistory
	}
	result, err := s.db.ExecWrite(`DELETE FROM jira_import_connections WHERE id = ?`, connectionID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrConnectionNotFound
	}
	return nil
}

func (s *Service) UseConnection(connectionID string) (ConnectionCredentials, error) {
	var result ConnectionCredentials
	var deploymentType sql.NullString
	err := s.db.QueryRow(`
		SELECT instance_url, email, encrypted_credentials, deployment_type
		FROM jira_import_connections WHERE id = ?
	`, connectionID).Scan(&result.InstanceURL, &result.Email, &result.EncryptedCredentials, &deploymentType)
	if errors.Is(err, sql.ErrNoRows) {
		return ConnectionCredentials{}, ErrConnectionNotFound
	}
	if err != nil {
		return ConnectionCredentials{}, err
	}
	result.DeploymentType = deploymentType.String
	if _, err := s.db.ExecWrite(`
		UPDATE jira_import_connections SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?
	`, connectionID); err != nil {
		return ConnectionCredentials{}, err
	}
	return result, nil
}

func (s *Service) ConnectionDeploymentType(connectionID string) (string, error) {
	var deploymentType sql.NullString
	err := s.db.QueryRow(`SELECT deployment_type FROM jira_import_connections WHERE id = ?`, connectionID).Scan(&deploymentType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrConnectionNotFound
	}
	if err != nil {
		return "", err
	}
	return deploymentType.String, nil
}

func (s *Service) WorkspaceKeys() (map[string]struct{}, error) {
	workspaces, err := s.workspaces.ListIDKeys()
	if err != nil {
		return nil, err
	}
	keys := make(map[string]struct{})
	for _, workspace := range workspaces {
		keys[normalizeProjectKey(workspace.Key)] = struct{}{}
	}
	return keys, nil
}

type WorkspaceKeyPlan struct {
	Key       string
	Collision bool
}

func (s *Service) PlanWorkspaceKeys(projectKeys []string) (map[string]WorkspaceKeyPlan, error) {
	occupied, err := s.WorkspaceKeys()
	if err != nil {
		return nil, fmt.Errorf("load existing workspace keys: %w", err)
	}
	plans := make(map[string]WorkspaceKeyPlan, len(projectKeys))
	for _, projectKey := range projectKeys {
		jiraKey := normalizeProjectKey(projectKey)
		if jiraKey == "" {
			continue
		}
		if _, exists := plans[jiraKey]; exists {
			continue
		}
		targetKey := jiraKey
		_, collision := occupied[targetKey]
		if collision {
			base := "JIRA_" + jiraKey
			targetKey = base
			for suffix := 2; ; suffix++ {
				if _, exists := occupied[targetKey]; !exists {
					break
				}
				targetKey = fmt.Sprintf("%s_%d", base, suffix)
			}
		}
		plans[jiraKey] = WorkspaceKeyPlan{Key: targetKey, Collision: collision}
		occupied[strings.ToUpper(targetKey)] = struct{}{}
	}
	return plans, nil
}
