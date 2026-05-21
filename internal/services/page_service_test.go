package services

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"windshift/internal/database"
)

// newPagesTestDB spins up an in-memory SQLite with the minimum schema the
// PageService and PageRepository touch. Avoids database.Initialize() so an
// unrelated module's startup migration cannot break these tests.
func newPagesTestDB(t *testing.T) database.Database {
	t.Helper()
	dsn := fmt.Sprintf("file:pages-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := database.NewSQLiteDB(dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, stmt := range []string{
		`CREATE TABLE workspaces (id INTEGER PRIMARY KEY, name TEXT, key TEXT)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT)`,
		`CREATE TABLE pages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace_id INTEGER NOT NULL,
			parent_id INTEGER,
			title TEXT NOT NULL,
			slug TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '',
			excerpt TEXT NOT NULL DEFAULT '',
			created_by INTEGER NOT NULL,
			updated_by INTEGER,
			archived_by INTEGER,
			is_home BOOLEAN NOT NULL DEFAULT 0,
			inherit_permissions BOOLEAN NOT NULL DEFAULT 1,
			rank TEXT,
			frac_index TEXT,
			path TEXT NOT NULL DEFAULT '/',
			depth INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			archived_at DATETIME,
			UNIQUE(workspace_id, parent_id, slug)
		)`,
		`INSERT INTO workspaces (id, name, key) VALUES (1, 'ws', 'WS'), (2, 'ws2', 'WS2')`,
		`INSERT INTO users (id, username) VALUES (1, 'alice'), (2, 'bob')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return db
}

func TestPageService_Create_RootSetsPathAndDepth(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)

	got, err := s.Create(1, CreatePageInput{
		WorkspaceID: 1,
		Title:       "Welcome",
		Content:     "# Hello",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Path != "/" {
		t.Errorf("root path: want /, got %q", got.Path)
	}
	if got.Depth != 0 {
		t.Errorf("root depth: want 0, got %d", got.Depth)
	}
	if got.Slug != "welcome" {
		t.Errorf("slug: want welcome, got %q", got.Slug)
	}
	if got.ContentHash == "" {
		t.Error("content hash should be set when content is non-empty")
	}
	if !got.InheritPermissions {
		t.Error("inherit_permissions should default to true")
	}
}

func TestPageService_Create_ChildInheritsParentPath(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)

	root, err := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "Root"})
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	child, err := s.Create(1, CreatePageInput{WorkspaceID: 1, ParentID: &root.ID, Title: "Child"})
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	wantPath := fmt.Sprintf("/%d/", root.ID)
	if child.Path != wantPath {
		t.Errorf("child path: want %q, got %q", wantPath, child.Path)
	}
	if child.Depth != 1 {
		t.Errorf("child depth: want 1, got %d", child.Depth)
	}
}

func TestPageService_Create_RejectsEmptyTitle(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)

	if _, err := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "   "}); !errors.Is(err, ErrPageTitleRequired) {
		t.Errorf("want ErrPageTitleRequired, got %v", err)
	}
}

func TestPageService_Create_RejectsCrossWorkspaceParent(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)

	other, err := s.Create(1, CreatePageInput{WorkspaceID: 2, Title: "Other"})
	if err != nil {
		t.Fatalf("seed other workspace page: %v", err)
	}
	if _, err := s.Create(1, CreatePageInput{
		WorkspaceID: 1,
		ParentID:    &other.ID,
		Title:       "Bad child",
	}); !errors.Is(err, ErrPageParentMismatch) {
		t.Errorf("want ErrPageParentMismatch, got %v", err)
	}
}

func TestPageService_Create_SlugDisambiguatesSiblings(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)

	first, err := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "Notes"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "Notes"})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.Slug != "notes" || second.Slug != "notes-2" {
		t.Errorf("slug disambiguation: got (%q, %q), want (notes, notes-2)", first.Slug, second.Slug)
	}
}

func TestPageService_Update_TitleChangeRetargetsSlug(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)

	page, err := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "Old"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := s.Update(2, UpdatePageInput{
		ID:                 page.ID,
		Title:              "Brand New",
		Content:            "body",
		InheritPermissions: true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Slug != "brand-new" {
		t.Errorf("slug: want brand-new, got %q", updated.Slug)
	}
	if updated.UpdatedBy == nil || *updated.UpdatedBy != 2 {
		t.Errorf("updated_by: want 2, got %v", updated.UpdatedBy)
	}
}

func TestPageService_Move_RejectsSelf(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	page, err := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "Solo"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Move(1, page.ID, &page.ID); !errors.Is(err, ErrPageCycle) {
		t.Errorf("self-move: want ErrPageCycle, got %v", err)
	}
}

func TestPageService_Move_RejectsCycle(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	a, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "A"})
	b, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, ParentID: &a.ID, Title: "B"})
	c, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, ParentID: &b.ID, Title: "C"})

	// Moving A under C (its grandchild) would create a cycle.
	if _, err := s.Move(1, a.ID, &c.ID); !errors.Is(err, ErrPageCycle) {
		t.Errorf("want ErrPageCycle, got %v", err)
	}
}

func TestPageService_Move_UpdatesDescendantPaths(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	a, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "A"})
	b, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, ParentID: &a.ID, Title: "B"})
	c, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, ParentID: &b.ID, Title: "C"})
	otherRoot, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "OtherRoot"})

	// Move B under otherRoot. c (descendant of b) should follow.
	if _, err := s.Move(1, b.ID, &otherRoot.ID); err != nil {
		t.Fatalf("move: %v", err)
	}

	bAfter, err := s.GetByID(b.ID)
	if err != nil {
		t.Fatalf("get b: %v", err)
	}
	wantBPath := fmt.Sprintf("/%d/", otherRoot.ID)
	if bAfter.Path != wantBPath || bAfter.Depth != 1 {
		t.Errorf("b after move: path=%q depth=%d, want path=%q depth=1", bAfter.Path, bAfter.Depth, wantBPath)
	}

	cAfter, err := s.GetByID(c.ID)
	if err != nil {
		t.Fatalf("get c: %v", err)
	}
	wantCPath := fmt.Sprintf("/%d/%d/", otherRoot.ID, b.ID)
	if cAfter.Path != wantCPath || cAfter.Depth != 2 {
		t.Errorf("c after move: path=%q depth=%d, want path=%q depth=2", cAfter.Path, cAfter.Depth, wantCPath)
	}

	// Original root A is no longer an ancestor of B/C.
	aAfter, _ := s.GetByID(a.ID)
	if aAfter.Path != "/" || aAfter.Depth != 0 {
		t.Errorf("a should be unchanged, got path=%q depth=%d", aAfter.Path, aAfter.Depth)
	}
}

func TestPageService_Archive_CascadesToSubtree(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	a, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "A"})
	b, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, ParentID: &a.ID, Title: "B"})
	c, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, ParentID: &b.ID, Title: "C"})
	other, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "Other"})

	if err := s.Archive(1, a.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	for _, id := range []int{a.ID, b.ID, c.ID} {
		page, err := s.GetByID(id)
		if err != nil {
			t.Fatalf("get %d: %v", id, err)
		}
		if page.ArchivedAt == nil {
			t.Errorf("page %d should be archived", id)
		}
	}

	// Other root must remain live.
	otherAfter, _ := s.GetByID(other.ID)
	if otherAfter.ArchivedAt != nil {
		t.Errorf("unrelated page %d should not be archived", other.ID)
	}
}

func TestPageService_ListTree_FiltersArchived(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	live, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "Live"})
	archived, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "Bye"})
	if err := s.Archive(1, archived.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	visible, err := s.ListTree(1, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(visible) != 1 || visible[0].ID != live.ID {
		t.Errorf("expected only live page in tree, got %+v", visible)
	}

	full, err := s.ListTree(1, true)
	if err != nil {
		t.Fatalf("list w/ archived: %v", err)
	}
	if len(full) != 2 {
		t.Errorf("expected 2 pages with archived included, got %d", len(full))
	}
}

func TestBuildPageTree_AssemblesNestedNodes(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	a, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "A"})
	b, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, ParentID: &a.ID, Title: "B"})
	_, _ = s.Create(1, CreatePageInput{WorkspaceID: 1, ParentID: &b.ID, Title: "C"})

	flat, err := s.ListTree(1, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	roots := BuildPageTree(flat)
	if len(roots) != 1 || roots[0].ID != a.ID {
		t.Fatalf("expected single root A, got %+v", roots)
	}
	if len(roots[0].Children) != 1 || roots[0].Children[0].ID != b.ID {
		t.Fatalf("expected A→B, got %+v", roots[0].Children)
	}
	if len(roots[0].Children[0].Children) != 1 {
		t.Fatalf("expected B→C, got %+v", roots[0].Children[0].Children)
	}
}
