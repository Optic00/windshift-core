package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/fileserve"
	"windshift/internal/models"
	"windshift/internal/repository"
)

var (
	// ErrItemAttachmentDisabled is returned when attachment storage is not
	// configured on this server.
	ErrItemAttachmentDisabled = errors.New("item attachment storage disabled")
	// ErrItemAttachmentInvalid wraps a validation failure (bad extension,
	// content-type mismatch, oversize file, disallowed MIME type).
	ErrItemAttachmentInvalid = errors.New("item attachment upload invalid")
	// ErrItemAttachmentNotFound is returned when the target item — or the
	// attachment being deleted — doesn't exist or isn't visible to the
	// caller. Collapsing "not found" and "forbidden" matches the v1 items
	// convention so resource existence is never leaked.
	ErrItemAttachmentNotFound = errors.New("item attachment target not found")
)

// ItemAttachmentService owns item-attachment upload and delete: item-edit
// authorization, validation, on-disk storage, thumbnailing, DB record
// management, and best-effort item-history rows. It is the bearer-token v1
// counterpart to the cookie-auth AttachmentHandler's item branches; both
// surfaces share the same validation helpers (extension/content/MIME), the
// AttachmentService record + authorization layer, and fileserve's root
// confinement so storage semantics stay in one place.
type ItemAttachmentService struct {
	db                database.Database
	attachmentPath    string
	attachmentService *AttachmentService
}

// NewItemAttachmentService creates an item-attachment upload/delete service.
func NewItemAttachmentService(db database.Database, attachmentPath string, permissionService *PermissionService) *ItemAttachmentService {
	return &ItemAttachmentService{
		db:                db,
		attachmentPath:    attachmentPath,
		attachmentService: NewAttachmentServiceWithPermissions(db, permissionService),
	}
}

// ItemAttachmentUploadInput contains the validated HTTP upload payload.
type ItemAttachmentUploadInput struct {
	ItemID           int
	UploaderID       int
	OriginalFilename string
	FileData         []byte
	FileSize         int64
}

// ItemAttachmentUploadPolicy is the public-safe subset of attachment settings.
// It intentionally omits the server filesystem path.
type ItemAttachmentUploadPolicy struct {
	Enabled          bool
	MaxFileSize      int64
	AllowedMimeTypes []string
}

// UploadPolicy returns the current public-safe upload limits.
func (s *ItemAttachmentService) UploadPolicy() (ItemAttachmentUploadPolicy, error) {
	if s.attachmentPath == "" {
		return ItemAttachmentUploadPolicy{}, nil
	}
	settings, err := loadAttachmentSettings(s.db, s.attachmentPath)
	if err != nil {
		return ItemAttachmentUploadPolicy{}, err
	}
	policy := ItemAttachmentUploadPolicy{Enabled: settings.Enabled, MaxFileSize: settings.MaxFileSize}
	if settings.AllowedMimeTypes != "" && json.Valid([]byte(settings.AllowedMimeTypes)) {
		_ = json.Unmarshal([]byte(settings.AllowedMimeTypes), &policy.AllowedMimeTypes)
	}
	return policy, nil
}

// ValidatePublicFormAttachment performs every file-level check before a form
// item is created. UploadPublicFormAttachment repeats these checks before
// storage, keeping validation safe across the create/store boundary.
func (s *ItemAttachmentService) ValidatePublicFormAttachment(in ItemAttachmentUploadInput) error {
	return s.validateUpload(in)
}

// UploadItemAttachment stores a new attachment for an item and returns the
// same response model the cookie-auth upload endpoint uses for regular
// attachments, so bearer and cookie callers see identical shapes.
func (s *ItemAttachmentService) UploadItemAttachment(in ItemAttachmentUploadInput) (models.AttachmentUploadResponse, error) {
	return s.uploadItemAttachment(in, true)
}

// UploadPublicFormAttachment stores a file on an item that the public-form
// handler has just created. It deliberately skips actor authorization because
// the caller never accepts an item id from the browser; target ownership was
// established by the channel/request-type validation and CreateItem call.
func (s *ItemAttachmentService) UploadPublicFormAttachment(in ItemAttachmentUploadInput) (models.AttachmentUploadResponse, error) {
	return s.uploadItemAttachment(in, false)
}

