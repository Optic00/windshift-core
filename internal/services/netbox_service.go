package services

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"windshift/internal/database"
	"windshift/internal/integrations/netbox"
	"windshift/internal/models"
	"windshift/internal/repository"

	"uuid"
)

var (
	ErrNetBoxUnavailable = errors.New("NetBox connection is unavailable in this workspace")
	ErrNetBoxPermission  = errors.New("NetBox operation is not permitted")
	netBoxSlugPattern    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,79}$`)
)

type NetBoxValidationError struct{ Message string }

func (e *NetBoxValidationError) Error() string { return e.Message }

func netBoxInvalid(message string) error { return &NetBoxValidationError{Message: message} }

type netBoxPermissionChecker interface {
	HasWorkspacePermission(userID, workspaceID int, permission string) (bool, error)
}

// NetBoxService owns only local connection and link lifecycle. All outbound
// operations are GETs; snapshots never enter item history or notifications.
type NetBoxService struct {
	db                database.Database
	repo              *repository.NetBoxRepository
	credentials       *ActionCredentialService
	permission        netBoxPermissionChecker
	transportOverride netbox.Transport
	beforePersist     func()
}

func NewNetBoxService(db database.Database, repo *repository.NetBoxRepository, credentials *ActionCredentialService, permission netBoxPermissionChecker) *NetBoxService {
	return &NetBoxService{db: db, repo: repo, credentials: credentials, permission: permission}
}

// SetTransportForTesting is never called by production bootstrap.
func (s *NetBoxService) SetTransportForTesting(transport netbox.Transport) {
	s.transportOverride = transport
}

func (s *NetBoxService) ListConnections() ([]*models.NetBoxConnection, error) {
	return s.repo.ListConnections()
}

func (s *NetBoxService) GetConnection(id string) (*models.NetBoxConnection, error) {
	return s.repo.GetConnection(id)
}

func (s *NetBoxService) requireAdmin(actorID int) error {
	var allowed bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE id = ? AND is_active = true AND offboarded_at IS NULL)
		AND (`+repository.SystemAdminGrantQuery+`)`, actorID, actorID, actorID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNetBoxPermission
	}
	return nil
}

func validateNetBoxName(name string) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 120 || strings.ContainsFunc(name, unicode.IsControl) {
		return netBoxInvalid("name must contain 1 to 120 characters without control characters")
	}
	return nil
}

func (s *NetBoxService) validateScope(all bool, ids []int) ([]int, error) {
	if len(ids) > 1000 {
		return nil, netBoxInvalid("too many workspace IDs")
	}
	normalized, err := normalizeCredentialWorkspaceIDs(ids)
	if err != nil {
		return nil, netBoxInvalid("workspace IDs must be positive integers")
	}
	if all {
		return []int{}, nil
	}
	if len(normalized) == 0 {
		return nil, netBoxInvalid("select at least one allowed workspace")
	}
	for _, id := range normalized {
		var exists bool
		if err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM workspaces WHERE id = ?)", id).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, netBoxInvalid("an allowed workspace no longer exists")
		}
	}
	return normalized, nil
}

func (s *NetBoxService) CreateConnection(req models.CreateNetBoxConnectionRequest, actorID int) (*models.NetBoxConnection, error) {
	if err := s.requireAdmin(actorID); err != nil {
		return nil, err
	}
	req.Name, req.Slug = strings.TrimSpace(req.Name), strings.TrimSpace(req.Slug)
	if err := validateNetBoxName(req.Name); err != nil {
		return nil, err
	}
	if !netBoxSlugPattern.MatchString(req.Slug) {
		return nil, netBoxInvalid("slug must start with a lowercase letter and contain only lowercase letters, digits, underscores or hyphens")
	}
	if req.AppliesToAllWorkspaces == nil {
		return nil, netBoxInvalid("applies_to_all_workspaces must be explicitly specified")
	}
	baseURL, err := netbox.NormalizeBaseURL(req.BaseURL)
	if err != nil {
		return nil, err
	}
	if req.AuthScheme == "" {
		req.AuthScheme = models.NetBoxAuthBearer
	}
	if err := netbox.ValidateToken(req.APIToken, req.AuthScheme); err != nil {
		return nil, err
	}
	workspaceIDs, err := s.validateScope(*req.AppliesToAllWorkspaces, req.WorkspaceIDs)
	if err != nil {
		return nil, err
	}
	enabled := req.Enabled == nil || *req.Enabled
	connection := &models.NetBoxConnection{
		ProviderID: uuid.New().String(), Slug: req.Slug, Name: req.Name, Enabled: enabled,
		BaseURL: baseURL, AuthScheme: req.AuthScheme, ConfigRevision: 1,
		AppliesToAllWorkspaces: *req.AppliesToAllWorkspaces, WorkspaceIDs: workspaceIDs, CreatedBy: &actorID,
	}
	credential, err := s.credentials.CreateManaged(models.CreateActionCredentialRequest{
		Name: connection.Name + " NetBox API token", CredentialType: models.CredentialCustomHeader,
		Secret: req.APIToken, AppliesToAllWorkspaces: boolPointer(connection.AppliesToAllWorkspaces), WorkspaceIDs: workspaceIDs,
	}, &actorID, string(models.IntegrationProviderNetBox), connection.ProviderID)
	if err != nil {
		return nil, err
	}
	connection.CredentialID = credential.ID
	// This follows the existing compensated managed-credential create pattern.
	// It is not crash-atomic across credential and provider creation.
	if err := s.repo.CreateConnection(connection); err != nil {
		_ = s.credentials.DeleteManaged(credential.ID, string(models.IntegrationProviderNetBox), connection.ProviderID)
		return nil, err
	}
	return s.repo.GetConnection(connection.ProviderID)
}

