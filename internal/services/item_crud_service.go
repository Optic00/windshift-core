package services

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"windshift/internal/cql"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

// ItemCRUDService handles item CRUD operations
type ItemCRUDService struct {
	db            database.Database
	repo          *repository.ItemRepository
	workspaceRepo *repository.WorkspaceRepository
}

// NewItemCRUDService creates a new item CRUD service
func NewItemCRUDService(db database.Database) *ItemCRUDService {
	return &ItemCRUDService{
		db:            db,
		repo:          repository.NewItemRepository(db),
		workspaceRepo: repository.NewWorkspaceRepository(db),
	}
}

// GetByID retrieves an item by ID with all details
func (s *ItemCRUDService) GetByID(id int) (*models.Item, error) {
	return s.repo.FindByIDWithDetails(id)
}

// GetByIDWithWorkspaceStatus retrieves an item with workspace active status for permission checks
func (s *ItemCRUDService) GetByIDWithWorkspaceStatus(id int) (*repository.ItemWithWorkspaceStatus, error) {
	return s.repo.FindByIDWithWorkspaceStatus(id)
}

// GetByIDBasic retrieves an item by ID without joins
// deadcode-keep: called by core-tests/internal/services/item_crud_service_test.go
func (s *ItemCRUDService) GetByIDBasic(id int) (*models.Item, error) {
	return s.repo.FindByID(id)
}

// Exists checks if an item exists
// deadcode-keep: called by core-tests/internal/services/item_crud_service_test.go
func (s *ItemCRUDService) Exists(id int) (bool, error) {
	return s.repo.Exists(id)
}

// GetWorkspaceID returns the workspace ID for an item
func (s *ItemCRUDService) GetWorkspaceID(itemID int) (int, error) {
	return s.repo.GetWorkspaceID(itemID)
}

// DeleteResult contains the result of a delete operation
type DeleteResult struct {
	DeletedCount   int
	DescendantIDs  []int
	AffectedParent *int
}

