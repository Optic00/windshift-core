package services

import (
	"errors"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

var (
	// ErrItemDeletionForbidden is returned when the actor lacks item.delete.
	// HTTP surfaces existence-mask it; tools may report permission denied after
	// independently confirming workspace visibility.
	ErrItemDeletionForbidden = errors.New("item deletion forbidden")
	ErrItemDeletionMode      = errors.New("invalid item deletion mode")
)

type ItemDeletionMode int

const (
	ItemDeletionSingle ItemDeletionMode = iota
	ItemDeletionCascade
)

// ItemDeletedEmitter receives the deleted root item and the number of removed
// descendants after the destructive transaction commits.
type ItemDeletedEmitter interface {
	EmitItemDeleted(item *models.Item, actorUserID, descendantCount int, actorUsername ...string)
}

type ItemDeletionRequest struct {
	ItemID             int
	ActorUserID        int
	ActorUsername      string
	Mode               ItemDeletionMode
	CanAccessWorkspace func(workspaceID int) (bool, error)
}

type ItemDeletionResult struct {
	Item            *models.Item
	DeletedCount    int
	DescendantCount int
}

// ItemDeletionApplicationService owns authorization and committed side effects
// for user-facing item deletion. Callers retain transport scope checks,
// existence-masking envelopes, and audit metadata.
type ItemDeletionApplicationService struct {
	db        database.Database
	perm      *PermissionService
	crud      *ItemCRUDService
	itemCache *ItemCacheService
	hierarchy *HierarchyService
	emitter   ItemDeletedEmitter
}

func NewItemDeletionApplicationService(db database.Database, perm *PermissionService) *ItemDeletionApplicationService {
	return &ItemDeletionApplicationService{
		db:        db,
		perm:      perm,
		crud:      NewItemCRUDService(db),
		hierarchy: NewHierarchyService(db),
	}
}

func (s *ItemDeletionApplicationService) SetCache(itemCache *ItemCacheService, hierarchy *HierarchyService) {
	s.itemCache = itemCache
	if hierarchy != nil {
		s.hierarchy = hierarchy
	}
}

func (s *ItemDeletionApplicationService) SetEmitter(emitter ItemDeletedEmitter) {
	s.emitter = emitter
}

func (s *ItemDeletionApplicationService) Delete(req ItemDeletionRequest) (*ItemDeletionResult, error) {
	item, err := repository.NewItemRepository(s.db).FindByID(req.ItemID)
	if err != nil {
		return nil, err
	}

	if req.CanAccessWorkspace != nil {
		accessible, accessErr := req.CanAccessWorkspace(item.WorkspaceID)
		if accessErr != nil {
			return nil, accessErr
		}
		if !accessible {
			return nil, repository.ErrNotFound
		}
	}

	if s.perm == nil {
		return nil, ErrItemDeletionForbidden
	}
	allowed, err := s.perm.HasWorkspacePermission(req.ActorUserID, item.WorkspaceID, models.PermissionItemDelete)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrItemDeletionForbidden
	}

	ancestorIDs := s.loadAncestorIDs(item.ID)
	deletedCount := 1
	var descendantIDs []int
	switch req.Mode {
	case ItemDeletionSingle:
		if err := s.crud.DeleteSingle(item.ID); err != nil {
			return nil, err
		}
	case ItemDeletionCascade:
		result, err := s.crud.Delete(item.ID)
		if err != nil {
			return nil, err
		}
		deletedCount = result.DeletedCount
		descendantIDs = result.DescendantIDs
	default:
		return nil, ErrItemDeletionMode
	}

	s.invalidateCaches(item.ID, descendantIDs, ancestorIDs)
	descendantCount := deletedCount - 1
	if s.emitter != nil {
		s.emitter.EmitItemDeleted(item, req.ActorUserID, descendantCount, req.ActorUsername)
	}

	return &ItemDeletionResult{
		Item:            item,
		DeletedCount:    deletedCount,
		DescendantCount: descendantCount,
	}, nil
}

func (s *ItemDeletionApplicationService) loadAncestorIDs(itemID int) []int {
	if s.itemCache == nil || s.hierarchy == nil {
		return nil
	}
	ancestors, err := s.hierarchy.GetAncestors(itemID)
	if err != nil {
		return nil
	}
	ids := make([]int, 0, len(ancestors))
	for i := range ancestors {
		ids = append(ids, ancestors[i].ID)
	}
	return ids
}

func (s *ItemDeletionApplicationService) invalidateCaches(itemID int, descendantIDs, ancestorIDs []int) {
	if s.itemCache == nil {
		return
	}
	_ = s.itemCache.InvalidateItemHierarchy(itemID, ancestorIDs)
	for _, descendantID := range descendantIDs {
		_ = s.itemCache.InvalidateItemHierarchy(descendantID, nil)
	}
}
