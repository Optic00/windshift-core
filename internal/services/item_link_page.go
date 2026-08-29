package services

import (
	"context"
	"fmt"
	"strings"

	"windshift/internal/database"
	"windshift/internal/models"
)

// MaxOneHopLinksPerItem bounds each anchor's direct-link page. A future
// traversal layer can call this primitive once per breadth-first frontier.
const MaxOneHopLinksPerItem = 50

// MaxWorkspaceItemLinksPerPage bounds one workspace graph response. The
// REST API shares its general pagination ceiling, but the service applies the
// same limit so non-HTTP callers cannot accidentally request an unbounded
// graph page.
const MaxWorkspaceItemLinksPerPage = 100

// OneHopItemLinksPage contains one anchor item's direct visible links.
type OneHopItemLinksPage struct {
	Outgoing    []models.ItemLink
	Incoming    []models.ItemLink
	HasMore     bool
	NextAfterID int
}

type itemLinkCandidate struct {
	anchorID int
	linkID   int
	outgoing bool
}

// ListWorkspaceItemLinksWithChecks returns one deterministic page of direct
// work-item links whose two endpoints belong to workspaceID. It intentionally
// excludes cross-workspace and non-item links: returning either would expose
// metadata from another workspace or permission domain through the joined link
// response.
//
// The caller needs item.view on the workspace. A missing permission checker
// fails closed, just like the other checked link-list operations.
func (s *ItemLinkService) ListWorkspaceItemLinksWithChecks(
	ctx context.Context,
	userID, workspaceID, limit, offset int,
) ([]models.ItemLink, int, error) {
	if workspaceID <= 0 {
		return nil, 0, &EntityNotAccessibleError{EntityType: "workspace", EntityID: workspaceID}
	}
	if limit <= 0 || limit > MaxWorkspaceItemLinksPerPage {
		limit = MaxWorkspaceItemLinksPerPage
	}
	if offset < 0 {
		return nil, 0, fmt.Errorf("workspace link offset must be non-negative")
	}

	if s.perm == nil {
		return nil, 0, &EntityNotAccessibleError{EntityType: "workspace", EntityID: workspaceID}
	}
	allowed, err := s.perm.HasWorkspacePermission(userID, workspaceID, models.PermissionItemView)
	if err != nil {
		return nil, 0, fmt.Errorf("check workspace link-list permission: %w", err)
	}
	if !allowed {
		return nil, 0, &EntityNotAccessibleError{EntityType: "workspace", EntityID: workspaceID}
	}

	// Permission is checked before existence so callers without item.view get
	// the same opaque path for present and absent workspace IDs. The separate
	// existence check still prevents system administrators and stale positive
	// permission-cache entries from turning a missing workspace into 200.
	var exists bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM workspaces WHERE id = ?)", workspaceID).Scan(&exists); err != nil {
		return nil, 0, fmt.Errorf("check workspace link-list existence: %w", err)
	}
	if !exists {
		return nil, 0, &EntityNotAccessibleError{EntityType: "workspace", EntityID: workspaceID}
	}

	return listWorkspaceItemLinksPage(ctx, s.db, workspaceID, limit, offset)
}

