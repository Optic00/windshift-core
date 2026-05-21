// Package services — page_service owns the wiki-pages business rules:
// sanitization, slug derivation, path/depth bookkeeping, cycle prevention,
// and tree assembly. The HTTP handlers, AI tools, and knowledge retrieval
// service all go through PageService rather than touching the repository
// directly. Revisions and search chunks land in a follow-up slice.
package services

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/utils"
)

// PageService is the entry point for all page CRUD and tree operations.
type PageService struct {
	db    database.Database
	pages *repository.PageRepository
}

// NewPageService creates a PageService backed by the provided database.
func NewPageService(db database.Database) *PageService {
	return &PageService{
		db:    db,
		pages: repository.NewPageRepository(db),
	}
}

// Service-level errors. Wraps repository errors so the handler layer can
// map them to HTTP status codes without knowing repository internals.
var (
	ErrPageNotFound         = errors.New("page not found")
	ErrPageTitleRequired    = errors.New("page title is required")
	ErrPageParentMismatch   = errors.New("parent page belongs to a different workspace")
	ErrPageCycle            = errors.New("move would create a cycle")
	ErrPageDepthExceeded    = errors.New("page tree depth limit exceeded")
	ErrPageSlugConflict     = errors.New("slug conflicts with an existing sibling page")
	ErrPageRevisionMismatch = errors.New("revision does not belong to the target page")
)

// CreatePageInput is the request shape for Create. Permission inheritance
// is always true on create; the permissions dialog (Phase 2) lets an
// admin break inheritance later.
type CreatePageInput struct {
	WorkspaceID int
	ParentID    *int
	Title       string
	Content     string
	IsHome      bool
	Rank        *string
	FracIndex   *string
}

// Create inserts a new page after sanitizing inputs and computing derived
// columns. Returns the persisted page.
func (s *PageService) Create(actorID int, in CreatePageInput) (*models.Page, error) {
	title := utils.SanitizeTitle(in.Title)
	if title == "" {
		return nil, ErrPageTitleRequired
	}

	content := utils.SanitizePageMarkdown(in.Content)
	excerpt := deriveExcerpt(content)
	hash := contentHash(content)

	// Pages always inherit on create; the permissions dialog (Phase 2)
	// lets an admin break inheritance later. The schema default is also
	// 1/true, so we pass true explicitly rather than relying on the column
	// default.
	inherit := true

	return database.WithTxResult(s.db, func(tx database.Tx) (*models.Page, error) {
		parentID, parentPath, parentDepth, err := s.resolveParent(tx, in.WorkspaceID, in.ParentID)
		if err != nil {
			return nil, err
		}
		depth := parentDepth + 1
		if in.ParentID == nil {
			depth = 0
		}
		if depth >= repository.MaxPageDepth {
			return nil, ErrPageDepthExceeded
		}

		baseSlug := makeSlug(title)
		slug, slugErr := s.pickAvailableSlug(tx, in.WorkspaceID, parentID, baseSlug, 0)
		if slugErr != nil {
			return nil, slugErr
		}

		id, err := s.pages.CreateTx(tx, repository.CreateInput{
			WorkspaceID:        in.WorkspaceID,
			ParentID:           parentID,
			Title:              title,
			Slug:               slug,
			Content:            content,
			ContentHash:        hash,
			Excerpt:            excerpt,
			CreatedBy:          actorID,
			IsHome:             in.IsHome,
			InheritPermissions: inherit,
			Rank:               in.Rank,
			FracIndex:          in.FracIndex,
			Path:               parentPath,
			Depth:              depth,
		})
		if err != nil {
			if errors.Is(err, repository.ErrDuplicateEntry) {
				return nil, ErrPageSlugConflict
			}
			return nil, err
		}

		page, err := s.pages.GetByIDTx(tx, id)
		if err != nil {
			return nil, err
		}

		if err := s.snapshotAndRebuildChunks(tx, page, actorID, models.PageRevisionChangeTypeCreate, ""); err != nil {
			return nil, err
		}
		return page, nil
	})
}

// GetByID returns a single page, or ErrPageNotFound when no row matches.
// Workspace scoping is the caller's responsibility — the handler layer
// checks workspace membership and runs the page ACL evaluator.
func (s *PageService) GetByID(id int) (*models.Page, error) {
	page, err := s.pages.GetByID(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrPageNotFound
		}
		return nil, err
	}
	return page, nil
}

