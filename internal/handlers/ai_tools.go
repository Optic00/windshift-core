package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"windshift/internal/cql"
	"windshift/internal/database"
	"windshift/internal/services"
)

// ToolExecutor executes tool calls on behalf of the agentic chat loop.
// It enforces workspace access via a pre-computed list of accessible workspace IDs.
type ToolExecutor struct {
	db                     database.Database
	accessibleWorkspaceIDs []int
}

// NewToolExecutor creates a tool executor scoped to the given user's accessible workspaces.
func NewToolExecutor(db database.Database, accessibleWorkspaceIDs []int) *ToolExecutor {
	return &ToolExecutor{
		db:                     db,
		accessibleWorkspaceIDs: accessibleWorkspaceIDs,
	}
}

// Execute dispatches a tool call by name and returns the JSON result.
func (e *ToolExecutor) Execute(_ context.Context, name string, arguments string) (string, error) {
	switch name {
	case "list_workspaces":
		return e.listWorkspaces()
	case "get_workspace":
		return e.getWorkspace(arguments)
	case "list_items":
		return e.listItems(arguments)
	case "get_item":
		return e.getItem(arguments)
	case "search_items":
		return e.searchItems(arguments)
	case "list_milestones":
		return e.listMilestones(arguments)
	case "list_iterations":
		return e.listIterations(arguments)
	case "list_custom_fields":
		return e.listCustomFields()
	default:
		return `{"error": "unknown tool"}`, nil
	}
}

func (e *ToolExecutor) hasWorkspaceAccess(workspaceID int) bool {
	for _, id := range e.accessibleWorkspaceIDs {
		if id == workspaceID {
			return true
		}
	}
	return false
}