func listWorkspaceItemLinksPage(
	ctx context.Context,
	db database.Database,
	workspaceID, limit, offset int,
) ([]models.ItemLink, int, error) {
	// Keeping both item joins in the predicate is the security boundary for
	// this bulk surface. A link is a member of this graph only if *both*
	// endpoint items are in the requested workspace.
	const where = `il.source_type = 'item' AND il.target_type = 'item'
		AND il.custom_field_id IS NULL
		AND si.workspace_id = ? AND ti.workspace_id = ?`

	var total int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM item_links il
		JOIN items si ON il.source_type = 'item' AND il.source_id = si.id
		JOIN items ti ON il.target_type = 'item' AND il.target_id = ti.id
		WHERE `+where, workspaceID, workspaceID).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count workspace item links: %w", err)
	}

	rows, err := db.QueryContext(ctx,
		itemLinksWhereQueryWithOrder(where, "il.id ASC")+" LIMIT ? OFFSET ?",
		workspaceID, workspaceID, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list workspace item links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	links, err := scanItemLinks(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("scan workspace item links: %w", err)
	}
	if links == nil {
		links = []models.ItemLink{}
	}
	return links, total, nil
}

// ListOneHopItemLinksPageWithChecks loads one direct-link page for every
// anchor in a fixed number of queries. Links to items outside the caller's
// accessible workspaces are excluded before per-anchor ranking.
func (s *ItemLinkService) ListOneHopItemLinksPageWithChecks(
	ctx context.Context,
	userID int,
	itemIDs []int,
	afterID int,
	limit int,
	includeCustomFields bool,
) (map[int]OneHopItemLinksPage, error) {
	ids := dedupInts(itemIDs)
	result := make(map[int]OneHopItemLinksPage, len(ids))
	for _, itemID := range ids {
		result[itemID] = OneHopItemLinksPage{
			Outgoing: []models.ItemLink{},
			Incoming: []models.ItemLink{},
		}
	}
	if len(ids) == 0 {
		return result, nil
	}
	if afterID < 0 {
		return nil, fmt.Errorf("after link id must be non-negative")
	}
	if limit <= 0 || limit > MaxOneHopLinksPerItem {
		limit = MaxOneHopLinksPerItem
	}
	if s.perm == nil {
		return result, nil
	}
	workspaceIDs, err := s.perm.AccessibleWorkspaceIDs(userID)
	if err != nil {
		return nil, fmt.Errorf("load accessible workspaces for link page: %w", err)
	}
	if len(workspaceIDs) == 0 {
		return result, nil
	}

	candidates, err := s.listOneHopItemLinkCandidates(ctx, ids, workspaceIDs, afterID, limit+1, includeCustomFields)
	if err != nil {
		return nil, err
	}

	returned := make([]itemLinkCandidate, 0, len(candidates))
	linkIDSet := make(map[int]struct{}, len(candidates))
	perAnchorCount := make(map[int]int, len(ids))
	for _, candidate := range candidates {
		count := perAnchorCount[candidate.anchorID]
		if count >= limit {
			group := result[candidate.anchorID]
			group.HasMore = true
			result[candidate.anchorID] = group
			continue
		}
		perAnchorCount[candidate.anchorID] = count + 1
		returned = append(returned, candidate)
		linkIDSet[candidate.linkID] = struct{}{}
		group := result[candidate.anchorID]
		group.NextAfterID = candidate.linkID
		result[candidate.anchorID] = group
	}
	if len(returned) == 0 {
		return result, nil
	}

	linkIDs := make([]int, 0, len(linkIDSet))
	for linkID := range linkIDSet {
		linkIDs = append(linkIDs, linkID)
	}
	where := "il.id IN (" + placeholders(len(linkIDs)) + ")"
	links, err := getLinksWhereContext(ctx, s.db, where, toIfaceSlice(linkIDs)...)
	if err != nil {
		return nil, fmt.Errorf("hydrate one-hop item links: %w", err)
	}
	linksByID := make(map[int]models.ItemLink, len(links))
	for _, link := range links {
		linksByID[link.ID] = link
	}

	for _, candidate := range returned {
		link, ok := linksByID[candidate.linkID]
		if !ok {
			continue
		}
		group := result[candidate.anchorID]
		if candidate.outgoing {
			group.Outgoing = append(group.Outgoing, link)
		} else {
			group.Incoming = append(group.Incoming, link)
		}
		result[candidate.anchorID] = group
	}
	return result, nil
}

func (s *ItemLinkService) listOneHopItemLinkCandidates(
	ctx context.Context,
	itemIDs, workspaceIDs []int,
	afterID, fetchLimit int,
	includeCustomFields bool,
) ([]itemLinkCandidate, error) {
	itemPH := placeholders(len(itemIDs))
	workspacePH := placeholders(len(workspaceIDs))
	customFieldFilter := " AND il.custom_field_id IS NULL"
	if includeCustomFields {
		customFieldFilter = ""
	}

	query := `
		WITH candidates AS (
			SELECT il.source_id AS anchor_id, il.id AS link_id, 1 AS outgoing
			FROM item_links il
			JOIN items source_item ON source_item.id = il.source_id
			JOIN items target_item ON target_item.id = il.target_id
			WHERE il.source_type = 'item' AND il.target_type = 'item'
			  AND il.source_id IN (` + itemPH + `)
			  AND source_item.workspace_id IN (` + workspacePH + `)
			  AND target_item.workspace_id IN (` + workspacePH + `)
			  AND il.id > ?` + customFieldFilter + `

			UNION ALL

			SELECT il.target_id AS anchor_id, il.id AS link_id, 0 AS outgoing
			FROM item_links il
			JOIN items source_item ON source_item.id = il.source_id
			JOIN items target_item ON target_item.id = il.target_id
			WHERE il.source_type = 'item' AND il.target_type = 'item'
			  AND il.target_id IN (` + itemPH + `)
			  AND source_item.workspace_id IN (` + workspacePH + `)
			  AND target_item.workspace_id IN (` + workspacePH + `)
			  AND il.id > ?` + customFieldFilter + `
		), ranked AS (
			SELECT anchor_id, link_id, outgoing,
			       ROW_NUMBER() OVER (PARTITION BY anchor_id ORDER BY link_id ASC) AS row_number
			FROM candidates
		)
		SELECT anchor_id, link_id, outgoing
		FROM ranked
		WHERE row_number <= ?
		ORDER BY anchor_id ASC, row_number ASC`

	args := make([]any, 0, len(itemIDs)*2+len(workspaceIDs)*4+3)
	args = append(args, toIfaceSlice(itemIDs)...)
	args = append(args, toIfaceSlice(workspaceIDs)...)
	args = append(args, toIfaceSlice(workspaceIDs)...)
	args = append(args, afterID)
	args = append(args, toIfaceSlice(itemIDs)...)
	args = append(args, toIfaceSlice(workspaceIDs)...)
	args = append(args, toIfaceSlice(workspaceIDs)...)
	args = append(args, afterID, fetchLimit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query one-hop item link candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	candidates := []itemLinkCandidate{}
	for rows.Next() {
		var candidate itemLinkCandidate
		var outgoing int
		if err := rows.Scan(&candidate.anchorID, &candidate.linkID, &outgoing); err != nil {
			return nil, fmt.Errorf("scan one-hop item link candidate: %w", err)
		}
		candidate.outgoing = outgoing == 1
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate one-hop item link candidates: %w", err)
	}
	return candidates, nil
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}
