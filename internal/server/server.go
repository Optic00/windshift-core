// Package server provides a reusable HTTP server for windshift.
// This allows the server to be started both from the main binary
// and in-process for integration tests.
package server

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"windshift/internal/auth"
	"windshift/internal/config"
	"windshift/internal/database"
	"windshift/internal/email"
	"windshift/internal/emailutil"
	"windshift/internal/handlers"
	"windshift/internal/ldap"
	"windshift/internal/llm"
	"windshift/internal/logger"
	mcpserver "windshift/internal/mcp"
	"windshift/internal/middleware"
	"windshift/internal/models"
	"windshift/internal/plugins"
	"windshift/internal/portalwebauthn"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	v1 "windshift/internal/restapi/v1"
	"windshift/internal/router"
	"windshift/internal/routes"
	"windshift/internal/scheduler"
	"windshift/internal/scm"
	"windshift/internal/services"
	"windshift/internal/smtp"
	"windshift/internal/utils"
	"windshift/internal/webauthn"
	"windshift/internal/webhook"
)

// Config is an alias to config.Config — the canonical, fully-resolved runtime
// configuration. All resolution of env vars and CLI flags happens in
// internal/config/Load; this package only consumes the result.
type Config = config.Config

// Server represents a windshift HTTP server instance.
type Server struct {
	config     Config
	httpServer *http.Server
	db         database.Database
	listener   net.Listener

	// Services that need cleanup
	ldapHandler               *handlers.LDAPHandler
	notificationManager       *handlers.NotificationManager
	notificationService       *services.NotificationService
	notificationScheduler     *scheduler.NotificationScheduler
	recurrenceScheduler       *scheduler.RecurrenceScheduler
	cfvCleanupScheduler       *scheduler.CFVCleanupScheduler
	todoistSyncScheduler      *scheduler.TodoistSyncScheduler
	runnerLeaseReaper         *scheduler.RunnerLeaseReaper
	codingRunService          *services.RunService
	workflowService           *services.WorkflowService
	actionService             *services.ActionService
	assetActionService        *services.AssetActionService
	approvalEscalationSweeper *services.ApprovalEscalationSweeper
	emailScheduler            *scheduler.EmailScheduler
	emailTrackingRetention    *scheduler.EmailTrackingRetentionSweeper
	briefingScheduler         *scheduler.BriefingScheduler
	pluginScheduleScheduler   *scheduler.PluginScheduleScheduler
	activityTracker           *services.ActivityTracker
	tokenTracker              *services.TokenTracker
	scmSyncStopChan           chan struct{}
	issueSyncStopChan         chan struct{}
	magicLinkStopChan         chan struct{}
	cleanupStopChan           chan struct{}
	jiraHostStopChan          chan struct{}
	cleanupTicker             *time.Ticker
	pluginManager             *plugins.Manager

	// Rate limiters that need cleanup
	loginRateLimiter    *middleware.RateLimiter
	fidoRateLimiter     *middleware.RateLimiter
	authRateLimiter     *middleware.RateLimiter
	scimRateLimiter     *middleware.RateLimiter
	portalSubmitLimiter *middleware.RateLimiter
	portalSearchLimiter *middleware.RateLimiter
	emailVerifyLimiter  *middleware.RateLimiter
	setupLimiter        *middleware.RateLimiter
	ssoRateLimiter      *middleware.RateLimiter
	portalAuthLimiter   *middleware.RateLimiter
	oauthTokenLimiter   *middleware.RateLimiter
	aiRateLimiter       *middleware.RateLimiter
	uploadLimiter       *middleware.RateLimiter
	webhookLimiter      *middleware.RateLimiter
	searchLimiter       *middleware.RateLimiter
	calendarFeedLimiter *middleware.RateLimiter
	userConcurrency     *middleware.UserConcurrencyLimiter

	// Server state
	actualPort   int
	started      bool
	shuttingDown bool
}

// New creates a new Server instance with the given configuration.
// It initializes all services and handlers but does not start listening.
func New(cfg Config) (*Server, error) {
	s := &Server{
		config:            cfg,
		scmSyncStopChan:   make(chan struct{}),
		issueSyncStopChan: make(chan struct{}),
		magicLinkStopChan: make(chan struct{}),
		cleanupStopChan:   make(chan struct{}),
		jiraHostStopChan:  make(chan struct{}),
	}

	if err := s.initialize(); err != nil {
		s.cleanup()
		return nil, err
	}

	return s, nil
}

