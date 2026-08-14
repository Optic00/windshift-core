package database

func migrateSQLiteGlobalItemLabels(db Database) error {
	return withSQLiteForeignKeysDisabled(db, "migrate global item labels", []string{
		`INSERT OR IGNORE INTO item_labels (item_id, label_id, created_at)
		 SELECT il.item_id,
		        (SELECT MIN(canonical.id) FROM labels canonical WHERE LOWER(canonical.name) = LOWER(duplicate.name)),
		        il.created_at
		 FROM item_labels il
		 JOIN labels duplicate ON duplicate.id = il.label_id
		 WHERE duplicate.id <> (
			 SELECT MIN(canonical.id) FROM labels canonical WHERE LOWER(canonical.name) = LOWER(duplicate.name)
		 )`,
		`DELETE FROM item_labels
		 WHERE label_id IN (
			 SELECT duplicate.id
			 FROM labels duplicate
			 WHERE duplicate.id <> (
				 SELECT MIN(canonical.id) FROM labels canonical WHERE LOWER(canonical.name) = LOWER(duplicate.name)
			 )
		 )`,
		`CREATE TABLE labels_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			color TEXT DEFAULT '#3B82F6',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO labels_new (id, name, color, created_at, updated_at)
		 SELECT id, name, color, created_at, updated_at
		 FROM labels duplicate
		 WHERE duplicate.id = (
			 SELECT MIN(canonical.id) FROM labels canonical WHERE LOWER(canonical.name) = LOWER(duplicate.name)
		 )`,
		`DROP TABLE labels`,
		`ALTER TABLE labels_new RENAME TO labels`,
		`CREATE UNIQUE INDEX uq_labels_name_ci ON labels(LOWER(name))`,
	})
}
