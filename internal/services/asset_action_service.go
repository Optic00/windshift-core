package services

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/repository/actionutil"
	"windshift/internal/sanitize"
	"windshift/internal/utils"

	"uuid"
)

// AssetActionService handles asynchronous asset action execution
type AssetActionService struct {
	db     database.Database
	repo   *repository.AssetActionRepository
	config ActionServiceConfig

	// Action cache: set_id -> enabled actions
	actionCache map[int][]*models.AssetAction
	cacheMu     sync.RWMutex

	// Event processing
	eventChan chan *models.AssetActionEvent
	stopChan  chan struct{}
	wg        sync.WaitGroup

	// Dependencies
	notificationService *NotificationService
	chainStore          *ExecutionChainStore
	permissionService   *PermissionService
	assetPermChecker    AssetSetPermissionChecker
	itemCreation        *ItemCreationService

	// Statistics
	eventsProcessed int64
	actionsExecuted int64
	errors          int64
}

// AssetActionExecutionResult is the observable outcome of one action run.
// Execution failures are represented by Status; Error is reserved for
// infrastructure failures that prevent a trustworthy result from being stored.
type AssetActionExecutionResult struct {
	LogID        int                          `json:"log_id"`
	Status       models.ActionExecutionStatus `json:"status"`
	ErrorMessage string                       `json:"error,omitempty"`
}

var assetConditionFieldAliases = map[string]string{
	"title":       "asset_title",
	"description": "asset_description",
	"type_id":     "asset_type_id",
	"status_id":   "asset_status_id",
	"type_name":   "asset_type_name",
	"status_name": "asset_status_name",
}

var assetConditionFields = map[string]struct{}{
	"asset_title":       {},
	"asset_tag":         {},
	"asset_description": {},
	"asset_type_id":     {},
	"asset_status_id":   {},
	"asset_type_name":   {},
	"asset_status_name": {},
}

func canonicalAssetConditionField(fieldName string) (string, bool) {
	if canonical, ok := assetConditionFieldAliases[fieldName]; ok {
		return canonical, true
	}
	_, ok := assetConditionFields[fieldName]
	return fieldName, ok
}

// NewAssetActionService creates a new asset action service
func NewAssetActionService(db database.Database, config ActionServiceConfig, chainStore *ExecutionChainStore) *AssetActionService {
	if chainStore == nil {
		chainStore = NewExecutionChainStore()
	}
	service := &AssetActionService{
		db:           db,
		repo:         repository.NewAssetActionRepository(db),
		config:       config,
		actionCache:  make(map[int][]*models.AssetAction),
		eventChan:    make(chan *models.AssetActionEvent, config.EventBufferSize),
		stopChan:     make(chan struct{}),
		chainStore:   chainStore,
		itemCreation: NewItemCreationService(db, nil),
	}

	// Load initial cache
	if err := service.refreshActionCache(); err != nil {
		slog.Warn("failed to load initial asset action cache", slog.String("component", "asset-actions"), slog.Any("error", err))
	}

	// Start background workers
	service.wg.Add(2)
	go service.eventProcessor()
	go service.cacheRefresher()

	slog.Debug("asset action service initialized", slog.String("component", "asset-actions"))

	return service
}

// ValidateTaxonomyReferences rejects asset-action definitions that point at a
// type or status outside the action's set. Execution repeats the ownership
// checks so definitions cannot become unsafe if taxonomy data changes later.
func (as *AssetActionService) ValidateTaxonomyReferences(setID int, triggerConfig string, nodes []models.AssetActionNode) error {
	repo := repository.NewAssetRepository(as.db)
	if triggerConfig != "" {
		var config models.AssetTriggerConfig
		if err := json.Unmarshal([]byte(triggerConfig), &config); err != nil {
			return fmt.Errorf("trigger_config: %w", err)
		}
		if config.AssetTypeID != nil {
			belongs, err := repo.AssetTypeBelongsToSet(*config.AssetTypeID, setID)
			if err != nil {
				return fmt.Errorf("validate trigger asset_type_id: %w", err)
			}
			if !belongs {
				return fmt.Errorf("trigger asset_type_id %d does not belong to asset set %d", *config.AssetTypeID, setID)
			}
		}
		for field, statusID := range map[string]*int{
			"from_status_id": config.FromStatusID,
			"to_status_id":   config.ToStatusID,
		} {
			if statusID == nil {
				continue
			}
			belongs, err := repo.StatusBelongsToSet(*statusID, setID)
			if err != nil {
				return fmt.Errorf("validate trigger %s: %w", field, err)
			}
			if !belongs {
				return fmt.Errorf("trigger %s %d does not belong to asset set %d", field, *statusID, setID)
			}
		}
	}
	for i, node := range nodes {
		switch node.NodeType {
		case models.AssetNodeSetStatus:
			var config models.SetStatusNodeConfig
			if err := json.Unmarshal([]byte(node.NodeConfig), &config); err != nil {
				return fmt.Errorf("nodes[%d].node_config: %w", i, err)
			}
			if config.StatusID <= 0 {
				return fmt.Errorf("nodes[%d].node_config.status_id must be positive", i)
			}
			belongs, err := repo.StatusBelongsToSet(config.StatusID, setID)
			if err != nil {
				return fmt.Errorf("validate nodes[%d] status_id: %w", i, err)
			}
			if !belongs {
				return fmt.Errorf("nodes[%d].node_config.status_id %d does not belong to asset set %d", i, config.StatusID, setID)
			}
		case models.AssetNodeCondition:
			var config models.ConditionNodeConfig
			if err := json.Unmarshal([]byte(node.NodeConfig), &config); err != nil {
				return fmt.Errorf("nodes[%d].node_config: %w", i, err)
			}
			if _, ok := canonicalAssetConditionField(config.FieldName); !ok {
				return fmt.Errorf("nodes[%d].node_config.field_name %q is not a supported asset condition field", i, config.FieldName)
			}
		}
	}
	return nil
}

