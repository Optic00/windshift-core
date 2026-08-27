package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"windshift/internal/constants"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/validation"
)

// ErrMissingItemType is returned by CreateItem when the caller did not
// supply an item_type_id and no workspace or global default could be
// resolved. The handler maps this to a 400 instead of a 500.
var ErrMissingItemType = errors.New("item_type_id is required: workspace has no default item type configured")

// ErrInvalidItemType is returned by CreateItem when the caller supplied an
// item_type_id that does not exist in the item_types table. Guards against
// dangling type references (e.g. `ws task create --type 999`). The handler
// maps this to a 400 instead of a 500.
var ErrInvalidItemType = errors.New("item_type_id does not reference a valid item type")

// mapTextStatusToID maps legacy text status values to status IDs
// Returns nil if the status cannot be mapped
// Default status IDs from database setup:
// 1: "Open", 2: "To Do", 3: "In Progress", 4: "Under Review", 5: "Done", 6: "Closed"
func mapTextStatusToID(status string) *int {
	normalized := strings.ToLower(strings.TrimSpace(status))
	normalized = strings.ReplaceAll(normalized, "_", " ") // Handle in_progress -> in progress

	switch normalized {
	case "open":
		id := 1
		return &id
	case "to do", "todo":
		id := 2
		return &id
	case "in progress", "inprogress", "in-progress":
		id := 3
		return &id
	case "under review", "review":
		id := 4
		return &id
	case "done", "completed":
		id := 5
		return &id
	case "closed":
		id := 6
		return &id
	default:
		return nil
	}
}

// mapTextPriorityToID maps legacy text priority values to priority IDs
// Returns nil if the priority cannot be mapped
// Default priority IDs from database setup:
// 1: "Low", 2: "Medium", 3: "High", 4: "Critical"
func mapTextPriorityToID(priority string) *int {
	normalized := strings.ToLower(strings.TrimSpace(priority))

	switch normalized {
	case "low":
		id := 1
		return &id
	case "medium":
		id := 2
		return &id
	case "high":
		id := 3
		return &id
	case "critical", "urgent":
		id := 4
		return &id
	default:
		return nil
	}
}

// ItemCreationParams contains all parameters for creating an item
type ItemCreationParams struct {
	WorkspaceID             int
	Title                   string
	Description             string
	Status                  string // Text status (legacy) - mapped to StatusID if StatusID is nil
	StatusID                *int   // Direct status ID - takes precedence over Status text
	ItemTypeID              *int
	Priority                string // Text priority (legacy) - mapped to PriorityID if PriorityID is nil
	PriorityID              *int   // Direct priority ID - takes precedence over Priority text
	IsTask                  bool
	ParentID                *int
	MilestoneIDs            []int
	IterationID             *int
	ProjectID               *int
	InheritProject          bool
	TimeProjectID           *int
	AssigneeID              *int
	ReporterID              *int // Reporter/submitter of the item
	CreatorID               *int
	CreatorPortalCustomerID *int
	ChannelID               *int       // Portal-specific: track portal/channel
	RequestTypeID           *int       // Portal-specific: track request type
	DueDate                 *time.Time // Due date for the item
	StartDate               *time.Time // Start date for the item
	EndDate                 *time.Time // End date for the item
	RelatedWorkItemID       *int       // For personal tasks: related work item
	StoryPoints             *float64   // Story points for velocity tracking
	EstimateMinutes         *int       // Time estimate in minutes (compared against logged worklog time)
	CustomFieldValuesJSON   string     // JSON string of custom field values
	VirtualFieldDataJSON    string     // JSON string of request-type virtual values
	// CreatedAt / UpdatedAt override the default `time.Now()` timestamps. Used
	// by the Jira importer to preserve the original issue chronology so audit
	// views, reports, and "recent" filters reflect Jira's history rather than
	// import time. Both fall back to time.Now() when nil.
	CreatedAt *time.Time
	UpdatedAt *time.Time
	// SkipAssigneeTrigger suppresses the coding-agent assignee trigger for
	// bulk paths (e.g. the Jira importer) where pre-assigned items must not
	// each start an agent run.
	SkipAssigneeTrigger bool
	// SkipPublish suppresses live item-change publication for callers that
	// explicitly defer or replace post-commit publication.
	SkipPublish bool
	// SkipMandatoryTemplate preserves source content for reconciliation and
	// import paths. User-facing creation keeps template enforcement enabled.
	SkipMandatoryTemplate bool
	// AfterCreate extends the canonical creation transaction with source-owned
	// records that must commit atomically with the item and its milestones.
	AfterCreate ItemCreateTransactionHook
	// AllowUnparentedGenericSubtask permits the Jira importer's two-phase
	// insert-then-link flow to stage a level -1 item before its source parent
	// has been imported. No interactive or automation caller should set it.
	AllowUnparentedGenericSubtask bool
	// ValidatingUserID marks user-facing creation. CreateItem uses it to reject
	// assignees who cannot act in the workspace. PermService also rejects a
	// ProjectID / TimeProjectID the user may not view (returning ErrProjectNotFound,
	// indistinguishable from a non-existent project to avoid ID enumeration).
	// Internal callers such as imports leave it zero to preserve imported identity.
	ValidatingUserID int
	PermService      *PermissionService
	// MandatoryTemplateOut, when non-nil, is populated by CreateItem with the
	// mandatory work item template (WI-438) the resolved item type enforces, if
	// any — and whether its body was applied. Lets non-UI callers (v1 REST, the
	// ws CLI) echo the enforced structure even when they supplied their own
	// description. TemplateID == 0 after the call means the type enforces none.
	MandatoryTemplateOut *MandatoryTemplateInfo
}

