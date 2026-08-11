package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"windshift/internal/models"
	"windshift/internal/repository"
)

type actionAssetNodeSupport struct {
	assets      *AssetService
	items       *repository.ItemRepository
	permissions AssetSetPermissionChecker
	api         NodeAPI
}

// CreateAssetNodeExecutor adapts create_asset nodes to AssetService.
type CreateAssetNodeExecutor struct {
	support actionAssetNodeSupport
}

// NewCreateAssetNodeExecutor constructs a create_asset executor.
func NewCreateAssetNodeExecutor(assets *AssetService, items *repository.ItemRepository, permissions AssetSetPermissionChecker, api NodeAPI) *CreateAssetNodeExecutor {
	return &CreateAssetNodeExecutor{support: actionAssetNodeSupport{
		assets: assets, items: items, permissions: permissions, api: api,
	}}
}

func (e *CreateAssetNodeExecutor) NodeType() models.ActionNodeType {
	return models.ActionNodeCreateAsset
}

func (e *CreateAssetNodeExecutor) Execute(node *models.ActionNode, ctx *models.ExecutionContext, stepResult *models.StepResult) error {
	if err := e.support.validate(); err != nil {
		return fmt.Errorf("create_asset: %w", err)
	}
	if ctx == nil {
		return fmt.Errorf("create_asset: execution context is required")
	}
	var config models.CreateAssetNodeConfig
	if err := json.Unmarshal([]byte(node.NodeConfig), &config); err != nil {
		return fmt.Errorf("failed to parse create_asset config: %w", err)
	}
	if config.AssetSetID == 0 {
		return fmt.Errorf("asset_set_id is required")
	}
	if config.AssetTypeID == 0 {
		return fmt.Errorf("asset_type_id is required")
	}
	if err := e.support.authorize(ctx.EffectiveActorID, config.AssetSetID, AssetPermissionKeyCreate); err != nil {
		return err
	}

	title := e.support.api.SubstituteVariables(config.Title, ctx)
	description := e.support.api.SubstituteVariables(config.Description, ctx)
	assetTag := e.support.api.SubstituteVariables(config.AssetTag, ctx)
	itemFields, err := e.support.itemCustomFields(currentActionItemID(ctx), true, true)
	if err != nil {
		return err
	}
	assetFields := e.support.mapFields(config.FieldMappings, ctx, itemFields, nil)

	created, err := e.support.assets.CreateAssetWithContext(
		automationAuditActor(e.support.assets.db, ctx.EffectiveActorID, "workspace_action"),
		repository.CreateAssetInput{
			SetID:       config.AssetSetID,
			AssetTypeID: config.AssetTypeID,
			CategoryID:  config.CategoryID,
			StatusID:    config.StatusID,
			Title:       title,
			Description: description,
			AssetTag:    assetTag,
			CreatedBy:   ctx.EffectiveActorID,
			CreatedAt:   time.Now(),
		},
		assetFields,
		actionAssetAutomationContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("create asset through mutation service: %w", err)
	}

	stepResult.Output = map[string]any{
		"asset_id":      created.ID,
		"title":         created.Title,
		"description":   created.Description,
		"asset_tag":     created.AssetTag,
		"asset_set_id":  config.AssetSetID,
		"asset_type_id": config.AssetTypeID,
		"mapping_count": len(config.FieldMappings),
	}
	slog.Debug("created asset via action",
		slog.String("component", "actions"),
		slog.Int("asset_id", created.ID),
		slog.Int("item_id", currentActionItemID(ctx)),
	)
	return nil
}

// UpdateAssetNodeExecutor adapts update_asset nodes to AssetService.
type UpdateAssetNodeExecutor struct {
	support actionAssetNodeSupport
}

// NewUpdateAssetNodeExecutor constructs an update_asset executor.
func NewUpdateAssetNodeExecutor(assets *AssetService, items *repository.ItemRepository, permissions AssetSetPermissionChecker, api NodeAPI) *UpdateAssetNodeExecutor {
	return &UpdateAssetNodeExecutor{support: actionAssetNodeSupport{
		assets: assets, items: items, permissions: permissions, api: api,
	}}
}

func (e *UpdateAssetNodeExecutor) NodeType() models.ActionNodeType {
	return models.ActionNodeUpdateAsset
}