func (s *ItemAttachmentService) validateUpload(in ItemAttachmentUploadInput) error {
	if s.attachmentPath == "" {
		return ErrItemAttachmentDisabled
	}
	if strings.TrimSpace(in.OriginalFilename) == "" {
		return fmt.Errorf("%w: filename is required", ErrItemAttachmentInvalid)
	}
	if len(in.FileData) == 0 {
		return fmt.Errorf("%w: file is empty", ErrItemAttachmentInvalid)
	}
	if err := validateAttachmentFileExtension(in.OriginalFilename); err != nil {
		return fmt.Errorf("%w: %s", ErrItemAttachmentInvalid, err.Error())
	}
	detectedMimeType, err := verifyAttachmentFileContent(in.FileData, in.OriginalFilename)
	if err != nil {
		return fmt.Errorf("%w: File content validation failed: %s", ErrItemAttachmentInvalid, err.Error())
	}
	settings, err := loadAttachmentSettings(s.db, s.attachmentPath)
	if err != nil {
		return fmt.Errorf("get attachment settings: %w", err)
	}
	if !settings.Enabled {
		return ErrItemAttachmentDisabled
	}
	if int64(len(in.FileData)) > settings.MaxFileSize {
		return fmt.Errorf("%w: File too large. Maximum size: %d bytes", ErrItemAttachmentInvalid, settings.MaxFileSize)
	}
	if err := validateAllowedMimeType(settings.AllowedMimeTypes, detectedMimeType); err != nil {
		return fmt.Errorf("%w: %s", ErrItemAttachmentInvalid, err.Error())
	}
	return nil
}

func (s *ItemAttachmentService) uploadItemAttachment(in ItemAttachmentUploadInput, authorize bool) (models.AttachmentUploadResponse, error) {
	if in.ItemID <= 0 {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: item_id is required", ErrItemAttachmentInvalid)
	}
	if authorize {
		if err := s.authorizeItemEdit(in.UploaderID, in.ItemID); err != nil {
			return models.AttachmentUploadResponse{}, err
		}
	} else {
		exists, err := repository.NewItemRepository(s.db).Exists(in.ItemID)
		if err != nil {
			return models.AttachmentUploadResponse{}, fmt.Errorf("check public form item: %w", err)
		}
		if !exists {
			return models.AttachmentUploadResponse{}, ErrItemAttachmentNotFound
		}
	}
	if err := s.validateUpload(in); err != nil {
		return models.AttachmentUploadResponse{}, err
	}
	detectedMimeType, err := verifyAttachmentFileContent(in.FileData, in.OriginalFilename)
	if err != nil {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: File content validation failed: %s", ErrItemAttachmentInvalid, err.Error())
	}

	uniqueFilename, err := generateAttachmentFilename(in.OriginalFilename)
	if err != nil {
		return models.AttachmentUploadResponse{}, fmt.Errorf("generate filename: %w", err)
	}

	// Match the cookie-auth path layout (attachments/items/{itemID}/...) so
	// downloads/thumbnails served by the existing v1 AttachmentHandler resolve
	// the same files regardless of which surface wrote them.
	itemDir := filepath.Join(s.attachmentPath, "items", strconv.Itoa(in.ItemID))
	if err := os.MkdirAll(itemDir, 0o750); err != nil { //nolint:gosec // path built from configured root + numeric item id
		return models.AttachmentUploadResponse{}, fmt.Errorf("create attachment directory: %w", err)
	}
	filePath := filepath.Join(itemDir, uniqueFilename)
	if err := os.WriteFile(filePath, in.FileData, 0o600); err != nil { //nolint:gosec // path built from configured root + generated filename
		return models.AttachmentUploadResponse{}, fmt.Errorf("save file: %w", err)
	}

	hasThumbnail := false
	thumbnailPath := ""
	if strings.HasPrefix(detectedMimeType, "image/") {
		if path, err := generateAttachmentThumbnail(filePath, uniqueFilename); err == nil {
			hasThumbnail = true
			thumbnailPath = path
		} else {
			slog.Warn("failed to generate item attachment thumbnail", slog.String("component", "attachments"), slog.Any("error", err))
		}
	}

	var uploaderID *int
	if in.UploaderID > 0 {
		uploaderID = &in.UploaderID
	}
	attachmentID, err := s.attachmentService.CreateRecord(CreateAttachmentParams{
		ItemID:           in.ItemID,
		EntityType:       "item",
		Filename:         uniqueFilename,
		OriginalFilename: in.OriginalFilename,
		FilePath:         filePath,
		MimeType:         detectedMimeType,
		FileSize:         int64(len(in.FileData)),
		UploadedBy:       uploaderID,
		HasThumbnail:     hasThumbnail,
		ThumbnailPath:    thumbnailPath,
		Category:         "",
	})
	if err != nil {
		_ = os.Remove(filePath) //nolint:gosec // cleanup of path built above
		if thumbnailPath != "" {
			_ = os.Remove(thumbnailPath) //nolint:gosec // cleanup of path derived from filePath
		}
		return models.AttachmentUploadResponse{}, fmt.Errorf("save attachment record: %w", err)
	}

	// Best-effort history row, mirroring the cookie-auth handler. A failure
	// here must not fail an otherwise-successful upload.
	if histErr := s.attachmentService.RecordItemHistory(in.ItemID, uploaderID, "attachment_uploaded", nil, attachmentID, in.OriginalFilename); histErr != nil {
		slog.Warn("failed to record attachment upload history", slog.String("component", "attachments"), slog.Any("error", histErr))
	}

	itemID := in.ItemID
	return models.AttachmentUploadResponse{
		Success: true,
		Message: "File uploaded successfully",
		Attachment: models.Attachment{
			ID:               int(attachmentID),
			ItemID:           &itemID,
			Filename:         uniqueFilename,
			OriginalFilename: in.OriginalFilename,
			MimeType:         detectedMimeType,
			FileSize:         int64(len(in.FileData)),
			UploadedBy:       uploaderID,
			CreatedAt:        time.Now(),
		},
	}, nil
}

