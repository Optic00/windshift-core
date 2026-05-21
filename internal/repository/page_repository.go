// Package repository — page_repository persists workspace knowledge pages.
package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
)

// MaxPageDepth caps every recursive page-tree walk. Mirrors the items
// hierarchy ceiling so a stored cycle cannot loop the DB forever and the
// CTE-based ancestor/descendant traversals stay bounded.
const MaxPageDepth = 30

// PageRepository persists the pages table and its tree helpers (ancestors,
// descendants, children, and parent-walk for cycle detection).
type PageRepository struct {
	db database.Database
}

// NewPageRepository creates a PageRepository.
func NewPageRepository(db database.Database) *PageRepository {
	return &PageRepository{db: db}
}

// pageColumns lists every column of the pages table in the order used by
// scanPage. Centralized so SELECT and Scan stay in sync.
const pageColumns = `id, workspace_id, parent_id, title, slug, content, content_hash,
	excerpt, created_by, updated_by, archived_by, is_home, inherit_permissions,
	rank, frac_index, path, depth, created_at, updated_at, archived_at`

// scanPage scans a single row into a Page using the package-local rowScanner
// abstraction (declared by custom_field_repository).
func scanPage(s rowScanner) (*models.Page, error) {
	var p models.Page
	var parentID, updatedBy, archivedBy sql.NullInt64
	var rank, fracIndex sql.NullString
	var archivedAt sql.NullTime

	if err := s.Scan(
		&p.ID, &p.WorkspaceID, &parentID, &p.Title, &p.Slug, &p.Content, &p.ContentHash,
		&p.Excerpt, &p.CreatedBy, &updatedBy, &archivedBy, &p.IsHome, &p.InheritPermissions,
		&rank, &fracIndex, &p.Path, &p.Depth, &p.CreatedAt, &p.UpdatedAt, &archivedAt,
	); err != nil {
		return nil, err
	}

	if parentID.Valid {
		v := int(parentID.Int64)
		p.ParentID = &v
	}
	if updatedBy.Valid {
		v := int(updatedBy.Int64)
		p.UpdatedBy = &v
	}
	if archivedBy.Valid {
		v := int(archivedBy.Int64)
		p.ArchivedBy = &v
	}
	if rank.Valid {
		p.Rank = &rank.String
	}
	if fracIndex.Valid {
		p.FracIndex = &fracIndex.String
	}
	if archivedAt.Valid {
		p.ArchivedAt = &archivedAt.Time
	}
	return &p, nil
}

// CreateInput is the persisted shape of a new page. The service computes
// slug/content/hash/excerpt/path/depth/inheritance flags before calling.
type CreateInput struct {
	WorkspaceID        int
	ParentID           *int
	Title              string
	Slug               string
	Content            string
	ContentHash        string
	Excerpt            string
	CreatedBy          int
	IsHome             bool
	InheritPermissions bool
	Rank               *string
	FracIndex          *string
	Path               string
	Depth              int
}

// CreateTx inserts a page within the given transaction. Returns the new id.
func (r *PageRepository) CreateTx(tx database.Tx, in CreateInput) (int, error) {
	now := time.Now().UTC()
	var id int
	err := tx.QueryRow(`
		INSERT INTO pages (
			workspace_id, parent_id, title, slug, content, content_hash, excerpt,
			created_by, updated_by, is_home, inherit_permissions,
			rank, frac_index, path, depth, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id
	`,
		in.WorkspaceID, nullInt(in.ParentID), in.Title, in.Slug, in.Content, in.ContentHash, in.Excerpt,
		in.CreatedBy, in.CreatedBy, in.IsHome, in.InheritPermissions,
		nullString(in.Rank), nullString(in.FracIndex), in.Path, in.Depth, now, now,
	).Scan(&id)
	if err != nil {
		if isUniqueConstraintError(err) {
			return 0, ErrDuplicateEntry
		}
		return 0, fmt.Errorf("insert page: %w", err)
	}
	return id, nil
}

// GetByID loads a single page. Returns ErrNotFound when no row matches.
func (r *PageRepository) GetByID(id int) (*models.Page, error) {
	row := r.db.QueryRow("SELECT "+pageColumns+" FROM pages WHERE id = ?", id)
	page, err := scanPage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get page %d: %w", id, err)
	}
	return page, nil
}

// GetByIDTx loads a single page within a transaction.
func (r *PageRepository) GetByIDTx(tx database.Tx, id int) (*models.Page, error) {
	row := tx.QueryRow("SELECT "+pageColumns+" FROM pages WHERE id = ?", id)
	page, err := scanPage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get page %d (tx): %w", id, err)
	}
	return page, nil
}