// SetNotificationService sets the notification service for notify_user actions
func (as *AssetActionService) SetNotificationService(ns *NotificationService) {
	as.notificationService = ns
}

// SetEventCoordinator routes action-driven item creation through the shared
// event pipeline.
func (as *AssetActionService) SetEventCoordinator(ec *EventCoordinator) {
	if as.itemCreation != nil {
		as.itemCreation.SetEmitter(ec)
	}
}

// SetPermissionService wires workspace RBAC for create_item nodes.
func (as *AssetActionService) SetPermissionService(ps *PermissionService) {
	as.permissionService = ps
	if as.itemCreation != nil {
		as.itemCreation.SetPermissionService(ps)
	}
}

// SetItemCreationService shares the canonical item creation pipeline with
// asset action execution.
func (as *AssetActionService) SetItemCreationService(service *ItemCreationService) {
	if service != nil {
		as.itemCreation = service
	}
}

// SetAssetPermissionChecker wires asset-set RBAC for mutating asset nodes and
// recipient visibility checks.
func (as *AssetActionService) SetAssetPermissionChecker(checker AssetSetPermissionChecker) {
	as.assetPermChecker = checker
}

// EmitAssetActionEvent sends an event to be processed asynchronously (non-blocking)
func (as *AssetActionService) EmitAssetActionEvent(event *models.AssetActionEvent) {
	slog.Debug("queuing asset action event",
		slog.String("component", "asset-actions"),
		slog.String("event_type", string(event.EventType)),
		slog.Int("set_id", event.SetID),
		slog.Int("asset_id", event.AssetID),
	)

	select {
	case as.eventChan <- event:
	default:
		slog.Warn("asset action event channel full, dropping event",
			slog.String("component", "asset-actions"),
			slog.String("event_type", string(event.EventType)),
			slog.Int("set_id", event.SetID),
		)
		atomic.AddInt64(&as.errors, 1)
	}
}

// ProcessImportedAssetEvent executes an import event synchronously. Imports can
// produce events faster than the ordinary bounded async queue can drain; using
// synchronous processing applies backpressure instead of silently dropping
// asset-created events when a large CSV is imported.
func (as *AssetActionService) ProcessImportedAssetEvent(event *models.AssetActionEvent) error {
	return as.processEvent(event)
}

// isActionStillEnabled returns true if the action is present in the enabled-actions
// cache for its set. Used to short-circuit events that were queued before the
// user disabled the action.
func (as *AssetActionService) isActionStillEnabled(setID, actionID int) bool {
	as.cacheMu.RLock()
	defer as.cacheMu.RUnlock()
	for _, a := range as.actionCache[setID] {
		if a.ID == actionID {
			return true
		}
	}
	return false
}

// InvalidateSetCache invalidates the cache for a specific asset set
func (as *AssetActionService) InvalidateSetCache(setID int) {
	actions, err := as.repo.ListEnabledBySet(setID)
	if err != nil {
		slog.Error("failed to reload actions for asset set",
			slog.String("component", "asset-actions"),
			slog.Int("set_id", setID),
			slog.Any("error", err),
		)
		return
	}

	as.cacheMu.Lock()
	if len(actions) > 0 {
		as.actionCache[setID] = actions
	} else {
		delete(as.actionCache, setID)
	}
	as.cacheMu.Unlock()
}

// Stop gracefully shuts down the asset action service
func (as *AssetActionService) Stop() {
	close(as.stopChan)

	done := make(chan struct{})
	go func() {
		as.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Debug("asset action service stopped successfully", slog.String("component", "asset-actions"))
	case <-time.After(3 * time.Second):
		slog.Warn("asset action service stop timed out after 3s", slog.String("component", "asset-actions"))
	}
}

