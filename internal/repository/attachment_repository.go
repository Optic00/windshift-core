package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"windshift/internal/database"
)

// AttachmentDownloadRecord is the persistence projection needed to authorize
// and stream an item-scoped attachment download.
type AttachmentDownloadRecord struct {
	WorkspaceID      int
	Filename         string
	OriginalFilename string
	FilePath         string
	MimeType         string
	FileSize         int64
}

// AttachmentThumbnailRecord is the persistence projection needed to authorize
// and stream an item-scoped attachment thumbnail.
type AttachmentThumbnailRecord struct {
	WorkspaceID   int
	ThumbnailPath string
	HasThumbnail  bool
}

// AttachmentRepository owns attachment metadata lookups.
type AttachmentRepository struct {
	db database.Database
}

// NewAttachmentRepository creates an attachment repository.
func NewAttachmentRepository(db database.Database) *AttachmentRepository {
	return &AttachmentRepository{db: db}
}

// GetItemDownloadRecord returns download metadata for an item-scoped
// attachment. Non-item attachments are deliberately hidden from the items-token
// route and return ErrNotFound.
func (r *AttachmentRepository) GetItemDownloadRecord(id int) (*AttachmentDownloadRecord, error) {
	var rec AttachmentDownloadRecord
	err := r.db.QueryRow(`
		SELECT i.workspace_id, a.filename, a.original_filename, a.file_path, a.mime_type, a.file_size
		FROM attachments a
		JOIN items i ON i.id = a.item_id
		WHERE a.id = ?
		  AND a.item_id IS NOT NULL
		  AND (a.entity_type IS NULL OR a.entity_type = '' OR a.entity_type = 'item')
	`, id).Scan(&rec.WorkspaceID, &rec.Filename, &rec.OriginalFilename, &rec.FilePath, &rec.MimeType, &rec.FileSize)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get attachment download: %w", err)
	}
	return &rec, nil
}

// GetItemThumbnailRecord returns thumbnail metadata for an item-scoped
// attachment. Missing thumbnails and non-item attachments are deliberately
// hidden from the items-token route and return ErrNotFound.
func (r *AttachmentRepository) GetItemThumbnailRecord(id int) (*AttachmentThumbnailRecord, error) {
	var rec AttachmentThumbnailRecord
	err := r.db.QueryRow(`
		SELECT i.workspace_id, a.has_thumbnail, a.thumbnail_path
		FROM attachments a
		JOIN items i ON i.id = a.item_id
		WHERE a.id = ?
		  AND a.item_id IS NOT NULL
		  AND (a.entity_type IS NULL OR a.entity_type = '' OR a.entity_type = 'item')
		  AND a.has_thumbnail = true
		  AND a.thumbnail_path IS NOT NULL
		  AND a.thumbnail_path <> ''
	`, id).Scan(&rec.WorkspaceID, &rec.HasThumbnail, &rec.ThumbnailPath)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get attachment thumbnail: %w", err)
	}
	return &rec, nil
}