// initialize sets up all services and handlers.
func (s *Server) initialize() error {
	// FIXME(human-review): This 1k+ line method wires database, services, routes,
	// schedulers, plugins, and shutdown state. Split into focused builders plus a
	// scheduler/lifecycle registry so start/stop wiring cannot drift silently.
	cfg := s.config

	// Suppress all logging in silent mode (for testing)
	if cfg.SilentMode {
		logger.SetSilent(true)
	}

	// Determine which database to use
	var err error
	if cfg.DB.PostgresConn != "" {
		slog.Info("connecting to PostgreSQL database")
		s.db, err = database.NewDatabase("postgres", cfg.DB.PostgresConn, cfg.DB.MaxReadConns, cfg.DB.MaxWriteConns)
		if err != nil {
			return fmt.Errorf("failed to connect to PostgreSQL database: %w", err)
		}
		slog.Info("PostgreSQL database initialized", "max_read_conns", cfg.DB.MaxReadConns, "max_write_conns", cfg.DB.MaxWriteConns)
	} else {
		slog.Info("connecting to SQLite database", "path", cfg.DB.SQLitePath)
		s.db, err = database.NewDatabase("sqlite3", cfg.DB.SQLitePath, cfg.DB.MaxReadConns, cfg.DB.MaxWriteConns)
		if err != nil {
			return fmt.Errorf("failed to connect to SQLite database: %w", err)
		}
		slog.Info("SQLite database initialized", "max_read_conns", cfg.DB.MaxReadConns, "max_write_conns", cfg.DB.MaxWriteConns, "mode", "WAL")
	}

	if err = s.db.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}

	// Seed built-in email templates (idempotent). Lives outside Initialize
	// so the database layer doesn't depend on the email domain.
	if err = emailutil.SeedTemplates(s.db); err != nil {
		slog.Warn("failed to seed default email templates", "error", err)
	}

	// Ensure default notification settings exist
	if err = repository.NewNotificationSettingsRepository(s.db).EnsureDefault(); err != nil {
		slog.Warn("failed to ensure notification settings", "error", err)
	}

	// Migrate legacy select field options to ID-based format
	if err = database.MigrateSelectFieldOptions(s.db); err != nil {
		slog.Warn("failed to migrate select field options", "error", err)
	}

	if cfg.RecoverUser != "" {
		s.recoverUser(cfg.RecoverUser)
	}

	// Determine setup status
	setupCompleted, err := checkSetupStatusWithRetry(s.db, 5, time.Second)
	if err != nil {
		return fmt.Errorf("failed to determine setup status: %w", err)
	}

	// Initialize permission service
	permService, err := services.NewPermissionService(s.db, services.PermissionCacheConfig{
		TTL:          15 * time.Minute,
		MaxCacheSize: 512,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize permission service: %w", err)
	}

	// Shared channel service used by ChannelHandler, WebhookHandler,
	// FormHandler, RequestTypeHandler, and AssetReportHandler for the
	// "user manages channel C" gate.
	channelService := services.NewChannelService(s.db, permService)

	// Initialize activity tracker
	s.activityTracker, err = services.NewActivityTracker(s.db, services.DefaultActivityTrackerConfig())
	if err != nil {
		return fmt.Errorf("failed to initialize activity tracker: %w", err)
	}

	// Start activity cleanup scheduler
	s.cleanupTicker = time.NewTicker(24 * time.Hour)
	go s.runActivityCleanup()

	// Determine HTTPS mode
	enableHTTPS := cfg.TLSCertPath != "" && cfg.TLSKeyPath != ""

	// Parse additional proxies
	var additionalProxyList []string
	if cfg.AdditionalProxies != "" {
		additionalProxyList = strings.Split(cfg.AdditionalProxies, ",")
	}

	// Create IP extractor
	ipExtractor := utils.NewIPExtractor(cfg.UseProxy, additionalProxyList)

	// Authentication management
	sessionManager := auth.NewSessionManager(s.db, enableHTTPS, cfg.UseProxy, additionalProxyList, cfg.Auth.SessionSecret)

	// Determine effective port for CORS
	effectivePort := cfg.Port
	if cfg.AllowedPort != "" {
		effectivePort = cfg.AllowedPort
	}

	// Initialize WebAuthn — RPID/RPName are pre-resolved by config.Load;
	// webauthn only overrides RPID when in development mode.
	isDevelopment := cfg.DisableCSRF
	rpID := cfg.WebAuthn.RPID
	if isDevelopment {
		rpID = ""
	}
	webAuthnConfig, err := webauthn.NewConfig(rpID, cfg.WebAuthn.RPName, nil, isDevelopment, cfg.AllowedHosts, effectivePort, enableHTTPS, cfg.UseProxy)
	if err != nil {
		return fmt.Errorf("failed to initialize WebAuthn configuration: %w", err)
	}
	slog.Info("WebAuthn configuration initialized",
		"rp_id", webAuthnConfig.RPID,
		"rp_name", webAuthnConfig.RPName,
		"development_mode", isDevelopment)

	// Portal passkeys reuse the relying-party settings but require resident keys
	// so customers can sign in passwordlessly (BeginDiscoverableLogin).
	portalWebAuthnConfig, err := portalwebauthn.NewConfig(webAuthnConfig)
	if err != nil {
		return fmt.Errorf("failed to initialize portal WebAuthn configuration: %w", err)
	}

	// Build options for user-keyed rate limiters (authenticated endpoints)
	var userKeyedOpts []middleware.RateLimiterOption
	userKeyedOpts = append(userKeyedOpts, middleware.WithUserKeyed())
	if cfg.DisableIPRateLimit {
		userKeyedOpts = append(userKeyedOpts, middleware.WithDisableIPLimit())
	}

	// Create rate limiters
	// IP-only limiters (pre-auth / unauthenticated endpoints)
	s.loginRateLimiter = middleware.NewRateLimiter(5.0/60.0, 10, cfg.UseProxy, additionalProxyList)
	s.fidoRateLimiter = middleware.NewRateLimiter(10.0/60.0, 15, cfg.UseProxy, additionalProxyList)
	s.scimRateLimiter = middleware.NewRateLimiter(10.0, 100, cfg.UseProxy, additionalProxyList)
	s.portalSubmitLimiter = middleware.NewRateLimiter(5.0/60.0, 10, cfg.UseProxy, additionalProxyList)
	s.portalSearchLimiter = middleware.NewRateLimiter(10.0/60.0, 15, cfg.UseProxy, additionalProxyList)
	s.emailVerifyLimiter = middleware.NewRateLimiter(10.0/60.0, 15, cfg.UseProxy, additionalProxyList)
	s.setupLimiter = middleware.NewRateLimiter(20.0/60.0, 30, cfg.UseProxy, additionalProxyList)
	s.ssoRateLimiter = middleware.NewRateLimiter(10.0/60.0, 5, cfg.UseProxy, additionalProxyList)
	s.portalAuthLimiter = middleware.NewRateLimiter(3.0/60.0, 3, cfg.UseProxy, additionalProxyList)
	s.calendarFeedLimiter = middleware.NewRateLimiter(10.0/60.0, 15, cfg.UseProxy, additionalProxyList)
	// OAuth /token is unauthenticated (server-to-server), so it must stay
	// IP-keyed and must NOT honor DisableIPRateLimit — otherwise enabling that
	// flag for NAT deployments would silently remove all brute-force protection
	// on client_secret/code guessing. Kept separate from the user-keyed
	// authRateLimiter for exactly this reason.
	s.oauthTokenLimiter = middleware.NewRateLimiter(20.0/60.0, 30, cfg.UseProxy, additionalProxyList)
	// User-keyed limiters (authenticated endpoints — key by user ID, optionally skip IP)
	s.authRateLimiter = middleware.NewRateLimiter(20.0/60.0, 30, cfg.UseProxy, additionalProxyList, userKeyedOpts...)
	s.aiRateLimiter = middleware.NewRateLimiter(20.0/60.0, 30, cfg.UseProxy, additionalProxyList, userKeyedOpts...)
	s.uploadLimiter = middleware.NewRateLimiter(10.0/60.0, 15, cfg.UseProxy, additionalProxyList, userKeyedOpts...)
	s.webhookLimiter = middleware.NewRateLimiter(10.0/60.0, 15, cfg.UseProxy, additionalProxyList, userKeyedOpts...)
	s.searchLimiter = middleware.NewRateLimiter(20.0/60.0, 30, cfg.UseProxy, additionalProxyList, userKeyedOpts...)
	// Per-user in-flight concurrency cap for the whole /api surface — bounds how
	// many shared DB-pool connections one user can hold so a runaway client
	// can't starve the others. Applied to the api group below.
	s.userConcurrency = middleware.NewUserConcurrencyLimiter(cfg.MaxUserConcurrency)

	// Initialize token tracker
	s.tokenTracker = services.NewTokenTracker(s.db, services.DefaultTokenTrackerConfig())

	// Create token manager
	tokenManager := auth.NewTokenManager(s.db, s.tokenTracker)
	if cleaned, cleanupErr := tokenManager.CleanupExpiredTokens(); cleanupErr != nil {
		slog.Warn("failed to cleanup expired api tokens on startup", "error", cleanupErr)
	} else if cleaned > 0 {
		slog.Info("cleaned expired api tokens on startup", "count", cleaned)
	}

	// Create auth middleware
	authMiddleware := middleware.NewAuthMiddleware(sessionManager, tokenManager, s.db, cfg.UseProxy, additionalProxyList, setupCompleted)

	// Parse additional proxy IPs
	var additionalProxyIPs []net.IP
	for _, proxyStr := range additionalProxyList {
		if ip := net.ParseIP(strings.TrimSpace(proxyStr)); ip != nil {
			additionalProxyIPs = append(additionalProxyIPs, ip)
		}
	}

	mux := http.NewServeMux()

	// Initialize notification manager
	nmCfg := handlers.DefaultNotificationManagerConfig()
	if cfg.Notification.FlushInterval > 0 {
		nmCfg.FlushInterval = cfg.Notification.FlushInterval
	}
	if cfg.Notification.BatchSize > 0 {
		nmCfg.MaxBatchSize = cfg.Notification.BatchSize
	}
	if cfg.Notification.SyncInterval > 0 {
		nmCfg.SyncInterval = cfg.Notification.SyncInterval
	}
	s.notificationManager, err = handlers.NewNotificationManager(s.db, nmCfg)
	if err != nil {
		return fmt.Errorf("failed to create notification manager: %w", err)
	}

	// Initialize notification service
	s.notificationService = services.NewNotificationService(
		s.db,
		s.notificationManager,
		permService,
		services.DefaultNotificationServiceConfig(),
	)

	// Initialize SMTP and schedulers
	smtpSender := smtp.NewNotificationSMTPSender(s.db)
	s.notificationScheduler = scheduler.NewNotificationScheduler(s.db, smtpSender, cfg.Notification.BatchInterval, s.notificationService)
	s.notificationScheduler.Start()
	slog.Info("notification scheduler started")

	// WorkflowService is constructed here (moved up from later in bootstrap) so the
	// recurrence scheduler can resolve a workspace+item-type's initial status the
	// same way the rest of the system does. The handler-side instance below reuses
	// the same pointer, so the in-memory cache is shared.
	s.workflowService = services.NewWorkflowService(s.db)
	s.recurrenceScheduler = scheduler.NewRecurrenceScheduler(s.db, s.workflowService)
	s.recurrenceScheduler.Start()

	// Drains pending_custom_field_cleanups: when a custom field is
	// deleted, items' cfv JSON still carries the deleted key. This
	// scheduler scrubs them in batches so the Delete request returns
	// immediately even when the workspace has millions of items.
	s.cfvCleanupScheduler = scheduler.NewCFVCleanupScheduler(s.db)
	s.cfvCleanupScheduler.Start()
	// Liveness backstop for remote agent runs (WI-141): fail runs whose
	// runner's heartbeat went stale and revoke the dead runner instances.
	s.runnerLeaseReaper = scheduler.NewRunnerLeaseReaper(
		repository.NewAgentRunRepository(s.db),
		repository.NewRunnerRepository(s.db),
	)
	s.runnerLeaseReaper.Start()
	slog.Info("recurrence scheduler started")

	// Initialize shared execution chain store for cross-application loop prevention
	chainStore := services.NewExecutionChainStore()

	// Initialize action service
	s.actionService = services.NewActionService(s.db, services.DefaultActionServiceConfig(), chainStore)
	s.actionService.SetNotificationService(s.notificationService)
	s.actionService.SetPermissionService(permService)
	slog.Info("action service initialized")

	// Initialize asset action service (shared chain store for cross-application loop prevention)
	s.assetActionService = services.NewAssetActionService(s.db, services.DefaultActionServiceConfig(), chainStore)
	s.assetActionService.SetNotificationService(s.notificationService)
	slog.Info("asset action service initialized")

	// Determine base URL — cfg.BaseURL is already resolved by config.Load
	// from the --base-url flag or BASE_URL env; only the localhost fallback
	// remains here because it needs cfg.Port.
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://localhost:%s%s", cfg.Port, cfg.ContextPath)
	}

	// Initialize email verification service
	emailVerificationService := services.NewEmailVerificationService(s.db, smtpSender, baseURL)

	// Initialize portal session manager
	portalSessionManager := auth.NewPortalSessionManager(s.db, enableHTTPS, cfg.UseProxy, additionalProxyList, cfg.Auth.SessionSecret)

	// Initialize magic link service
	magicLinkService := services.NewMagicLinkService(s.db, smtpSender, baseURL)

	// Initialize invitation service
	invitationService := services.NewInvitationService(s.db, smtpSender, baseURL)

	// Initialize workspace key cache (resolves workspace keys to IDs without DB lookups)
	workspaceKeyCache := handlers.NewWorkspaceKeyCache(repository.NewWorkspaceRepository(s.db))

	// Initialize handlers
	itemHandler := handlers.NewItemHandler(s.db, permService, s.activityTracker, s.notificationService)
	customFieldHandler := handlers.NewCustomFieldHandler(s.db)
	workspaceHandler := handlers.NewWorkspaceHandler(s.db, permService, s.activityTracker, workspaceKeyCache)
	screenHandler := handlers.NewScreenHandler(s.db)
	configSetHandler := handlers.NewConfigurationSetHandler(s.db, s.notificationService, permService)
	itemTypeHandler := handlers.NewItemTypeHandler(s.db)
	priorityHandler := handlers.NewPriorityHandler(s.db)

	// Shared audit emitter for enum services
	enumAuditEmit := services.AuditEmitFunc(func(db database.Database, r *http.Request, actionType, resourceType string, entityID int, entityName string) {
		currentUser := utils.GetCurrentUser(r)
		if currentUser == nil {
			return
		}
		_ = logger.LogAudit(db, logger.AuditEvent{
			UserID:       currentUser.ID,
			Username:     currentUser.Username,
			IPAddress:    utils.GetClientIP(r),
			UserAgent:    r.UserAgent(),
			ActionType:   actionType,
			ResourceType: resourceType,
			ResourceID:   &entityID,
			ResourceName: entityName,
			Success:      true,
		})
	})

	// Generic enum handlers
	hierarchyLevelConfig := services.NewHierarchyLevelConfig()
	hierarchyLevelConfig.AuditEmit = enumAuditEmit
	hierarchyLevelHandler := handlers.NewEnumHandler(
		services.NewEnumService(s.db, hierarchyLevelConfig),
		func() interface{} { return &models.HierarchyLevel{} })
	requestTypeHandler := handlers.NewRequestTypeHandler(
		repository.NewRequestTypeRepository(s.db),
		repository.NewChannelRepository(s.db),
		repository.NewScreenRepository(s.db),
		repository.NewItemTypeRepository(s.db),
		logger.NewAuditor(s.db),
		channelService,
	)
	statusCategoryConfig := services.NewStatusCategoryConfig()
	statusCategoryConfig.AuditEmit = enumAuditEmit
	statusCategoryHandler := handlers.NewEnumHandler(
		services.NewEnumService(s.db, statusCategoryConfig),
		func() interface{} { return &models.StatusCategory{} })
	statusHandler := handlers.NewEnumHandler(
		services.NewEnumService(s.db, services.NewStatusConfig()),
		func() interface{} { return &models.Status{} })
	statusHandlerLegacy := handlers.NewStatusHandler(repository.NewStatusRepository(s.db), repository.NewItemRepository(s.db), logger.NewAuditor(s.db))
	workflowService := s.workflowService
	workflowHandler := handlers.NewWorkflowHandler(repository.NewWorkflowRepository(s.db), logger.NewAuditor(s.db))
	workflowHandler.SetWorkflowService(workflowService)
	userHandler := handlers.NewUserHandler(
		repository.NewUserRepository(s.db),
		logger.NewAuditor(s.db),
		permService,
		invitationService,
		services.NewUserReadService(s.db),
		func(id int) error {
			tokenIDs, err := services.OffboardUser(s.db, id, s.notificationService)
			if err != nil {
				return err
			}
			tokenManager.InvalidateTokens(tokenIDs)
			return nil
		},
		func(id int) (services.AgentDeactivationResult, error) {
			result, err := services.DeactivateOwnedAgentsAndTokens(s.db, id)
			if err == nil {
				tokenManager.InvalidateTokens(result.RevokedAPITokens)
			}
			return result, err
		},
	)
	groupHandler := handlers.NewGroupHandler(repository.NewGroupRepository(s.db), permService, logger.NewAuditor(s.db))
	credentialHandler := handlers.NewCredentialHandler(repository.NewCredentialRepository(s.db), logger.NewAuditor(s.db), permService, cfg.SSH.Enabled)
	webAuthnHandler := handlers.NewWebAuthnHandler(s.db, permService, sessionManager, webAuthnConfig, ipExtractor)
	collectionHandler := handlers.NewCollectionHandler(s.db, permService)
	boardConfigHandler := handlers.NewBoardConfigurationHandler(repository.NewBoardConfigurationRepository(s.db), repository.NewCollectionRepository(s.db), permService)
	testCoverageHandler := handlers.NewTestCoverageHandler(repository.NewTestCoverageRepository(s.db), permService)
	publicBoardHandler := handlers.NewPublicBoardHandler(s.db, permService, cfg.AttachmentPath)
	permissionHandler := handlers.NewPermissionHandlerWithCache(repository.NewPermissionRepository(s.db), permService, logger.NewAuditor(s.db))
	apiTokenHandler := handlers.NewAPITokenHandler(
		tokenManager,
		repository.NewAPITokenPolicyRepository(s.db),
		repository.NewWorkspaceRepository(s.db),
		logger.NewAuditor(s.db),
		permService,
	)
	agentHandler := handlers.NewAgentHandler(s.db, permService)

	// SCIM handlers
	scimTokenManager := auth.NewSCIMTokenManager(s.db)
	scimAuthMiddleware := middleware.NewSCIMAuthMiddleware(scimTokenManager)
	scimHandler := handlers.NewSCIMHandler(
		repository.NewSCIMRepository(s.db),
		baseURL,
		permService,
		logger.NewAuditor(s.db),
		func(id int) (services.AgentDeactivationResult, error) {
			return services.DeactivateOwnedAgentsAndTokens(s.db, id)
		},
		func() ([]int, error) {
			return services.ActiveSystemAdminIDs(s.db)
		},
		s.notificationService,
	)
	scimTokenHandler := handlers.NewSCIMTokenHandler(scimTokenManager, logger.NewAuditor(s.db))

	permissionSetHandler := handlers.NewPermissionSetHandlerWithPool(repository.NewPermissionSetRepository(s.db), permService, logger.NewAuditor(s.db))
	workspaceRoleHandler := handlers.NewWorkspaceRoleHandlerWithPool(repository.NewWorkspaceRoleRepository(s.db), permService, logger.NewAuditor(s.db))

	// Time tracking handlers
	timePermissionService := services.NewTimePermissionService(s.db, permService)
	customerOrgPermissionService := services.NewCustomerOrganisationPermissionService(s.db, permService, timePermissionService)
	timeCustomerHandler := handlers.NewTimeCustomerHandler(repository.NewCustomerOrganisationRepository(s.db), logger.NewAuditor(s.db), timePermissionService, customerOrgPermissionService)
	timeProjectHandler := handlers.NewTimeProjectHandler(s.db, timePermissionService, customerOrgPermissionService, workspaceKeyCache)
	timeProjectCategoryHandler := handlers.NewTimeProjectCategoryHandler(repository.NewTimeProjectCategoryRepository(s.db), logger.NewAuditor(s.db), timePermissionService)
	timeWorklogHandler := handlers.NewTimeWorklogHandler(s.db, permService, timePermissionService)
	activeTimerRepo := repository.NewActiveTimerRepository(s.db)
	timerService := services.NewTimerService(activeTimerRepo, repository.NewItemRepository(s.db), timePermissionService, permService)
	activeTimerHandler := handlers.NewActiveTimerHandler(activeTimerRepo, timerService)
	timeProjectPermissionHandler := handlers.NewTimeProjectPermissionHandler(logger.NewAuditor(s.db), timePermissionService)
	customerOrgPermissionHandler := handlers.NewCustomerOrganisationPermissionHandler(logger.NewAuditor(s.db), customerOrgPermissionService)

	// Test management handlers
	testFolderHandler := handlers.NewTestFolderHandler(services.NewTestFolderService(s.db), logger.NewAuditor(s.db))
	testCaseHandler := handlers.NewTestCaseHandlerWithPool(services.NewTestCaseService(s.db), logger.NewAuditor(s.db))
	workspaceResourceRepo := repository.NewWorkspaceResourceRepository(s.db)
	testSetHandler := handlers.NewTestSetHandlerWithPool(repository.NewTestSetRepository(s.db), workspaceResourceRepo, logger.NewAuditor(s.db))
	testRunTemplateHandler := handlers.NewTestRunTemplateHandlerWithPool(repository.NewTestRunTemplateRepository(s.db), workspaceResourceRepo)
	testRunHandler := handlers.NewTestRunHandlerWithPool(services.NewTestRunService(s.db), repository.NewTestRunRepository(s.db), repository.NewItemRepository(s.db), logger.NewAuditor(s.db))
	testSummaryHandler := handlers.NewTestSummaryHandlerWithPool(repository.NewTestSummaryRepository(s.db))

	// Link management handlers
	linkTypeHandler := handlers.NewLinkTypeHandler(repository.NewLinkTypeRepository(s.db), logger.NewAuditor(s.db))
	itemLinkHandler := handlers.NewItemLinkHandler(s.db, s.notificationService, permService)

	// Label handler
	labelHandler := handlers.NewLabelHandler(repository.NewLabelRepository(s.db), repository.NewItemRepository(s.db), permService, logger.NewAuditor(s.db))

	// Work item template handler (WI-438)
	itemTemplateHandler := handlers.NewItemTemplateHandler(repository.NewTemplateRepository(s.db), permService, logger.NewAuditor(s.db))

	// Knowledge pages handler (workspace-scoped wiki).
	pageLabelRepo := repository.NewPageLabelRepository(s.db)
	pageService := services.NewPageService(s.db)
	pageService.SetPageLabelRepository(pageLabelRepo)
	pagePermissionService := services.NewPagePermissionService(s.db, permService)
	itemLinkHandler.SetPagePermissionChecker(pagePermissionService)
	pageHandler := handlers.NewPageHandler(pageService, pagePermissionService, logger.NewAuditor(s.db))
	knowledgeRetrieval := services.NewKnowledgeRetrievalService(s.db, pagePermissionService)
	knowledgeSearchHandler := handlers.NewKnowledgeSearchHandler(knowledgeRetrieval)
	pageLabelHandler := handlers.NewPageLabelHandler(pageLabelRepo, pagePermissionService, logger.NewAuditor(s.db))

	// Recurrence handler
	recurrenceHandler := handlers.NewRecurrenceHandler(repository.NewRecurrenceRepository(s.db), repository.NewItemRepository(s.db), s.recurrenceScheduler, permService)

	// Actions handler
	actionsHandler := handlers.NewActionsHandler(
		repository.NewActionRepository(s.db),
		repository.NewActionCredentialRepository(s.db),
		repository.NewItemRepository(s.db),
		logger.NewAuditor(s.db),
		s.actionService,
		permService,
		workspaceKeyCache,
	)
	actionCredentialService := services.NewActionCredentialService(repository.NewActionCredentialRepository(s.db), cfg.Auth.SessionSecret)
	actionCredentialsHandler := handlers.NewActionCredentialsHandler(actionCredentialService, permService, workspaceKeyCache, logger.NewAuditor(s.db))
	// Wire credential resolution into the action runtime so HTTP capabilities
	// can reference tokens by ID. The service shares the same SSO_SECRET via
	// a domain-separated HKDF label (ActionCredentialEncryptionInfo).
	credentialSvc := services.NewActionCredentialService(
		repository.NewActionCredentialRepository(s.db),
		cfg.Auth.SessionSecret,
	)
	s.actionService.SetCredentialService(credentialSvc)
	// Lets container_run nodes dispatch to a remote runner pool (WI-146).
	s.actionService.SetAgentRunRepository(repository.NewAgentRunRepository(s.db))
	// One-shot scanner: warn about any legacy capability whose
	// default_headers still holds a sensitive header value. The scanner logs
	// capability ID + header name only — never the value.
	services.ScanLegacyInlineSecrets(s.db)

	// Team handlers
	teamRepo := repository.NewTeamRepository(s.db)
	leaveRepo := repository.NewLeaveRepository(s.db)
	onCallRepo := repository.NewOnCallRepository(s.db)
	teamService := services.NewTeamService(s.db, teamRepo, leaveRepo)
	onCallService := services.NewOnCallService(s.db, onCallRepo, leaveRepo)
	teamHandler := handlers.NewTeamHandler(teamRepo, leaveRepo, permService, logger.NewAuditor(s.db))
	leaveHandler := handlers.NewLeaveHandler(leaveRepo, repository.NewUserRepository(s.db), permService)
	onCallHandler := handlers.NewOnCallHandler(onCallRepo, teamRepo, onCallService, permService)
	s.actionService.SetTeamService(teamService)

	milestoneCategoryConfig := services.NewMilestoneCategoryConfig()
	milestoneCategoryConfig.AuditEmit = enumAuditEmit
	milestoneCategoryHandler := handlers.NewEnumHandler(
		services.NewEnumService(s.db, milestoneCategoryConfig),
		func() interface{} { return &models.MilestoneCategory{} })
	channelCategoryConfig := services.NewChannelCategoryConfig()
	channelCategoryConfig.AuditEmit = enumAuditEmit
	channelCategoryHandler := handlers.NewEnumHandler(
		services.NewEnumService(s.db, channelCategoryConfig),
		func() interface{} { return &models.ChannelCategory{} })
	collectionCategoryConfig := services.NewCollectionCategoryConfig()
	collectionCategoryConfig.AuditEmit = enumAuditEmit
	collectionCategoryHandler := handlers.NewEnumHandler(
		services.NewEnumService(s.db, collectionCategoryConfig),
		func() interface{} { return &models.CollectionCategory{} })
	iterationTypeConfig := services.NewIterationTypeConfig()
	iterationTypeConfig.AuditEmit = enumAuditEmit
	iterationTypeHandler := handlers.NewEnumHandler(
		services.NewEnumService(s.db, iterationTypeConfig),
		func() interface{} { return &models.IterationType{} })
	iterationHandler := handlers.NewIterationHandler(services.NewPlanningService(s.db), permService, logger.NewAuditor(s.db))
	personalLabelHandler := handlers.NewPersonalLabelHandler(s.db, permService)
	commentHandler := handlers.NewCommentHandler(s.db, permService, s.activityTracker, s.notificationService)
	reviewHandler := handlers.NewReviewHandler(s.db)
	calendarFeedHandler := handlers.NewCalendarFeedHandler(s.db, permService, cfg.BaseURL)
	securitySettingsHandler := handlers.NewSecuritySettingsHandler(repository.NewSystemSettingRepository(s.db), logger.NewAuditor(s.db), cfg.Plugins.Disabled)

	// WI-87/88/89/90 coding-agent harness stack lands later in the
	// constructor — see the block right after the SCM handlers are
	// built, since scm.CredentialResolver needs scmProviderHandler.GetEncryption().

	// Admin rate limiter
	var adminRateLimiter *middleware.AdminFallbackRateLimiter
	if cfg.EnableAdminFallback {
		adminRateLimiter = middleware.NewAdminFallbackRateLimiter(s.db)
		slog.Info("Admin password fallback enabled", slog.String("component", "auth"))
	}

	authPolicyHandler := handlers.NewAuthPolicyHandlerWithFallback(s.db, cfg.EnableAdminFallback, logger.NewAuditor(s.db))
	webAuthnHandler.SetAuthPolicyHandler(authPolicyHandler)

	// Initialize auth handler
	authHandler := handlers.NewAuthHandler(
		repository.NewUserRepository(s.db),
		repository.NewCredentialRepository(s.db),
		logger.NewAuditor(s.db),
		sessionManager,
		s.loginRateLimiter,
		permService,
		emailVerificationService,
		ipExtractor,
		authPolicyHandler,
		adminRateLimiter,
	)

	// Initialize invitation handler
	invitationHandler := handlers.NewInvitationHandler(invitationService)

	themeHandler := handlers.NewThemeHandler(services.NewThemeService(repository.NewThemeRepository(s.db)), logger.NewAuditor(s.db))
	userPreferencesHandler := handlers.NewUserPreferencesHandler(services.NewUserPreferencesService(repository.NewUserPreferencesRepository(s.db), repository.NewThemeRepository(s.db)))
	homepageHandler := handlers.NewHomepageHandler(repository.NewWorkspaceRepository(s.db), repository.NewItemRepository(s.db), s.activityTracker, permService)

	// Notification handlers
	notificationHandler := handlers.NewNotificationHandler(s.notificationManager, s.notificationService)
	emailTemplateHandler := handlers.NewEmailTemplateHandler(repository.NewEmailTemplateRepository(s.db), logger.NewAuditor(s.db))

	// Web Push: store subscriptions + fan notifications out to them. The
	// dispatcher is wired into the notification manager so every created
	// notification (assignments, mentions, comments, …) triggers a push.
	// VAPID keys are resolved env > persisted > auto-generated, so push works
	// out of the box on a fresh deployment with no operator configuration.
	pushCfg := services.ResolveVAPIDConfig(s.db, cfg.Push, slog.Default())
	pushService := services.NewPushService(s.db, pushCfg)
	pushHandler := handlers.NewPushHandler(pushService)
	s.notificationManager.SetPushDispatcher(pushService)
	if pushService.Enabled() {
		slog.Info("Web Push enabled")
	}

	permissionMiddleware := middleware.NewPermissionMiddleware(s.db, permService)

	// Setup handler
	setupHandler := handlers.NewSetupHandler(s.db, sessionManager, authMiddleware)

	// SSO handler
	ssoHandler := handlers.NewSSOHandler(s.db, sessionManager, permService, emailVerificationService, s.pluginManager, cfg.Auth.SessionSecret, baseURL, cfg.AllowedHosts, cfg.DisableCSRF, ipExtractor, cfg.UseProxy, additionalProxyList)

	// SCM provider handler
	scmProviderHandler := handlers.NewSCMProviderHandler(s.db, cfg.Auth.SessionSecret, baseURL)
	scmWorkspaceHandler := handlers.NewSCMWorkspaceHandler(repository.NewSCMWorkspaceRepository(s.db), scmProviderHandler.GetEncryption(), scmProviderHandler, scm.NewCredentialResolver(s.db, scmProviderHandler.GetEncryption()), permService, baseURL)
	scmItemLinksHandler := handlers.NewSCMItemLinksHandler(s.db, scmProviderHandler.GetEncryption(), permService)
	userSCMTokenHandler := handlers.NewUserSCMTokenHandler(repository.NewUserSCMTokenRepository(s.db), scmProviderHandler.GetEncryption())
	milestoneHandler := handlers.NewMilestoneHandler(services.NewPlanningService(s.db), permService, scm.NewCredentialResolver(s.db, scmProviderHandler.GetEncryption()), logger.NewAuditor(s.db))

	// WI-87/88/89/90 coding-agent harness stack. The acting-identity
	// chokepoint (WI-87) is constructed first; both the workspace-binding
	// service and the admin AgentSecurity handler share its repo handle.
	// When CodingAgent.Enabled is set, the harness boots an orchestration-only
	// RunService (WI-89): RunTokenService → AgentPRService (WI-90, opens draft
	// PRs on GitHub or Gitea via scm.Provider). It queues runs, enriches remote
	// claims, and finalizes remote results — but executes nothing on this host.
	// All runs are dispatched to remote runner pools (windshift-runner). Without
	// the flag the harness stays in observer mode — bindings can still be
	// created, the trigger logs but no run starts.
	agentSecurityRepo := repository.NewAgentSecurityRepository(s.db)
	agentIdentitySvc, _ := services.NewAgentActingIdentityService(services.NewUserReadService(s.db), agentSecurityRepo)
	agentBindingRepo := repository.NewWorkspaceAgentBindingRepository(s.db)
	scmCredResolver := scm.NewCredentialResolver(s.db, scmProviderHandler.GetEncryption())

	// Shared AI prompt store: embedded defaults overridable from cfg.LLM.PromptsDir
	// (AI_PROMPTS_DIR). Built here so the coding-agent harness and the AI handlers
	// below resolve the same overridable prompts.
	promptStore := llm.NewPromptStore(cfg.LLM.PromptsDir)

	// Load LLM provider definitions before coding-agent bindings are wired: the
	// binding trigger resolves per-binding llm_connection_id rows into agent runtime
	// env, and that requires the same provider registry the AI handlers use.
	if cfg.LLM.ProvidersFile != "" {
		if err := llm.LoadProviders(cfg.LLM.ProvidersFile); err != nil {
			slog.Error("failed to load custom LLM providers file, falling back to built-in defaults", "path", cfg.LLM.ProvidersFile, "error", err)
			llm.LoadDefaultProviders()
		} else {
			slog.Info("loaded custom LLM providers", "path", cfg.LLM.ProvidersFile)
		}
	} else {
		llm.LoadDefaultProviders()
	}

	fallbackLLMClient := llm.NewClient(llm.Config{Endpoint: cfg.LLM.Endpoint})
	if fallbackLLMClient.Available() {
		slog.Info("LLM fallback service configured", slog.String("endpoint", cfg.LLM.Endpoint))
	} else {
		slog.Info("LLM fallback service not configured")
	}
	llmManager := llm.NewConnectionManager(s.db, scmProviderHandler.GetEncryption(), fallbackLLMClient)
	llmModelCache := llm.NewModelCache(s.db)
	llmManager.SetModelCache(llmModelCache) // freshest vision-capability resolution
	llmModelRefresher := llm.NewModelRefresher(llmModelCache)

	var codingRunSvc *services.RunService
	if cfg.CodingAgent.Enabled {
		var bootErr error
		codingRunSvc, bootErr = bootCodingAgentRunService(s.db, tokenManager, agentBindingRepo, scmCredResolver, promptStore.Get(llm.PromptCodingAgentInitial))
		if bootErr != nil {
			slog.Warn("coding-agent harness disabled: failed to construct RunService",
				slog.String("component", "coding-agent"),
				slog.Any("error", bootErr),
			)
		}
	}
	// Kept on the Server so Shutdown can drain in-flight local runs instead
	// of leaving them to be killed mid-flight with their rows stuck
	// non-terminal (WI-332).
	s.codingRunService = codingRunSvc

	agentAPIURL := cfg.CodingAgent.WSAPIURL
	if agentAPIURL == "" {
		// The agent-facing URL convention INCLUDES the mandatory /api suffix
		// (see apiBaseURLFor); the broker URLs handed to agent containers
		// (LLM_BASE_URL, git-proxy) are built directly on it. Falling back to
		// the bare BASE_URL would send the agent's chat-completion POSTs to a
		// path only the SPA catch-all matches — a 405 on every model call.
		agentAPIURL = strings.TrimRight(baseURL, "/") + "/api"
	}
	agentSkillRepo := repository.NewWorkspaceAgentSkillRepository(s.db)
	bindingSvc, _ := services.NewBindingService(services.BindingServiceOptions{
		Repo:       agentBindingRepo,
		Identity:   agentIdentitySvc,
		Runs:       codingRunSvc,
		SCMCreds:   &scmCredsAdapter{cr: scmCredResolver},
		LLMRuntime: llmManager,
		RunContext: agentBindingRepo,
		Pools:      repository.NewActionRepository(s.db),
		Skills:     agentSkillRepo,
		// @mention on an item with an open linked PR continues that PR instead of
		// opening a competing one (WI-426).
		Continuations: &itemPRContinuationResolver{db: s.db, cr: scmCredResolver},
		APIURL:        agentAPIURL,
	})
	// Let the run service enrich remote claims from the binding (WI-195): a
	// remote runner's claim mints the per-run token + grants the same way the
	// local path does. Wired post-construction to break the service cycle.
	if codingRunSvc != nil && bindingSvc != nil {
		codingRunSvc.SetBindingInputsResolver(bindingSvc)
	}
	agentBindingHandler := handlers.NewWorkspaceAgentBindingHandler(bindingSvc, agentIdentitySvc, permService, logger.NewAuditor(s.db))
	agentBindingHandler.SetSkillsRepo(agentSkillRepo)
	agentSkillHandler := handlers.NewAgentSkillHandler(agentSkillRepo, permService, logger.NewAuditor(s.db))
	agentRunHandler := handlers.NewAgentRunHandler(repository.NewAgentRunRepository(s.db), codingRunSvc, permService, repository.NewItemRepository(s.db), bindingSvc)
	agentRunHandler.SetUsageRepository(repository.NewLLMUsageRepository(s.db)) // per-run token/cost readout (WI-494)
	// Remote-runner control plane (WI-141). Constructed unconditionally;
	// the handler 503s when the registry/run service is unavailable (i.e.
	// CodingAgent.Enabled is off).
	runnerRegistry := services.NewRunnerRegistryService(repository.NewRunnerRepository(s.db), nil)
	runnerControlHandler := handlers.NewRunnerControlHandler(runnerRegistry, repository.NewAgentRunRepository(s.db), codingRunSvc, repository.NewActionRepository(s.db), nil, baseURL)
	// Agent presence for assignment pickers (WI-272): binding → pool →
	// heartbeat-fresh runner count, surfaced as online/offline/local/unbound.
	userHandler.SetAgentPresenceService(services.NewAgentPresenceService(agentBindingRepo, repository.NewRunnerRepository(s.db)))
	// Secretless access layer (WI-144): brokers a granted credential to a
	// running job without it ever living on the runner host.
	runnerBrokerHandler := handlers.NewRunnerBrokerHandler(tokenManager, repository.NewAgentRunRepository(s.db), credentialSvc, llmManager, &scmCredsAdapter{cr: scmCredResolver})
	runnerBrokerHandler.SetUsageRepository(repository.NewLLMUsageRepository(s.db)) // meter LLM token/cost at the broker (WI-493)
	if bindingSvc != nil {
		// Registers the coding-agent assignee trigger inside the item
		// create/update services, so every surface that sets an assignee
		// (cookie handlers, REST v1, MCP/AI tools, automation actions,
		// recurrence) starts runs — not just the cookie update handler.
		services.SetItemAssigneeTrigger(bindingSvc)
	}

	// Asset management handlers
	assetHandler := handlers.NewAssetHandler(s.db, permService, cfg.AttachmentPath)
	assetHandler.SetAssetActionService(s.assetActionService)
	if n, err := assetHandler.ReconcileInterruptedImports(); err != nil {
		slog.Warn("failed to reconcile interrupted asset imports", slog.Any("error", err))
	} else if n > 0 {
		slog.Info("reconciled interrupted asset imports", slog.Int("count", n))
	}
	itemLinkHandler.SetAssetPermissionChecker(assetHandler)
	assetRepo := repository.NewAssetRepository(s.db)
	assetTypeHandler := handlers.NewAssetTypeHandler(assetRepo, assetHandler, logger.NewAuditor(s.db))
	assetCategoryHandler := handlers.NewAssetCategoryHandler(assetRepo, assetHandler, logger.NewAuditor(s.db))
	assetStatusHandler := handlers.NewAssetStatusHandler(assetRepo, assetHandler, logger.NewAuditor(s.db))
	assetReportHandler := handlers.NewAssetReportHandler(
		repository.NewAssetReportRepository(s.db),
		repository.NewChannelRepository(s.db),
		repository.NewScreenRepository(s.db),
		logger.NewAuditor(s.db),
		channelService,
		services.NewAssetPermissionService(assetRepo, permService),
	)
	assetActionHandler := handlers.NewAssetActionHandler(repository.NewAssetActionRepository(s.db), assetHandler, s.assetActionService, logger.NewAuditor(s.db))

	// Jira import handler
	jiraImportHandler := handlers.NewJiraImportHandler(s.db, cfg.Auth.SessionSecret, cfg.Jira.CapturePayloadsDir)

	// Email provider handler
	emailProviderHandler := handlers.NewEmailProviderHandler(s.db, scmProviderHandler.GetEncryption(), baseURL)

	// Email scheduler
	emailCredManager := email.NewCredentialManager(s.db, scmProviderHandler.GetEncryption())
	s.emailScheduler = scheduler.NewEmailScheduler(s.db, emailCredManager, cfg.AttachmentPath)
	s.emailScheduler.Start()
	slog.Info("email scheduler started (IMAP polling)")

	// Daily retention sweep for email_message_tracking. Per-channel
	// retention comes from ChannelConfig.EmailTrackingRetentionDays; anchors
	// referenced by in_reply_to are preserved past the cutoff.
	s.emailTrackingRetention = scheduler.NewEmailTrackingRetentionSweeper(s.db)
	s.emailTrackingRetention.Start()

	// Integration provider handlers
	integrationProviderHandler := handlers.NewIntegrationProviderHandler(repository.NewIntegrationProviderRepository(s.db), scmProviderHandler.GetEncryption(), logger.NewAuditor(s.db))
	integrationOAuthHandler := handlers.NewIntegrationOAuthHandler(s.db, scmProviderHandler.GetEncryption(), baseURL)
	integrationItemLinksHandler := handlers.NewIntegrationItemLinksHandler(s.db, scmProviderHandler.GetEncryption(), permService)
	todoistSyncHandler := handlers.NewTodoistSyncHandler(s.db, scmProviderHandler.GetEncryption())
	s.todoistSyncScheduler = scheduler.NewTodoistSyncScheduler(s.db, scmProviderHandler.GetEncryption())
	s.todoistSyncScheduler.Start()

	// SCM sync service (started below once smart-commit dependencies exist)
	scmSyncService := scm.NewSyncService(s.db, scmProviderHandler.GetEncryption())

	// Issue sync service
	issueSyncService := scm.NewIssueSyncService(s.db, scmProviderHandler.GetEncryption())
	issueSyncService.SetUserService(services.NewUserReadService(s.db))

	// Start issue sync scheduler
	go s.runIssueSync(issueSyncService)

	// Start magic link cleanup scheduler
	go s.runMagicLinkCleanup(magicLinkService)

	// Webhook sender
	webhookSender := webhook.NewWebhookSender(s.db)

	// Event coordinator
	eventCoordinator := services.NewEventCoordinator(s.db)
	eventCoordinator.SetNotificationService(s.notificationService)
	eventCoordinator.SetActivityTracker(s.activityTracker)
	eventCoordinator.SetWebhookDispatcher(webhookSender)
	eventCoordinator.SetActionService(s.actionService)
	eventCoordinator.SetAssetActionService(s.assetActionService)
	eventCoordinator.SetMagicLinkService(magicLinkService)
	s.actionService.SetAssetActionService(s.assetActionService)
	s.actionService.SetEventCoordinator(eventCoordinator)
	s.actionService.SetAssetPermissionChecker(assetHandler)
	s.assetActionService.SetEventCoordinator(eventCoordinator)
	slog.Info("event coordinator initialized")

	// Wire up services
	itemHandler.SetWebhookSender(webhookSender)
	itemHandler.SetEventCoordinator(eventCoordinator)
	commentHandler.SetWebhookSender(webhookSender)

	// Item live-update stream (WI-484): register the in-memory SSE hub as the
	// process-wide item-change publisher (WI-483 installed a no-op default), and
	// give the item handler the hub so GET /items/{id}/events can subscribe.
	sseHub := services.NewSSEHub()
	services.SetItemChangePublisher(sseHub)
	itemHandler.SetSSEHub(sseHub)

	// Mention service
	mentionService := services.NewMentionService(s.db, s.notificationService, permService)
	itemHandler.SetMentionService(mentionService)
	commentHandler.SetMentionService(mentionService)

	// Comment service
	commentService := services.NewCommentService(s.db)
	commentService.SetActivityTracker(s.activityTracker)
	commentService.SetNotificationService(s.notificationService)
	commentService.SetMentionService(mentionService)
	commentService.SetWebhookSender(webhookSender)
	if bindingSvc != nil {
		// @mentioning a binding's acting user in a comment starts a run
		// (WI-264), same machinery as the assignee-change trigger.
		commentService.SetAgentMentionTrigger(bindingSvc)
	}
	commentHandler.SetCommentService(commentService)
	commentHandler.SetIssueSyncService(issueSyncService)
	s.actionService.SetCommentService(commentService)

	// Wire email reply service for bidirectional email threading
	emailReplyService := services.NewEmailReplyService(s.db, smtpSender)
	commentService.SetEmailReplyService(emailReplyService)

	// Wire CommentService into email processor for unified comment creation
	s.emailScheduler.SetCommentService(commentService)

	// Wire EventCoordinator into email processor so inbound-email-created items
	// emit the same notifications/webhooks/action events as REST-created ones.
	s.emailScheduler.SetEventCoordinator(eventCoordinator)

	slog.Info("comment service initialized")

	// Wire up action service
	itemHandler.SetActionService(s.actionService)
	itemHandler.SetIssueSyncService(issueSyncService)
	itemLinkHandler.SetActionService(s.actionService)

	// Wire up condition service for workflow transition conditions
	scriptEngine := services.NewScriptEngine()
	conditionService := services.NewConditionService(s.db, permService, scriptEngine)
	itemHandler.SetConditionService(conditionService)

	// Wire up approval service for status-bound approvals (sibling of conditions).
	approvalService := services.NewApprovalService(s.db, permService, leaveRepo, workflowService)
	approvalService.SetEventCoordinator(eventCoordinator)
	approvalSetService := services.NewApprovalSetService(s.db)
	itemHandler.SetApprovalService(approvalService)
	commentHandler.SetApprovalService(approvalService)
	s.actionService.SetApprovalService(approvalService)
	workspaceRoleHandler.SetApprovalService(approvalService)

	// Background sweeper drives time-based escalation for pending approval steps.
	s.approvalEscalationSweeper = services.NewApprovalEscalationSweeper(s.db, approvalService, services.DefaultApprovalEscalationSweeperConfig())
	s.approvalEscalationSweeper.Start()

	// Wire smart-commit dependencies into the SCM sync service and start its
	// scheduler. Must be done after commentService and conditionService exist.
	scmSyncService.SetSmartCommitServices(
		workflowService, commentService, permService, conditionService,
		repository.NewItemRepository(s.db),
	)
	scmSyncService.SetApprovalService(approvalService)
	// Outbound "@agent" PR-comment continuation trigger (WI-426): the sync poller
	// hands detected comments to the binding service to continue the PR. Nil-safe
	// when the coding-agent harness is disabled (bindingSvc may be nil).
	if bindingSvc != nil {
		scmSyncService.SetContinuationStarter(bindingSvc)
	}

	// Wire the SCM-driven milestone automation:
	//  1) sync emits ActionEvents for new tags / release branches,
	//  2) the create_milestone node executor consumes them and upserts
	//     by external_key (with optional release attach + commit-issue
	//     attachment via the scm.MilestoneAttacher adapter).
	scmSyncService.SetActionEvents(s.actionService)
	milestoneAttacher := scm.NewMilestoneAttacher(
		scmSyncService,
		repository.NewMilestoneAttachRepository(s.db),
	)
	s.actionService.RegisterNodeExecutor(
		services.NewCreateMilestoneExecutor(services.NewPlanningService(s.db), s.actionService).
			WithCommitAttacher(milestoneAttacher),
	)

	go s.runSCMRepoSync(scmSyncService)
	go s.runSCMLinkRefresh(scmSyncService)
	go s.runSCMOAuthStateCleanup()

	// Channel handler (reuses the shared channelService initialized earlier)
	channelRepoForHandler := repository.NewChannelRepository(s.db)
	channelHandler := handlers.NewChannelHandler(
		channelRepoForHandler,
		repository.NewUserRepository(s.db),
		channelService,
		permService,
		webhookSender,
		logger.NewAuditor(s.db),
	)
	channelHandler.SetEmailScheduler(s.emailScheduler)
	channelHandler.SetEncryption(scmProviderHandler.GetEncryption())
	channelHandler.SetBaseURL(baseURL)
	channelHandler.SetSMTPSender(smtpSender)
	channelHandler.SetCredentialManager(email.NewCredentialManager(s.db, scmProviderHandler.GetEncryption()))
	// Wire at-rest decryption into the SMTP sender so dispatch can decrypt
	// SMTPPassword before AUTH PLAIN. Done here (after scmProviderHandler is
	// initialized) rather than at smtpSender construction time because the
	// scheduler/notification wiring above can't depend on the encryption
	// service yet.
	smtpSender.SetEncryption(scmProviderHandler.GetEncryption())

	// Webhook handler
	webhookHandler := handlers.NewWebhookHandler(repository.NewChannelRepository(s.db), repository.NewItemRepository(s.db), webhookSender, permService, channelService)
	portalHandler := handlers.NewPortalHandler(s.db, sessionManager, portalSessionManager, ipExtractor, cfg.AttachmentPath)
	portalHandler.SetApprovalService(approvalService)
	portalAuthHandler := handlers.NewPortalAuthHandler(repository.NewPortalAuthRepository(s.db), portalSessionManager, sessionManager, magicLinkService, ipExtractor)
	portalWebAuthnHandler := handlers.NewPortalWebAuthnHandler(
		portalSessionManager,
		portalWebAuthnConfig,
		portalwebauthn.NewSessionStore(s.db),
		portalwebauthn.NewCredentialStore(s.db),
		portalwebauthn.NewPortalLookupStore(s.db),
		ipExtractor,
	)
	portalCustomersHandler := handlers.NewPortalCustomersHandler(s.db, permService, customerOrgPermissionService)
	contactRoleConfig := services.NewContactRoleConfig()
	contactRoleConfig.AuditEmit = enumAuditEmit
	contactRolesHandler := handlers.NewEnumHandler(
		services.NewEnumService(s.db, contactRoleConfig),
		func() interface{} { return &models.ContactRole{} })
	hubHandler := handlers.NewHubHandler(s.db, permService, logger.NewAuditor(s.db))
	formHandler := handlers.NewFormHandler(s.db, sessionManager, portalSessionManager, ipExtractor, channelService)

	// Notification settings
	notificationSettingsHandler := handlers.NewNotificationSettingsHandler(repository.NewNotificationSettingsRepository(s.db), logger.NewAuditor(s.db), s.notificationService)
	configSetNotificationHandler := handlers.NewConfigurationSetNotificationHandler(repository.NewConfigurationSetRepository(s.db), s.notificationService, logger.NewAuditor(s.db))

	// Attachment handlers
	var attachmentHandler *handlers.AttachmentHandler
	var attachmentSettingsHandler *handlers.AttachmentSettingsHandler
	if cfg.AttachmentPath != "" {
		slog.Info("attachments enabled", "path", cfg.AttachmentPath)
		attachmentHandler = handlers.NewAttachmentHandler(s.db, cfg.AttachmentPath, permService)
		attachmentHandler.SetApprovalService(approvalService)
		attachmentHandler.SetPagePermissionService(pagePermissionService)
		attachmentHandler.SetChannelService(channelService)
		attachmentSettingsService := services.NewAttachmentSettingsService(s.db)
		if err := attachmentSettingsService.Initialize(cfg.AttachmentPath); err != nil {
			slog.Warn("failed to initialize attachment settings", "error", err)
		}
		attachmentSettingsHandler = handlers.NewAttachmentSettingsHandler(attachmentSettingsService, logger.NewAuditor(s.db))
	} else {
		slog.Info("attachments disabled (no attachment path specified)")
	}

	// Diagram handler
	diagramHandler := handlers.NewDiagramHandler(repository.NewDiagramRepository(s.db), repository.NewItemRepository(s.db), permService)

	// Plugin system
	var pluginRouter *plugins.Router
	if !cfg.Plugins.Disabled {
		var pluginOpts []plugins.Option
		pluginOpts = append(pluginOpts, plugins.WithDatabase(s.db), plugins.WithSCMService(scmSyncService), plugins.WithCommentService(commentService))

		pluginDir := cfg.Plugins.Dir
		if pluginDir == "" {
			pluginDir = "plugins"
		}

		// PLUGIN_DIRS additional dirs (pre-split by config.Load)
		var additionalDirs []string
		for _, dir := range cfg.Plugins.ExtraDirs {
			if dir != "" && dir != pluginDir {
				additionalDirs = append(additionalDirs, dir)
			}
		}
		if len(additionalDirs) > 0 {
			slog.Info("loading plugins from additional directories", "dirs", additionalDirs)
			pluginOpts = append(pluginOpts, plugins.WithAdditionalPluginDirs(additionalDirs...))
		}

		s.pluginManager = plugins.NewManager(pluginDir, pluginOpts...)
		slog.Info("initializing plugin system")
		if err := s.pluginManager.LoadPlugins(); err != nil {
			slog.Warn("failed to load plugins", "error", err)
		}

		// Create webhook dispatcher
		webhookDispatcher := plugins.NewWebhookDispatcher(s.pluginManager, s.db)
		webhookSender.SetPluginDispatcher(webhookDispatcher)

		// Register plugin webhooks
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		for _, plugin := range s.pluginManager.ListPlugins() {
			if err := s.pluginManager.RegisterPluginWebhooks(ctx, s.db, plugin); err != nil {
				slog.Warn("failed to register plugin webhooks", "plugin", plugin.Manifest.Name, "error", err)
			}
		}
		cancel()

		pluginRouter = plugins.NewRouter(s.pluginManager)

		// Plugin schedule scheduler — invokes plugin handlers on their declared
		// interval (manifest `schedules:` field). Must start after LoadPlugins
		// so the in-memory schedule registry is populated for the first tick.
		s.pluginScheduleScheduler = scheduler.NewPluginScheduleScheduler(s.pluginManager, s.db)
		s.pluginScheduleScheduler.Start()
	} else {
		slog.Info("plugin system disabled")
	}

	pluginHandler := handlers.NewPluginHandler(s.pluginManager, repository.NewPluginRegistryRepository(s.db), logger.NewAuditor(s.db), cfg.Plugins.Disabled)

	// Audit log handler
	auditLogHandler := handlers.NewAuditLogHandler(repository.NewAuditLogRepository(s.db))

	// LDAP handler — keep on Server so Shutdown can drain in-flight syncs.
	ldapSyncService := ldap.NewSyncService(s.db, ssoHandler.GetEncryption())
	s.ldapHandler = handlers.NewLDAPHandler(s.db, ldapSyncService, ssoHandler.GetEncryption())
	ldapHandler := s.ldapHandler

	// Features handler
	featuresHandler := handlers.NewFeaturesHandler(s.pluginManager, cfg.SSH.Enabled, cfg.Logbook.Endpoint != "")

	// System handler
	shutdownChan := cfg.ShutdownChan
	if shutdownChan == nil {
		shutdownChan = make(chan os.Signal, 1)
	}
	systemHandler := handlers.NewSystemHandler(shutdownChan)

	// LLM connection manager and AI handler
	llmConnHandler := handlers.NewLLMConnectionHandler(llmManager, logger.NewAuditor(s.db), llmModelCache, llmModelRefresher)
	aiHandler := handlers.NewAIHandler(s.db, llmManager, permService, timePermissionService, timerService, promptStore, s.actionService)

	// Briefing scheduler (generates daily briefings for all users)
	s.briefingScheduler = scheduler.NewBriefingScheduler(s.db, llmManager, permService, timePermissionService, services.NewUserReadService(s.db), promptStore)
	s.briefingScheduler.Start()

	// Logbook reverse proxy (optional sidecar)
	if cfg.Logbook.Endpoint != "" {
		proxyCfg := LogbookProxyConfig{
			Endpoint:          cfg.Logbook.Endpoint,
			AuthMiddleware:    authMiddleware,
			PermissionService: permService,
			UploadLimiter:     s.uploadLimiter,
			SharedSecret:      cfg.Auth.SessionSecret,
		}
		logbookProxy := NewLogbookProxy(proxyCfg)

		// Rate-limited upload routes (registered before the catch-all so they take priority)
		logbookUploadProxy := NewLogbookUploadProxy(proxyCfg)
		mux.Handle("POST /api/logbook/buckets/{bucketID}/documents/upload", logbookUploadProxy)
		mux.Handle("POST /api/logbook/documents/{documentID}/attachments", logbookUploadProxy)

		// All logbook routes (including actions) are proxied to the sidecar
		mux.Handle("GET /api/logbook/", logbookProxy)
		mux.Handle("POST /api/logbook/", logbookProxy)
		mux.Handle("PUT /api/logbook/", logbookProxy)
		mux.Handle("PATCH /api/logbook/", logbookProxy)
		mux.Handle("DELETE /api/logbook/", logbookProxy)
		slog.Info("logbook proxy enabled", "endpoint", cfg.Logbook.Endpoint)

		// Internal endpoints for sidecar → main server communication.
		// cfg.Auth.SessionSecret is already validated non-empty by config.Load,
		// so the guard is cosmetic — kept for defense-in-depth.
		if ssoSecret := cfg.Auth.SessionSecret; ssoSecret != "" {
			// LLM proxy for logbook article generation
			llmProxy := NewInternalLLMProxy(llmManager, ssoSecret)
			mux.Handle("POST /api/internal/llm/v1/chat/completions", llmProxy)
			mux.Handle("GET /api/internal/llm/health", NewInternalLLMHealthCheck(llmManager, ssoSecret))
			slog.Info("internal LLM proxy enabled for logbook article generation")

			// Node execution endpoint for logbook actions (create_item, create_asset on SQLite)
			nodeExecHandler := handlers.NewLogbookNodeExecutionHandler(
				ssoSecret,
				eventCoordinator,
				permService,
				assetHandler,
				func(params services.ItemCreationParams) (int64, error) { return services.CreateItem(s.db, params) },
				repository.NewWorkspaceRepository(s.db),
				repository.NewAssetRepository(s.db),
			)
			mux.Handle("POST /api/internal/logbook/execute-node", http.HandlerFunc(nodeExecHandler.HandleNodeExecution))
			slog.Info("internal logbook node execution endpoint enabled")
		}
	}

	// Build API middleware chain
	// Derive scheme from BASE_URL for CORS origin construction
	corsScheme := ""
	if cfg.BaseURL != "" {
		if parsed, err := url.Parse(cfg.BaseURL); err == nil {
			corsScheme = parsed.Scheme
		}
	}
	csrfOrigins := buildAllowedOrigins(cfg.AllowedHosts, effectivePort, corsScheme, cfg.UseProxy)
	corsMiddleware := createCORSMiddleware(cfg.AllowedHosts, effectivePort, corsScheme, cfg.DisableCSRF, cfg.UseProxy, cfg.AllowInsecureHTTP)
	apiCORSMiddleware := createFormEmbedCORSMiddleware(cfg.FormEmbedOrigins, csrfOrigins, corsMiddleware)
	apiMiddleware := router.MiddlewareChain{apiCORSMiddleware, authMiddleware.OptionalAuth}

	if !cfg.DisableCSRF {
		slog.Info("CSRF protection enabled (Sec-Fetch-Site + Origin/Referer fallback)", "allowed_origins", csrfOrigins)
		apiMiddleware = append(apiMiddleware, middleware.CSRFProtection(csrfOrigins))
	} else {
		slog.Warn("CSRF protection disabled (development mode)")
	}

	// Per-user concurrency cap goes last so it is the innermost wrapper: the
	// slot is held only around the handler, and OptionalAuth (earlier in the
	// chain) has already put the user in context for keying. Cheap rejections
	// (CORS/CSRF/auth failures) never consume a slot.
	apiMiddleware = append(apiMiddleware, s.userConcurrency.Limit)
	if cfg.MaxUserConcurrency > 0 {
		slog.Info("per-user API concurrency cap enabled", "max_in_flight_per_user", cfg.MaxUserConcurrency)
	}

	// Create API route group
	api := router.NewRouteGroup(mux, "/api", apiMiddleware...)

	// SCIM routes
	scimMiddleware := router.MiddlewareChain{corsMiddleware}
	scimGroup := router.NewRouteGroup(mux, "/scim/v2", scimMiddleware...)

	// Create portal auth middleware (accepts both internal and portal sessions)
	portalAuthMiddleware := middleware.NewPortalAuthMiddleware(sessionManager, portalSessionManager, cfg.UseProxy, additionalProxyList)

	// Build route dependencies
	routeDeps := &routes.Deps{
		API:       api,
		SCIMGroup: scimGroup,
		Mux:       mux,

		AuthMiddleware:       authMiddleware,
		PermissionMiddleware: permissionMiddleware,
		SCIMAuthMiddleware:   scimAuthMiddleware,
		PortalAuthMiddleware: portalAuthMiddleware,

		LoginRateLimiter:    s.loginRateLimiter,
		AuthRateLimiter:     s.authRateLimiter,
		FIDORateLimiter:     s.fidoRateLimiter,
		SSORateLimiter:      s.ssoRateLimiter,
		SCIMRateLimiter:     s.scimRateLimiter,
		PortalSubmitLimiter: s.portalSubmitLimiter,
		PortalSearchLimiter: s.portalSearchLimiter,
		PortalAuthLimiter:   s.portalAuthLimiter,
		OAuthTokenLimiter:   s.oauthTokenLimiter,
		EmailVerifyLimiter:  s.emailVerifyLimiter,
		SetupLimiter:        s.setupLimiter,
		AIRateLimiter:       s.aiRateLimiter,
		UploadLimiter:       s.uploadLimiter,
		WebhookLimiter:      s.webhookLimiter,
		SearchLimiter:       s.searchLimiter,
		CalendarFeedLimiter: s.calendarFeedLimiter,

		Auth: routes.AuthHandlers{
			Auth:       authHandler,
			SSO:        ssoHandler,
			WebAuthn:   webAuthnHandler,
			Invitation: invitationHandler,
		},
		SCIM: routes.SCIMHandlers{
			SCIM:      scimHandler,
			SCIMToken: scimTokenHandler,
		},
		SCM: routes.SCMHandlers{
			Provider:      scmProviderHandler,
			Workspace:     scmWorkspaceHandler,
			ItemLinks:     scmItemLinksHandler,
			UserToken:     userSCMTokenHandler,
			EmailProvider: emailProviderHandler,
			IssueSync:     handlers.NewIssueSyncHandler(issueSyncService, permService, logger.NewAuditor(s.db)),
		},
		Items: routes.ItemHandlers{
			Item:               itemHandler,
			Recurrence:         recurrenceHandler,
			Comment:            commentHandler,
			Attachment:         attachmentHandler,
			AttachmentSettings: attachmentSettingsHandler,
			Diagram:            diagramHandler,
			ItemLink:           itemLinkHandler,
			LinkType:           linkTypeHandler,
			Label:              labelHandler,
			ItemTemplate:       itemTemplateHandler,
		},
		Workspaces: routes.WorkspaceHandlers{
			Workspace:             workspaceHandler,
			Screen:                screenHandler,
			ConfigSet:             configSetHandler,
			ConfigSetNotification: configSetNotificationHandler,
			NotificationSettings:  notificationSettingsHandler,
			ItemType:              itemTypeHandler,
			Priority:              priorityHandler,
			HierarchyLevel:        hierarchyLevelHandler,
			RequestType:           requestTypeHandler,
			StatusCategory:        statusCategoryHandler,
			Status:                statusHandler,
			StatusLegacy:          statusHandlerLegacy,
			Workflow:              workflowHandler,
			Actions:               actionsHandler,
			ActionCredentials:     actionCredentialsHandler,
			ActionTemplates:       handlers.NewActionTemplatesHandler(services.NewActionTemplateService(s.db), s.actionService, workspaceKeyCache, logger.NewAuditor(s.db)),
			Analytics:             handlers.NewAnalyticsHandler(services.NewAnalyticsService(s.db), permService, workspaceKeyCache),
			ConditionSet:          handlers.NewConditionSetHandler(s.db),
			ApprovalSet:           handlers.NewApprovalSetHandler(approvalSetService, logger.NewAuditor(s.db)),
			Approval:              handlers.NewApprovalHandler(permService, approvalService, repository.NewItemRepository(s.db), logger.NewAuditor(s.db)),
			TransitionGovernance:  handlers.NewTransitionGovernanceHandler(repository.NewTransitionRepository(s.db), approvalSetService),
			AgentBinding:          agentBindingHandler,
			AgentSkill:            agentSkillHandler,
			AgentRun:              agentRunHandler,
			RunnerControl:         runnerControlHandler,
			RunnerBroker:          runnerBrokerHandler,
		},
		Users: routes.UserHandlers{
			User:          userHandler,
			Group:         groupHandler,
			Permission:    permissionHandler,
			PermissionSet: permissionSetHandler,
			WorkspaceRole: workspaceRoleHandler,
			Credential:    credentialHandler,
			APIToken:      apiTokenHandler,
			Agent:         agentHandler,
			CLIAuth:       handlers.NewCLIAuthHandler(repository.NewCLIAuthRepository(s.db), logger.NewAuditor(s.db), agentHandler, tokenManager, apiTokenHandler, permService),
			OAuth:         handlers.NewOAuthHandler(s.db, agentHandler, tokenManager, apiTokenHandler, permService),
		},
		Admin: routes.AdminHandlers{
			SecuritySettings: securitySettingsHandler,
			AuthPolicy:       authPolicyHandler,
			Theme:            themeHandler,
			UserPreferences:  userPreferencesHandler,
			JiraImport:       jiraImportHandler,
			Plugin:           pluginHandler,
			Setup:            setupHandler,
			System:           systemHandler,
			AuditLog:         auditLogHandler,
			LDAP:             ldapHandler,
			Features:         featuresHandler,
			OAuthClients:     handlers.NewAdminOAuthClientHandler(s.db, tokenManager, permService),
			Diagnostics: handlers.NewDiagnosticsHandler(
				repository.NewActionRepository(s.db),
				repository.NewWebhookDeliveryRepository(s.db),
				repository.NewSchedulerRunRepository(s.db),
				repository.NewFracIndexRepository(s.db),
				repository.NewAIRepository(s.db),
				llmManager,
				llmModelCache,
				logger.NewAuditor(s.db),
				repository.NewRunnerRepository(s.db),
				repository.NewAgentRunRepository(s.db),
			),
			AgentSecurity: handlers.NewAgentSecurityHandler(
				agentSecurityRepo,
				services.NewUserReadService(s.db),
				permService,
				logger.NewAuditor(s.db),
			),
		},
		Planning: routes.PlanningHandlers{
			MilestoneCategory: milestoneCategoryHandler,
			Milestone:         milestoneHandler,
			IterationType:     iterationTypeHandler,
			Iteration:         iterationHandler,
			PersonalLabel:     personalLabelHandler,
		},
		TimeTracking: routes.TimeTrackingHandlers{
			Customer:           timeCustomerHandler,
			ProjectCategory:    timeProjectCategoryHandler,
			Project:            timeProjectHandler,
			Worklog:            timeWorklogHandler,
			ActiveTimer:        activeTimerHandler,
			ProjectPermission:  timeProjectPermissionHandler,
			CustomerPermission: customerOrgPermissionHandler,
		},
		TestMgmt: routes.TestManagementHandlers{
			Folder:      testFolderHandler,
			Case:        testCaseHandler,
			Set:         testSetHandler,
			RunTemplate: testRunTemplateHandler,
			Run:         testRunHandler,
			Summary:     testSummaryHandler,
		},
		Channels: routes.ChannelHandlers{
			ChannelCategory: channelCategoryHandler,
			Channel:         channelHandler,
			Notification:    notificationHandler,
			EmailTemplate:   emailTemplateHandler,
			Webhook:         webhookHandler,
			AssetReport:     assetReportHandler,
		},
		Portal: routes.PortalHandlers{
			Portal:         portalHandler,
			PortalAuth:     portalAuthHandler,
			PortalWebAuthn: portalWebAuthnHandler,
			PortalCustomer: portalCustomersHandler,
			ContactRole:    contactRolesHandler,
			Hub:            hubHandler,
			Form:           formHandler,
		},
		Assets: routes.AssetHandlers{
			Asset:    assetHandler,
			Type:     assetTypeHandler,
			Category: assetCategoryHandler,
			Status:   assetStatusHandler,
			Action:   assetActionHandler,
		},
		PublicBoard: publicBoardHandler,
		Collections: routes.CollectionHandlers{
			Category:     collectionCategoryHandler,
			Collection:   collectionHandler,
			BoardConfig:  boardConfigHandler,
			TestCoverage: testCoverageHandler,
		},
		AI: routes.AIHandlers{
			AI:            aiHandler,
			LLMConnection: llmConnHandler,
		},
		Misc: routes.MiscHandlers{
			Homepage:      homepageHandler,
			Review:        reviewHandler,
			CalendarFeed:  calendarFeedHandler,
			CustomField:   customFieldHandler,
			RunnerInstall: handlers.NewRunnerInstallHandler(baseURL),
		},
		Teams: routes.TeamHandlers{
			Team:   teamHandler,
			Leave:  leaveHandler,
			OnCall: onCallHandler,
		},
		Integrations: routes.IntegrationHandlers{
			Provider:    integrationProviderHandler,
			OAuth:       integrationOAuthHandler,
			ItemLinks:   integrationItemLinksHandler,
			TodoistSync: todoistSyncHandler,
		},
		Pages: routes.PageHandlers{
			Page:            pageHandler,
			KnowledgeSearch: knowledgeSearchHandler,
			PageLabel:       pageLabelHandler,
		},
		Push: pushHandler,
	}
	routes.RegisterAll(routeDeps)

	// Test-only endpoint: gated on WINDSHIFT_E2E_TEST_HOOKS=1. Lets the
	// Playwright suite inject a synthetic SCM ref ActionEvent — same
	// payload shape the sync layer emits — so the create_milestone
	// action chain can be exercised end-to-end without standing up a
	// real GitHub or pushing real refs. Production never sets this env.
	if os.Getenv("WINDSHIFT_E2E_TEST_HOOKS") == "1" {
		mux.Handle("POST /api/test/scm/setup-mock-repo", handlers.NewTestSetupMockRepo(services.NewTestSCMHookService(s.db, nil)))
		mux.Handle("POST /api/test/scm/inject-ref", handlers.NewTestSCMInjectRef(services.NewTestSCMHookService(s.db, s.actionService)))
		slog.Warn("WINDSHIFT_E2E_TEST_HOOKS enabled — test hook routes are mounted; never enable in production")
	}

	// Register plugin routes
	if pluginRouter != nil {
		pluginRouter.RegisterRoutes(mux)
	}

	// REST API v1
	restapi.SetupRoutes(restapi.Deps{
		Mux:                    mux,
		DB:                     s.db,
		TokenManager:           tokenManager,
		PermissionService:      permService,
		ActionService:          s.actionService,
		AttachmentPath:         cfg.AttachmentPath,
		ItemLinkService:        itemLinkHandler.LinkService(),
		AssetPermissionService: assetHandler.AssetPermissionService(),
		AssetService:           assetHandler.AssetService(),
		CommentService:         commentService,
	}, v1.RegisterRoutes)

	// MCP Server (Model Context Protocol) — opt-in via --mcp or MCP_ENABLED=true
	if cfg.MCPEnabled {
		mcpServer := mcpserver.NewMCPServer(mcpserver.Deps{
			DB:                    s.db,
			TokenManager:          tokenManager,
			PermissionService:     permService,
			TimePermissionService: timePermissionService,
			TimerService:          timerService,
			CommentService:        commentService,
			ActionService:         s.actionService,
		})
		mux.Handle("GET /mcp", mcpServer.Handler())
		mux.Handle("POST /mcp", mcpServer.Handler())
		mux.Handle("DELETE /mcp", mcpServer.Handler())
		slog.Info("MCP server enabled", "path", "/mcp")
	}

	// Frontend files
	if cfg.FrontendFiles != (embed.FS{}) {
		distFS, err := fs.Sub(cfg.FrontendFiles, "frontend/dist")
		if err != nil {
			slog.Warn("frontend files not found, serving API only")
		} else {
			fileServer := http.FileServer(http.FS(distFS))

			// Vite emits content-hashed filenames under /_app/, so those bytes
			// never change for a given URL — cache them aggressively. The other
			// static entry points have stable filenames whose contents can change
			// between builds, so force revalidation.
			immutableAssets := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				fileServer.ServeHTTP(w, r)
			})
			revalidatingAssets := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-cache, must-revalidate")
				fileServer.ServeHTTP(w, r)
			})

			mux.Handle("GET /remoteEntry.js", revalidatingAssets)
			mux.Handle("GET /_app/", immutableAssets)
			mux.Handle("GET /windshift-3.svg", revalidatingAssets)
			mux.Handle("GET /favicon-32x32.png", revalidatingAssets)
			mux.Handle("GET /apple-touch-icon.png", revalidatingAssets)
			mux.Handle("GET /forms/widget.js", revalidatingAssets)
			mux.Handle("GET /embed/", revalidatingAssets)

			// PWA entry points. These need explicit routes (the SPA fallback at
			// "GET /" would otherwise serve index.html for them) and explicit
			// content types — Go has no registered MIME for .webmanifest, and the
			// service worker needs a root scope grant + no-cache so updates land.
			mux.Handle("GET /manifest.webmanifest", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/manifest+json")
				w.Header().Set("Cache-Control", "no-cache, must-revalidate")
				fileServer.ServeHTTP(w, r)
			}))
			mux.Handle("GET /service-worker.js", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/javascript")
				w.Header().Set("Service-Worker-Allowed", "/")
				w.Header().Set("Cache-Control", "no-cache, must-revalidate")
				fileServer.ServeHTTP(w, r)
			}))

			// Read index.html once at startup for nonce injection
			indexHTML, err := fs.ReadFile(distFS, "index.html")
			if err != nil {
				slog.Warn("could not read index.html from embedded FS", "error", err)
			}

			contextPath := cfg.ContextPath
			mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
				// Anything under an API root that hasn't matched a specific
				// route is a 404 — don't fall through to the SPA shell.
				// The prefixes must be path-segment scoped so client routes
				// like /api-docs aren't shadowed by the /api check.
				if isAPIPath(r.URL.Path) {
					http.NotFound(w, r)
					return
				}

				if indexHTML == nil {
					http.NotFound(w, r)
					return
				}

				// Inject CSP nonce into the inline theme script tag and expose the
				// externally visible context path for the SPA translation layer.
				nonce := CSPNonceFromContext(r.Context())
				html := prepareIndexHTML(indexHTML, nonce, contextPath)

				w.Header().Set("Content-Type", "text/html")
				// Force the SPA shell to revalidate on every load so that a
				// new desktop/web build is picked up without users having to
				// force-quit the Tauri WebView or hard-refresh the browser.
				w.Header().Set("Cache-Control", "no-cache, must-revalidate")
				http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(html))
			})
		}
	}

	// Maintain a small in-memory list of configured Jira instance origins so the
	// CSP `img-src` directive allows project avatars served from each tenant.
	jiraHosts := NewJiraHostAllowlist(s.db, 60*time.Second)
	go jiraHosts.Start(s.jiraHostStopChan)

	// Apply middleware (recovery is outermost to catch all panics)
	securityMiddleware := createSecurityHeaders(enableHTTPS, cfg.UseProxy, additionalProxyIPs, jiraHosts.Allowed)
	compressionMiddleware := middleware.CreateCompressionMiddleware(cfg.UseProxy)
	handler := middleware.Recovery(compressionMiddleware(securityMiddleware(mux)))
	handler = withContextPath(handler, cfg.ContextPath)

	// Create HTTP server
	s.httpServer = &http.Server{
		Handler:        handler,
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   30 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	return nil
}

