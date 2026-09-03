package services

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"windshift/internal/models"
	"windshift/internal/repository"
)

const (
	assetGraphMaxNodes = 100
	assetGraphMaxHops  = 2
)

type assetGraphReference struct {
	entityType string
	entityID   int
	title      string
	fieldName  string
}

func (s *AssetApplicationService) RelationshipGraph(userID, assetID int) (models.RelationshipGraphResponse, error) {
	asset, err := s.repo.GetAssetByID(assetID)
	if err != nil {
		return models.RelationshipGraphResponse{}, err
	}
	if err := s.require(userID, asset.SetID, AssetPermissionKeyView); err != nil {
		return models.RelationshipGraphResponse{}, err
	}
	workspaceIDs, err := s.permissions.AccessibleWorkspaceIDs(userID)
	if err != nil {
		return models.RelationshipGraphResponse{}, err
	}
	workspaces := make(map[int]bool, len(workspaceIDs))
	for _, id := range workspaceIDs {
		workspaces[id] = true
	}
	setAccess := map[int]bool{asset.SetID: true}
	canViewSet := func(setID int) bool {
		if allowed, ok := setAccess[setID]; ok {
			return allowed
		}
		allowed := s.require(userID, setID, AssetPermissionKeyView) == nil
		setAccess[setID] = allowed
		return allowed
	}

	type queueEntry struct {
		key, entityType string
		entityID, hop   int
	}
	key := func(entityType string, entityID int) string { return fmt.Sprintf("%s-%d", entityType, entityID) }
	originKey := key("asset", assetID)
	queue := []queueEntry{{key: originKey, entityType: "asset", entityID: assetID}}
	visited := map[string]bool{originKey: true}
	nodes := map[string]*models.RelationshipGraphNode{originKey: {ID: originKey, EntityID: assetID, Type: "asset", Title: asset.Title, IsOrigin: true}}
	edges, seenEdges, edgeNumber, truncated := make([]models.RelationshipGraphEdge, 0), make(map[string]bool), 0, false
	addNode := func(nodeKey, entityType string, entityID int, title string, hop int) {
		if visited[nodeKey] {
			return
		}
		if len(nodes) >= assetGraphMaxNodes {
			truncated = true
			return
		}
		visited[nodeKey] = true
		nodes[nodeKey] = &models.RelationshipGraphNode{ID: nodeKey, EntityID: entityID, Type: entityType, Title: title, Hop: hop}
		queue = append(queue, queueEntry{key: nodeKey, entityType: entityType, entityID: entityID, hop: hop})
	}
	addEdge := func(source, target, label, edgeType, color string) {
		dedup := source + ":" + target + ":" + label + ":" + edgeType
		if seenEdges[dedup] || nodes[source] == nil || nodes[target] == nil {
			return
		}
		seenEdges[dedup] = true
		edgeNumber++
		edges = append(edges, models.RelationshipGraphEdge{ID: fmt.Sprintf("e%d", edgeNumber), Source: source, Target: target, Label: label, Color: color, EdgeType: edgeType})
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.hop >= assetGraphMaxHops {
			continue
		}
		neighbors, err := s.assetGraphLinkNeighbors(current.entityType, current.entityID)
		if err != nil {
			return models.RelationshipGraphResponse{}, err
		}
		for _, neighbor := range neighbors {
			if !s.canAccessAssetGraphEntity(userID, neighbor.entityType, neighbor.entityID, workspaces, canViewSet) {
				continue
			}
			nodeKey := key(neighbor.entityType, neighbor.entityID)
			addNode(nodeKey, neighbor.entityType, neighbor.entityID, neighbor.title, current.hop+1)
			addEdge(current.key, nodeKey, neighbor.fieldName, "link", neighbor.color)
		}
		if current.entityType != "asset" {
			continue
		}
		for _, ref := range s.assetGraphIncomingFieldReferences(current.entityID, workspaces, canViewSet) {
			nodeKey := key(ref.entityType, ref.entityID)
			addNode(nodeKey, ref.entityType, ref.entityID, ref.title, current.hop+1)
			addEdge(nodeKey, current.key, "Field: "+ref.fieldName, "field_reference", "")
		}
		for _, ref := range s.assetGraphOutgoingFieldReferences(current.entityID, canViewSet) {
			nodeKey := key(ref.entityType, ref.entityID)
			addNode(nodeKey, ref.entityType, ref.entityID, ref.title, current.hop+1)
			addEdge(current.key, nodeKey, "Field: "+ref.fieldName, "field_reference", "")
		}
	}

	result := make([]models.RelationshipGraphNode, 0, len(nodes))
	for _, node := range nodes {
		node.Metadata = s.assetGraphMetadata(node.Type, node.EntityID)
		result = append(result, *node)
	}
	return models.RelationshipGraphResponse{Nodes: result, Edges: edges, Truncated: truncated, TotalCount: len(result)}, nil
}

