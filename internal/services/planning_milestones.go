package services

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/repository"
)

// PlanningService encapsulates business logic for milestones, iterations, and projects.
type PlanningService struct {
	db       database.Database
	items    *repository.ItemRepository
	statuses *repository.StatusRepository
}

// milestonePositionStep is the gap left between adjacent positions so that a
// single insert between two milestones rarely needs a full renormalization.
// Matches the test-folder reorder convention (1000).
const milestonePositionStep = 1000

var ErrInvalidMilestoneReorder = errors.New("invalid milestone reorder")

// milestoneScopeClause builds the SQL WHERE fragment that pins a milestone
// to its reorder scope. Position is scoped per (is_global, workspace_id,
// category_id); COALESCE normalizes NULL workspace_id / category_id to 0 so
// every row in the same logical group compares equal. The fragment uses ?
// placeholders consumed by the scope's parameters (returned by
// milestoneScopeArgs) — callers must append those args in the same order.
func milestoneScopeClause() string {
	return "is_global = ? AND COALESCE(workspace_id, 0) = ? AND COALESCE(category_id, 0) = ?"
}

// milestoneScopeArgs returns the args for milestoneScopeClause for a scope.
func milestoneScopeArgs(isGlobal bool, workspaceID *int, categoryID *int) []interface{} {
	ws := 0
	if workspaceID != nil {
		ws = *workspaceID
	}
	cat := 0
	if categoryID != nil {
		cat = *categoryID
	}
	return []interface{}{isGlobal, ws, cat}
}

// NewPlanningService creates a new PlanningService.
func NewPlanningService(db database.Database) *PlanningService {
	return &PlanningService{
		db:       db,
		items:    repository.NewItemRepository(db),
		statuses: repository.NewStatusRepository(db),
	}
}

// milestoneScanner is satisfied by both *sql.Row and *sql.Rows.
type milestoneScanner interface {
	Scan(dest ...interface{}) error
}

// scanMilestoneRow scans a single milestone row (with LEFT JOIN release columns)
// into a MilestoneResult. The column order must match the standard milestone query.
func scanMilestoneRow(sc milestoneScanner) (MilestoneResult, error) {
	var m MilestoneResult
	var description, targetDate, categoryName, categoryColor, workspaceName, externalKey sql.NullString
	var categoryID, workspaceID sql.NullInt64
	// Release columns
	var mrID, mrCreatedBy, mrSCMConnectionID sql.NullInt64
	var mrTagName, mrName, mrBody, mrTargetCommitish sql.NullString
	var mrSCMRepository, mrSCMReleaseID, mrSCMReleaseURL sql.NullString
	var mrIsDraft, mrIsPrerelease sql.NullBool
	var mrCreatedAt sql.NullString

	err := sc.Scan(&m.ID, &m.Name, &description, &targetDate, &m.Status, &categoryID,
		&categoryName, &categoryColor, &m.IsGlobal, &workspaceID, &workspaceName,
		&externalKey, &m.Position,
		&mrID, &mrTagName, &mrName, &mrBody, &mrIsDraft, &mrIsPrerelease,
		&mrTargetCommitish, &mrSCMConnectionID, &mrSCMRepository,
		&mrSCMReleaseID, &mrSCMReleaseURL, &mrCreatedBy, &mrCreatedAt,
		&m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return m, err
	}

	m.Description = description.String
	m.TargetDate = targetDate.String
	m.CategoryName = categoryName.String
	m.CategoryColor = categoryColor.String
	m.WorkspaceName = workspaceName.String
	if externalKey.Valid {
		ek := externalKey.String
		m.ExternalKey = &ek
	}
	if categoryID.Valid {
		id := int(categoryID.Int64)
		m.CategoryID = &id
	}
	if workspaceID.Valid {
		id := int(workspaceID.Int64)
		m.WorkspaceID = &id
	}
	m.LatestRelease = hydrateMilestoneRelease(m.ID,
		mrID, mrCreatedBy, mrSCMConnectionID,
		mrTagName, mrName, mrBody, mrTargetCommitish,
		mrSCMRepository, mrSCMReleaseID, mrSCMReleaseURL,
		mrIsDraft, mrIsPrerelease, mrCreatedAt,
	)

	return m, nil
}