// GetParentIDTx returns the parent_id of a page within a transaction.
// Used by the cycle-detection walker. Returns ErrNotFound for unknown ids.
func (r *PageRepository) GetParentIDTx(tx database.Tx, id int) (*int, error) {
	var parentID sql.NullInt64
	err := tx.QueryRow("SELECT parent_id FROM pages WHERE id = ?", id).Scan(&parentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get parent_id of page %d: %w", id, err)
	}
	if !parentID.Valid {
		return nil, nil
	}
	v := int(parentID.Int64)
	return &v, nil
}

// UpdateInput is the persisted shape of a page update. The service computes
// the derived columns (slug/content_hash/excerpt) before calling.
type UpdateInput struct {
	ID                 int
	Title              string
	Slug               string
	Content            string
	ContentHash        string
	Excerpt            string
	InheritPermissions bool
	Rank               *string
	FracIndex          *string
	UpdatedBy          int
}

// UpdateTx applies a content/title/slug/inheritance edit within a transaction.
// Move and Archive are separate methods because they touch parent_id/path/depth
// and archived_* fields respectively.
func (r *PageRepository) UpdateTx(tx database.Tx, in UpdateInput) error {
	now := time.Now().UTC()
	res, err := tx.Exec(`
		UPDATE pages
		SET title = ?,
		    slug = ?,
		    content = ?,
		    content_hash = ?,
		    excerpt = ?,
		    inherit_permissions = ?,
		    rank = ?,
		    frac_index = ?,
		    updated_by = ?,
		    updated_at = ?
		WHERE id = ?
	`, in.Title, in.Slug, in.Content, in.ContentHash, in.Excerpt, in.InheritPermissions,
		nullString(in.Rank), nullString(in.FracIndex), in.UpdatedBy, now, in.ID)
	if err != nil {
		if isUniqueConstraintError(err) {
			return ErrDuplicateEntry
		}
		return fmt.Errorf("update page %d: %w", in.ID, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveTx reparents a page and overwrites its path/depth. Cycle detection is
// the caller's responsibility (see WouldCreateCycleTx).
func (r *PageRepository) MoveTx(tx database.Tx, pageID int, newParentID *int, newPath string, newDepth, updatedBy int) error {
	now := time.Now().UTC()
	res, err := tx.Exec(`
		UPDATE pages
		SET parent_id = ?,
		    path = ?,
		    depth = ?,
		    updated_by = ?,
		    updated_at = ?
		WHERE id = ?
	`, nullInt(newParentID), newPath, newDepth, updatedBy, now, pageID)
	if err != nil {
		if isUniqueConstraintError(err) {
			return ErrDuplicateEntry
		}
		return fmt.Errorf("move page %d: %w", pageID, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ArchiveTx flags a page (and only this row — descendants are archived by the
// service in a separate pass) as archived. Idempotent: re-archiving a page
// updates archived_at and archived_by to the latest call.
func (r *PageRepository) ArchiveTx(tx database.Tx, pageID, archivedBy int) error {
	now := time.Now().UTC()
	res, err := tx.Exec(`
		UPDATE pages
		SET archived_at = ?,
		    archived_by = ?,
		    updated_at = ?,
		    updated_by = ?
		WHERE id = ?
	`, now, archivedBy, now, archivedBy, pageID)
	if err != nil {
		return fmt.Errorf("archive page %d: %w", pageID, err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ListWorkspaceTree returns every (non-archived unless includeArchived) page
// in a workspace, ordered by depth and then by frac_index/rank/title so
// callers can build the tree client-side with a single query.
func (r *PageRepository) ListWorkspaceTree(workspaceID int, includeArchived bool) ([]models.Page, error) {
	cond := "workspace_id = ? AND archived_at IS NULL"
	if includeArchived {
		cond = "workspace_id = ?"
	}
	rows, err := r.db.Query(`
		SELECT `+pageColumns+`
		FROM pages
		WHERE `+cond+`
		ORDER BY depth ASC,
		         COALESCE(frac_index, '') ASC,
		         COALESCE(rank, '') ASC,
		         title ASC,
		         id ASC
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace pages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []models.Page
	for rows.Next() {
		page, scanErr := scanPage(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan page: %w", scanErr)
		}
		out = append(out, *page)
	}
	return out, rows.Err()
}

// ListChildren returns direct children of a page (or root pages when
// parentID is nil), ordered the same way as ListWorkspaceTree.
func (r *PageRepository) ListChildren(workspaceID int, parentID *int) ([]models.Page, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if parentID == nil {
		rows, err = r.db.Query(`
			SELECT `+pageColumns+`
			FROM pages
			WHERE workspace_id = ? AND parent_id IS NULL AND archived_at IS NULL
			ORDER BY COALESCE(frac_index, '') ASC, COALESCE(rank, '') ASC, title ASC, id ASC
		`, workspaceID)
	} else {
		rows, err = r.db.Query(`
			SELECT `+pageColumns+`
			FROM pages
			WHERE workspace_id = ? AND parent_id = ? AND archived_at IS NULL
			ORDER BY COALESCE(frac_index, '') ASC, COALESCE(rank, '') ASC, title ASC, id ASC
		`, workspaceID, *parentID)
	}
	if err != nil {
		return nil, fmt.Errorf("list children: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []models.Page
	for rows.Next() {
		page, scanErr := scanPage(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan page child: %w", scanErr)
		}
		out = append(out, *page)
	}
	return out, rows.Err()
}

// GetAncestors returns all ancestors of a page (root first, direct parent
// last), capped at MaxPageDepth so a stored cycle cannot loop the DB.
// The original page is excluded from the result.
func (r *PageRepository) GetAncestors(pageID int) ([]models.Page, error) {
	rows, err := r.db.Query(`
		WITH RECURSIVE ancestors AS (
			SELECT `+pageColumns+`, 0 AS level
			FROM pages
			WHERE id = ?

			UNION ALL

			SELECT p.id, p.workspace_id, p.parent_id, p.title, p.slug, p.content, p.content_hash,
			       p.excerpt, p.created_by, p.updated_by, p.archived_by, p.is_home, p.inherit_permissions,
			       p.rank, p.frac_index, p.path, p.depth, p.created_at, p.updated_at, p.archived_at,
			       a.level + 1 AS level
			FROM pages p
			JOIN ancestors a ON p.id = a.parent_id
			WHERE a.level < ?
		)
		SELECT `+pageColumns+`
		FROM ancestors
		WHERE id != ?
		ORDER BY level DESC
	`, pageID, MaxPageDepth, pageID)
	if err != nil {
		return nil, fmt.Errorf("query ancestors: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []models.Page
	for rows.Next() {
		page, scanErr := scanPage(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan ancestor: %w", scanErr)
		}
		out = append(out, *page)
	}
	return out, rows.Err()
}

// GetDescendants returns every descendant of a page up to maxDepth.
// maxDepth <= 0 or > MaxPageDepth is clamped to MaxPageDepth.
func (r *PageRepository) GetDescendants(pageID, maxDepth int) ([]models.Page, error) {
	if maxDepth <= 0 || maxDepth > MaxPageDepth {
		maxDepth = MaxPageDepth
	}
	rows, err := r.db.Query(`
		WITH RECURSIVE descendants AS (
			SELECT `+pageColumns+`, 1 AS sub_depth
			FROM pages
			WHERE parent_id = ?

			UNION ALL

			SELECT p.id, p.workspace_id, p.parent_id, p.title, p.slug, p.content, p.content_hash,
			       p.excerpt, p.created_by, p.updated_by, p.archived_by, p.is_home, p.inherit_permissions,
			       p.rank, p.frac_index, p.path, p.depth, p.created_at, p.updated_at, p.archived_at,
			       d.sub_depth + 1
			FROM pages p
			JOIN descendants d ON p.parent_id = d.id
			WHERE d.sub_depth < ?
		)
		SELECT `+pageColumns+`
		FROM descendants
		ORDER BY sub_depth ASC, COALESCE(frac_index, '') ASC, COALESCE(rank, '') ASC, title ASC, id ASC
	`, pageID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("query descendants: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []models.Page
	for rows.Next() {
		page, scanErr := scanPage(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan descendant: %w", scanErr)
		}
		out = append(out, *page)
	}
	return out, rows.Err()
}

// WouldCreatePageCycleTx reports whether reparenting page pageID under
// newParentID would create a cycle. Walks parent_id upward from
// newParentID; encountering pageID — or pageID == newParentID — means a
// cycle would result. If the walk exhausts MaxPageDepth without reaching a
// root, the hierarchy is either already cyclic or too deep; fail-closed
// and return (true, nil).
func (r *PageRepository) WouldCreatePageCycleTx(tx database.Tx, pageID, newParentID int) (bool, error) {
	current := newParentID
	for i := 0; i < MaxPageDepth; i++ {
		if current == pageID {
			return true, nil
		}
		parent, err := r.GetParentIDTx(tx, current)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return false, nil
			}
			return false, fmt.Errorf("walk page hierarchy: %w", err)
		}
		if parent == nil {
			return false, nil
		}
		current = *parent
	}
	return true, nil
}

// CountWorkspacePages returns the number of non-archived pages in a
// workspace. Used by handlers to short-circuit empty trees.
func (r *PageRepository) CountWorkspacePages(workspaceID int) (int, error) {
	var n int
	err := r.db.QueryRow(
		"SELECT COUNT(*) FROM pages WHERE workspace_id = ? AND archived_at IS NULL",
		workspaceID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count workspace pages: %w", err)
	}
	return n, nil
}

// --- helpers ---

func nullInt(v *int) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func nullString(v *string) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

// isUniqueConstraintError matches both SQLite and Postgres unique-violation
// errors without depending on driver-specific error types.
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "unique_violation") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "sqlite_constraint_unique") ||
		strings.Contains(msg, "constraint failed: unique")
}
