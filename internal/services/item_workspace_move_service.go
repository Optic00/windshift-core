package services

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

var (
	ErrItemWorkspaceMoveSameWorkspace   = errors.New("item is already in the destination workspace")
	ErrItemWorkspaceMoveInvalidType     = errors.New("item type is not available in the destination workspace")
	ErrItemWorkspaceMoveInvalidStatus   = errors.New("status is not available for the destination item type")
	ErrItemWorkspaceMoveInvalidPriority = errors.New("priority is not available in the destination workspace")
)

type ItemWorkspaceMoveInput struct {
	DestinationWorkspaceID int  `json:"destination_workspace_id"`
	TargetItemTypeID       int  `json:"target_item_type_id,omitempty"`
	TargetStatusID         int  `json:"target_status_id,omitempty"`
	TargetPriorityID       *int `json:"target_priority_id"`
}

type ItemWorkspaceMoveOption struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Icon      string `json:"icon,omitempty"`
	Color     string `json:"color,omitempty"`
	IsDefault bool   `json:"is_default,omitempty"`
}

type ItemWorkspaceMoveField struct {
	Field  string `json:"field"`
	Action string `json:"action"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

type ItemWorkspaceMovePreview struct {
	SourceWorkspaceID        int                       `json:"source_workspace_id"`
	SourceWorkspaceName      string                    `json:"source_workspace_name"`
	SourceKey                string                    `json:"source_key"`
	DestinationWorkspaceID   int                       `json:"destination_workspace_id"`
	DestinationWorkspaceName string                    `json:"destination_workspace_name"`
	DestinationWorkspaceKey  string                    `json:"destination_workspace_key"`
	TargetItemTypeID         int                       `json:"target_item_type_id"`
	TargetStatusID           int                       `json:"target_status_id"`
	TargetPriorityID         *int                      `json:"target_priority_id"`
	ItemTypes                []ItemWorkspaceMoveOption `json:"item_types"`
	Statuses                 []ItemWorkspaceMoveOption `json:"statuses"`
	Priorities               []ItemWorkspaceMoveOption `json:"priorities"`
	Fields                   []ItemWorkspaceMoveField  `json:"fields"`
	LabelsKept               []string                  `json:"labels_kept"`
	LabelsDropped            []string                  `json:"labels_dropped"`
	CustomFieldsKept         []string                  `json:"custom_fields_kept"`
	CustomFieldsDropped      []string                  `json:"custom_fields_dropped"`
	ChildrenDetached         int                       `json:"children_detached"`
}

type ItemWorkspaceMoveResult struct {
	Item             *models.Item              `json:"item"`
	OldKey           string                    `json:"old_key"`
	NewKey           string                    `json:"new_key"`
	Preview          *ItemWorkspaceMovePreview `json:"preview"`
	DetachedChildIDs []int                     `json:"-"`
}

type itemMoveSnapshot struct {
	ID                  int
	WorkspaceID         int
	WorkspaceItemNumber int
	WorkspaceName       string
	WorkspaceKey        string
	ItemTypeID          *int
	ItemTypeName        string
	StatusID            *int
	StatusName          string
	PriorityID          *int
	PriorityName        string
	IterationID         *int
	ProjectID           *int
	TimeProjectID       *int
	ParentID            *int
	ChannelID           *int
	RequestTypeID       *int
	CustomFieldValues   map[string]interface{}
	Path                string
}

type ItemWorkspaceMoveService struct {
	db database.Database
}

func NewItemWorkspaceMoveService(db database.Database) *ItemWorkspaceMoveService {
	return &ItemWorkspaceMoveService{db: db}
}

func (s *ItemWorkspaceMoveService) Preview(itemID int, input ItemWorkspaceMoveInput) (*ItemWorkspaceMovePreview, error) {
	item, err := s.loadSnapshot(itemID)
	if err != nil {
		return nil, err
	}
	if input.DestinationWorkspaceID == item.WorkspaceID {
		return nil, ErrItemWorkspaceMoveSameWorkspace
	}

	destinationName, destinationKey, err := s.loadDestination(input.DestinationWorkspaceID)
	if err != nil {
		return nil, err
	}
	itemTypes, err := s.listDestinationItemTypes(input.DestinationWorkspaceID)
	if err != nil {
		return nil, err
	}
	if len(itemTypes) == 0 {
		return nil, ErrItemWorkspaceMoveInvalidType
	}
	targetTypeID := pickMoveOption(input.TargetItemTypeID, item.ItemTypeID, itemTypes)
	if targetTypeID == 0 {
		return nil, ErrItemWorkspaceMoveInvalidType
	}

	statuses, err := s.listDestinationStatuses(input.DestinationWorkspaceID, targetTypeID)
	if err != nil {
		return nil, err
	}
	if len(statuses) == 0 {
		return nil, ErrItemWorkspaceMoveInvalidStatus
	}
	targetStatusID := pickMoveOption(input.TargetStatusID, item.StatusID, statuses)
	if targetStatusID == 0 {
		return nil, ErrItemWorkspaceMoveInvalidStatus
	}

	priorities, err := s.listDestinationPriorities(input.DestinationWorkspaceID)
	if err != nil {
		return nil, err
	}
	targetPriorityID := input.TargetPriorityID
	if input.TargetItemTypeID == 0 && input.TargetStatusID == 0 && input.TargetPriorityID == nil {
		targetPriorityID = pickOptionalMoveOption(item.PriorityID, priorities)
	}
	if targetPriorityID != nil && !containsMoveOption(priorities, *targetPriorityID) {
		return nil, ErrItemWorkspaceMoveInvalidPriority
	}

	preview := &ItemWorkspaceMovePreview{
		SourceWorkspaceID: item.WorkspaceID, SourceWorkspaceName: item.WorkspaceName,
		SourceKey:                fmt.Sprintf("%s-%d", item.WorkspaceKey, item.WorkspaceItemNumber),
		DestinationWorkspaceID:   input.DestinationWorkspaceID,
		DestinationWorkspaceName: destinationName, DestinationWorkspaceKey: destinationKey,
		TargetItemTypeID: targetTypeID, TargetStatusID: targetStatusID, TargetPriorityID: targetPriorityID,
		ItemTypes: itemTypes, Statuses: statuses, Priorities: priorities,
	}
	if err := s.populatePreviewMappings(preview, item); err != nil {
		return nil, err
	}
	return preview, nil
}

func pickMoveOption(requested int, current *int, options []ItemWorkspaceMoveOption) int {
	if requested > 0 {
		if containsMoveOption(options, requested) {
			return requested
		}
		return 0
	}
	if current != nil && containsMoveOption(options, *current) {
		return *current
	}
	for _, option := range options {
		if option.IsDefault {
			return option.ID
		}
	}
	return options[0].ID
}

func pickOptionalMoveOption(current *int, options []ItemWorkspaceMoveOption) *int {
	if current != nil && containsMoveOption(options, *current) {
		value := *current
		return &value
	}
	for _, option := range options {
		if option.IsDefault {
			value := option.ID
			return &value
		}
	}
	return nil
}

func containsMoveOption(options []ItemWorkspaceMoveOption, id int) bool {
	for _, option := range options {
		if option.ID == id {
			return true
		}
	}
	return false
}

func (s *ItemWorkspaceMoveService) loadSnapshot(itemID int) (*itemMoveSnapshot, error) {
	var out itemMoveSnapshot
	var itemTypeID, statusID, priorityID, iterationID, projectID, timeProjectID sql.NullInt64
	var parentID, channelID, requestTypeID sql.NullInt64
	var itemTypeName, statusName, priorityName, customJSON sql.NullString
	err := s.db.QueryRow(`
		SELECT i.id, i.workspace_id, i.workspace_item_number, w.name, w.key,
		       i.item_type_id, it.name, i.status_id, st.name, i.priority_id, p.name,
		       i.iteration_id, i.project_id, i.time_project_id, i.parent_id,
		       i.channel_id, i.request_type_id, i.custom_field_values, COALESCE(i.path, '/')
		FROM items i
		JOIN workspaces w ON w.id = i.workspace_id
		LEFT JOIN item_types it ON it.id = i.item_type_id
		LEFT JOIN statuses st ON st.id = i.status_id
		LEFT JOIN priorities p ON p.id = i.priority_id
		WHERE i.id = ?
	`, itemID).Scan(&out.ID, &out.WorkspaceID, &out.WorkspaceItemNumber, &out.WorkspaceName, &out.WorkspaceKey,
		&itemTypeID, &itemTypeName, &statusID, &statusName, &priorityID, &priorityName,
		&iterationID, &projectID, &timeProjectID, &parentID, &channelID, &requestTypeID, &customJSON, &out.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load item move snapshot: %w", err)
	}
	out.ItemTypeID, out.StatusID, out.PriorityID = nullableMoveInt(itemTypeID), nullableMoveInt(statusID), nullableMoveInt(priorityID)
	out.IterationID, out.ProjectID, out.TimeProjectID = nullableMoveInt(iterationID), nullableMoveInt(projectID), nullableMoveInt(timeProjectID)
	out.ParentID, out.ChannelID, out.RequestTypeID = nullableMoveInt(parentID), nullableMoveInt(channelID), nullableMoveInt(requestTypeID)
	out.ItemTypeName, out.StatusName, out.PriorityName = itemTypeName.String, statusName.String, priorityName.String
	out.CustomFieldValues = map[string]interface{}{}
	if customJSON.Valid && strings.TrimSpace(customJSON.String) != "" {
		if err := json.Unmarshal([]byte(customJSON.String), &out.CustomFieldValues); err != nil {
			return nil, fmt.Errorf("decode item custom fields: %w", err)
		}
	}
	return &out, nil
}

func nullableMoveInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	v := int(value.Int64)
	return &v
}

func (s *ItemWorkspaceMoveService) loadDestination(workspaceID int) (name, key string, err error) {
	err = s.db.QueryRow(`SELECT name, key FROM workspaces WHERE id = ? AND active = true`, workspaceID).Scan(&name, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", repository.ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("load destination workspace: %w", err)
	}
	return name, key, nil
}

func (s *ItemWorkspaceMoveService) listDestinationItemTypes(workspaceID int) ([]ItemWorkspaceMoveOption, error) {
	rows, err := s.db.Query(`
		SELECT it.id, it.name, COALESCE(it.icon, ''), COALESCE(it.color, ''), it.is_default
		FROM item_types it
		LEFT JOIN workspace_configuration_sets wcs ON wcs.workspace_id = ?
		LEFT JOIN configuration_set_item_types csit
		  ON csit.configuration_set_id = wcs.configuration_set_id AND csit.item_type_id = it.id
		WHERE (wcs.configuration_set_id IS NULL OR csit.id IS NOT NULL)
		  AND COALESCE(it.hierarchy_level, 0) != -1
		ORDER BY it.is_default DESC, it.hierarchy_level, it.sort_order, it.name
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list destination item types: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanMoveOptions(rows)
}