func (s *NetBoxService) UpdateConnection(id string, req models.UpdateNetBoxConnectionRequest, actorID int) (*models.NetBoxConnection, error) {
	if err := s.requireAdmin(actorID); err != nil {
		return nil, err
	}
	connection, err := s.repo.GetConnection(id)
	if err != nil {
		return nil, err
	}
	if req.Revision <= 0 || req.Revision != connection.ConfigRevision {
		return nil, repository.ErrConcurrentUpdate
	}
	if req.BaseURL != nil && *req.BaseURL != connection.BaseURL || req.AuthScheme != nil && *req.AuthScheme != connection.AuthScheme {
		return nil, netBoxInvalid("base URL and authentication scheme are immutable; create a new connection")
	}
	if req.Name != nil {
		connection.Name = strings.TrimSpace(*req.Name)
	}
	if err := validateNetBoxName(connection.Name); err != nil {
		return nil, err
	}
	if req.Enabled != nil {
		connection.Enabled = *req.Enabled
	}
	if req.AppliesToAllWorkspaces != nil {
		connection.AppliesToAllWorkspaces = *req.AppliesToAllWorkspaces
	}
	if req.WorkspaceIDs != nil {
		connection.WorkspaceIDs = *req.WorkspaceIDs
	}
	connection.WorkspaceIDs, err = s.validateScope(connection.AppliesToAllWorkspaces, connection.WorkspaceIDs)
	if err != nil {
		return nil, err
	}
	if req.APIToken != nil && *req.APIToken != "" {
		if err := netbox.ValidateToken(*req.APIToken, connection.AuthScheme); err != nil {
			return nil, err
		}
	}
	updateCredential, err := s.credentials.PrepareManagedUpdate(connection.CredentialID, connection.Name+" NetBox API token",
		connection.AppliesToAllWorkspaces, connection.WorkspaceIDs, req.APIToken, string(models.IntegrationProviderNetBox), id)
	if err != nil {
		return nil, err
	}
	err = database.WithTx(s.db, func(tx database.Tx) error {
		if err := s.repo.LockConnectionTx(tx, id); err != nil {
			return err
		}
		// Every change, including token rotation, fences in-flight remote reads.
		if err := s.repo.UpdateConnectionTx(tx, connection); err != nil {
			return err
		}
		return updateCredential(tx)
	})
	if err != nil {
		return nil, err
	}
	return s.repo.GetConnection(id)
}

func (s *NetBoxService) DeleteConnection(id string, actorID int) error {
	if err := s.requireAdmin(actorID); err != nil {
		return err
	}
	return database.WithTx(s.db, func(tx database.Tx) error {
		if err := s.repo.LockConnectionTx(tx, id); err != nil {
			return err
		}
		connection, err := s.repo.GetConnectionTx(tx, id)
		if err != nil {
			return err
		}
		var metadata string
		if err := tx.QueryRow("SELECT COALESCE(secret_metadata, '') FROM action_credentials WHERE id = ?", connection.CredentialID).Scan(&metadata); err != nil {
			return err
		}
		if !credentialMatchesPurpose(&models.ActionCredential{SecretMetadata: metadata}, string(models.IntegrationProviderNetBox), id) {
			return ErrCredentialPurposeMismatch
		}
		return s.repo.DeleteConnectionTx(tx, id)
	})
}

func (s *NetBoxService) requireWorkspace(actorID, workspaceID int, permission string) error {
	actor, err := repository.NewUserRepository(s.db).GetActivationTarget(actorID)
	if err != nil {
		return err
	}
	if !actor.IsActive || actor.Offboarded {
		return ErrNetBoxPermission
	}
	allowed, err := s.permission.HasWorkspacePermission(actorID, workspaceID, permission)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNetBoxPermission
	}
	return nil
}

