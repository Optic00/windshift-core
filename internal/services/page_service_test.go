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
		`CREATE TABLE page_revisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			page_id INTEGER NOT NULL,
			revision_number INTEGER NOT NULL,
			title TEXT NOT NULL,
			slug TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '',
			excerpt TEXT NOT NULL DEFAULT '',
			parent_id INTEGER,
			path TEXT NOT NULL DEFAULT '/',
			depth INTEGER NOT NULL DEFAULT 0,
			change_summary TEXT NOT NULL DEFAULT '',
			change_type TEXT NOT NULL DEFAULT 'edit',
			created_by INTEGER NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(page_id, revision_number)
		)`,
		`CREATE TABLE page_permissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			page_id INTEGER NOT NULL,
			principal_type TEXT NOT NULL,
			principal_id INTEGER NOT NULL,
			permission_level TEXT NOT NULL,
			granted_by INTEGER,
			granted_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(page_id, principal_type, principal_id, permission_level)
		)`,
		`CREATE TABLE page_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			page_id INTEGER NOT NULL,
			workspace_id INTEGER NOT NULL,
			revision_number INTEGER NOT NULL,
			position INTEGER NOT NULL,
			heading_path TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			token_count INTEGER NOT NULL DEFAULT 0,
			byte_start INTEGER NOT NULL DEFAULT 0,
			byte_end INTEGER NOT NULL DEFAULT 0,
			content_hash TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(page_id, revision_number, position)
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

func TestPageService_Create_WritesFirstRevisionAndChunks(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)

	page, err := s.Create(1, CreatePageInput{
		WorkspaceID: 1,
		Title:       "Onboarding",
		Content:     "# Welcome\n\nThis is the intro.\n\n## Setup\n\nFollow these steps.",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	revs, err := s.ListRevisions(page.ID, 0, 0)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("expected 1 revision after create, got %d", len(revs))
	}
	if revs[0].ChangeType != "create" || revs[0].RevisionNumber != 1 {
		t.Errorf("first revision: change_type=%q rev=%d, want change_type=create rev=1", revs[0].ChangeType, revs[0].RevisionNumber)
	}

	chunks := listChunksForPage(t, db, page.ID)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks (2 headings), got %d", len(chunks))
	}
	if chunks[0].RevisionNumber != 1 {
		t.Errorf("chunk revision_number: want 1, got %d", chunks[0].RevisionNumber)
	}
	if chunks[0].HeadingPath == "" {
		t.Error("first chunk should carry the heading_path of the first heading section")
	}
}

func TestPageService_Update_BumpsRevisionAndRebuildsChunks(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)

	page, err := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "Doc", Content: "# A\n\nbody"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := s.Update(2, UpdatePageInput{ID: page.ID, Title: "Doc", Content: "# A\n\nrewritten body"}); err != nil {
		t.Fatalf("update: %v", err)
	}

	revs, err := s.ListRevisions(page.ID, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("expected 2 revisions, got %d", len(revs))
	}
	if revs[0].RevisionNumber != 2 || revs[0].ChangeType != "edit" {
		t.Errorf("newest revision: rev=%d type=%q, want 2/edit", revs[0].RevisionNumber, revs[0].ChangeType)
	}

	chunks := listChunksForPage(t, db, page.ID)
	for _, c := range chunks {
		if c.RevisionNumber != 2 {
			t.Errorf("chunk revision_number: want 2, got %d", c.RevisionNumber)
		}
		if !strings.Contains(c.Content, "rewritten") {
			t.Errorf("chunk content should reflect latest update, got %q", c.Content)
		}
	}
}

func TestPageService_Restore_ProducesRestoreRevision(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)

	page, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "Doc", Content: "original"})
	if _, err := s.Update(2, UpdatePageInput{ID: page.ID, Title: "Doc", Content: "second pass"}); err != nil {
		t.Fatalf("update: %v", err)
	}

	revs, _ := s.ListRevisions(page.ID, 0, 0)
	// revs[0] = rev 2 (edit), revs[1] = rev 1 (create with "original" content)
	var rev1 int
	for _, r := range revs {
		if r.RevisionNumber == 1 {
			rev1 = r.ID
			break
		}
	}
	if rev1 == 0 {
		t.Fatalf("revision 1 not found in %+v", revs)
	}

	restored, err := s.Restore(1, page.ID, rev1)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Content != "original" {
		t.Errorf("restored content: want %q, got %q", "original", restored.Content)
	}

	revs2, _ := s.ListRevisions(page.ID, 0, 0)
	if len(revs2) != 3 {
		t.Fatalf("expected 3 revisions after restore, got %d", len(revs2))
	}
	if revs2[0].ChangeType != "restore" {
		t.Errorf("newest revision should be 'restore', got %q", revs2[0].ChangeType)
	}
}

func TestPageService_Restore_RejectsCrossPageRevision(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	a, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "A", Content: "a"})
	b, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "B", Content: "b"})

	bRevs, _ := s.ListRevisions(b.ID, 0, 0)
	if len(bRevs) == 0 {
		t.Fatalf("no revisions on B")
	}
	if _, err := s.Restore(1, a.ID, bRevs[0].ID); !errors.Is(err, ErrPageRevisionMismatch) {
		t.Errorf("want ErrPageRevisionMismatch, got %v", err)
	}
}

func TestPageService_Archive_RemovesChunks(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	page, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "Old", Content: "stuff to forget"})

	if got := listChunksForPage(t, db, page.ID); len(got) == 0 {
		t.Fatalf("expected chunks before archive")
	}
	if err := s.Archive(1, page.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if got := listChunksForPage(t, db, page.ID); len(got) != 0 {
		t.Errorf("expected chunks removed on archive, got %d", len(got))
	}

	revs, _ := s.ListRevisions(page.ID, 0, 0)
	if len(revs) == 0 || revs[0].ChangeType != "archive" {
		t.Errorf("newest revision should be 'archive', got %+v", revs)
	}
}

// listChunksForPage reads page_chunks directly for assertions, decoupled
// from the service surface so test failures point at the chunk pipeline.
type chunkRow struct {
	Position       int
	RevisionNumber int
	HeadingPath    string
	Content        string
}

func listChunksForPage(t *testing.T, db database.Database, pageID int) []chunkRow {
	t.Helper()
	rows, err := db.Query(`
		SELECT position, revision_number, heading_path, content
		FROM page_chunks WHERE page_id = ? ORDER BY position
	`, pageID)
	if err != nil {
		t.Fatalf("query chunks: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []chunkRow
	for rows.Next() {
		var c chunkRow
		if err := rows.Scan(&c.Position, &c.RevisionNumber, &c.HeadingPath, &c.Content); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, c)
	}
	return out
}

func TestChunkPageMarkdown_BreaksOnHeadings(t *testing.T) {
	md := "# Top\n\nintro\n\n## Mid\n\nmiddle\n\n### Deep\n\nbottom\n"
	specs := chunkPageMarkdown(md)
	if len(specs) < 3 {
		t.Fatalf("expected at least 3 chunks for 3 headings, got %d", len(specs))
	}
	if specs[0].HeadingPath != "Top" {
		t.Errorf("first chunk heading_path: want Top, got %q", specs[0].HeadingPath)
	}
	if specs[1].HeadingPath != "Top > Mid" {
		t.Errorf("second chunk heading_path: want Top > Mid, got %q", specs[1].HeadingPath)
	}
	if specs[2].HeadingPath != "Top > Mid > Deep" {
		t.Errorf("third chunk heading_path: want Top > Mid > Deep, got %q", specs[2].HeadingPath)
	}
}

func TestChunkPageMarkdown_SplitsOversizeSections(t *testing.T) {
	body := strings.Repeat("paragraph one. ", 200) + "\n\n" + strings.Repeat("paragraph two. ", 200)
	md := "# Big\n\n" + body
	specs := chunkPageMarkdown(md)
	if len(specs) < 2 {
		t.Fatalf("expected oversize section to split, got %d", len(specs))
	}
	for _, s := range specs {
		if len(s.Content) > pageChunkMaxBytes+pageChunkMinBytes {
			t.Errorf("chunk exceeds max+min slack: %d bytes", len(s.Content))
		}
	}
}

func TestPageService_GrantPermission_PersistsAndRecordsRevision(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	page, err := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "T", Content: "c"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	row, err := s.GrantPermission(1, page.ID, "user", 2, "edit")
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if row.PageID != page.ID || row.PrincipalID != 2 || row.PermissionLevel != "edit" {
		t.Errorf("granted row mismatch: %+v", row)
	}

	// Audit revision should record the change.
	revs, _ := s.ListRevisions(page.ID, 0, 0)
	found := false
	for _, r := range revs {
		if r.ChangeType == "permissions" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'permissions' revision after grant; got revs=%+v", revs)
	}
}

func TestPageService_GrantPermission_RejectsBadPrincipalAndLevel(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	page, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "T"})

	if _, err := s.GrantPermission(1, page.ID, "team", 5, "edit"); !errors.Is(err, ErrPageInvalidPrincipal) {
		t.Errorf("bad principal: want ErrPageInvalidPrincipal, got %v", err)
	}
	if _, err := s.GrantPermission(1, page.ID, "user", 5, "owner"); !errors.Is(err, ErrPageInvalidLevel) {
		t.Errorf("bad level: want ErrPageInvalidLevel, got %v", err)
	}
}

func TestPageService_GrantPermission_DuplicateReturnsError(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	page, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "T"})

	if _, err := s.GrantPermission(1, page.ID, "user", 2, "edit"); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	if _, err := s.GrantPermission(1, page.ID, "user", 2, "edit"); !errors.Is(err, ErrPagePermissionDuplicate) {
		t.Errorf("duplicate grant: want ErrPagePermissionDuplicate, got %v", err)
	}
}

func TestPageService_RevokePermission_RemovesRow(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	page, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "T"})
	row, _ := s.GrantPermission(1, page.ID, "user", 2, "edit")

	if err := s.RevokePermission(1, page.ID, row.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	acl, _ := s.ListOwnACL(page.ID)
	if len(acl) != 0 {
		t.Errorf("expected empty ACL after revoke, got %d", len(acl))
	}
}

func TestPageService_RevokePermission_RejectsCrossPageID(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	a, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "A"})
	b, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "B"})
	rowA, _ := s.GrantPermission(1, a.ID, "user", 2, "edit")

	if err := s.RevokePermission(1, b.ID, rowA.ID); !errors.Is(err, ErrPageNotFound) {
		t.Errorf("cross-page revoke: want ErrPageNotFound, got %v", err)
	}
	// The row should still exist under page A.
	acl, _ := s.ListOwnACL(a.ID)
	if len(acl) != 1 {
		t.Errorf("row should remain on page A, got %d", len(acl))
	}
}

func TestPageService_SetInheritPermissions_TogglesAndRecords(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	page, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "T"})
	if !page.InheritPermissions {
		t.Fatal("page should inherit by default")
	}

	updated, err := s.SetInheritPermissions(1, page.ID, false)
	if err != nil {
		t.Fatalf("set inherit=false: %v", err)
	}
	if updated.InheritPermissions {
		t.Error("expected inherit_permissions=false after flip")
	}

	// Revision recorded with change_type=permissions.
	revs, _ := s.ListRevisions(page.ID, 0, 0)
	if len(revs) == 0 || revs[0].ChangeType != "permissions" {
		t.Errorf("expected newest revision to be 'permissions', got %+v", revs)
	}
}

func TestPageService_SetInheritPermissions_NoopWhenUnchanged(t *testing.T) {
	db := newPagesTestDB(t)
	s := NewPageService(db)
	page, _ := s.Create(1, CreatePageInput{WorkspaceID: 1, Title: "T"})

	// Already inherit=true; calling with true should be a no-op.
	before, _ := s.ListRevisions(page.ID, 0, 0)
	if _, err := s.SetInheritPermissions(1, page.ID, true); err != nil {
		t.Fatalf("noop set: %v", err)
	}
	after, _ := s.ListRevisions(page.ID, 0, 0)
	if len(after) != len(before) {
		t.Errorf("no-op set inheritance should not add a revision; before=%d after=%d", len(before), len(after))
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
