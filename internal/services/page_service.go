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
	db         database.Database
	pages      *repository.PageRepository
	pageLabels *repository.PageLabelRepository
}

// NewPageService creates a PageService backed by the provided database.
func NewPageService(db database.Database) *PageService {
	return &PageService{
		db:    db,
		pages: repository.NewPageRepository(db),
	}
}

// SetPageLabelRepository wires the page-label repository for label preload
// on tree/detail responses. Optional — when unset, responses still serialize
// pages but omit the `labels` field (it remains nil/empty).
func (s *PageService) SetPageLabelRepository(repo *repository.PageLabelRepository) {
	s.pageLabels = repo
}

// PreloadLabels populates Labels on each page when a page-label repository
// is wired. Safe to call on an empty slice or when the repo is unset.
func (s *PageService) PreloadLabels(pages []models.Page) error {
	if s.pageLabels == nil {
		return nil
	}
	return s.pageLabels.LoadForPages(pages)
}

// PreloadLabelsForPage loads labels for a single page. No-op when no
// page-label repository is wired.
func (s *PageService) PreloadLabelsForPage(page *models.Page) error {
	if s.pageLabels == nil || page == nil {
		return nil
	}
	labels, err := s.pageLabels.ListForPage(page.ID)
	if err != nil {
		return err
	}
	if labels == nil {
		labels = []models.PageLabel{}
	}
	page.Labels = labels
	return nil
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
// a normal title/content save, bypassing the admin gate. Rank / FracIndex
// are absent for the same reason: reordering goes through Move /
// SetFracIndex so a normal save cannot clear an existing ordering.
type UpdatePageInput struct {
	ID      int
	Title   string
	Content string
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
//
// prevSiblingID / nextSiblingID position the moved page within its new
// parent's children. Either may be nil to mean "start of list" / "end of
// list"; when both are nil the existing append-by-natural-order behavior
// is preserved (no frac_index write).
func (s *PageService) Move(actorID, pageID int, newParentID, prevSiblingID, nextSiblingID *int) (*models.Page, error) {
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

		// Compute the moved page's new frac_index when the caller supplied
		// sibling positioning, or when the parent is changing (in which
		// case we must give the page a fresh key in its new sibling set
		// rather than carry its old key). Backfills NULL keys for the
		// new parent's children so KeyBetween has well-defined neighbors
		// to bisect.
		parentChanged := !samePageParent(page.ParentID, newParentID)
		newFracIndex, err := s.resolveSiblingFracIndex(tx, page.WorkspaceID, newParentID, pageID, prevSiblingID, nextSiblingID, parentChanged, actorID)
		if err != nil {
			return nil, err
		}

		if err := s.pages.MoveTx(tx, pageID, newParentID, newPath, newDepth, actorID, newFracIndex); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrPageNotFound
			}
			// Repo maps unique-violations (composite UNIQUE on
			// workspace_id, parent_id, slug or the partial root-slug
			// index) to ErrDuplicateEntry. Without translation here the
			// handler hit its default 500 branch instead of the 409 the
			// other slug-conflict paths already produced.
			if errors.Is(err, repository.ErrDuplicateEntry) {
				return nil, ErrPageSlugConflict
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
		pathLike := prefix + "%"
		if _, err := tx.Exec(`
			UPDATE pages
			SET archived_at = CURRENT_TIMESTAMP,
			    archived_by = ?,
			    updated_at = CURRENT_TIMESTAMP,
			    updated_by = ?
			WHERE id = ? OR (workspace_id = ? AND path LIKE ?)
		`, actorID, actorID, pageID, page.WorkspaceID, pathLike); err != nil {
			return fmt.Errorf("archive subtree: %w", err)
		}

		// Drop the now-stale chunks for the archived subtree so search and
		// AI tools cannot surface content from a hidden page even before
		// the permission filter runs.
		if err := s.pages.DeleteChunksForSubtreeTx(tx, page.ID, page.WorkspaceID, pathLike); err != nil {
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
		// parent/path/depth/rank/frac_index are deliberately not restored —
		// moving and reordering a page are separate explicit actions, and
		// UpdateTx no longer touches those columns. If a user wants to undo
		// a move, they should run Move explicitly.
		if err := s.pages.UpdateTx(tx, repository.UpdateInput{
			ID:                 page.ID,
			Title:              rev.Title,
			Slug:               rev.Slug,
			Content:            rev.Content,
			ContentHash:        rev.ContentHash,
			Excerpt:            rev.Excerpt,
			InheritPermissions: page.InheritPermissions,
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

// ListTree returns every non-archived page in a workspace. Rows are
// grouped by depth (all roots, then all depth-1 children, …) and
// within each depth-band sorted by frac_index, rank, title, id —
// i.e. breadth-by-depth, not depth-first. Callers that care about
// rendering order rebuild the tree via BuildPageTree, which is
// id-based and order-insensitive.
func (s *PageService) ListTree(workspaceID int, includeArchived bool) ([]models.Page, error) {
	return s.pages.ListWorkspaceTree(workspaceID, includeArchived)
}

// SearchByTitle delegates to the repository's title-substring search.
// Permission filtering happens at the handler layer via ListVisiblePageIDs.
func (s *PageService) SearchByTitle(workspaceID int, query string, limit int) ([]models.Page, error) {
	return s.pages.SearchByTitle(workspaceID, query, limit)
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

// samePageParent treats nil as "workspace root" and compares two parent
// pointers structurally. Used by Move to decide whether the page is
// crossing into a different sibling set (which requires a fresh
// frac_index) or just reordering within its current one.
func samePageParent(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

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

// resolveSiblingFracIndex computes the frac_index the moved page should
// receive given prev/next sibling IDs that bracket the drop position.
//
// When the caller supplied no anchors and the parent is unchanged, returns
// nil (no frac_index write) to preserve the page's current position in
// its existing sibling set. When the caller supplied no anchors AND the
// parent is changing, the moved page is appended to the end of the new
// parent's children — without this, the page would carry its old key
// into the new sibling set and land in a visually arbitrary position
// (and potentially collide on the per-sibling-set uniqueness invariant).
//
// When neighbors have NULL frac_index (pages predating drag-and-drop),
// they are backfilled with monotonically-increasing keys in their
// current display order before KeyBetween is consulted, so subsequent
// reorders against the same group have well-defined endpoints.
func (s *PageService) resolveSiblingFracIndex(
	tx database.Tx,
	workspaceID int,
	newParentID *int,
	movedPageID int,
	prevSiblingID, nextSiblingID *int,
	parentChanged bool,
	actorID int,
) (*string, error) {
	if prevSiblingID == nil && nextSiblingID == nil && !parentChanged {
		return nil, nil
	}

	siblings, err := s.pages.ListChildrenTx(tx, workspaceID, newParentID)
	if err != nil {
		return nil, fmt.Errorf("list new parent children: %w", err)
	}

	// A neighbor pointed at the page being moved is meaningless — it
	// cannot be its own anchor. Treat as nil so KeyBetween still runs.
	if prevSiblingID != nil && *prevSiblingID == movedPageID {
		prevSiblingID = nil
	}
	if nextSiblingID != nil && *nextSiblingID == movedPageID {
		nextSiblingID = nil
	}

	siblingByID := make(map[int]*models.Page, len(siblings))
	for i := range siblings {
		if siblings[i].ID == movedPageID {
			continue
		}
		siblingByID[siblings[i].ID] = &siblings[i]
	}
	for _, id := range []*int{prevSiblingID, nextSiblingID} {
		if id == nil {
			continue
		}
		if _, ok := siblingByID[*id]; !ok {
			return nil, fmt.Errorf("sibling %d is not a child of the target parent", *id)
		}
	}

	// Append-to-end on a parent-changing move with no anchors: anchor
	// the new page after the current last sibling (or as the lone child
	// if the new parent is empty). The backfill branch below already
	// handles a sibling set with NULL keys; once that runs, the
	// per-sibling-set last key is known and the second pass below
	// (prevKey / nextKey computation) produces a fresh key to its right.
	if prevSiblingID == nil && nextSiblingID == nil {
		// No non-moved siblings → the moved page is the lone child;
		// pick a deterministic starting key.
		if len(siblingByID) == 0 {
			key, kerr := KeyBetween("", "")
			if kerr != nil {
				return nil, fmt.Errorf("compute initial frac_index for empty parent: %w", kerr)
			}
			return &key, nil
		}
		// Anchor after the last sibling in display order. ListChildrenTx
		// returns siblings sorted by COALESCE(frac_index,''), rank, title,
		// id — so the final non-moved entry is the visual end of the list.
		var lastSibling *models.Page
		for i := len(siblings) - 1; i >= 0; i-- {
			if siblings[i].ID == movedPageID {
				continue
			}
			lastSibling = &siblings[i]
			break
		}
		// lastSibling can't be nil here because len(siblingByID) > 0.
		prevSiblingID = &lastSibling.ID
	}

	needsBackfill := false
	for _, id := range []*int{prevSiblingID, nextSiblingID} {
		if id == nil {
			continue
		}
		sib := siblingByID[*id]
		if sib.FracIndex == nil || *sib.FracIndex == "" {
			needsBackfill = true
			break
		}
	}

	if needsBackfill {
		// Re-sequence ALL siblings (overwriting any existing frac_index
		// values too) in their current display order. Mixed NULL +
		// non-NULL groups can interleave in ways that would collide with
		// freshly minted keys, so a full rewrite is the only safe option.
		var lastKey string
		for i := range siblings {
			if siblings[i].ID == movedPageID {
				continue
			}
			fresh, kerr := KeyBetween(lastKey, "")
			if kerr != nil {
				return nil, fmt.Errorf("backfill frac_index for sibling %d: %w", siblings[i].ID, kerr)
			}
			if err := s.pages.SetFracIndexTx(tx, siblings[i].ID, fresh, actorID); err != nil {
				return nil, fmt.Errorf("persist backfilled frac_index for sibling %d: %w", siblings[i].ID, err)
			}
			siblings[i].FracIndex = &fresh
			siblingByID[siblings[i].ID] = &siblings[i]
			lastKey = fresh
		}
	}

	prevKey := ""
	if prevSiblingID != nil {
		if p := siblingByID[*prevSiblingID].FracIndex; p != nil {
			prevKey = *p
		}
	}
	nextKey := ""
	if nextSiblingID != nil {
		if n := siblingByID[*nextSiblingID].FracIndex; n != nil {
			nextKey = *n
		}
	}

	newKey, err := KeyBetween(prevKey, nextKey)
	if err != nil {
		return nil, fmt.Errorf("compute frac_index between %q and %q: %w", prevKey, nextKey, err)
	}
	return &newKey, nil
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
