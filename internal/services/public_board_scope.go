package services

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	"windshift/internal/cql"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

var (
	// ErrPublicBoardWorkspaceScopeRequired means the query is not safely bounded.
	ErrPublicBoardWorkspaceScopeRequired = errors.New("public board workspace scope required")
	// ErrPublicBoardWorkspaceNotFound means a scope value does not resolve.
	ErrPublicBoardWorkspaceNotFound = errors.New("public board workspace not found")
	// ErrPublicBoardWorkspaceAdminRequired means the actor cannot publish one scope.
	ErrPublicBoardWorkspaceAdminRequired = errors.New("public board workspace admin required")
)

// PublicBoardScopeService resolves and authorizes collection workspace scopes.
type PublicBoardScopeService struct {
	workspaceRepo     *repository.WorkspaceRepository
	permissionService *PermissionService
}

// NewPublicBoardScopeService creates a public-board scope service.
func NewPublicBoardScopeService(db database.Database, permissionService *PermissionService) *PublicBoardScopeService {
	return &PublicBoardScopeService{
		workspaceRepo:     repository.NewWorkspaceRepository(db),
		permissionService: permissionService,
	}
}

// ResolveWorkspaceIDs returns every workspace named by the query's effective scope.
func (s *PublicBoardScopeService) ResolveWorkspaceIDs(query string) ([]int, error) {
	references, err := cql.ExtractWorkspaceScope(query)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPublicBoardWorkspaceScopeRequired, err)
	}

	workspaceIDs := make(map[int]struct{}, len(references))
	for _, reference := range references {
		ids, err := s.resolveReference(reference)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("%w: %s %q", ErrPublicBoardWorkspaceNotFound, reference.Field, reference.Value)
		}
		for _, id := range ids {
			workspaceIDs[id] = struct{}{}
		}
	}

	ids := make([]int, 0, len(workspaceIDs))
	for id := range workspaceIDs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids, nil
}

// AuthorizePublishing requires workspace.admin in every resolved scope.
func (s *PublicBoardScopeService) AuthorizePublishing(userID int, query string) ([]int, error) {
	workspaceIDs, err := s.ResolveWorkspaceIDs(query)
	if err != nil {
		return nil, err
	}
	if s.permissionService == nil {
		return nil, errors.New("public board scope authorization requires a permission service")
	}
	for _, workspaceID := range workspaceIDs {
		allowed, err := s.permissionService.HasWorkspacePermission(userID, workspaceID, models.PermissionWorkspaceAdmin)
		if err != nil {
			return nil, fmt.Errorf("check workspace %d public-board permission: %w", workspaceID, err)
		}
		if !allowed {
			return nil, fmt.Errorf("%w: workspace %d", ErrPublicBoardWorkspaceAdminRequired, workspaceID)
		}
	}
	return workspaceIDs, nil
}

func (s *PublicBoardScopeService) resolveReference(reference cql.WorkspaceScopeReference) ([]int, error) {
	switch reference.Field {
	case cql.WorkspaceScopeNameOrKey:
		ids, err := s.workspaceRepo.ListIDsByName(reference.Value)
		if err != nil {
			return nil, err
		}
		keyID, err := s.workspaceRepo.FindIDByKey(reference.Value)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}
		if err == nil {
			for _, id := range ids {
				if id == keyID {
					return ids, nil
				}
			}
			ids = append(ids, keyID)
		}
		return ids, nil
	case cql.WorkspaceScopeName:
		return s.workspaceRepo.ListIDsByName(reference.Value)
	case cql.WorkspaceScopeKey:
		id, err := s.workspaceRepo.FindIDByKey(reference.Value)
		if errors.Is(err, repository.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []int{id}, nil
	case cql.WorkspaceScopeID:
		id, err := strconv.Atoi(reference.Value)
		if err != nil {
			return nil, fmt.Errorf("parse workspace id %q: %w", reference.Value, err)
		}
		exists, err := s.workspaceRepo.Exists(id)
		if err != nil {
			return nil, fmt.Errorf("check workspace %d: %w", id, err)
		}
		if !exists {
			return nil, nil
		}
		return []int{id}, nil
	default:
		return nil, fmt.Errorf("unsupported public-board workspace scope field %q", reference.Field)
	}
}