// DeleteSingle removes only the requested item and shared related rows. It
// preserves the legacy non-cascade delete endpoint semantics; use Delete for
// item + descendants cleanup.
func (s *ItemCRUDService) DeleteSingle(itemID int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.repo.DeleteItemLinks(tx, itemID); err != nil {
		return err
	}
	if err := s.repo.ClearWorklogItemReferences(tx, itemID); err != nil {
		return err
	}
	if err := s.repo.Delete(tx, itemID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// Delete removes an item and all its descendants
func (s *ItemCRUDService) Delete(itemID int) (*DeleteResult, error) {
	// Get parent ID before deleting
	parentID, err := s.repo.GetParentID(itemID)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil, fmt.Errorf("item not found")
		}
		return nil, err
	}

	// Get all descendant IDs for cascade operations
	descendantIDs, err := s.repo.GetDescendantIDs(itemID)
	if err != nil {
		return nil, fmt.Errorf("failed to get descendants: %w", err)
	}

	// Start transaction
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete all related data for item and descendants
	allIDs := append([]int{itemID}, descendantIDs...)
	for _, id := range allIDs {
		// Delete watches
		if err := s.repo.DeleteItemWatches(tx, id); err != nil {
			return nil, err
		}

		// Delete history
		if err := s.repo.DeleteItemHistory(tx, id); err != nil {
			return nil, err
		}

		// Delete links
		if err := s.repo.DeleteItemLinks(tx, id); err != nil {
			return nil, err
		}

		// Clear worklog references
		if err := s.repo.ClearWorklogItemReferences(tx, id); err != nil {
			return nil, err
		}

		// Delete the item itself
		if err := s.repo.Delete(tx, id); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return &DeleteResult{
		DeletedCount:   len(allIDs),
		DescendantIDs:  descendantIDs,
		AffectedParent: parentID,
	}, nil
}

// CopyOptions contains options for copying an item
type CopyOptions struct {
	IncludeChildren bool
	NewParentID     *int
	NewTitle        string
	CreatorID       int
}

// CopyResult contains the result of a copy operation
type CopyResult struct {
	NewItemID int
	CopyCount int
}

// Copy creates a copy of an item.
// deadcode-keep: called by core-tests/internal/services/item_crud_service_test.go
func (s *ItemCRUDService) Copy(itemID int, opts CopyOptions) (*CopyResult, error) {
	source, err := s.repo.FindByID(itemID)
	if err != nil {
		return nil, fmt.Errorf("source item not found: %w", err)
	}

	driverName := s.db.GetDriverName()
	const maxRetries = 5
	var newID int
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		newID, lastErr = func() (int, error) {
			tx, err := s.db.Begin()
			if err != nil {
				return 0, fmt.Errorf("failed to begin transaction: %w", err)
			}
			defer func() { _ = tx.Rollback() }()

			fracIndex, err := repository.GenerateFracIndexForNewItem(tx, driverName)
			if err != nil {
				return 0, err
			}
			nextNum, err := s.repo.GetNextWorkspaceItemNumber(tx, source.WorkspaceID)
			if err != nil {
				return 0, err
			}

			parentID := opts.NewParentID
			if parentID == nil {
				parentID = source.ParentID
			}
			newItem := &models.Item{
				WorkspaceID:         source.WorkspaceID,
				WorkspaceItemNumber: nextNum,
				ItemTypeID:          source.ItemTypeID,
				Title:               opts.NewTitle,
				Description:         source.Description,
				StatusID:            source.StatusID,
				PriorityID:          source.PriorityID,
				DueDate:             source.DueDate,
				StartDate:           source.StartDate,
				EndDate:             source.EndDate,
				IsTask:              source.IsTask,
				IterationID:         source.IterationID,
				ProjectID:           source.ProjectID,
				InheritProject:      source.InheritProject,
				AssigneeID:          source.AssigneeID,
				CreatorID:           &opts.CreatorID,
				CustomFieldValues:   source.CustomFieldValues,
				ParentID:            parentID,
				RelatedWorkItemID:   source.RelatedWorkItemID,
				StoryPoints:         source.StoryPoints,
				FracIndex:           &fracIndex,
			}

			id, err := s.repo.Create(tx, newItem)
			if err != nil {
				return 0, fmt.Errorf("failed to create copy: %w", err)
			}

			now := time.Now()
			if _, err := tx.Exec(`
				INSERT INTO item_milestones (item_id, milestone_id, created_at)
				SELECT ?, milestone_id, ? FROM item_milestones WHERE item_id = ?
			`, id, now, source.ID); err != nil {
				return 0, fmt.Errorf("failed to copy milestones: %w", err)
			}

			if err := tx.Commit(); err != nil {
				return 0, fmt.Errorf("failed to commit transaction: %w", err)
			}
			return id, nil
		}()
		if lastErr == nil {
			break
		}
		if !repository.IsFracIndexUniqueViolation(lastErr) {
			return nil, lastErr
		}
		slog.Warn("frac_index unique violation on copy, retrying",
			slog.Int("attempt", attempt+1),
			slog.Int("source_item_id", source.ID),
			slog.String("component", "fracindex"))
		if attempt == maxRetries-1 {
			return nil, fmt.Errorf("copy item %d failed after %d frac_index retries: %w", source.ID, maxRetries, lastErr)
		}
	}

	updateService := NewItemUpdateService(s.db)
	if err := updateService.recordItemCreationHistory(s.db, newID, opts.CreatorID); err != nil {
		slog.Warn("failed to record item creation history", "error", err, "item_id", newID)
	}

	return &CopyResult{NewItemID: newID, CopyCount: 1}, nil
}

// GetChildren returns direct children of an item
func (s *ItemCRUDService) GetChildren(parentID int) ([]*models.Item, error) {
	return s.repo.GetChildren(parentID)
}

// GetDescendants returns all descendants of an item
// deadcode-keep: called by core-tests/internal/services/item_crud_service_test.go
func (s *ItemCRUDService) GetDescendants(parentID int) ([]*models.Item, error) {
	return s.repo.GetDescendants(parentID)
}

// GetAncestors returns the ancestors of an item (path to root)
// deadcode-keep: called by core-tests/internal/services/item_crud_service_test.go
func (s *ItemCRUDService) GetAncestors(itemID int) ([]*models.Item, error) {
	return s.repo.GetAncestors(itemID)
}

// GetRootItems returns all root items for a workspace
// deadcode-keep: called by core-tests/internal/services/item_crud_service_test.go
func (s *ItemCRUDService) GetRootItems(workspaceID int) ([]*models.Item, error) {
	return s.repo.GetRootItems(workspaceID)
}

// ItemListParams re-exports repository.ItemListParams for service layer consumers
type ItemListParams = repository.ItemListParams

// ItemFilters re-exports repository.ItemFilters for service layer consumers
type ItemFilters = repository.ItemFilters

// PaginationParams re-exports repository.PaginationParams for service layer consumers
type PaginationParams = repository.PaginationParams