// UpdatePageInput is the request shape for Update. InheritPermissions is
// intentionally absent: inheritance changes go through SetInheritPermissions
// (PageOpAdmin) — accepting it here would let an editor flip the flag via
// a normal title/content save, bypassing the admin gate.
type UpdatePageInput struct {
	ID        int
	Title     string
	Content   string
	Rank      *string
	FracIndex *string
}

// Update overwrites a page's title/content and recomputes the derived
// columns. Inheritance, parent (Move), and archive each have their own
// admin-gated call so the audit trail and handler authorization paths
// stay distinct.
func (s *PageService) Update(actorID int, in UpdatePageInput) (*models.Page, error) {
	title := utils.SanitizeTitle(in.Title)
	if title == "" {
		return nil, ErrPageTitleRequired
	}
	content := utils.SanitizePageMarkdown(in.Content)
	excerpt := deriveExcerpt(content)
	hash := contentHash(content)

	return database.WithTxResult(s.db, func(tx database.Tx) (*models.Page, error) {
		existing, err := s.pages.GetByIDTx(tx, in.ID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrPageNotFound
			}
			return nil, err
		}

		newSlug := existing.Slug
		if !strings.EqualFold(title, existing.Title) {
			candidate := makeSlug(title)
			if candidate != existing.Slug {
				picked, slugErr := s.pickAvailableSlug(tx, existing.WorkspaceID, existing.ParentID, candidate, existing.ID)
				if slugErr != nil {
					return nil, slugErr
				}
				newSlug = picked
			}
		}

		err = s.pages.UpdateTx(tx, repository.UpdateInput{
			ID:                 in.ID,
			Title:              title,
			Slug:               newSlug,
			Content:            content,
			ContentHash:        hash,
			Excerpt:            excerpt,
			InheritPermissions: existing.InheritPermissions,
			Rank:               in.Rank,
			FracIndex:          in.FracIndex,
			UpdatedBy:          actorID,
		})
		if err != nil {
			if errors.Is(err, repository.ErrDuplicateEntry) {
				return nil, ErrPageSlugConflict
			}
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrPageNotFound
			}
			return nil, err
		}

		updated, err := s.pages.GetByIDTx(tx, in.ID)
		if err != nil {
			return nil, err
		}
		if err := s.snapshotAndRebuildChunks(tx, updated, actorID, models.PageRevisionChangeTypeEdit, ""); err != nil {
			return nil, err
		}
		return updated, nil
	})
}

