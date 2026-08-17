package services

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // Register GIF decoder for thumbnails.
	"image/jpeg"
	_ "image/png" // Register PNG decoder for thumbnails.
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/fileserve"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/utils"

	"golang.org/x/image/draw"
)

// maxAttachmentThumbnailSourcePixels bounds the declared dimensions an
// attachment may carry before the thumbnail decoder allocates for it.
const maxAttachmentThumbnailSourcePixels = 25_000_000

var (
	ErrPageAttachmentUploadDisabled = errors.New("page attachment upload disabled")
	ErrPageAttachmentUploadInvalid  = errors.New("page attachment upload invalid")
	ErrPageAttachmentUploadNotFound = errors.New("page attachment upload target not found")
)

// PageAttachmentUploadService owns page-attachment upload validation, storage,
// thumbnail generation, DB insert, and page-edit authorization. Both cookie and
// bearer HTTP adapters can call this use case without sharing handler code.
type PageAttachmentUploadService struct {
	db                    database.Database
	attachmentPath        string
	permissionService     *PermissionService
	pagePermissionService *PagePermissionService
	attachmentService     *AttachmentService
}

// NewPageAttachmentUploadService creates a page attachment upload service.
func NewPageAttachmentUploadService(db database.Database, attachmentPath string, permissionService *PermissionService, pagePermissionService *PagePermissionService) *PageAttachmentUploadService {
	return &PageAttachmentUploadService{
		db:                    db,
		attachmentPath:        attachmentPath,
		permissionService:     permissionService,
		pagePermissionService: pagePermissionService,
		attachmentService:     NewAttachmentService(db),
	}
}

// PageAttachmentUploadInput contains the validated HTTP upload payload.
type PageAttachmentUploadInput struct {
	PageID           int
	UploaderID       int
	OriginalFilename string
	FileData         []byte
	FileSize         int64
}