// scanMilestones scans all rows from a milestone query into a slice.
func scanMilestones(rows *sql.Rows) ([]MilestoneResult, error) { //nolint:unparam // error is always nil but kept for consistency with scan pattern
	var milestones []MilestoneResult
	for rows.Next() {
		m, err := scanMilestoneRow(rows)
		if err != nil {
			continue
		}
		milestones = append(milestones, m)
	}
	if milestones == nil {
		milestones = []MilestoneResult{}
	}
	return milestones, nil
}

// MilestoneReleaseResult represents a release record for a milestone.
type MilestoneReleaseResult struct {
	ID              int
	MilestoneID     int
	TagName         string
	Name            string
	Body            string
	IsDraft         bool
	IsPrerelease    bool
	TargetCommitish string
	SCMConnectionID *int
	SCMRepository   *string
	SCMReleaseID    *string
	SCMReleaseURL   *string
	CreatedBy       *int
	CreatedAt       string
}

// MilestoneResult represents a milestone with category details.
type MilestoneResult struct {
	ID            int
	Name          string
	Description   string
	TargetDate    string
	Status        string
	CategoryID    *int
	CategoryName  string
	CategoryColor string
	IsGlobal      bool
	WorkspaceID   *int
	WorkspaceName string
	ExternalKey   *string
	Position      int
	LatestRelease *MilestoneReleaseResult
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// milestoneOrderByClause returns the ORDER BY clause for ListMilestones.
// With no client sort requested (SortBy empty), milestones use their manual
// drag-and-drop order (position), with name as a stable tiebreaker. When the
// client requests an explicit sort, that column wins; unknown sort keys fall
// back to the manual order to avoid injecting arbitrary SQL.
func milestoneOrderByClause(sortBy, sortOrder string) string {
	dir := "ASC"
	if strings.EqualFold(sortOrder, "desc") {
		dir = "DESC"
	}
	switch sortBy {
	case "name":
		return " ORDER BY m.name " + dir + ", m.position ASC"
	case "target_date":
		// NULL dates sort last in ascending order on both backends via the
		// IS NULL tiebreaker, keeping behavior consistent.
		return " ORDER BY m.target_date IS NULL ASC, m.target_date " + dir + ", m.name ASC, m.position ASC"
	case "status":
		return " ORDER BY m.status " + dir + ", m.position ASC"
	case "created_at", "updated_at":
		return " ORDER BY m." + sortBy + " " + dir + ", m.position ASC"
	default:
		return " ORDER BY m.position ASC, m.name ASC"
	}
}

// MilestoneListParams contains parameters for listing milestones.
type MilestoneListParams struct {
	Limit         int
	Offset        int
	WorkspaceID   *int   // Filter by workspace
	CategoryID    *int   // Filter by category
	Status        string // Filter by status
	IncludeGlobal bool   // Include global milestones
	// SortBy overrides the default manual-position ordering. When empty,
	// results are ordered by position then name (the drag-and-drop order).
	// When set (e.g. "name", "target_date", "status"), the client sort wins.
	SortBy string
	// SortOrder is "asc" or "desc"; defaults to "asc" when SortBy is set.
	SortOrder string
}

// ListMilestones retrieves milestones with pagination and filtering.
func (s *PlanningService) ListMilestones(params MilestoneListParams) ([]MilestoneResult, int, error) {
	query := `
		SELECT m.id, m.name, m.description, m.target_date, m.status, m.category_id,
		       mc.name as category_name, mc.color as category_color,
		       m.is_global, m.workspace_id, w.name as workspace_name,
		       m.external_key, m.position,
		       mr.id, mr.tag_name, mr.name, mr.body, mr.is_draft, mr.is_prerelease,
		       mr.target_commitish, mr.scm_connection_id, mr.scm_repository,
		       mr.scm_release_id, mr.scm_release_url, mr.created_by, mr.created_at,
		       m.created_at, m.updated_at
		FROM milestones m
		LEFT JOIN milestone_categories mc ON m.category_id = mc.id
		LEFT JOIN workspaces w ON m.workspace_id = w.id
		LEFT JOIN (
			SELECT * FROM milestone_releases
			WHERE id IN (
				SELECT MAX(id) FROM milestone_releases GROUP BY milestone_id
			)
		) mr ON mr.milestone_id = m.id
		WHERE 1=1`

	countQuery := "SELECT COUNT(*) FROM milestones m WHERE 1=1"
	var args []interface{}
	var countArgs []interface{}

	// Filter by workspace - show local milestones for this workspace + optionally global milestones
	if params.WorkspaceID != nil {
		if params.IncludeGlobal {
			query += " AND (m.workspace_id = ? OR m.is_global = ?)"
			countQuery += " AND (m.workspace_id = ? OR m.is_global = ?)"
			args = append(args, *params.WorkspaceID, true)
			countArgs = append(countArgs, *params.WorkspaceID, true)
		} else {
			query += " AND m.workspace_id = ?"
			countQuery += " AND m.workspace_id = ?"
			args = append(args, *params.WorkspaceID)
			countArgs = append(countArgs, *params.WorkspaceID)
		}
	} else if params.IncludeGlobal {
		// If no workspace specified but include_global, only show global milestones
		query += " AND m.is_global = ?"
		countQuery += " AND m.is_global = ?"
		args = append(args, true)
		countArgs = append(countArgs, true)
	}

	// Filter by category
	if params.CategoryID != nil {
		if *params.CategoryID == 0 {
			query += " AND m.category_id IS NULL"
			countQuery += " AND m.category_id IS NULL"
		} else {
			query += " AND m.category_id = ?"
			countQuery += " AND m.category_id = ?"
			args = append(args, *params.CategoryID)
			countArgs = append(countArgs, *params.CategoryID)
		}
	}

	// Filter by status
	if params.Status != "" {
		query += " AND m.status = ?"
		countQuery += " AND m.status = ?"
		args = append(args, params.Status)
		countArgs = append(countArgs, params.Status)
	}

	query += milestoneOrderByClause(params.SortBy, params.SortOrder)
	query += " LIMIT ? OFFSET ?"
	args = append(args, params.Limit, params.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list milestones: %w", err)
	}
	defer rows.Close()

	milestones, _ := scanMilestones(rows)

	var total int
	if err := s.db.QueryRow(countQuery, countArgs...).Scan(&total); err != nil {
		slog.Warn("failed to get milestone pagination count", slog.Any("error", err))
	}

	return milestones, total, nil
}

// GetMilestone retrieves a milestone by ID.
func (s *PlanningService) GetMilestone(id int) (*MilestoneResult, error) {
	row := s.db.QueryRow(`
		SELECT m.id, m.name, m.description, m.target_date, m.status, m.category_id,
		       mc.name as category_name, mc.color as category_color,
		       m.is_global, m.workspace_id, w.name as workspace_name,
		       m.external_key, m.position,
		       mr.id, mr.tag_name, mr.name, mr.body, mr.is_draft, mr.is_prerelease,
		       mr.target_commitish, mr.scm_connection_id, mr.scm_repository,
		       mr.scm_release_id, mr.scm_release_url, mr.created_by, mr.created_at,
		       m.created_at, m.updated_at
		FROM milestones m
		LEFT JOIN milestone_categories mc ON m.category_id = mc.id
		LEFT JOIN workspaces w ON m.workspace_id = w.id
		LEFT JOIN (
			SELECT * FROM milestone_releases
			WHERE id IN (
				SELECT MAX(id) FROM milestone_releases GROUP BY milestone_id
			)
		) mr ON mr.milestone_id = m.id
		WHERE m.id = ?
	`, id)

	m, err := scanMilestoneRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("milestone not found: %d", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get milestone: %w", err)
	}

	return &m, nil
}

// hydrateMilestoneRelease builds a MilestoneReleaseResult from nullable scan variables.
// Returns nil if mrID is not valid (no release row).
func hydrateMilestoneRelease(
	milestoneID int,
	mrID, mrCreatedBy, mrSCMConnectionID sql.NullInt64,
	mrTagName, mrName, mrBody, mrTargetCommitish sql.NullString,
	mrSCMRepository, mrSCMReleaseID, mrSCMReleaseURL sql.NullString,
	mrIsDraft, mrIsPrerelease sql.NullBool,
	mrCreatedAt sql.NullString,
) *MilestoneReleaseResult {
	if !mrID.Valid {
		return nil
	}
	rel := &MilestoneReleaseResult{
		ID:              int(mrID.Int64),
		MilestoneID:     milestoneID,
		TagName:         mrTagName.String,
		Name:            mrName.String,
		Body:            mrBody.String,
		CreatedAt:       mrCreatedAt.String,
		TargetCommitish: mrTargetCommitish.String,
	}
	if mrIsDraft.Valid {
		rel.IsDraft = mrIsDraft.Bool
	}
	if mrIsPrerelease.Valid {
		rel.IsPrerelease = mrIsPrerelease.Bool
	}
	if mrSCMConnectionID.Valid {
		cid := int(mrSCMConnectionID.Int64)
		rel.SCMConnectionID = &cid
	}
	if mrSCMRepository.Valid {
		rel.SCMRepository = &mrSCMRepository.String
	}
	if mrSCMReleaseID.Valid {
		rel.SCMReleaseID = &mrSCMReleaseID.String
	}
	if mrSCMReleaseURL.Valid {
		rel.SCMReleaseURL = &mrSCMReleaseURL.String
	}
	if mrCreatedBy.Valid {
		cb := int(mrCreatedBy.Int64)
		rel.CreatedBy = &cb
	}
	return rel
}

// GetSCMConnectionWorkspaceID returns the workspace_id for a given SCM connection ID.
// Returns 0 and no error if the connection doesn't exist.
func (s *PlanningService) GetSCMConnectionWorkspaceID(connectionID int) (int, error) {
	var workspaceID int
	err := s.db.QueryRow(`SELECT workspace_id FROM workspace_scm_connections WHERE id = ?`, connectionID).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get SCM connection workspace: %w", err)
	}
	return workspaceID, nil
}

