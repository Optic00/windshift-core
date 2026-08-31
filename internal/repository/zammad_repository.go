package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
)

type ZammadRepository struct {
	db database.Database
}

func NewZammadRepository(db database.Database) *ZammadRepository {
	return &ZammadRepository{db: db}
}

const zammadConnectionColumns = `
	zc.provider_id, ip.slug, ip.name, ip.enabled, zc.base_url,
	zc.credential_id, zc.auth_method, zc.oauth_generation, COALESCE(zc.oauth_attempt_id, ''), ip.oauth_client_id, ip.oauth_client_secret_encrypted,
	EXISTS(SELECT 1 FROM zammad_oauth_tokens zot WHERE zot.provider_id = zc.provider_id AND zot.reauthorization_required = false),
	COALESCE((SELECT reauthorization_required FROM zammad_oauth_tokens zot WHERE zot.provider_id = zc.provider_id), false),
	zc.default_group_id, zc.default_group_name,
	zc.allowed_group_ids, zc.default_customer, zc.correlation_field, zc.closed_state_ids,
	zc.completion_status_id, zc.applies_to_all_workspaces,
	zc.last_tested_at, zc.last_test_error, zc.created_by,
	zc.created_at, zc.updated_at`

func (r *ZammadRepository) ListConnections() ([]*models.ZammadConnection, error) {
	rows, err := r.db.Query(`SELECT ` + zammadConnectionColumns + `
		FROM zammad_connections zc
		JOIN integration_providers ip ON ip.id = zc.provider_id
		ORDER BY ip.name`)
	if err != nil {
		return nil, fmt.Errorf("list Zammad connections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return r.scanConnections(rows)
}

func (r *ZammadRepository) ListConnectionsForWorkspace(workspaceID int) ([]*models.ZammadConnection, error) {
	rows, err := r.db.Query(`SELECT `+zammadConnectionColumns+`
		FROM zammad_connections zc
		JOIN integration_providers ip ON ip.id = zc.provider_id
		WHERE ip.enabled = true AND (
			zc.applies_to_all_workspaces = true OR EXISTS (
				SELECT 1 FROM zammad_connection_workspaces zcw
				WHERE zcw.provider_id = zc.provider_id AND zcw.workspace_id = ?
			)
		)
		ORDER BY ip.name`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace Zammad connections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return r.scanConnections(rows)
}

func (r *ZammadRepository) GetConnection(id string) (*models.ZammadConnection, error) {
	row := r.db.QueryRow(`SELECT `+zammadConnectionColumns+`
		FROM zammad_connections zc
		JOIN integration_providers ip ON ip.id = zc.provider_id
		WHERE zc.provider_id = ?`, id)
	connection, err := scanZammadConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get Zammad connection: %w", err)
	}
	connection.WorkspaceIDs, err = r.connectionWorkspaceIDs(id)
	if err != nil {
		return nil, err
	}
	return connection, nil
}

func (r *ZammadRepository) IsConnectionAvailableToWorkspace(id string, workspaceID int) (bool, error) {
	var available bool
	err := r.db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM zammad_connections zc
		JOIN integration_providers ip ON ip.id = zc.provider_id
		WHERE zc.provider_id = ? AND ip.enabled = true AND (
			zc.applies_to_all_workspaces = true OR EXISTS (
				SELECT 1 FROM zammad_connection_workspaces zcw
				WHERE zcw.provider_id = zc.provider_id AND zcw.workspace_id = ?
			)
		))`, id, workspaceID).Scan(&available)
	return available, err
}

func (r *ZammadRepository) CreateConnection(connection *models.ZammadConnection) error {
	closedJSON, err := json.Marshal(connection.ClosedStateIDs)
	if err != nil {
		return err
	}
	allowedGroupsJSON, err := json.Marshal(connection.AllowedGroupIDs)
	if err != nil {
		return err
	}
	return database.WithTx(r.db, func(tx database.Tx) error {
		if _, err := tx.Exec(`INSERT INTO integration_providers
			(id, slug, name, provider_type, enabled, oauth_client_id, oauth_client_secret_encrypted, provider_config)
			VALUES (?, ?, ?, ?, ?, ?, ?, '{}')`, connection.ProviderID, connection.Slug,
			connection.Name, models.IntegrationProviderZammad, connection.Enabled,
			nullableString(connection.OAuthClientID), nullableString(connection.OAuthClientSecretEncrypted)); err != nil {
			if database.IsUniqueConstraintError(err) {
				return ErrDuplicateEntry
			}
			return err
		}
		if _, err := tx.Exec(`INSERT INTO zammad_connections
			(provider_id, credential_id, auth_method, base_url, default_group_id,
			 default_group_name, allowed_group_ids, default_customer, correlation_field,
			 closed_state_ids, completion_status_id, applies_to_all_workspaces,
			 created_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, connection.ProviderID,
			nullablePositiveInt(connection.CredentialID), connection.AuthMethod, connection.BaseURL, nullablePositiveInt(connection.DefaultGroupID),
			connection.DefaultGroupName, string(allowedGroupsJSON), connection.DefaultCustomer, connection.CorrelationField,
			string(closedJSON), connection.CompletionStatusID,
			connection.AppliesToAllWorkspaces, connection.CreatedBy); err != nil {
			return err
		}
		return replaceZammadConnectionWorkspaces(tx, connection.ProviderID, connection.AppliesToAllWorkspaces, connection.WorkspaceIDs)
	})
}