type assetGraphNeighbor struct {
	entityType string
	entityID   int
	fieldName  string
	color      string
	title      string
}

func (s *AssetApplicationService) assetGraphLinkNeighbors(entityType string, entityID int) ([]assetGraphNeighbor, error) {
	queries := []string{
		`SELECT il.target_type, il.target_id, lt.forward_label, lt.color,
		 CASE WHEN il.target_type = 'asset' THEN (SELECT title FROM assets WHERE id = il.target_id)
		 WHEN il.target_type = 'test_case' THEN (SELECT title FROM test_cases WHERE id = il.target_id) ELSE '' END
		 FROM item_links il JOIN link_types lt ON il.link_type_id = lt.id
		 WHERE il.source_type = ? AND il.source_id = ?`,
		`SELECT il.source_type, il.source_id, lt.reverse_label, lt.color,
		 CASE WHEN il.source_type = 'asset' THEN (SELECT title FROM assets WHERE id = il.source_id)
		 WHEN il.source_type = 'test_case' THEN (SELECT title FROM test_cases WHERE id = il.source_id) ELSE '' END
		 FROM item_links il JOIN link_types lt ON il.link_type_id = lt.id
		 WHERE il.target_type = ? AND il.target_id = ?`,
	}
	result := make([]assetGraphNeighbor, 0)
	for _, query := range queries {
		rows, err := s.db.Query(query, entityType, entityID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var neighbor assetGraphNeighbor
			if err := rows.Scan(&neighbor.entityType, &neighbor.entityID, &neighbor.fieldName, &neighbor.color, &neighbor.title); err != nil {
				_ = rows.Close()
				return nil, err
			}
			result = append(result, neighbor)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	itemIndexes, itemIDs := make([]int, 0), make([]int, 0)
	for i := range result {
		if result[i].entityType == "item" {
			itemIndexes, itemIDs = append(itemIndexes, i), append(itemIDs, result[i].entityID)
		}
	}
	if len(itemIDs) > 0 {
		titles, err := repository.NewItemRepository(s.db).GetTitles(itemIDs)
		if err != nil {
			return nil, err
		}
		for _, index := range itemIndexes {
			result[index].title = titles[result[index].entityID]
		}
	}
	return result, nil
}

func (s *AssetApplicationService) canAccessAssetGraphEntity(userID int, entityType string, entityID int, workspaces map[int]bool, canViewSet func(int) bool) bool {
	switch entityType {
	case "item":
		workspaceID, err := repository.NewItemRepository(s.db).GetWorkspaceID(entityID)
		return err == nil && workspaces[workspaceID]
	case "asset":
		setID, err := s.repo.GetAssetSetID(entityID)
		return err == nil && canViewSet(setID)
	case "test_case":
		var workspaceID int
		if err := s.db.QueryRow("SELECT workspace_id FROM test_cases WHERE id = ?", entityID).Scan(&workspaceID); err != nil {
			return false
		}
		allowed, err := s.permissions.HasWorkspacePermission(userID, workspaceID, models.PermissionTestView)
		return err == nil && allowed
	default:
		return false
	}
}

func (s *AssetApplicationService) assetGraphIncomingFieldReferences(assetID int, workspaces map[int]bool, canViewSet func(int) bool) []assetGraphReference {
	fields := s.assetReferenceFields()
	result, assetIDString := make([]assetGraphReference, 0), strconv.Itoa(assetID)
	for id, name := range fields {
		fieldKey := strconv.Itoa(id)
		itemRefs, err := repository.NewItemRepository(s.db).ListItemsReferencingAssetInCustomField(fieldKey, assetIDString)
		if err == nil {
			for _, ref := range itemRefs {
				if workspaces[ref.WorkspaceID] {
					result = append(result, assetGraphReference{entityType: "item", entityID: ref.ID, title: ref.Title, fieldName: name})
				}
			}
		}
		query := s.assetFieldReferenceQuery(fieldKey)
		rows, err := s.db.Query(query, assetIDString, assetIDString, assetIDString, assetIDString)
		if err != nil {
			continue
		}
		for rows.Next() {
			var refID, setID int
			var title string
			if rows.Scan(&refID, &title, &setID) == nil && refID != assetID && canViewSet(setID) {
				result = append(result, assetGraphReference{entityType: "asset", entityID: refID, title: title, fieldName: name})
			}
		}
		if err := rows.Err(); err != nil {
			slog.Warn("failed to iterate incoming asset references", "field_id", id, "error", err)
		}
		_ = rows.Close()
	}
	return result
}

func (s *AssetApplicationService) assetFieldReferenceQuery(fieldKey string) string {
	if s.db.GetDriverName() == "postgres" {
		return fmt.Sprintf(`SELECT a.id, a.title, a.set_id FROM assets a WHERE (
		 a.custom_field_values->>'%s' = ? OR a.custom_field_values->'%s'->>'id' = ? OR EXISTS (
		 SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(a.custom_field_values->'%s') = 'array' THEN a.custom_field_values->'%s' ELSE '[]'::jsonb END) elem
		 WHERE elem #>> '{}' = ? OR elem->>'id' = ?))`, fieldKey, fieldKey, fieldKey, fieldKey)
	}
	return fmt.Sprintf(`SELECT a.id, a.title, a.set_id FROM assets a WHERE (
	 NULLIF(a.custom_field_values,'') ->> '$."%s"' = ? OR NULLIF(a.custom_field_values,'') ->> '$."%s".id' = ? OR EXISTS (
	 SELECT 1 FROM json_each(NULLIF(a.custom_field_values,'') -> '$."%s"') elem
	 WHERE CAST(elem.value AS TEXT) = ? OR elem.value ->> '$.id' = ?))`, fieldKey, fieldKey, fieldKey)
}

func (s *AssetApplicationService) assetGraphOutgoingFieldReferences(assetID int, canViewSet func(int) bool) []assetGraphReference {
	var raw sql.NullString
	if err := s.db.QueryRow("SELECT custom_field_values FROM assets WHERE id = ?", assetID).Scan(&raw); err != nil || !raw.Valid || raw.String == "" {
		return nil
	}
	values := make(map[string]json.RawMessage)
	if json.Unmarshal([]byte(raw.String), &values) != nil {
		return nil
	}
	result := make([]assetGraphReference, 0)
	for id, name := range s.assetReferenceFields() {
		references := extractAssetReferenceIDs(values[strconv.Itoa(id)])
		for _, refID := range references {
			if refID == 0 || refID == assetID {
				continue
			}
			var title string
			var setID int
			if s.db.QueryRow("SELECT title, set_id FROM assets WHERE id = ?", refID).Scan(&title, &setID) == nil && canViewSet(setID) {
				result = append(result, assetGraphReference{entityType: "asset", entityID: refID, title: title, fieldName: name})
			}
		}
	}
	return result
}

func (s *AssetApplicationService) assetReferenceFields() map[int]string {
	rows, err := s.db.Query("SELECT id, name FROM custom_field_definitions WHERE field_type = 'asset'")
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	result := make(map[int]string)
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			slog.Warn("failed to scan asset reference field", "error", err)
			continue
		}
		result[id] = name
	}
	if err := rows.Err(); err != nil {
		slog.Warn("failed to iterate asset reference fields", "error", err)
	}
	return result
}

