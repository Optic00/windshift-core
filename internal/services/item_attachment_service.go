package services

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"windshift/internal/database"
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
// surfaces share the same validation helpers (extension/content/MIME) and
// the same AttachmentService record layer so storage semantics stay in one
// place.
type ItemAttachmentService struct {
	db                database.Database
	attachmentPath    string
	permissionService *PermissionService
	attachmentService *AttachmentService
	itemRepo          *repository.ItemRepository
}

// NewItemAttachmentService creates an item-attachment upload/delete service.
func NewItemAttachmentService(db database.Database, attachmentPath string, permissionService *PermissionService) *ItemAttachmentService {
	return &ItemAttachmentService{
		db:                db,
		attachmentPath:    attachmentPath,
		permissionService: permissionService,
		attachmentService: NewAttachmentService(db),
		itemRepo:          repository.NewItemRepository(db),
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

// UploadItemAttachment stores a new attachment for an item and returns the
// same response model the cookie-auth upload endpoint uses for regular
// attachments, so bearer and cookie callers see identical shapes.
func (s *ItemAttachmentService) UploadItemAttachment(in ItemAttachmentUploadInput) (models.AttachmentUploadResponse, error) {
	if s.attachmentPath == "" {
		return models.AttachmentUploadResponse{}, ErrItemAttachmentDisabled
	}
	if in.ItemID <= 0 {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: item_id is required", ErrItemAttachmentInvalid)
	}
	if strings.TrimSpace(in.OriginalFilename) == "" {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: filename is required", ErrItemAttachmentInvalid)
	}
	if len(in.FileData) == 0 {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: file is empty", ErrItemAttachmentInvalid)
	}

	if err := s.authorizeItemEdit(in.UploaderID, in.ItemID); err != nil {
		return models.AttachmentUploadResponse{}, err
	}

	if err := validateAttachmentFileExtension(in.OriginalFilename); err != nil {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: %s", ErrItemAttachmentInvalid, err.Error())
	}
	detectedMimeType, err := verifyAttachmentFileContent(in.FileData, in.OriginalFilename)
	if err != nil {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: File content validation failed: %s", ErrItemAttachmentInvalid, err.Error())
	}

	settings, err := s.getAttachmentSettings()
	if err != nil {
		return models.AttachmentUploadResponse{}, fmt.Errorf("get attachment settings: %w", err)
	}
	if !settings.Enabled {
		return models.AttachmentUploadResponse{}, ErrItemAttachmentDisabled
	}
	if in.FileSize > settings.MaxFileSize {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: File too large. Maximum size: %d bytes", ErrItemAttachmentInvalid, settings.MaxFileSize)
	}
	// validateAllowedMimeType wraps its error with the page-upload sentinel;
	// re-wrap under the item sentinel so the v1 handler maps it to a 400.
	if err := validateAllowedMimeType(settings.AllowedMimeTypes, detectedMimeType); err != nil {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: %s", ErrItemAttachmentInvalid, err.Error())
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

	uploaderID := in.UploaderID
	attachmentID, err := s.attachmentService.CreateRecord(CreateAttachmentParams{
		ItemID:           in.ItemID,
		EntityType:       "item",
		Filename:         uniqueFilename,
		OriginalFilename: in.OriginalFilename,
		FilePath:         filePath,
		MimeType:         detectedMimeType,
		FileSize:         int64(len(in.FileData)),
		UploadedBy:       &uploaderID,
		HasThumbnail:     hasThumbnail,
		ThumbnailPath:    thumbnailPath,
		Category:         "",
	})
	if err != nil {
		_ = os.Remove(filePath) //nolint:gosec // cleanup of path built above
		return models.AttachmentUploadResponse{}, fmt.Errorf("save attachment record: %w", err)
	}

	// Best-effort history row, mirroring the cookie-auth handler. A failure
	// here must not fail an otherwise-successful upload.
	if histErr := s.recordAttachmentHistory(in.ItemID, &uploaderID, "attachment_uploaded", nil, attachmentID, in.OriginalFilename); histErr != nil {
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
			UploadedBy:       &uploaderID,
			CreatedAt:        time.Now(),
		},
	}, nil
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
	if histErr := s.recordAttachmentHistory(itemID, &deleterID, "attachment_deleted", &details.OriginalFilename, 0, details.OriginalFilename); histErr != nil {
		slog.Warn("failed to record attachment deletion history", slog.String("component", "attachments"), slog.Any("error", histErr))
	}

	rowsAffected, err := s.attachmentService.DeleteRecord(attachmentID)
	if err != nil {
		return fmt.Errorf("delete attachment record: %w", err)
	}
	if rowsAffected == 0 {
		return ErrItemAttachmentNotFound
	}

	// Best-effort blob removal. The cookie-auth delete also removes only the
	// main file (not the thumbnail), so this matches that surface exactly;
	// orphaned thumbnails are harmless and cleaned up by routine storage
	// sweeps.
	s.removeItemAttachmentFile(details.FilePath)
	return nil
}

// removeItemAttachmentFile best-effort removes the stored blob, confined to
// the configured attachment root. Paths that resolve outside the root are
// refused rather than followed (defense against a malicious row or planted
// symlink). A failure to remove the file is logged but does not undo the
// DB delete — the record is already gone.
func (s *ItemAttachmentService) removeItemAttachmentFile(storedPath string) {
	if storedPath == "" {
		return
	}
	resolved, err := s.resolveStoredPath(storedPath)
	if err != nil {
		slog.Warn("refusing to delete attachment file outside storage root", slog.String("component", "attachments"), slog.String("file_path", storedPath), slog.Any("error", err))
		return
	}
	if err := os.Remove(resolved); err != nil && !os.IsNotExist(err) { //nolint:gosec // path validated against attachment root
		slog.Warn("failed to delete attachment file", slog.String("component", "attachments"), slog.String("file_path", resolved), slog.Any("error", err))
	}
}

// resolveStoredPath returns the absolute on-disk path for a stored value,
// rejecting anything that lands outside the configured attachment root. It
// accepts both absolute stored paths (legacy rows) and root-relative ones
// (e.g. "items/123/file.png"), mirroring fileserve.OpenUnderRoot's
// resolution order.
func (s *ItemAttachmentService) resolveStoredPath(storedPath string) (string, error) {
	if s.attachmentPath == "" {
		return "", errors.New("attachment storage root not configured")
	}
	absRoot, err := filepath.Abs(s.attachmentPath)
	if err != nil {
		return "", err
	}
	inside := func(candidate string) (string, error) {
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return "", err
		}
		if abs == absRoot || strings.HasPrefix(abs, absRoot+string(os.PathSeparator)) {
			return abs, nil
		}
		return "", errors.New("attachment path is outside configured storage root")
	}

	if filepath.IsAbs(storedPath) {
		return inside(storedPath)
	}
	// Try the stored path as-written first (relative to CWD — covers rows
	// written when the root was itself relative), then joined under the root.
	if abs, err := inside(storedPath); err == nil {
		return abs, nil
	}
	return inside(filepath.Join(s.attachmentPath, storedPath))
}

// authorizeItemEdit resolves the item and checks the caller holds
// item.edit in its workspace. A missing item or insufficient permission
// collapses to ErrItemAttachmentNotFound so existence isn't leaked.
func (s *ItemAttachmentService) authorizeItemEdit(userID, itemID int) error {
	item, err := s.itemRepo.FindByID(itemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrItemAttachmentNotFound
		}
		return fmt.Errorf("resolve item: %w", err)
	}
	if s.permissionService == nil {
		return ErrItemAttachmentNotFound
	}
	allowed, err := s.permissionService.HasWorkspacePermission(userID, item.WorkspaceID, models.PermissionItemEdit)
	if err != nil {
		return fmt.Errorf("check workspace permission: %w", err)
	}
	if !allowed {
		return ErrItemAttachmentNotFound
	}
	return nil
}

