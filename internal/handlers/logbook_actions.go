package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

// LogbookNodeExecutionHandler handles internal node execution requests from the
// logbook sidecar. It executes SQLite-dependent nodes (create_item, create_asset)
// that the sidecar cannot perform directly.
//
// Transport auth is a shared secret with the sidecar (X-Internal-Service-Auth
// header). The payload contents (target workspace/set IDs) come from
// user-authored action configs, so each executor must re-check that the
// acting user is authorized against the target workspace or asset set before
// performing a write.
type LogbookNodeExecutionHandler struct {
	secret            string
	eventCoordinator  *services.EventCoordinator
	permissionService *services.PermissionService
	assetHandler      *AssetHandler
	itemCreation      *services.ItemCreationService
	assetRepo         *repository.AssetRepository
}

// NewLogbookNodeExecutionHandler creates a new node execution handler.
func NewLogbookNodeExecutionHandler(secret string, eventCoordinator *services.EventCoordinator, permissionService *services.PermissionService, assetHandler *AssetHandler, itemCreation *services.ItemCreationService, assetRepo *repository.AssetRepository) *LogbookNodeExecutionHandler {
	return &LogbookNodeExecutionHandler{
		secret:            secret,
		eventCoordinator:  eventCoordinator,
		permissionService: permissionService,
		assetHandler:      assetHandler,
		itemCreation:      itemCreation,
		assetRepo:         assetRepo,
	}
}

