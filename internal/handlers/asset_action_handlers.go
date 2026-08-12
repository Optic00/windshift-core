package handlers

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/repository/actionutil"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

func validateAssetActionKinds(triggerType models.AssetActionTriggerType, nodes []models.AssetActionNode) string {
	switch triggerType {
	case models.AssetTriggerAssetCreated, models.AssetTriggerAssetUpdated,
		models.AssetTriggerAssetStatusChanged, models.AssetTriggerAssetDeleted,
		models.AssetTriggerManual:
		// valid
	default:
		return fmt.Sprintf("Invalid asset action trigger type: %s", triggerType)
	}
	for i, node := range nodes {
		switch node.NodeType {
		case models.AssetNodeTrigger, models.AssetNodeCreateItem, models.AssetNodeSetField,
			models.AssetNodeSetStatus, models.AssetNodeCondition, models.AssetNodeNotifyUser:
			// valid
		default:
			return fmt.Sprintf("Invalid asset action node type at nodes[%d]: %s", i, node.NodeType)
		}
	}
	return ""
}

// sanitizeAssetActionFlow validates persisted JSON node configs rather than
// scrubbing them, and sanitizes identifier-shaped edge fields.
func sanitizeAssetActionFlow(w http.ResponseWriter, r *http.Request, nodes []models.AssetActionNode, edges []models.AssetActionEdge) bool {
	for i := range nodes {
		if err := sanitize.ValidateJSONPayload("node_config", nodes[i].NodeConfig); err != nil {
			respondValidationError(w, r, err.Error())
			return false
		}
	}
	for i := range edges {
		sanitize.ApplyAll(
			sanitize.Pair{Target: &edges[i].EdgeType, Policy: sanitize.ShortIdentifier},
			sanitize.Pair{Target: &edges[i].SourceHandle, Policy: sanitize.ShortIdentifier},
			sanitize.Pair{Target: &edges[i].TargetHandle, Policy: sanitize.ShortIdentifier},
		)
	}
	return true
}

// AssetActionHandler handles asset action automation API endpoints
type AssetActionHandler struct {
	repo          *repository.AssetActionRepository
	assetHandler  *AssetHandler
	actionService *services.AssetActionService
	auditor       *logger.Auditor
}

// NewAssetActionHandler creates a new asset action handler
// requireAssetAction fetches an asset action by ID and verifies set ownership.
func (h *AssetActionHandler) requireAssetAction(w http.ResponseWriter, r *http.Request, actionID, setID int) (*models.AssetAction, bool) {
	action, err := h.repo.GetByID(actionID)
	if err == repository.ErrNotFound || (err == nil && action.SetID != setID) {
		respondNotFound(w, r, "asset action")
		return nil, false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return nil, false
	}
	return action, true
}

func NewAssetActionHandler(repo *repository.AssetActionRepository, assetHandler *AssetHandler, actionService *services.AssetActionService, auditor *logger.Auditor) *AssetActionHandler {
	return &AssetActionHandler{
		repo:          repo,
		assetHandler:  assetHandler,
		actionService: actionService,
		auditor:       auditor,
	}
}

// requireSetAdminAccess parses setID from the "setId" path param and checks admin permission.
func (h *AssetActionHandler) requireSetAdminAccess(w http.ResponseWriter, r *http.Request) (*models.User, int, bool) {
	return h.assetHandler.requireSetAdminAccess(w, r)
}

