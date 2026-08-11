-- Default data for initial setup (SQLite)
-- This file contains INSERT statements for default priorities

-- Default priorities
INSERT INTO priorities (name, description, icon, color, sort_order, is_default)
VALUES
    ('Critical', 'Urgent items requiring immediate attention', 'AlertCircle', '#dc2626', 1, FALSE),
    ('High', 'High priority items', 'ArrowUp', '#ea580c', 2, FALSE),
    ('Medium', 'Normal priority items', 'Minus', '#ca8a04', 3, TRUE),
    ('Low', 'Low priority items', 'ArrowDown', '#16a34a', 4, FALSE);

-- migration: 0000_baseline
