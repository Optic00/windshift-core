-- Workspace knowledge pages (Confluence-style wiki, per knowledge-management-module-plan.md).
-- Markdown-source pages organized into a per-workspace tree, with immutable
-- revision history, page-level ACL rows, page attachments, and chunked
-- content for full-text / future vector search.

CREATE TABLE IF NOT EXISTS pages (
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
    frac_index TEXT COLLATE BINARY,
    path TEXT NOT NULL DEFAULT '/',
    depth INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at DATETIME,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES pages(id) ON DELETE CASCADE,
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT,
    FOREIGN KEY (updated_by) REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (archived_by) REFERENCES users(id) ON DELETE SET NULL,
    UNIQUE(workspace_id, parent_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_pages_workspace ON pages(workspace_id);
CREATE INDEX IF NOT EXISTS idx_pages_parent ON pages(parent_id);
CREATE INDEX IF NOT EXISTS idx_pages_workspace_parent ON pages(workspace_id, parent_id);
CREATE INDEX IF NOT EXISTS idx_pages_workspace_archived ON pages(workspace_id, archived_at);
CREATE INDEX IF NOT EXISTS idx_pages_path ON pages(path);
CREATE INDEX IF NOT EXISTS idx_pages_content_hash ON pages(content_hash) WHERE content_hash != '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_pages_workspace_home ON pages(workspace_id) WHERE is_home = 1 AND archived_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pages_workspace_parent_rank ON pages(workspace_id, parent_id, rank) WHERE rank IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_pages_frac_index ON pages(frac_index) WHERE frac_index IS NOT NULL;
-- UNIQUE(workspace_id, parent_id, slug) above is bypassed for root pages
-- because parent_id IS NULL collates as NOT EQUAL to itself in both
-- SQLite and PostgreSQL. This partial unique index plugs that gap for
-- live (non-archived) root pages so concurrent inserts cannot race past
-- the service-level pickAvailableSlug check. Archived root pages may
-- still share a slug — once archived they're frozen and out of the
-- caller's address space.
CREATE UNIQUE INDEX IF NOT EXISTS idx_pages_workspace_root_slug ON pages(workspace_id, slug) WHERE parent_id IS NULL AND archived_at IS NULL;

-- Immutable page revision history. revision_number is assigned MAX(revision_number)+1
-- inside the same transaction as the page mutation that produced it.

CREATE TABLE IF NOT EXISTS page_revisions (
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
    change_type TEXT NOT NULL DEFAULT 'edit' CHECK (change_type IN ('create', 'edit', 'move', 'permissions', 'restore', 'archive')),
    created_by INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE,
    FOREIGN KEY (parent_id) REFERENCES pages(id) ON DELETE SET NULL,
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT,
    UNIQUE(page_id, revision_number)
);

CREATE INDEX IF NOT EXISTS idx_page_revisions_page ON page_revisions(page_id, revision_number DESC);
CREATE INDEX IF NOT EXISTS idx_page_revisions_created_by ON page_revisions(created_by);
CREATE INDEX IF NOT EXISTS idx_page_revisions_created_at ON page_revisions(created_at DESC);

-- Page ACL rows. Phase 1 is grant-only; deny semantics are deferred.

CREATE TABLE IF NOT EXISTS page_permissions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    page_id INTEGER NOT NULL,
    principal_type TEXT NOT NULL CHECK (principal_type IN ('user', 'group', 'role')),
    principal_id INTEGER NOT NULL,
    permission_level TEXT NOT NULL CHECK (permission_level IN ('view', 'edit', 'admin')),
    granted_by INTEGER,
    granted_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE,
    FOREIGN KEY (granted_by) REFERENCES users(id) ON DELETE SET NULL,
    UNIQUE(page_id, principal_type, principal_id, permission_level)
);

CREATE INDEX IF NOT EXISTS idx_page_permissions_page ON page_permissions(page_id);
CREATE INDEX IF NOT EXISTS idx_page_permissions_principal ON page_permissions(principal_type, principal_id);

-- Page attachments are stored in the polymorphic `attachments` table with
-- entity_type='page' rather than a dedicated table; reuses the existing
-- upload/download/thumbnail/audit pipeline (handlers/attachment.go).

-- Search/RAG chunks. Rebuilt within the same transaction as any page content
-- change so chunks cannot drift from live content.

CREATE TABLE IF NOT EXISTS page_chunks (
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
    FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    UNIQUE(page_id, revision_number, position)
);

CREATE INDEX IF NOT EXISTS idx_page_chunks_page ON page_chunks(page_id);
CREATE INDEX IF NOT EXISTS idx_page_chunks_workspace ON page_chunks(workspace_id);
CREATE INDEX IF NOT EXISTS idx_page_chunks_hash ON page_chunks(content_hash) WHERE content_hash != '';

-- Vector search is intentionally not supported; the embeddings table and
-- knowledge.embedding_* / knowledge.vector_search_enabled settings were
-- removed. Full-text search over page_chunks (above) is the only retrieval
-- path. Fresh installs never see the legacy tables/settings; existing
-- installs get them dropped by the migration block in database.go.

-- Workspace-scoped page permission keys.

INSERT OR IGNORE INTO permissions (permission_key, permission_name, description, scope, is_system) VALUES
    ('page.view', 'View Pages', 'Can view workspace knowledge pages', 'workspace', 0),
    ('page.create', 'Create Pages', 'Can create workspace knowledge pages', 'workspace', 0),
    ('page.edit', 'Edit Pages', 'Can edit workspace knowledge pages', 'workspace', 0),
    ('page.delete', 'Delete Pages', 'Can delete or archive workspace knowledge pages', 'workspace', 0),
    ('page.admin', 'Administer Pages', 'Can manage page permissions and restore page versions', 'workspace', 0);

-- Default role grants for page permissions.

INSERT OR IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM workspace_roles r
JOIN permissions p ON p.permission_key = 'page.view'
WHERE r.name = 'Viewer';

INSERT OR IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM workspace_roles r
JOIN permissions p ON p.permission_key IN ('page.view', 'page.create', 'page.edit')
WHERE r.name = 'Editor';

INSERT OR IGNORE INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM workspace_roles r
JOIN permissions p ON p.permission_key IN ('page.view', 'page.create', 'page.edit', 'page.delete', 'page.admin')
WHERE r.name = 'Administrator';

-- Knowledge module system settings. Only the FTS toggle is honored;
-- vector-search settings were removed because we do not use vectors.

INSERT OR IGNORE INTO system_settings (key, value, value_type, description, category) VALUES
    ('knowledge.full_text_search_enabled', 'true', 'boolean', 'Enable full-text knowledge search', 'knowledge');
