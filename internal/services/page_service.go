// Package services — page_service owns the wiki-pages business rules:
// sanitization, slug derivation, path/depth bookkeeping, cycle prevention,
// and tree assembly. The HTTP handlers, AI tools, and knowledge retrieval
// service all go through PageService rather than touching the repository
// directly. Revisions and search chunks land in a follow-up slice.
package services

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
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
	ErrPageNotFound       = errors.New("page not found")
	ErrPageTitleRequired  = errors.New("page title is required")
	ErrPageParentMismatch = errors.New("parent page belongs to a different workspace")
	ErrPageCycle          = errors.New("move would create a cycle")
	ErrPageDepthExceeded  = errors.New("page tree depth limit exceeded")
	ErrPageSlugConflict   = errors.New("slug conflicts with an existing sibling page")
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

// UpdatePageInput is the request shape for Update.
type UpdatePageInput struct {
	ID                 int
	Title              string
	Content            string
	InheritPermissions bool
	Rank               *string
	FracIndex          *string
}

// Update overwrites a page's title/content/inheritance and recomputes the
// derived columns. Move (parent change) and Archive are separate calls so
// the audit trail and handler authorization paths stay distinct.
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
			InheritPermissions: in.InheritPermissions,
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

		return s.pages.GetByIDTx(tx, in.ID)
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

		return s.pages.GetByIDTx(tx, pageID)
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
		return nil
	})
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