func (as *AssetActionService) eventProcessor() {
	defer as.wg.Done()

	for {
		select {
		case event := <-as.eventChan:
			if err := as.processEvent(event); err != nil {
				slog.Error("failed to process asset action event",
					slog.String("component", "asset-actions"),
					slog.String("event_type", string(event.EventType)),
					slog.Any("error", err),
				)
				atomic.AddInt64(&as.errors, 1)
			} else {
				atomic.AddInt64(&as.eventsProcessed, 1)
			}
		case <-as.stopChan:
			slog.Debug("stopping asset action event processor", slog.String("component", "asset-actions"))
			for len(as.eventChan) > 0 {
				event := <-as.eventChan
				if err := as.processEvent(event); err != nil {
					slog.Error("failed to process asset action event during shutdown",
						slog.String("component", "asset-actions"),
						slog.Any("error", err),
					)
				}
			}
			return
		}
	}
}

func (as *AssetActionService) cacheRefresher() {
	defer as.wg.Done()

	ticker := time.NewTicker(as.config.RefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := as.refreshActionCache(); err != nil {
				slog.Error("failed to refresh asset action cache", slog.String("component", "asset-actions"), slog.Any("error", err))
			}
			as.chainStore.Cleanup()
		case <-as.stopChan:
			return
		}
	}
}

