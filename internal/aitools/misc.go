package aitools

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"windshift/internal/auth"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
)

// oneYearAgoCutoff returns a dialect-appropriate SQL fragment that evaluates
// to "one year before now". Postgres uses INTERVAL syntax; SQLite uses
// datetime() with relative modifiers. Both are returned as bare expressions
// suitable for inlining into a WHERE clause.
func oneYearAgoCutoff(driver string) string {
	if driver == "postgres" {
		return "(NOW() - INTERVAL '1 year')"
	}
	return "datetime('now', '-1 year')"
}

// ----------------------------------------------------------------------------
// list_milestones
// ----------------------------------------------------------------------------

type createMilestoneArgs struct {
	WorkspaceID int    `json:"workspace_id" jsonschema:"Workspace to create the milestone in"`
	Name        string `json:"name" jsonschema:"Milestone name"`
	Description string `json:"description,omitempty" jsonschema:"Milestone description (TipTap JSON or plain text)"`
	TargetDate  string `json:"target_date,omitempty" jsonschema:"Target date in YYYY-MM-DD format"`
	Status      string `json:"status,omitempty" jsonschema:"Initial status: planning, in-progress, completed, or cancelled (default planning)"` //nolint:misspell // British spelling matches the persisted planning status
	CategoryID  *int   `json:"category_id,omitempty" jsonschema:"Milestone category ID"`
}

type listMilestonesArgs struct {
	WorkspaceID   int    `json:"workspace_id,omitempty" jsonschema:"Filter to a specific workspace"`
	Status        string `json:"status,omitempty" jsonschema:"Filter by status: planning, in-progress, completed, cancelled"` //nolint:misspell // British spelling matches the persisted planning status
	IncludeGlobal *bool  `json:"include_global,omitempty" jsonschema:"Include cross-workspace milestones (default true)"`
}

type milestoneDTO struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Status        string `json:"status"`
	TargetDate    string `json:"target_date,omitempty"`
	CategoryName  string `json:"category_name,omitempty"`
	WorkspaceID   int    `json:"workspace_id,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
}

type listMilestonesOut struct {
	Milestones []milestoneDTO `json:"milestones"`
}

func milestoneToDTO(m *services.MilestoneResult) milestoneDTO {
	result := milestoneDTO{
		ID:            m.ID,
		Name:          m.Name,
		Description:   m.Description,
		Status:        m.Status,
		TargetDate:    m.TargetDate,
		CategoryName:  m.CategoryName,
		WorkspaceName: m.WorkspaceName,
	}
	if m.WorkspaceID != nil {
		result.WorkspaceID = *m.WorkspaceID
	}
	return result
}

// ----------------------------------------------------------------------------
// list_iterations
// ----------------------------------------------------------------------------

type createIterationArgs struct {
	WorkspaceID int    `json:"workspace_id" jsonschema:"Workspace to create the iteration in"`
	Name        string `json:"name" jsonschema:"Iteration name"`
	Description string `json:"description,omitempty" jsonschema:"Iteration description (TipTap JSON or plain text)"`
	StartDate   string `json:"start_date" jsonschema:"Start date in YYYY-MM-DD format"`
	EndDate     string `json:"end_date" jsonschema:"End date in YYYY-MM-DD format"`
	Status      string `json:"status,omitempty" jsonschema:"Initial status: planned, active, completed, or cancelled (default planned)"` //nolint:misspell // British spelling matches the persisted planning status
	TypeID      *int   `json:"type_id,omitempty" jsonschema:"Iteration type ID"`
}

type listIterationsArgs struct {
	WorkspaceID   int    `json:"workspace_id,omitempty" jsonschema:"Filter to a specific workspace"`
	Status        string `json:"status,omitempty" jsonschema:"Filter by status: planned, active, completed, cancelled"` //nolint:misspell // British spelling matches the persisted planning status
	IncludeGlobal *bool  `json:"include_global,omitempty" jsonschema:"Include cross-workspace iterations (default true)"`
}

type iterationDTO struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Status        string `json:"status"`
	StartDate     string `json:"start_date"`
	EndDate       string `json:"end_date"`
	TypeName      string `json:"type_name,omitempty"`
	WorkspaceID   int    `json:"workspace_id,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
}