// listWorkspaces returns all accessible workspaces.
func (e *ToolExecutor) listWorkspaces() (string, error) {
	if len(e.accessibleWorkspaceIDs) == 0 {
		return `{"workspaces": []}`, nil
	}

	placeholders := make([]string, len(e.accessibleWorkspaceIDs))
	args := make([]interface{}, len(e.accessibleWorkspaceIDs))
	for i, id := range e.accessibleWorkspaceIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	rows, err := e.db.Query(
		fmt.Sprintf("SELECT id, name, key, description FROM workspaces WHERE id IN (%s) AND active = true ORDER BY name",
			strings.Join(placeholders, ",")),
		args...,
	)
	if err != nil {
		return "", fmt.Errorf("failed to list workspaces: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type ws struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		Key         string `json:"key"`
		Description string `json:"description,omitempty"`
	}
	var workspaces []ws
	for rows.Next() {
		var w ws
		var desc sql.NullString
		if err := rows.Scan(&w.ID, &w.Name, &w.Key, &desc); err != nil {
			continue
		}
		if desc.Valid {
			w.Description = desc.String
		}
		workspaces = append(workspaces, w)
	}
	if workspaces == nil {
		workspaces = []ws{}
	}

	b, _ := json.Marshal(map[string]interface{}{"workspaces": workspaces})
	return string(b), nil
}

// getWorkspace returns details for a single workspace.
func (e *ToolExecutor) getWorkspace(arguments string) (string, error) {
	var args struct {
		WorkspaceID int `json:"workspace_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return `{"error": "invalid arguments"}`, nil
	}
	if !e.hasWorkspaceAccess(args.WorkspaceID) {
		return `{"error": "workspace not found"}`, nil
	}

	wsSvc := services.NewWorkspaceService(e.db)
	ws, err := wsSvc.GetByID(args.WorkspaceID)
	if err != nil {
		return `{"error": "workspace not found"}`, nil
	}

	type result struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		Key         string `json:"key"`
		Description string `json:"description,omitempty"`
		Active      bool   `json:"active"`
		IsPersonal  bool   `json:"is_personal"`
	}
	b, _ := json.Marshal(result{
		ID:          ws.ID,
		Name:        ws.Name,
		Key:         ws.Key,
		Description: ws.Description,
		Active:      ws.Active,
		IsPersonal:  ws.IsPersonal,
	})
	return string(b), nil
}

// listItems returns items in a workspace with optional filters.
func (e *ToolExecutor) listItems(arguments string) (string, error) {
	var args struct {
		WorkspaceID int    `json:"workspace_id"`
		Status      string `json:"status"`
		AssigneeID  int    `json:"assignee_id"`
		Limit       int    `json:"limit"`
		Filter      string `json:"filter"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return `{"error": "invalid arguments"}`, nil
	}

	// Determine which workspaces to query
	var wsIDs []int
	if args.WorkspaceID > 0 {
		if !e.hasWorkspaceAccess(args.WorkspaceID) {
			return `{"error": "workspace not found"}`, nil
		}
		wsIDs = []int{args.WorkspaceID}
	} else {
		wsIDs = e.accessibleWorkspaceIDs
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	// Build QL filter for optional status and assignee
	var qlParts []string
	var qlArgs []interface{}
	if args.Status != "" {
		qlParts = append(qlParts, "st.name = ?")
		qlArgs = append(qlArgs, args.Status)
	}
	if args.AssigneeID > 0 {
		qlParts = append(qlParts, "i.assignee_id = ?")
		qlArgs = append(qlArgs, args.AssigneeID)
	}

	// Evaluate CQL filter expression if provided
	if args.Filter != "" {
		workspaceMap := make(map[string]int)
		wsRows, err := e.db.Query("SELECT id, name, key FROM workspaces")
		if err == nil {
			defer func() { _ = wsRows.Close() }()
			for wsRows.Next() {
				var id int
				var name, key string
				if err := wsRows.Scan(&id, &name, &key); err == nil {
					idStr := strconv.Itoa(id)
					workspaceMap[idStr] = id
					workspaceMap[strings.ToLower(name)] = id
					workspaceMap[strings.ToLower(key)] = id
				}
			}
		}

		evaluator := cql.NewEvaluator(workspaceMap, e.db.GetDriverName())
		cqlSQL, cqlArgs, err := evaluator.EvaluateToSQL(args.Filter)
		if err != nil {
			return fmt.Sprintf(`{"error": "invalid filter expression: %s"}`, err.Error()), nil
		}
		if cqlSQL != "" {
			qlParts = append(qlParts, cqlSQL)
			qlArgs = append(qlArgs, cqlArgs...)
		}
	}

	var filters services.ItemFilters
	if len(qlParts) > 0 {
		filters.QLQuery = strings.Join(qlParts, " AND ")
		filters.QLArgs = qlArgs
	}

	crudSvc := services.NewItemCRUDService(e.db)
	items, total, err := crudSvc.List(services.ItemListParams{
		WorkspaceIDs: wsIDs,
		Filters:      filters,
		SortBy:       "created_at",
		SortAsc:      false,
		Pagination:   services.PaginationParams{Limit: limit},
	})
	if err != nil {
		return `{"error": "failed to list items"}`, nil
	}

	type itemSummary struct {
		ID            int    `json:"id"`
		Key           string `json:"key"`
		Title         string `json:"title"`
		Status        string `json:"status,omitempty"`
		Priority      string `json:"priority,omitempty"`
		Assignee      string `json:"assignee,omitempty"`
		DueDate       string `json:"due_date,omitempty"`
		Type          string `json:"type,omitempty"`
		MilestoneName string `json:"milestone_name,omitempty"`
		IterationName string `json:"iteration_name,omitempty"`
		WorkspaceID   int    `json:"workspace_id"`
	}
	results := make([]itemSummary, 0, len(items))
	for _, item := range items {
		s := itemSummary{
			ID:            item.ID,
			Key:           fmt.Sprintf("%s-%d", item.WorkspaceKey, item.WorkspaceItemNumber),
			Title:         item.Title,
			Status:        item.StatusName,
			Priority:      item.PriorityName,
			Assignee:      item.AssigneeName,
			Type:          item.ItemTypeName,
			MilestoneName: item.MilestoneName,
			IterationName: item.IterationName,
			WorkspaceID:   item.WorkspaceID,
		}
		if item.DueDate != nil {
			s.DueDate = item.DueDate.Format("2006-01-02")
		}
		results = append(results, s)
	}

	b, _ := json.Marshal(map[string]interface{}{"items": results, "total": total})
	return string(b), nil
}

// getItem returns details for a single item by ID or key.
func (e *ToolExecutor) getItem(arguments string) (string, error) {
	var args struct {
		ItemID  int    `json:"item_id"`
		ItemKey string `json:"item_key"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return `{"error": "invalid arguments"}`, nil
	}

	crudSvc := services.NewItemCRUDService(e.db)
	var itemID int

	if args.ItemID > 0 {
		itemID = args.ItemID
	} else if args.ItemKey != "" {
		// Parse key like "PROJ-42"
		parts := strings.SplitN(strings.ToUpper(args.ItemKey), "-", 2)
		if len(parts) != 2 {
			return `{"error": "invalid item key format, expected KEY-NUMBER"}`, nil
		}
		num, err := strconv.Atoi(parts[1])
		if err != nil {
			return `{"error": "invalid item key format, expected KEY-NUMBER"}`, nil
		}
		// Look up by workspace key + item number
		err = e.db.QueryRow(
			"SELECT i.id FROM items i JOIN workspaces w ON i.workspace_id = w.id WHERE UPPER(w.key) = ? AND i.workspace_item_number = ?",
			parts[0], num,
		).Scan(&itemID)
		if err != nil {
			return `{"error": "item not found"}`, nil
		}
	} else {
		return `{"error": "must provide item_id or item_key"}`, nil
	}

	// Check workspace access
	wsID, err := crudSvc.GetWorkspaceID(itemID)
	if err != nil {
		return `{"error": "item not found"}`, nil
	}
	if !e.hasWorkspaceAccess(wsID) {
		return `{"error": "item not found"}`, nil
	}

	item, err := crudSvc.GetByID(itemID)
	if err != nil {
		return `{"error": "item not found"}`, nil
	}

	type itemDetail struct {
		ID          int      `json:"id"`
		Key         string   `json:"key"`
		Title       string   `json:"title"`
		Description string   `json:"description,omitempty"`
		Status      string   `json:"status,omitempty"`
		Priority    string   `json:"priority,omitempty"`
		Assignee    string   `json:"assignee,omitempty"`
		Creator     string   `json:"creator,omitempty"`
		DueDate     string   `json:"due_date,omitempty"`
		Type        string   `json:"type,omitempty"`
		Workspace   string   `json:"workspace"`
		WorkspaceID int      `json:"workspace_id"`
		Labels      []string `json:"labels,omitempty"`
	}

	d := itemDetail{
		ID:          item.ID,
		Key:         fmt.Sprintf("%s-%d", item.WorkspaceKey, item.WorkspaceItemNumber),
		Title:       item.Title,
		Status:      item.StatusName,
		Priority:    item.PriorityName,
		Assignee:    item.AssigneeName,
		Creator:     item.CreatorName,
		Type:        item.ItemTypeName,
		Workspace:   item.WorkspaceName,
		WorkspaceID: wsID,
	}
	if item.Description != "" {
		desc := item.Description
		if len(desc) > 500 {
			desc = desc[:500] + "..."
		}
		d.Description = desc
	}
	if item.DueDate != nil {
		d.DueDate = item.DueDate.Format("2006-01-02")
	}
	for _, l := range item.Labels {
		d.Labels = append(d.Labels, l.Name)
	}

	b, _ := json.Marshal(d)
	return string(b), nil
}

// searchItems searches for items by text across accessible workspaces.
func (e *ToolExecutor) searchItems(arguments string) (string, error) {
	var args struct {
		Query        string `json:"query"`
		WorkspaceIDs []int  `json:"workspace_ids"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return `{"error": "invalid arguments"}`, nil
	}
	if args.Query == "" {
		return `{"error": "query is required"}`, nil
	}

	// Filter workspace IDs to only accessible ones
	var searchWSIDs []int
	if len(args.WorkspaceIDs) > 0 {
		for _, id := range args.WorkspaceIDs {
			if e.hasWorkspaceAccess(id) {
				searchWSIDs = append(searchWSIDs, id)
			}
		}
		if len(searchWSIDs) == 0 {
			return `{"items": [], "total": 0}`, nil
		}
	} else {
		searchWSIDs = e.accessibleWorkspaceIDs
	}

	crudSvc := services.NewItemCRUDService(e.db)
	items, total, err := crudSvc.Search(args.Query, searchWSIDs, services.PaginationParams{Limit: 20})
	if err != nil {
		return `{"error": "search failed"}`, nil
	}

	type itemSummary struct {
		ID          int    `json:"id"`
		Key         string `json:"key"`
		Title       string `json:"title"`
		Status      string `json:"status,omitempty"`
		Priority    string `json:"priority,omitempty"`
		Assignee    string `json:"assignee,omitempty"`
		WorkspaceID int    `json:"workspace_id"`
	}
	results := make([]itemSummary, 0, len(items))
	for _, item := range items {
		// Filter results to accessible workspaces
		if !e.hasWorkspaceAccess(item.WorkspaceID) {
			continue
		}
		results = append(results, itemSummary{
			ID:          item.ID,
			Key:         fmt.Sprintf("%s-%d", item.WorkspaceKey, item.WorkspaceItemNumber),
			Title:       item.Title,
			Status:      item.StatusName,
			Priority:    item.PriorityName,
			Assignee:    item.AssigneeName,
			WorkspaceID: item.WorkspaceID,
		})
	}

	b, _ := json.Marshal(map[string]interface{}{"items": results, "total": total})
	return string(b), nil
}

// listMilestones returns milestones the user can see.
func (e *ToolExecutor) listMilestones(arguments string) (string, error) {
	var args struct {
		WorkspaceID   int    `json:"workspace_id"`
		Status        string `json:"status"`
		IncludeGlobal *bool  `json:"include_global"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return `{"error": "invalid arguments"}`, nil
	}

	includeGlobal := true
	if args.IncludeGlobal != nil {
		includeGlobal = *args.IncludeGlobal
	}

	query := `SELECT m.id, m.name, COALESCE(m.description, ''), m.status,
		COALESCE(CAST(m.target_date AS TEXT), ''),
		COALESCE(mc.name, ''),
		COALESCE(m.workspace_id, 0), COALESCE(w.name, '')
		FROM milestones m
		LEFT JOIN milestone_categories mc ON m.category_id = mc.id
		LEFT JOIN workspaces w ON m.workspace_id = w.id
		WHERE NOT (m.status IN ('completed', 'cancelled') AND m.updated_at < NOW() - INTERVAL '1 year')`

	var queryArgs []interface{}

	// Access control: global milestones always visible; workspace-scoped only if user has access
	var accessParts []string
	if includeGlobal {
		accessParts = append(accessParts, "m.is_global = true")
	}
	if args.WorkspaceID > 0 {
		if !e.hasWorkspaceAccess(args.WorkspaceID) {
			return `{"error": "workspace not found"}`, nil
		}
		accessParts = append(accessParts, "m.workspace_id = ?")
		queryArgs = append(queryArgs, args.WorkspaceID)
	} else if len(e.accessibleWorkspaceIDs) > 0 {
		placeholders := make([]string, len(e.accessibleWorkspaceIDs))
		for i, id := range e.accessibleWorkspaceIDs {
			placeholders[i] = "?"
			queryArgs = append(queryArgs, id)
		}
		accessParts = append(accessParts, fmt.Sprintf("m.workspace_id IN (%s)", strings.Join(placeholders, ",")))
	}
	if len(accessParts) > 0 {
		query += " AND (" + strings.Join(accessParts, " OR ") + ")"
	}

	if args.Status != "" {
		query += " AND m.status = ?"
		queryArgs = append(queryArgs, args.Status)
	}

	query += " ORDER BY m.status, m.target_date NULLS LAST, m.name"

	rows, err := e.db.Query(query, queryArgs...)
	if err != nil {
		return `{"error": "failed to list milestones"}`, nil
	}
	defer func() { _ = rows.Close() }()

	type milestone struct {
		ID            int    `json:"id"`
		Name          string `json:"name"`
		Description   string `json:"description,omitempty"`
		Status        string `json:"status"`
		TargetDate    string `json:"target_date,omitempty"`
		CategoryName  string `json:"category_name,omitempty"`
		WorkspaceID   int    `json:"workspace_id,omitempty"`
		WorkspaceName string `json:"workspace_name,omitempty"`
	}
	var milestones []milestone
	for rows.Next() {
		var ms milestone
		if err := rows.Scan(&ms.ID, &ms.Name, &ms.Description, &ms.Status, &ms.TargetDate, &ms.CategoryName, &ms.WorkspaceID, &ms.WorkspaceName); err != nil {
			continue
		}
		milestones = append(milestones, ms)
	}
	if milestones == nil {
		milestones = []milestone{}
	}

	b, _ := json.Marshal(map[string]interface{}{"milestones": milestones})
	return string(b), nil
}

// listIterations returns iterations the user can see.
func (e *ToolExecutor) listIterations(arguments string) (string, error) {
	var args struct {
		WorkspaceID   int    `json:"workspace_id"`
		Status        string `json:"status"`
		IncludeGlobal *bool  `json:"include_global"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return `{"error": "invalid arguments"}`, nil
	}

	includeGlobal := true
	if args.IncludeGlobal != nil {
		includeGlobal = *args.IncludeGlobal
	}

	query := `SELECT iter.id, iter.name, COALESCE(iter.description, ''), iter.status,
		CAST(iter.start_date AS TEXT), CAST(iter.end_date AS TEXT),
		COALESCE(it.name, ''),
		COALESCE(iter.workspace_id, 0), COALESCE(w.name, '')
		FROM iterations iter
		LEFT JOIN iteration_types it ON iter.type_id = it.id
		LEFT JOIN workspaces w ON iter.workspace_id = w.id
		WHERE NOT (iter.status IN ('completed', 'cancelled') AND iter.end_date < NOW() - INTERVAL '1 year')`

	var queryArgs []interface{}

	// Access control: global iterations always visible; workspace-scoped only if user has access
	var accessParts []string
	if includeGlobal {
		accessParts = append(accessParts, "iter.is_global = true")
	}
	if args.WorkspaceID > 0 {
		if !e.hasWorkspaceAccess(args.WorkspaceID) {
			return `{"error": "workspace not found"}`, nil
		}
		accessParts = append(accessParts, "iter.workspace_id = ?")
		queryArgs = append(queryArgs, args.WorkspaceID)
	} else if len(e.accessibleWorkspaceIDs) > 0 {
		placeholders := make([]string, len(e.accessibleWorkspaceIDs))
		for i, id := range e.accessibleWorkspaceIDs {
			placeholders[i] = "?"
			queryArgs = append(queryArgs, id)
		}
		accessParts = append(accessParts, fmt.Sprintf("iter.workspace_id IN (%s)", strings.Join(placeholders, ",")))
	}
	if len(accessParts) > 0 {
		query += " AND (" + strings.Join(accessParts, " OR ") + ")"
	}

	if args.Status != "" {
		query += " AND iter.status = ?"
		queryArgs = append(queryArgs, args.Status)
	}

	query += " ORDER BY iter.status, iter.start_date, iter.name"

	rows, err := e.db.Query(query, queryArgs...)
	if err != nil {
		return `{"error": "failed to list iterations"}`, nil
	}
	defer func() { _ = rows.Close() }()

	type iteration struct {
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
	var iterations []iteration
	for rows.Next() {
		var it iteration
		if err := rows.Scan(&it.ID, &it.Name, &it.Description, &it.Status, &it.StartDate, &it.EndDate, &it.TypeName, &it.WorkspaceID, &it.WorkspaceName); err != nil {
			continue
		}
		iterations = append(iterations, it)
	}
	if iterations == nil {
		iterations = []iteration{}
	}

	b, _ := json.Marshal(map[string]interface{}{"iterations": iterations})
	return string(b), nil
}

// listCustomFields returns all custom field definitions.
func (e *ToolExecutor) listCustomFields() (string, error) {
	rows, err := e.db.Query(
		"SELECT id, name, field_type, COALESCE(description, ''), required, COALESCE(options, '') FROM custom_field_definitions ORDER BY display_order, name",
	)
	if err != nil {
		return `{"error": "failed to list custom fields"}`, nil
	}
	defer func() { _ = rows.Close() }()

	type customField struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		FieldType   string `json:"field_type"`
		Description string `json:"description,omitempty"`
		Required    bool   `json:"required"`
		Options     string `json:"options,omitempty"`
	}
	var fields []customField
	for rows.Next() {
		var cf customField
		if err := rows.Scan(&cf.ID, &cf.Name, &cf.FieldType, &cf.Description, &cf.Required, &cf.Options); err != nil {
			continue
		}
		fields = append(fields, cf)
	}
	if fields == nil {
		fields = []customField{}
	}

	b, _ := json.Marshal(map[string]interface{}{"custom_fields": fields})
	return string(b), nil
}