// Move reparents a page to a new parent. Cycle detection and depth check
// run inside the transaction; descendants' paths/depths are updated in the
// same pass so the tree stays consistent.
func (s *PageService) Move(actorID, pageID int, newParentID *int) (*models.Page, error) {
	return database.WithTxResult(s.db, func(tx database.Tx) (*models.Page, error) {
		page, err := s.pages.GetByIDTx(tx, pageID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrPageNotFound
			}
			return nil, err
		}

		var (
			newPath  string
			newDepth int
		)
		if newParentID == nil {
			newPath = "/"
			newDepth = 0
		} else {
			cyclic, cErr := s.pages.WouldCreatePageCycleTx(tx, pageID, *newParentID)
			if cErr != nil {
				return nil, cErr
			}
			if cyclic {
				return nil, ErrPageCycle
			}
			parent, pErr := s.pages.GetByIDTx(tx, *newParentID)
			if pErr != nil {
				if errors.Is(pErr, repository.ErrNotFound) {
					return nil, ErrPageNotFound
				}
				return nil, pErr
			}
			if parent.WorkspaceID != page.WorkspaceID {
				return nil, ErrPageParentMismatch
			}
			newDepth = parent.Depth + 1
			if newDepth >= repository.MaxPageDepth {
				return nil, ErrPageDepthExceeded
			}
			newPath = parent.Path + fmt.Sprintf("%d/", parent.ID)
		}

		// Verify the WHOLE subtree fits under MaxPageDepth after the
		// shift, not just the moved page. The deepest descendant gains
		// (newDepth - page.Depth) levels; if its post-move depth would
		// breach the cap, refuse the move. Pulled into a single MAX
		// query so deeply-nested subtrees don't cost a recursive walk.
		descendantPrefix := page.Path + fmt.Sprintf("%d/", page.ID)
		var deepestDescendant sql.NullInt64
		if err := tx.QueryRow(
			`SELECT MAX(depth) FROM pages WHERE workspace_id = ? AND path LIKE ?`,
			page.WorkspaceID, descendantPrefix+"%",
		).Scan(&deepestDescendant); err != nil {
			return nil, fmt.Errorf("measure subtree depth: %w", err)
		}
		if deepestDescendant.Valid {
			shifted := int(deepestDescendant.Int64) - page.Depth + newDepth
			if shifted >= repository.MaxPageDepth {
				return nil, ErrPageDepthExceeded
			}
		}

		if err := s.pages.MoveTx(tx, pageID, newParentID, newPath, newDepth, actorID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrPageNotFound
			}
			return nil, err
		}

		// Recompute path/depth for every descendant. The new prefix is
		// newPath + "{pageID}/" (everything previously rooted at the old
		// page-path becomes rooted at the new one).
		oldPrefix := page.Path + fmt.Sprintf("%d/", page.ID)
		newPrefix := newPath + fmt.Sprintf("%d/", pageID)
		depthShift := newDepth + 1 - (page.Depth + 1)
		if oldPrefix != newPrefix || depthShift != 0 {
			_, execErr := tx.Exec(`
				UPDATE pages
				SET path = ? || SUBSTR(path, ?),
				    depth = depth + ?
				WHERE workspace_id = ?
				  AND path LIKE ?
			`, newPrefix, len(oldPrefix)+1, depthShift, page.WorkspaceID, oldPrefix+"%")
			if execErr != nil {
				return nil, fmt.Errorf("rewrite descendant paths: %w", execErr)
			}
		}

		moved, err := s.pages.GetByIDTx(tx, pageID)
		if err != nil {
			return nil, err
		}
		// Move does not rewrite chunks — content didn't change — but we
		// still snapshot a revision so the audit log captures the
		// parent/path change.
		if _, err := s.writeRevisionTx(tx, moved, actorID, models.PageRevisionChangeTypeMove, ""); err != nil {
			return nil, err
		}
		return moved, nil
	})
}

// Archive flags a page (and its entire subtree) as archived. Archive is
// reversible only by restoring an explicit revision in a later slice;
// for Phase 1 it just sets archived_at on the target and every descendant.
func (s *PageService) Archive(actorID, pageID int) error {
	return database.WithTx(s.db, func(tx database.Tx) error {
		page, err := s.pages.GetByIDTx(tx, pageID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return ErrPageNotFound
			}
			return err
		}

		// Archive the page and every descendant by materialized-path prefix.
		// A single statement keeps the operation atomic and avoids walking
		// the CTE for each row.
		prefix := page.Path + fmt.Sprintf("%d/", page.ID)
		if _, err := tx.Exec(`
			UPDATE pages
			SET archived_at = CURRENT_TIMESTAMP,
			    archived_by = ?,
			    updated_at = CURRENT_TIMESTAMP,
			    updated_by = ?
			WHERE id = ? OR (workspace_id = ? AND path LIKE ?)
		`, actorID, actorID, pageID, page.WorkspaceID, prefix+"%"); err != nil {
			return fmt.Errorf("archive subtree: %w", err)
		}

		// Drop the now-stale chunks for the archived page so search and AI
		// tools cannot surface content from a hidden page even before the
		// permission filter runs.
		if err := s.pages.DeleteChunksForPageTx(tx, page.ID); err != nil {
			return err
		}

		archived, err := s.pages.GetByIDTx(tx, page.ID)
		if err != nil {
			return err
		}
		_, err = s.writeRevisionTx(tx, archived, actorID, models.PageRevisionChangeTypeArchive, "")
		return err
	})
}

// GetRevision returns a single revision by id, or ErrPageNotFound when no
// row matches.
func (s *PageService) GetRevision(id int) (*models.PageRevision, error) {
	rev, err := s.pages.GetRevisionByID(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrPageNotFound
		}
		return nil, err
	}
	return rev, nil
}

