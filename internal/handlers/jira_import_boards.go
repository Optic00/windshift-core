package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"windshift/internal/jira"
	"windshift/internal/models"
)

var (
	jqlOrderByPattern      = regexp.MustCompile(`(?i)\s+ORDER\s+BY\s+.+$`)
	jqlSimpleClausePattern = regexp.MustCompile(`(?is)^\s*([A-Za-z][A-Za-z0-9_ -]*)\s*(=|!=|<>|~|IN|NOT\s+IN)\s*(.+?)\s*$`)
	jqlOuterParensPattern  = regexp.MustCompile(`^\((.*)\)$`)
	jqlWhitespacePattern   = regexp.MustCompile(`\s+`)
	jqlIdentifierSafe      = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
)

// importJiraBoardsAndFilters imports Jira saved filters as Windshift collections
// and Jira Agile boards as collection-backed board configurations. The Jira API
// does not expose every JQL/board concept in a Windshift-compatible shape; when
// translation is partial, the importer records unsupported clauses in mapping
// metadata and the collection description instead of silently dropping them.
func (h *JiraImportHandler) importJiraBoardsAndFilters(ctx context.Context, jobID, projectKey string, workspaceID int, statusMap map[string]int, client jira.Client, createdByUserID int) {
	h.importJiraSavedFilters(ctx, jobID, projectKey, workspaceID, client, createdByUserID)
	h.importJiraBoards(ctx, jobID, projectKey, workspaceID, statusMap, client, createdByUserID)
}

func (h *JiraImportHandler) importJiraSavedFilters(ctx context.Context, jobID, projectKey string, workspaceID int, client jira.Client, createdByUserID int) {
	filters, err := client.ListFilters(ctx, projectKey)
	if err != nil {
		slog.Warn("Failed to list Jira saved filters",
			slog.String("component", "jira"),
			slog.String("project", projectKey),
			slog.Any("error", err))
		return
	}
	if filters == nil {
		return
	}
	for _, filter := range filters.Values {
		if strings.TrimSpace(filter.ID) == "" {
			continue
		}
		ql, unsupported := translateJQLToWindshiftQL(filter.JQL, workspaceID)
		description := jiraFilterCollectionDescription(filter, unsupported)
		metadata := map[string]any{
			"jira_entity": "filter",
			"jira_jql":    filter.JQL,
		}
		if filter.ViewURL != "" {
			metadata["view_url"] = filter.ViewURL
		}
		if len(unsupported) > 0 {
			metadata["unsupported_jql"] = unsupported
		}
		collectionID, ok := h.ensureJiraCollection(jobID, "filter:"+filter.ID, filter.ID, jiraCollectionName("Jira Filter", filter.Name, filter.ID), description, ql, workspaceID, createdByUserID, metadata)
		if ok {
			slog.Debug("Imported Jira saved filter", slog.String("component", "jira"), slog.String("filterID", filter.ID), slog.Int("collectionID", collectionID))
		}
	}
}