func (e *UpdateAssetNodeExecutor) Execute(node *models.ActionNode, ctx *models.ExecutionContext, stepResult *models.StepResult) error {
	if err := e.support.validate(); err != nil {
		return fmt.Errorf("update_asset: %w", err)
	}
	if ctx == nil {
		return fmt.Errorf("update_asset: execution context is required")
	}
	var config models.UpdateAssetNodeConfig
	if err := json.Unmarshal([]byte(node.NodeConfig), &config); err != nil {
		return fmt.Errorf("failed to parse update_asset config: %w", err)
	}
	if len(config.FieldMappings) == 0 {
		stepResult.Output = map[string]any{"skipped": true, "reason": "no field mappings configured"}
		return nil
	}

	itemID := currentActionItemID(ctx)
	itemFields, err := e.support.itemCustomFields(itemID, false, false)
	if err != nil {
		return err
	}
	assetID := actionAssetReferenceID(itemFields[config.SourceFieldID])
	if assetID == 0 {
		reason := "invalid asset reference format"
		if itemFields[config.SourceFieldID] == nil {
			reason = "no asset linked in source field"
		}
		stepResult.Output = map[string]any{"skipped": true, "reason": reason}
		return nil
	}

	asset, err := e.support.assets.FindAsset(assetID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("asset not found: %d", assetID)
		}
		return fmt.Errorf("failed to get asset: %w", err)
	}
	if config.AssetTypeID > 0 && asset.AssetTypeID != config.AssetTypeID {
		return fmt.Errorf("asset type mismatch: expected %d, got %d", config.AssetTypeID, asset.AssetTypeID)
	}
	if config.AssetSetID > 0 && asset.SetID != config.AssetSetID {
		return fmt.Errorf("asset set mismatch: expected %d, got %d", config.AssetSetID, asset.SetID)
	}
	if err := e.support.authorize(ctx.EffectiveActorID, asset.SetID, AssetPermissionKeyEdit); err != nil {
		return err
	}

	assetFields := make(map[string]any, len(asset.CustomFieldValues)+len(config.FieldMappings))
	for key, value := range asset.CustomFieldValues {
		assetFields[key] = value
	}
	oldValues := make(map[string]any, len(config.FieldMappings))
	newValues := make(map[string]any, len(config.FieldMappings))
	e.support.mapFields(config.FieldMappings, ctx, itemFields, func(target string, value any) {
		oldValues[target] = assetFields[target]
		assetFields[target] = value
		newValues[target] = value
	})

	updated, err := e.support.assets.MutateAsset(
		automationAuditActor(e.support.assets.db, ctx.EffectiveActorID, "workspace_action"),
		assetID,
		AssetMutationPatch{CustomFieldValues: assetFields},
		actionAssetAutomationContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("mutate asset custom fields: %w", err)
	}
	for key := range newValues {
		newValues[key] = updated.CustomFieldValues[key]
	}

	stepResult.Output = map[string]any{
		"asset_id":      assetID,
		"old_values":    oldValues,
		"new_values":    newValues,
		"mapping_count": len(config.FieldMappings),
	}
	slog.Debug("updated asset via action",
		slog.String("component", "actions"),
		slog.Int("asset_id", assetID),
		slog.Int("item_id", itemID),
		slog.Int("mappings", len(config.FieldMappings)),
	)
	return nil
}

func (s *actionAssetNodeSupport) validate() error {
	if s.assets == nil {
		return fmt.Errorf("asset service not configured")
	}
	if s.items == nil {
		return fmt.Errorf("item repository not configured")
	}
	if s.api == nil {
		return fmt.Errorf("node API not configured")
	}
	return nil
}

func (s *actionAssetNodeSupport) authorize(actorUserID, setID int, permissionKey string) error {
	if actorUserID <= 0 {
		return fmt.Errorf("asset mutation requires an identified actor (set %d)", setID)
	}
	if s.permissions == nil {
		return fmt.Errorf("asset mutation blocked: asset permission checker not configured")
	}
	ok, err := s.permissions.HasAssetSetPermission(actorUserID, setID, permissionKey)
	if err != nil {
		return fmt.Errorf("failed to check asset set %d permission: %w", setID, err)
	}
	if !ok {
		return fmt.Errorf("user %d not authorized (%s) on asset set %d", actorUserID, permissionKey, setID)
	}
	return nil
}

func (s *actionAssetNodeSupport) itemCustomFields(itemID int, allowMissing, allowMalformed bool) (map[string]any, error) {
	raw, err := s.items.GetCustomFieldValuesRaw(itemID)
	if err != nil {
		if allowMissing && errors.Is(err, repository.ErrNotFound) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("failed to get item custom_field_values: %w", err)
	}
	values := map[string]any{}
	if raw.Valid && raw.String != "" {
		if err := json.Unmarshal([]byte(raw.String), &values); err != nil {
			if allowMalformed {
				return map[string]any{}, nil
			}
			return nil, fmt.Errorf("failed to parse item custom_field_values: %w", err)
		}
	}
	return values, nil
}

func (s *actionAssetNodeSupport) mapFields(mappings []models.AssetFieldMapping, ctx *models.ExecutionContext, itemFields map[string]any, apply func(string, any)) map[string]any {
	values := make(map[string]any, len(mappings))
	for _, mapping := range mappings {
		var value any
		switch mapping.SourceType {
		case "item_field":
			value = currentItemFieldValue(s.items, ctx, mapping.SourceValue)
			if value == nil {
				value = itemFields[mapping.SourceValue]
			}
		case "literal":
			value = mapping.SourceValue
		default:
			value = s.api.SubstituteVariables(mapping.SourceValue, ctx)
		}
		values[mapping.TargetFieldID] = value
		if apply != nil {
			apply(mapping.TargetFieldID, value)
		}
	}
	return values
}

func actionAssetReferenceID(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case map[string]any:
		return actionAssetReferenceID(v["id"])
	default:
		return 0
	}
}

func actionAssetAutomationContext(ctx *models.ExecutionContext) AssetAutomationContext {
	depth := 1
	if ctx != nil && ctx.Event != nil {
		depth = ctx.Event.CascadeDepth + 1
	}
	return AssetAutomationContext{
		TriggeredByAction: true,
		ExecutionChainID:  ctx.ChainID,
		CascadeDepth:      depth,
		SourceApplication: "workspace",
	}
}
