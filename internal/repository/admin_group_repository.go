package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"windshift/internal/database"
)

// AdminGroupRecord is the admin-facing projection of a global group with its
// member count. It intentionally mirrors the REST v1 response shape without
// tying the repository to HTTP concerns.
type AdminGroupRecord struct {
	ID          int
	Name        string
	Description string
	MemberCount int
	CreatedAt   time.Time
}

// AdminGroupUpdate holds optional group fields for admin updates. Empty string
// values are ignored to preserve the existing v1 handler semantics.
type AdminGroupUpdate struct {
	Name        *string
	Description *string
}

// IsEmpty reports whether the update would touch no persisted fields.
func (u AdminGroupUpdate) IsEmpty() bool {
	return (u.Name == nil || *u.Name == "") && (u.Description == nil || *u.Description == "")
}

// AdminGroupRepository owns persistence for admin group management.
type AdminGroupRepository struct {
	db database.Database
}

// NewAdminGroupRepository creates an admin group repository.
func NewAdminGroupRepository(db database.Database) *AdminGroupRepository {
	return &AdminGroupRepository{db: db}
}

// Count returns the number of groups.
func (r *AdminGroupRepository) Count() (int, error) {
	var total int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM groups").Scan(&total); err != nil {
		return 0, fmt.Errorf("count groups: %w", err)
	}
	return total, nil
}

// List returns admin group records ordered by name.
func (r *AdminGroupRepository) List(limit, offset int) ([]AdminGroupRecord, error) {
	rows, err := r.db.Query(`
		SELECT g.id, g.name, g.description,
		       (SELECT COUNT(*) FROM group_members gm WHERE gm.group_id = g.id) AS member_count,
		       g.created_at
		FROM groups g
		ORDER BY g.name ASC
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	groups := []AdminGroupRecord{}
	for rows.Next() {
		var g AdminGroupRecord
		var desc sql.NullString
		if err := rows.Scan(&g.ID, &g.Name, &desc, &g.MemberCount, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		if desc.Valid {
			g.Description = desc.String
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate groups: %w", err)
	}
	return groups, nil
}

// Create inserts a group and returns its ID.
func (r *AdminGroupRepository) Create(name, description string, createdBy int) (int, error) {
	var id int
	err := r.db.QueryRow(`
		INSERT INTO groups (name, description, created_by, created_at, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		RETURNING id
	`, name, description, createdBy).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create group: %w", err)
	}
	return id, nil
}

// Update applies a partial group update. It returns ErrNotFound when no row is
// updated. Callers should check AdminGroupUpdate.IsEmpty before invoking it.
func (r *AdminGroupRepository) Update(id int, update AdminGroupUpdate) error {
	sets := []string{}
	args := []any{}
	if update.Name != nil && *update.Name != "" {
		sets = append(sets, "name = ?")
		args = append(args, *update.Name)
	}
	if update.Description != nil && *update.Description != "" {
		sets = append(sets, "description = ?")
		args = append(args, *update.Description)
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id)

	result, err := r.db.ExecWrite("UPDATE groups SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return fmt.Errorf("update group: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update group rows affected: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a group and its memberships transactionally. It returns
// ErrNotFound when the group row does not exist.
func (r *AdminGroupRepository) Delete(id int) error {
	return database.WithTx(r.db, func(tx database.Tx) error {
		if _, err := tx.Exec("DELETE FROM group_members WHERE group_id = ?", id); err != nil {
			return fmt.Errorf("delete group members: %w", err)
		}
		result, err := tx.Exec("DELETE FROM groups WHERE id = ?", id)
		if err != nil {
			return fmt.Errorf("delete group: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete group rows affected: %w", err)
		}
		if rows == 0 {
			return ErrNotFound
		}
		return nil
	})
}
