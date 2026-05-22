-- Page labels: workspace-scoped labels that attach to pages. Fully separate
-- from the work-item label system (`labels` + `item_labels`); the two never
-- share rows or join keys.

CREATE TABLE IF NOT EXISTS page_labels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    color TEXT DEFAULT '#3B82F6',
    workspace_id INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    UNIQUE(name, workspace_id)
);

CREATE INDEX IF NOT EXISTS idx_page_labels_workspace_id ON page_labels(workspace_id);
CREATE INDEX IF NOT EXISTS idx_page_labels_workspace_name ON page_labels(workspace_id, name);

-- Junction table for page ↔ page_label many-to-many. Named
-- `page_label_assignments` rather than `page_page_labels` to avoid colliding
-- with the entity table above.

CREATE TABLE IF NOT EXISTS page_label_assignments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    page_id INTEGER NOT NULL,
    page_label_id INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE,
    FOREIGN KEY (page_label_id) REFERENCES page_labels(id) ON DELETE CASCADE,
    UNIQUE(page_id, page_label_id)
);

CREATE INDEX IF NOT EXISTS idx_page_label_assignments_page_id ON page_label_assignments(page_id);
CREATE INDEX IF NOT EXISTS idx_page_label_assignments_label_id ON page_label_assignments(page_label_id);