// getAttachmentSettings loads the system-wide attachment settings, falling
// back to permissive defaults when no row exists.
func (s *ItemAttachmentService) getAttachmentSettings() (*models.AttachmentSettings, error) {
	settings := &models.AttachmentSettings{
		MaxFileSize:      52428800, // 50MB default
		AllowedMimeTypes: "",
		AttachmentPath:   s.attachmentPath,
		Enabled:          true,
	}
	err := s.db.QueryRow(`
		SELECT max_file_size, allowed_mime_types, attachment_path, enabled
		FROM attachment_settings ORDER BY id DESC LIMIT 1
	`).Scan(&settings.MaxFileSize, &settings.AllowedMimeTypes, &settings.AttachmentPath, &settings.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return nil, err
	}
	return settings, nil
}

// recordAttachmentHistory appends an item_history row for an attachment
// lifecycle event. Mirrors the cookie-auth AttachmentHandler.recordAttachment
// History shape so the v1 and cookie surfaces emit identical history.
func (s *ItemAttachmentService) recordAttachmentHistory(itemID int, userID *int, action string, oldValue *string, attachmentID int64, filename string) error {
	if userID == nil {
		return nil
	}
	var value string
	if action == "attachment_uploaded" {
		value = fmt.Sprintf("attachment:%d:%s", attachmentID, filename)
	} else {
		value = filename
	}
	_, err := s.db.ExecWrite(
		`INSERT INTO item_history (item_id, user_id, field_name, old_value, new_value, changed_at)
		 VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		itemID, *userID, action, oldValue, value,
	)
	return err
}
