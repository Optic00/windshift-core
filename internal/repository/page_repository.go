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

// --- revisions ---

// pageRevisionColumns mirrors models.PageRevision field order.
const pageRevisionColumns = `id, page_id, revision_number, title, slug, content, content_hash,
	excerpt, parent_id, path, depth, change_summary, change_type, created_by, created_at`

func scanPageRevision(s rowScanner) (*models.PageRevision, error) {
	var rev models.PageRevision
	var parentID sql.NullInt64
	if err := s.Scan(
		&rev.ID, &rev.PageID, &rev.RevisionNumber, &rev.Title, &rev.Slug, &rev.Content, &rev.ContentHash,
		&rev.Excerpt, &parentID, &rev.Path, &rev.Depth, &rev.ChangeSummary, &rev.ChangeType,
		&rev.CreatedBy, &rev.CreatedAt,
	); err != nil {
		return nil, err
	}
	if parentID.Valid {
		v := int(parentID.Int64)
		rev.ParentID = &v
	}
	return &rev, nil
}

// NextRevisionNumberTx returns MAX(revision_number)+1 for the given page,
// or 1 when the page has no revisions yet. Run inside the same tx as the
// subsequent insert so revision_number stays unique under concurrent writes.
func (r *PageRepository) NextRevisionNumberTx(tx database.Tx, pageID int) (int, error) {
	var next int
	err := tx.QueryRow(
		"SELECT COALESCE(MAX(revision_number), 0) + 1 FROM page_revisions WHERE page_id = ?",
		pageID,
	).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("compute next revision number: %w", err)
	}
	return next, nil
}

// InsertRevisionTx persists an immutable snapshot inside an existing tx.
// Returns the new revision id.
func (r *PageRepository) InsertRevisionTx(tx database.Tx, rev models.PageRevision) (int, error) {
	var id int
	err := tx.QueryRow(`
		INSERT INTO page_revisions (
			page_id, revision_number, title, slug, content, content_hash, excerpt,
			parent_id, path, depth, change_summary, change_type, created_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id
	`,
		rev.PageID, rev.RevisionNumber, rev.Title, rev.Slug, rev.Content, rev.ContentHash, rev.Excerpt,
		nullInt(rev.ParentID), rev.Path, rev.Depth, rev.ChangeSummary, rev.ChangeType, rev.CreatedBy,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert revision: %w", err)
	}
	return id, nil
}

// GetRevisionByID loads a single revision. Returns ErrNotFound when no row
// matches.
func (r *PageRepository) GetRevisionByID(id int) (*models.PageRevision, error) {
	row := r.db.QueryRow("SELECT "+pageRevisionColumns+" FROM page_revisions WHERE id = ?", id)
	rev, err := scanPageRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get revision %d: %w", id, err)
	}
	return rev, nil
}