func (h *JiraImportHandler) importJiraBoards(ctx context.Context, jobID, projectKey string, workspaceID int, statusMap map[string]int, client jira.Client, createdByUserID int) {
	boards, err := client.ListBoards(ctx, projectKey)
	if err != nil {
		slog.Warn("Failed to list Jira boards for board import",
			slog.String("component", "jira"),
			slog.String("project", projectKey),
			slog.Any("error", err))
		return
	}
	if boards == nil {
		return
	}
	for _, board := range boards.Values {
		config, err := client.GetBoardConfiguration(ctx, board.ID)
		if err != nil {
			slog.Warn("Failed to load Jira board configuration; importing minimal board collection",
				slog.String("component", "jira"),
				slog.String("project", projectKey),
				slog.Int("boardID", board.ID),
				slog.Any("error", err))
		}

		jql := ""
		filterMeta := map[string]any{}
		if config != nil && config.Filter != nil && strings.TrimSpace(config.Filter.ID) != "" {
			filter, filterErr := client.GetFilter(ctx, config.Filter.ID)
			if filterErr != nil {
				slog.Warn("Failed to load Jira board filter JQL",
					slog.String("component", "jira"),
					slog.Int("boardID", board.ID),
					slog.String("filterID", config.Filter.ID),
					slog.Any("error", filterErr))
			} else if filter != nil {
				jql = filter.JQL
				filterMeta["jira_filter_id"] = filter.ID
				filterMeta["jira_filter_name"] = filter.Name
				filterMeta["jira_filter_jql"] = filter.JQL
			}
		}
		if strings.TrimSpace(jql) == "" {
			jql = fmt.Sprintf("project = %s", projectKey)
		}

		ql, unsupported := translateJQLToWindshiftQL(jql, workspaceID)
		metadata := map[string]any{
			"jira_entity":   "board",
			"jira_board_id": board.ID,
			"jira_type":     board.Type,
			"jira_jql":      jql,
		}
		for k, v := range filterMeta {
			metadata[k] = v
		}
		if len(unsupported) > 0 {
			metadata["unsupported_jql"] = unsupported
		}
		collectionID, ok := h.ensureJiraCollection(jobID, fmt.Sprintf("board:%d", board.ID), strconv.Itoa(board.ID), jiraCollectionName("Jira Board", board.Name, strconv.Itoa(board.ID)), jiraBoardCollectionDescription(board, config, jql, unsupported), ql, workspaceID, createdByUserID, metadata)
		if !ok {
			continue
		}

		columns, backlogStatusIDs, columnUnsupported := h.jiraBoardColumns(config, statusMap)
		if len(columns) == 0 {
			columns = h.defaultBoardColumnsFromMappedStatuses(statusMap)
		}
		if len(columnUnsupported) > 0 {
			metadata["unsupported_status_ids"] = columnUnsupported
		}
		boardConfigID, ok := h.ensureJiraBoardConfiguration(jobID, board, collectionID, columns, backlogStatusIDs, metadata)
		if ok {
			slog.Debug("Imported Jira board configuration", slog.String("component", "jira"), slog.Int("boardID", board.ID), slog.Int("boardConfigID", boardConfigID))
		}
	}
}

func jiraCollectionName(prefix, name, fallbackID string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fallbackID
	}
	return fmt.Sprintf("%s: %s", prefix, name)
}

func jiraFilterCollectionDescription(filter jira.JiraFilter, unsupported []string) string {
	parts := []string{"Imported from Jira saved filter."}
	if strings.TrimSpace(filter.Description) != "" {
		parts = append(parts, strings.TrimSpace(filter.Description))
	}
	if strings.TrimSpace(filter.JQL) != "" {
		parts = append(parts, "Original JQL:\n```jql\n"+strings.TrimSpace(filter.JQL)+"\n```")
	}
	if len(unsupported) > 0 {
		parts = append(parts, "Unsupported JQL clauses not translated into Windshift QL:\n- "+strings.Join(unsupported, "\n- "))
	}
	return strings.Join(parts, "\n\n")
}

func jiraBoardCollectionDescription(board jira.JiraBoard, config *jira.JiraBoardConfiguration, jql string, unsupported []string) string {
	parts := []string{fmt.Sprintf("Imported from Jira %s board %d.", strings.TrimSpace(board.Type), board.ID)}
	if config != nil && config.SubQuery != nil && strings.TrimSpace(config.SubQuery.Query) != "" {
		parts = append(parts, "Board sub-query preserved for reference:\n```jql\n"+strings.TrimSpace(config.SubQuery.Query)+"\n```")
	}
	if strings.TrimSpace(jql) != "" {
		parts = append(parts, "Original board/filter JQL:\n```jql\n"+strings.TrimSpace(jql)+"\n```")
	}
	if len(unsupported) > 0 {
		parts = append(parts, "Unsupported JQL clauses not translated into Windshift QL:\n- "+strings.Join(unsupported, "\n- "))
	}
	return strings.Join(parts, "\n\n")
}