// CreateMilestoneParams contains parameters for creating a milestone.
// ExternalKey is a stable upsert key used by automation (e.g. the
// create_milestone action's `{{ref.short}}`); a non-empty value is unique
// per workspace (see uq_milestones_workspace_external_key).
type CreateMilestoneParams struct {
	Name        string
	Description string
	TargetDate  *string
	Status      string
	CategoryID  *int
	IsGlobal    bool
	WorkspaceID *int
	ExternalKey *string
}

// CreateMilestone creates a new milestone.
func (s *PlanningService) CreateMilestone(params CreateMilestoneParams) (*MilestoneResult, error) {
	status := params.Status
	if status == "" {
		status = "planning"
	}

	// New milestones land at the end of their scope's manual order. Position
	// is scoped per (is_global, workspace_id, category_id); MaxMilestonePosition
	// returns the current max for this scope, and we step by 1000 to leave
	// gaps for future inserts (mirrors the test-folder reorder convention).
	position, err := s.MaxMilestonePosition(params.IsGlobal, params.WorkspaceID, params.CategoryID)
	if err != nil {
		return nil, fmt.Errorf("failed to compute milestone position: %w", err)
	}
	position += milestonePositionStep

	var id int64
	err = s.db.QueryRow(`
		INSERT INTO milestones (name, description, target_date, status, category_id, is_global, workspace_id, external_key, position)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id
	`, params.Name, params.Description, params.TargetDate, status, params.CategoryID, params.IsGlobal, params.WorkspaceID, params.ExternalKey, position).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("failed to create milestone: %w", err)
	}

	return s.GetMilestone(int(id))
}