// ItemCreateTransactionHook extends the item creation transaction.
type ItemCreateTransactionHook func(context.Context, database.Tx, int) error

// MandatoryTemplateInfo reports the mandatory template a resolved item type
// enforces at create time and whether CreateItem applied its body (only when
// the incoming description was empty, per the "fill only when empty" rule).
type MandatoryTemplateInfo struct {
	TemplateID int    `json:"template_id"`
	Name       string `json:"name"`
	Applied    bool   `json:"applied"`
}

// ErrProjectNotFound is returned by CreateItem when a supplied project_id /
// time_project_id either does not exist or is not accessible to the validating
// user. The two cases share one error so callers cannot enumerate project IDs.
var ErrProjectNotFound = errors.New("project not found")

// validateProjectAssignmentAccess ensures the validating user may attach the
// given project IDs. Existence is checked first (CanViewProject treats an
// unrestricted non-existent project as viewable), then access.
func validateProjectAssignmentAccess(db database.Database, perm *PermissionService, userID int, projectIDs ...*int) error {
	ts := NewTimePermissionService(db, perm)
	for _, pid := range projectIDs {
		if pid == nil || *pid <= 0 {
			continue
		}
		var exists bool
		if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM time_projects WHERE id = ?)", *pid).Scan(&exists); err != nil {
			return fmt.Errorf("failed to validate project: %w", err)
		}
		if !exists {
			return ErrProjectNotFound
		}
		hasAccess, err := ts.CanViewProject(userID, *pid)
		if err != nil {
			return fmt.Errorf("failed to check project access: %w", err)
		}
		if !hasAccess {
			return ErrProjectNotFound
		}
	}
	return nil
}

func resolveItemTypeForCreation(db database.Database, workspaceID int, itemTypeID *int) (*int, error) {
	if itemTypeID != nil {
		if *itemTypeID <= 0 {
			return nil, ErrInvalidItemType
		}
		var exists bool
		if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM item_types WHERE id = ?)", *itemTypeID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("failed to check item type existence: %w", err)
		}
		if !exists {
			return nil, ErrInvalidItemType
		}

		allowed, err := IsItemTypeAllowedInWorkspace(db, workspaceID, *itemTypeID)
		if err != nil {
			return nil, fmt.Errorf("failed to check item type restriction: %w", err)
		}
		if !allowed {
			return nil, fmt.Errorf("item type is not allowed in this workspace")
		}
		return itemTypeID, nil
	}

	var defaultItemTypeID int
	err := db.QueryRow(`
		SELECT cs.default_item_type_id FROM configuration_sets cs
		INNER JOIN workspace_configuration_sets wcs ON cs.id = wcs.configuration_set_id
		WHERE wcs.workspace_id = ? AND cs.default_item_type_id IS NOT NULL
		ORDER BY cs.is_default DESC
		LIMIT 1
	`, workspaceID).Scan(&defaultItemTypeID)
	if err != nil {
		err = db.QueryRow("SELECT id FROM item_types WHERE is_default = true LIMIT 1").Scan(&defaultItemTypeID)
	}
	if err != nil || defaultItemTypeID == 0 {
		return nil, ErrMissingItemType
	}
	return &defaultItemTypeID, nil
}

// CreateItem creates a new item with proper transaction handling and number generation
// This centralizes the item creation logic used by normal creation, portal submissions, and copying
func CreateItem(db database.Database, params ItemCreationParams) (int64, error) {
	return createItem(context.Background(), db, params)
}

