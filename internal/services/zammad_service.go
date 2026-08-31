package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/integrations/zammad"
	"windshift/internal/itemevents"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sso"

	"uuid"
)

var zammadFieldNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)
var zammadSlugPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,79}$`)

var ErrZammadReauthorizationRequired = errors.New("zammad OAuth reauthorization is required")
var ErrZammadOAuthSuperseded = errors.New("zammad OAuth operation was superseded by a configuration change")

const zammadOAuthRefreshLeaseDuration = 2 * zammadHTTPTimeout

type ZammadOAuthCallbackResult struct {
	ProviderID      string
	ProviderName    string
	Initiator       *models.User
	OAuthGeneration int64
}

// zammadOAuthCredential is encrypted as the secret of the provider-managed
// action credential. It is deliberately not a value that can be sent as an
// Authorization header, including while OAuth is pending.
type zammadOAuthCredential struct {
	Version      int    `json:"version"`
	Status       string `json:"status"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

func pendingZammadOAuthCredential(status string) string {
	payload, _ := json.Marshal(zammadOAuthCredential{Version: 1, Status: status})
	return string(payload)
}

func activeZammadOAuthCredential(accessToken, refreshToken string) (string, error) {
	payload, err := json.Marshal(zammadOAuthCredential{Version: 1, Status: "active", AccessToken: accessToken, RefreshToken: refreshToken})
	return string(payload), err
}

func parseZammadOAuthCredential(raw string) (*zammadOAuthCredential, error) {
	var payload zammadOAuthCredential
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || payload.Version != 1 || payload.Status != "active" || strings.TrimSpace(payload.AccessToken) == "" || strings.TrimSpace(payload.RefreshToken) == "" {
		return nil, ErrZammadReauthorizationRequired
	}
	return &payload, nil
}

type ZammadValidationError struct{ Message string }

func (e *ZammadValidationError) Error() string { return e.Message }

func zammadValidationError(message string) error {
	return &ZammadValidationError{Message: message}
}

type zammadWorkflowTransitioner interface {
	PerformTransition(context.Context, PerformTransitionRequest, *repository.ItemRepository, *ConditionService, transitionApprovalService) (*PerformTransitionResult, error)
}

type zammadPermissionChecker interface {
	HasWorkspacePermission(userID, workspaceID int, permission string) (bool, error)
}

type ZammadService struct {
	db                      database.Database
	repo                    *repository.ZammadRepository
	credentials             *ActionCredentialService
	permission              zammadPermissionChecker
	workflow                zammadWorkflowTransitioner
	condition               *ConditionService
	approval                *ApprovalService
	events                  *EventCoordinator
	transportOverride       zammad.Transport
	oauthTransportOverride  zammad.Transport
	encryption              *sso.SecretEncryption
	oauthBeforeRefreshClaim func()
}

func NewZammadService(db database.Database, repo *repository.ZammadRepository, credentials *ActionCredentialService, permission zammadPermissionChecker, workflow zammadWorkflowTransitioner, condition *ConditionService, approval *ApprovalService) *ZammadService {
	return &ZammadService{
		db: db, repo: repo, credentials: credentials, permission: permission,
		workflow: workflow, condition: condition, approval: approval,
	}
}

func (s *ZammadService) SetEventCoordinator(events *EventCoordinator) {
	s.events = events
}

// SetTransportForTesting replaces the production SSRF-safe transport. Tests
// use it with httptest; production bootstrap never calls this method.
func (s *ZammadService) SetTransportForTesting(transport zammad.Transport) {
	s.transportOverride = transport
}

// SetOAuthEncryption supplies the system secret realm already used by
// integration_providers. It is set during server bootstrap, never exposed.
func (s *ZammadService) SetOAuthEncryption(encryption *sso.SecretEncryption) {
	s.encryption = encryption
}

func (s *ZammadService) SetOAuthTransportForTesting(transport zammad.Transport) {
	s.oauthTransportOverride = transport
}

func (s *ZammadService) SetOAuthBeforeRefreshClaimForTesting(hook func()) {
	s.oauthBeforeRefreshClaim = hook
}

func (s *ZammadService) ListConnections() ([]*models.ZammadConnection, error) {
	return s.repo.ListConnections()
}

func (s *ZammadService) ListConnectionsForWorkspace(workspaceID int) ([]*models.ZammadConnection, error) {
	return s.repo.ListConnectionsForWorkspace(workspaceID)
}

func (s *ZammadService) GetConnection(id string) (*models.ZammadConnection, error) {
	return s.repo.GetConnection(id)
}

