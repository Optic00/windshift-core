package services

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"windshift/internal/cql"
	"windshift/internal/database"
	"windshift/internal/repository"
)

// ErrAnalyticsCollectionNotFound is returned when an analytics collection does not exist.
var ErrAnalyticsCollectionNotFound = sql.ErrNoRows

// AnalyticsService provides analytics computations for collection/workspace data.
type AnalyticsService struct {
	db                database.Database
	now               func() time.Time
	workItemStaleness *WorkItemStalenessService
}

// NewAnalyticsService creates a new analytics service.
func NewAnalyticsService(db database.Database) *AnalyticsService {
	return &AnalyticsService{
		db:                db,
		now:               time.Now,
		workItemStaleness: NewWorkItemStalenessService(db),
	}
}

// GetCollectionWorkspaceID returns the workspace_id stored on the given
// collection. Used by the analytics handler to enforce that a caller cannot
// fetch analytics for a collection outside the path workspace.
func (s *AnalyticsService) GetCollectionWorkspaceID(collectionID int) (int, error) {
	return s.GetCollectionWorkspaceIDContext(context.Background(), collectionID)
}

// GetCollectionWorkspaceIDContext is the request-aware collection ownership lookup.
func (s *AnalyticsService) GetCollectionWorkspaceIDContext(ctx context.Context, collectionID int) (int, error) {
	var workspaceID sql.NullInt64
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT workspace_id FROM collections WHERE id = ?`,
		collectionID,
	).Scan(&workspaceID); err != nil {
		return 0, err
	}
	if !workspaceID.Valid {
		return 0, nil
	}
	return int(workspaceID.Int64), nil
}

// DataQuality describes whether enough data exists for meaningful analytics.
type DataQuality struct {
	Sufficient bool   `json:"sufficient"`
	Reason     string `json:"reason,omitempty"`
}

// DatasetIteration describes an iteration attached to current cohort items and
// overlapping the requested date range.
type DatasetIteration struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Status    string `json:"status"`
	TypeName  string `json:"type_name,omitempty"`
}

// DatasetSummary is the public summary of the resolved current cohort.
type DatasetSummary struct {
	TotalItems     int                `json:"total_items"`
	IterationCount int                `json:"iteration_count"`
	Iterations     []DatasetIteration `json:"iterations"`
	DateFrom       string             `json:"date_from"`
	DateTo         string             `json:"date_to"`
	CohortMode     string             `json:"cohort_mode"`
}

type dataset struct {
	Summary DatasetSummary
	ItemIDs []int
}

// ResolveDatasetParams defines how to resolve the current analytics cohort.
type ResolveDatasetParams struct {
	WorkspaceID  int
	CollectionID int
	QLQuery      string
	UserID       int
	StartDate    time.Time
	EndDate      time.Time
}

// AnalyticsResult is the delivery-health response for the analytics page.
type AnalyticsResult struct {
	SchemaVersion int                   `json:"schema_version"`
	Dataset       DatasetSummary        `json:"dataset"`
	Health        WorkHealthResult      `json:"health"`
	Throughput    ThroughputResult      `json:"throughput"`
	AgingWIP      AgingWIPResult        `json:"aging_wip"`
	DeliveryTime  DeliveryTimeResult    `json:"delivery_time"`
	Capabilities  AnalyticsCapabilities `json:"capabilities"`
}

// GetAnalytics computes analytics without an external request context.
func (s *AnalyticsService) GetAnalytics(params ResolveDatasetParams) (*AnalyticsResult, error) {
	return s.GetAnalyticsContext(context.Background(), params)
}

func (s *AnalyticsService) resolveDataset(ctx context.Context, params ResolveDatasetParams) (*dataset, error) {
	var itemIDs []int
	switch {
	case params.CollectionID > 0:
		var err error
		itemIDs, _, err = s.resolveCollectionItems(ctx, params.CollectionID, params.UserID)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve collection: %w", err)
		}
	case params.QLQuery != "":
		var err error
		itemIDs, _, err = s.resolveQLItems(
			ctx,
			params.QLQuery,
			params.WorkspaceID,
			params.UserID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve QL query: %w", err)
		}
	default:
		var err error
		itemIDs, _, err = s.resolveWorkspaceItems(ctx, params.WorkspaceID)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve workspace items: %w", err)
		}
	}

	summary := DatasetSummary{
		TotalItems: len(itemIDs),
		Iterations: []DatasetIteration{},
		DateFrom:   params.StartDate.Format("2006-01-02"),
		DateTo:     params.EndDate.Format("2006-01-02"),
		CohortMode: analyticsCohortMode(params),
	}
	if len(itemIDs) == 0 {
		return &dataset{Summary: summary, ItemIDs: []int{}}, nil
	}

	iterationIDs, err := s.extractIterationIDs(ctx, itemIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to extract iteration IDs: %w", err)
	}
	iterations, err := s.loadIterations(ctx, iterationIDs, params.StartDate, params.EndDate)
	if err != nil {
		return nil, fmt.Errorf("failed to load iterations: %w", err)
	}
	summary.IterationCount = len(iterations)
	summary.Iterations = iterations

	return &dataset{Summary: summary, ItemIDs: itemIDs}, nil
}

func (s *AnalyticsService) resolveCollectionItems(
	ctx context.Context,
	collectionID, userID int,
) (_ []int, _ int, retErr error) {
	var workspaceID sql.NullInt64
	var query sql.NullString
	err := s.db.QueryRowContext(
		ctx,
		`SELECT workspace_id, ql_query FROM collections WHERE id = ?`,
		collectionID,
	).Scan(&workspaceID, &query)
	if err != nil {
		return nil, 0, fmt.Errorf("collection not found: %w", err)
	}

	effectiveWorkspaceID := 0
	if workspaceID.Valid {
		effectiveWorkspaceID = int(workspaceID.Int64)
	}
	if !query.Valid || strings.TrimSpace(query.String) == "" {
		return []int{}, effectiveWorkspaceID, nil
	}

	ids, err := s.evaluateQLToItemIDs(ctx, query.String, effectiveWorkspaceID, userID)
	return ids, effectiveWorkspaceID, err
}

func (s *AnalyticsService) resolveQLItems(
	ctx context.Context,
	query string,
	workspaceID, userID int,
) (_ []int, _ int, retErr error) {
	ids, err := s.evaluateQLToItemIDs(ctx, query, workspaceID, userID)
	return ids, workspaceID, err
}

func (s *AnalyticsService) resolveWorkspaceItems(
	ctx context.Context,
	workspaceID int,
) (itemIDs []int, resolvedWorkspaceID int, retErr error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM items WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, workspaceID, err
	}
	defer rows.Close()

	ids := make([]int, 0)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, workspaceID, fmt.Errorf("scan workspace analytics item: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, workspaceID, rows.Err()
}

func (s *AnalyticsService) evaluateQLToItemIDs(
	ctx context.Context,
	query string,
	workspaceID, userID int,
) ([]int, error) {
	workspaceMap, err := s.buildWorkspaceMap(ctx)
	if err != nil {
		return nil, err
	}
	customFieldMap, err := repository.NewItemRepository(s.db).GetCQLCustomFieldMap()
	if err != nil {
		return nil, fmt.Errorf("failed to build custom field map: %w", err)
	}

	resolvedQuery := cql.SubstituteFunctions(query, cql.UserContext(userID))
	evaluator := cql.NewEvaluator(workspaceMap, customFieldMap, s.db.GetDriverName())
	sqlWhere, sqlArgs, err := evaluator.EvaluateToSQL(resolvedQuery)
	if err != nil {
		return nil, fmt.Errorf("CQL evaluation failed: %w", err)
	}
	if sqlWhere == "" {
		return []int{}, nil
	}

	sqlQuery := fmt.Sprintf(`SELECT i.id %s WHERE (%s)`, repository.ItemListFilterFromClause(), sqlWhere)
	if workspaceID > 0 {
		sqlQuery += ` AND i.workspace_id = ?`
		sqlArgs = append(sqlArgs, workspaceID)
	}
	rows, err := s.db.QueryContext(ctx, sqlQuery, sqlArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]int, 0)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan CQL analytics item: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *AnalyticsService) buildWorkspaceMap(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, key FROM workspaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	workspaceMap := make(map[string]int)
	for rows.Next() {
		var id int
		var name, key string
		if err := rows.Scan(&id, &name, &key); err != nil {
			return nil, fmt.Errorf("scan analytics workspace map: %w", err)
		}
		workspaceMap[fmt.Sprintf("%d", id)] = id
		workspaceMap[name] = id
		workspaceMap[key] = id
	}
	return workspaceMap, rows.Err()
}

func (s *AnalyticsService) extractIterationIDs(ctx context.Context, itemIDs []int) ([]int, error) {
	if len(itemIDs) == 0 {
		return []int{}, nil
	}

	seen := make(map[int]struct{})
	for _, chunk := range analyticsIDChunks(itemIDs) {
		placeholders, args := analyticsIDArgs(chunk)
		rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
			`SELECT DISTINCT iteration_id
			 FROM items
			 WHERE id IN (%s) AND iteration_id IS NOT NULL`,
			placeholders,
		), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan analytics iteration ID: %w", err)
			}
			seen[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids, nil
}

func (s *AnalyticsService) loadIterations(
	ctx context.Context,
	iterationIDs []int,
	startDate, endDate time.Time,
) ([]DatasetIteration, error) {
	if len(iterationIDs) == 0 {
		return []DatasetIteration{}, nil
	}

	iterations := make([]DatasetIteration, 0, len(iterationIDs))
	for _, chunk := range analyticsIDChunks(iterationIDs) {
		placeholders, args := analyticsIDArgs(chunk)
		args = append(args, endDate.Format("2006-01-02"), startDate.Format("2006-01-02"))
		rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT i.id, i.name, i.start_date, i.end_date, i.status,
			       COALESCE(it.name, '')
			FROM iterations i
			LEFT JOIN iteration_types it ON i.type_id = it.id
			WHERE i.id IN (%s)
			  AND i.start_date <= ? AND i.end_date >= ?
		`, placeholders), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var iteration DatasetIteration
			if err := rows.Scan(
				&iteration.ID,
				&iteration.Name,
				&iteration.StartDate,
				&iteration.EndDate,
				&iteration.Status,
				&iteration.TypeName,
			); err != nil {
				rows.Close()
				return nil, err
			}
			iterations = append(iterations, iteration)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	sort.Slice(iterations, func(i, j int) bool {
		if iterations[i].StartDate != iterations[j].StartDate {
			return iterations[i].StartDate < iterations[j].StartDate
		}
		return iterations[i].ID < iterations[j].ID
	})
	return iterations, nil
}