func (s *ItemWorkspaceMoveService) listDestinationStatuses(workspaceID, itemTypeID int) ([]ItemWorkspaceMoveOption, error) {
	var workflowID sql.NullInt64
	err := s.db.QueryRow(`
		SELECT COALESCE(csit.workflow_id, cs.workflow_id)
		FROM workspace_configuration_sets wcs
		JOIN configuration_sets cs ON cs.id = wcs.configuration_set_id
		JOIN configuration_set_item_types csit
		  ON csit.configuration_set_id = cs.id AND csit.item_type_id = ?
		WHERE wcs.workspace_id = ?
	`, itemTypeID, workspaceID).Scan(&workflowID)
	if errors.Is(err, sql.ErrNoRows) {
		return s.listAllStatuses()
	}
	if err != nil {
		return nil, fmt.Errorf("resolve destination workflow: %w", err)
	}
	if !workflowID.Valid {
		return s.listAllStatuses()
	}
	rows, err := s.db.Query(`
		SELECT DISTINCT s.id, s.name, '', COALESCE(sc.color, ''), s.is_default
		FROM statuses s
		JOIN status_categories sc ON sc.id = s.category_id
		WHERE s.id IN (
			SELECT to_status_id FROM workflow_transitions WHERE workflow_id = ?
			UNION
			SELECT from_status_id FROM workflow_transitions WHERE workflow_id = ? AND from_status_id IS NOT NULL
		)
		ORDER BY s.is_default DESC, s.name
	`, workflowID.Int64, workflowID.Int64)
	if err != nil {
		return nil, fmt.Errorf("list destination statuses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanMoveOptions(rows)
}

func (s *ItemWorkspaceMoveService) listAllStatuses() ([]ItemWorkspaceMoveOption, error) {
	rows, err := s.db.Query(`SELECT s.id, s.name, '', COALESCE(sc.color, ''), s.is_default FROM statuses s JOIN status_categories sc ON sc.id = s.category_id ORDER BY s.is_default DESC, s.name`)
	if err != nil {
		return nil, fmt.Errorf("list statuses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanMoveOptions(rows)
}

func (s *ItemWorkspaceMoveService) listDestinationPriorities(workspaceID int) ([]ItemWorkspaceMoveOption, error) {
	rows, err := s.db.Query(`
		SELECT p.id, p.name, COALESCE(p.icon, ''), COALESCE(p.color, ''), p.is_default
		FROM priorities p
		LEFT JOIN workspace_configuration_sets wcs ON wcs.workspace_id = ?
		LEFT JOIN configuration_set_priorities csp
		  ON csp.configuration_set_id = wcs.configuration_set_id AND csp.priority_id = p.id
		WHERE wcs.configuration_set_id IS NULL
		   OR NOT EXISTS (SELECT 1 FROM configuration_set_priorities x WHERE x.configuration_set_id = wcs.configuration_set_id)
		   OR csp.id IS NOT NULL
		ORDER BY p.is_default DESC, p.sort_order, p.name
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list destination priorities: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanMoveOptions(rows)
}

func scanMoveOptions(rows *sql.Rows) ([]ItemWorkspaceMoveOption, error) {
	options := []ItemWorkspaceMoveOption{}
	for rows.Next() {
		var option ItemWorkspaceMoveOption
		if err := rows.Scan(&option.ID, &option.Name, &option.Icon, &option.Color, &option.IsDefault); err != nil {
			return nil, err
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

func optionName(options []ItemWorkspaceMoveOption, id int) string {
	for _, option := range options {
		if option.ID == id {
			return option.Name
		}
	}
	return ""
}

func displayMoveValue(id *int, name string) string {
	if id == nil {
		return "None"
	}
	if name != "" {
		return name
	}
	return strconv.Itoa(*id)
}

func (s *ItemWorkspaceMoveService) populatePreviewMappings(preview *ItemWorkspaceMovePreview, item *itemMoveSnapshot) error {
	keptLabels, droppedLabels, err := s.previewLabels(item.ID, preview.DestinationWorkspaceID)
	if err != nil {
		return err
	}
	preview.LabelsKept, preview.LabelsDropped = keptLabels, droppedLabels

	_, keptCustom, droppedCustom, err := s.destinationCustomFields(item.CustomFieldValues, preview.DestinationWorkspaceID, preview.TargetItemTypeID)
	if err != nil {
		return err
	}
	preview.CustomFieldsKept, preview.CustomFieldsDropped = keptCustom, droppedCustom

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE parent_id = ?`, item.ID).Scan(&preview.ChildrenDetached); err != nil {
		return fmt.Errorf("count children for move preview: %w", err)
	}

	priorityName := "None"
	if preview.TargetPriorityID != nil {
		priorityName = optionName(preview.Priorities, *preview.TargetPriorityID)
	}
	preview.Fields = []ItemWorkspaceMoveField{
		{Field: "workspace", Action: "map", From: item.WorkspaceName, To: preview.DestinationWorkspaceName},
		{Field: "key", Action: "map", From: preview.SourceKey, To: preview.DestinationWorkspaceKey + "-(assigned on confirmation)"},
		{Field: "item_type", Action: "map", From: displayMoveValue(item.ItemTypeID, item.ItemTypeName), To: optionName(preview.ItemTypes, preview.TargetItemTypeID)},
		{Field: "status", Action: "map", From: displayMoveValue(item.StatusID, item.StatusName), To: optionName(preview.Statuses, preview.TargetStatusID)},
		{Field: "priority", Action: moveAction(item.PriorityID != nil, preview.TargetPriorityID != nil), From: displayMoveValue(item.PriorityID, item.PriorityName), To: priorityName},
		{Field: "parent", Action: "drop", From: presenceMoveValue(item.ParentID), To: "Workspace root"},
		{Field: "children", Action: childrenMoveAction(preview.ChildrenDetached), From: strconv.Itoa(preview.ChildrenDetached), To: "Detached to source workspace root"},
		{Field: "labels", Action: collectionMoveAction(len(keptLabels), len(droppedLabels)), From: strings.Join(append(append([]string{}, keptLabels...), droppedLabels...), ", "), To: strings.Join(keptLabels, ", ")},
		{Field: "custom_fields", Action: collectionMoveAction(len(keptCustom), len(droppedCustom)), From: strings.Join(append(append([]string{}, keptCustom...), droppedCustom...), ", "), To: strings.Join(keptCustom, ", ")},
		{Field: "iteration", Action: "drop", From: presenceMoveValue(item.IterationID), To: "None"},
		{Field: "milestones", Action: "drop", From: "Current assignments", To: "None"},
		{Field: "project", Action: "drop", From: presenceMoveValue(item.ProjectID), To: "None"},
		{Field: "time_project", Action: "drop", From: presenceMoveValue(item.TimeProjectID), To: "None"},
		{Field: "channel", Action: "drop", From: presenceMoveValue(item.ChannelID), To: "None"},
		{Field: "request_type", Action: "drop", From: presenceMoveValue(item.RequestTypeID), To: "None"},
		{Field: "calendar_schedule", Action: "drop", From: "Current schedule", To: "None"},
		{Field: "approvals", Action: "drop", From: "Current requests", To: "None"},
		{Field: "recurrence", Action: "map", From: item.WorkspaceKey, To: preview.DestinationWorkspaceKey},
		{Field: "comments_attachments_worklogs_links_watches", Action: "keep", From: "Item-scoped", To: "Unchanged"},
		{Field: "assignee_creator_reporter_relations", Action: "keep", From: "Item-scoped", To: "Unchanged"},
	}
	return nil
}

func presenceMoveValue(value *int) string {
	if value == nil {
		return "None"
	}
	return strconv.Itoa(*value)
}

func moveAction(from, to bool) string {
	if from && !to {
		return "drop"
	}
	if !from && !to {
		return "keep"
	}
	return "map"
}

func childrenMoveAction(count int) string {
	if count == 0 {
		return "keep"
	}
	return "detach"
}

func collectionMoveAction(kept, dropped int) string {
	if dropped > 0 && kept > 0 {
		return "partial"
	}
	if dropped > 0 {
		return "drop"
	}
	return "keep"
}

func (s *ItemWorkspaceMoveService) previewLabels(itemID, destinationWorkspaceID int) (kept, dropped []string, err error) {
	rows, err := s.db.Query(`
		SELECT source.name,
		       CASE WHEN destination.id IS NULL THEN false ELSE true END
		FROM item_labels il
		JOIN labels source ON source.id = il.label_id
		LEFT JOIN labels destination
		  ON destination.workspace_id = ? AND LOWER(destination.name) = LOWER(source.name)
		WHERE il.item_id = ?
		ORDER BY source.name
	`, destinationWorkspaceID, itemID)
	if err != nil {
		return nil, nil, fmt.Errorf("preview item labels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	kept, dropped = []string{}, []string{}
	for rows.Next() {
		var name string
		var available bool
		if err := rows.Scan(&name, &available); err != nil {
			return nil, nil, err
		}
		if available {
			kept = append(kept, name)
		} else {
			dropped = append(dropped, name)
		}
	}
	return kept, dropped, rows.Err()
}

func (s *ItemWorkspaceMoveService) destinationCustomFields(values map[string]interface{}, workspaceID, itemTypeID int) (keptValues map[string]interface{}, kept, dropped []string, err error) {
	if len(values) == 0 {
		return map[string]interface{}{}, []string{}, []string{}, nil
	}
	var screenID sql.NullInt64
	err = s.db.QueryRow(`
		SELECT COALESCE(csit.create_screen_id, css.screen_id)
		FROM workspace_configuration_sets wcs
		JOIN configuration_sets cs ON cs.id = wcs.configuration_set_id
		JOIN configuration_set_item_types csit
		  ON csit.configuration_set_id = cs.id AND csit.item_type_id = ?
		LEFT JOIN configuration_set_screens css
		  ON css.configuration_set_id = cs.id AND css.context = 'create'
		WHERE wcs.workspace_id = ?
	`, itemTypeID, workspaceID).Scan(&screenID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, fmt.Errorf("resolve destination screen: %w", err)
	}

	allowed := map[string]string{}
	if screenID.Valid {
		rows, err := s.db.Query(`
			SELECT sf.field_identifier, COALESCE(cfd.name, sf.field_identifier)
			FROM screen_fields sf
			LEFT JOIN custom_field_definitions cfd ON cfd.id = CAST(sf.field_identifier AS INTEGER)
			WHERE sf.screen_id = ? AND sf.field_type = 'custom'
		`, screenID.Int64)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("list destination custom fields: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return nil, nil, nil, err
			}
			allowed[id] = name
		}
		if err := rows.Err(); err != nil {
			return nil, nil, nil, err
		}
	}

	names := map[string]string{}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var name string
		if err := s.db.QueryRow(`SELECT name FROM custom_field_definitions WHERE CAST(id AS TEXT) = ?`, key).Scan(&name); err == nil {
			names[key] = name
		} else {
			names[key] = key
		}
	}

	keptValues = map[string]interface{}{}
	kept, dropped = []string{}, []string{}
	for _, key := range keys {
		if name, ok := allowed[key]; ok {
			keptValues[key] = values[key]
			kept = append(kept, name)
		} else {
			dropped = append(dropped, names[key])
		}
	}
	return keptValues, kept, dropped, nil
}

func (s *ItemWorkspaceMoveService) Move(itemID, actorUserID int, input ItemWorkspaceMoveInput) (*ItemWorkspaceMoveResult, error) {
	if input.TargetItemTypeID <= 0 {
		return nil, ErrItemWorkspaceMoveInvalidType
	}
	if input.TargetStatusID <= 0 {
		return nil, ErrItemWorkspaceMoveInvalidStatus
	}
	preview, err := s.Preview(itemID, input)
	if err != nil {
		return nil, err
	}
	item, err := s.loadSnapshot(itemID)
	if err != nil {
		return nil, err
	}
	customValues, _, _, err := s.destinationCustomFields(item.CustomFieldValues, input.DestinationWorkspaceID, input.TargetItemTypeID)
	if err != nil {
		return nil, err
	}
	customJSON, err := json.Marshal(customValues)
	if err != nil {
		return nil, fmt.Errorf("encode destination custom fields: %w", err)
	}
	labelIDs, err := s.destinationLabelIDs(itemID, input.DestinationWorkspaceID)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin item workspace move: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	newNumber, err := repository.NewItemRepository(s.db).GetNextWorkspaceItemNumber(tx, input.DestinationWorkspaceID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if _, err := tx.Exec(`
		INSERT INTO item_key_reservations (
			workspace_id, workspace_item_number, moved_item_id,
			destination_workspace_id, destination_workspace_item_number, moved_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, item.WorkspaceID, item.WorkspaceItemNumber, itemID, input.DestinationWorkspaceID, newNumber, actorUserID, now); err != nil {
		return nil, fmt.Errorf("reserve old item key: %w", err)
	}

	childIDs, err := detachMoveChildren(tx, itemID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM item_labels WHERE item_id = ?`, itemID); err != nil {
		return nil, fmt.Errorf("clear source labels: %w", err)
	}
	for _, labelID := range labelIDs {
		if _, err := tx.Exec(`INSERT INTO item_labels (item_id, label_id, created_at) VALUES (?, ?, ?)`, itemID, labelID, now); err != nil {
			return nil, fmt.Errorf("attach destination label: %w", err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM item_milestones WHERE item_id = ?`, itemID); err != nil {
		return nil, fmt.Errorf("clear milestones: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM approval_requests WHERE item_id = ?`, itemID); err != nil {
		return nil, fmt.Errorf("clear approvals: %w", err)
	}
	if _, err := tx.Exec(`UPDATE recurrence_rules SET workspace_id = ?, status_on_create = NULL, updated_at = ? WHERE template_item_id = ?`, input.DestinationWorkspaceID, now, itemID); err != nil {
		return nil, fmt.Errorf("remap recurrence: %w", err)
	}

	priorityValue := interface{}(nil)
	if input.TargetPriorityID != nil {
		priorityValue = *input.TargetPriorityID
	}
	result, err := tx.Exec(`
		UPDATE items
		SET workspace_id = ?, workspace_item_number = ?, item_type_id = ?, status_id = ?, priority_id = ?,
		    iteration_id = NULL, project_id = NULL, time_project_id = NULL, inherit_project = false,
		    custom_field_values = ?, parent_id = NULL, path = '/', channel_id = NULL,
		    request_type_id = NULL, calendar_data = NULL, updated_at = ?, last_active_at = ?
		WHERE id = ? AND workspace_id = ? AND workspace_item_number = ?
	`, input.DestinationWorkspaceID, newNumber, input.TargetItemTypeID, input.TargetStatusID, priorityValue,
		string(customJSON), now, now, itemID, item.WorkspaceID, item.WorkspaceItemNumber)
	if err != nil {
		return nil, fmt.Errorf("move item to workspace: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil || rowsAffected != 1 {
		return nil, fmt.Errorf("item changed while move was being confirmed")
	}

	newKey := fmt.Sprintf("%s-%d", preview.DestinationWorkspaceKey, newNumber)
	historyJSON, err := json.Marshal(map[string]interface{}{
		"old_key": preview.SourceKey, "new_key": newKey, "fields": preview.Fields,
		"labels_kept": preview.LabelsKept, "labels_dropped": preview.LabelsDropped,
		"custom_fields_kept": preview.CustomFieldsKept, "custom_fields_dropped": preview.CustomFieldsDropped,
	})
	if err != nil {
		return nil, fmt.Errorf("encode move history: %w", err)
	}
	if err := repository.NewItemRepository(s.db).RecordHistory(tx, repository.HistoryEntry{
		ItemID: itemID, UserID: actorUserID, FieldName: "workspace_move",
		OldValue: preview.SourceKey, NewValue: string(historyJSON), ChangedAt: now,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit item workspace move: %w", err)
	}

	updated, err := repository.NewItemRepository(s.db).FindByIDWithDetails(itemID)
	if err != nil {
		return nil, err
	}
	items := []models.Item{*updated}
	if err := repository.NewLabelRepository(s.db).LoadForItems(items); err != nil {
		return nil, err
	}
	updated = &items[0]
	PublishItemChange(itemID, ItemChangeUpdated)
	for _, childID := range childIDs {
		PublishItemChange(childID, ItemChangeUpdated)
	}
	return &ItemWorkspaceMoveResult{
		Item:             updated,
		OldKey:           preview.SourceKey,
		NewKey:           newKey,
		Preview:          preview,
		DetachedChildIDs: childIDs,
	}, nil
}

func (s *ItemWorkspaceMoveService) destinationLabelIDs(itemID, workspaceID int) ([]int, error) {
	rows, err := s.db.Query(`
		SELECT destination.id
		FROM item_labels il
		JOIN labels source ON source.id = il.label_id
		JOIN labels destination ON destination.workspace_id = ? AND LOWER(destination.name) = LOWER(source.name)
		WHERE il.item_id = ?
		ORDER BY destination.id
	`, workspaceID, itemID)
	if err != nil {
		return nil, fmt.Errorf("resolve destination labels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := []int{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func detachMoveChildren(tx database.Tx, itemID int) ([]int, error) {
	rows, err := tx.Query(`SELECT id, COALESCE(path, '/') FROM items WHERE parent_id = ? ORDER BY id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("list children to detach: %w", err)
	}
	type childPath struct {
		id   int
		path string
	}
	children := []childPath{}
	for rows.Next() {
		var child childPath
		if err := rows.Scan(&child.id, &child.path); err != nil {
			_ = rows.Close()
			return nil, err
		}
		children = append(children, child)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(children))
	for _, child := range children {
		oldPrefix := child.path + strconv.Itoa(child.id) + "/"
		newPrefix := "/" + strconv.Itoa(child.id) + "/"
		if _, err := tx.Exec(`UPDATE items SET path = ? || SUBSTR(path, ?) WHERE path LIKE ?`, newPrefix, len(oldPrefix)+1, oldPrefix+"%"); err != nil {
			return nil, fmt.Errorf("rewrite detached child descendants: %w", err)
		}
		if _, err := tx.Exec(`UPDATE items SET parent_id = NULL, path = '/', updated_at = ? WHERE id = ?`, time.Now(), child.id); err != nil {
			return nil, fmt.Errorf("detach child: %w", err)
		}
		ids = append(ids, child.id)
	}
	return ids, nil
}