// HandleNodeExecution authenticates the sidecar via the X-Internal-Service-Auth
// header (shared secret) and executes a node. Distinct from the user-bearer
// auth flow on /rest/api/v1/* — this is internal RPC, not user-presented
// credentials.
func (h *LogbookNodeExecutionHandler) HandleNodeExecution(w http.ResponseWriter, r *http.Request) {
	provided := r.Header.Get("X-Internal-Service-Auth")
	if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(h.secret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req models.NodeExecutionRequest
	if err := newJSONDecoder(w, r).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, models.NodeExecutionResponse{
			Error: "invalid request body",
		})
		return
	}
	// Event.Title is the sidecar-relayed document title — user-supplied
	// text that feeds fallback item/asset titles below.
	sanitize.Apply(&req.Event.Title, sanitize.PlainTextField)

	slog.Info("received node execution request",
		slog.String("component", "logbook-actions"),
		slog.String("node_type", req.NodeType),
		slog.String("document_id", req.Event.DocumentID),
	)

	var output map[string]any
	var execErr error

	switch models.LogbookActionNodeType(req.NodeType) {
	case models.LogbookNodeCreateItem:
		output, execErr = h.executeCreateItem(req.NodeConfig, &req.Event, &req)
	case models.LogbookNodeCreateAsset:
		output, execErr = h.executeCreateAsset(req.NodeConfig, &req.Event, &req)
	default:
		respondJSON(w, http.StatusBadRequest, models.NodeExecutionResponse{
			Error: fmt.Sprintf("unsupported node type: %s", req.NodeType),
		})
		return
	}

	if execErr != nil {
		slog.Error("node execution failed",
			slog.String("component", "logbook-actions"),
			slog.String("node_type", req.NodeType),
			slog.Any("error", execErr),
		)
		respondJSON(w, http.StatusInternalServerError, models.NodeExecutionResponse{
			Error: execErr.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, models.NodeExecutionResponse{
		Output: output,
	})
}

func (h *LogbookNodeExecutionHandler) executeCreateItem(nodeConfig string, event *models.LogbookActionEvent, req *models.NodeExecutionRequest) (map[string]any, error) {
	var config models.CreateItemNodeConfig
	if err := json.Unmarshal([]byte(nodeConfig), &config); err != nil {
		return nil, fmt.Errorf("failed to parse create_item config: %w", err)
	}
	if config.WorkspaceID <= 0 || config.ItemTypeID <= 0 {
		return nil, fmt.Errorf("create_item requires positive workspace_id and item_type_id")
	}
	if event.ActorUserID <= 0 {
		return nil, fmt.Errorf("create_item requires an identified actor")
	}
	sanitize.ApplyAll(
		sanitize.Pair{Target: &config.Title, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &config.Description, Policy: sanitize.RichText},
	)

	title := config.Title
	if title == "" {
		title = "Logbook: " + event.Title
	}

	creatorID := event.ActorUserID
	itemTypeID := config.ItemTypeID

	// Authorize: the action config may target any workspace, so verify the
	// acting user has edit permission on the target before creating an item.
	if h.permissionService == nil || h.itemCreation == nil {
		return nil, fmt.Errorf("create_item blocked: permission or item service not configured")
	}
	hasPerm, permErr := h.permissionService.HasWorkspacePermission(creatorID, config.WorkspaceID, models.PermissionItemCreate)
	if permErr != nil {
		return nil, fmt.Errorf("failed to check workspace permission: %w", permErr)
	}
	if !hasPerm {
		return nil, fmt.Errorf("user %d not authorized to create items in workspace %d", creatorID, config.WorkspaceID)
	}

	slog.Info("creating item from logbook action",
		slog.String("component", "logbook-actions"),
		slog.Int("workspace_id", config.WorkspaceID),
		slog.Int("item_type_id", itemTypeID),
		slog.Int("creator_id", creatorID),
	)

	result, err := h.itemCreation.CreateWithContext(creatorID, "", services.ItemCreateInput{
		WorkspaceID: config.WorkspaceID,
		Title:       title,
		Description: config.Description,
		ItemTypeID:  &itemTypeID,
	}, services.ActionContext{
		TriggeredByAction: true,
		ExecutionChainID:  req.ExecutionChainID,
		CascadeDepth:      req.CascadeDepth + 1,
		SourceApplication: "logbook",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create item: %w", err)
	}
	itemID := result.Item.ID

	slog.Info("item created from logbook action",
		slog.String("component", "logbook-actions"),
		slog.Int("item_id", itemID),
		slog.Int("workspace_id", config.WorkspaceID),
	)

	return map[string]any{
		"item_id":      itemID,
		"title":        title,
		"workspace_id": config.WorkspaceID,
	}, nil
}

func (h *LogbookNodeExecutionHandler) executeCreateAsset(nodeConfig string, event *models.LogbookActionEvent, req *models.NodeExecutionRequest) (map[string]any, error) {
	var config models.CreateAssetNodeConfig
	if err := json.Unmarshal([]byte(nodeConfig), &config); err != nil {
		return nil, fmt.Errorf("failed to parse create_asset config: %w", err)
	}
	if config.AssetSetID <= 0 || config.AssetTypeID <= 0 {
		return nil, fmt.Errorf("create_asset requires positive asset_set_id and asset_type_id")
	}
	if event.ActorUserID <= 0 {
		return nil, fmt.Errorf("create_asset requires an identified actor")
	}
	sanitize.ApplyAll(
		sanitize.Pair{Target: &config.Title, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &config.Description, Policy: sanitize.RichText},
		sanitize.Pair{Target: &config.AssetTag, Policy: sanitize.ShortIdentifier},
	)

	title := config.Title
	if title == "" {
		title = "Logbook: " + event.Title
	}

	// Authorize: the action config may target any asset set, so verify the
	// acting user has create permission on the target set before inserting.
	if h.assetHandler == nil || h.assetRepo == nil {
		return nil, fmt.Errorf("create_asset blocked: asset permission or repository service not configured")
	}
	hasPerm, permErr := h.assetHandler.HasAssetSetPermission(event.ActorUserID, config.AssetSetID, services.AssetPermissionKeyCreate)
	if permErr != nil {
		return nil, fmt.Errorf("failed to check asset set permission: %w", permErr)
	}
	if !hasPerm {
		return nil, fmt.Errorf("user %d not authorized to create assets in set %d", event.ActorUserID, config.AssetSetID)
	}
	belongs, err := h.assetRepo.AssetTypeBelongsToSet(config.AssetTypeID, config.AssetSetID)
	if err != nil {
		return nil, fmt.Errorf("validate asset type: %w", err)
	}
	if !belongs {
		return nil, fmt.Errorf("asset type %d does not belong to set %d", config.AssetTypeID, config.AssetSetID)
	}
	if config.CategoryID != nil {
		belongs, err = h.assetRepo.CategoryBelongsToSet(*config.CategoryID, config.AssetSetID)
		if err != nil {
			return nil, fmt.Errorf("validate asset category: %w", err)
		}
		if !belongs {
			return nil, fmt.Errorf("asset category %d does not belong to set %d", *config.CategoryID, config.AssetSetID)
		}
	}
	if config.StatusID != nil {
		belongs, err = h.assetRepo.StatusBelongsToSet(*config.StatusID, config.AssetSetID)
		if err != nil {
			return nil, fmt.Errorf("validate asset status: %w", err)
		}
		if !belongs {
			return nil, fmt.Errorf("asset status %d does not belong to set %d", *config.StatusID, config.AssetSetID)
		}
	}

	createdAt := time.Now()
	assetID, err := h.assetRepo.CreateAsset(repository.CreateAssetInput{
		SetID:       config.AssetSetID,
		AssetTypeID: config.AssetTypeID,
		CategoryID:  config.CategoryID,
		StatusID:    config.StatusID,
		Title:       title,
		Description: config.Description,
		AssetTag:    config.AssetTag,
		CreatedBy:   event.ActorUserID,
		CreatedAt:   createdAt,
	})
	if err != nil {
		return nil, err
	}

	// Emit asset action event with cascade context (once asset action service exists)
	if h.eventCoordinator != nil && h.eventCoordinator.GetAssetActionService() != nil {
		h.eventCoordinator.GetAssetActionService().EmitAssetActionEvent(&models.AssetActionEvent{
			EventType:         models.AssetTriggerAssetCreated,
			SetID:             config.AssetSetID,
			AssetID:           assetID,
			ActorUserID:       event.ActorUserID,
			TriggeredByAction: true,
			ExecutionChainID:  req.ExecutionChainID,
			CascadeDepth:      req.CascadeDepth + 1,
			SourceApplication: "logbook",
		})
	}

	return map[string]any{
		"asset_id":     assetID,
		"title":        title,
		"asset_set_id": config.AssetSetID,
	}, nil
}