func extractAssetReferenceIDs(raw json.RawMessage) []int {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] != '[' {
		if id, ok := extractAssetReferenceIDRaw(raw); ok && id != 0 {
			return []int{id}
		}
		return nil
	}
	var values []any
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	result := make([]int, 0, len(values))
	for _, value := range values {
		if id, ok := extractAssetReferenceID(value); ok && id != 0 {
			result = append(result, id)
		}
	}
	return result
}

func extractAssetReferenceIDRaw(raw json.RawMessage) (int, bool) {
	var id int
	if json.Unmarshal(raw, &id) == nil {
		return id, true
	}
	var value struct {
		ID int `json:"id"`
	}
	if json.Unmarshal(raw, &value) == nil {
		return value.ID, true
	}
	return 0, false
}

func extractAssetReferenceID(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case map[string]any:
		return extractAssetReferenceID(typed["id"])
	default:
		return 0, false
	}
}

func (s *AssetApplicationService) assetGraphMetadata(entityType string, entityID int) map[string]any {
	metadata := make(map[string]any)
	switch entityType {
	case "item":
		if item, err := repository.NewItemRepository(s.db).GetItemGraphMetadata(entityID); err == nil {
			metadata["display_key"] = fmt.Sprintf("%s-%d", item.WorkspaceKey, item.WorkspaceItemNumber)
			metadata["workspace_id"] = item.WorkspaceID
			if item.StatusName != "" {
				metadata["status"] = item.StatusName
			}
		}
	case "asset":
		var setID int
		var status, assetType string
		if s.db.QueryRow(`SELECT a.set_id, COALESCE(s.name, ''), COALESCE(t.name, '') FROM assets a LEFT JOIN asset_statuses s ON a.status_id = s.id LEFT JOIN asset_types t ON a.asset_type_id = t.id WHERE a.id = ?`, entityID).Scan(&setID, &status, &assetType) == nil {
			metadata["set_id"] = setID
			if status != "" {
				metadata["status"] = status
			}
			if assetType != "" {
				metadata["asset_type"] = assetType
			}
		}
	case "test_case":
		var workspaceID int
		var workspaceKey string
		if s.db.QueryRow("SELECT tc.workspace_id, w.key FROM test_cases tc JOIN workspaces w ON tc.workspace_id = w.id WHERE tc.id = ?", entityID).Scan(&workspaceID, &workspaceKey) == nil {
			metadata["workspace_id"] = workspaceID
			metadata["workspace_key"] = workspaceKey
		}
	}
	return metadata
}
