-- Core tables (custom field definitions)
-- The legacy `projects` table that lived here was removed. "Project" in
-- Windshift refers exclusively to `time_projects` (see time_tracking.sql);
-- pre-existing databases keep the orphan `projects` table around but no
-- code reads or writes it.

CREATE TABLE IF NOT EXISTS custom_field_definitions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	field_type TEXT NOT NULL,
	description TEXT,
	required BOOLEAN DEFAULT false,
	options TEXT,
	display_order INTEGER DEFAULT 0,
	system_default BOOLEAN DEFAULT false,
	applies_to_portal_customers BOOLEAN DEFAULT false,
	applies_to_customer_organisations BOOLEAN DEFAULT false,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS custom_field_indexes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	custom_field_id INTEGER NOT NULL,
	target_table TEXT NOT NULL,
	index_name TEXT NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (custom_field_id) REFERENCES custom_field_definitions(id) ON DELETE CASCADE,
	UNIQUE(custom_field_id, target_table)
);