func translateJQLToWindshiftQL(jql string, workspaceID int) (ql string, unsupported []string) {
	base := fmt.Sprintf("workspace_id = %d", workspaceID)
	jql = strings.TrimSpace(jql)
	if jql == "" {
		return base, nil
	}
	unsupported = []string{}
	if match := jqlOrderByPattern.FindString(jql); match != "" {
		unsupported = append(unsupported, strings.TrimSpace(match))
		jql = strings.TrimSpace(jqlOrderByPattern.ReplaceAllString(jql, ""))
	}
	clauses := splitJQLAndClauses(jql)
	qlClauses := []string{base}
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		if translated, ok := translateJQLClause(clause); ok {
			if translated != "" {
				qlClauses = append(qlClauses, translated)
			}
			continue
		}
		unsupported = append(unsupported, clause)
	}
	return strings.Join(qlClauses, " AND "), unsupported
}

func splitJQLAndClauses(jql string) []string {
	var clauses []string
	var current strings.Builder
	quote := rune(0)
	depth := 0
	runes := []rune(jql)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			current.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			current.WriteRune(r)
		case '(':
			depth++
			current.WriteRune(r)
		case ')':
			if depth > 0 {
				depth--
			}
			current.WriteRune(r)
		default:
			if depth == 0 && hasJQLKeywordAt(runes, i, "AND") {
				clauses = append(clauses, current.String())
				current.Reset()
				i += len("AND") - 1
				continue
			}
			current.WriteRune(r)
		}
	}
	clauses = append(clauses, current.String())
	return clauses
}

func hasJQLKeywordAt(runes []rune, idx int, keyword string) bool {
	if idx+len(keyword) > len(runes) {
		return false
	}
	for j, r := range keyword {
		if !strings.EqualFold(string(runes[idx+j]), string(r)) {
			return false
		}
	}
	beforeOK := idx == 0 || isJQLBoundary(runes[idx-1])
	afterIdx := idx + len(keyword)
	afterOK := afterIdx >= len(runes) || isJQLBoundary(runes[afterIdx])
	return beforeOK && afterOK
}

func isJQLBoundary(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '(' || r == ')'
}

func containsTopLevelJQLKeyword(value, keyword string) bool {
	quote := rune(0)
	depth := 0
	runes := []rune(value)
	for i, r := range runes {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && hasJQLKeywordAt(runes, i, keyword) {
				return true
			}
		}
	}
	return false
}

func translateJQLClause(clause string) (string, bool) {
	clause = strings.TrimSpace(clause)
	if m := jqlOuterParensPattern.FindStringSubmatch(clause); len(m) == 2 {
		clause = strings.TrimSpace(m[1])
	}
	if containsTopLevelJQLKeyword(clause, "OR") || containsTopLevelJQLKeyword(clause, "AND") {
		return "", false
	}
	m := jqlSimpleClausePattern.FindStringSubmatch(clause)
	if len(m) != 4 {
		return "", false
	}
	field := normalizeJQLField(m[1])
	op := normalizeJQLOperator(m[2])
	rawValue := strings.TrimSpace(m[3])

	if field == "project" {
		if op == "=" || op == "IN" {
			return "", true // represented by workspace_id in every imported collection
		}
		return "", false
	}
	qlField, valueMapper, ok := jqlFieldMapping(field)
	if !ok {
		return "", false
	}
	values, listOK := parseJQLValueList(rawValue)
	if op == "IN" || op == "NOT IN" {
		if !listOK || len(values) == 0 {
			return "", false
		}
		mapped := make([]string, 0, len(values))
		for _, value := range values {
			if mv, ok := valueMapper(value); ok {
				mapped = append(mapped, quoteQLValue(mv))
			}
		}
		if len(mapped) == 0 {
			return "", false
		}
		return fmt.Sprintf("%s %s (%s)", qlField, op, strings.Join(mapped, ", ")), true
	}
	if listOK {
		return "", false
	}
	value := unquoteJQLValue(rawValue)
	mapped, ok := valueMapper(value)
	if !ok {
		return "", false
	}
	if op == "~" {
		return fmt.Sprintf("%s ~ %s", qlField, quoteQLValue(mapped)), true
	}
	return fmt.Sprintf("%s %s %s", qlField, op, quoteQLValue(mapped)), true
}