// ListRevisions returns the revision history for a page, newest first.
func (s *PageService) ListRevisions(pageID, limit, offset int) ([]models.PageRevision, error) {
	return s.pages.ListRevisions(pageID, limit, offset)
}

// Restore overwrites a page's live content/title with the snapshot stored
// in the given revision and records a new revision of change_type
// 'restore'. The revision must belong to the same page; cross-page
// restores return ErrPageRevisionMismatch.
func (s *PageService) Restore(actorID, pageID, revisionID int) (*models.Page, error) {
	return database.WithTxResult(s.db, func(tx database.Tx) (*models.Page, error) {
		page, err := s.pages.GetByIDTx(tx, pageID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrPageNotFound
			}
			return nil, err
		}
		rev, err := s.pages.GetRevisionByID(revisionID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrPageNotFound
			}
			return nil, err
		}
		if rev.PageID != pageID {
			return nil, ErrPageRevisionMismatch
		}

		// Restore overwrites title/slug/content/excerpt/hash on the live row.
		// parent/path/depth are deliberately not restored — moving a page is
		// a separate explicit action. If a user wants to undo a move,
		// they should run Move explicitly.
		if err := s.pages.UpdateTx(tx, repository.UpdateInput{
			ID:                 page.ID,
			Title:              rev.Title,
			Slug:               rev.Slug,
			Content:            rev.Content,
			ContentHash:        rev.ContentHash,
			Excerpt:            rev.Excerpt,
			InheritPermissions: page.InheritPermissions,
			Rank:               page.Rank,
			FracIndex:          page.FracIndex,
			UpdatedBy:          actorID,
		}); err != nil {
			if errors.Is(err, repository.ErrDuplicateEntry) {
				return nil, ErrPageSlugConflict
			}
			return nil, err
		}

		restored, err := s.pages.GetByIDTx(tx, page.ID)
		if err != nil {
			return nil, err
		}
		if err := s.snapshotAndRebuildChunks(tx, restored, actorID, models.PageRevisionChangeTypeRestore, fmt.Sprintf("restored from revision %d", rev.RevisionNumber)); err != nil {
			return nil, err
		}
		return restored, nil
	})
}

// writeRevisionTx persists a revision row for the given page snapshot.
// Used by every page-mutating operation so the history is always complete.
// Returns the revision_number that was just written.
func (s *PageService) writeRevisionTx(tx database.Tx, page *models.Page, actorID int, changeType, summary string) (int, error) {
	next, err := s.pages.NextRevisionNumberTx(tx, page.ID)
	if err != nil {
		return 0, err
	}
	if _, err := s.pages.InsertRevisionTx(tx, models.PageRevision{
		PageID:         page.ID,
		RevisionNumber: next,
		Title:          page.Title,
		Slug:           page.Slug,
		Content:        page.Content,
		ContentHash:    page.ContentHash,
		Excerpt:        page.Excerpt,
		ParentID:       page.ParentID,
		Path:           page.Path,
		Depth:          page.Depth,
		ChangeSummary:  summary,
		ChangeType:     changeType,
		CreatedBy:      actorID,
	}); err != nil {
		return 0, err
	}
	return next, nil
}

// snapshotAndRebuildChunks persists a revision and rebuilds the page chunk
// table in a single transaction. Use this on every content-affecting
// operation (create, edit, restore). Move uses writeRevisionTx directly
// because the content hasn't changed.
func (s *PageService) snapshotAndRebuildChunks(tx database.Tx, page *models.Page, actorID int, changeType, summary string) error {
	revisionNumber, err := s.writeRevisionTx(tx, page, actorID, changeType, summary)
	if err != nil {
		return err
	}
	if err := s.pages.DeleteChunksForPageTx(tx, page.ID); err != nil {
		return err
	}
	if page.Content == "" {
		return nil
	}
	specs := chunkPageMarkdown(page.Content)
	chunks := buildPageChunks(page, revisionNumber, specs)
	for _, chunk := range chunks {
		if err := s.pages.InsertChunkTx(tx, chunk); err != nil {
			return err
		}
	}
	return nil
}

// ListTree returns every non-archived page in a workspace ordered for
// client-side tree assembly (depth-first by frac_index/rank/title).
func (s *PageService) ListTree(workspaceID int, includeArchived bool) ([]models.Page, error) {
	return s.pages.ListWorkspaceTree(workspaceID, includeArchived)
}