func (r *ZammadRepository) UpdateConnection(connection *models.ZammadConnection) error {
	return database.WithTx(r.db, func(tx database.Tx) error {
		return r.UpdateConnectionTx(tx, connection)
	})
}

func (r *ZammadRepository) UpdateConnectionTx(tx database.Tx, connection *models.ZammadConnection) error {
	closedJSON, err := json.Marshal(connection.ClosedStateIDs)
	if err != nil {
		return err
	}
	allowedGroupsJSON, err := json.Marshal(connection.AllowedGroupIDs)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE integration_providers
			SET slug = ?, name = ?, enabled = ?, oauth_client_id = ?, oauth_client_secret_encrypted = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND provider_type = ?`, connection.Slug, connection.Name,
		connection.Enabled, nullableString(connection.OAuthClientID), nullableString(connection.OAuthClientSecretEncrypted), connection.ProviderID, models.IntegrationProviderZammad)
	if err != nil {
		if database.IsUniqueConstraintError(err) {
			return ErrDuplicateEntry
		}
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	_, err = tx.Exec(`UPDATE zammad_connections SET
			credential_id = ?, auth_method = ?, base_url = ?, default_group_id = ?, default_group_name = ?, allowed_group_ids = ?,
			default_customer = ?, correlation_field = ?, closed_state_ids = ?,
			completion_status_id = ?, applies_to_all_workspaces = ?,
			updated_at = CURRENT_TIMESTAMP
			WHERE provider_id = ?`, nullablePositiveInt(connection.CredentialID), connection.AuthMethod, connection.BaseURL, nullablePositiveInt(connection.DefaultGroupID),
		connection.DefaultGroupName, string(allowedGroupsJSON), connection.DefaultCustomer, connection.CorrelationField,
		string(closedJSON), connection.CompletionStatusID,
		connection.AppliesToAllWorkspaces, connection.ProviderID)
	if err != nil {
		return err
	}
	return replaceZammadConnectionWorkspaces(tx, connection.ProviderID, connection.AppliesToAllWorkspaces, connection.WorkspaceIDs)
}

func (r *ZammadRepository) DeleteConnection(id string) error {
	return database.WithTx(r.db, func(tx database.Tx) error {
		var credentialID sql.NullInt64
		if err := tx.QueryRow("SELECT credential_id FROM zammad_connections WHERE provider_id = ?", id).Scan(&credentialID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.Exec("DELETE FROM integration_providers WHERE id = ?", id); err != nil {
			return err
		}
		if credentialID.Valid {
			_, err := tx.Exec("DELETE FROM action_credentials WHERE id = ?", credentialID.Int64)
			return err
		}
		return nil
	})
}

func (r *ZammadRepository) HasTicketLinksForConnection(id string) (bool, error) {
	var hasLinks bool
	err := r.db.QueryRow("SELECT EXISTS(SELECT 1 FROM zammad_ticket_links WHERE provider_id = ?)", id).Scan(&hasLinks)
	return hasLinks, err
}

func (r *ZammadRepository) SetConnectionTestResult(id string, testedAt time.Time, testError string) error {
	_, err := r.db.ExecWrite(`UPDATE zammad_connections
		SET last_tested_at = ?, last_test_error = ?, updated_at = CURRENT_TIMESTAMP
		WHERE provider_id = ?`, testedAt, testError, id)
	return err
}

func (r *ZammadRepository) scanConnections(rows *sql.Rows) ([]*models.ZammadConnection, error) {
	connections := []*models.ZammadConnection{}
	for rows.Next() {
		connection, err := scanZammadConnection(rows)
		if err != nil {
			return nil, err
		}
		connection.WorkspaceIDs, err = r.connectionWorkspaceIDs(connection.ProviderID)
		if err != nil {
			return nil, err
		}
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}

func scanZammadConnection(scanner interface{ Scan(...any) error }) (*models.ZammadConnection, error) {
	var connection models.ZammadConnection
	var credentialID, groupID, completionStatusID, createdBy sql.NullInt64
	var groupName, lastError sql.NullString
	var authMethod, oauthClientID, oauthClientSecret sql.NullString
	var testedAt sql.NullTime
	var oauthConnected, reauthorizationRequired bool
	var allowedGroupsJSON, closedJSON string
	err := scanner.Scan(&connection.ProviderID, &connection.Slug, &connection.Name,
		&connection.Enabled, &connection.BaseURL, &credentialID, &authMethod, &connection.OAuthGeneration, &connection.OAuthAttemptID, &oauthClientID, &oauthClientSecret,
		&oauthConnected, &reauthorizationRequired, &groupID,
		&groupName, &allowedGroupsJSON, &connection.DefaultCustomer, &connection.CorrelationField,
		&closedJSON, &completionStatusID, &connection.AppliesToAllWorkspaces,
		&testedAt, &lastError, &createdBy, &connection.CreatedAt, &connection.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if groupID.Valid {
		connection.DefaultGroupID = int(groupID.Int64)
	}
	if credentialID.Valid {
		v := int(credentialID.Int64)
		connection.CredentialID = v
	}
	if authMethod.Valid {
		connection.AuthMethod = models.ZammadAuthMethod(authMethod.String)
	}
	if oauthClientID.Valid {
		connection.OAuthClientID = oauthClientID.String
	}
	connection.HasOAuthClientSecret = oauthClientSecret.Valid && oauthClientSecret.String != ""
	if oauthClientSecret.Valid {
		connection.OAuthClientSecretEncrypted = oauthClientSecret.String
	}
	connection.OAuthConnected = oauthConnected
	connection.ReauthorizationRequired = reauthorizationRequired
	if groupName.Valid {
		connection.DefaultGroupName = groupName.String
	}
	if completionStatusID.Valid {
		v := int(completionStatusID.Int64)
		connection.CompletionStatusID = &v
	}
	if testedAt.Valid {
		connection.LastTestedAt = &testedAt.Time
	}
	if lastError.Valid {
		connection.LastTestError = lastError.String
	}
	if createdBy.Valid {
		v := int(createdBy.Int64)
		connection.CreatedBy = &v
	}
	if err := json.Unmarshal([]byte(closedJSON), &connection.ClosedStateIDs); err != nil {
		return nil, fmt.Errorf("decode closed_state_ids: %w", err)
	}
	if err := json.Unmarshal([]byte(allowedGroupsJSON), &connection.AllowedGroupIDs); err != nil {
		return nil, fmt.Errorf("decode allowed_group_ids: %w", err)
	}
	connection.HasAPIToken = connection.AuthMethod == models.ZammadAuthMethodAPIToken && connection.CredentialID > 0
	return &connection, nil
}

func (r *ZammadRepository) connectionWorkspaceIDs(providerID string) ([]int, error) {
	rows, err := r.db.Query(`SELECT workspace_id FROM zammad_connection_workspaces
		WHERE provider_id = ? ORDER BY workspace_id`, providerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := []int{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func replaceZammadConnectionWorkspaces(tx database.Tx, providerID string, appliesAll bool, workspaceIDs []int) error {
	if _, err := tx.Exec("DELETE FROM zammad_connection_workspaces WHERE provider_id = ?", providerID); err != nil {
		return err
	}
	if appliesAll {
		return nil
	}
	for _, workspaceID := range workspaceIDs {
		if _, err := tx.Exec(`INSERT INTO zammad_connection_workspaces(provider_id, workspace_id)
			VALUES (?, ?)`, providerID, workspaceID); err != nil {
			return err
		}
	}
	return nil
}

func (r *ZammadRepository) GetTicketLinksForItem(itemID int) ([]*models.ZammadTicketLink, error) {
	rows, err := r.db.Query(`SELECT `+zammadTicketLinkColumns+`
		FROM zammad_ticket_links ztl
		JOIN integration_providers ip ON ip.id = ztl.provider_id
		WHERE ztl.item_id = ? ORDER BY ztl.created_at DESC`, itemID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	links := []*models.ZammadTicketLink{}
	for rows.Next() {
		link, err := scanZammadTicketLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

const zammadTicketLinkColumns = `
	ztl.id, ztl.item_id, ztl.provider_id, ip.name,
	ztl.item_integration_link_id, ztl.ticket_id, ztl.ticket_number,
	ztl.ticket_url, ztl.group_id, ztl.group_name, ztl.owner_id, ztl.owner_name, ztl.correlation_key,
	ztl.sync_state, ztl.last_status_id, ztl.last_status_name,
	ztl.last_synced_at, ztl.last_attempt_at, ztl.next_attempt_at, ztl.last_error, ztl.completion_applied, ztl.created_by,
	ztl.created_at, ztl.updated_at`

func (r *ZammadRepository) GetTicketLink(id string) (*models.ZammadTicketLink, error) {
	link, err := scanZammadTicketLink(r.db.QueryRow(`SELECT `+zammadTicketLinkColumns+`
		FROM zammad_ticket_links ztl
		JOIN integration_providers ip ON ip.id = ztl.provider_id
		WHERE ztl.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return link, err
}