type listIterationsOut struct {
	Iterations []iterationDTO `json:"iterations"`
}

func iterationToDTO(iter *services.IterationResult) iterationDTO {
	result := iterationDTO{
		ID:            iter.ID,
		Name:          iter.Name,
		Description:   iter.Description,
		Status:        iter.Status,
		StartDate:     iter.StartDate,
		EndDate:       iter.EndDate,
		TypeName:      iter.TypeName,
		WorkspaceName: iter.WorkspaceName,
	}
	if iter.WorkspaceID != nil {
		result.WorkspaceID = *iter.WorkspaceID
	}
	return result
}

// ----------------------------------------------------------------------------
// list_custom_fields
// ----------------------------------------------------------------------------

type listCustomFieldsArgs struct{}

type customFieldDTO struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	FieldType   string `json:"field_type"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Options     string `json:"options,omitempty"`
}

type listCustomFieldsOut struct {
	CustomFields []customFieldDTO `json:"custom_fields"`
}

// ----------------------------------------------------------------------------
// list_recent_activity
// ----------------------------------------------------------------------------

type listRecentActivityArgs struct {
	SinceDate   string `json:"since_date,omitempty" jsonschema:"Start date (YYYY-MM-DD), defaults to yesterday"`
	WorkspaceID int    `json:"workspace_id,omitempty" jsonschema:"Filter to a specific workspace"`
	Limit       int    `json:"limit,omitempty" jsonschema:"Max items (default 50, max 100)"`
}

type recentChangeDTO struct {
	FieldName string `json:"field"`
	OldValue  string `json:"old_value,omitempty"`
	NewValue  string `json:"new_value,omitempty"`
	ChangedAt string `json:"changed_at"`
	ItemKey   string `json:"item_key"`
	ItemTitle string `json:"item_title"`
	ChangedBy string `json:"changed_by"`
}

type recentCommentDTO struct {
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	ItemKey   string `json:"item_key"`
	ItemTitle string `json:"item_title"`
	Author    string `json:"author"`
}

type listRecentActivityOut struct {
	Changes  []recentChangeDTO  `json:"changes"`
	Comments []recentCommentDTO `json:"comments"`
}

func init() {
	Register(Default, Tool[createMilestoneArgs]{
		Name:        "create_milestone",
		Description: "Create a milestone in an accessible workspace.",
		Scopes:      []string{auth.ScopeItemsWrite},
		Run: func(_ context.Context, env *Env, args createMilestoneArgs) (any, error) {
			if strings.TrimSpace(args.Name) == "" {
				return map[string]string{"error": "name is required"}, nil
			}
			if !env.HasWorkspaceAccess(args.WorkspaceID) {
				return map[string]string{"error": "workspace not found"}, nil
			}
			canEdit, err := env.PermService.HasWorkspacePermission(env.UserID, args.WorkspaceID, models.PermissionItemEdit)
			if err != nil {
				return nil, err
			}
			if !canEdit {
				return map[string]string{"error": "workspace not found"}, nil
			}
			if args.TargetDate != "" {
				if _, err := time.Parse("2006-01-02", args.TargetDate); err != nil {
					return map[string]string{"error": "invalid target_date format, use YYYY-MM-DD"}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
				}
			}

			name := args.Name
			description := args.Description
			sanitize.ApplyAllWithWarnings(
				sanitize.Pair{Target: &name, Policy: sanitize.PlainTextField, Label: "Name"},
				sanitize.Pair{Target: &description, Policy: sanitize.RichText, Label: "Description"},
			)
			if strings.TrimSpace(name) == "" {
				return map[string]string{"error": "name is required"}, nil
			}

			var targetDate *string
			if args.TargetDate != "" {
				targetDate = &args.TargetDate
			}
			workspaceID := args.WorkspaceID
			milestone, err := services.NewPlanningService(env.DB).CreateMilestone(services.CreateMilestoneParams{
				Name:        name,
				Description: description,
				TargetDate:  targetDate,
				Status:      args.Status,
				CategoryID:  args.CategoryID,
				IsGlobal:    false,
				WorkspaceID: &workspaceID,
			})
			if err != nil {
				return map[string]string{"error": fmt.Sprintf("create failed: %s", err.Error())}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}
			env.AuditWrite(logger.ResourceMilestone, milestone.ID, "create_milestone", milestone.Name)
			return milestoneToDTO(milestone), nil
		},
	})

	Register(Default, Tool[listMilestonesArgs]{
		Name:        "list_milestones",
		Description: "List milestones the user can see, with optional workspace, status and global-include filters.",
		Scopes:      []string{auth.ScopeMilestonesRead}, // cross-workspace list — matches v1 GET /milestones
		Run: func(_ context.Context, env *Env, args listMilestonesArgs) (any, error) {
			includeGlobal := true
			if args.IncludeGlobal != nil {
				includeGlobal = *args.IncludeGlobal
			}
			oneYearAgo := oneYearAgoCutoff(env.DB.GetDriverName())
			query := `SELECT m.id, m.name, COALESCE(m.description, ''), m.status,
			       COALESCE(CAST(m.target_date AS TEXT), ''),
			       COALESCE(mc.name, ''),
			       COALESCE(m.workspace_id, 0), COALESCE(w.name, '')
			       FROM milestones m
			       LEFT JOIN milestone_categories mc ON m.category_id = mc.id
			       LEFT JOIN workspaces w ON m.workspace_id = w.id
			       WHERE NOT (m.status IN ('completed', 'cancelled') AND m.updated_at < ` + oneYearAgo + `)`
			var qa []interface{}
			var accessParts []string
			if includeGlobal {
				accessParts = append(accessParts, "m.is_global = true")
			}
			if args.WorkspaceID > 0 {
				if !env.HasWorkspaceAccess(args.WorkspaceID) {
					return map[string]string{"error": "workspace not found"}, nil
				}
				accessParts = append(accessParts, "m.workspace_id = ?")
				qa = append(qa, args.WorkspaceID)
			} else if len(env.AccessibleWorkspaceIDs) > 0 {
				ph := make([]string, len(env.AccessibleWorkspaceIDs))
				for i, id := range env.AccessibleWorkspaceIDs {
					ph[i] = "?"
					qa = append(qa, id)
				}
				accessParts = append(accessParts, fmt.Sprintf("m.workspace_id IN (%s)", strings.Join(ph, ",")))
			}
			if len(accessParts) == 0 {
				return listMilestonesOut{Milestones: []milestoneDTO{}}, nil
			}
			query += " AND (" + strings.Join(accessParts, " OR ") + ")"
			if args.Status != "" {
				query += " AND m.status = ?"
				qa = append(qa, args.Status)
			}
			query += " ORDER BY m.status, m.target_date NULLS LAST, m.name"
			rows, err := env.DB.Query(query, qa...)
			if err != nil {
				return nil, err
			}
			defer func() { _ = rows.Close() }()
			out := listMilestonesOut{Milestones: []milestoneDTO{}}
			for rows.Next() {
				var m milestoneDTO
				if err := rows.Scan(&m.ID, &m.Name, &m.Description, &m.Status, &m.TargetDate, &m.CategoryName, &m.WorkspaceID, &m.WorkspaceName); err != nil {
					continue
				}
				out.Milestones = append(out.Milestones, m)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			return out, nil
		},
	})

	Register(Default, Tool[createIterationArgs]{
		Name:        "create_iteration",
		Description: "Create an iteration in an accessible workspace.",
		Scopes:      []string{auth.ScopeItemsWrite},
		Run: func(_ context.Context, env *Env, args createIterationArgs) (any, error) {
			if strings.TrimSpace(args.Name) == "" {
				return map[string]string{"error": "name is required"}, nil
			}
			if strings.TrimSpace(args.StartDate) == "" {
				return map[string]string{"error": "start_date is required"}, nil
			}
			if strings.TrimSpace(args.EndDate) == "" {
				return map[string]string{"error": "end_date is required"}, nil
			}
			if !env.HasWorkspaceAccess(args.WorkspaceID) {
				return map[string]string{"error": "workspace not found"}, nil
			}
			canEdit, err := env.PermService.HasWorkspacePermission(env.UserID, args.WorkspaceID, models.PermissionItemEdit)
			if err != nil {
				return nil, err
			}
			if !canEdit {
				return map[string]string{"error": "workspace not found"}, nil
			}
			startDate, err := time.Parse("2006-01-02", args.StartDate)
			if err != nil {
				return map[string]string{"error": "invalid start_date format, use YYYY-MM-DD"}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}
			endDate, err := time.Parse("2006-01-02", args.EndDate)
			if err != nil {
				return map[string]string{"error": "invalid end_date format, use YYYY-MM-DD"}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}
			if endDate.Before(startDate) {
				return map[string]string{"error": "end_date must be on or after start_date"}, nil
			}

			name := args.Name
			description := args.Description
			sanitize.ApplyAllWithWarnings(
				sanitize.Pair{Target: &name, Policy: sanitize.PlainTextField, Label: "Name"},
				sanitize.Pair{Target: &description, Policy: sanitize.RichText, Label: "Description"},
			)
			if strings.TrimSpace(name) == "" {
				return map[string]string{"error": "name is required"}, nil
			}

			workspaceID := args.WorkspaceID
			iteration, err := services.NewPlanningService(env.DB).CreateIteration(services.CreateIterationParams{
				Name:        name,
				Description: description,
				StartDate:   args.StartDate,
				EndDate:     args.EndDate,
				Status:      args.Status,
				TypeID:      args.TypeID,
				IsGlobal:    false,
				WorkspaceID: &workspaceID,
			})
			if err != nil {
				return map[string]string{"error": fmt.Sprintf("create failed: %s", err.Error())}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}
			env.AuditWrite(logger.ResourceIteration, iteration.ID, "create_iteration", iteration.Name)
			return iterationToDTO(iteration), nil
		},
	})

	Register(Default, Tool[listIterationsArgs]{
		Name:        "list_iterations",
		Description: "List iterations (sprints, PIs, releases) the user can see.",
		Scopes:      []string{auth.ScopeIterationsRead}, // cross-workspace list — matches v1 GET /iterations
		Run: func(_ context.Context, env *Env, args listIterationsArgs) (any, error) {
			includeGlobal := true
			if args.IncludeGlobal != nil {
				includeGlobal = *args.IncludeGlobal
			}
			oneYearAgo := oneYearAgoCutoff(env.DB.GetDriverName())
			// Iterations created without dates have NULL start_date/end_date.
			// `null < timestamp` is NULL (≈ false) in both Postgres and SQLite,
			// which would silently drop them from the result. Treat NULL
			// end_date as "not stale" so newly seeded completed iterations
			// still surface.
			query := `SELECT iter.id, iter.name, COALESCE(iter.description, ''), iter.status,
			       CAST(iter.start_date AS TEXT), CAST(iter.end_date AS TEXT),
			       COALESCE(it.name, ''),
			       COALESCE(iter.workspace_id, 0), COALESCE(w.name, '')
			       FROM iterations iter
			       LEFT JOIN iteration_types it ON iter.type_id = it.id
			       LEFT JOIN workspaces w ON iter.workspace_id = w.id
			       WHERE NOT (iter.status IN ('completed', 'cancelled') AND iter.end_date IS NOT NULL AND iter.end_date < ` + oneYearAgo + `)`
			var qa []interface{}
			var accessParts []string
			if includeGlobal {
				accessParts = append(accessParts, "iter.is_global = true")
			}
			if args.WorkspaceID > 0 {
				if !env.HasWorkspaceAccess(args.WorkspaceID) {
					return map[string]string{"error": "workspace not found"}, nil
				}
				accessParts = append(accessParts, "iter.workspace_id = ?")
				qa = append(qa, args.WorkspaceID)
			} else if len(env.AccessibleWorkspaceIDs) > 0 {
				ph := make([]string, len(env.AccessibleWorkspaceIDs))
				for i, id := range env.AccessibleWorkspaceIDs {
					ph[i] = "?"
					qa = append(qa, id)
				}
				accessParts = append(accessParts, fmt.Sprintf("iter.workspace_id IN (%s)", strings.Join(ph, ",")))
			}
			if len(accessParts) == 0 {
				return listIterationsOut{Iterations: []iterationDTO{}}, nil
			}
			query += " AND (" + strings.Join(accessParts, " OR ") + ")"
			if args.Status != "" {
				query += " AND iter.status = ?"
				qa = append(qa, args.Status)
			}
			query += " ORDER BY iter.status, iter.start_date, iter.name"
			rows, err := env.DB.Query(query, qa...)
			if err != nil {
				return nil, err
			}
			defer func() { _ = rows.Close() }()
			out := listIterationsOut{Iterations: []iterationDTO{}}
			for rows.Next() {
				var it iterationDTO
				var startDate, endDate sql.NullString
				if err := rows.Scan(&it.ID, &it.Name, &it.Description, &it.Status, &startDate, &endDate, &it.TypeName, &it.WorkspaceID, &it.WorkspaceName); err != nil {
					continue
				}
				if startDate.Valid {
					it.StartDate = startDate.String
				}
				if endDate.Valid {
					it.EndDate = endDate.String
				}
				out.Iterations = append(out.Iterations, it)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			return out, nil
		},
	})

	Register(Default, Tool[listCustomFieldsArgs]{
		Name:        "list_custom_fields",
		Description: "List available custom field definitions. Use this to discover what custom fields exist before filtering items with cf_<name> in the filter parameter of list_items.",
		Scopes:      []string{auth.ScopeCustomFieldsRead},
		Run: func(_ context.Context, env *Env, _ listCustomFieldsArgs) (any, error) {
			rows, err := env.DB.Query(
				"SELECT id, name, field_type, COALESCE(description, ''), required, COALESCE(options, '') FROM custom_field_definitions ORDER BY display_order, name",
			)
			if err != nil {
				return nil, err
			}
			defer func() { _ = rows.Close() }()
			out := listCustomFieldsOut{CustomFields: []customFieldDTO{}}
			for rows.Next() {
				var cf customFieldDTO
				if err := rows.Scan(&cf.ID, &cf.Name, &cf.FieldType, &cf.Description, &cf.Required, &cf.Options); err != nil {
					continue
				}
				out.CustomFields = append(out.CustomFields, cf)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			return out, nil
		},
	})

	Register(Default, Tool[listRecentActivityArgs]{
		Name:        "list_recent_activity",
		Description: "List recent changes and comments across accessible workspaces. Useful for understanding what happened recently.",
		Scopes:      []string{auth.ScopeItemsRead}, // activity is item history — matches v1 GET /items/{id}/history
		Run: func(_ context.Context, env *Env, args listRecentActivityArgs) (any, error) {
			sinceDate := args.SinceDate
			if sinceDate == "" {
				sinceDate = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
			}
			since, err := time.Parse("2006-01-02", sinceDate)
			if err != nil {
				return map[string]string{"error": "invalid since_date format, use YYYY-MM-DD"}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}
			limit := args.Limit
			if limit <= 0 {
				limit = 50
			}
			if limit > 100 {
				limit = 100
			}
			var wsIDs []int
			if args.WorkspaceID > 0 {
				if !env.HasWorkspaceAccess(args.WorkspaceID) {
					return map[string]string{"error": "workspace not found"}, nil
				}
				wsIDs = []int{args.WorkspaceID}
			} else {
				wsIDs = env.AccessibleWorkspaceIDs
			}
			out := listRecentActivityOut{Changes: []recentChangeDTO{}, Comments: []recentCommentDTO{}}
			if len(wsIDs) == 0 {
				return out, nil
			}
			itemRepo := repository.NewItemRepository(env.DB)
			changes, err := itemRepo.RecentItemChanges(wsIDs, since, limit)
			if err != nil {
				return nil, err
			}
			for _, c := range changes {
				out.Changes = append(out.Changes, recentChangeDTO{
					FieldName: c.FieldName,
					OldValue:  c.OldValue,
					NewValue:  c.NewValue,
					ChangedAt: c.ChangedAt.Format(time.RFC3339),
					ItemKey:   c.ItemKey,
					ItemTitle: c.Title,
					ChangedBy: c.ChangedBy,
				})
			}

			comments, err := itemRepo.RecentComments(wsIDs, since, 30)
			if err != nil {
				return nil, err
			}
			for _, c := range comments {
				cm := recentCommentDTO{
					Content:   c.Content,
					CreatedAt: c.CreatedAt.Format(time.RFC3339),
					ItemKey:   c.ItemKey,
					ItemTitle: c.Title,
					Author:    c.Author,
				}
				if len(cm.Content) > 200 {
					cm.Content = cm.Content[:200] + "..."
				}
				out.Comments = append(out.Comments, cm)
			}
			return out, nil
		},
	})
}