// UploadPageAttachment stores a new attachment for a page and returns the same
// response model used by the legacy upload endpoint for regular attachments.
func (s *PageAttachmentUploadService) UploadPageAttachment(in PageAttachmentUploadInput) (models.AttachmentUploadResponse, error) {
	if s.attachmentPath == "" {
		return models.AttachmentUploadResponse{}, ErrPageAttachmentUploadDisabled
	}
	if in.PageID <= 0 {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: page_id is required", ErrPageAttachmentUploadInvalid)
	}
	if strings.TrimSpace(in.OriginalFilename) == "" {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: filename is required", ErrPageAttachmentUploadInvalid)
	}
	if len(in.FileData) == 0 {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: file is empty", ErrPageAttachmentUploadInvalid)
	}

	if err := s.authorizePageEdit(in.UploaderID, in.PageID); err != nil {
		return models.AttachmentUploadResponse{}, err
	}

	if err := validateAttachmentFileExtension(in.OriginalFilename); err != nil {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: %s", ErrPageAttachmentUploadInvalid, err.Error())
	}
	detectedMimeType, err := verifyAttachmentFileContent(in.FileData, in.OriginalFilename)
	if err != nil {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: File content validation failed: %s", ErrPageAttachmentUploadInvalid, err.Error())
	}

	settings, err := loadAttachmentSettings(s.db, s.attachmentPath)
	if err != nil {
		return models.AttachmentUploadResponse{}, fmt.Errorf("get attachment settings: %w", err)
	}
	if !settings.Enabled {
		return models.AttachmentUploadResponse{}, ErrPageAttachmentUploadDisabled
	}
	if in.FileSize > settings.MaxFileSize {
		return models.AttachmentUploadResponse{}, fmt.Errorf("%w: File too large. Maximum size: %d bytes", ErrPageAttachmentUploadInvalid, settings.MaxFileSize)
	}
	if err := validateAllowedMimeType(settings.AllowedMimeTypes, detectedMimeType); err != nil {
		return models.AttachmentUploadResponse{}, err
	}

	uniqueFilename, err := generateAttachmentFilename(in.OriginalFilename)
	if err != nil {
		return models.AttachmentUploadResponse{}, fmt.Errorf("generate filename: %w", err)
	}

	pageDir := filepath.Join(s.attachmentPath, "pages", fmt.Sprintf("%d", in.PageID))
	if err := os.MkdirAll(pageDir, 0o750); err != nil { //nolint:gosec // path built from configured root + numeric page id
		return models.AttachmentUploadResponse{}, fmt.Errorf("create attachment directory: %w", err)
	}
	filePath := filepath.Join(pageDir, uniqueFilename)
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
			slog.Warn("failed to generate page attachment thumbnail", slog.String("component", "attachments"), slog.Any("error", err))
		}
	}

	uploaderID := in.UploaderID
	attachmentID, err := s.attachmentService.CreateRecord(CreateAttachmentParams{
		ItemID:           in.PageID,
		EntityType:       "page",
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

	pageID := in.PageID
	return models.AttachmentUploadResponse{
		Success: true,
		Message: "File uploaded successfully",
		Attachment: models.Attachment{
			ID:               int(attachmentID),
			ItemID:           &pageID,
			Filename:         uniqueFilename,
			OriginalFilename: in.OriginalFilename,
			MimeType:         detectedMimeType,
			FileSize:         int64(len(in.FileData)),
			UploadedBy:       &uploaderID,
			CreatedAt:        time.Now(),
		},
	}, nil
}

// DeleteUploadedPageAttachment compensates a failed higher-level mutation.
// It is intentionally not exposed as a user-facing delete operation: live and
// historical Page revisions may still reference successful diagram uploads.
func (s *PageAttachmentUploadService) DeleteUploadedPageAttachment(pageID, attachmentID int) error {
	rec, err := repository.NewAttachmentRepository(s.db).GetPageAttachmentRecord(pageID, attachmentID)
	if err != nil {
		return err
	}
	if err := fileserve.RemoveUnderRoot(s.attachmentPath, rec.FilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove page attachment file: %w", err)
	}
	rows, err := s.attachmentService.DeleteRecord(attachmentID)
	if err != nil {
		return err
	}
	if rows != 1 {
		return repository.ErrNotFound
	}
	return nil
}

func (s *PageAttachmentUploadService) authorizePageEdit(userID, pageID int) error {
	var wsID int
	err := s.db.QueryRow(`SELECT workspace_id FROM pages WHERE id = ?`, pageID).Scan(&wsID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPageAttachmentUploadNotFound
	}
	if err != nil {
		return fmt.Errorf("resolve page workspace: %w", err)
	}
	if s.pagePermissionService != nil {
		can, err := s.pagePermissionService.Can(userID, wsID, pageID, PageOpEdit)
		if err != nil {
			return fmt.Errorf("check page permission: %w", err)
		}
		if !can {
			return ErrPageAttachmentUploadNotFound
		}
		return nil
	}
	if s.permissionService == nil {
		return ErrPageAttachmentUploadNotFound
	}
	allowed, err := s.permissionService.HasWorkspacePermission(userID, wsID, models.PermissionPageEdit)
	if err != nil {
		return fmt.Errorf("check workspace permission: %w", err)
	}
	if !allowed {
		return ErrPageAttachmentUploadNotFound
	}
	return nil
}

// loadAttachmentSettings loads the system-wide attachment settings, falling
// back to permissive defaults when no row exists. Shared by the page and item
// attachment upload services so both surfaces read storage limits identically.
func loadAttachmentSettings(db database.Database, attachmentPath string) (*models.AttachmentSettings, error) {
	settings := &models.AttachmentSettings{
		MaxFileSize:      52428800, // 50MB default
		AllowedMimeTypes: "",
		AttachmentPath:   attachmentPath,
		Enabled:          true,
	}
	err := db.QueryRow(`
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

func validateAllowedMimeType(allowedMimeTypes, detectedMimeType string) error {
	if allowedMimeTypes == "" {
		return nil
	}
	// Preserve the legacy upload behavior: malformed settings are treated as
	// no MIME restriction rather than blocking all uploads.
	if !json.Valid([]byte(allowedMimeTypes)) {
		return nil
	}
	var allowedTypes []string
	_ = json.Unmarshal([]byte(allowedMimeTypes), &allowedTypes)
	if len(allowedTypes) == 0 {
		return nil
	}
	for _, allowedType := range allowedTypes {
		if strings.HasPrefix(detectedMimeType, allowedType) {
			return nil
		}
	}
	return fmt.Errorf("%w: File type %s not allowed by server configuration", ErrPageAttachmentUploadInvalid, detectedMimeType)
}

func verifyAttachmentFileContent(fileData []byte, filename string) (string, error) {
	detectSize := 512
	if len(fileData) < detectSize {
		detectSize = len(fileData)
	}
	detectedType := http.DetectContentType(fileData[:detectSize])
	ext := filepath.Ext(filename)
	expectedType := mime.TypeByExtension(ext)
	if expectedType != "" {
		detectedBase := strings.Split(detectedType, ";")[0]
		expectedBase := strings.Split(expectedType, ";")[0]
		if detectedBase != expectedBase && detectedBase != "application/octet-stream" &&
			(detectedBase != "text/plain" || !strings.HasPrefix(expectedBase, "text/")) {
			if detectedBase == "application/zip" && isZipBasedAttachmentMimeType(expectedBase) {
				return expectedType, nil
			}
			if detectedBase == "text/plain" && expectedBase == "application/json" && looksLikeAttachmentJSON(fileData) {
				return expectedType, nil
			}
			return "", fmt.Errorf("file content type (%s) doesn't match extension %s (expected %s)", detectedBase, ext, expectedBase)
		}
	}
	return detectedType, nil
}

func isZipBasedAttachmentMimeType(mimeType string) bool {
	return mimeType == "application/zip" ||
		strings.Contains(mimeType, "openxmlformats") ||
		strings.Contains(mimeType, "opendocument") ||
		mimeType == "application/epub+zip"
}

func looksLikeAttachmentJSON(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return false
	}
	return json.Valid(trimmed)
}

func validateAttachmentFileExtension(filename string) error {
	dangerousExtensions := []string{
		".exe", ".bat", ".cmd", ".com", ".pif", ".scr", ".msi",
		".js", ".jsx", ".ts", ".tsx",
		".html", ".htm", ".svg",
		".sh", ".bash", ".zsh", ".fish",
		".py", ".rb", ".pl", ".php", ".asp", ".aspx", ".jsp",
		".jar", ".class", ".dex",
		".app", ".dmg", ".pkg",
		".deb", ".rpm",
		".apk", ".ipa",
	}
	ext := strings.ToLower(filepath.Ext(filename))
	for _, dangerous := range dangerousExtensions {
		if ext == dangerous {
			return fmt.Errorf("file extension %s is not allowed for security reasons", ext)
		}
	}
	if ext == "" || ext == "." {
		return fmt.Errorf("files without extensions are not allowed")
	}
	return nil
}

func generateAttachmentFilename(originalFilename string) (string, error) {
	ext := filepath.Ext(originalFilename)
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x%s", randomBytes, ext), nil
}

func generateAttachmentThumbnail(originalPath, filename string) (string, error) {
	file, err := os.Open(originalPath) //nolint:gosec // path from server-managed attachment storage
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	// Reject pixel bombs before the full decode allocates.
	if err := utils.EnsureImageDimensionsBounded(file, maxAttachmentThumbnailSourcePixels); err != nil {
		return "", err
	}
	img, _, err := image.Decode(file)
	if err != nil {
		return "", err
	}
	bounds := img.Bounds()
	origWidth := bounds.Dx()
	origHeight := bounds.Dy()
	maxSize := 200
	var newWidth, newHeight int
	if origWidth > origHeight {
		newWidth = maxSize
		newHeight = (origHeight * maxSize) / origWidth
	} else {
		newHeight = maxSize
		newWidth = (origWidth * maxSize) / origHeight
	}
	thumbnail := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	draw.CatmullRom.Scale(thumbnail, thumbnail.Bounds(), img, bounds, draw.Over, nil)

	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	thumbnailPath := filepath.Join(filepath.Dir(originalPath), base+".thumb.jpg")
	thumbnailFile, err := os.Create(thumbnailPath) //nolint:gosec // path derived from server-managed attachment storage
	if err != nil {
		return "", err
	}
	defer func() { _ = thumbnailFile.Close() }()
	if err := jpeg.Encode(thumbnailFile, thumbnail, &jpeg.Options{Quality: 85}); err != nil {
		return "", err
	}
	return thumbnailPath, nil
}