func (r *ZammadRepository) GetTicketLinkForItem(itemID int, providerID string) (*models.ZammadTicketLink, error) {
	link, err := scanZammadTicketLink(r.db.QueryRow(`SELECT `+zammadTicketLinkColumns+`
		FROM zammad_ticket_links ztl
		JOIN integration_providers ip ON ip.id = ztl.provider_id
		WHERE ztl.item_id = ? AND ztl.provider_id = ?`, itemID, providerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return link, err
}

func (r *ZammadRepository) HasTicketLinks(providerID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow("SELECT EXISTS(SELECT 1 FROM zammad_ticket_links WHERE provider_id = ?)", providerID).Scan(&exists)
	return exists, err
}

func (r *ZammadRepository) CreatePendingTicketLink(link *models.ZammadTicketLink) error {
	_, err := r.db.ExecWrite(`INSERT INTO zammad_ticket_links
		(id, item_id, provider_id, group_id, group_name, correlation_key,
		 sync_state, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, link.ID, link.ItemID, link.ProviderID,
		nullablePositiveInt(link.GroupID), link.GroupName, link.CorrelationKey,
		models.ZammadSyncPending, link.CreatedBy)
	if database.IsUniqueConstraintError(err) {
		return ErrDuplicateEntry
	}
	return err
}

// ReserveExistingTicketLink claims the provider/ticket pair before changing
// the remote correlation field. The unique constraint makes competing item
// links fail closed rather than attaching one ticket to two Windshift items.
func (r *ZammadRepository) ReserveExistingTicketLink(link *models.ZammadTicketLink) error {
	_, err := r.db.ExecWrite(`INSERT INTO zammad_ticket_links
		(id, item_id, provider_id, ticket_id, ticket_number, ticket_url, group_id, group_name,
		 owner_id, owner_name, correlation_key, sync_state, last_status_id, last_status_name, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		link.ID, link.ItemID, link.ProviderID, link.TicketID, link.TicketNumber, link.TicketURL,
		nullablePositiveInt(link.GroupID), link.GroupName, nullablePositiveInt(link.OwnerID), link.OwnerName,
		link.CorrelationKey, models.ZammadSyncCreating, nullablePositiveInt(link.LastStatusID), link.LastStatusName, link.CreatedBy)
	if database.IsUniqueConstraintError(err) {
		return ErrDuplicateEntry
	}
	return err
}

func (r *ZammadRepository) ClaimTicketCreation(itemID int, providerID string, now time.Time) (bool, error) {
	staleBefore := now.Add(-2 * time.Minute)
	result, err := r.db.ExecWrite(`UPDATE zammad_ticket_links
		SET sync_state = ?, creating_started_at = ?, last_error = '', updated_at = CURRENT_TIMESTAMP
		WHERE item_id = ? AND provider_id = ? AND (
			sync_state IN (?, ?, ?) OR (sync_state = ? AND creating_started_at < ?)
		)`, models.ZammadSyncCreating, now, itemID, providerID,
		models.ZammadSyncPending, models.ZammadSyncFailed, models.ZammadSyncUncertain,
		models.ZammadSyncCreating, staleBefore)
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	return rows == 1, nil
}

func (r *ZammadRepository) CompleteTicketCreation(linkID string, ticketID int, number, ticketURL string, statusID int, statusName string, groupID int, groupName string, ownerID int, ownerName string, linkedBy int) error {
	genericLinkID := linkID + "-external"
	metadata, _ := json.Marshal(map[string]any{
		"status_id": statusID, "status_name": statusName,
		"group_id": groupID, "group_name": groupName,
		"owner_id": ownerID, "owner_name": ownerName,
	})
	return database.WithTx(r.db, func(tx database.Tx) error {
		if _, err := tx.Exec(`INSERT INTO item_integration_links
			(id, item_id, integration_provider_id, external_id, external_url,
			 title, icon, link_type, link_metadata, linked_by)
			SELECT ?, item_id, provider_id, ?, ?, ?, ?, 'ticket', ?, ?
			FROM zammad_ticket_links WHERE id = ?
			ON CONFLICT (item_id, integration_provider_id, external_id) DO UPDATE SET
				external_url = excluded.external_url,
				title = excluded.title,
				link_metadata = excluded.link_metadata,
				updated_at = CURRENT_TIMESTAMP`,
			genericLinkID, strconv.Itoa(ticketID), ticketURL, "Zammad #"+number,
			"ticket", string(metadata), strconv.Itoa(linkedBy), linkID); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT iil.id FROM item_integration_links iil
			JOIN zammad_ticket_links ztl ON CAST(ztl.item_id AS TEXT) = iil.item_id
				AND ztl.provider_id = iil.integration_provider_id
			WHERE ztl.id = ? AND iil.external_id = ?`, linkID, strconv.Itoa(ticketID)).Scan(&genericLinkID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE zammad_ticket_links SET
			item_integration_link_id = ?, ticket_id = ?, ticket_number = ?,
			ticket_url = ?, group_id = ?, group_name = ?, owner_id = ?, owner_name = ?,
			sync_state = ?, creating_started_at = NULL,
			last_status_id = ?, last_status_name = ?, last_synced_at = CURRENT_TIMESTAMP,
			last_attempt_at = CURRENT_TIMESTAMP, next_attempt_at = NULL,
			last_error = '', updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`, genericLinkID, ticketID, number, ticketURL,
			nullablePositiveInt(groupID), groupName, nullablePositiveInt(ownerID), ownerName,
			models.ZammadSyncLinked, nullablePositiveInt(statusID), statusName, linkID)
		return err
	})
}

func (r *ZammadRepository) CompleteExistingTicketLink(linkID, genericLinkID string, ticket *models.ZammadTicketLink, linkedBy int) error {
	metadata, _ := json.Marshal(map[string]any{"status_id": ticket.LastStatusID, "status_name": ticket.LastStatusName, "group_id": ticket.GroupID, "group_name": ticket.GroupName, "owner_id": ticket.OwnerID, "owner_name": ticket.OwnerName})
	return database.WithTx(r.db, func(tx database.Tx) error {
		if _, err := tx.Exec(`INSERT INTO item_integration_links
			(id, item_id, integration_provider_id, external_id, external_url, title, icon, link_type, link_metadata, linked_by)
			SELECT ?, item_id, provider_id, ?, ?, ?, ?, 'ticket', ?, ? FROM zammad_ticket_links WHERE id = ?
			ON CONFLICT (item_id, integration_provider_id, external_id) DO UPDATE SET external_url=excluded.external_url, title=excluded.title, link_metadata=excluded.link_metadata, updated_at=CURRENT_TIMESTAMP`,
			genericLinkID, strconv.Itoa(ticket.TicketID), ticket.TicketURL, "Zammad #"+ticket.TicketNumber, "ticket", string(metadata), strconv.Itoa(linkedBy), linkID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE zammad_ticket_links SET item_integration_link_id=?, ticket_id=?, ticket_number=?, ticket_url=?, group_id=?, group_name=?, owner_id=?, owner_name=?, sync_state=?, creating_started_at=NULL, last_status_id=?, last_status_name=?, last_synced_at=CURRENT_TIMESTAMP, last_attempt_at=CURRENT_TIMESTAMP, next_attempt_at=NULL, last_error='', updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			genericLinkID, ticket.TicketID, ticket.TicketNumber, ticket.TicketURL, nullablePositiveInt(ticket.GroupID), ticket.GroupName, nullablePositiveInt(ticket.OwnerID), ticket.OwnerName, models.ZammadSyncLinked, nullablePositiveInt(ticket.LastStatusID), ticket.LastStatusName, linkID)
		return err
	})
}

func (r *ZammadRepository) MarkTicketLinkFailed(id, safeError string) error {
	_, err := r.db.ExecWrite(`UPDATE zammad_ticket_links SET
		sync_state = ?, creating_started_at = NULL, last_error = ?,
		updated_at = CURRENT_TIMESTAMP WHERE id = ?`, models.ZammadSyncFailed, safeError, id)
	return err
}

func (r *ZammadRepository) MarkTicketLinkUncertain(id, safeError string) error {
	_, err := r.db.ExecWrite(`UPDATE zammad_ticket_links SET
		sync_state = ?, creating_started_at = NULL, last_error = ?,
		updated_at = CURRENT_TIMESTAMP WHERE id = ?`, models.ZammadSyncUncertain, safeError, id)
	return err
}

func (r *ZammadRepository) ResetUncertainTicketCreation(id string) (bool, error) {
	result, err := r.db.ExecWrite(`UPDATE zammad_ticket_links SET
		sync_state = ?, last_error = '', updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND sync_state = ? AND ticket_id IS NULL`,
		models.ZammadSyncFailed, id, models.ZammadSyncUncertain)
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	return rows == 1, nil
}

func (r *ZammadRepository) UpdateTicketLinkSync(id string, statusID int, statusName string, groupID int, groupName string, ownerID int, ownerName, safeError string, now time.Time, setCompletionApplied, completionApplied bool) error {
	state := models.ZammadSyncLinked
	var nextAttempt any
	if safeError != "" {
		state = models.ZammadSyncFailed
		nextAttempt = now.Add(time.Minute)
	}
	_, err := r.db.ExecWrite(`UPDATE zammad_ticket_links SET
		last_status_id = ?, last_status_name = ?,
		group_id = ?, group_name = ?, owner_id = ?, owner_name = ?,
		last_synced_at = CASE WHEN ? = '' THEN ? ELSE last_synced_at END,
		last_attempt_at = ?, next_attempt_at = ?,
		last_error = ?, sync_state = ?, sync_lock_until = NULL,
		completion_applied = CASE WHEN ? THEN ? ELSE completion_applied END,
		updated_at = CURRENT_TIMESTAMP WHERE id = ?`, nullablePositiveInt(statusID), statusName,
		nullablePositiveInt(groupID), groupName, nullablePositiveInt(ownerID), ownerName,
		safeError, now, now, nextAttempt, safeError, state, setCompletionApplied, completionApplied, id)
	return err
}

func (r *ZammadRepository) ListDueTicketLinks(before time.Time, limit int) ([]*models.ZammadTicketLink, error) {
	rows, err := r.db.Query(`SELECT `+zammadTicketLinkColumns+`
		FROM zammad_ticket_links ztl
		JOIN integration_providers ip ON ip.id = ztl.provider_id
		JOIN zammad_connections zc ON zc.provider_id = ztl.provider_id
		WHERE ip.enabled = true AND ztl.ticket_id IS NOT NULL
		  AND (zc.auth_method != 'oauth' OR EXISTS (
			SELECT 1 FROM zammad_oauth_tokens zot
			WHERE zot.provider_id = zc.provider_id AND zot.reauthorization_required = false
		  ))
		  AND (ztl.last_synced_at IS NULL OR ztl.last_synced_at < ?)
		  AND (ztl.next_attempt_at IS NULL OR ztl.next_attempt_at <= CURRENT_TIMESTAMP)
		  AND (ztl.sync_lock_until IS NULL OR ztl.sync_lock_until < CURRENT_TIMESTAMP)
		ORDER BY COALESCE(ztl.last_attempt_at, ztl.last_synced_at, ztl.created_at) ASC LIMIT ?`, before, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	links := []*models.ZammadTicketLink{}
	for rows.Next() {
		link, err := scanZammadTicketLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func (r *ZammadRepository) ClaimSync(id string, until time.Time) (bool, error) {
	result, err := r.db.ExecWrite(`UPDATE zammad_ticket_links SET sync_lock_until = ?
		WHERE id = ? AND (sync_lock_until IS NULL OR sync_lock_until < CURRENT_TIMESTAMP)`, until, id)
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	return rows == 1, nil
}

func scanZammadTicketLink(scanner interface{ Scan(...any) error }) (*models.ZammadTicketLink, error) {
	var link models.ZammadTicketLink
	var genericLinkID, number, ticketURL, groupName, ownerName, statusName, lastError sql.NullString
	var ticketID, groupID, ownerID, statusID, createdBy sql.NullInt64
	var lastSynced, lastAttempt, nextAttempt sql.NullTime
	err := scanner.Scan(&link.ID, &link.ItemID, &link.ProviderID, &link.ProviderName,
		&genericLinkID, &ticketID, &number, &ticketURL, &groupID, &groupName, &ownerID, &ownerName,
		&link.CorrelationKey, &link.SyncState, &statusID, &statusName,
		&lastSynced, &lastAttempt, &nextAttempt, &lastError, &link.CompletionApplied, &createdBy, &link.CreatedAt, &link.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if genericLinkID.Valid {
		link.ItemIntegrationLinkID = genericLinkID.String
	}
	if ticketID.Valid {
		link.TicketID = int(ticketID.Int64)
	}
	if number.Valid {
		link.TicketNumber = number.String
	}
	if ticketURL.Valid {
		link.TicketURL = ticketURL.String
	}
	if groupID.Valid {
		link.GroupID = int(groupID.Int64)
	}
	if groupName.Valid {
		link.GroupName = groupName.String
	}
	if ownerID.Valid {
		link.OwnerID = int(ownerID.Int64)
	}
	if ownerName.Valid {
		link.OwnerName = ownerName.String
	}
	if statusID.Valid {
		link.LastStatusID = int(statusID.Int64)
	}
	if statusName.Valid {
		link.LastStatusName = statusName.String
	}
	if lastSynced.Valid {
		link.LastSyncedAt = &lastSynced.Time
	}
	if lastAttempt.Valid {
		link.LastAttemptAt = &lastAttempt.Time
	}
	if nextAttempt.Valid {
		link.NextAttemptAt = &nextAttempt.Time
	}
	if lastError.Valid {
		link.LastError = lastError.String
	}
	if createdBy.Valid {
		v := int(createdBy.Int64)
		link.CreatedBy = &v
	}
	return &link, nil
}

func (r *ZammadRepository) DeleteTicketLink(id string) error {
	return database.WithTx(r.db, func(tx database.Tx) error {
		var genericID sql.NullString
		if err := tx.QueryRow(`SELECT item_integration_link_id FROM zammad_ticket_links WHERE id=?`, id).Scan(&genericID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM zammad_ticket_links WHERE id=?`, id); err != nil {
			return err
		}
		if genericID.Valid {
			_, err := tx.Exec(`DELETE FROM item_integration_links WHERE id=?`, genericID.String)
			return err
		}
		return nil
	})
}

func nullablePositiveInt(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}
