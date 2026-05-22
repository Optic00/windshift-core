-- Page labels: workspace-scoped labels that attach to pages (PostgreSQL).
-- Fully separate from the work-item label system (`labels` + `item_labels`).

CREATE TABLE IF NOT EXISTS page_labels (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    color TEXT DEFAULT '#3B82F6',
    workspace_id INTEGER NOT NULL,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    UNIQUE(name, workspace_id)
);

CREATE INDEX IF NOT EXISTS idx_page_labels_workspace_id ON page_labels(workspace_id);
CREATE INDEX IF NOT EXISTS idx_page_labels_workspace_name ON page_labels(workspace_id, name);

-- Junction table for page ↔ page_label many-to-many.

CREATE TABLE IF NOT EXISTS page_label_assignments (
    id SERIAL PRIMARY KEY,
    page_id INTEGER NOT NULL,
    page_label_id INTEGER NOT NULL,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE,
    FOREIGN KEY (page_label_id) REFERENCES page_labels(id) ON DELETE CASCADE,
    UNIQUE(page_id, page_label_id)
);

CREATE INDEX IF NOT EXISTS idx_page_label_assignments_page_id ON page_label_assignments(page_id);
CREATE INDEX IF NOT EXISTS idx_page_label_assignments_label_id ON page_label_assignments(page_label_id);