// BuildPageTree turns a flat ordered page list (typically from ListTree)
// into a nested PageNode tree suitable for direct rendering in the
// frontend.
func BuildPageTree(pages []models.Page) []*models.PageNode {
	byID := make(map[int]*models.PageNode, len(pages))
	for i := range pages {
		node := &models.PageNode{Page: pages[i]}
		byID[pages[i].ID] = node
	}
	var roots []*models.PageNode
	for i := range pages {
		node := byID[pages[i].ID]
		if pages[i].ParentID == nil {
			roots = append(roots, node)
			continue
		}
		parent, ok := byID[*pages[i].ParentID]
		if !ok {
			// Orphaned (e.g., parent was archived but this page wasn't —
			// shouldn't happen in normal flow). Promote to a root so the UI
			// still renders it instead of dropping it silently.
			roots = append(roots, node)
			continue
		}
		parent.Children = append(parent.Children, node)
	}
	return roots
}

// ListChildren returns direct children of the given parent (root pages when
// parentID is nil), ordered by frac_index/rank/title.
func (s *PageService) ListChildren(workspaceID int, parentID *int) ([]models.Page, error) {
	return s.pages.ListChildren(workspaceID, parentID)
}

// ListDescendants returns every descendant of pageID up to the global
// hierarchy depth cap. Used by the archive handler to verify admin
// access on the whole subtree before triggering the cascade.
func (s *PageService) ListDescendants(pageID int) ([]models.Page, error) {
	return s.pages.GetDescendants(pageID, 0)
}

// ListOwnACL returns the ACL rows stored directly against a page (no
// inheritance). Used by the read-only permissions endpoint in Phase 1; the
// Phase 2 dialog will use this plus a separate inheritance-walk endpoint.
func (s *PageService) ListOwnACL(pageID int) ([]models.PagePermission, error) {
	return s.pages.ListACLForPage(pageID)
}

// --- ACL writes (Phase 2) ---

// ErrPageInvalidPrincipal is returned when a Grant call names a principal
// type the data model doesn't accept (anything outside user/group/role).
var ErrPageInvalidPrincipal = errors.New("invalid principal_type")

// ErrPageInvalidLevel is returned when a Grant call names a permission
// level the data model doesn't accept.
var ErrPageInvalidLevel = errors.New("invalid permission_level")

// ErrPagePermissionDuplicate is returned when the (page, principal, level)
// tuple already exists. The caller can ignore this as a no-op or surface
// it to the user.
var ErrPagePermissionDuplicate = errors.New("permission already granted")

// ErrPageGrantPrincipalNotFound is returned when GrantPermission is asked
// to attach an ACL row to a user/group/role that does not exist or is
// disabled. The runtime evaluator already requires workspace membership
// for the match to count, but rejecting unknown principals at write time
// prevents stale-id grants from sitting in the audit log forever.
var ErrPageGrantPrincipalNotFound = errors.New("principal does not exist")

// GrantPermission attaches an ACL row to a page. Writes a 'permissions'
// revision so the audit trail captures the change. Returns the persisted
// row.
func (s *PageService) GrantPermission(actorID, pageID int, principalType string, principalID int, level string) (*models.PagePermission, error) {
	if !isValidPrincipalType(principalType) {
		return nil, ErrPageInvalidPrincipal
	}
	if !isValidPermissionLevel(level) {
		return nil, ErrPageInvalidLevel
	}

	return database.WithTxResult(s.db, func(tx database.Tx) (*models.PagePermission, error) {
		page, err := s.pages.GetByIDTx(tx, pageID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrPageNotFound
			}
			return nil, err
		}

		if err := s.validateGrantPrincipal(tx, principalType, principalID); err != nil {
			return nil, err
		}

		actor := actorID
		insert := models.PagePermission{
			PageID:          pageID,
			PrincipalType:   principalType,
			PrincipalID:     principalID,
			PermissionLevel: level,
			GrantedBy:       &actor,
		}
		id, err := s.pages.GrantPermissionTx(tx, insert)
		if err != nil {
			if errors.Is(err, repository.ErrDuplicateEntry) {
				return nil, ErrPagePermissionDuplicate
			}
			return nil, err
		}

		if _, err := s.writeRevisionTx(tx, page, actorID, models.PageRevisionChangeTypePermissions, "granted "+level+" to "+principalType); err != nil {
			return nil, err
		}

		// Synthesize the persisted row from the insert input rather than
		// re-querying — re-reading through the read pool while the write
		// tx still holds the row deadlocks under SQLite's single-writer
		// model. granted_at is filled in by the DB; we surface the request
		// time which is close enough for the audit echo back to the
		// caller. Clients that need the canonical timestamp can re-fetch.
		insert.ID = id
		insert.GrantedAt = time.Now().UTC()
		return &insert, nil
	})
}

