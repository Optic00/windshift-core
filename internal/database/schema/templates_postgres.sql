-- Work item templates (WI-438): workspace-scoped reusable bodies that
-- pre-fill a new item's description at creation time. (PostgreSQL)

CREATE TABLE IF NOT EXISTS item_templates (
    id SERIAL PRIMARY KEY,
    workspace_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    description_body TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL DEFAULT 'selectable',
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_by INTEGER,
    updated_by INTEGER,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL,
    FOREIGN KEY (updated_by) REFERENCES users(id) ON DELETE SET NULL,
    UNIQUE(name, workspace_id)
);

CREATE INDEX IF NOT EXISTS idx_item_templates_workspace_id ON item_templates(workspace_id);
CREATE INDEX IF NOT EXISTS idx_item_templates_ws_mode_active ON item_templates(workspace_id, mode, is_active);

-- Optional N:N target item-type filter. A mandatory template targets exactly
-- one type (enforced in the service layer); a selectable template targets zero
-- (global) or many.

CREATE TABLE IF NOT EXISTS item_template_item_types (
    template_id INTEGER NOT NULL,
    item_type_id INTEGER NOT NULL,
    PRIMARY KEY (template_id, item_type_id),
    FOREIGN KEY (template_id) REFERENCES item_templates(id) ON DELETE CASCADE,
    FOREIGN KEY (item_type_id) REFERENCES item_types(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_item_template_item_types_type ON item_template_item_types(item_type_id, template_id);