// List retrieves items with filters and pagination using the repository
func (s *ItemCRUDService) List(params ItemListParams) ([]models.Item, int, error) {
	return s.repo.FindAllWithDetails(params)
}

// Search searches items by title and description
func (s *ItemCRUDService) Search(query string, workspaceIDs []int, pagination PaginationParams) ([]models.Item, int, error) {
	return s.repo.Search(query, workspaceIDs, pagination)
}

// SearchParams contains parameters for the advanced Search handler
type SearchParams struct {
	TextQuery    string
	WorkspaceIDs []int
	StatusIDs    []int
	PriorityIDs  []int
	Pagination   PaginationParams
}

// SearchWithFilters searches items with multiple filter criteria
func (s *ItemCRUDService) SearchWithFilters(params SearchParams) ([]models.Item, int, error) {
	if len(params.WorkspaceIDs) == 0 {
		return []models.Item{}, 0, nil
	}

	filters := ItemFilters{
		StatusIDs:   params.StatusIDs,
		PriorityIDs: params.PriorityIDs,
	}

	// Detect workspace key pattern (e.g. "OK-40")
	if params.TextQuery != "" {
		parts := strings.Split(strings.ToUpper(params.TextQuery), "-")
		isKeyPattern := len(parts) == 2 && parts[0] != "" && parts[1] != ""
		if isKeyPattern {
			if _, err := strconv.Atoi(parts[1]); err == nil {
				filters.ItemKeyQuery = params.TextQuery
			} else {
				filters.TextQuery = params.TextQuery
			}
		} else {
			filters.TextQuery = params.TextQuery
		}
	}

	return s.repo.FindAllWithDetails(ItemListParams{
		WorkspaceIDs: params.WorkspaceIDs,
		Filters:      filters,
		Pagination:   params.Pagination,
		SortBy:       "updated_at",
	})
}

// resolveCollectionQL resolves a QL query string from either a direct QL parameter
// or a collection ID. Returns the resolved QL string and whether a collection was used.
func (s *ItemCRUDService) resolveCollectionQL(qlQuery string, collectionID int) (resolvedQL string, isCollection bool, err error) {
	if qlQuery != "" {
		return qlQuery, false, nil
	}
	if collectionID <= 0 {
		return "", false, nil
	}
	_, collectionQL, err := s.workspaceRepo.GetCollectionQuery(collectionID)
	if err != nil {
		if err == repository.ErrNotFound {
			return "", false, fmt.Errorf("collection not found")
		}
		return "", false, fmt.Errorf("failed to get collection query: %w", err)
	}
	if strings.TrimSpace(collectionQL) != "" {
		return collectionQL, true, nil
	}
	return "", true, nil
}

// evaluateQL compiles a QL query string into SQL WHERE clause and arguments.
// Returns empty qlSQL when the input query is empty.
func (s *ItemCRUDService) evaluateQL(qlQuery string, ctx cql.FunctionContext) (qlSQL string, qlArgs []interface{}, err error) {
	if qlQuery == "" {
		return "", nil, nil
	}
	qlQuery = cql.SubstituteFunctions(qlQuery, ctx)
	workspaceMap, err := s.workspaceRepo.BuildWorkspaceMap()
	if err != nil {
		return "", nil, fmt.Errorf("failed to build workspace map: %w", err)
	}
	customFieldMap, err := s.repo.GetCQLCustomFieldMap()
	if err != nil {
		return "", nil, fmt.Errorf("failed to build custom field map: %w", err)
	}
	evaluator := cql.NewEvaluator(workspaceMap, customFieldMap, s.db.GetDriverName())
	qlSQL, qlArgs, err = evaluator.EvaluateToSQL(qlQuery)
	if err != nil {
		return "", nil, fmt.Errorf("QL query error: %w", err)
	}
	return qlSQL, qlArgs, nil
}

// BacklogParams contains parameters for retrieving backlog items
type BacklogParams struct {
	WorkspaceID  int    // 0 if not specified (collection-only query)
	CollectionID int    // 0 if not specified
	QLQuery      string // Direct QL query, overrides collection
	SubQLQuery   string // Sub-filter QL query (ANDed with collection/direct QL)
	WorkspaceIDs []int  // Accessible workspace IDs for security filtering
	UserID       int    // Authenticated user ID for currentUser() resolution
	Pagination   PaginationParams
}