// RollbackPublicFormItem removes attachment blobs and the just-created item
// after any post-create upload failure. The item delete cascades attachment
// rows; blob paths are removed explicitly because the database cannot own the
// filesystem transaction.
func (s *ItemAttachmentService) RollbackPublicFormItem(itemID int) error {
	rows, err := s.db.Query(`
		SELECT file_path, COALESCE(thumbnail_path, '')
		FROM attachments
		WHERE item_id = ? AND COALESCE(entity_type, 'item') = 'item'
	`, itemID)
	if err != nil {
		return fmt.Errorf("list public form attachment rollback paths: %w", err)
	}
	var paths []string
	for rows.Next() {
		var filePath, thumbnailPath string
		if err := rows.Scan(&filePath, &thumbnailPath); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan public form attachment rollback path: %w", err)
		}
		paths = append(paths, filePath)
		if thumbnailPath != "" {
			paths = append(paths, thumbnailPath)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate public form attachment rollback paths: %w", err)
	}
	_ = rows.Close()

	itemRepo := repository.NewItemRepository(s.db)
	workspaceID, _ := itemRepo.GetWorkspaceID(itemID)
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin public form item rollback: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := itemRepo.Delete(tx, itemID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit public form item rollback: %w", err)
	}
	repository.InvalidateItemListCountCache(s.db, workspaceID)
	for _, path := range paths {
		if err := fileserve.RemoveUnderRoot(s.attachmentPath, path); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("failed to remove rolled-back public form attachment", slog.String("component", "attachments"), slog.String("file_path", path), slog.Any("error", err))
		}
	}
	return nil
}

// DeleteItemAttachment deletes an item-scoped attachment record and removes
// its blob from disk. Non-item attachments are deliberately refused — they
// are owned by other entity lifecycles (pages, avatars, branding) and must
// not be removable through the items-token surface. A failure to remove the
// on-disk file is logged but does not undo the DB delete; the record is
// already gone and an orphaned file is cleaned up by routine storage sweeps.
func (s *ItemAttachmentService) DeleteItemAttachment(attachmentID, deleterID int) error {
	details, err := s.attachmentService.GetAttachmentDetails(attachmentID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrItemAttachmentNotFound
		}
		return fmt.Errorf("get attachment details: %w", err)
	}

	// Only item-scoped rows are reachable from this surface. Any other
	// entity_type (page, test_case, avatar, branding, …) is reported as
	// not found so existence of those rows is never leaked to an
	// items-scoped token.
	if (details.EntityType != "item" && details.EntityType != "") || details.ItemID == nil {
		return ErrItemAttachmentNotFound
	}

	itemID := *details.ItemID
	if err := s.authorizeItemEdit(deleterID, itemID); err != nil {
		return err
	}

	// Record the deletion in item history before removing the row so the
	// original filename survives for the `old_value` snapshot.
	if histErr := s.attachmentService.RecordItemHistory(itemID, &deleterID, "attachment_deleted", &details.OriginalFilename, 0, details.OriginalFilename); histErr != nil {
		slog.Warn("failed to record attachment deletion history", slog.String("component", "attachments"), slog.Any("error", histErr))
	}

	rowsAffected, err := s.attachmentService.DeleteRecord(attachmentID)
	if err != nil {
		return fmt.Errorf("delete attachment record: %w", err)
	}
	if rowsAffected == 0 {
		return ErrItemAttachmentNotFound
	}

	// Best-effort blob removal, confined to the attachment root by the same
	// fileserve helper the download/thumbnail handlers use. A path that
	// resolves outside the root (malicious row or planted symlink) is refused
	// rather than followed. The cookie-auth delete also removes only the main
	// file (not the thumbnail), so this matches that surface exactly; orphaned
	// thumbnails are harmless and cleaned up by routine storage sweeps.
	if err := fileserve.RemoveUnderRoot(s.attachmentPath, details.FilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("failed to delete attachment file", slog.String("component", "attachments"), slog.String("file_path", details.FilePath), slog.Any("error", err))
	}
	return nil
}

// authorizeItemEdit resolves the item and checks the caller holds item.edit in
// its workspace, reusing the shared AttachmentService check (which also handles
// portal customers). A missing item or insufficient permission collapses to
// ErrItemAttachmentNotFound so existence isn't leaked.
func (s *ItemAttachmentService) authorizeItemEdit(userID, itemID int) error {
	can, err := s.attachmentService.CanModifyItemAttachment(&userID, nil, itemID)
	if err != nil {
		return fmt.Errorf("check item edit permission: %w", err)
	}
	if !can {
		return ErrItemAttachmentNotFound
	}
	return nil
}