func (as *AssetActionService) refreshActionCache() error {
	rows, err := as.db.Query(`SELECT DISTINCT set_id FROM asset_actions WHERE is_enabled = true`)
	if err != nil {
		return fmt.Errorf("failed to query sets with asset actions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	newCache := make(map[int][]*models.AssetAction)
	var setIDs []int

	for rows.Next() {
		var setID int
		if err := rows.Scan(&setID); err != nil {
			continue
		}
		setIDs = append(setIDs, setID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate sets with asset actions: %w", err)
	}

	for _, setID := range setIDs {
		actions, err := as.repo.ListEnabledBySet(setID)
		if err != nil {
			slog.Error("failed to load asset actions for set",
				slog.String("component", "asset-actions"),
				slog.Int("set_id", setID),
				slog.Any("error", err),
			)
			continue
		}
		newCache[setID] = actions
	}

	as.cacheMu.Lock()
	as.actionCache = newCache
	as.cacheMu.Unlock()

	return nil
}

func (as *AssetActionService) processEvent(event *models.AssetActionEvent) error { //nolint:unparam // error kept for interface consistency
	slog.Debug("processing asset action event",
		slog.String("component", "asset-actions"),
		slog.String("event_type", string(event.EventType)),
		slog.Int("set_id", event.SetID),
		slog.Int("asset_id", event.AssetID),
		slog.Bool("triggered_by_action", event.TriggeredByAction),
		slog.Int("cascade_depth", event.CascadeDepth),
	)

	// Check cascade depth limit
	if event.CascadeDepth >= MaxCascadeDepth {
		slog.Warn("asset action execution depth limit reached",
			slog.String("component", "asset-actions"),
			slog.String("chain_id", event.ExecutionChainID),
			slog.Int("depth", event.CascadeDepth),
		)
		return nil
	}

	// Get chain state for cycle detection
	var chain *ExecutionChain
	if event.ExecutionChainID != "" {
		chain = as.chainStore.GetChain(event.ExecutionChainID)
	}

	// Get actions for this set from cache
	as.cacheMu.RLock()
	actions := as.actionCache[event.SetID]
	as.cacheMu.RUnlock()

	if len(actions) == 0 {
		return nil
	}

	for _, action := range actions {
		// Cycle detection
		actionKey := fmt.Sprintf("asset:%d", action.ID)
		if chain != nil && chain.HasExecuted(actionKey) {
			slog.Debug("skipping asset action - already executed in chain",
				slog.String("component", "asset-actions"),
				slog.Int("action_id", action.ID),
				slog.String("chain_id", event.ExecutionChainID),
			)
			continue
		}

		if as.matchesTrigger(action, event) {
			// Re-check enablement against the live cache: an earlier event in
			// this batch may have been queued before the user disabled the
			// action. InvalidateSetCache rewrites actionCache[setID] to only
			// enabled actions, so absence == disabled.
			if !as.isActionStillEnabled(event.SetID, action.ID) {
				slog.Debug("skipping asset action - disabled mid-queue",
					slog.String("component", "asset-actions"),
					slog.Int("action_id", action.ID),
				)
				continue
			}
			result, err := as.executeActionWithResult(action, event, chain)
			switch {
			case err != nil:
				slog.Error("failed to execute asset action",
					slog.String("component", "asset-actions"),
					slog.Int("action_id", action.ID),
					slog.Any("error", err),
				)
				atomic.AddInt64(&as.errors, 1)
			case result.Status == models.ActionStatusFailed:
				atomic.AddInt64(&as.errors, 1)
			case result.Status == models.ActionStatusCompleted:
				atomic.AddInt64(&as.actionsExecuted, 1)
			}
		}
	}

	return nil
}

func (as *AssetActionService) matchesTrigger(action *models.AssetAction, event *models.AssetActionEvent) bool {
	if action.TriggerType != event.EventType {
		return false
	}

	var config models.AssetTriggerConfig
	if action.TriggerConfig != "" {
		if err := json.Unmarshal([]byte(action.TriggerConfig), &config); err != nil {
			slog.Warn("failed to parse asset trigger config",
				slog.String("component", "asset-actions"),
				slog.Int("action_id", action.ID),
				slog.Any("error", err),
			)
			return false
		}
	}

	// Cascade control
	if event.TriggeredByAction && !config.RespondToCascades {
		return false
	}

	if action.TriggerConfig == "" {
		return true
	}

	if config.AssetTypeID != nil {
		assetTypeID := utils.InterfaceToIntPtr(event.NewValues["asset_type_id"])
		if assetTypeID == nil {
			assetTypeID = utils.InterfaceToIntPtr(event.OldValues["asset_type_id"])
		}
		if assetTypeID == nil {
			var typeID int
			if err := as.db.QueryRow(`SELECT asset_type_id FROM assets WHERE id = ?`, event.AssetID).Scan(&typeID); err != nil {
				return false
			}
			assetTypeID = &typeID
		}
		if *assetTypeID != *config.AssetTypeID {
			return false
		}
	}

	if event.EventType == models.AssetTriggerAssetStatusChanged {
		if config.FromStatusID != nil {
			oldStatusID := utils.InterfaceToIntPtr(event.OldValues["status_id"])
			if oldStatusID == nil || *oldStatusID != *config.FromStatusID {
				return false
			}
		}
		if config.ToStatusID != nil {
			newStatusID := utils.InterfaceToIntPtr(event.NewValues["status_id"])
			if newStatusID == nil || *newStatusID != *config.ToStatusID {
				return false
			}
		}
	}

	return true
}

//nolint:unused // retained as the error-only compatibility surface for white-box callers
func (as *AssetActionService) executeAction(action *models.AssetAction, event *models.AssetActionEvent, chain *ExecutionChain) error {
	_, err := as.executeActionWithResult(action, event, chain)
	return err
}

func (as *AssetActionService) executeActionWithResult(action *models.AssetAction, event *models.AssetActionEvent, chain *ExecutionChain) (*AssetActionExecutionResult, error) {
	if action == nil {
		return nil, fmt.Errorf("asset action is required")
	}
	if !action.IsEnabled {
		return nil, fmt.Errorf("asset action %d is disabled", action.ID)
	}
	if event == nil {
		return nil, fmt.Errorf("asset action event is required")
	}
	startTime := time.Now()

	// Get or create execution chain
	chainID := event.ExecutionChainID
	if chainID == "" {
		chainID = uuid.New().String()
		chain = as.chainStore.CreateChain(chainID)
	} else if chain == nil {
		chain = as.chainStore.CreateChain(chainID)
	}

	// Mark this action as executed
	actionKey := fmt.Sprintf("asset:%d", action.ID)
	chain.MarkExecuted(actionKey)

	// Create execution log
	log := &models.AssetActionExecutionLog{
		ActionID:     action.ID,
		AssetID:      &event.AssetID,
		TriggerEvent: string(event.EventType),
		Status:       models.ActionStatusRunning,
		StartedAt:    startTime,
	}
	logID, err := as.repo.CreateExecutionLog(log)
	if err != nil {
		return nil, fmt.Errorf("create asset action execution log: %w", err)
	}
	log.ID = logID

	// Build execution context
	ctx := &models.AssetActionExecutionContext{
		Action:      action,
		Event:       event,
		Variables:   make(map[string]any),
		StepResults: []models.StepResult{},
		ChainID:     chainID,
	}

	// Populate initial variables
	ctx.Variables["asset_id"] = event.AssetID
	ctx.Variables["set_id"] = event.SetID
	ctx.Variables["actor_user_id"] = event.ActorUserID

	// Load asset data for variable substitution
	as.loadAssetVariables(ctx)

	for k, v := range event.OldValues {
		ctx.Variables["old_"+k] = v
	}
	for k, v := range event.NewValues {
		ctx.Variables["new_"+k] = v
	}

	// Topological sort
	sortedNodes, err := as.topologicalSort(action.Nodes, action.Edges)
	if err != nil {
		log.Status = models.ActionStatusFailed
		log.ErrorMessage = fmt.Sprintf("failed to sort nodes: %v", err)
		completedAt := time.Now()
		log.CompletedAt = &completedAt
		if logErr := as.repo.UpdateExecutionLog(log); logErr != nil {
			slog.Error("failed to update asset execution log", slog.Any("error", logErr))
		}
		return &AssetActionExecutionResult{
			LogID:        log.ID,
			Status:       log.Status,
			ErrorMessage: log.ErrorMessage,
		}, fmt.Errorf("failed to topologically sort nodes: %w", err)
	}

	// Execute nodes
	executedNodes := make(map[int]bool)
	for _, node := range sortedNodes {
		if node.NodeType == models.AssetNodeTrigger {
			executedNodes[node.ID] = true
			continue
		}

		canExecute := as.canExecuteNode(node.ID, action.Edges, executedNodes, ctx)
		if !canExecute {
			continue
		}

		stepResult := models.StepResult{
			NodeID:    node.ID,
			NodeType:  models.ActionNodeType(node.NodeType),
			Status:    models.ActionStatusRunning,
			StartedAt: time.Now(),
		}

		err := as.executeNode(&node, ctx, &stepResult)
		completedAt := time.Now()
		stepResult.CompletedAt = &completedAt

		if err != nil {
			stepResult.Status = models.ActionStatusFailed
			stepResult.ErrorMessage = err.Error()
			ctx.StepResults = append(ctx.StepResults, stepResult)
			slog.Warn("asset action node execution failed",
				slog.String("component", "asset-actions"),
				slog.Int("node_id", node.ID),
				slog.String("node_type", string(node.NodeType)),
				slog.Any("error", err),
			)
		} else {
			stepResult.Status = models.ActionStatusCompleted
			ctx.StepResults = append(ctx.StepResults, stepResult)
			executedNodes[node.ID] = true
		}
	}

	// Update execution log
	log.CompletedAt, log.Status, log.ErrorMessage, log.ExecutionTrace = actionutil.FinalizeExecutionLog(ctx.StepResults)
	if len(ctx.StepResults) == 0 {
		log.Status = models.ActionStatusSkipped
	}

	if logErr := as.repo.UpdateExecutionLog(log); logErr != nil {
		return nil, fmt.Errorf("update asset action execution log: %w", logErr)
	}

	return &AssetActionExecutionResult{
		LogID:        log.ID,
		Status:       log.Status,
		ErrorMessage: log.ErrorMessage,
	}, nil
}

func (as *AssetActionService) loadAssetVariables(ctx *models.AssetActionExecutionContext) {
	var title, assetTag, description sql.NullString
	var assetTypeID, statusID sql.NullInt64
	var typeName, statusName sql.NullString

	err := as.db.QueryRow(`
		SELECT a.title, a.asset_tag, a.description, a.asset_type_id, a.status_id,
		       COALESCE(t.name, ''), COALESCE(s.name, '')
		FROM assets a
		LEFT JOIN asset_types t ON a.asset_type_id = t.id
		LEFT JOIN asset_statuses s ON a.status_id = s.id
		WHERE a.id = ?
	`, ctx.Event.AssetID).Scan(&title, &assetTag, &description, &assetTypeID, &statusID, &typeName, &statusName)
	if err != nil {
		slog.Debug("failed to load asset for variables",
			slog.String("component", "asset-actions"),
			slog.Int("asset_id", ctx.Event.AssetID),
			slog.Any("error", err),
		)
		return
	}

	if title.Valid {
		ctx.Variables["asset_title"] = title.String
	}
	if assetTag.Valid {
		ctx.Variables["asset_tag"] = assetTag.String
	}
	if description.Valid {
		ctx.Variables["asset_description"] = description.String
	}
	if assetTypeID.Valid {
		ctx.Variables["asset_type_id"] = int(assetTypeID.Int64)
	}
	if statusID.Valid {
		ctx.Variables["asset_status_id"] = int(statusID.Int64)
	}
	if typeName.Valid {
		ctx.Variables["asset_type_name"] = typeName.String
	}
	if statusName.Valid {
		ctx.Variables["asset_status_name"] = statusName.String
	}
}

func (as *AssetActionService) executeNode(node *models.AssetActionNode, ctx *models.AssetActionExecutionContext, stepResult *models.StepResult) error {
	switch node.NodeType {
	case models.AssetNodeCreateItem:
		return as.executeCreateItem(node, ctx, stepResult)
	case models.AssetNodeSetField:
		if err := as.authorizeAssetActionMutation(ctx.Event.ActorUserID, ctx.Event.SetID, AssetPermissionKeyEdit); err != nil {
			return err
		}
		return as.executeSetField(node, ctx, stepResult)
	case models.AssetNodeSetStatus:
		if err := as.authorizeAssetActionMutation(ctx.Event.ActorUserID, ctx.Event.SetID, AssetPermissionKeyEdit); err != nil {
			return err
		}
		return as.executeSetStatus(node, ctx, stepResult)
	case models.AssetNodeCondition:
		return as.executeCondition(node, ctx, stepResult)
	case models.AssetNodeNotifyUser:
		return as.executeNotifyUser(node, ctx, stepResult)
	default:
		return fmt.Errorf("unknown asset action node type: %s", node.NodeType)
	}
}

// executeCreateItem creates a work item from an asset action
func (as *AssetActionService) executeCreateItem(node *models.AssetActionNode, ctx *models.AssetActionExecutionContext, stepResult *models.StepResult) error {
	var config models.CreateItemNodeConfig
	if err := json.Unmarshal([]byte(node.NodeConfig), &config); err != nil {
		return fmt.Errorf("failed to parse create_item config: %w", err)
	}
	if err := as.authorizeAssetActionWorkspaceMutation(ctx.Event.ActorUserID, config.WorkspaceID, models.PermissionItemCreate); err != nil {
		return err
	}

	title := as.substituteVariables(config.Title, ctx)
	if title == "" {
		title = "Asset Action: " + fmt.Sprintf("%v", ctx.Variables["asset_title"])
	}
	description := as.substituteVariables(config.Description, ctx)
	sanitize.ApplyAll(
		sanitize.Pair{Target: &title, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &description, Policy: sanitize.RichText},
	)
	if title == "" {
		return fmt.Errorf("create_item title is empty after sanitization")
	}

	if as.itemCreation == nil {
		return fmt.Errorf("item creation application service not configured")
	}
	creatorID := ctx.Event.ActorUserID
	itemTypeID := config.ItemTypeID
	result, err := as.itemCreation.CreateWithContext(creatorID, "", ItemCreateInput{
		WorkspaceID: config.WorkspaceID,
		Title:       title,
		Description: description,
		ItemTypeID:  &itemTypeID,
	}, ActionContext{
		TriggeredByAction: true,
		ExecutionChainID:  ctx.ChainID,
		CascadeDepth:      ctx.Event.CascadeDepth + 1,
		SourceApplication: "asset",
	})
	if err != nil {
		return fmt.Errorf("failed to create item: %w", err)
	}
	itemID := result.Item.ID

	stepResult.Output = map[string]any{
		"item_id":      itemID,
		"title":        title,
		"workspace_id": config.WorkspaceID,
	}

	return nil
}

func (as *AssetActionService) authorizeAssetActionMutation(actorUserID, setID int, permissionKey string) error {
	if actorUserID <= 0 {
		return fmt.Errorf("asset action mutation requires an identified actor (set %d)", setID)
	}
	if as.assetPermChecker == nil {
		return fmt.Errorf("asset action mutation blocked: asset permission checker not configured")
	}
	allowed, err := as.assetPermChecker.HasAssetSetPermission(actorUserID, setID, permissionKey)
	if err != nil {
		return fmt.Errorf("failed to check asset set %d permission: %w", setID, err)
	}
	if !allowed {
		return fmt.Errorf("user %d not authorized (%s) on asset set %d", actorUserID, permissionKey, setID)
	}
	return nil
}

func (as *AssetActionService) authorizeAssetActionWorkspaceMutation(actorUserID, workspaceID int, permissionKey string) error {
	if actorUserID <= 0 {
		return fmt.Errorf("asset action workspace mutation requires an identified actor (workspace %d)", workspaceID)
	}
	if as.permissionService == nil {
		return fmt.Errorf("asset action workspace mutation blocked: permission service not configured")
	}
	allowed, err := as.permissionService.HasWorkspacePermission(actorUserID, workspaceID, permissionKey)
	if err != nil {
		return fmt.Errorf("failed to check workspace %d permission: %w", workspaceID, err)
	}
	if !allowed {
		return fmt.Errorf("user %d not authorized (%s) on workspace %d", actorUserID, permissionKey, workspaceID)
	}
	return nil
}

// executeSetField updates an asset's built-in field or custom_field_values
func (as *AssetActionService) executeSetField(node *models.AssetActionNode, ctx *models.AssetActionExecutionContext, stepResult *models.StepResult) error {
	var config models.SetFieldNodeConfig
	if err := json.Unmarshal([]byte(node.NodeConfig), &config); err != nil {
		return fmt.Errorf("failed to parse set_field config: %w", err)
	}

	value := as.substituteVariables(config.Value, ctx)
	value = sanitizeAssetBuiltinFieldValue(config.FieldName, value)

	assetRepo := repository.NewAssetRepository(as.db)
	row, err := assetRepo.FindAssetFullByID(ctx.Event.AssetID)
	if err != nil {
		return fmt.Errorf("load asset for set_field: %w", err)
	}
	current := repository.AssetRowToModel(*row)
	if current.SetID != ctx.Event.SetID {
		return fmt.Errorf("asset %d does not belong to set %d", ctx.Event.AssetID, ctx.Event.SetID)
	}

	var oldValue, newValue any
	patch := AssetMutationPatch{}
	switch config.FieldName {
	case "title":
		oldValue = current.Title
		patch.Title = &value
	case "asset_tag":
		oldValue = current.AssetTag
		patch.AssetTag = &value
	case "description":
		oldValue = current.Description
		patch.Description = &value
	default:
		customFields := make(map[string]any, len(current.CustomFieldValues)+1)
		for key, currentValue := range current.CustomFieldValues {
			customFields[key] = currentValue
		}
		oldValue = customFields[config.FieldName]
		customFields[config.FieldName] = value
		patch.CustomFieldValues = customFields
	}

	assetService := NewAssetService(as.db, assetRepo)
	assetService.SetActionService(as)
	updated, err := assetService.MutateAsset(
		automationAuditActor(as.db, ctx.Event.ActorUserID, "asset_action"),
		ctx.Event.AssetID,
		patch,
		AssetAutomationContext{
			TriggeredByAction: true,
			ExecutionChainID:  ctx.ChainID,
			CascadeDepth:      ctx.Event.CascadeDepth + 1,
			SourceApplication: "asset",
		},
	)
	if err != nil {
		return fmt.Errorf("mutate asset field %q: %w", config.FieldName, err)
	}
	switch config.FieldName {
	case "title":
		newValue = updated.Title
	case "asset_tag":
		newValue = updated.AssetTag
	case "description":
		newValue = updated.Description
	default:
		newValue = updated.CustomFieldValues[config.FieldName]
	}
	stepResult.Output = map[string]any{
		"field_name": config.FieldName,
		"old_value":  oldValue,
		"new_value":  newValue,
	}
	return nil
}

// sanitizeAssetBuiltinFieldValue routes a single built-in asset column value
// through sanitizeAssetText — the same choke point CreateAsset/UpdateAsset
// use — so the per-column policies (PlainTextField for title, RichText for
// description, ShortIdentifier for asset_tag) stay defined in exactly one
// place. Non built-in field names pass through unchanged; custom-field
// values get the asset CF text pass instead.
func sanitizeAssetBuiltinFieldValue(fieldName, value string) string {
	var title, description, assetTag string
	switch fieldName {
	case "title":
		title = value
	case "description":
		description = value
	case "asset_tag":
		assetTag = value
	default:
		return value
	}
	sanitizeAssetText(&title, &description, &assetTag)
	switch fieldName {
	case "title":
		return title
	case "description":
		return description
	default:
		return assetTag
	}
}

// executeSetStatus updates an asset's status_id
func (as *AssetActionService) executeSetStatus(node *models.AssetActionNode, ctx *models.AssetActionExecutionContext, stepResult *models.StepResult) error {
	var config models.SetStatusNodeConfig
	if err := json.Unmarshal([]byte(node.NodeConfig), &config); err != nil {
		return fmt.Errorf("failed to parse set_status config: %w", err)
	}
	if config.StatusID <= 0 {
		return fmt.Errorf("set_status requires a positive status_id")
	}
	assetRepo := repository.NewAssetRepository(as.db)
	row, err := assetRepo.FindAssetFullByID(ctx.Event.AssetID)
	if err != nil {
		return fmt.Errorf("load asset for set_status: %w", err)
	}
	current := repository.AssetRowToModel(*row)
	if current.SetID != ctx.Event.SetID {
		return fmt.Errorf("asset %d does not belong to set %d", ctx.Event.AssetID, ctx.Event.SetID)
	}
	oldStatusID := 0
	if current.StatusID != nil {
		oldStatusID = *current.StatusID
	}
	assetService := NewAssetService(as.db, assetRepo)
	assetService.SetActionService(as)
	if _, err := assetService.MutateAsset(
		automationAuditActor(as.db, ctx.Event.ActorUserID, "asset_action"),
		ctx.Event.AssetID,
		AssetMutationPatch{StatusID: &config.StatusID},
		AssetAutomationContext{
			TriggeredByAction: true,
			ExecutionChainID:  ctx.ChainID,
			CascadeDepth:      ctx.Event.CascadeDepth + 1,
			SourceApplication: "asset",
		},
	); err != nil {
		return fmt.Errorf("mutate asset status: %w", err)
	}

	stepResult.Output = map[string]any{
		"old_status_id": oldStatusID,
		"new_status_id": config.StatusID,
	}
	return nil
}

// executeCondition evaluates a condition on an asset field
func (as *AssetActionService) executeCondition(node *models.AssetActionNode, ctx *models.AssetActionExecutionContext, stepResult *models.StepResult) error {
	var config models.ConditionNodeConfig
	if err := json.Unmarshal([]byte(node.NodeConfig), &config); err != nil {
		return fmt.Errorf("failed to parse condition config: %w", err)
	}

	canonicalField, ok := canonicalAssetConditionField(config.FieldName)
	if !ok {
		return fmt.Errorf("unsupported asset condition field %q", config.FieldName)
	}

	fieldValue := ctx.Variables[canonicalField]
	if fieldValue == nil {
		fieldValue = ctx.Variables["new_"+canonicalField]
	}
	if fieldValue == nil && canonicalField != config.FieldName {
		fieldValue = ctx.Variables["new_"+config.FieldName]
	}

	result := evaluateCondition(fieldValue, config.Operator, config.Value)

	stepResult.Output = map[string]any{
		"condition_result": result,
		"field_name":       canonicalField,
		"field_value":      fieldValue,
		"operator":         config.Operator,
		"compare_value":    config.Value,
	}

	return nil
}

// executeNotifyUser sends notifications
func (as *AssetActionService) executeNotifyUser(node *models.AssetActionNode, ctx *models.AssetActionExecutionContext, stepResult *models.StepResult) error {
	if as.notificationService == nil {
		return fmt.Errorf("notify_user: notification service not configured")
	}

	var config models.NotifyUserNodeConfig
	if err := json.Unmarshal([]byte(node.NodeConfig), &config); err != nil {
		return fmt.Errorf("failed to parse notify_user config: %w", err)
	}

	recipients := config.Recipients
	if len(recipients) == 0 && config.RecipientType != "" {
		recipients = []string{config.RecipientType}
	}
	userIDs := []int{}
	for _, recipient := range recipients {
		if id, err := strconv.Atoi(recipient); err == nil && id > 0 {
			userIDs = append(userIDs, id)
		}
	}

	message := as.substituteVariables(config.Message, ctx)
	title := as.substituteVariables(config.Title, ctx)

	deliveredUserIDs, err := as.notificationService.NotifyUsersForAsset(
		userIDs,
		ctx.Event.SetID,
		ctx.Event.AssetID,
		ctx.Event.ActorUserID,
		"action",
		title,
		message,
		config.IncludeLink,
		as.assetPermChecker,
	)
	if err != nil {
		return fmt.Errorf("notify_user failed: %w", err)
	}

	stepResult.Output = map[string]any{
		"recipient_count": len(deliveredUserIDs),
		"recipient_ids":   deliveredUserIDs,
		"title":           title,
		"message":         message,
	}

	return nil
}

// substituteVariables replaces {{variable}} placeholders with actual values
func (as *AssetActionService) substituteVariables(template string, ctx *models.AssetActionExecutionContext) string {
	re := regexp.MustCompile(`\{\{([^}]+)\}\}`)

	return re.ReplaceAllStringFunc(template, func(match string) string {
		varName := strings.TrimPrefix(strings.TrimSuffix(match, "}}"), "{{")
		varName = strings.TrimSpace(varName)

		parts := strings.Split(varName, ".")
		if len(parts) == 2 {
			switch parts[0] {
			case "asset":
				key := "asset_" + parts[1]
				if val, ok := ctx.Variables[key]; ok {
					return fmt.Sprintf("%v", val)
				}
			case "actor":
				if parts[1] == "id" {
					return strconv.Itoa(ctx.Event.ActorUserID)
				}
			}
		}

		if val, ok := ctx.Variables[varName]; ok {
			return fmt.Sprintf("%v", val)
		}

		return match
	})
}

// Shared topology and execution helpers

func (as *AssetActionService) topologicalSort(nodes []models.AssetActionNode, edges []models.AssetActionEdge) ([]models.AssetActionNode, error) {
	return actionutil.TopologicalSort(nodes, edges)
}

func (as *AssetActionService) canExecuteNode(nodeID int, edges []models.AssetActionEdge, executedNodes map[int]bool, ctx *models.AssetActionExecutionContext) bool {
	return actionutil.CanExecuteNodeTyped(nodeID, edges, executedNodes, ctx.StepResults)
}

// evaluateCondition evaluates a condition (reused from workspace action service)
func evaluateCondition(value any, operator, compareValue string) bool {
	strValue := fmt.Sprintf("%v", value)

	switch operator {
	case "eq", "==", "equals":
		return strValue == compareValue
	case "ne", "!=", "not_equals":
		return strValue != compareValue
	case "contains":
		return strings.Contains(strValue, compareValue)
	case "not_contains":
		return !strings.Contains(strValue, compareValue)
	case "starts_with":
		return strings.HasPrefix(strValue, compareValue)
	case "ends_with":
		return strings.HasSuffix(strValue, compareValue)
	case "gt", ">":
		if numVal, err := strconv.ParseFloat(strValue, 64); err == nil {
			if numCompare, err := strconv.ParseFloat(compareValue, 64); err == nil {
				return numVal > numCompare
			}
		}
		return strValue > compareValue
	case "lt", "<":
		if numVal, err := strconv.ParseFloat(strValue, 64); err == nil {
			if numCompare, err := strconv.ParseFloat(compareValue, 64); err == nil {
				return numVal < numCompare
			}
		}
		return strValue < compareValue
	case "is_empty":
		return strValue == "" || strValue == "null" || strValue == "<nil>"
	case "is_not_empty":
		return strValue != "" && strValue != "null" && strValue != "<nil>"
	default:
		return false
	}
}

// ExecuteActionManually executes an asset action manually for a given asset.
func (as *AssetActionService) ExecuteActionManually(action *models.AssetAction, assetID, actorUserID int) error {
	_, err := as.ExecuteActionManuallyWithResult(action, assetID, actorUserID)
	return err
}

// ExecuteActionManuallyWithResult executes an asset action manually and
// returns the same final status persisted in its execution log.
func (as *AssetActionService) ExecuteActionManuallyWithResult(action *models.AssetAction, assetID, actorUserID int) (*AssetActionExecutionResult, error) {
	event := &models.AssetActionEvent{
		EventType:         models.AssetTriggerManual,
		SetID:             action.SetID,
		AssetID:           assetID,
		ActorUserID:       actorUserID,
		OldValues:         map[string]any{},
		NewValues:         map[string]any{},
		TriggeredByAction: false,
		CascadeDepth:      0,
	}

	result, err := as.executeActionWithResult(action, event, nil)
	if err != nil {
		atomic.AddInt64(&as.errors, 1)
		return result, err
	}

	switch result.Status {
	case models.ActionStatusFailed:
		atomic.AddInt64(&as.errors, 1)
	case models.ActionStatusCompleted:
		atomic.AddInt64(&as.actionsExecuted, 1)
	}
	return result, nil
}