func (s *NetBoxService) itemWorkspace(itemID, actorID int, permission string) (int, error) {
	item, err := repository.NewItemRepository(s.db).FindByID(itemID)
	if err != nil {
		return 0, err
	}
	return item.WorkspaceID, s.requireWorkspace(actorID, item.WorkspaceID, permission)
}

func (s *NetBoxService) ListWorkspaceConnections(workspaceID, actorID int) ([]models.NetBoxWorkspaceConnection, error) {
	if err := s.requireWorkspace(actorID, workspaceID, models.PermissionItemView); err != nil {
		return nil, err
	}
	connections, err := s.repo.ListConnectionsForWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	result := make([]models.NetBoxWorkspaceConnection, 0, len(connections))
	for _, connection := range connections {
		result = append(result, models.NetBoxWorkspaceConnection{ProviderID: connection.ProviderID, Name: connection.Name})
	}
	return result, nil
}

func (s *NetBoxService) client(ctx context.Context, id string, workspaceID int) (*models.NetBoxConnection, *netbox.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	connection, err := s.repo.GetConnection(id)
	if err != nil {
		return nil, nil, err
	}
	available, err := s.repo.IsConnectionAvailableToWorkspace(id, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	if !available {
		return nil, nil, ErrNetBoxUnavailable
	}
	token, _, err := s.credentials.ResolveManaged(ctx, connection.CredentialID, workspaceID, string(models.IntegrationProviderNetBox), id)
	if err != nil {
		return nil, nil, err
	}
	transport := s.transportOverride
	if transport == nil {
		transport = netbox.NewSafeTransport(connection.BaseURL)
	}
	client, err := netbox.NewClient(connection.BaseURL, token, connection.AuthScheme, transport)
	return connection, client, err
}

func (s *NetBoxService) recheckConnection(connection *models.NetBoxConnection, workspaceID int) error {
	current, err := s.repo.GetConnection(connection.ProviderID)
	if err != nil {
		return err
	}
	if current.ConfigRevision != connection.ConfigRevision {
		return repository.ErrConcurrentUpdate
	}
	available, err := s.repo.IsConnectionAvailableToWorkspace(connection.ProviderID, workspaceID)
	if err != nil {
		return err
	}
	if !available {
		return ErrNetBoxUnavailable
	}
	return nil
}

func (s *NetBoxService) TestConnection(ctx context.Context, id string, actorID int) (*models.NetBoxConnectionTestResult, error) {
	if err := s.requireAdmin(actorID); err != nil {
		return nil, err
	}
	connection, err := s.repo.GetConnection(id)
	if err != nil {
		return nil, err
	}
	workspaceID := 0
	if !connection.AppliesToAllWorkspaces {
		if len(connection.WorkspaceIDs) == 0 {
			return nil, netBoxInvalid("connection has no allowed workspaces; select a workspace before testing")
		}
		workspaceID = connection.WorkspaceIDs[0]
	}
	connection, client, err := s.client(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	result, err := client.Test(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.requireAdmin(actorID); err != nil {
		return nil, err
	}
	if err := s.recheckConnection(connection, workspaceID); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *NetBoxService) Search(ctx context.Context, workspaceID, actorID int, connectionID string, objectType models.NetBoxObjectType, query string, limit, offset int) (*models.NetBoxSearchResult, error) {
	if err := s.requireWorkspace(actorID, workspaceID, models.PermissionItemEdit); err != nil {
		return nil, err
	}
	connection, client, err := s.client(ctx, connectionID, workspaceID)
	if err != nil {
		return nil, err
	}
	result, err := client.Search(ctx, objectType, query, limit, offset)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.requireWorkspace(actorID, workspaceID, models.PermissionItemEdit); err != nil {
		return nil, err
	}
	if err := s.recheckConnection(connection, workspaceID); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *NetBoxService) GetItemLinks(itemID, actorID int) ([]*models.NetBoxItemLink, error) {
	workspaceID, err := s.itemWorkspace(itemID, actorID, models.PermissionItemView)
	if err != nil {
		return nil, err
	}
	return s.repo.ListVisibleLinks(itemID, workspaceID)
}

// guardItemTx uses connection -> item -> link lock order. No network I/O or
// outer-database permission-cache reads occur inside a SQLite write transaction.
func (s *NetBoxService) guardItemTx(tx database.Tx, connection *models.NetBoxConnection, itemID, expectedWorkspaceID int) error {
	if err := s.repo.LockConnectionTx(tx, connection.ProviderID); err != nil {
		return err
	}
	current, err := s.repo.GetConnectionTx(tx, connection.ProviderID)
	if err != nil {
		return err
	}
	if current.ConfigRevision != connection.ConfigRevision {
		return repository.ErrConcurrentUpdate
	}
	query := "SELECT workspace_id FROM items WHERE id = ?"
	if s.db.GetDriverName() == "postgres" {
		query += " FOR UPDATE"
	}
	var workspaceID int
	if err := tx.QueryRow(query, itemID).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return repository.ErrNotFound
		}
		return err
	}
	if workspaceID != expectedWorkspaceID {
		return repository.ErrConcurrentUpdate
	}
	available, err := s.repo.IsConnectionAvailableToWorkspaceTx(tx, connection.ProviderID, workspaceID)
	if err != nil {
		return err
	}
	if !available {
		return ErrNetBoxUnavailable
	}
	return nil
}

// LinkObject returns the visible link and whether this call created it.
func (s *NetBoxService) LinkObject(ctx context.Context, itemID, actorID int, req models.CreateNetBoxItemLinkRequest) (*models.NetBoxItemLink, bool, error) {
	workspaceID, err := s.itemWorkspace(itemID, actorID, models.PermissionItemEdit)
	if err != nil {
		return nil, false, err
	}
	connection, client, err := s.client(ctx, req.ConnectionID, workspaceID)
	if err != nil {
		return nil, false, err
	}
	object, err := client.GetObject(ctx, req.ObjectType, req.ObjectID)
	if err != nil {
		return nil, false, err
	}
	link := &models.NetBoxItemLink{ID: uuid.New().String(), ItemID: itemID, ProviderID: connection.ProviderID,
		Object: *object, Revision: 1, SnapshotUpdatedAt: time.Now().UTC(), CreatedBy: &actorID}
	if err := s.beforeCommit(ctx, itemID, actorID, workspaceID); err != nil {
		return nil, false, err
	}
	created := false
	err = database.WithTx(s.db, func(tx database.Tx) error {
		if err := s.guardItemTx(tx, connection, itemID, workspaceID); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		existing, err := s.repo.GetLinkByObjectTx(tx, itemID, connection.ProviderID, req.ObjectType, req.ObjectID)
		if err == nil {
			link = existing
			return nil
		}
		if !errors.Is(err, repository.ErrNotFound) {
			return err
		}
		count, err := s.repo.CountLinksForItemTx(tx, itemID)
		if err != nil {
			return err
		}
		if count >= 100 {
			return netBoxInvalid("an item may have at most 100 NetBox links")
		}
		created, err = s.repo.CreateLinkTx(tx, link)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	link, err = s.repo.GetVisibleLink(itemID, workspaceID, link.ID)
	return link, created, err
}

func (s *NetBoxService) beforeCommit(ctx context.Context, itemID, actorID, workspaceID int) error {
	if s.beforePersist != nil {
		s.beforePersist()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	currentWorkspaceID, err := s.itemWorkspace(itemID, actorID, models.PermissionItemEdit)
	if err != nil {
		return err
	}
	if currentWorkspaceID != workspaceID {
		return repository.ErrConcurrentUpdate
	}
	return nil
}

func (s *NetBoxService) RefreshLink(ctx context.Context, itemID, actorID int, linkID string) (*models.NetBoxItemLink, error) {
	workspaceID, err := s.itemWorkspace(itemID, actorID, models.PermissionItemEdit)
	if err != nil {
		return nil, err
	}
	link, err := s.repo.GetVisibleLink(itemID, workspaceID, linkID)
	if err != nil {
		return nil, err
	}
	connection, client, err := s.client(ctx, link.ProviderID, workspaceID)
	if err != nil {
		return nil, err
	}
	object, err := client.GetObject(ctx, link.Object.ObjectType, link.Object.ObjectID)
	if err != nil {
		return nil, err
	}
	link.Object, link.SnapshotUpdatedAt = *object, time.Now().UTC()
	if err := s.beforeCommit(ctx, itemID, actorID, workspaceID); err != nil {
		return nil, err
	}
	err = database.WithTx(s.db, func(tx database.Tx) error {
		if err := s.guardItemTx(tx, connection, itemID, workspaceID); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.repo.UpdateLinkTx(tx, link)
	})
	if err != nil {
		return nil, err
	}
	return s.repo.GetVisibleLink(itemID, workspaceID, link.ID)
}

func (s *NetBoxService) Unlink(ctx context.Context, itemID, actorID int, linkID string) error {
	workspaceID, err := s.itemWorkspace(itemID, actorID, models.PermissionItemEdit)
	if err != nil {
		return err
	}
	link, err := s.repo.GetVisibleLink(itemID, workspaceID, linkID)
	if err != nil {
		return err
	}
	connection, err := s.repo.GetConnection(link.ProviderID)
	if err != nil {
		return err
	}
	if err := s.beforeCommit(ctx, itemID, actorID, workspaceID); err != nil {
		return err
	}
	return database.WithTx(s.db, func(tx database.Tx) error {
		if err := s.guardItemTx(tx, connection, itemID, workspaceID); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.repo.DeleteLinkTx(tx, itemID, linkID)
	})
}