// ListRevisions returns revisions for a page newest-first. limit <= 0
// returns up to 50; clients can paginate via offset for older history.
func (r *PageRepository) ListRevisions(pageID, limit, offset int) ([]models.PageRevision, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.Query(`
		SELECT `+pageRevisionColumns+`
		FROM page_revisions
		WHERE page_id = ?
		ORDER BY revision_number DESC
		LIMIT ? OFFSET ?
	`, pageID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []models.PageRevision
	for rows.Next() {
		rev, scanErr := scanPageRevision(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan revision: %w", scanErr)
		}
		out = append(out, *rev)
	}
	return out, rows.Err()
}

// --- ACL ---

// ListACLForPage returns the rows stored directly against this page (no
// inheritance). The Phase 2 ACL UI will fetch inherited rows separately so
// admins can see exactly what's set vs. inherited.
func (r *PageRepository) ListACLForPage(pageID int) ([]models.PagePermission, error) {
	rows, err := r.db.Query(`
		SELECT id, page_id, principal_type, principal_id, permission_level, granted_by, granted_at
		FROM page_permissions
		WHERE page_id = ?
		ORDER BY id
	`, pageID)
	if err != nil {
		return nil, fmt.Errorf("list page ACL: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []models.PagePermission
	for rows.Next() {
		var p models.PagePermission
		var grantedBy sql.NullInt64
		if err := rows.Scan(&p.ID, &p.PageID, &p.PrincipalType, &p.PrincipalID, &p.PermissionLevel, &grantedBy, &p.GrantedAt); err != nil {
			return nil, fmt.Errorf("scan ACL row: %w", err)
		}
		if grantedBy.Valid {
			v := int(grantedBy.Int64)
			p.GrantedBy = &v
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- chunks ---

const pageChunkColumns = `id, page_id, workspace_id, revision_number, position, heading_path,
	content, token_count, byte_start, byte_end, content_hash, created_at`

func scanPageChunk(s rowScanner) (*models.PageChunk, error) {
	var c models.PageChunk
	if err := s.Scan(
		&c.ID, &c.PageID, &c.WorkspaceID, &c.RevisionNumber, &c.Position, &c.HeadingPath,
		&c.Content, &c.TokenCount, &c.ByteStart, &c.ByteEnd, &c.ContentHash, &c.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

// DeleteChunksForPageTx removes every chunk row for a page within a tx.
// Used before re-inserting freshly computed chunks.
func (r *PageRepository) DeleteChunksForPageTx(tx database.Tx, pageID int) error {
	_, err := tx.Exec("DELETE FROM page_chunks WHERE page_id = ?", pageID)
	if err != nil {
		return fmt.Errorf("delete page chunks: %w", err)
	}
	return nil
}

// InsertChunkTx persists a chunk inside the same tx as the page edit that
// produced it.
func (r *PageRepository) InsertChunkTx(tx database.Tx, c models.PageChunk) error {
	_, err := tx.Exec(`
		INSERT INTO page_chunks (
			page_id, workspace_id, revision_number, position, heading_path,
			content, token_count, byte_start, byte_end, content_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, c.PageID, c.WorkspaceID, c.RevisionNumber, c.Position, c.HeadingPath,
		c.Content, c.TokenCount, c.ByteStart, c.ByteEnd, c.ContentHash)
	if err != nil {
		return fmt.Errorf("insert page chunk: %w", err)
	}
	return nil
}

// ListChunksForPage returns chunks in position order. Used by the search
// pipeline once the page passes the permission check.
func (r *PageRepository) ListChunksForPage(pageID int) ([]models.PageChunk, error) {
	rows, err := r.db.Query(`
		SELECT `+pageChunkColumns+`
		FROM page_chunks
		WHERE page_id = ?
		ORDER BY position ASC
	`, pageID)
	if err != nil {
		return nil, fmt.Errorf("list page chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []models.PageChunk
	for rows.Next() {
		c, scanErr := scanPageChunk(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan chunk: %w", scanErr)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// PageChunkSearchResult is a single ranked search hit. Score range depends
// on the backend (ts_rank floats on Postgres; substring-presence integer on
// SQLite) — callers should treat it as opaque except for sort ordering.
type PageChunkSearchResult struct {
	ChunkID     int
	PageID      int
	WorkspaceID int
	HeadingPath string
	Content     string
	Snippet     string
	Score       float64
}

// SearchChunks runs full-text search over page_chunks in the given
// workspace. The query is treated as a websearch-style phrase on Postgres
// and falls back to case-insensitive LIKE on SQLite. Permission filtering
// is the caller's responsibility (see KnowledgeRetrievalService.Search).
func (r *PageRepository) SearchChunks(workspaceID int, query string, limit int) ([]PageChunkSearchResult, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	switch r.db.GetDriverName() {
	case "postgres":
		return r.searchChunksPostgres(workspaceID, query, limit)
	default:
		return r.searchChunksSQLite(workspaceID, query, limit)
	}
}

func (r *PageRepository) searchChunksPostgres(workspaceID int, query string, limit int) ([]PageChunkSearchResult, error) {
	rows, err := r.db.Query(`
		SELECT c.id, c.page_id, c.workspace_id, c.heading_path, c.content,
		       ts_headline('english', c.content, websearch_to_tsquery('english', ?),
		           'MaxWords=40, MinWords=15, StartSel=<mark>, StopSel=</mark>') AS snippet,
		       ts_rank(to_tsvector('english', coalesce(c.heading_path, '') || ' ' || coalesce(c.content, '')),
		           websearch_to_tsquery('english', ?)) AS score
		FROM page_chunks c
		JOIN pages p ON p.id = c.page_id
		WHERE c.workspace_id = ? AND p.archived_at IS NULL
		AND to_tsvector('english', coalesce(c.heading_path, '') || ' ' || coalesce(c.content, ''))
		    @@ websearch_to_tsquery('english', ?)
		ORDER BY score DESC
		LIMIT ?
	`, query, query, workspaceID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("page chunk search (postgres): %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanChunkSearch(rows)
}

func (r *PageRepository) searchChunksSQLite(workspaceID int, query string, limit int) ([]PageChunkSearchResult, error) {
	like := "%" + strings.ToLower(query) + "%"
	rows, err := r.db.Query(`
		SELECT c.id, c.page_id, c.workspace_id, c.heading_path, c.content,
		       SUBSTR(c.content, 1, 240) AS snippet,
		       CAST(
		           (CASE WHEN LOWER(c.heading_path) LIKE ? THEN 2 ELSE 0 END) +
		           (CASE WHEN LOWER(c.content) LIKE ? THEN 1 ELSE 0 END)
		       AS REAL) AS score
		FROM page_chunks c
		JOIN pages p ON p.id = c.page_id
		WHERE c.workspace_id = ? AND p.archived_at IS NULL
		AND (LOWER(c.heading_path) LIKE ? OR LOWER(c.content) LIKE ?)
		ORDER BY score DESC, c.id ASC
		LIMIT ?
	`, like, like, workspaceID, like, like, limit)
	if err != nil {
		return nil, fmt.Errorf("page chunk search (sqlite): %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanChunkSearch(rows)
}

func scanChunkSearch(rows *sql.Rows) ([]PageChunkSearchResult, error) {
	var out []PageChunkSearchResult
	for rows.Next() {
		var hit PageChunkSearchResult
		if err := rows.Scan(&hit.ChunkID, &hit.PageID, &hit.WorkspaceID, &hit.HeadingPath, &hit.Content, &hit.Snippet, &hit.Score); err != nil {
			return nil, fmt.Errorf("scan chunk search result: %w", err)
		}
		out = append(out, hit)
	}
	return out, rows.Err()
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