// GetBacklogItems retrieves items with non-completed statuses for a workspace/collection
func (s *ItemCRUDService) GetBacklogItems(params BacklogParams) ([]models.Item, int, error) {
	if len(params.WorkspaceIDs) == 0 {
		return []models.Item{}, 0, nil
	}

	// Resolve backlog status IDs
	backlogStatusIDs, err := s.repo.GetBacklogStatusIDs(params.WorkspaceID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get backlog statuses: %w", err)
	}
	if len(backlogStatusIDs) == 0 {
		return []models.Item{}, 0, nil
	}

	filters := ItemFilters{
		StatusIDs: backlogStatusIDs,
	}

	// Resolve QL query from collection or direct parameter
	qlQuery, collectionResolved, err := s.resolveCollectionQL(params.QLQuery, params.CollectionID)
	if err != nil {
		return nil, 0, err
	}

	// Combine with sub-filter QL if provided
	if subQL := strings.TrimSpace(params.SubQLQuery); subQL != "" {
		if qlQuery != "" {
			qlQuery = "(" + qlQuery + ") AND (" + subQL + ")"
		} else {
			qlQuery = subQL
		}
	}

	// Evaluate QL query into SQL
	qlSQL, qlArgs, err := s.evaluateQL(qlQuery, cql.UserContext(params.UserID))
	if err != nil {
		return nil, 0, err
	}
	if qlSQL != "" {
		filters.QLQuery = qlSQL
		filters.QLArgs = qlArgs
	}

	// Apply workspace_id filter only when no collection was resolved
	if !collectionResolved && params.WorkspaceID > 0 {
		filters.WorkspaceID = &params.WorkspaceID
	}

	return s.repo.FindAllWithDetails(ItemListParams{
		WorkspaceIDs: params.WorkspaceIDs,
		Filters:      filters,
		Pagination:   params.Pagination,
	})
}

// ListWithQLParams contains parameters for listing items with QL support
type ListWithQLParams struct {
	WorkspaceID  int    // Single workspace filter (0 = all accessible)
	CollectionID int    // Collection to resolve QL from (0 = none)
	QLQuery      string // Direct QL query (overrides collection)
	SubQLQuery   string // Sub-filter QL query (ANDed with base QL)
	WorkspaceIDs []int  // Accessible workspace IDs for security filtering
	UserID       int    // Authenticated user ID for currentUser() resolution
	Filters      ItemFilters
	Pagination   PaginationParams
	SortBy       string
	SortAsc      bool
}

// ListWithQL retrieves items with QL evaluation and collection resolution
func (s *ItemCRUDService) ListWithQL(params ListWithQLParams) ([]models.Item, int, error) {
	if len(params.WorkspaceIDs) == 0 {
		return []models.Item{}, 0, nil
	}

	filters := params.Filters

	// Resolve QL query from collection or direct parameter
	qlQuery, collectionResolved, err := s.resolveCollectionQL(params.QLQuery, params.CollectionID)
	if err != nil {
		return nil, 0, err
	}

	// Combine with sub-filter QL if provided
	if subQL := strings.TrimSpace(params.SubQLQuery); subQL != "" {
		if qlQuery != "" {
			qlQuery = "(" + qlQuery + ") AND (" + subQL + ")"
		} else {
			qlQuery = subQL
		}
	}

	// Evaluate QL query into SQL
	qlSQL, qlArgs, err := s.evaluateQL(qlQuery, cql.UserContext(params.UserID))
	if err != nil {
		return nil, 0, err
	}
	if qlSQL != "" {
		filters.QLQuery = qlSQL
		filters.QLArgs = qlArgs
	}

	// If collection was resolved but produced no effective query, return empty results.
	// A collection with no filter means "nothing to show yet."
	if collectionResolved && filters.QLQuery == "" {
		return []models.Item{}, 0, nil
	}

	// Apply workspace_id filter only when no collection was resolved
	if !collectionResolved && params.WorkspaceID > 0 {
		filters.WorkspaceID = &params.WorkspaceID
	}

	return s.repo.FindAllWithDetails(ItemListParams{
		WorkspaceIDs: params.WorkspaceIDs,
		Filters:      filters,
		Pagination:   params.Pagination,
		SortBy:       params.SortBy,
		SortAsc:      params.SortAsc,
	})
}