// RevokePermission removes a single ACL row from a page. The repository
// enforces the (permissionID, pageID) pairing so callers can't revoke a
// row belonging to a different page even if they construct a request by
// hand.
func (s *PageService) RevokePermission(actorID, pageID, permissionID int) error {
	return database.WithTx(s.db, func(tx database.Tx) error {
		page, err := s.pages.GetByIDTx(tx, pageID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return ErrPageNotFound
			}
			return err
		}
		if err := s.pages.RevokePermissionTx(tx, pageID, permissionID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return ErrPageNotFound
			}
			return err
		}
		_, err = s.writeRevisionTx(tx, page, actorID, models.PageRevisionChangeTypePermissions, "revoked permission")
		return err
	})
}

// SetInheritPermissions flips the inherit_permissions flag on a page and
// records a 'permissions' revision. Toggling has no UI cascade in Phase 2
// — descendants always inherit through the walker until they themselves
// flip the flag.
func (s *PageService) SetInheritPermissions(actorID, pageID int, inherit bool) (*models.Page, error) {
	return database.WithTxResult(s.db, func(tx database.Tx) (*models.Page, error) {
		page, err := s.pages.GetByIDTx(tx, pageID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrPageNotFound
			}
			return nil, err
		}
		if page.InheritPermissions == inherit {
			// No-op; return the current page without writing a revision so
			// the audit trail isn't polluted with churn from idempotent UI
			// calls.
			return page, nil
		}
		if err := s.pages.SetInheritPermissionsTx(tx, pageID, inherit, actorID); err != nil {
			return nil, err
		}
		updated, err := s.pages.GetByIDTx(tx, pageID)
		if err != nil {
			return nil, err
		}
		summary := "broke permission inheritance"
		if inherit {
			summary = "restored permission inheritance"
		}
		if _, err := s.writeRevisionTx(tx, updated, actorID, models.PageRevisionChangeTypePermissions, summary); err != nil {
			return nil, err
		}
		return updated, nil
	})
}

// validateGrantPrincipal verifies the principal exists (and is_active
// where applicable) inside the same transaction as the grant. We
// deliberately do NOT also check workspace membership here — membership
// can drop independently, and the runtime evaluator already requires
// workspace.page.view as a floor for ACL matches. This validation just
// catches dead-on-arrival grants (typo'd ids, deleted users) so the audit
// row points at a real entity.
func (s *PageService) validateGrantPrincipal(tx database.Tx, principalType string, principalID int) error {
	var query string
	switch principalType {
	case models.PagePrincipalTypeUser:
		query = "SELECT EXISTS(SELECT 1 FROM users WHERE id = ? AND is_active = 1)"
	case models.PagePrincipalTypeGroup:
		query = "SELECT EXISTS(SELECT 1 FROM groups WHERE id = ? AND is_active = 1)"
	case models.PagePrincipalTypeRole:
		query = "SELECT EXISTS(SELECT 1 FROM workspace_roles WHERE id = ?)"
	default:
		return ErrPageInvalidPrincipal
	}
	var exists bool
	if err := tx.QueryRow(query, principalID).Scan(&exists); err != nil {
		return fmt.Errorf("validate grant principal %s/%d: %w", principalType, principalID, err)
	}
	if !exists {
		return ErrPageGrantPrincipalNotFound
	}
	return nil
}

func isValidPrincipalType(t string) bool {
	return t == models.PagePrincipalTypeUser ||
		t == models.PagePrincipalTypeGroup ||
		t == models.PagePrincipalTypeRole
}

