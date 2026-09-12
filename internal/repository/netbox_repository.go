package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
)

const netBoxSnapshotMaxBytes = 16 * 1024

type NetBoxRepository struct{ db database.Database }

func NewNetBoxRepository(db database.Database) *NetBoxRepository { return &NetBoxRepository{db: db} }

type netBoxQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

const netBoxConnectionColumns = `nc.provider_id, ip.slug, ip.name, ip.enabled,
	nc.base_url, nc.auth_scheme, nc.config_revision, nc.credential_id,
	ac.is_enabled, (ac.encrypted_secret IS NOT NULL AND ac.encrypted_secret <> ''),
	ac.applies_to_all_workspaces, nc.created_by, nc.created_at, nc.updated_at`

const netBoxConnectionJoins = ` FROM netbox_connections nc
	JOIN integration_providers ip ON ip.id = nc.provider_id AND ip.provider_type = 'netbox'
	JOIN action_credentials ac ON ac.id = nc.credential_id `

// Use this same predicate for live remote access and saved snapshots. Scope
// comes only from the managed credential and the item's current workspace.
const netBoxVisibleScope = `ip.enabled = true AND ac.is_enabled = true AND (
	ac.applies_to_all_workspaces = true OR EXISTS (
		SELECT 1 FROM action_credential_workspaces acw
		WHERE acw.credential_id = ac.id AND acw.workspace_id = %s))`