// GetWithEffectiveProject retrieves an item with effective project calculated
// This is the most comprehensive Get method, used by the handler
// deadcode-keep: called by core-tests/internal/services/item_crud_service_test.go
func (s *ItemCRUDService) GetWithEffectiveProject(id int) (*models.Item, error) {
	item, err := s.repo.FindByIDWithDetails(id)
	if err != nil {
		return nil, err
	}

	// Resolve the effective project through the canonical resolver so this
	// shares the single source of truth with the item handler and the cache
	// (it keys off the inherit_project boolean and stops at the first ancestor
	// with a direct project).
	res, err := s.repo.ResolveEffectiveProject(id)
	if err != nil {
		return nil, err
	}
	switch {
	case res.DirectProjectID == nil && !res.InheritProject:
		item.ProjectInheritanceMode = "none"
	case res.InheritProject:
		item.EffectiveProjectID = res.EffectiveProjectID
		item.ProjectInheritanceMode = "inherit"
	default:
		item.EffectiveProjectID = res.EffectiveProjectID
		item.ProjectInheritanceMode = "direct"
	}

	// Populate the effective project name when one resolved.
	if item.EffectiveProjectID != nil {
		if item.ProjectID != nil && *item.ProjectID == *item.EffectiveProjectID {
			item.EffectiveProjectName = item.ProjectName
		} else {
			var name sql.NullString
			if err := s.db.QueryRow("SELECT name FROM time_projects WHERE id = ?", *item.EffectiveProjectID).Scan(&name); err != nil {
				slog.Warn("failed to look up project name", slog.Any("error", err))
			}
			if name.Valid {
				item.EffectiveProjectName = name.String
			}
		}
	}

	return item, nil
}

// GetHistory retrieves the change history for an item
func (s *ItemCRUDService) GetHistory(itemID int) ([]models.ItemHistory, error) {
	rows, err := s.db.Query(`
		SELECT h.id, h.item_id, h.user_id, h.changed_at, h.field_name, h.old_value, h.new_value,
		       u.first_name || ' ' || u.last_name as user_name, u.email as user_email
		FROM item_history h
		LEFT JOIN users u ON h.user_id = u.id
		WHERE h.item_id = ?
		ORDER BY h.changed_at DESC
	`, itemID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch item history: %w", err)
	}
	defer rows.Close()

	var history []models.ItemHistory
	for rows.Next() {
		var h models.ItemHistory
		var userName, userEmail sql.NullString
		err := rows.Scan(&h.ID, &h.ItemID, &h.UserID, &h.ChangedAt, &h.FieldName, &h.OldValue, &h.NewValue,
			&userName, &userEmail)
		if err != nil {
			slog.Error("failed to scan item history row", slog.Int("item_id", itemID), slog.Any("error", err))
			continue
		}
		if userName.Valid {
			h.UserName = userName.String
		}
		if userEmail.Valid {
			h.UserEmail = userEmail.String
		}
		history = append(history, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate item history: %w", err)
	}

	if history == nil {
		history = []models.ItemHistory{}
	}

	return history, nil
}

// GetAttachments retrieves all attachments for an item
func (s *ItemCRUDService) GetAttachments(itemID int) ([]models.Attachment, error) {
	rows, err := s.db.Query(`
		SELECT a.id, a.item_id, a.filename, a.original_filename, a.mime_type, a.file_size,
		       a.has_thumbnail, a.uploaded_by, a.created_at,
		       u.first_name || ' ' || u.last_name as uploader_name, u.email as uploader_email
		FROM attachments a
		LEFT JOIN users u ON a.uploaded_by = u.id
		WHERE a.item_id = ? AND COALESCE(a.entity_type, 'item') = 'item'
		ORDER BY a.created_at DESC
	`, itemID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch attachments: %w", err)
	}
	defer rows.Close()

	var attachments []models.Attachment
	for rows.Next() {
		var a models.Attachment
		var itemID sql.NullInt64
		var uploaderID sql.NullInt64
		var uploaderName, uploaderEmail sql.NullString
		err := rows.Scan(&a.ID, &itemID, &a.Filename, &a.OriginalFilename, &a.MimeType, &a.FileSize,
			&a.HasThumbnail, &uploaderID, &a.CreatedAt, &uploaderName, &uploaderEmail)
		if err != nil {
			slog.Error("failed to scan attachment row", slog.Any("error", err))
			continue
		}
		if itemID.Valid {
			id := int(itemID.Int64)
			a.ItemID = &id
		}
		if uploaderID.Valid {
			id := int(uploaderID.Int64)
			a.UploadedBy = &id
		}
		if uploaderName.Valid {
			a.UploaderName = uploaderName.String
		}
		if uploaderEmail.Valid {
			a.UploaderEmail = uploaderEmail.String
		}
		attachments = append(attachments, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate attachments: %w", err)
	}

	if attachments == nil {
		attachments = []models.Attachment{}
	}

	return attachments, nil
}