// ListActions lists all actions for an asset set
func (h *AssetActionHandler) ListActions(w http.ResponseWriter, r *http.Request) {
	_, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	actions, err := h.repo.ListBySet(setID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if actions == nil {
		actions = []*models.AssetAction{}
	}

	respondJSONOK(w, actions)
}

// GetAction gets a single asset action by ID
func (h *AssetActionHandler) GetAction(w http.ResponseWriter, r *http.Request) {
	_, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	actionID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	action, ok := h.requireAssetAction(w, r, actionID, setID)
	if !ok {
		return
	}

	respondJSONOK(w, action)
}

// CreateAction creates a new asset action
func (h *AssetActionHandler) CreateAction(w http.ResponseWriter, r *http.Request) {
	currentUser, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	req, ok := decodeJSON[models.CreateAssetActionRequest](w, r)
	if !ok {
		return
	}
	sanitize.ApplyAll(
		sanitize.Pair{Target: &req.Name, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &req.Description, Policy: sanitize.RichText},
	)
	// TriggerConfig is a JSON blob — reject invalid JSON instead of
	// HTML-stripping it (which would corrupt valid payloads).
	if err := sanitize.ValidateJSONPayload("trigger_config", req.TriggerConfig); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	if !sanitizeAssetActionFlow(w, r, req.Nodes, req.Edges) {
		return
	}

	if msg := actionutil.ValidateActionFields(req.Name, string(req.TriggerType)); msg != "" {
		respondValidationError(w, r, msg)
		return
	}
	if msg := validateAssetActionKinds(req.TriggerType, req.Nodes); msg != "" {
		respondValidationError(w, r, msg)
		return
	}
	if h.actionService != nil {
		if err := h.actionService.ValidateTaxonomyReferences(setID, req.TriggerConfig, req.Nodes); err != nil {
			respondValidationError(w, r, err.Error())
			return
		}
	}

	if err := actionutil.ValidateFlowAcyclic[
		models.AssetActionNode, *models.AssetActionNode,
		models.AssetActionEdge, *models.AssetActionEdge,
	](req.Nodes, req.Edges); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	action := &models.AssetAction{
		SetID:         setID,
		Name:          req.Name,
		Description:   req.Description,
		IsEnabled:     true,
		TriggerType:   req.TriggerType,
		TriggerConfig: req.TriggerConfig,
		CreatedBy:     &currentUser.ID,
	}

	actionID, err := h.repo.Create(action)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	action.ID = actionID

	// Create nodes and edges if provided
	if flowErr := actionutil.CreateFlowNodesAndEdges[
		models.AssetActionNode, *models.AssetActionNode,
		models.AssetActionEdge, *models.AssetActionEdge,
	](
		actionID, req.Nodes, req.Edges,
		func(n *models.AssetActionNode) (int, error) { return h.createNode(*n) },
		func(e *models.AssetActionEdge) (int, error) { return h.createEdge(*e) },
		func() { _ = h.repo.Delete(actionID) },
	); flowErr != nil {
		respondInternalError(w, r, flowErr)
		return
	}

	// Invalidate cache
	if h.actionService != nil {
		h.actionService.InvalidateSetCache(setID)
	}

	createdAction, err := h.repo.GetByID(actionID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	h.auditor.Log(r, currentUser, logger.ActionAutomationCreate, logger.ResourceAutomation, &createdAction.ID, createdAction.Name)

	respondJSONCreated(w, createdAction)
}

// applyAssetActionUpdateFields applies non-nil fields from the update request to the asset action.
func applyAssetActionUpdateFields(action *models.AssetAction, req *models.UpdateAssetActionRequest) {
	if req.Name != nil {
		action.Name = *req.Name
	}
	if req.Description != nil {
		action.Description = *req.Description
	}
	if req.TriggerType != nil {
		action.TriggerType = *req.TriggerType
	}
	if req.TriggerConfig != nil {
		action.TriggerConfig = *req.TriggerConfig
	}
	if req.IsEnabled != nil {
		action.IsEnabled = *req.IsEnabled
	}
}

// UpdateAction updates an existing asset action
func (h *AssetActionHandler) UpdateAction(w http.ResponseWriter, r *http.Request) {
	currentUser, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	actionID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	action, ok := h.requireAssetAction(w, r, actionID, setID)
	if !ok {
		return
	}

	req, ok := decodeJSON[models.UpdateAssetActionRequest](w, r)
	if !ok {
		return
	}
	sanitize.ApplyAll(
		sanitize.Pair{Target: req.Name, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: req.Description, Policy: sanitize.RichText},
	)
	// TriggerConfig is a JSON blob — reject invalid JSON instead of
	// HTML-stripping it (which would corrupt valid payloads).
	if req.TriggerConfig != nil {
		if err := sanitize.ValidateJSONPayload("trigger_config", *req.TriggerConfig); err != nil {
			respondValidationError(w, r, err.Error())
			return
		}
	}
	if !sanitizeAssetActionFlow(w, r, req.Nodes, req.Edges) {
		return
	}
	if req.Nodes == nil && req.Edges != nil {
		respondValidationError(w, r, "nodes must be provided when replacing action edges")
		return
	}

	var err error

	applyAssetActionUpdateFields(action, &req)
	if msg := actionutil.ValidateActionFields(action.Name, string(action.TriggerType)); msg != "" {
		respondValidationError(w, r, msg)
		return
	}
	effectiveNodes := action.Nodes
	if req.Nodes != nil {
		effectiveNodes = req.Nodes
	}
	if msg := validateAssetActionKinds(action.TriggerType, effectiveNodes); msg != "" {
		respondValidationError(w, r, msg)
		return
	}
	if h.actionService != nil {
		if err := h.actionService.ValidateTaxonomyReferences(setID, action.TriggerConfig, effectiveNodes); err != nil {
			respondValidationError(w, r, err.Error())
			return
		}
	}

	if req.Nodes != nil {
		if cycleErr := actionutil.ValidateFlowAcyclic[
			models.AssetActionNode, *models.AssetActionNode,
			models.AssetActionEdge, *models.AssetActionEdge,
		](req.Nodes, req.Edges); cycleErr != nil {
			respondValidationError(w, r, cycleErr.Error())
			return
		}
		err = h.repo.SaveActionWithNodesAndEdges(action, req.Nodes, req.Edges)
		if err != nil {
			respondInternalError(w, r, fmt.Errorf("failed to save action: %w", err))
			return
		}
	} else {
		err = h.repo.Update(action)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
	}

	if h.actionService != nil {
		h.actionService.InvalidateSetCache(setID)
	}

	updatedAction, err := h.repo.GetByID(actionID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionAutomationUpdate, logger.ResourceAutomation, &actionID, updatedAction.Name)
	}

	respondJSONOK(w, updatedAction)
}

// DeleteAction deletes an asset action
func (h *AssetActionHandler) DeleteAction(w http.ResponseWriter, r *http.Request) {
	currentUser, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	actionID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if _, ok := h.requireAssetAction(w, r, actionID, setID); !ok {
		return
	}

	if err := h.repo.Delete(actionID); err != nil {
		respondInternalError(w, r, err)
		return
	}

	if h.actionService != nil {
		h.actionService.InvalidateSetCache(setID)
	}

	if currentUser != nil {
		h.auditor.Log(r, currentUser, logger.ActionAutomationDelete, logger.ResourceAutomation, &actionID, "")
	}

	w.WriteHeader(http.StatusNoContent)
}

// ToggleAction enables or disables an asset action
func (h *AssetActionHandler) ToggleAction(w http.ResponseWriter, r *http.Request) {
	currentUser, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	actionID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	action, ok := h.requireAssetAction(w, r, actionID, setID)
	if !ok {
		return
	}

	var req struct {
		IsEnabled bool `json:"is_enabled"`
	}
	if err := newJSONDecoder(w, r).Decode(&req); err != nil {
		if !errors.Is(err, io.EOF) {
			respondBadRequest(w, r, "Invalid JSON request body")
			return
		}
		req.IsEnabled = !action.IsEnabled
	}

	if err := h.repo.SetEnabled(actionID, req.IsEnabled); err != nil {
		respondInternalError(w, r, err)
		return
	}

	if h.actionService != nil {
		h.actionService.InvalidateSetCache(setID)
	}

	updatedAction, err := h.repo.GetByID(actionID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if currentUser != nil {
		h.auditor.LogWithDetails(
			r,
			currentUser,
			logger.ActionAutomationToggle,
			logger.ResourceAutomation,
			&actionID,
			updatedAction.Name,
			map[string]any{
				"old_is_enabled": action.IsEnabled,
				"is_enabled":     updatedAction.IsEnabled,
			},
		)
	}

	respondJSONOK(w, updatedAction)
}

// ExecuteAssetActionRequest represents the request body for manual asset action execution
type ExecuteAssetActionRequest struct {
	AssetID int `json:"asset_id"`
}

// ExecuteAction manually executes an asset action
func (h *AssetActionHandler) ExecuteAction(w http.ResponseWriter, r *http.Request) {
	currentUser, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	actionID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	var err error

	req, ok := decodeJSON[ExecuteAssetActionRequest](w, r)
	if !ok {
		return
	}

	if req.AssetID == 0 {
		respondValidationError(w, r, "asset_id is required")
		return
	}

	action, ok := h.requireAssetAction(w, r, actionID, setID)
	if !ok {
		return
	}
	if !action.IsEnabled {
		respondValidationError(w, r, "action is disabled")
		return
	}

	// The target asset id is caller-controlled; confirm it belongs to the
	// admin-checked set before executing, otherwise an admin of one set could
	// drive an action against an asset in a set they cannot access. Return 404
	// (not 403) to avoid disclosing the existence of out-of-set assets.
	assetSetID, err := h.assetHandler.repo.GetAssetSetID(req.AssetID)
	if err == repository.ErrNotFound || (err == nil && assetSetID != setID) {
		respondNotFound(w, r, "asset")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if h.actionService == nil {
		respondInternalError(w, r, fmt.Errorf("asset action service not available"))
		return
	}

	result, err := h.actionService.ExecuteActionManuallyWithResult(action, req.AssetID, currentUser.ID)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("failed to execute action: %w", err))
		return
	}

	respondJSONOK(w, result)
}

// GetActionLogs gets execution logs for an asset action
func (h *AssetActionHandler) GetActionLogs(w http.ResponseWriter, r *http.Request) {
	_, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	actionID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if _, ok := h.requireAssetAction(w, r, actionID, setID); !ok {
		return
	}

	limit, offset := parseOffsetPagination(r, 50, 100)

	logs, err := h.repo.GetExecutionLogs(actionID, limit, offset)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if logs == nil {
		logs = []*models.AssetActionExecutionLog{}
	}

	respondJSONOK(w, logs)
}

// GetSetLogs gets all execution logs for an asset set
func (h *AssetActionHandler) GetSetLogs(w http.ResponseWriter, r *http.Request) {
	_, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	limit, offset := parseOffsetPagination(r, 50, 100)

	logs, err := h.repo.GetSetExecutionLogs(setID, limit, offset)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if logs == nil {
		logs = []*models.AssetActionExecutionLog{}
	}

	respondJSONOK(w, logs)
}

// createNode delegates to the repository (kept as a method for the flow callback).
func (h *AssetActionHandler) createNode(node models.AssetActionNode) (int, error) {
	return h.repo.CreateNode(node)
}

// createEdge delegates to the repository (kept as a method for the flow callback).
func (h *AssetActionHandler) createEdge(edge models.AssetActionEdge) (int, error) {
	return h.repo.CreateEdge(edge)
}