func scanNetBoxConnection(row rowScanner) (*models.NetBoxConnection, error) {
	c := &models.NetBoxConnection{WorkspaceIDs: []int{}}
	err := row.Scan(&c.ProviderID, &c.Slug, &c.Name, &c.Enabled,
		&c.BaseURL, &c.AuthScheme, &c.ConfigRevision, &c.CredentialID,
		&c.CredentialEnabled, &c.HasAPIToken, &c.AppliesToAllWorkspaces,
		&c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func netBoxWorkspaceIDs(q netBoxQuerier, credentialID int) ([]int, error) {
	rows, err := q.Query(`SELECT workspace_id FROM action_credential_workspaces
		WHERE credential_id = ? ORDER BY workspace_id`, credentialID)
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

func (r *NetBoxRepository) listConnections(where string, args ...any) ([]*models.NetBoxConnection, error) {
	rows, err := r.db.Query("SELECT "+netBoxConnectionColumns+netBoxConnectionJoins+where+" ORDER BY ip.name, ip.id", args...)
	if err != nil {
		return nil, err
	}
	connections := []*models.NetBoxConnection{}
	for rows.Next() {
		connection, err := scanNetBoxConnection(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		connections = append(connections, connection)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	for _, c := range connections {
		if c.WorkspaceIDs, err = netBoxWorkspaceIDs(r.db, c.CredentialID); err != nil {
			return nil, err
		}
	}
	return connections, nil
}

func (r *NetBoxRepository) ListConnections() ([]*models.NetBoxConnection, error) {
	return r.listConnections("")
}

func (r *NetBoxRepository) ListConnectionsForWorkspace(workspaceID int) ([]*models.NetBoxConnection, error) {
	return r.listConnections(" WHERE "+fmt.Sprintf(netBoxVisibleScope, "?"), workspaceID)
}

func getNetBoxConnection(q netBoxQuerier, id string) (*models.NetBoxConnection, error) {
	c, err := scanNetBoxConnection(q.QueryRow("SELECT "+netBoxConnectionColumns+netBoxConnectionJoins+" WHERE nc.provider_id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.WorkspaceIDs, err = netBoxWorkspaceIDs(q, c.CredentialID)
	return c, err
}

func (r *NetBoxRepository) GetConnection(id string) (*models.NetBoxConnection, error) {
	return getNetBoxConnection(r.db, id)
}

func (r *NetBoxRepository) GetConnectionTx(tx database.Tx, id string) (*models.NetBoxConnection, error) {
	return getNetBoxConnection(tx, id)
}

func (r *NetBoxRepository) LockConnectionTx(tx database.Tx, id string) error {
	query := "SELECT provider_id FROM netbox_connections WHERE provider_id = ?"
	if database.IsPostgresDriver(r.db.GetDriverName()) {
		query += " FOR UPDATE"
	}
	var found string
	if err := tx.QueryRow(query, id).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (r *NetBoxRepository) CreateConnection(c *models.NetBoxConnection) error {
	err := database.WithTx(r.db, func(tx database.Tx) error {
		if _, err := tx.Exec(`INSERT INTO integration_providers
			(id, slug, name, provider_type, enabled, provider_config)
			VALUES (?, ?, ?, 'netbox', ?, '{}')`, c.ProviderID, c.Slug, c.Name, c.Enabled); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO netbox_connections
			(provider_id, credential_id, base_url, auth_scheme, created_by)
			VALUES (?, ?, ?, ?, ?)`, c.ProviderID, c.CredentialID, c.BaseURL, c.AuthScheme, c.CreatedBy)
		return err
	})
	if database.IsUniqueConstraintError(err) {
		return ErrDuplicateEntry
	}
	if err == nil {
		c.ConfigRevision = 1
	}
	return err
}

// UpdateConnectionTx saves a revision-checked configuration change. The service
// updates managed credential metadata in this same transaction.
// Origin, auth scheme and credential identity are immutable for the MVP.
func (r *NetBoxRepository) UpdateConnectionTx(tx database.Tx, c *models.NetBoxConnection) error {
	result, err := tx.Exec(`UPDATE netbox_connections SET config_revision = config_revision + 1,
		updated_at = CURRENT_TIMESTAMP WHERE provider_id = ? AND config_revision = ?
		AND base_url = ? AND auth_scheme = ? AND credential_id = ?`,
		c.ProviderID, c.ConfigRevision, c.BaseURL, c.AuthScheme, c.CredentialID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrConcurrentUpdate
	}
	result, err = tx.Exec(`UPDATE integration_providers SET name = ?, enabled = ?,
		updated_at = CURRENT_TIMESTAMP WHERE id = ? AND provider_type = 'netbox'`, c.Name, c.Enabled, c.ProviderID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	c.ConfigRevision++
	return nil
}

// DeleteConnectionTx removes a connection. The caller holds LockConnectionTx
// and verifies managed credential ownership.
// Delete the provider first, cascading connection and local links, then its
// owned credential; the connection's restrictive FK protects the ordering.
func (r *NetBoxRepository) DeleteConnectionTx(tx database.Tx, id string) error {
	var credentialID int
	if err := tx.QueryRow("SELECT credential_id FROM netbox_connections WHERE provider_id = ?", id).Scan(&credentialID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec("DELETE FROM integration_providers WHERE id = ? AND provider_type = 'netbox'", id); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM action_credentials WHERE id = ?", credentialID)
	return err
}

func netBoxAvailable(q netBoxQuerier, id string, workspaceID int) (bool, error) {
	var allowed bool
	err := q.QueryRow("SELECT EXISTS(SELECT 1"+netBoxConnectionJoins+
		" WHERE nc.provider_id = ? AND "+fmt.Sprintf(netBoxVisibleScope, "?")+")", id, workspaceID).Scan(&allowed)
	return allowed, err
}

func (r *NetBoxRepository) IsConnectionAvailableToWorkspace(id string, workspaceID int) (bool, error) {
	return netBoxAvailable(r.db, id, workspaceID)
}

func (r *NetBoxRepository) IsConnectionAvailableToWorkspaceTx(tx database.Tx, id string, workspaceID int) (bool, error) {
	return netBoxAvailable(tx, id, workspaceID)
}

const netBoxLinkColumns = `nl.id, nl.item_id, nl.provider_id, ip.name, nl.object_type,
	nl.object_id, nl.snapshot_json, nl.revision, nl.snapshot_updated_at, nl.created_by, nl.created_at`

const netBoxLinkJoins = ` FROM netbox_item_links nl
	JOIN items i ON i.id = nl.item_id
	JOIN netbox_connections nc ON nc.provider_id = nl.provider_id
	JOIN integration_providers ip ON ip.id = nc.provider_id AND ip.provider_type = 'netbox'
	JOIN action_credentials ac ON ac.id = nc.credential_id `

func scanNetBoxLink(row rowScanner) (*models.NetBoxItemLink, error) {
	l := &models.NetBoxItemLink{}
	var objectType models.NetBoxObjectType
	var objectID int64
	var snapshot string
	err := row.Scan(&l.ID, &l.ItemID, &l.ProviderID, &l.ConnectionName, &objectType,
		&objectID, &snapshot, &l.Revision, &l.SnapshotUpdatedAt, &l.CreatedBy, &l.CreatedAt)
	if err != nil {
		return nil, err
	}
	if len(snapshot) > netBoxSnapshotMaxBytes {
		return nil, ErrInvalidInput
	}
	if err := json.Unmarshal([]byte(snapshot), &l.Object); err != nil {
		return nil, fmt.Errorf("decode saved NetBox snapshot: %w", err)
	}
	l.Object.ObjectType, l.Object.ObjectID = objectType, objectID
	l.Object.ExternalID = fmt.Sprintf("%s:%d", objectType, objectID)
	return l, nil
}

func (r *NetBoxRepository) ListVisibleLinks(itemID, expectedWorkspaceID int) ([]*models.NetBoxItemLink, error) {
	rows, err := r.db.Query("SELECT "+netBoxLinkColumns+netBoxLinkJoins+
		" WHERE nl.item_id = ? AND i.workspace_id = ? AND "+fmt.Sprintf(netBoxVisibleScope, "i.workspace_id")+
		" ORDER BY nl.created_at, nl.id", itemID, expectedWorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	links := []*models.NetBoxItemLink{}
	for rows.Next() {
		link, err := scanNetBoxLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func getNetBoxLink(q netBoxQuerier, where string, args ...any) (*models.NetBoxItemLink, error) {
	link, err := scanNetBoxLink(q.QueryRow("SELECT "+netBoxLinkColumns+netBoxLinkJoins+" WHERE "+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return link, err
}

func (r *NetBoxRepository) GetVisibleLink(itemID, expectedWorkspaceID int, linkID string) (*models.NetBoxItemLink, error) {
	return getNetBoxLink(r.db, "nl.item_id = ? AND i.workspace_id = ? AND nl.id = ? AND "+fmt.Sprintf(netBoxVisibleScope, "i.workspace_id"), itemID, expectedWorkspaceID, linkID)
}

// GetLink returns only identity/revision for internal authorization and lock
// routing. It never exposes a potentially hidden saved snapshot.
func (r *NetBoxRepository) GetLink(itemID int, linkID string) (*models.NetBoxItemLink, error) {
	link := &models.NetBoxItemLink{}
	err := r.db.QueryRow(`SELECT id, item_id, provider_id, object_type, object_id, revision
		FROM netbox_item_links WHERE item_id = ? AND id = ?`, itemID, linkID).Scan(
		&link.ID, &link.ItemID, &link.ProviderID, &link.Object.ObjectType, &link.Object.ObjectID, &link.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	link.Object.ExternalID = fmt.Sprintf("%s:%d", link.Object.ObjectType, link.Object.ObjectID)
	return link, nil
}

// GetLinkByObjectTx reads a saved link after the caller revalidates current
// visibility while holding the connection then the item.
func (r *NetBoxRepository) GetLinkByObjectTx(tx database.Tx, itemID int, providerID string, objectType models.NetBoxObjectType, objectID int64) (*models.NetBoxItemLink, error) {
	return getNetBoxLink(tx, "nl.item_id = ? AND nl.provider_id = ? AND nl.object_type = ? AND nl.object_id = ?", itemID, providerID, objectType, objectID)
}

func encodeNetBoxSnapshot(object models.NetBoxObject) (string, error) {
	if object.ObjectID <= 0 || (object.ObjectType != models.NetBoxDevice && object.ObjectType != models.NetBoxVirtualMachine) {
		return "", ErrInvalidInput
	}
	object.ExternalID = fmt.Sprintf("%s:%d", object.ObjectType, object.ObjectID)
	snapshot, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	if len(snapshot) > netBoxSnapshotMaxBytes {
		return "", ErrInvalidInput
	}
	return string(snapshot), nil
}

func (r *NetBoxRepository) CreateLinkTx(tx database.Tx, link *models.NetBoxItemLink) (bool, error) {
	snapshot, err := encodeNetBoxSnapshot(link.Object)
	if err != nil {
		return false, err
	}
	if link.SnapshotUpdatedAt.IsZero() {
		link.SnapshotUpdatedAt = time.Now().UTC()
	}
	result, err := tx.Exec(`INSERT INTO netbox_item_links
		(id, item_id, provider_id, object_type, object_id, snapshot_json, snapshot_updated_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(item_id, provider_id, object_type, object_id) DO NOTHING`, link.ID,
		link.ItemID, link.ProviderID, link.Object.ObjectType, link.Object.ObjectID,
		snapshot, link.SnapshotUpdatedAt, link.CreatedBy)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 1 {
		link.Revision = 1
	}
	return n == 1, err
}

func (r *NetBoxRepository) UpdateLinkTx(tx database.Tx, link *models.NetBoxItemLink) error {
	snapshot, err := encodeNetBoxSnapshot(link.Object)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE netbox_item_links SET snapshot_json = ?, snapshot_updated_at = ?,
		revision = revision + 1 WHERE id = ? AND item_id = ? AND provider_id = ?
		AND object_type = ? AND object_id = ? AND revision = ?`, snapshot, link.SnapshotUpdatedAt,
		link.ID, link.ItemID, link.ProviderID, link.Object.ObjectType, link.Object.ObjectID, link.Revision)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrConcurrentUpdate
	}
	link.Revision++
	return nil
}

func (r *NetBoxRepository) DeleteLinkTx(tx database.Tx, itemID int, linkID string) error {
	result, err := tx.Exec("DELETE FROM netbox_item_links WHERE item_id = ? AND id = ?", itemID, linkID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

func (r *NetBoxRepository) CountLinksForItemTx(tx database.Tx, itemID int) (int, error) {
	var count int
	err := tx.QueryRow("SELECT COUNT(*) FROM netbox_item_links WHERE item_id = ?", itemID).Scan(&count)
	return count, err
}