func createItem(ctx context.Context, db database.Database, params ItemCreationParams) (int64, error) {
	if ctx == nil {
		return 0, fmt.Errorf("item creation requires a context")
	}
	if err := validation.ValidatePlanningAssignments(db, params.WorkspaceID, params.MilestoneIDs, params.IterationID); err != nil {
		return 0, err
	}
	if params.ValidatingUserID > 0 && params.AssigneeID != nil {
		var actionable bool
		var err error
		if params.PermService != nil {
			actionable, err = NewWorkspaceUserResolver(db, params.PermService).CanActInWorkspace(*params.AssigneeID, params.WorkspaceID)
		} else {
			actionable, err = repository.NewUserRepository(db).ActiveExists(*params.AssigneeID)
		}
		if err != nil {
			return 0, fmt.Errorf("failed to validate assignee: %w", err)
		}
		if !actionable {
			return 0, &validation.ValidationError{Field: "assignee_id", Message: "Assignee user not found"}
		}
	}

	// Enforce project-assignment access control: a user may only attach a
	// project_id / time_project_id they can view. Skipped for internal callers
	// that don't set a validating user.
	if params.ValidatingUserID > 0 && params.PermService != nil {
		if err := validateProjectAssignmentAccess(db, params.PermService, params.ValidatingUserID, params.ProjectID, params.TimeProjectID); err != nil {
			return 0, err
		}
	}

	// Resolve and validate the item type before status and hierarchy checks.
	var err error
	params.ItemTypeID, err = resolveItemTypeForCreation(db, params.WorkspaceID, params.ItemTypeID)
	if err != nil {
		return 0, err
	}
	if err := validation.ValidateGenericSubtaskBoundary(
		db,
		*params.ItemTypeID,
		params.ParentID,
		params.AllowUnparentedGenericSubtask,
	); err != nil {
		return 0, err
	}

	// Apply a mandatory work item template (WI-438). Every create path funnels
	// through here, so this single seam gives uniform mandatory-template
	// behavior. If the resolved type enforces an active mandatory template,
	// fill an empty description with its body (decision #2: only when empty so
	// agents/importers that supply a description stay predictable), and report
	// what was enforced regardless of whether it was applied.
	if !params.SkipMandatoryTemplate {
		mandatory, terr := repository.NewTemplateRepository(db).GetMandatoryForType(params.WorkspaceID, *params.ItemTypeID)
		if terr == nil {
			applied := false
			if strings.TrimSpace(params.Description) == "" {
				params.Description = mandatory.DescriptionBody
				applied = true
			}
			if params.MandatoryTemplateOut != nil {
				*params.MandatoryTemplateOut = MandatoryTemplateInfo{
					TemplateID: mandatory.ID,
					Name:       mandatory.Name,
					Applied:    applied,
				}
			}
		} else if !errors.Is(terr, repository.ErrNotFound) {
			return 0, fmt.Errorf("failed to resolve mandatory template: %w", terr)
		}
	}

	now := time.Now()
	createdAt := now
	if params.CreatedAt != nil {
		createdAt = *params.CreatedAt
	}
	updatedAt := now
	if params.UpdatedAt != nil {
		updatedAt = *params.UpdatedAt
	}

	// Resolve status ID BEFORE starting the transaction to avoid holding the TX open during lookups.
	// User/API create paths may request a non-initial status (e.g. board quick-add into a column),
	// but only when that status is reachable from the workflow initial status without conditions,
	// validators, or approval gates. Internal import/automation callers that do not set
	// ValidatingUserID keep the historical direct-status behavior.
	workflowService := NewWorkflowService(db)
	var statusID *int
	if params.StatusID != nil {
		if params.ValidatingUserID > 0 {
			if err := workflowService.ValidateCreateStatusOverride(ctx, params.WorkspaceID, params.ItemTypeID, *params.StatusID); err != nil {
				return 0, err
			}
		}
		statusID = params.StatusID
	} else if params.Status != "" {
		statusID = mapTextStatusToID(params.Status)
	}

	// If status is still nil, resolve from workflow initial status using cache.
	if statusID == nil {
		statusID, _ = workflowService.GetInitialStatusIDCached(params.WorkspaceID, params.ItemTypeID)
	}
	// Personal workspaces intentionally have no workflow, but their tasks still
	// use the shared Open/Done status pair. Keep new personal items completable.
	if statusID == nil {
		isPersonal, err := repository.IsPersonalWorkspace(db, params.WorkspaceID)
		if err != nil {
			return 0, fmt.Errorf("resolve personal workspace status: %w", err)
		}
		if isPersonal {
			statusID = intPtr(constants.StatusIDOpen)
		}
	}

	// Resolve priority ID BEFORE transaction: direct ID takes precedence, then text mapping, then default
	var priorityID *int
	if params.PriorityID != nil {
		priorityID = params.PriorityID
	} else if params.Priority != "" {
		priorityID = mapTextPriorityToID(params.Priority)
	}

	// If priority is still nil, get the default priority scoped to the workspace's configuration set
	if priorityID == nil {
		var defaultPriorityID int
		err := db.QueryRow(`
			SELECT p.id FROM priorities p
			INNER JOIN configuration_set_priorities csp ON p.id = csp.priority_id
			INNER JOIN workspace_configuration_sets wcs ON csp.configuration_set_id = wcs.configuration_set_id
			WHERE wcs.workspace_id = ?
			ORDER BY p.is_default DESC, p.sort_order, p.id
			LIMIT 1
		`, params.WorkspaceID).Scan(&defaultPriorityID)
		if err != nil {
			// Fallback to global default if workspace has no configuration set
			err = db.QueryRow("SELECT id FROM priorities WHERE is_default = true LIMIT 1").Scan(&defaultPriorityID)
		}
		if err == nil {
			priorityID = &defaultPriorityID
		}
	}
	if priorityID != nil {
		allowed, err := validation.IsPriorityAllowedInWorkspace(db, params.WorkspaceID, *priorityID)
		if err != nil {
			return 0, fmt.Errorf("failed to validate workspace priority: %w", err)
		}
		if !allowed {
			return 0, &validation.ValidationError{Field: "priority_id", Message: "Priority is not allowed in this workspace"}
		}
	}

	itemID, err := repository.WithItemCreateTransaction(ctx, db, func(tx database.Tx) (int, error) {
		fracIndex, err := repository.GenerateFracIndexForNewItem(tx, db.GetDriverName())
		if err != nil {
			return 0, fmt.Errorf("failed to generate frac_index: %w", err)
		}

		// Get next workspace-specific item number (within transaction to prevent race conditions)
		nextWorkspaceItemNumber, err := repository.NewItemRepository(db).GetNextWorkspaceItemNumber(tx, params.WorkspaceID)
		if err != nil {
			return 0, fmt.Errorf("failed to generate workspace item number: %w", err)
		}

		// Insert item with all fields
		// Note: Uses RETURNING id for both SQLite (3.35+) and PostgreSQL
		insertQuery := `
			INSERT INTO items (
				workspace_id, workspace_item_number, item_type_id, title, description, status_id, priority_id, is_task,
				iteration_id, project_id, inherit_project, time_project_id, assignee_id, reporter_id, creator_id, creator_portal_customer_id,
				channel_id, request_type_id, due_date, start_date, end_date, related_work_item_id,
				story_points, estimate_minutes, custom_field_values, virtual_field_data, parent_id,
				frac_index, created_at, updated_at, last_active_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING id
		`

		var itemID int64
		if err := tx.QueryRow(insertQuery,
			params.WorkspaceID,
			nextWorkspaceItemNumber,
			params.ItemTypeID,
			params.Title,
			params.Description,
			statusID,
			priorityID,
			params.IsTask,
			params.IterationID,
			params.ProjectID,
			params.InheritProject,
			params.TimeProjectID,
			params.AssigneeID,
			params.ReporterID,
			params.CreatorID,
			params.CreatorPortalCustomerID,
			params.ChannelID,
			params.RequestTypeID,
			params.DueDate,
			params.StartDate,
			params.EndDate,
			params.RelatedWorkItemID,
			params.StoryPoints,
			params.EstimateMinutes,
			nullString(params.CustomFieldValuesJSON),
			nullString(params.VirtualFieldDataJSON),
			params.ParentID,
			fracIndex,
			createdAt,
			updatedAt,
			updatedAt, // last_active_at: a new item is "active" as of creation
		).Scan(&itemID); err != nil {
			return 0, fmt.Errorf("failed to insert item: %w", err)
		}

		// Attach milestones inside the same transaction so a milestone-validation
		// failure rolls back the item insert. Empty/nil slice = no milestones.
		for _, mID := range params.MilestoneIDs {
			if _, err := tx.Exec(
				"INSERT INTO item_milestones (item_id, milestone_id, created_at) VALUES (?, ?, ?)",
				itemID, mID, now,
			); err != nil {
				return 0, fmt.Errorf("failed to attach milestone %d to new item: %w", mID, err)
			}
		}
		if params.AfterCreate != nil {
			if err := params.AfterCreate(ctx, tx, int(itemID)); err != nil {
				return 0, err
			}
		}
		return int(itemID), nil
	})
	if err != nil {
		return 0, err
	}

	// Record item creation history asynchronously if a creator is specified
	if params.CreatorID != nil {
		historyService := GetHistoryService(db)
		historyService.RecordItemCreationHistoryAsync(db, models.Item{
			ID:              itemID,
			WorkspaceID:     params.WorkspaceID,
			ItemTypeID:      params.ItemTypeID,
			Title:           params.Title,
			Description:     params.Description,
			StatusID:        statusID,
			PriorityID:      priorityID,
			IterationID:     params.IterationID,
			ProjectID:       params.ProjectID,
			InheritProject:  params.InheritProject,
			TimeProjectID:   params.TimeProjectID,
			AssigneeID:      params.AssigneeID,
			CreatorID:       params.CreatorID,
			ParentID:        params.ParentID,
			DueDate:         params.DueDate,
			StartDate:       params.StartDate,
			EndDate:         params.EndDate,
			StoryPoints:     params.StoryPoints,
			EstimateMinutes: params.EstimateMinutes,
		}, *params.CreatorID)
	}

	// Items created with an assignee already set fire the coding-agent
	// binding trigger exactly like a later assignment would (the create
	// surfaces previously skipped it, so create-with-agent-assignee
	// silently never started a run).
	if !params.SkipAssigneeTrigger && params.AssigneeID != nil {
		triggeredBy := params.ValidatingUserID
		if triggeredBy == 0 && params.CreatorID != nil {
			triggeredBy = *params.CreatorID
		}
		maybeTriggerAssigneeRun(params.WorkspaceID, itemID, nil, params.AssigneeID, triggeredBy)
	}
	repository.InvalidateItemListCountCache(db, params.WorkspaceID)

	// Live-update publish (WI-483): the insert has committed. Announce the new
	// item, and refresh the parent's child list if this item has a parent.
	if !params.SkipPublish {
		PublishItemChange(itemID, ItemChangeCreated)
		if params.ParentID != nil {
			PublishItemChange(*params.ParentID, ItemChangeUpdated)
		}
	}

	return int64(itemID), nil
}