func (s *Server) recoverUser(username string) {
	var id int
	var userEmail string
	var isActive bool
	err := s.db.QueryRow(
		`SELECT id, email, is_active FROM users WHERE username = ?`, username,
	).Scan(&id, &userEmail, &isActive)
	if err != nil {
		slog.Error("RECOVER_USER: user not found", "username", username)
		return
	}
	if isActive {
		slog.Info("RECOVER_USER: user is already active, no action needed", "username", username, "email", userEmail)
		return
	}
	_, err = s.db.ExecWrite(`UPDATE users SET is_active = true, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	if err != nil {
		slog.Error("RECOVER_USER: failed to re-enable user", "username", username, "error", err)
		return
	}
	slog.Warn("RECOVER_USER: re-enabled disabled user", "username", username, "email", userEmail, "id", id)
}

// Start begins listening for HTTP requests.
// This method is non-blocking; the server runs in a goroutine.
// Use Shutdown to stop the server gracefully.
func (s *Server) Start() error {
	if s.started {
		return errors.New("server already started")
	}

	// Create listener
	addr := ":" + s.config.Port
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	s.listener = listener

	// Get actual port (important for port 0)
	tcpAddr := listener.Addr().(*net.TCPAddr) //nolint:errcheck // Type assertion is safe; net.Listen("tcp", ...) always returns *net.TCPAddr
	s.actualPort = tcpAddr.Port

	enableHTTPS := s.config.TLSCertPath != "" && s.config.TLSKeyPath != ""

	if enableHTTPS {
		slog.Info("HTTPS server starting", "port", s.actualPort)
		go func() {
			if err := s.httpServer.ServeTLS(s.listener, s.config.TLSCertPath, s.config.TLSKeyPath); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("HTTPS server error", "error", err)
			}
		}()
	} else {
		slog.Info("HTTP server starting", "port", s.actualPort)
		go func() {
			if err := s.httpServer.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("HTTP server error", "error", err)
			}
		}()
	}

	s.started = true
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	// Prevent double shutdown
	if s.shuttingDown {
		return nil
	}
	s.shuttingDown = true

	slog.Info("starting graceful shutdown")

	// Stop schedulers first - use safeClose helper to avoid panics on already-closed channels
	safeClose := func(ch chan struct{}) {
		if ch != nil {
			defer func() { recover() }() //nolint:errcheck // Intentionally ignoring recover() return; used to suppress panics from closing already-closed channels
			close(ch)
		}
	}

	// Close, but do NOT nil, the stop channels: background schedulers select
	// on these fields in a loop, so the nil-write races with their reads (and
	// a select on a nil channel blocks forever, leaking the goroutine).
	// Double-close safety comes from safeClose's recover, not from nil-ing.
	safeClose(s.scmSyncStopChan)
	safeClose(s.issueSyncStopChan)
	safeClose(s.magicLinkStopChan)

	if s.cleanupTicker != nil {
		// Stop, but do NOT nil: runActivityCleanup selects on cleanupTicker.C
		// in a loop and the nil-write races with that read.
		s.cleanupTicker.Stop()
	}
	safeClose(s.cleanupStopChan)
	safeClose(s.jiraHostStopChan)

	if s.notificationScheduler != nil {
		slog.Info("stopping notification scheduler")
		s.notificationScheduler.Stop()
	}

	if s.recurrenceScheduler != nil {
		slog.Info("stopping recurrence scheduler")
		s.recurrenceScheduler.Stop()
	}

	if s.cfvCleanupScheduler != nil {
		slog.Info("stopping cfv cleanup scheduler")
		s.cfvCleanupScheduler.Stop()
	}

	if s.todoistSyncScheduler != nil {
		slog.Info("stopping todoist sync scheduler")
		s.todoistSyncScheduler.Stop()
	}

	if s.runnerLeaseReaper != nil {
		slog.Info("stopping runner lease reaper")
		s.runnerLeaseReaper.Stop()
	}

	if s.codingRunService != nil {
		slog.Info("shutting down coding-agent run service")
		// Stops admission, drains still-queued local runs as canceled, and
		// cancels in-flight runs so their workers finalize a terminal status
		// (WI-332). Bounded by the shutdown ctx like the LDAP drain below.
		if err := s.codingRunService.Shutdown(ctx); err != nil {
			slog.Warn("coding-agent run service shutdown did not drain in time", "error", err)
		}
	}

	if s.actionService != nil {
		slog.Info("stopping action service")
		s.actionService.Stop()
	}

	if s.approvalEscalationSweeper != nil {
		slog.Info("stopping approval escalation sweeper")
		s.approvalEscalationSweeper.Stop()
	}

	if s.assetActionService != nil {
		slog.Info("stopping asset action service")
		s.assetActionService.Stop()
	}

	if s.emailScheduler != nil {
		slog.Info("stopping email scheduler")
		s.emailScheduler.Stop()
	}

	if s.emailTrackingRetention != nil {
		slog.Info("stopping email tracking retention sweeper")
		s.emailTrackingRetention.Stop()
	}

	if s.briefingScheduler != nil {
		slog.Info("stopping briefing scheduler")
		s.briefingScheduler.Stop()
	}

	if s.pluginScheduleScheduler != nil {
		slog.Info("stopping plugin schedule scheduler")
		s.pluginScheduleScheduler.Stop()
	}

	if s.notificationService != nil {
		slog.Info("stopping notification service")
		_ = s.notificationService.Close()
	}

	if s.ldapHandler != nil {
		slog.Info("draining LDAP sync goroutines")
		s.ldapHandler.Stop(ctx)
	}

	// Stop HTTP server
	if s.httpServer != nil {
		s.httpServer.SetKeepAlivesEnabled(false)
		slog.Info("shutting down HTTP server")
		if err := s.httpServer.Shutdown(ctx); err != nil {
			slog.Warn("HTTP server shutdown timed out, forcing close", "error", err)
			_ = s.httpServer.Close()
		}
	}

	// Cleanup remaining resources
	s.cleanup()

	slog.Info("server shutdown complete")
	return nil
}

// isAPIPath reports whether p falls under an API root (and so should be a
// hard 404 when no specific route matched) rather than the SPA shell. The
// match is path-segment scoped — `/api-docs` is *not* an API path even
// though it starts with the four bytes `/api`.
func isAPIPath(p string) bool {
	for _, root := range []string{"/api", "/rest", "/scim"} {
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}

// cleanup releases all resources.
func (s *Server) cleanup() {
	// Stop rate limiters
	if s.loginRateLimiter != nil {
		s.loginRateLimiter.Stop()
	}
	if s.fidoRateLimiter != nil {
		s.fidoRateLimiter.Stop()
	}
	if s.authRateLimiter != nil {
		s.authRateLimiter.Stop()
	}
	if s.scimRateLimiter != nil {
		s.scimRateLimiter.Stop()
	}
	if s.portalSubmitLimiter != nil {
		s.portalSubmitLimiter.Stop()
	}
	if s.portalSearchLimiter != nil {
		s.portalSearchLimiter.Stop()
	}
	if s.emailVerifyLimiter != nil {
		s.emailVerifyLimiter.Stop()
	}
	if s.setupLimiter != nil {
		s.setupLimiter.Stop()
	}
	if s.ssoRateLimiter != nil {
		s.ssoRateLimiter.Stop()
	}
	if s.portalAuthLimiter != nil {
		s.portalAuthLimiter.Stop()
	}
	if s.oauthTokenLimiter != nil {
		s.oauthTokenLimiter.Stop()
	}
	if s.aiRateLimiter != nil {
		s.aiRateLimiter.Stop()
	}
	if s.uploadLimiter != nil {
		s.uploadLimiter.Stop()
	}
	if s.webhookLimiter != nil {
		s.webhookLimiter.Stop()
	}
	if s.searchLimiter != nil {
		s.searchLimiter.Stop()
	}
	if s.calendarFeedLimiter != nil {
		s.calendarFeedLimiter.Stop()
	}

	// Stop notification manager (flush cached notifications to DB)
	if s.notificationManager != nil {
		slog.Info("stopping notification manager")
		s.notificationManager.Stop()
	}

	// Close activity tracker
	if s.activityTracker != nil {
		_ = s.activityTracker.Close()
	}

	// Close token tracker
	if s.tokenTracker != nil {
		_ = s.tokenTracker.Close()
	}

	// Close database
	if s.db != nil {
		_ = s.db.Close()
	}
}

// BaseURL returns the server's base URL.
// deadcode-keep: called by core-tests/tests/helpers.go
func (s *Server) BaseURL() string {
	if s.actualPort == 0 {
		return fmt.Sprintf("http://localhost:%s%s", s.config.Port, s.config.ContextPath)
	}
	return fmt.Sprintf("http://localhost:%d%s", s.actualPort, s.config.ContextPath)
}

// Port returns the actual port the server is listening on.
func (s *Server) Port() int {
	return s.actualPort
}

// DB returns the database instance (for testing).
// deadcode-keep: called by core-tests/tests/helpers.go
func (s *Server) DB() database.Database {
	return s.db
}

// runActivityCleanup runs periodic activity cleanup.
func (s *Server) runActivityCleanup() {
	// Initial cleanup after 1 hour
	select {
	case <-time.After(1 * time.Hour):
		slog.Info("running initial activity cleanup")
		if err := s.activityTracker.CleanupExpiredActivities(); err != nil {
			slog.Error("failed to cleanup expired activities", "error", err)
		}
	case <-s.cleanupStopChan:
		return
	}

	// Then run daily
	for {
		select {
		case <-s.cleanupTicker.C:
			slog.Info("running scheduled activity cleanup")
			if err := s.activityTracker.CleanupExpiredActivities(); err != nil {
				slog.Error("failed to cleanup expired activities", "error", err)
			}
		case <-s.cleanupStopChan:
			return
		}
	}
}

// runMagicLinkCleanup runs periodic cleanup of expired magic link tokens.
func (s *Server) runMagicLinkCleanup(magicLinkService *services.MagicLinkService) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	slog.Info("magic link cleanup scheduler started (1-hour interval)")
	for {
		select {
		case <-ticker.C:
			if err := magicLinkService.CleanupExpiredMagicLinks(); err != nil {
				slog.Error("magic link cleanup error", "error", err)
			}
		case <-s.magicLinkStopChan:
			slog.Info("magic link cleanup scheduler stopped")
			return
		}
	}
}

// runSCMRepoSync periodically walks every active repo and upserts PR/branch
// SCM links. Runs on its own ticker so the slower runSCMLinkRefresh below
// can't push a sync tick off the end of the deadline.
func (s *Server) runSCMRepoSync(scmSyncService *scm.SyncService) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	slog.Info("SCM repo sync scheduler started (5-minute interval)")
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			if err := scmSyncService.SyncAllRepositories(ctx); err != nil {
				slog.Error("SCM sync error", "error", err)
			}
			cancel()
		case <-s.scmSyncStopChan:
			slog.Info("SCM repo sync scheduler stopped")
			return
		}
	}
}

// runSCMLinkRefresh periodically re-reads the state of every non-merged PR
// link. Runs on a slower cadence than the repo-level sync because each
// link costs one provider round-trip, and a stale "merged" badge is far
// less critical than a missed link discovery.
func (s *Server) runSCMLinkRefresh(scmSyncService *scm.SyncService) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	slog.Info("SCM PR link refresh scheduler started (15-minute interval)")
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			if err := scmSyncService.RefreshAllPRLinkStates(ctx); err != nil {
				slog.Error("PR state refresh error", "error", err)
			}
			cancel()
		case <-s.scmSyncStopChan:
			slog.Info("SCM PR link refresh scheduler stopped")
			return
		}
	}
}

// runSCMOAuthStateCleanup periodically deletes expired rows from
// scm_oauth_state. Postgres has a stored function defined for this but
// nothing in the code or schema schedules it; SQLite has a probabilistic
// AFTER INSERT trigger that fires on ~1% of inserts. A unified Go-side
// periodic covers both backends and bounds table growth on Postgres.
func (s *Server) runSCMOAuthStateCleanup() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	slog.Info("SCM OAuth state cleanup scheduler started (1-hour interval)")
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			res, err := s.db.ExecWriteContext(ctx, `DELETE FROM scm_oauth_state WHERE expires_at < CURRENT_TIMESTAMP`)
			cancel()
			if err != nil {
				slog.Error("scm_oauth_state cleanup failed", slog.Any("error", err))
				continue
			}
			if n, _ := res.RowsAffected(); n > 0 {
				slog.Debug("scm_oauth_state cleanup", slog.Int64("deleted", n))
			}
		case <-s.scmSyncStopChan:
			slog.Info("SCM OAuth state cleanup scheduler stopped")
			return
		}
	}
}

// runIssueSync runs periodic GitHub Issue synchronization.
func (s *Server) runIssueSync(issueSyncService *scm.IssueSyncService) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	slog.Info("Issue sync scheduler started (5-minute interval)")
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			if err := issueSyncService.SyncAll(ctx); err != nil {
				slog.Error("Issue sync error", "error", err)
			}
			cancel()
		case <-s.issueSyncStopChan:
			slog.Info("Issue sync scheduler stopped")
			return
		}
	}
}

// scmCredsAdapter wraps scm.CredentialResolver into the interfaces the
// coding-agent services expect, so the services layer doesn't have to
// import scm directly (which would create a cycle).
type scmCredsAdapter struct {
	cr *scm.CredentialResolver
}

// ResolveForRun implements services.SCMCredentialResolver — used by
// BindingService to embed an access token in the remote URL at run-start
// time so both `git fetch` and the agent's `git push` authenticate
// transparently. Resolution order matches what the scm.Provider would
// pick for HTTP traffic: OAuth access token → personal access token →
// GitHub App installation token (minted on demand via the App's JWT
// flow). Works for GitHub OAuth, GitHub PAT, GitHub App, Gitea OAuth,
// and Gitea PAT identically; the URL form `oauth2:<token>@host/...` is
// provider-agnostic on the git side.
func (a *scmCredsAdapter) ResolveForRun(ctx context.Context, connectionID int) (token, providerType, baseURL string, err error) {
	creds, err := a.cr.GetCredentialsByConnectionID(ctx, connectionID)
	if err != nil {
		return "", "", "", err
	}
	return a.tokenFromCreds(ctx, connectionID, creds)
}

// ResolveForRunAsUser implements the user-principal variant of
// services.SCMCredentialResolver (WI-275): credentials are resolved with
// scm.CredentialResolver.GetCredentialsForUser, so an OAuth-method
// connection yields the triggering user's personal token — or fails with
// services.ErrTriggerUserSCMNotConnected in the chain when the user has
// not connected an account (deliberately no fallback to the workspace
// credential). PAT and GitHub App connections resolve identically to
// ResolveForRun because GetCredentialsForUser falls back to the
// impersonal connection-level credential for those auth methods.
func (a *scmCredsAdapter) ResolveForRunAsUser(ctx context.Context, connectionID, userID int) (token, providerType, baseURL string, err error) {
	creds, err := a.cr.GetCredentialsForUser(ctx, connectionID, userID)
	if err != nil {
		if errors.Is(err, scm.ErrUserSCMNotConnected) {
			return "", "", "", fmt.Errorf("user %d on connection %d: %w", userID, connectionID, services.ErrTriggerUserSCMNotConnected)
		}
		return "", "", "", err
	}
	return a.tokenFromCreds(ctx, connectionID, creds)
}

// tokenFromCreds picks the git-auth token out of resolved credentials.
// Resolution order matches what the scm.Provider would pick for HTTP
// traffic: OAuth access token → personal access token → GitHub App
// installation token (minted on demand via the App's JWT flow).
func (a *scmCredsAdapter) tokenFromCreds(ctx context.Context, connectionID int, creds *scm.ProviderCredentials) (token, providerType, baseURL string, err error) {
	switch {
	case creds.OAuthAccessToken != "":
		token = creds.OAuthAccessToken
	case creds.PersonalAccessToken != "":
		token = creds.PersonalAccessToken
	case creds.GitHubAppID != "" && creds.GitHubAppPrivateKey != "" && creds.GitHubAppInstallationID != "":
		// GitHub App-backed connection: mint a short-lived installation
		// access token via the App JWT flow. scm.GitHubProvider does
		// the cryptography; we just feed it the provider config and ask
		// for a token. Note: installation tokens expire after ~1h; the
		// per-run lifetime is well within that, so we don't bother
		// caching or refreshing.
		t, terr := a.mintGitHubAppToken(ctx, creds)
		if terr != nil {
			return "", "", "", fmt.Errorf("mint GitHub App installation token: %w", terr)
		}
		token = t
	default:
		return "", "", "", fmt.Errorf("connection %d has no usable auth (no OAuth, no PAT, no complete GitHub App config)", connectionID)
	}
	return token, string(creds.ProviderType), creds.BaseURL, nil
}

// mintGitHubAppToken builds a transient scm.GitHubAppProvider from the
// stored App credentials and asks it for an installation token. Used by
// ResolveForRun for the git-CLI auth path; CreatePullRequest goes through
// the same provider already (it just calls GetInstallationAccessToken
// internally on the first authenticated request).
func (a *scmCredsAdapter) mintGitHubAppToken(ctx context.Context, creds *scm.ProviderCredentials) (string, error) {
	provider, err := scm.NewProvider(scm.ProviderConfig{
		ProviderType:            creds.ProviderType,
		AuthMethod:              creds.AuthMethod,
		BaseURL:                 creds.BaseURL,
		GitHubAppID:             creds.GitHubAppID,
		GitHubAppPrivateKey:     creds.GitHubAppPrivateKey,
		GitHubAppInstallationID: creds.GitHubAppInstallationID,
	})
	if err != nil {
		return "", fmt.Errorf("build provider: %w", err)
	}
	appProvider, ok := provider.(scm.GitHubAppProvider)
	if !ok {
		return "", fmt.Errorf("provider for connection is not a GitHubAppProvider (type %T)", provider)
	}
	installationID, err := strconv.ParseInt(creds.GitHubAppInstallationID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("parse installation id %q: %w", creds.GitHubAppInstallationID, err)
	}
	token, _, err := appProvider.GetInstallationAccessToken(ctx, installationID)
	if err != nil {
		return "", err
	}
	return token, nil
}

// openPRViaCredentialResolver implements services.OpenPRFn. Builds a
// scm.Provider for the connection, calls CreatePullRequest, and lifts
// the result into the orchestrator's OpenedPR shape. When the request
// carries a UserID (the run's triggering user, WI-275), credentials
// resolve per-user — on OAuth connections the PR is authored by that
// user; PAT / GitHub App connections resolve identically either way.
// permanentOpenPRErrors are the scm sentinel failures a retry can't fix: the
// request reached the provider and was refused (bad/expired credentials,
// forbidden, repo not found, a PR that already exists, an unsupported provider).
// Everything else — a timeout, a 5xx, a dropped connection, a rate-limit — is
// transient and left bare so AgentPRService's retry loop re-attempts it.
var permanentOpenPRErrors = []error{
	scm.ErrInvalidCredentials,
	scm.ErrNotAuthenticated,
	scm.ErrTokenExpired,
	scm.ErrRefreshTokenInvalid,
	scm.ErrForbidden,
	scm.ErrNotFound,
	scm.ErrAlreadyExists,
	scm.ErrUserSCMNotConnected,
	scm.ErrUnsupportedProvider,
}

// classifyOpenPRError wraps the scm errors that must not be retried so the
// AgentPRService retry loop surfaces them immediately; transient errors pass
// through unwrapped and stay retryable. ErrRateLimited is deliberately omitted
// from the permanent set — the retry loop's backoff is the right response to it.
func classifyOpenPRError(err error) error {
	for _, sentinel := range permanentOpenPRErrors {
		if errors.Is(err, sentinel) {
			return services.NewPermanentOpenPRError(err)
		}
	}
	return err
}

func openPRViaCredentialResolver(cr *scm.CredentialResolver) services.OpenPRFn {
	return func(ctx context.Context, req services.OpenPRRequest) (*services.OpenedPR, error) {
		var creds *scm.ProviderCredentials
		var err error
		if req.UserID > 0 {
			creds, err = cr.GetCredentialsForUser(ctx, req.ConnectionID, req.UserID)
		} else {
			creds, err = cr.GetCredentialsByConnectionID(ctx, req.ConnectionID)
		}
		if err != nil {
			return nil, fmt.Errorf("resolve connection %d: %w", req.ConnectionID, err)
		}
		provider, err := scm.NewProvider(scm.ProviderConfig{
			ProviderType:        creds.ProviderType,
			AuthMethod:          creds.AuthMethod,
			BaseURL:             creds.BaseURL,
			OAuthAccessToken:    creds.OAuthAccessToken,
			OAuthRefreshToken:   creds.OAuthRefreshToken,
			PersonalAccessToken: creds.PersonalAccessToken,
			OAuthClientID:       creds.OAuthClientID,
			OAuthClientSecret:   creds.OAuthClientSecret,
		})
		if err != nil {
			return nil, fmt.Errorf("build provider: %w", err)
		}
		pr, err := provider.CreatePullRequest(ctx, req.Owner, req.Repo, scm.CreatePROptions{
			Title:      req.Title,
			Body:       req.Body,
			HeadBranch: req.HeadBranch,
			BaseBranch: req.BaseBranch,
			Draft:      req.Draft,
		})
		if err != nil {
			return nil, classifyOpenPRError(err)
		}
		authorName := pr.Author.Username
		if authorName == "" {
			authorName = pr.Author.Name
		}
		return &services.OpenedPR{
			ID:     fmt.Sprintf("%d", pr.ID),
			Number: pr.Number,
			URL:    pr.URL,
			Title:  pr.Title,
			State:  pr.State,
			Author: authorName,
		}, nil
	}
}

// commentPRViaCredentialResolver implements services.CommentPRFn. Builds a
// scm.Provider for the connection and posts a comment on the PR via
// IssueCommentProvider.CreateIssueComment (a PR is an issue on both GitHub and Gitea).
// Credentials resolve per-user when a UserID is present (WI-275), matching the
// open-PR path. Returns an error if the provider lacks issue-comment support.
func commentPRViaCredentialResolver(cr *scm.CredentialResolver) services.CommentPRFn {
	return func(ctx context.Context, req services.PRCommentRequest) error {
		var creds *scm.ProviderCredentials
		var err error
		if req.UserID > 0 {
			creds, err = cr.GetCredentialsForUser(ctx, req.ConnectionID, req.UserID)
		} else {
			creds, err = cr.GetCredentialsByConnectionID(ctx, req.ConnectionID)
		}
		if err != nil {
			return fmt.Errorf("resolve connection %d: %w", req.ConnectionID, err)
		}
		provider, err := scm.NewProvider(scm.ProviderConfig{
			ProviderType:        creds.ProviderType,
			AuthMethod:          creds.AuthMethod,
			BaseURL:             creds.BaseURL,
			OAuthAccessToken:    creds.OAuthAccessToken,
			OAuthRefreshToken:   creds.OAuthRefreshToken,
			PersonalAccessToken: creds.PersonalAccessToken,
			OAuthClientID:       creds.OAuthClientID,
			OAuthClientSecret:   creds.OAuthClientSecret,
		})
		if err != nil {
			return fmt.Errorf("build provider: %w", err)
		}
		issues, ok := provider.(scm.IssueCommentProvider)
		if !ok {
			return fmt.Errorf("provider %s does not support issue comments", creds.ProviderType)
		}
		_, err = issues.CreateIssueComment(ctx, req.Owner, req.Repo, req.Number, req.Body)
		return err
	}
}

// itemPRContinuationResolver implements services.ItemPRContinuationResolver: it
// finds an item's most-recently-updated open linked PR and resolves its head
// branch via the SCM provider, so the @mention trigger can land commits on that
// PR instead of opening a new one. Read-only, so it resolves connection-level
// credentials (no per-user principal needed just to read a PR head).
type itemPRContinuationResolver struct {
	db database.Database
	cr *scm.CredentialResolver
}

func (r *itemPRContinuationResolver) ContinuationForItem(ctx context.Context, itemID int) (*services.ContinuationTarget, error) {
	var externalID, repoName string
	var connectionID int
	err := r.db.QueryRowContext(ctx, `
		SELECT l.external_id, wr.repository_name, wr.workspace_scm_connection_id
		FROM item_scm_links l
		JOIN workspace_repositories wr ON l.workspace_repository_id = wr.id
		WHERE l.item_id = ? AND l.link_type = 'pull_request' AND lower(l.state) = 'open'
		ORDER BY l.updated_at DESC
		LIMIT 1
	`, itemID).Scan(&externalID, &repoName, &connectionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // no open PR — caller starts a fresh run
	}
	if err != nil {
		return nil, fmt.Errorf("query open PR link: %w", err)
	}
	number, err := strconv.Atoi(externalID)
	if err != nil {
		return nil, fmt.Errorf("PR link external_id %q is not a number: %w", externalID, err)
	}
	owner, repo, ok := strings.Cut(repoName, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("repository_name %q is not owner/repo", repoName)
	}
	creds, err := r.cr.GetCredentialsByConnectionID(ctx, connectionID)
	if err != nil {
		return nil, fmt.Errorf("resolve connection %d: %w", connectionID, err)
	}
	provider, err := scm.NewProvider(scm.ProviderConfig{
		ProviderType:        creds.ProviderType,
		AuthMethod:          creds.AuthMethod,
		BaseURL:             creds.BaseURL,
		OAuthAccessToken:    creds.OAuthAccessToken,
		OAuthRefreshToken:   creds.OAuthRefreshToken,
		PersonalAccessToken: creds.PersonalAccessToken,
		OAuthClientID:       creds.OAuthClientID,
		OAuthClientSecret:   creds.OAuthClientSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("build provider: %w", err)
	}
	pr, err := provider.GetPullRequest(ctx, owner, repo, number)
	if err != nil {
		return nil, fmt.Errorf("get PR %s/%s#%d: %w", owner, repo, number, err)
	}
	// The link said open but the provider is authoritative: a since-closed/merged
	// PR is not continuable, and an empty head branch can't be checked out.
	if pr.IsMerged || strings.EqualFold(pr.State, "closed") || pr.HeadBranch == "" {
		return nil, nil
	}
	return &services.ContinuationTarget{
		PRNumber:   number,
		RepoSlug:   repoName,
		HeadBranch: pr.HeadBranch,
	}, nil
}

// bootCodingAgentRunService builds the orchestration-only WI-89 + WI-90
// RunService when cfg.CodingAgent.Enabled is set: initialPrompt is the static
// coding-agent operational prompt the remote runner hands the agent as its
// first message (per-binding suffixes append to it); the per-run token minter and
// the post-run hook that opens a draft PR (via either GitHub or Gitea,
// transparently) and writes back an item_scm_links row. The service queues
// runs, enriches remote claims (PrepareRemoteClaim), and finalizes remote
// results (FinalizeRemote) — but runs no in-process worker pool, so no agent
// executes on the orchestrator host. All runs are dispatched to remote runner
// pools (windshift-runner). Returns an error for any misconfig so the rest of
// the server still comes up with the harness disabled.
func bootCodingAgentRunService(
	db database.Database,
	tm *auth.TokenManager,
	bindings *repository.WorkspaceAgentBindingRepository,
	cr *scm.CredentialResolver,
	initialPrompt string,
) (*services.RunService, error) {
	tokens, err := services.NewRunTokenService(tm)
	if err != nil {
		return nil, fmt.Errorf("coding-agent token service: %w", err)
	}

	// PR-creation post-run hook. cr is the same CredentialResolver
	// BindingService uses for URL embedding; binding lookups go through
	// the shared bindings repo so the hook sees the exact row the
	// trigger fired on. It fires for remote runs via FinalizeRemote.
	prSvc, err := services.NewAgentPRService(services.AgentPRServiceOptions{
		Bindings:  bindings,
		OpenPR:    openPRViaCredentialResolver(cr),
		CommentPR: commentPRViaCredentialResolver(cr),
		DB:        db,
	})
	if err != nil {
		return nil, fmt.Errorf("coding-agent pr service: %w", err)
	}

	runRepo := repository.NewAgentRunRepository(db)
	// Boot reconciliation (WI-332): local runs exist only in a previous
	// process's in-memory queue, so any local run still queued/running in the
	// DB was orphaned by a crash or kill and no worker will ever pick it up
	// again. Fail them before the new service starts accepting work. (With the
	// in-process loop removed, no new local runs are created — this only clears
	// rows left over from an older build.)
	if n, recErr := runRepo.ReapOrphanedLocalRuns(context.Background(), time.Now().UTC()); recErr != nil {
		slog.Warn("coding-agent: reconcile orphaned local runs",
			slog.String("component", "coding-agent"),
			slog.Any("error", recErr),
		)
	} else if n > 0 {
		slog.Info("coding-agent: failed local runs orphaned by a previous process",
			slog.String("component", "coding-agent"),
			slog.Int("count", n),
		)
	}
	// Orchestration-only: no Runner, so NewRunService starts no worker pool.
	runSvc, err := services.NewRunService(runRepo, services.RunServiceOptions{
		Tokens:        tokens,
		PostRunHook:   prSvc,
		InitialPrompt: initialPrompt,
	})
	if err != nil {
		return nil, fmt.Errorf("coding-agent run service: %w", err)
	}
	return runSvc, nil
}
