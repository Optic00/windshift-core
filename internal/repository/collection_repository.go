package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"windshift/internal/database"
)

// CollectionRecord is the read projection needed by REST v1 collection embeds.
type CollectionRecord struct {
	ID          int
	Slug        string
	Name        string
	Description string
	QLQuery     string
	WorkspaceID *int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	IsPublic    bool
	CreatedBy   *int
}

// CollectionRepository owns collection metadata lookups.
type CollectionRepository struct {
	db database.Database
}

// NewCollectionRepository creates a collection repository.
func NewCollectionRepository(db database.Database) *CollectionRepository {
	return &CollectionRepository{db: db}
}

// GetByID fetches a collection by numeric id.
func (r *CollectionRepository) GetByID(id int) (*CollectionRecord, error) {
	var rec CollectionRecord
	var workspaceID sql.NullInt64
	var createdBy sql.NullInt64
	var slug sql.NullString
	err := r.db.QueryRow(`
		SELECT id, name, COALESCE(description, ''), COALESCE(ql_query, ''), workspace_id, is_public, created_by,
		       public_slug, created_at, updated_at
		FROM collections
		WHERE id = ?
	`, id).Scan(
		&rec.ID, &rec.Name, &rec.Description, &rec.QLQuery,
		&workspaceID, &rec.IsPublic, &createdBy,
		&slug, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get collection by id: %w", err)
	}
	hydrateCollectionNulls(&rec, workspaceID, createdBy)
	if slug.Valid {
		rec.Slug = slug.String
	}
	return &rec, nil
}

// GetBySlug fetches a collection by public slug.
func (r *CollectionRepository) GetBySlug(slug string) (*CollectionRecord, error) {
	var rec CollectionRecord
	var workspaceID sql.NullInt64
	var createdBy sql.NullInt64
	err := r.db.QueryRow(`
		SELECT id, name, COALESCE(description, ''), workspace_id, is_public, created_by, created_at, updated_at
		FROM collections
		WHERE public_slug = ? AND public_slug IS NOT NULL
	`, slug).Scan(
		&rec.ID, &rec.Name, &rec.Description,
		&workspaceID, &rec.IsPublic, &createdBy,
		&rec.CreatedAt, &rec.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get collection by slug: %w", err)
	}
	rec.Slug = slug
	hydrateCollectionNulls(&rec, workspaceID, createdBy)
	return &rec, nil
}

// SearchByName returns a bounded candidate set for case-insensitive collection
// name search. Callers still apply per-row visibility filtering.
func (r *CollectionRepository) SearchByName(q string, limit int) ([]CollectionRecord, error) {
	pattern := "%" + strings.ToLower(q) + "%"
	rows, err := r.db.Query(`
		SELECT id, name, COALESCE(description, ''), workspace_id, is_public, created_by,
		       COALESCE(public_slug, ''), created_at, updated_at
		FROM collections
		WHERE LOWER(name) LIKE ?
		ORDER BY name ASC
		LIMIT ?
	`, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search collections: %w", err)
	}
	defer func() { _ = rows.Close() }()

	records := []CollectionRecord{}
	for rows.Next() {
		var rec CollectionRecord
		var workspaceID sql.NullInt64
		var createdBy sql.NullInt64
		if err := rows.Scan(
			&rec.ID, &rec.Name, &rec.Description,
			&workspaceID, &rec.IsPublic, &createdBy,
			&rec.Slug, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan collection: %w", err)
		}
		hydrateCollectionNulls(&rec, workspaceID, createdBy)
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate collections: %w", err)
	}
	return records, nil
}

func hydrateCollectionNulls(rec *CollectionRecord, workspaceID, createdBy sql.NullInt64) {
	if workspaceID.Valid {
		id := int(workspaceID.Int64)
		rec.WorkspaceID = &id
	}
	if createdBy.Valid {
		id := int(createdBy.Int64)
		rec.CreatedBy = &id
	}
}