func (s *ZammadService) CreateConnection(req models.CreateZammadConnectionRequest, actorID int) (*models.ZammadConnection, error) {
	connection, err := validateNewZammadConnection(req, actorID)
	if err != nil {
		return nil, err
	}
	if connection.AuthMethod == models.ZammadAuthMethodOAuth {
		if s.encryption == nil {
			return nil, errors.New("zammad OAuth encryption is not configured")
		}
		connection.OAuthClientSecretEncrypted, err = s.encryption.Encrypt(req.OAuthClientSecret)
		if err != nil {
			return nil, fmt.Errorf("encrypt Zammad OAuth client secret: %w", err)
		}
		credential, err := s.credentials.CreateManaged(models.CreateActionCredentialRequest{
			Name: connection.Name + " Zammad OAuth credentials", CredentialType: models.CredentialCustomHeader,
			Secret: pendingZammadOAuthCredential("pending"), AppliesToAllWorkspaces: boolPointer(connection.AppliesToAllWorkspaces), WorkspaceIDs: connection.WorkspaceIDs,
		}, &actorID, string(models.IntegrationProviderZammad), connection.ProviderID)
		if err != nil {
			return nil, err
		}
		connection.CredentialID = credential.ID
		if err := s.repo.CreateConnection(connection); err != nil {
			_ = s.credentials.DeleteManaged(credential.ID, string(models.IntegrationProviderZammad), connection.ProviderID)
			return nil, err
		}
		return s.repo.GetConnection(connection.ProviderID)
	}
	credential, err := s.credentials.CreateManaged(models.CreateActionCredentialRequest{
		Name:                   connection.Name + " Zammad API token",
		CredentialType:         models.CredentialCustomHeader,
		Secret:                 req.APIToken,
		AppliesToAllWorkspaces: boolPointer(connection.AppliesToAllWorkspaces),
		WorkspaceIDs:           connection.WorkspaceIDs,
	}, &actorID, string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil {
		return nil, err
	}
	connection.CredentialID = credential.ID
	connection.HasAPIToken = true
	if err := s.repo.CreateConnection(connection); err != nil {
		_ = s.credentials.DeleteManaged(credential.ID, string(models.IntegrationProviderZammad), connection.ProviderID)
		return nil, err
	}
	return s.repo.GetConnection(connection.ProviderID)
}

func (s *ZammadService) UpdateConnection(id string, req models.UpdateZammadConnectionRequest) (*models.ZammadConnection, error) {
	connection, err := s.repo.GetConnection(id)
	if err != nil {
		return nil, err
	}
	originalBaseURL := connection.BaseURL
	originalCorrelationField := connection.CorrelationField
	originalOAuthClientID := connection.OAuthClientID
	oauthSecretChanged := req.OAuthClientSecret != nil && strings.TrimSpace(*req.OAuthClientSecret) != ""
	if req.Slug != nil {
		connection.Slug = strings.TrimSpace(*req.Slug)
	}
	if req.Name != nil {
		connection.Name = strings.TrimSpace(*req.Name)
	}
	if req.Enabled != nil {
		connection.Enabled = *req.Enabled
	}
	if req.AuthMethod != nil && *req.AuthMethod != connection.AuthMethod {
		return nil, zammadValidationError("auth_method cannot be changed after a connection is created")
	}
	if req.OAuthClientID != nil {
		connection.OAuthClientID = strings.TrimSpace(*req.OAuthClientID)
	}
	if oauthSecretChanged {
		if s.encryption == nil {
			return nil, errors.New("zammad OAuth encryption is not configured")
		}
		connection.OAuthClientSecretEncrypted, err = s.encryption.Encrypt(*req.OAuthClientSecret)
		if err != nil {
			return nil, fmt.Errorf("encrypt Zammad OAuth client secret: %w", err)
		}
	}
	if req.BaseURL != nil {
		connection.BaseURL, err = NormalizeZammadBaseURL(*req.BaseURL)
		if err != nil {
			return nil, err
		}
	}
	if req.DefaultGroupID != nil {
		connection.DefaultGroupID = *req.DefaultGroupID
	}
	if req.DefaultGroupName != nil {
		connection.DefaultGroupName = strings.TrimSpace(*req.DefaultGroupName)
	}
	if req.AllowedGroupIDs != nil {
		if hasNonPositiveIDs(*req.AllowedGroupIDs) {
			return nil, zammadValidationError("allowed_group_ids must contain positive IDs")
		}
		connection.AllowedGroupIDs = normalizePositiveIDs(*req.AllowedGroupIDs)
	}
	if req.DefaultCustomer != nil {
		connection.DefaultCustomer = strings.TrimSpace(*req.DefaultCustomer)
	}
	if req.CorrelationField != nil {
		connection.CorrelationField = strings.TrimSpace(*req.CorrelationField)
	}
	if req.ClosedStateIDs != nil {
		if hasNonPositiveIDs(*req.ClosedStateIDs) {
			return nil, zammadValidationError("closed_state_ids must contain positive IDs")
		}
		connection.ClosedStateIDs = normalizePositiveIDs(*req.ClosedStateIDs)
	}
	if req.ClearCompletionStatus {
		connection.CompletionStatusID = nil
	} else if req.CompletionStatusID != nil {
		v := *req.CompletionStatusID
		connection.CompletionStatusID = &v
	}
	if req.AppliesToAllWorkspaces != nil {
		connection.AppliesToAllWorkspaces = *req.AppliesToAllWorkspaces
	}
	if req.WorkspaceIDs != nil {
		if hasNonPositiveIDs(*req.WorkspaceIDs) {
			return nil, zammadValidationError("workspace_ids must contain positive IDs")
		}
		connection.WorkspaceIDs = normalizePositiveIDs(*req.WorkspaceIDs)
	}
	if err := validateZammadConnection(connection); err != nil {
		return nil, err
	}
	if connection.BaseURL != originalBaseURL || connection.CorrelationField != originalCorrelationField {
		hasLinks, err := s.repo.HasTicketLinks(connection.ProviderID)
		if err != nil {
			return nil, err
		}
		if hasLinks {
			return nil, zammadValidationError("base_url and correlation_field cannot change after ticket creation has started")
		}
	}
	if connection.AuthMethod == models.ZammadAuthMethodOAuth {
		if err := validateZammadOAuthConfiguration(connection); err != nil {
			return nil, err
		}
		credentialsChanged := connection.BaseURL != originalBaseURL || connection.OAuthClientID != originalOAuthClientID || oauthSecretChanged
		var replacementSecret *string
		if credentialsChanged {
			pending := pendingZammadOAuthCredential("pending")
			replacementSecret = &pending
		}
		credentialUpdate, err := s.credentials.PrepareManagedUpdate(connection.CredentialID,
			connection.Name+" Zammad OAuth credentials", connection.AppliesToAllWorkspaces, connection.WorkspaceIDs, replacementSecret,
			string(models.IntegrationProviderZammad), connection.ProviderID)
		if err != nil {
			return nil, err
		}
		if err := database.WithTx(s.db, func(tx database.Tx) error {
			if err := s.repo.UpdateConnectionTx(tx, connection); err != nil {
				return err
			}
			if err := credentialUpdate(tx); err != nil {
				return err
			}
			if replacementSecret != nil {
				return s.repo.ResetOAuthAuthorizationTx(tx, connection.ProviderID)
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return s.repo.GetConnection(id)
	}
	if connection.CredentialID <= 0 {
		return nil, zammadValidationError("API token is not configured")
	}
	credentialUpdate, err := s.credentials.PrepareManagedUpdate(connection.CredentialID,
		connection.Name+" Zammad API token", connection.AppliesToAllWorkspaces,
		connection.WorkspaceIDs, req.APIToken, string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil {
		return nil, err
	}
	if err := database.WithTx(s.db, func(tx database.Tx) error {
		if err := s.repo.UpdateConnectionTx(tx, connection); err != nil {
			return err
		}
		return credentialUpdate(tx)
	}); err != nil {
		return nil, err
	}
	return s.repo.GetConnection(id)
}

func (s *ZammadService) DeleteConnection(id string) error {
	hasLinks, err := s.repo.HasTicketLinksForConnection(id)
	if err != nil {
		return err
	}
	if hasLinks {
		return zammadValidationError("unlink all Zammad tickets before deleting this connection")
	}
	return s.repo.DeleteConnection(id)
}

// StartOAuth stores a short-lived state bound to this system connection and
// the initiating administrator. The callback URI is deliberately fixed.
func (s *ZammadService) StartOAuth(ctx context.Context, id string, actorID int, publicBaseURL string) (string, error) {
	connection, err := s.repo.GetConnection(id)
	if err != nil {
		return "", err
	}
	if connection.AuthMethod != models.ZammadAuthMethodOAuth {
		return "", zammadValidationError("connection does not use OAuth")
	}
	if err := validateZammadOAuthConfiguration(connection); err != nil {
		return "", err
	}
	redirectURI, err := zammadOAuthRedirectURI(publicBaseURL)
	if err != nil {
		return "", err
	}
	state := uuid.New().String() + uuid.New().String()
	if err := s.repo.CreateOAuthState(state, connection.ProviderID, actorID, connection.OAuthGeneration, time.Now().Add(5*time.Minute)); err != nil {
		return "", err
	}
	authorizeURL := connection.BaseURL + "/oauth/authorize?" + url.Values{"response_type": {"code"}, "client_id": {connection.OAuthClientID}, "redirect_uri": {redirectURI}, "scope": {"full"}, "state": {state}}.Encode()
	return authorizeURL, nil
}

func (s *ZammadService) InvalidateOAuthState(state string) error {
	if strings.TrimSpace(state) == "" {
		return nil
	}
	return s.repo.InvalidateOAuthState(state)
}

func (s *ZammadService) ConsumeFailedOAuthCallback(state string) (*ZammadOAuthCallbackResult, error) {
	if strings.TrimSpace(state) == "" {
		return nil, repository.ErrNotFound
	}
	consumed, err := s.repo.ConsumeOAuthState(state)
	if err != nil {
		return nil, err
	}
	if err := s.repo.ClearOAuthAttempt(consumed.ProviderID, consumed.OAuthGeneration, state); err != nil {
		return s.oauthCallbackResult(consumed), err
	}
	return s.oauthCallbackResult(consumed), nil
}

func (s *ZammadService) CompleteOAuth(ctx context.Context, state, code, publicBaseURL string) (*ZammadOAuthCallbackResult, error) {
	redirectURI, err := zammadOAuthRedirectURI(publicBaseURL)
	if err != nil {
		return nil, err
	}
	consumed, err := s.repo.ConsumeOAuthState(state)
	if err != nil {
		return nil, err
	}
	attemptActive := true
	defer func() {
		if attemptActive {
			_ = s.repo.ClearOAuthAttempt(consumed.ProviderID, consumed.OAuthGeneration, state)
		}
	}()
	result := s.oauthCallbackResult(consumed)
	connection, err := s.repo.GetConnection(consumed.ProviderID)
	if err != nil {
		return result, err
	}
	result.ProviderName = connection.Name
	if connection.OAuthGeneration != consumed.OAuthGeneration {
		return result, ErrZammadOAuthSuperseded
	}
	if connection.AuthMethod != models.ZammadAuthMethodOAuth {
		return result, zammadValidationError("connection OAuth configuration changed")
	}
	if err := validateZammadOAuthConfiguration(connection); err != nil {
		return result, err
	}
	if s.encryption == nil {
		return result, errors.New("zammad OAuth encryption is not configured")
	}
	secret, err := s.encryption.Decrypt(connection.OAuthClientSecretEncrypted)
	if err != nil {
		return result, errors.New("could not decrypt Zammad OAuth client secret")
	}
	tokens, err := zammad.ExchangeOAuthCode(ctx, s.oauthTransport(connection), connection.BaseURL+"/oauth/token", connection.OAuthClientID, secret, code, redirectURI)
	if err != nil {
		return result, err
	}
	bundle, err := activeZammadOAuthCredential(tokens.AccessToken, tokens.RefreshToken)
	if err != nil {
		return result, err
	}
	credentialUpdate, err := s.credentials.PrepareManagedSecretUpdate(connection.CredentialID, bundle,
		string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil {
		return result, err
	}
	if err := database.WithTx(s.db, func(tx database.Tx) error {
		current, err := s.repo.GuardOAuthCallbackTx(tx, connection.ProviderID, consumed.OAuthGeneration, state)
		if err != nil {
			return err
		}
		if !current {
			return ErrZammadOAuthSuperseded
		}
		if err := credentialUpdate(tx); err != nil {
			return err
		}
		return s.repo.UpsertOAuthTokenTx(tx, repository.ZammadOAuthToken{ProviderID: connection.ProviderID, OAuthGeneration: consumed.OAuthGeneration, ExpiresAt: time.Now().Add(tokens.ExpiresIn)})
	}); err != nil {
		return result, err
	}
	attemptActive = false
	return result, nil
}

func (s *ZammadService) oauthCallbackResult(consumed *repository.ZammadOAuthState) *ZammadOAuthCallbackResult {
	result := &ZammadOAuthCallbackResult{ProviderID: consumed.ProviderID, OAuthGeneration: consumed.OAuthGeneration}
	initiator, err := repository.NewUserRepository(s.db).GetByID(consumed.InitiatedBy)
	if err != nil {
		initiator = &models.User{ID: consumed.InitiatedBy, Username: "unknown"}
	}
	result.Initiator = initiator
	return result
}

func (s *ZammadService) oauthTransport(connection *models.ZammadConnection) zammad.Transport {
	if s.oauthTransportOverride != nil {
		return s.oauthTransportOverride
	}
	return newZammadSafeTransport(connection.BaseURL, "/oauth/token")
}

func (s *ZammadService) oauthAccessToken(ctx context.Context, connection *models.ZammadConnection, workspaceID int) (string, error) {
	if s.encryption == nil {
		return "", errors.New("zammad OAuth encryption is not configured")
	}
	token, err := s.repo.GetOAuthToken(connection.ProviderID)
	if err != nil {
		return "", ErrZammadReauthorizationRequired
	}
	if token.ReauthorizationRequired {
		return "", ErrZammadReauthorizationRequired
	}
	if token.OAuthGeneration != connection.OAuthGeneration {
		return "", ErrZammadOAuthSuperseded
	}
	if token.ExpiresAt.After(time.Now().Add(2 * time.Minute)) {
		raw, _, err := s.credentials.ResolveManaged(ctx, connection.CredentialID, workspaceID, string(models.IntegrationProviderZammad), connection.ProviderID)
		if err != nil {
			return "", err
		}
		bundle, err := parseZammadOAuthCredential(raw)
		if err != nil {
			return "", err
		}
		return bundle.AccessToken, nil
	}
	if s.oauthBeforeRefreshClaim != nil {
		s.oauthBeforeRefreshClaim()
	}
	claimProviderID := connection.ProviderID
	claimGeneration := token.OAuthGeneration
	claimOwner := uuid.New().String()
	claimed, err := s.repo.ClaimOAuthRefresh(claimProviderID, claimGeneration, claimOwner, time.Now().Add(zammadOAuthRefreshLeaseDuration))
	if err != nil {
		return "", err
	}
	if !claimed {
		// Another request owns the short per-connection refresh lease. Never
		// issue a competing refresh request, which could invalidate rotation.
		return "", errors.New("zammad OAuth refresh is already in progress")
	}
	claimActive := true
	defer func() {
		if claimActive {
			_ = s.repo.ReleaseOAuthRefreshClaim(claimProviderID, claimGeneration, claimOwner)
		}
	}()
	connection, err = s.repo.GetConnection(claimProviderID)
	if err != nil {
		return "", err
	}
	if connection.OAuthGeneration != claimGeneration {
		return "", ErrZammadOAuthSuperseded
	}
	token, err = s.repo.GetOAuthTokenForRefreshClaim(claimProviderID, claimGeneration, claimOwner)
	if err != nil {
		return "", ErrZammadOAuthSuperseded
	}
	if token.ReauthorizationRequired {
		return "", ErrZammadReauthorizationRequired
	}
	raw, _, err := s.credentials.ResolveManaged(ctx, connection.CredentialID, workspaceID, string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil {
		return "", err
	}
	bundle, err := parseZammadOAuthCredential(raw)
	if err != nil {
		return "", err
	}
	if token.ExpiresAt.After(time.Now().Add(2 * time.Minute)) {
		if err := s.repo.ReleaseOAuthRefreshClaim(claimProviderID, claimGeneration, claimOwner); err != nil {
			return "", err
		}
		claimActive = false
		return bundle.AccessToken, nil
	}
	clientSecret, err := s.encryption.Decrypt(connection.OAuthClientSecretEncrypted)
	if err != nil {
		return "", errors.New("could not decrypt Zammad OAuth client secret")
	}
	refreshed, err := zammad.RefreshOAuthToken(ctx, s.oauthTransport(connection), connection.BaseURL+"/oauth/token", connection.OAuthClientID, clientSecret, bundle.RefreshToken)
	if errors.Is(err, zammad.ErrInvalidGrant) {
		if persistErr := s.markOAuthReauthorizationRequired(connection, connection.OAuthGeneration, claimOwner); persistErr != nil {
			return "", persistErr
		}
		claimActive = false
		return "", ErrZammadReauthorizationRequired
	}
	if err != nil {
		return "", err
	}
	updatedBundle, err := activeZammadOAuthCredential(refreshed.AccessToken, refreshed.RefreshToken)
	if err != nil {
		return "", err
	}
	credentialUpdate, err := s.credentials.PrepareManagedSecretUpdate(connection.CredentialID, updatedBundle,
		string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil {
		return "", err
	}
	if err := database.WithTx(s.db, func(tx database.Tx) error {
		current, err := s.repo.GuardOAuthGenerationTx(tx, connection.ProviderID, connection.OAuthGeneration)
		if err != nil {
			return err
		}
		if !current {
			return ErrZammadOAuthSuperseded
		}
		owned, err := s.repo.GuardOAuthRefreshClaimTx(tx, connection.ProviderID, connection.OAuthGeneration, claimOwner)
		if err != nil {
			return err
		}
		if !owned {
			return ErrZammadOAuthSuperseded
		}
		if err := credentialUpdate(tx); err != nil {
			return err
		}
		return s.repo.UpsertOAuthTokenTx(tx, repository.ZammadOAuthToken{ProviderID: connection.ProviderID, OAuthGeneration: connection.OAuthGeneration, ExpiresAt: time.Now().Add(refreshed.ExpiresIn)})
	}); err != nil {
		return "", err
	}
	claimActive = false
	return refreshed.AccessToken, nil
}

func (s *ZammadService) markOAuthReauthorizationRequired(connection *models.ZammadConnection, generation int64, claimOwner string) error {
	pending := pendingZammadOAuthCredential("reauthorization_required")
	credentialUpdate, err := s.credentials.PrepareManagedSecretUpdate(connection.CredentialID, pending,
		string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil {
		return err
	}
	return database.WithTx(s.db, func(tx database.Tx) error {
		current, err := s.repo.GuardOAuthGenerationTx(tx, connection.ProviderID, generation)
		if err != nil {
			return err
		}
		if !current {
			return ErrZammadOAuthSuperseded
		}
		owned, err := s.repo.GuardOAuthRefreshClaimTx(tx, connection.ProviderID, generation, claimOwner)
		if err != nil {
			return err
		}
		if !owned {
			return ErrZammadOAuthSuperseded
		}
		if err := credentialUpdate(tx); err != nil {
			return err
		}
		marked, err := s.repo.MarkOAuthReauthorizationRequiredTx(tx, connection.ProviderID, generation, claimOwner)
		if err != nil {
			return err
		}
		if !marked {
			return ErrZammadOAuthSuperseded
		}
		return nil
	})
}

func (s *ZammadService) TestConnection(ctx context.Context, id string) (*models.ZammadConnectionMetadata, error) {
	connection, err := s.repo.GetConnection(id)
	if err != nil {
		return nil, err
	}
	workspaceID := 0
	if !connection.AppliesToAllWorkspaces && len(connection.WorkspaceIDs) > 0 {
		workspaceID = connection.WorkspaceIDs[0]
	}
	client, err := s.clientForConnection(ctx, connection, workspaceID)
	if err != nil {
		return nil, err
	}
	metadata, err := client.Metadata(ctx)
	if err == nil && connection.AuthMethod == models.ZammadAuthMethodAPIToken {
		err = client.ValidateCorrelationField(ctx, connection.CorrelationField)
		metadata.CorrelationFieldVerified = err == nil
	} else if err == nil {
		// The scoped OAuth service account intentionally need not have
		// admin.object. It is still tested against the groups and ticket-state
		// endpoints it actually needs; the custom correlation field is shown as
		// explicitly unverified rather than requiring broader privileges.
		metadata.CorrelationFieldVerified = false
	}
	if err == nil {
		err = validateZammadMetadata(connection, metadata)
	}
	safeError := ""
	if err != nil {
		safeError = RedactString(err.Error())
	}
	_ = s.repo.SetConnectionTestResult(connection.ProviderID, time.Now(), safeError)
	return metadata, err
}

func (s *ZammadService) MetadataForWorkspace(ctx context.Context, id string, workspaceID int) (*models.ZammadConnectionMetadata, error) {
	connection, client, err := s.client(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	metadata, err := client.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	metadata.Groups = allowedZammadGroups(connection, metadata.Groups)
	return metadata, nil
}

func (s *ZammadService) OwnersForWorkspace(ctx context.Context, id string, workspaceID, groupID int) ([]models.ZammadOwner, error) {
	if groupID <= 0 {
		return nil, zammadValidationError("group_id must be positive")
	}
	connection, client, err := s.client(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	groups, err := client.Groups(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := allowedZammadGroup(connection, groups, groupID); err != nil {
		return nil, err
	}
	owners, err := client.Owners(ctx, groupID)
	if err != nil {
		return nil, err
	}
	result := make([]models.ZammadOwner, 0, len(owners)+1)
	result = append(result, models.ZammadOwner{ID: 1, Name: "Unassigned"})
	for _, owner := range owners {
		if owner.ID != 1 {
			result = append(result, models.ZammadOwner{ID: owner.ID, Name: owner.Name})
		}
	}
	return result, nil
}

func (s *ZammadService) TicketLinksForItem(itemID int) ([]*models.ZammadTicketLink, error) {
	return s.repo.GetTicketLinksForItem(itemID)
}

func (s *ZammadService) GetTicketLink(id string) (*models.ZammadTicketLink, error) {
	return s.repo.GetTicketLink(id)
}

// LinkExistingTicket attaches a remote ticket without creating a second one.
// The local reservation is written before the remote correlation field so a
// competing item cannot claim the same provider/ticket pair.
func (s *ZammadService) LinkExistingTicket(ctx context.Context, itemID, actorID int, req models.LinkZammadTicketRequest) (*models.ZammadTicketLink, error) {
	req.ConnectionID = strings.TrimSpace(req.ConnectionID)
	req.TicketNumber = strings.TrimSpace(req.TicketNumber)
	if req.ConnectionID == "" || req.TicketNumber == "" {
		return nil, zammadValidationError("connection_id and ticket_number are required")
	}
	item, err := repository.NewItemRepository(s.db).FindByIDWithDetails(itemID)
	if err != nil {
		return nil, err
	}
	connection, client, err := s.client(ctx, req.ConnectionID, item.WorkspaceID)
	if err != nil {
		return nil, err
	}
	found, err := client.FindByNumber(ctx, req.TicketNumber)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, zammadValidationError("Zammad ticket was not found")
	}
	ticket, err := client.GetTicket(ctx, found.ID)
	if err != nil {
		return nil, err
	}
	if ticket.Number != req.TicketNumber {
		return nil, zammadValidationError("Zammad ticket was not found")
	}
	metadata, err := client.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	group, err := allowedZammadGroup(connection, metadata.Groups, ticket.GroupID)
	if err != nil {
		return nil, err
	}
	correlation := fmt.Sprintf("windshift:%s:%s-%d", connection.ProviderID, item.WorkspaceKey, item.WorkspaceItemNumber)
	link, err := s.repo.GetTicketLinkForItem(itemID, connection.ProviderID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}
	if link != nil {
		if link.TicketID != ticket.ID {
			return nil, zammadValidationError("this item already has another Zammad ticket for the selected connection")
		}
		correlation = link.CorrelationKey
		if link.SyncState == models.ZammadSyncLinked && link.ItemIntegrationLinkID != "" {
			return link, nil
		}
	}
	remoteCorrelation, err := zammadTicketAttributeString(ticket, connection.CorrelationField)
	if err != nil {
		return nil, err
	}
	if remoteCorrelation != "" && remoteCorrelation != correlation {
		return nil, zammadValidationError("Zammad ticket is already linked through another correlation key")
	}
	if link == nil {
		statusName := zammadStateName(metadata, ticket.StateID, ticket.StateName)
		groupName := ticket.GroupName
		if groupName == "" {
			groupName = group.Name
		}
		link = &models.ZammadTicketLink{
			ID: uuid.New().String(), ItemID: itemID, ProviderID: connection.ProviderID,
			TicketID: ticket.ID, TicketNumber: ticket.Number,
			TicketURL: connection.BaseURL + "/#ticket/zoom/" + strconv.Itoa(ticket.ID),
			GroupID:   ticket.GroupID, GroupName: groupName,
			OwnerID: ticket.OwnerID, OwnerName: ticket.OwnerName,
			CorrelationKey: correlation, SyncState: models.ZammadSyncCreating,
			LastStatusID: ticket.StateID, LastStatusName: statusName, CreatedBy: &actorID,
		}
		if err := s.repo.ReserveExistingTicketLink(link); err != nil {
			if !errors.Is(err, repository.ErrDuplicateEntry) {
				return nil, err
			}
			existing, getErr := s.repo.GetTicketLinkForItem(itemID, connection.ProviderID)
			if getErr != nil || existing.TicketID != ticket.ID {
				return nil, err
			}
			link = existing
			correlation = link.CorrelationKey
		}
	}

	if remoteCorrelation == "" {
		ticket, err = client.UpdateTicket(ctx, ticket.ID, nil, nil, nil, connection.CorrelationField, correlation)
		if err != nil {
			return nil, err
		}
	}
	link.TicketID = ticket.ID
	link.TicketNumber = ticket.Number
	link.TicketURL = connection.BaseURL + "/#ticket/zoom/" + strconv.Itoa(ticket.ID)
	link.GroupID = ticket.GroupID
	link.GroupName = zammadGroupName(metadata, ticket.GroupID, ticket.GroupName)
	link.OwnerID = ticket.OwnerID
	link.OwnerName = resolveZammadOwnerName(ctx, client, ticket)
	link.LastStatusID = ticket.StateID
	link.LastStatusName = zammadStateName(metadata, ticket.StateID, ticket.StateName)
	if err := s.repo.CompleteExistingTicketLink(link.ID, link.ID+"-external", link, actorID); err != nil {
		return nil, err
	}
	return s.repo.GetTicketLink(link.ID)
}

func (s *ZammadService) CreateTicket(ctx context.Context, itemID, actorID int, req models.CreateZammadTicketRequest) (*models.ZammadTicketLink, error) {
	item, err := repository.NewItemRepository(s.db).FindByIDWithDetails(itemID)
	if err != nil {
		return nil, err
	}
	connection, client, err := s.client(ctx, req.ConnectionID, item.WorkspaceID)
	if err != nil {
		return nil, err
	}
	groupID, groupName := req.GroupID, ""
	if groupID == 0 {
		groupID = connection.DefaultGroupID
	}
	groups, metadataErr := client.Groups(ctx)
	if metadataErr != nil {
		return nil, metadataErr
	}
	for _, group := range groups {
		if group.ID == groupID || (groupID == 0 && group.Name == connection.DefaultGroupName) {
			groupID = group.ID
			groupName = group.Name
			break
		}
	}
	if groupID == 0 || groupName == "" {
		return nil, zammadValidationError("selected Zammad group is missing or inactive")
	}
	allowed := slices.Contains(connection.AllowedGroupIDs, groupID)
	if len(connection.AllowedGroupIDs) == 0 {
		allowed = (connection.DefaultGroupID > 0 && connection.DefaultGroupID == groupID) ||
			(connection.DefaultGroupID == 0 && connection.DefaultGroupName == groupName)
	}
	if !allowed {
		return nil, zammadValidationError("selected Zammad group is not allowed for this connection")
	}
	correlation := fmt.Sprintf("windshift:%s:%s-%d", connection.ProviderID, item.WorkspaceKey, item.WorkspaceItemNumber)
	link, err := s.repo.GetTicketLinkForItem(itemID, connection.ProviderID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}
	if link == nil {
		link = &models.ZammadTicketLink{
			ID: uuid.New().String(), ItemID: itemID, ProviderID: connection.ProviderID,
			GroupID: groupID, GroupName: groupName, CorrelationKey: correlation,
			SyncState: models.ZammadSyncPending, CreatedBy: &actorID,
		}
		if err := s.repo.CreatePendingTicketLink(link); err != nil && !errors.Is(err, repository.ErrDuplicateEntry) {
			return nil, err
		}
		link, err = s.repo.GetTicketLinkForItem(itemID, connection.ProviderID)
		if err != nil {
			return nil, err
		}
	} else {
		// Once a durable creation attempt exists, retries keep its original
		// destination and correlation key. This also covers an item moved to a
		// different workspace between attempts.
		groupID = link.GroupID
		groupName = link.GroupName
		correlation = link.CorrelationKey
	}
	if link.TicketID != 0 {
		return link, nil
	}
	wasUncertain := link.SyncState == models.ZammadSyncUncertain
	claimed, err := s.repo.ClaimTicketCreation(itemID, connection.ProviderID, time.Now())
	if err != nil {
		return nil, err
	}
	if !claimed {
		return s.repo.GetTicketLinkForItem(itemID, connection.ProviderID)
	}

	ticket, requestErr := client.FindByCorrelation(ctx, connection.CorrelationField, correlation)
	postAttempted := false
	if requestErr == nil && ticket == nil && wasUncertain {
		_ = s.repo.MarkTicketLinkUncertain(link.ID, "Zammad ticket creation outcome is uncertain; retry only searches by correlation key")
		return s.repo.GetTicketLink(link.ID)
	}
	if requestErr == nil && ticket == nil {
		postAttempted = true
		title := truncateRunes(fmt.Sprintf("[%s-%d] %s", item.WorkspaceKey, item.WorkspaceItemNumber, item.Title), 200)
		body := truncateRunes(strings.TrimSpace(item.Description), 20000)
		if body == "" {
			body = title
		}
		ticket, requestErr = client.CreateTicket(ctx, title, body, connection.DefaultCustomer,
			groupName, connection.CorrelationField, correlation)
	}
	if requestErr != nil {
		safeError := RedactString(requestErr.Error())
		if postAttempted && zammadCreationOutcomeUncertain(requestErr) {
			_ = s.repo.MarkTicketLinkUncertain(link.ID, safeError)
		} else {
			_ = s.repo.MarkTicketLinkFailed(link.ID, safeError)
		}
		return nil, requestErr
	}
	if ticket.GroupID == 0 && !postAttempted {
		ticket, requestErr = client.GetTicket(ctx, ticket.ID)
		if requestErr != nil {
			_ = s.repo.MarkTicketLinkUncertain(link.ID, RedactString(requestErr.Error()))
			return nil, requestErr
		}
	}
	ticketGroupID := ticket.GroupID
	if ticketGroupID == 0 {
		ticketGroupID = groupID
		ticket.GroupID = groupID
	}
	allowedGroup, groupErr := s.requireAllowedTicketGroup(ctx, connection, client, ticket, groups)
	if groupErr != nil {
		_ = s.repo.MarkTicketLinkUncertain(link.ID, RedactString(groupErr.Error()))
		return nil, groupErr
	}
	statusName := ticket.StateName
	if statusName == "" {
		statusName = s.resolveStateName(ctx, client, ticket.StateID)
	}
	ticketGroupName := ticket.GroupName
	if ticketGroupName == "" {
		ticketGroupName = allowedGroup.Name
	}
	ticketURL := connection.BaseURL + "/#ticket/zoom/" + fmt.Sprintf("%d", ticket.ID)
	if err := s.repo.CompleteTicketCreation(link.ID, ticket.ID, ticket.Number, ticketURL,
		ticket.StateID, statusName, ticketGroupID, ticketGroupName, ticket.OwnerID, ticket.OwnerName, actorID); err != nil {
		// The remote ticket is known to exist. Keep retries search-only until
		// the durable local association has been completed.
		_ = s.repo.MarkTicketLinkUncertain(link.ID, RedactString(err.Error()))
		return nil, err
	}
	return s.repo.GetTicketLink(link.ID)
}

func validateZammadMetadata(connection *models.ZammadConnection, metadata *models.ZammadConnectionMetadata) error {
	activeGroups := make(map[int]string, len(metadata.Groups))
	for _, group := range metadata.Groups {
		activeGroups[group.ID] = group.Name
	}
	if connection.DefaultGroupID > 0 {
		if _, ok := activeGroups[connection.DefaultGroupID]; !ok {
			return zammadValidationError("default Zammad group is missing or inactive")
		}
	} else if connection.DefaultGroupName != "" {
		found := false
		for _, name := range activeGroups {
			if name == connection.DefaultGroupName {
				found = true
				break
			}
		}
		if !found {
			return zammadValidationError("default Zammad group is missing or inactive")
		}
	}
	for _, groupID := range connection.AllowedGroupIDs {
		if _, ok := activeGroups[groupID]; !ok {
			return zammadValidationError(fmt.Sprintf("allowed Zammad group %d is missing or inactive", groupID))
		}
	}
	activeStates := make(map[int]struct{}, len(metadata.States))
	for _, state := range metadata.States {
		activeStates[state.ID] = struct{}{}
	}
	for _, stateID := range connection.ClosedStateIDs {
		if _, ok := activeStates[stateID]; !ok {
			return zammadValidationError(fmt.Sprintf("closed Zammad state %d is missing or inactive", stateID))
		}
	}
	return nil
}

func (s *ZammadService) UpdateTicketLink(ctx context.Context, linkID string, req models.UpdateZammadTicketLinkRequest) (*models.ZammadTicketLink, error) {
	if req.StateID == nil && req.GroupID == nil && req.OwnerID == nil {
		return nil, zammadValidationError("at least one of state_id, group_id, or owner_id is required")
	}
	if (req.StateID != nil && *req.StateID <= 0) || (req.GroupID != nil && *req.GroupID <= 0) || (req.OwnerID != nil && *req.OwnerID <= 0) {
		return nil, zammadValidationError("state_id, group_id, and owner_id must be positive")
	}
	link, err := s.repo.GetTicketLink(linkID)
	if err != nil {
		return nil, err
	}
	if link.TicketID <= 0 {
		return nil, zammadValidationError("Zammad ticket has not been created")
	}
	item, err := repository.NewItemRepository(s.db).FindByIDWithDetails(link.ItemID)
	if err != nil {
		return nil, err
	}
	connection, client, err := s.client(ctx, link.ProviderID, item.WorkspaceID)
	if err != nil {
		return nil, err
	}
	current, err := client.GetTicket(ctx, link.TicketID)
	if err != nil {
		return nil, err
	}
	metadata, err := client.Metadata(ctx)
	if err != nil {
		return nil, err
	}

	effectiveGroupID := current.GroupID
	if req.GroupID != nil {
		effectiveGroupID = *req.GroupID
	}
	if _, err := allowedZammadGroup(connection, metadata.Groups, effectiveGroupID); err != nil {
		return nil, err
	}
	if req.StateID != nil && !zammadStateExists(metadata, *req.StateID) {
		return nil, zammadValidationError("selected Zammad state is missing or inactive")
	}

	ownerID := req.OwnerID
	if req.GroupID != nil && req.OwnerID == nil && *req.GroupID != current.GroupID {
		// An owner may not have change access in the new group. Zammad's
		// unassigned system user is the deterministic safe default.
		unassigned := 1
		ownerID = &unassigned
	}
	if ownerID != nil && *ownerID != 1 {
		owners, err := client.Owners(ctx, effectiveGroupID)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(owners, func(owner zammad.Owner) bool { return owner.ID == *ownerID }) {
			return nil, zammadValidationError("selected Zammad owner cannot be assigned tickets in this group")
		}
	}

	updated, err := client.UpdateTicket(ctx, link.TicketID, req.StateID, req.GroupID, ownerID, "", "")
	if err != nil {
		return nil, err
	}
	if err := s.persistTicketSnapshot(ctx, link, connection, client, updated, metadata); err != nil {
		return nil, err
	}
	return s.repo.GetTicketLink(link.ID)
}

// UnlinkTicket removes the Windshift association but never deletes the Zammad
// ticket. The remote correlation field is cleared only when it still contains
// this link's exact value. Ambiguous upstream failures keep the local link so
// the user can safely retry.
func (s *ZammadService) UnlinkTicket(ctx context.Context, linkID string) (*models.ZammadTicketLink, error) {
	link, err := s.repo.GetTicketLink(linkID)
	if err != nil {
		return nil, err
	}
	if link.TicketID > 0 {
		item, err := repository.NewItemRepository(s.db).FindByIDWithDetails(link.ItemID)
		if err != nil {
			return nil, err
		}
		connection, client, err := s.client(ctx, link.ProviderID, item.WorkspaceID)
		if err != nil {
			return nil, err
		}
		ticket, getErr := client.GetTicket(ctx, link.TicketID)
		if getErr != nil {
			var apiErr *zammad.APIError
			if !errors.As(getErr, &apiErr) || apiErr.StatusCode != 404 {
				return nil, getErr
			}
		} else {
			remoteCorrelation, err := zammadTicketAttributeString(ticket, connection.CorrelationField)
			if err != nil {
				return nil, err
			}
			if remoteCorrelation == link.CorrelationKey {
				if _, err := client.UpdateTicket(ctx, ticket.ID, nil, nil, nil, connection.CorrelationField, ""); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := s.repo.DeleteTicketLink(link.ID); err != nil {
		return nil, err
	}
	return link, nil
}

func (s *ZammadService) SyncTicketLink(ctx context.Context, linkID string) (*models.ZammadTicketLink, error) {
	link, err := s.repo.GetTicketLink(linkID)
	if err != nil {
		return nil, err
	}
	if link.TicketID == 0 {
		return nil, zammadValidationError("Zammad ticket has not been created")
	}
	claimed, err := s.repo.ClaimSync(link.ID, time.Now().Add(2*time.Minute))
	if err != nil {
		return nil, err
	}
	if !claimed {
		return link, nil
	}
	return s.syncClaimedTicketLink(ctx, link)
}

// RetryUncertainTicketCreation is an explicit administrator override after
// the remote system has been checked and confirmed not to contain the ticket.
func (s *ZammadService) RetryUncertainTicketCreation(ctx context.Context, linkID string, actorID int) (*models.ZammadTicketLink, error) {
	link, err := s.repo.GetTicketLink(linkID)
	if err != nil {
		return nil, err
	}
	reset, err := s.repo.ResetUncertainTicketCreation(linkID)
	if err != nil {
		return nil, err
	}
	if !reset {
		return nil, zammadValidationError("ticket creation is not awaiting an administrator decision")
	}
	return s.CreateTicket(ctx, link.ItemID, actorID, models.CreateZammadTicketRequest{
		ConnectionID: link.ProviderID,
		GroupID:      link.GroupID,
	})
}

func (s *ZammadService) syncClaimedTicketLink(ctx context.Context, link *models.ZammadTicketLink) (*models.ZammadTicketLink, error) {
	item, itemErr := repository.NewItemRepository(s.db).FindByID(link.ItemID)
	if itemErr != nil {
		s.recordZammadSyncError(link, itemErr)
		return nil, itemErr
	}
	connection, client, err := s.client(ctx, link.ProviderID, item.WorkspaceID)
	if err != nil {
		s.recordZammadSyncError(link, err)
		return nil, err
	}
	if !connection.Enabled {
		_ = s.repo.UpdateTicketLinkSync(link.ID, link.LastStatusID, link.LastStatusName,
			link.GroupID, link.GroupName, link.OwnerID, link.OwnerName, "", time.Now(), false, false)
		return s.repo.GetTicketLink(link.ID)
	}
	ticket, err := client.GetTicket(ctx, link.TicketID)
	if err != nil {
		s.recordZammadSyncError(link, err)
		return nil, err
	}
	allowedGroup, err := s.requireAllowedTicketGroup(ctx, connection, client, ticket, nil)
	if err != nil {
		s.recordZammadSyncError(link, err)
		return nil, err
	}
	if ticket.GroupName == "" {
		ticket.GroupName = allowedGroup.Name
	}
	var metadata *models.ZammadConnectionMetadata
	needsStateName := ticket.StateName == "" && (ticket.StateID != link.LastStatusID || link.LastStatusName == "")
	needsGroupName := ticket.GroupName == "" && (ticket.GroupID != link.GroupID || link.GroupName == "")
	if needsStateName || needsGroupName {
		metadata, err = client.Metadata(ctx)
		if err != nil {
			s.recordZammadSyncError(link, err)
			return nil, err
		}
	}
	if err := s.persistTicketSnapshot(ctx, link, connection, client, ticket, metadata); err != nil {
		return nil, err
	}
	return s.repo.GetTicketLink(link.ID)
}

func (s *ZammadService) recordZammadSyncError(link *models.ZammadTicketLink, err error) {
	_ = s.repo.UpdateTicketLinkSync(link.ID, link.LastStatusID, link.LastStatusName,
		link.GroupID, link.GroupName, link.OwnerID, link.OwnerName,
		RedactString(err.Error()), time.Now(), false, false)
}

func (s *ZammadService) persistTicketSnapshot(ctx context.Context, link *models.ZammadTicketLink, connection *models.ZammadConnection, client *zammad.Client, ticket *zammad.Ticket, metadata *models.ZammadConnectionMetadata) error {
	statusName := zammadStateName(metadata, ticket.StateID, ticket.StateName)
	if statusName == "" && ticket.StateID == link.LastStatusID {
		statusName = link.LastStatusName
	}
	groupName := zammadGroupName(metadata, ticket.GroupID, ticket.GroupName)
	if groupName == "" && ticket.GroupID == link.GroupID {
		groupName = link.GroupName
	}
	ownerName := resolveZammadOwnerName(ctx, client, ticket)
	isClosed := slices.Contains(connection.ClosedStateIDs, ticket.StateID)
	completionApplied := false
	if connection.CompletionStatusID != nil && isClosed && !link.CompletionApplied {
		if err := s.completeWindshiftItem(ctx, link, connection); err != nil {
			safeError := RedactString(err.Error())
			_ = s.repo.UpdateTicketLinkSync(link.ID, ticket.StateID, statusName,
				ticket.GroupID, groupName, ticket.OwnerID, ownerName,
				safeError, time.Now(), false, false)
			return err
		}
		completionApplied = true
	}
	setCompletionApplied := !isClosed || completionApplied
	return s.repo.UpdateTicketLinkSync(link.ID, ticket.StateID, statusName,
		ticket.GroupID, groupName, ticket.OwnerID, ownerName,
		"", time.Now(), setCompletionApplied, completionApplied)
}

func resolveZammadOwnerName(ctx context.Context, client *zammad.Client, ticket *zammad.Ticket) string {
	if ticket == nil || ticket.OwnerID <= 0 {
		return ""
	}
	if ticket.OwnerName != "" {
		return ticket.OwnerName
	}
	if ticket.OwnerID == 1 {
		return "Unassigned"
	}
	owners, err := client.Owners(ctx, ticket.GroupID)
	if err != nil {
		return ""
	}
	for _, owner := range owners {
		if owner.ID == ticket.OwnerID {
			return owner.Name
		}
	}
	return ""
}

func allowedZammadGroups(connection *models.ZammadConnection, groups []models.ZammadGroup) []models.ZammadGroup {
	allowed := make([]models.ZammadGroup, 0, len(groups))
	for _, group := range groups {
		if slices.Contains(connection.AllowedGroupIDs, group.ID) ||
			(len(connection.AllowedGroupIDs) == 0 && (group.ID == connection.DefaultGroupID || group.Name == connection.DefaultGroupName)) {
			allowed = append(allowed, group)
		}
	}
	return allowed
}

func allowedZammadGroup(connection *models.ZammadConnection, groups []models.ZammadGroup, groupID int) (models.ZammadGroup, error) {
	for _, group := range allowedZammadGroups(connection, groups) {
		if group.ID == groupID {
			return group, nil
		}
	}
	return models.ZammadGroup{}, zammadValidationError("selected Zammad group is missing, inactive, or not allowed for this connection")
}

func (s *ZammadService) requireAllowedTicketGroup(ctx context.Context, connection *models.ZammadConnection, client *zammad.Client, ticket *zammad.Ticket, knownGroups []models.ZammadGroup) (models.ZammadGroup, error) {
	if ticket == nil || ticket.GroupID <= 0 {
		return models.ZammadGroup{}, zammadValidationError("Zammad ticket group is missing or invalid")
	}
	if len(knownGroups) > 0 {
		return allowedZammadGroup(connection, knownGroups, ticket.GroupID)
	}
	if len(connection.AllowedGroupIDs) > 0 {
		if slices.Contains(connection.AllowedGroupIDs, ticket.GroupID) {
			return models.ZammadGroup{ID: ticket.GroupID, Name: ticket.GroupName, Active: true}, nil
		}
		return models.ZammadGroup{}, zammadValidationError("Zammad ticket group is not allowed for this connection")
	}
	if connection.DefaultGroupID > 0 {
		if connection.DefaultGroupID == ticket.GroupID {
			return models.ZammadGroup{ID: ticket.GroupID, Name: ticket.GroupName, Active: true}, nil
		}
		return models.ZammadGroup{}, zammadValidationError("Zammad ticket group is not allowed for this connection")
	}
	if ticket.GroupName != "" {
		if ticket.GroupName == connection.DefaultGroupName {
			return models.ZammadGroup{ID: ticket.GroupID, Name: ticket.GroupName, Active: true}, nil
		}
		return models.ZammadGroup{}, zammadValidationError("Zammad ticket group is not allowed for this connection")
	}
	groups, err := client.Groups(ctx)
	if err != nil {
		return models.ZammadGroup{}, err
	}
	return allowedZammadGroup(connection, groups, ticket.GroupID)
}

func zammadCreationOutcomeUncertain(err error) bool {
	var upstreamErr *zammad.UpstreamError
	if errors.As(err, &upstreamErr) {
		return true
	}
	var apiErr *zammad.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusRequestTimeout || apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= http.StatusInternalServerError
}

func zammadStateExists(metadata *models.ZammadConnectionMetadata, stateID int) bool {
	return metadata != nil && slices.ContainsFunc(metadata.States, func(state models.ZammadState) bool {
		return state.ID == stateID && state.Active
	})
}

func zammadStateName(metadata *models.ZammadConnectionMetadata, stateID int, fallback string) string {
	if fallback != "" {
		return fallback
	}
	if metadata != nil {
		for _, state := range metadata.States {
			if state.ID == stateID {
				return state.Name
			}
		}
	}
	return ""
}

func zammadGroupName(metadata *models.ZammadConnectionMetadata, groupID int, fallback string) string {
	if fallback != "" {
		return fallback
	}
	if metadata != nil {
		for _, group := range metadata.Groups {
			if group.ID == groupID {
				return group.Name
			}
		}
	}
	return ""
}

func zammadTicketAttributeString(ticket *zammad.Ticket, field string) (string, error) {
	if ticket == nil || ticket.Attributes == nil {
		return "", nil
	}
	raw, ok := ticket.Attributes[field]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", &zammad.UpstreamError{Cause: errors.New("zammad correlation field is not a string")}
	}
	return strings.TrimSpace(value), nil
}

func (s *ZammadService) SyncDue(ctx context.Context, limit int) error {
	if limit <= 0 {
		limit = 50
	}
	links, err := s.repo.ListDueTicketLinks(time.Now().Add(-2*time.Minute), limit)
	if err != nil {
		return err
	}
	var firstError error
	for _, link := range links {
		claimed, claimErr := s.repo.ClaimSync(link.ID, time.Now().Add(2*time.Minute))
		if claimErr != nil || !claimed {
			if claimErr != nil && firstError == nil {
				firstError = claimErr
			}
			continue
		}
		if _, syncErr := s.syncClaimedTicketLink(ctx, link); syncErr != nil && firstError == nil {
			firstError = syncErr
		}
	}
	return firstError
}

func (s *ZammadService) completeWindshiftItem(ctx context.Context, link *models.ZammadTicketLink, connection *models.ZammadConnection) error {
	if connection.CompletionStatusID == nil {
		return nil
	}
	if connection.CreatedBy == nil {
		return errors.New("configured Zammad actor no longer exists")
	}
	itemRepo := repository.NewItemRepository(s.db)
	item, err := itemRepo.FindByIDWithDetails(link.ItemID)
	if err != nil {
		return err
	}
	allowed, err := s.permission.HasWorkspacePermission(*connection.CreatedBy, item.WorkspaceID, models.PermissionItemEdit)
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("configured Zammad actor no longer has item edit permission")
	}
	result, err := s.workflow.PerformTransition(ctx, PerformTransitionRequest{
		ItemID: link.ItemID, ToStatusID: *connection.CompletionStatusID,
		ActorUserID: *connection.CreatedBy,
		EventMetadata: func() itemevents.Metadata {
			metadata := itemevents.Integration(connection.ProviderID, "zammad_sync")
			metadata.SourceRef = fmt.Sprintf("ticket:%d/link:%s", link.TicketID, link.ID)
			metadata.CorrelationID = link.CorrelationKey
			return metadata
		}(),
	}, itemRepo, s.condition, s.approval)
	if err != nil {
		return err
	}
	if !result.NoOp && s.events != nil {
		s.events.EmitStatusChanged(result.Item, result.OldStatusID, result.NewStatusID, *connection.CreatedBy, "Zammad")
	}
	return nil
}

func (s *ZammadService) client(ctx context.Context, id string, workspaceID int) (*models.ZammadConnection, *zammad.Client, error) {
	connection, err := s.repo.GetConnection(id)
	if err != nil {
		return nil, nil, err
	}
	available, err := s.repo.IsConnectionAvailableToWorkspace(id, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	if !available {
		return nil, nil, ErrCredentialScopeMismatch
	}
	client, err := s.clientForConnection(ctx, connection, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	return connection, client, nil
}

func (s *ZammadService) clientForConnection(ctx context.Context, connection *models.ZammadConnection, workspaceID int) (*zammad.Client, error) {
	transport := s.transportOverride
	if transport == nil {
		transport = newZammadSafeTransport(connection.BaseURL, "/api/v1/")
	}
	if connection.AuthMethod == models.ZammadAuthMethodOAuth {
		token, err := s.oauthAccessToken(ctx, connection, workspaceID)
		if err != nil {
			return nil, err
		}
		return zammad.NewOAuthClient(connection.BaseURL, token, transport), nil
	}
	if connection.CredentialID <= 0 {
		return nil, zammadValidationError("API token is not configured")
	}
	token, _, err := s.credentials.ResolveManaged(ctx, connection.CredentialID, workspaceID, string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil {
		return nil, err
	}
	return zammad.NewClient(connection.BaseURL, token, transport), nil
}

func (s *ZammadService) resolveStateName(ctx context.Context, client *zammad.Client, stateID int) string {
	metadata, err := client.Metadata(ctx)
	if err != nil {
		return ""
	}
	for _, state := range metadata.States {
		if state.ID == stateID {
			return state.Name
		}
	}
	return ""
}

func validateNewZammadConnection(req models.CreateZammadConnectionRequest, actorID int) (*models.ZammadConnection, error) {
	if hasNonPositiveIDs(req.ClosedStateIDs) {
		return nil, zammadValidationError("closed_state_ids must contain positive IDs")
	}
	if hasNonPositiveIDs(req.AllowedGroupIDs) {
		return nil, zammadValidationError("allowed_group_ids must contain positive IDs")
	}
	if hasNonPositiveIDs(req.WorkspaceIDs) {
		return nil, zammadValidationError("workspace_ids must contain positive IDs")
	}
	baseURL, err := NormalizeZammadBaseURL(req.BaseURL)
	if err != nil {
		return nil, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	appliesAll := false
	if req.AppliesToAllWorkspaces != nil {
		appliesAll = *req.AppliesToAllWorkspaces
	}
	correlationField := strings.TrimSpace(req.CorrelationField)
	if correlationField == "" {
		correlationField = "windshift_item_key"
	}
	connection := &models.ZammadConnection{
		ProviderID: uuid.New().String(), Slug: strings.TrimSpace(req.Slug),
		Name: strings.TrimSpace(req.Name), Enabled: enabled, BaseURL: baseURL,
		DefaultGroupID: req.DefaultGroupID, DefaultGroupName: strings.TrimSpace(req.DefaultGroupName),
		AllowedGroupIDs: normalizePositiveIDs(req.AllowedGroupIDs),
		DefaultCustomer: strings.TrimSpace(req.DefaultCustomer), CorrelationField: correlationField,
		ClosedStateIDs: normalizePositiveIDs(req.ClosedStateIDs), CompletionStatusID: req.CompletionStatusID,
		AppliesToAllWorkspaces: appliesAll, WorkspaceIDs: normalizePositiveIDs(req.WorkspaceIDs),
		CreatedBy: &actorID,
	}
	connection.AuthMethod = req.AuthMethod
	if connection.AuthMethod == "" {
		connection.AuthMethod = models.ZammadAuthMethodAPIToken
	}
	switch connection.AuthMethod {
	case models.ZammadAuthMethodOAuth:
		connection.OAuthClientID = strings.TrimSpace(req.OAuthClientID)
		if connection.OAuthClientID == "" || strings.TrimSpace(req.OAuthClientSecret) == "" {
			return nil, zammadValidationError("oauth_client_id and oauth_client_secret are required for OAuth")
		}
	case models.ZammadAuthMethodAPIToken:
		if strings.TrimSpace(req.APIToken) == "" {
			return nil, zammadValidationError("Zammad API token is required")
		}
	default:
		return nil, zammadValidationError("auth_method must be api_token or oauth")
	}
	if err := validateZammadConnection(connection); err != nil {
		return nil, err
	}
	return connection, nil
}

func validateZammadConnection(connection *models.ZammadConnection) error {
	if connection.Name == "" || connection.Slug == "" {
		return zammadValidationError("name and slug are required")
	}
	if !zammadSlugPattern.MatchString(connection.Slug) {
		return zammadValidationError("slug must start with a letter and contain only lowercase letters, numbers, hyphens, and underscores")
	}
	if connection.DefaultCustomer == "" {
		return zammadValidationError("default_customer is required")
	}
	if connection.DefaultGroupID < 0 {
		return zammadValidationError("default_group_id must be positive")
	}
	if connection.CompletionStatusID != nil && *connection.CompletionStatusID <= 0 {
		return zammadValidationError("completion_status_id must be positive")
	}
	if !zammadFieldNamePattern.MatchString(connection.CorrelationField) {
		return zammadValidationError("correlation_field is not a valid Zammad object field name")
	}
	if !connection.AppliesToAllWorkspaces && len(connection.WorkspaceIDs) == 0 {
		return zammadValidationError("at least one workspace is required")
	}
	return nil
}

func validateZammadOAuthConfiguration(connection *models.ZammadConnection) error {
	if connection.AuthMethod != models.ZammadAuthMethodOAuth {
		return nil
	}
	if strings.TrimSpace(connection.OAuthClientID) == "" || strings.TrimSpace(connection.OAuthClientSecretEncrypted) == "" {
		return zammadValidationError("OAuth client credentials are not configured")
	}
	return nil
}

func NormalizeZammadBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return "", zammadValidationError("invalid Zammad base URL")
	}
	if parsed.Scheme != "https" {
		return "", zammadValidationError("Zammad base URL must use HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", zammadValidationError("Zammad base URL must not contain credentials, a query, or a fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func zammadOAuthRedirectURI(publicBaseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(publicBaseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", zammadValidationError("Zammad OAuth requires an absolute HTTPS public base URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/integrations/zammad/oauth/callback"
	parsed.RawPath = ""
	return parsed.String(), nil
}

func normalizePositiveIDs(ids []int) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func hasNonPositiveIDs(ids []int) bool {
	for _, id := range ids {
		if id <= 0 {
			return true
		}
	}
	return false
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func boolPointer(value bool) *bool { return &value }
