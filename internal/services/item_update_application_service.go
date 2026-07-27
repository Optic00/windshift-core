package services

import (
	"log/slog"

	"windshift/internal/database"
	"windshift/internal/models"
)

// ItemUpdatedEmitter receives the committed before/after state so user-facing
// transports trigger one notification, automation, and webhook pipeline.
type ItemUpdatedEmitter interface {
	EmitItemUpdated(original, updated *models.Item, statusChanged, assigneeChanged bool, actorUserID int, fieldChanges []HistoryEntry, actorUsername ...string)
}

type contextualItemUpdatedEmitter interface {
	EmitItemUpdatedWithContext(original, updated *models.Item, statusChanged, assigneeChanged bool, actorUserID int, fieldChanges []HistoryEntry, actionContext ActionContext, actorUsername ...string)
}

// ItemUpdateApplicationService owns the transport-neutral user-facing update
// pipeline. The lower-level ItemUpdateService remains usable by internal
// workflows that deliberately manage their own side effects.
type ItemUpdateApplicationService struct {
	update          *ItemUpdateService
	activityTracker *ActivityTracker
	itemCache       *ItemCacheService
	hierarchy       *HierarchyService
	mentionService  *MentionService
	emitter         ItemUpdatedEmitter
}

func NewItemUpdateApplicationService(db database.Database, perm *PermissionService) *ItemUpdateApplicationService {
	return &ItemUpdateApplicationService{
		update:    NewItemUpdateService(db).WithPermissionService(perm),
		hierarchy: NewHierarchyService(db),
	}
}

func (s *ItemUpdateApplicationService) SetActivityTracker(activityTracker *ActivityTracker) {
	s.activityTracker = activityTracker
}

func (s *ItemUpdateApplicationService) SetCache(itemCache *ItemCacheService, hierarchy *HierarchyService) {
	s.itemCache = itemCache
	if hierarchy != nil {
		s.hierarchy = hierarchy
	}
}

func (s *ItemUpdateApplicationService) SetMentionService(mentionService *MentionService) {
	s.mentionService = mentionService
}

func (s *ItemUpdateApplicationService) SetEmitter(emitter ItemUpdatedEmitter) {
	s.emitter = emitter
}

func (s *ItemUpdateApplicationService) Update(actorUserID int, actorUsername string, itemID int, updateData map[string]interface{}) (*UpdateItemResult, error) {
	if s.activityTracker != nil {
		if err := s.activityTracker.TrackItemActivity(actorUserID, itemID, ActivityEdit); err != nil {
			slog.Warn("failed to track item edit activity", slog.Int("user_id", actorUserID), slog.Int("item_id", itemID), slog.Any("error", err))
		}
	}

	result, err := s.update.UpdateItem(UpdateItemRequest{
		ItemID:     itemID,
		UpdateData: updateData,
		UserID:     actorUserID,
	})
	if err != nil {
		return nil, err
	}

	if s.itemCache != nil && itemProjectResolutionChanged(result.OriginalItem, result.Item) {
		s.invalidateEffectiveProjectSubtree(result.Item.ID)
	}

	assigneeChanged := !itemIntPtrEqual(result.OriginalItem.AssigneeID, result.Item.AssigneeID)
	if s.emitter != nil {
		s.emitter.EmitItemUpdated(
			result.OriginalItem,
			result.Item,
			result.StatusChanged,
			assigneeChanged,
			actorUserID,
			result.FieldChanges,
			actorUsername,
		)
	}

	if s.mentionService != nil && result.OriginalItem.Description != result.Item.Description {
		if err := s.mentionService.ProcessMentions(ProcessMentionsParams{
			SourceType:  "item_description",
			SourceID:    result.Item.ID,
			Content:     result.Item.Description,
			ItemID:      result.Item.ID,
			WorkspaceID: result.Item.WorkspaceID,
			ActorUserID: actorUserID,
		}); err != nil {
			slog.Warn("failed to process description mentions", slog.Int("item_id", result.Item.ID), slog.Any("error", err))
		}
	}

	return result, nil
}

// AddMilestoneWithContext atomically adds one milestone without replacing
// concurrent attachments. Duplicate deliveries are no-ops: they produce no
// history, live refresh, automation event, notification, or webhook.
func (s *ItemUpdateApplicationService) AddMilestoneWithContext(
	actorUserID int,
	actorUsername string,
	itemID int,
	milestoneID int,
	actionContext ActionContext,
) (*UpdateItemResult, bool, error) {
	result, changed, err := s.update.AddMilestone(UpdateItemRequest{
		ItemID: itemID,
		UserID: actorUserID,
	}, milestoneID)
	if err != nil || !changed {
		return result, changed, err
	}
	if contextual, ok := s.emitter.(contextualItemUpdatedEmitter); ok {
		contextual.EmitItemUpdatedWithContext(
			result.OriginalItem,
			result.Item,
			false,
			false,
			actorUserID,
			result.FieldChanges,
			actionContext,
			actorUsername,
		)
	} else if s.emitter != nil {
		s.emitter.EmitItemUpdated(
			result.OriginalItem,
			result.Item,
			false,
			false,
			actorUserID,
			result.FieldChanges,
			actorUsername,
		)
	}
	return result, true, nil
}

func itemProjectResolutionChanged(original, updated *models.Item) bool {
	return original.InheritProject != updated.InheritProject ||
		!itemIntPtrEqual(original.ProjectID, updated.ProjectID) ||
		!itemIntPtrEqual(original.ParentID, updated.ParentID)
}

func itemIntPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (s *ItemUpdateApplicationService) invalidateEffectiveProjectSubtree(itemID int) {
	_ = s.itemCache.InvalidateItemHierarchy(itemID, nil)
	if s.hierarchy == nil {
		return
	}
	descendants, err := s.hierarchy.GetDescendants(itemID, 0)
	if err != nil {
		slog.Warn("failed to load descendants for cache invalidation", slog.Int("item_id", itemID), slog.Any("error", err))
		return
	}
	for i := range descendants {
		_ = s.itemCache.InvalidateItemHierarchy(descendants[i].ID, nil)
	}
}