// FindMilestoneByExternalKey returns the milestone with the given
// (workspace_id, external_key). Returns (nil, nil) when no row matches —
// the executor treats that as "create a new one." Errors are reserved
// for actual DB failures.
func (s *PlanningService) FindMilestoneByExternalKey(workspaceID int, externalKey string) (*MilestoneResult, error) {
	if externalKey == "" {
		return nil, nil
	}
	var id int
	err := s.db.QueryRow(`
		SELECT id FROM milestones
		WHERE workspace_id = ? AND external_key = ?
	`, workspaceID, externalKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find milestone by external_key: %w", err)
	}
	return s.GetMilestone(id)
}

// SetMilestoneStatus updates only the status column on a milestone scoped
// to the given workspace. Used by automation to promote a "planning"
// milestone to "in-progress" or "completed" without disturbing the other
// fields. Returns a "not found" error when no row matches the scope.
func (s *PlanningService) SetMilestoneStatus(milestoneID, workspaceID int, status string) error {
	res, err := s.db.ExecWrite(`
		UPDATE milestones
		SET status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND workspace_id = ?
	`, status, milestoneID, workspaceID)
	if err != nil {
		return fmt.Errorf("failed to set milestone status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to read update result: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("milestone not found in workspace: %d", milestoneID)
	}
	return nil
}

// AttachRelease inserts a milestone_releases row without flipping the
// parent milestone's status. The standalone ReleaseMilestone (above) sets
// status to 'completed' atomically with the insert; AttachRelease is the
// version automation uses when it wants to keep status under explicit
// control (e.g. "tag promotes branch milestone to in-progress, not
// completed").
func (s *PlanningService) AttachRelease(params ReleaseMilestoneParams) error {
	_, err := s.db.ExecWrite(`
		INSERT INTO milestone_releases (
			milestone_id, tag_name, name, body, is_draft, is_prerelease,
			target_commitish, scm_connection_id, scm_repository, scm_release_id,
			scm_release_url, created_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, params.ID, params.TagName, params.Name, params.Body, params.IsDraft, params.IsPrerelease,
		params.TargetCommitish, params.SCMConnectionID, params.SCMRepository, params.SCMReleaseID,
		params.SCMReleaseURL, params.CreatedBy)
	if err != nil {
		return fmt.Errorf("failed to insert milestone release: %w", err)
	}
	return nil
}

// UpdateMilestoneParams contains parameters for updating a milestone.
// The scope (WorkspaceID) determines which rows the UPDATE may touch:
// nil means a global milestone (WHERE is_global = 1), non-nil scopes the
// UPDATE to that workspace. Cross-scope updates are impossible — the WHERE
// clause filters out milestones owned by another workspace, returning 0 rows
// affected, which surfaces as a "not found" error.
type UpdateMilestoneParams struct {
	ID          int
	Name        string
	Description string
	TargetDate  *string
	Status      string
	CategoryID  *int
	WorkspaceID *int // nil = global milestone
}

// UpdateMilestone updates an existing milestone within its declared scope.
// is_global / workspace_id cannot be changed via this method.
func (s *PlanningService) UpdateMilestone(params UpdateMilestoneParams) (*MilestoneResult, error) {
	var (
		res sql.Result
		err error
	)
	if params.WorkspaceID == nil {
		res, err = s.db.ExecWrite(`
			UPDATE milestones SET name = ?, description = ?, target_date = ?, status = ?, category_id = ?,
			       updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND is_global = true
		`, params.Name, params.Description, params.TargetDate, params.Status, params.CategoryID, params.ID)
	} else {
		res, err = s.db.ExecWrite(`
			UPDATE milestones SET name = ?, description = ?, target_date = ?, status = ?, category_id = ?,
			       updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND workspace_id = ? AND is_global = false
		`, params.Name, params.Description, params.TargetDate, params.Status, params.CategoryID, params.ID, *params.WorkspaceID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update milestone: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to read update result: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("milestone not found: %d", params.ID)
	}
	return s.GetMilestone(params.ID)
}

// ListMilestoneReleases fetches all releases for a given milestone, ordered by created_at DESC.
func (s *PlanningService) ListMilestoneReleases(milestoneID int) ([]MilestoneReleaseResult, error) {
	rows, err := s.db.Query(`
		SELECT id, milestone_id, tag_name, name, body, is_draft, is_prerelease,
		       target_commitish, scm_connection_id, scm_repository,
		       scm_release_id, scm_release_url, created_by, created_at
		FROM milestone_releases
		WHERE milestone_id = ?
		ORDER BY created_at DESC
	`, milestoneID)
	if err != nil {
		return nil, fmt.Errorf("failed to list milestone releases: %w", err)
	}
	defer rows.Close()

	var releases []MilestoneReleaseResult
	for rows.Next() {
		var r MilestoneReleaseResult
		var name, body, targetCommitish sql.NullString
		var scmConnectionID, createdBy sql.NullInt64
		var scmRepository, scmReleaseID, scmReleaseURL sql.NullString
		var isDraft, isPrerelease sql.NullBool

		if err := rows.Scan(&r.ID, &r.MilestoneID, &r.TagName, &name, &body,
			&isDraft, &isPrerelease, &targetCommitish, &scmConnectionID, &scmRepository,
			&scmReleaseID, &scmReleaseURL, &createdBy, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan milestone release: %w", err)
		}

		r.Name = name.String
		r.Body = body.String
		r.TargetCommitish = targetCommitish.String
		if isDraft.Valid {
			r.IsDraft = isDraft.Bool
		}
		if isPrerelease.Valid {
			r.IsPrerelease = isPrerelease.Bool
		}
		if scmConnectionID.Valid {
			cid := int(scmConnectionID.Int64)
			r.SCMConnectionID = &cid
		}
		if scmRepository.Valid {
			r.SCMRepository = &scmRepository.String
		}
		if scmReleaseID.Valid {
			r.SCMReleaseID = &scmReleaseID.String
		}
		if scmReleaseURL.Valid {
			r.SCMReleaseURL = &scmReleaseURL.String
		}
		if createdBy.Valid {
			cb := int(createdBy.Int64)
			r.CreatedBy = &cb
		}

		releases = append(releases, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate milestone releases: %w", err)
	}

	return releases, nil
}

// ReleaseMilestoneParams contains parameters for releasing a milestone.
type ReleaseMilestoneParams struct {
	ID              int
	TagName         string
	Name            string
	Body            string
	IsDraft         bool
	IsPrerelease    bool
	TargetCommitish string
	SCMConnectionID *int
	SCMRepository   *string
	SCMReleaseID    *string
	SCMReleaseURL   *string
	CreatedBy       *int
}

// ReleaseMilestone inserts a release record and marks the milestone as completed.
func (s *PlanningService) ReleaseMilestone(params ReleaseMilestoneParams) (*MilestoneResult, error) {
	_, err := s.db.ExecWrite(`
		INSERT INTO milestone_releases (
			milestone_id, tag_name, name, body, is_draft, is_prerelease,
			target_commitish, scm_connection_id, scm_repository, scm_release_id,
			scm_release_url, created_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, params.ID, params.TagName, params.Name, params.Body, params.IsDraft, params.IsPrerelease,
		params.TargetCommitish, params.SCMConnectionID, params.SCMRepository, params.SCMReleaseID,
		params.SCMReleaseURL, params.CreatedBy)
	if err != nil {
		return nil, fmt.Errorf("failed to insert milestone release: %w", err)
	}

	_, err = s.db.ExecWrite(`
		UPDATE milestones SET status = 'completed', updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, params.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to update milestone status: %w", err)
	}

	return s.GetMilestone(params.ID)
}

// DeleteMilestone deletes a milestone.
func (s *PlanningService) DeleteMilestone(id int) error {
	_, err := s.db.ExecWrite("DELETE FROM milestones WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete milestone: %w", err)
	}
	return nil
}

// MilestoneTestStats contains test plan statistics for a milestone.
type MilestoneTestStats struct {
	TotalTestPlans     int `json:"total_test_plans"`
	TotalTestRuns      int `json:"total_test_runs"`
	SuccessfulTestRuns int `json:"successful_test_runs"`
	FailedTestRuns     int `json:"failed_test_runs"`
	InProgressTestRuns int `json:"in_progress_test_runs"`
	TotalTestCases     int `json:"total_test_cases"`
}

// GetMilestoneTestStatistics retrieves test plan statistics for a milestone.
func (s *PlanningService) GetMilestoneTestStatistics(milestoneID int) (*MilestoneTestStats, error) {
	var stats MilestoneTestStats

	err := s.db.QueryRow(`
		SELECT
			COUNT(DISTINCT ts.id) as total_test_plans,
			COALESCE(SUM(run_stats.total_runs), 0) as total_test_runs,
			COALESCE(SUM(run_stats.successful_runs), 0) as successful_test_runs,
			COALESCE(SUM(run_stats.failed_runs), 0) as failed_test_runs,
			COALESCE(SUM(run_stats.in_progress_runs), 0) as in_progress_test_runs,
			COALESCE(SUM(tc_counts.test_case_count), 0) as total_test_cases
		FROM test_sets ts
		LEFT JOIN (
			SELECT
				set_id,
				COUNT(*) as total_runs,
				SUM(CASE WHEN ended_at IS NOT NULL THEN 1 ELSE 0 END) as successful_runs,
				SUM(CASE WHEN ended_at IS NULL THEN 1 ELSE 0 END) as failed_runs,
				0 as in_progress_runs
			FROM test_runs
			GROUP BY set_id
		) run_stats ON ts.id = run_stats.set_id
		LEFT JOIN (
			SELECT
				stc.set_id,
				COUNT(stc.test_case_id) as test_case_count
			FROM set_test_cases stc
			GROUP BY stc.set_id
		) tc_counts ON ts.id = tc_counts.set_id
		WHERE ts.milestone_id = ?
	`, milestoneID).Scan(
		&stats.TotalTestPlans,
		&stats.TotalTestRuns,
		&stats.SuccessfulTestRuns,
		&stats.FailedTestRuns,
		&stats.InProgressTestRuns,
		&stats.TotalTestCases,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get milestone test statistics: %w", err)
	}

	return &stats, nil
}

// MilestoneProgressReport represents the full milestone progress data.
type MilestoneProgressReport struct {
	MilestoneID     int                       `json:"milestone_id"`
	MilestoneName   string                    `json:"milestone_name"`
	Description     string                    `json:"description,omitempty"`
	TargetDate      *string                   `json:"target_date,omitempty"`
	Status          string                    `json:"status"`
	CategoryColor   string                    `json:"category_color,omitempty"`
	TotalItems      int                       `json:"total_items"`
	CompletedItems  int                       `json:"completed_items"`
	PercentComplete float64                   `json:"percent_complete"`
	StatusBreakdown []StatusBreakdown         `json:"status_breakdown"`
	ItemsByCategory map[string][]ProgressItem `json:"items_by_category"`
}

// GetMilestoneProgress retrieves progress report for a milestone.
func (s *PlanningService) GetMilestoneProgress(milestoneID int) (*MilestoneProgressReport, error) {
	var report MilestoneProgressReport
	report.MilestoneID = milestoneID
	// Get milestone details
	var description, targetDate, categoryColor sql.NullString
	err := s.db.QueryRow(`
		SELECT m.name, m.description, m.target_date, m.status, mc.color
		FROM milestones m
		LEFT JOIN milestone_categories mc ON m.category_id = mc.id
		WHERE m.id = ?
	`, milestoneID).Scan(&report.MilestoneName, &description, &targetDate, &report.Status, &categoryColor)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("milestone not found: %d", milestoneID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get milestone: %w", err)
	}

	report.Description = description.String
	if targetDate.Valid && targetDate.String != "" {
		report.TargetDate = &targetDate.String
	}
	report.CategoryColor = categoryColor.String

	// Get status breakdown and items grouped by status category
	acc, err := s.buildProgressReport(repository.ItemFilters{MilestoneID: &milestoneID})
	if err != nil {
		return nil, fmt.Errorf("failed to get milestone progress: %w", err)
	}

	report.TotalItems = acc.TotalItems
	report.CompletedItems = acc.CompletedItems
	report.PercentComplete = acc.PercentComplete
	report.StatusBreakdown = acc.StatusBreakdown
	report.ItemsByCategory = acc.ItemsByCategory

	return &report, nil
}

// IsMilestoneGlobal checks if a milestone is global.
func (s *PlanningService) IsMilestoneGlobal(id int) (isGlobal bool, workspaceID *int, err error) {
	var wsID sql.NullInt64
	err = s.db.QueryRow("SELECT is_global, workspace_id FROM milestones WHERE id = ?", id).Scan(&isGlobal, &wsID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, fmt.Errorf("milestone not found: %d", id)
	}
	if err != nil {
		return false, nil, fmt.Errorf("failed to check milestone: %w", err)
	}
	if wsID.Valid {
		wid := int(wsID.Int64)
		workspaceID = &wid
	}
	return isGlobal, workspaceID, nil
}

// MaxMilestonePosition returns the current maximum position for milestones
// in the given scope, or 0 when the scope is empty. Used by CreateMilestone
// to place a new milestone at the end of its group.
func (s *PlanningService) MaxMilestonePosition(isGlobal bool, workspaceID, categoryID *int) (int, error) {
	var maxPos sql.NullInt64
	err := s.db.QueryRow(
		"SELECT MAX(position) FROM milestones WHERE "+milestoneScopeClause(),
		milestoneScopeArgs(isGlobal, workspaceID, categoryID)...,
	).Scan(&maxPos)
	if err != nil {
		return 0, fmt.Errorf("failed to get max milestone position: %w", err)
	}
	if !maxPos.Valid {
		return 0, nil
	}
	return int(maxPos.Int64), nil
}

// MilestoneScope identifies the (is_global, workspace_id, category_id) group
// a reorder operation applies to. Drag-and-drop reorders milestones within a
// single scope only; cross-scope moves are out of scope and rejected.
type MilestoneScope struct {
	IsGlobal    bool
	WorkspaceID *int
	CategoryID  *int
}

// ReorderMilestones reassigns position for the milestones identified by
// orderedIDs, all of which must belong to scope. Positions are normalized to
// (index+1)*milestonePositionStep in a single transaction, leaving gaps for
// future inserts. Any id in orderedIDs that isn't in scope is ignored (its
// UPDATE matches 0 rows); the caller is responsible for supplying a complete,
// in-scope ordering. Mirrors the test-folder Reorder pattern.
func (s *PlanningService) ReorderMilestones(scope MilestoneScope, orderedIDs []int) error {
	if len(orderedIDs) == 0 {
		return nil
	}

	scopeClause := milestoneScopeClause()
	scopeArgs := milestoneScopeArgs(scope.IsGlobal, scope.WorkspaceID, scope.CategoryID)

	seen := make(map[int]struct{}, len(orderedIDs))
	for _, id := range orderedIDs {
		if id <= 0 {
			return fmt.Errorf("%w: ordered_ids must contain positive ids", ErrInvalidMilestoneReorder)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: ordered_ids contains duplicate id %d", ErrInvalidMilestoneReorder, id)
		}
		seen[id] = struct{}{}
	}

	rows, err := s.db.Query("SELECT id FROM milestones WHERE "+scopeClause, scopeArgs...)
	if err != nil {
		return fmt.Errorf("failed to load milestone reorder scope: %w", err)
	}
	defer func() { _ = rows.Close() }()

	scopeIDs := make(map[int]struct{}, len(orderedIDs))
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("failed to scan milestone reorder scope: %w", err)
		}
		scopeIDs[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate milestone reorder scope: %w", err)
	}
	if len(scopeIDs) != len(orderedIDs) {
		return fmt.Errorf("%w: ordered_ids must include every milestone in the selected scope", ErrInvalidMilestoneReorder)
	}
	for id := range seen {
		if _, ok := scopeIDs[id]; !ok {
			return fmt.Errorf("%w: milestone %d is not in the selected scope", ErrInvalidMilestoneReorder, id)
		}
	}

	return database.WithTx(s.db, func(tx database.Tx) error {
		now := time.Now()
		for i, id := range orderedIDs {
			position := (i + 1) * milestonePositionStep
			updateArgs := append([]interface{}{position, now, id}, scopeArgs...)
			if _, err := tx.Exec(
				"UPDATE milestones SET position = ?, updated_at = ? WHERE id = ? AND "+scopeClause,
				updateArgs...,
			); err != nil {
				return fmt.Errorf("failed to reorder milestone %d: %w", id, err)
			}
		}
		return nil
	})
}