func isValidPermissionLevel(l string) bool {
	return l == models.PagePermissionLevelView ||
		l == models.PagePermissionLevelEdit ||
		l == models.PagePermissionLevelAdmin
}

// --- helpers ---

func (s *PageService) resolveParent(tx database.Tx, workspaceID int, parentID *int) (resolvedParentID *int, childPath string, parentDepth int, err error) {
	if parentID == nil {
		return nil, "/", -1, nil
	}
	parent, err := s.pages.GetByIDTx(tx, *parentID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, "", 0, ErrPageNotFound
		}
		return nil, "", 0, err
	}
	if parent.WorkspaceID != workspaceID {
		return nil, "", 0, ErrPageParentMismatch
	}
	parentPath := parent.Path + fmt.Sprintf("%d/", parent.ID)
	return &parent.ID, parentPath, parent.Depth, nil
}

// pickAvailableSlug returns a slug that does not collide with another
// non-archived sibling. excludeID lets Update keep its own slug when the
// title hasn't changed materially. Tries base, base-2, base-3, ... up to
// 50 attempts before giving up.
func (s *PageService) pickAvailableSlug(tx database.Tx, workspaceID int, parentID *int, base string, excludeID int) (string, error) {
	if base == "" {
		base = "page"
	}
	candidate := base
	for attempt := 2; attempt < 50; attempt++ {
		taken, err := slugInUseTx(tx, workspaceID, parentID, candidate, excludeID)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, attempt)
	}
	return "", ErrPageSlugConflict
}

func slugInUseTx(tx database.Tx, workspaceID int, parentID *int, slug string, excludeID int) (bool, error) {
	var n int
	var err error
	if parentID == nil {
		err = tx.QueryRow(`
			SELECT COUNT(*) FROM pages
			WHERE workspace_id = ? AND parent_id IS NULL AND slug = ? AND id != ?
		`, workspaceID, slug, excludeID).Scan(&n)
	} else {
		err = tx.QueryRow(`
			SELECT COUNT(*) FROM pages
			WHERE workspace_id = ? AND parent_id = ? AND slug = ? AND id != ?
		`, workspaceID, *parentID, slug, excludeID).Scan(&n)
	}
	if err != nil {
		return false, fmt.Errorf("check slug uniqueness: %w", err)
	}
	return n > 0, nil
}

var slugSpaceRe = regexp.MustCompile(`-+`)

func makeSlug(title string) string {
	var b strings.Builder
	b.Grow(len(title))
	for _, r := range title {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsSpace(r), r == '-', r == '_', r == '/', r == '\\':
			b.WriteByte('-')
		}
	}
	out := slugSpaceRe.ReplaceAllString(b.String(), "-")
	out = strings.Trim(out, "-")
	if len(out) > 80 {
		out = strings.TrimRight(out[:80], "-")
	}
	return out
}

// deriveExcerpt produces a short plain-text excerpt by stripping common
// Markdown syntax. Not a full Markdown parser — good enough for snippets.
func deriveExcerpt(content string) string {
	if content == "" {
		return ""
	}
	text := content
	text = strings.ReplaceAll(text, "\r", "")
	text = excerptCodeFence.ReplaceAllString(text, " ")
	text = excerptHeadingMark.ReplaceAllString(text, "")
	text = excerptListMark.ReplaceAllString(text, "")
	text = excerptInlineMark.ReplaceAllString(text, "")
	text = excerptLinkMark.ReplaceAllString(text, "$1")
	text = excerptHTML.ReplaceAllString(text, "")
	text = strings.Join(strings.Fields(text), " ")
	const maxLen = 240
	if len(text) > maxLen {
		text = strings.TrimRight(text[:maxLen], " ") + "…"
	}
	return text
}

var (
	excerptCodeFence   = regexp.MustCompile("(?s)```.*?```")
	excerptHeadingMark = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	excerptListMark    = regexp.MustCompile(`(?m)^(\s*)([-*+]|\d+\.)\s+`)
	excerptInlineMark  = regexp.MustCompile("[`*_~>]")
	excerptLinkMark    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	excerptHTML        = regexp.MustCompile(`(?s)<[^>]+>`)
)

func contentHash(content string) string {
	if content == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