func normalizeJQLField(field string) string {
	field = strings.ToLower(strings.TrimSpace(field))
	field = strings.ReplaceAll(field, " ", "")
	field = strings.ReplaceAll(field, "_", "")
	return field
}

func normalizeJQLOperator(op string) string {
	op = strings.ToUpper(jqlWhitespacePattern.ReplaceAllString(strings.TrimSpace(op), " "))
	if op == "<>" {
		return "!="
	}
	return op
}

func jqlFieldMapping(field string) (qlField string, valueMapper func(string) (string, bool), ok bool) {
	identity := func(v string) (string, bool) { return strings.TrimSpace(v), strings.TrimSpace(v) != "" }
	switch field {
	case "status":
		return "status", identity, true
	case "statuscategory":
		return "status_category", identity, true
	case "priority":
		return "priority", func(v string) (string, bool) { return jira.SuggestPriorityMapping(v), strings.TrimSpace(v) != "" }, true
	case "issuetype", "type":
		return "itemtypename", identity, true
	case "summary":
		return "title", identity, true
	case "description":
		return "description", identity, true
	case "labels", "label":
		return "labels", identity, true
	case "fixversion", "fixversions", "fixversion/s":
		return "milestonename", identity, true
	case "component", "components":
		return "labels", func(v string) (string, bool) {
			v = strings.TrimSpace(v)
			if v == "" {
				return "", false
			}
			return "component:" + v, true
		}, true
	case "affectedversion", "affectedversions", "affectedversion/s":
		return "labels", func(v string) (string, bool) {
			v = strings.TrimSpace(v)
			if v == "" {
				return "", false
			}
			return "affects:" + v, true
		}, true
	case "key", "issuekey":
		return "key", identity, true
	}
	return "", nil, false
}

func parseJQLValueList(raw string) ([]string, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "(") || !strings.HasSuffix(raw, ")") {
		return nil, false
	}
	inner := strings.TrimSpace(raw[1 : len(raw)-1])
	if inner == "" {
		return nil, true
	}
	var values []string
	var current strings.Builder
	quote := rune(0)
	for _, r := range inner {
		if quote != 0 {
			current.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			current.WriteRune(r)
		case ',':
			values = append(values, unquoteJQLValue(current.String()))
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	values = append(values, unquoteJQLValue(current.String()))
	return values, true
}

func unquoteJQLValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	return value
}

func quoteQLValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return `""`
	}
	if jqlIdentifierSafe.MatchString(value) && !strings.Contains(value, ":") {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func (h *JiraImportHandler) ensureJiraCollection(jobID, jiraID, jiraKey, name, description, ql string, workspaceID, createdByUserID int, metadata map[string]any) (int, bool) {
	return h.imports.EnsureCollection(jobID, jiraID, jiraKey, name, description, ql, workspaceID, createdByUserID, metadata)
}

func (h *JiraImportHandler) existingMappedEntity(jobID, entityType, jiraID string) int {
	if id, ok := h.imports.MappedEntity(jobID, entityType, jiraID); ok {
		return id
	}
	return 0
}

func (h *JiraImportHandler) jiraBoardColumns(config *jira.JiraBoardConfiguration, statusMap map[string]int) (columns []models.BoardColumnRequest, backlogStatusIDs []int, unsupported []string) {
	if config == nil || config.ColumnConfig == nil || len(config.ColumnConfig.Columns) == 0 {
		return nil, nil, nil
	}
	columns = make([]models.BoardColumnRequest, 0, len(config.ColumnConfig.Columns))
	unsupported = []string{}
	backlogStatusIDs = []int{}
	for i, jiraColumn := range config.ColumnConfig.Columns {
		statusIDs := make([]int, 0, len(jiraColumn.Statuses))
		for _, st := range jiraColumn.Statuses {
			if id, ok := statusMap[st.ID]; ok {
				statusIDs = append(statusIDs, id)
			} else if strings.TrimSpace(st.ID) != "" {
				unsupported = append(unsupported, st.ID)
			}
		}
		statusIDs = dedupeInts(statusIDs)
		if len(statusIDs) == 0 {
			continue
		}
		name := strings.TrimSpace(jiraColumn.Name)
		if name == "" {
			name = fmt.Sprintf("Column %d", i+1)
		}
		if strings.EqualFold(name, "backlog") {
			backlogStatusIDs = append(backlogStatusIDs, statusIDs...)
			continue
		}
		columns = append(columns, models.BoardColumnRequest{
			Name:         name,
			DisplayOrder: len(columns),
			WIPLimit:     jiraColumn.Max,
			Color:        boardColumnColor(len(columns)),
			StatusIDs:    statusIDs,
		})
	}
	return columns, dedupeInts(backlogStatusIDs), unsupported
}

func (h *JiraImportHandler) defaultBoardColumnsFromMappedStatuses(statusMap map[string]int) []models.BoardColumnRequest {
	statusIDs := make([]int, 0, len(statusMap))
	seen := map[int]struct{}{}
	for _, id := range statusMap {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		statusIDs = append(statusIDs, id)
	}
	sort.Ints(statusIDs)
	groups := map[int][]int{1: {}, 2: {}, 3: {}}
	categoryIDs, _ := h.imports.StatusCategoryIDs(statusIDs)
	for _, id := range statusIDs {
		categoryID := categoryIDs[id]
		groups[categoryID] = append(groups[categoryID], id)
	}
	defs := []struct {
		categoryID int
		name       string
	}{
		{1, "To Do"},
		{2, "In Progress"},
		{3, "Done"},
	}
	columns := make([]models.BoardColumnRequest, 0, len(defs))
	for _, def := range defs {
		ids := dedupeInts(groups[def.categoryID])
		if len(ids) == 0 {
			continue
		}
		columns = append(columns, models.BoardColumnRequest{Name: def.name, DisplayOrder: len(columns), Color: boardColumnColor(len(columns)), StatusIDs: ids})
	}
	return columns
}

func (h *JiraImportHandler) ensureJiraBoardConfiguration(jobID string, board jira.JiraBoard, collectionID int, columns []models.BoardColumnRequest, backlogStatusIDs []int, metadata map[string]any) (int, bool) {
	jiraID := fmt.Sprintf("board:%d", board.ID)
	if existingID := h.existingMappedEntity(jobID, "board_configuration", jiraID); existingID > 0 {
		return existingID, true
	}
	if len(columns) == 0 {
		slog.Warn("Skipping Jira board configuration with no mapped columns", slog.String("component", "jira"), slog.Int("boardID", board.ID), slog.String("board", board.Name))
		return 0, false
	}
	listColumns := defaultImportedBoardListColumns()
	cardFields := defaultImportedBoardCardFields()
	return h.imports.EnsureBoardConfiguration(jobID, jiraID, board.Name, collectionID, &models.BoardConfigurationRequest{
		Columns: columns, BacklogStatusIDs: dedupeInts(backlogStatusIDs),
		ListColumns: listColumns, CardFields: cardFields, RoadmapConfig: &models.RoadmapConfig{},
	}, metadata)
}

func defaultImportedBoardListColumns() []models.ListColumn {
	fields := []string{"key", "title", "status", "priority", "assignee"}
	cols := make([]models.ListColumn, 0, len(fields))
	for i, field := range fields {
		cols = append(cols, models.ListColumn{FieldIdentifier: field, FieldType: "system", DisplayOrder: i, Width: 1})
	}
	return cols
}

func defaultImportedBoardCardFields() []models.ListColumn {
	fields := []string{"key", "title", "priority", "assignee"}
	cols := make([]models.ListColumn, 0, len(fields))
	for i, field := range fields {
		cols = append(cols, models.ListColumn{FieldIdentifier: field, FieldType: "system", DisplayOrder: i, Width: 1})
	}
	return cols
}

func boardColumnColor(index int) string {
	colors := []string{"#64748b", "#3b82f6", "#22c55e", "#f59e0b", "#a855f7", "#ef4444"}
	return colors[index%len(colors)]
}

func dedupeInts(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}
