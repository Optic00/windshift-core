package board

import (
	"sort"
	"strings"

	"windshift/internal/tui/data"
)

// RowKind distinguishes group headers from item rows in the flattened list.
type RowKind int

const (
	RowHeader RowKind = iota
	RowItem
)

// Row is one line of the board's left pane: either a collapsible group
// header or a work item.
type Row struct {
	Kind       RowKind
	GroupKey   string
	GroupName  string
	GroupColor string // header: status-category color (hex, may be empty)
	Count      int    // header: items in the group (pre-filter, pre-collapse)
	Shown      int    // header: items surviving the filter
	Collapsed  bool   // header: whether the group is folded
	Item       *data.WorkItem
}

// Grouping carries the precomputed lookup state BuildRows needs. All fields
// are optional-safe: missing map entries degrade to neutral ordering.
type Grouping struct {
	// CategoryByStatusID maps a status id to its category name.
	CategoryByStatusID map[int]string
	// PriorityRank maps a priority id to its position in the workspace's
	// priority list (lower = more important).
	PriorityRank map[int]int
	// MeUserID floats the current user's items to the top of each group.
	MeUserID int
	// Collapsed holds group keys whose items are hidden.
	Collapsed map[string]bool
	// ColorByCategory maps a category name to its hex color for header
	// styling.
	ColorByCategory map[string]string
	// Filter prunes items without changing grouping; groups left empty by
	// an active filter are hidden entirely.
	Filter Filter
	// WorkspaceKey feeds "WI-123"-style filter queries for items that don't
	// carry their own key.
	WorkspaceKey string
}

const noStatusGroup = "No status"

// BuildRows flattens items into ordered rows: groups by status category
// (in-progress categories first, done-ish last), and within each group the
// current user's items first, then priority, then most recently updated.
// Pure function — the unit-testable heart of the board.
func BuildRows(items []data.WorkItem, g Grouping) []Row {
	type group struct {
		key   string
		items []*data.WorkItem // post-filter
		total int              // pre-filter
	}
	buckets := make(map[string]*group)
	var order []string // first-seen order for stable ties within a rank

	for i := range items {
		it := &items[i]
		key := noStatusGroup
		if it.StatusID != nil {
			if cat, ok := g.CategoryByStatusID[*it.StatusID]; ok && cat != "" {
				key = cat
			} else if it.StatusName != "" {
				key = it.StatusName
			}
		} else if it.Status != "" {
			key = it.Status
		}
		b, ok := buckets[key]
		if !ok {
			b = &group{key: key}
			buckets[key] = b
			order = append(order, key)
		}
		b.total++
		if g.Filter.Match(it, g.WorkspaceKey) {
			b.items = append(b.items, it)
		}
	}

	firstSeen := make(map[string]int, len(order))
	for i, k := range order {
		firstSeen[k] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ra, rb := categoryRank(order[a]), categoryRank(order[b])
		if ra != rb {
			return ra < rb
		}
		return firstSeen[order[a]] < firstSeen[order[b]]
	})

	filtering := g.Filter.Active()
	var rows []Row
	for _, key := range order {
		b := buckets[key]
		if filtering && len(b.items) == 0 {
			continue // hide groups the filter emptied
		}
		sortGroupItems(b.items, g)
		collapsed := g.Collapsed[key]
		rows = append(rows, Row{
			Kind:       RowHeader,
			GroupKey:   key,
			GroupName:  key,
			GroupColor: g.ColorByCategory[key],
			Count:      b.total,
			Shown:      len(b.items),
			Collapsed:  collapsed,
		})
		if collapsed {
			continue
		}
		for _, it := range b.items {
			rows = append(rows, Row{Kind: RowItem, GroupKey: key, Item: it})
		}
	}
	return rows
}

// categoryRank orders groups: anything in-progress-ish first, done-ish last,
// the rest (open / to-do / backlog / no status) in between.
func categoryRank(name string) int {
	n := strings.ToLower(name)
	if strings.Contains(n, "progress") || strings.Contains(n, "review") || strings.Contains(n, "doing") {
		return 0
	}
	for _, done := range []string{"done", "complete", "closed", "cancel"} {
		if strings.Contains(n, done) {
			return 2
		}
	}
	return 1
}

// sortGroupItems orders one group's items: mine first, then priority rank,
// then most recently updated (RFC3339 strings compare lexicographically).
func sortGroupItems(items []*data.WorkItem, g Grouping) {
	mine := func(it *data.WorkItem) bool {
		return g.MeUserID != 0 && it.AssigneeID != nil && *it.AssigneeID == g.MeUserID
	}
	prio := func(it *data.WorkItem) int {
		if it.PriorityID != nil {
			if r, ok := g.PriorityRank[*it.PriorityID]; ok {
				return r
			}
		}
		return int(^uint(0) >> 1) // no priority sorts last
	}
	sort.SliceStable(items, func(a, b int) bool {
		ia, ib := items[a], items[b]
		if ma, mb := mine(ia), mine(ib); ma != mb {
			return ma
		}
		if pa, pb := prio(ia), prio(ib); pa != pb {
			return pa < pb
		}
		return ia.UpdatedAt > ib.UpdatedAt
	})
}