// GetInitialStatusForItemType determines the initial status for an item type
// by querying the workflow assigned to the item type. Uses a two-tier override system:
// 1. First checks if there's a workflow override for this specific item type
// 2. Falls back to the configuration set's default workflow if no override exists
// Returns the status name of the first status in the workflow (where from_status_id IS NULL).
// Returns an error if the item type, configuration set, workflow, or initial status cannot be found.
func GetInitialStatusForItemType(db database.Database, itemTypeID int) (string, error) {
	// First, get the workflow ID with fallback logic:
	// 1. Check for item-type-specific override in configuration_set_item_types
	// 2. If NULL, use configuration set default workflow
	workflowQuery := `
		SELECT COALESCE(csit.workflow_id, cs.workflow_id) as workflow_id
		FROM item_types it
		LEFT JOIN configuration_set_item_types csit ON it.id = csit.item_type_id
		JOIN configuration_sets cs ON (
			csit.configuration_set_id = cs.id OR
			(csit.configuration_set_id IS NULL AND it.configuration_set_id = cs.id)
		)
		WHERE it.id = ?
		LIMIT 1
	`

	var workflowID *int
	err := db.QueryRow(workflowQuery, itemTypeID).Scan(&workflowID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("no configuration set or workflow found for item type %d", itemTypeID)
	}
	if err != nil {
		return "", fmt.Errorf("failed to query workflow: %w", err)
	}
	if workflowID == nil {
		return "", fmt.Errorf("no workflow assigned for item type %d", itemTypeID)
	}

	// Now get the initial status from the workflow
	// The initial status is identified by from_status_id IS NULL (and not a
	// from-all row) in workflow_transitions
	statusQuery := `
		SELECT s.name
		FROM workflow_transitions wt
		JOIN statuses s ON wt.to_status_id = s.id
		WHERE wt.workflow_id = ?
		  AND wt.from_status_id IS NULL
		  AND wt.from_all_statuses = FALSE
		ORDER BY wt.display_order ASC
		LIMIT 1
	`

	var statusName string
	err = db.QueryRow(statusQuery, *workflowID).Scan(&statusName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("no initial status found for workflow %d (workflow may not be configured)", *workflowID)
	}
	if err != nil {
		return "", fmt.Errorf("failed to query initial status: %w", err)
	}

	return statusName, nil
}

// nullString converts an empty string to sql.NullString
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}
