package handlers

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"uuid"

	securejoin "github.com/cyphar/filepath-securejoin"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/utils"
)

const importErrorCap = 100

// newCSVReaderSkippingBOM strips a leading UTF-8 BOM from spreadsheet exports.
func newCSVReaderSkippingBOM(r io.Reader, delim rune) *csv.Reader {
	br := bufio.NewReader(r)
	if b, err := br.Peek(3); err == nil && len(b) == 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		_, _ = br.Discard(3)
	}
	reader := csv.NewReader(br)
	reader.Comma = delim
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	return reader
}

// CSVUploadResponse is returned after uploading a CSV file for preview.
type CSVUploadResponse struct {
	UploadID      string     `json:"upload_id"`
	Headers       []string   `json:"headers"`
	PreviewRows   [][]string `json:"preview_rows"`
	TotalRows     int        `json:"total_rows"`
	Delimiter     string     `json:"delimiter"`
	HeaderWarning string     `json:"header_warning,omitempty"`
}

// StartAssetImportRequest is the request body for starting a CSV import job.
type StartAssetImportRequest struct {
	UploadID    string              `json:"upload_id"`
	AssetTypeID int                 `json:"asset_type_id"`
	Mappings    AssetImportMappings `json:"mappings"`
	CategoryMap map[string]int      `json:"category_map,omitempty"`
	StatusMap   map[string]int      `json:"status_map,omitempty"`
	HasHeader   bool                `json:"has_header"`
	Delimiter   string              `json:"delimiter,omitempty"`
}

// AssetImportMappings maps CSV columns to asset fields.
type AssetImportMappings struct {
	Title        int            `json:"title"`
	Description  int            `json:"description"`
	AssetTag     int            `json:"asset_tag"`
	CategoryID   int            `json:"category_id"`
	StatusID     int            `json:"status_id"`
	CustomFields map[string]int `json:"custom_fields,omitempty"`
}

// AssetImportProgress tracks import job progress.
type AssetImportProgress struct {
	Phase         string   `json:"phase"`
	TotalRows     int      `json:"total_rows"`
	ImportedCount int      `json:"imported_count"`
	FailedCount   int      `json:"failed_count"`
	Errors        []string `json:"errors,omitempty"`
}

// AssetImportJobResponse is the API response for job status.
type AssetImportJobResponse struct {
	JobID        string               `json:"job_id"`
	Status       string               `json:"status"`
	Phase        string               `json:"phase,omitempty"`
	Progress     *AssetImportProgress `json:"progress,omitempty"`
	ErrorMessage string               `json:"error_message,omitempty"`
	CreatedAt    *time.Time           `json:"created_at,omitempty"`
	StartedAt    *time.Time           `json:"started_at,omitempty"`
	CompletedAt  *time.Time           `json:"completed_at,omitempty"`
}

// UploadCSV handles POST /asset-sets/{setId}/import/upload
func (h *AssetHandler) UploadCSV(w http.ResponseWriter, r *http.Request) {
	_, _, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	if h.attachmentPath == "" {
		respondBadRequest(w, r, "File storage is not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)

	// #nosec G120 -- MaxBytesReader caps the body; this is only the in-memory threshold.
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		respondBadRequest(w, r, "Failed to parse form data")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		respondBadRequest(w, r, "No file provided")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".csv" && ext != ".tsv" {
		respondValidationError(w, r, "Only CSV and TSV files are accepted")
		return
	}

	hasHeader := r.FormValue("has_header") != "false"
	delimiterStr := r.FormValue("delimiter")

	// Discard client-supplied directory components.
	safeFilename := filepath.Base(header.Filename)

	uploadID := uuid.New().String()
	importsBase := filepath.Join(h.attachmentPath, "imports")
	importDir, err := securejoin.SecureJoin(importsBase, uploadID)
	if err != nil {
		respondBadRequest(w, r, "Invalid file path")
		return
	}

	if err := os.MkdirAll(importDir, 0o750); err != nil {
		respondInternalError(w, r, fmt.Errorf("failed to create import directory: %w", err))
		return
	}

	destPath, err := securejoin.SecureJoin(importDir, safeFilename)
	if err != nil {
		respondBadRequest(w, r, "Invalid file path")
		return
	}
	destFile, err := os.Create(destPath) //nolint:gosec // path sanitized by securejoin
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("failed to create temp file: %w", err))
		return
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, file); err != nil {
		respondInternalError(w, r, fmt.Errorf("failed to save file: %w", err))
		return
	}

	delimiter := ','
	if delimiterStr != "" {
		switch delimiterStr {
		case "tab", "\t":
			delimiter = '\t'
		case "semicolon", ";":
			delimiter = ';'
		case "pipe", "|":
			delimiter = '|'
		default:
			if len(delimiterStr) == 1 {
				delimiter = rune(delimiterStr[0])
			}
		}
	} else {
		delimiter = h.detectDelimiter(destPath)
	}

	headers, previewRows, totalRows, err := h.parseCSVPreview(destPath, delimiter, hasHeader, 5)
	if err != nil {
		// Clean up on error
		_ = os.RemoveAll(importDir)
		respondBadRequest(w, r, fmt.Sprintf("Failed to parse CSV: %v", err))
		return
	}

	headerWarning := detectHeaderMismatch(headers, previewRows, hasHeader)

	delimiterDisplay := string(delimiter)
	if delimiter == '\t' {
		delimiterDisplay = "tab"
	}

	respondJSONOK(w, CSVUploadResponse{
		UploadID:      uploadID,
		Headers:       headers,
		PreviewRows:   previewRows,
		TotalRows:     totalRows,
		Delimiter:     delimiterDisplay,
		HeaderWarning: headerWarning,
	})
}

// StartImport handles POST /asset-sets/{setId}/import/start
func (h *AssetHandler) StartImport(w http.ResponseWriter, r *http.Request) {
	currentUser, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	req, ok := decodeJSON[StartAssetImportRequest](w, r)
	if !ok {
		return
	}

	if req.UploadID == "" {
		respondValidationError(w, r, "upload_id is required")
		return
	}
	if req.AssetTypeID == 0 {
		respondValidationError(w, r, "asset_type_id is required")
		return
	}

	typeSetID, err := h.repo.GetAssetTypeSetID(req.AssetTypeID)
	if errors.Is(err, repository.ErrNotFound) {
		respondValidationError(w, r, "Asset type not found")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if typeSetID != setID {
		respondValidationError(w, r, "Asset type does not belong to this set")
		return
	}

	// Reject foreign-set taxonomy IDs to prevent cross-set data disclosure.
	for name, id := range req.CategoryMap {
		if !h.validateResourceBelongsToSet(w, r, "asset_categories", id, setID, "Category "+name) {
			return
		}
	}
	for name, id := range req.StatusMap {
		if !h.validateResourceBelongsToSet(w, r, "asset_statuses", id, setID, "Status "+name) {
			return
		}
	}

	importsBase := filepath.Join(h.attachmentPath, "imports")
	importDir, err := securejoin.SecureJoin(importsBase, req.UploadID)
	if err != nil {
		respondBadRequest(w, r, "Invalid upload ID")
		return
	}

	entries, err := os.ReadDir(importDir)
	if err != nil {
		respondBadRequest(w, r, "Upload not found - please re-upload the file")
		return
	}
	if len(entries) == 0 {
		respondBadRequest(w, r, "Upload directory is empty")
		return
	}
	filePath := filepath.Join(importDir, entries[0].Name())

	jobID := uuid.New().String()
	configJSON, err := json.Marshal(req)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if err := h.repo.CreateImportJob(jobID, setID, filePath, string(configJSON), currentUser.ID, time.Now()); err != nil {
		respondInternalError(w, r, err)
		return
	}

	_ = logger.LogAudit(h.db, logger.AuditEvent{
		UserID:       currentUser.ID,
		Username:     currentUser.Username,
		IPAddress:    utils.GetClientIP(r),
		UserAgent:    r.UserAgent(),
		ActionType:   "asset_import",
		ResourceType: "asset_import",
		ResourceName: jobID,
		Details: map[string]any{
			"set_id":        setID,
			"asset_type_id": req.AssetTypeID,
		},
		Success: true,
	})

	go h.executeCSVImport(jobID, setID, req, filePath, currentUser.ID)

	respondJSONCreated(w, map[string]string{
		"job_id":  jobID,
		"message": "Import started successfully",
	})
}

// GetImportJob handles GET /asset-sets/{setId}/import/jobs/{jobId}
func (h *AssetHandler) GetImportJob(w http.ResponseWriter, r *http.Request) {
	_, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	jobID := r.PathValue("jobId")

	row, err := h.repo.GetImportJob(jobID, setID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "import job")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, importJobRowToResponse(jobID, row))
}

// GetImportJobs handles GET /asset-sets/{setId}/import/jobs
func (h *AssetHandler) GetImportJobs(w http.ResponseWriter, r *http.Request) {
	_, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	jobRows, err := h.repo.ListImportJobs(setID, 20)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	jobs := make([]AssetImportJobResponse, 0, len(jobRows))
	for _, row := range jobRows {
		jobs = append(jobs, importJobRowToResponse(row.JobID, &row))
	}

	respondJSONOK(w, jobs)
}

// importJobRowToResponse converts an import job row to its API response.
func importJobRowToResponse(jobID string, row *repository.ImportJobRow) AssetImportJobResponse {
	resp := AssetImportJobResponse{
		JobID:  jobID,
		Status: row.Status.String,
		Phase:  row.Phase.String,
	}
	if row.ProgressJSON.Valid && row.ProgressJSON.String != "" {
		var progress AssetImportProgress
		if err := json.Unmarshal([]byte(row.ProgressJSON.String), &progress); err == nil {
			resp.Progress = &progress
		}
	}
	if row.ErrorMessage.Valid {
		resp.ErrorMessage = row.ErrorMessage.String
	}
	if row.CreatedAt.Valid {
		t := row.CreatedAt.Time
		resp.CreatedAt = &t
	}
	if row.StartedAt.Valid {
		t := row.StartedAt.Time
		resp.StartedAt = &t
	}
	if row.CompletedAt.Valid {
		t := row.CompletedAt.Time
		resp.CompletedAt = &t
	}
	return resp
}

func (h *AssetHandler) executeCSVImport(jobID string, setID int, req StartAssetImportRequest, filePath string, userID int) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("asset import job panicked",
				slog.String("job_id", jobID),
				slog.Any("panic", rec))
			h.updateImportJobStatus(jobID, "failed", "", nil, fmt.Sprintf("Import crashed: %v", rec))
		}
	}()

	h.updateImportJobStatus(jobID, "running", "initializing", nil, "")

	f, err := os.Open(filePath) //nolint:gosec // filePath from trusted internal import job state
	if err != nil {
		h.updateImportJobStatus(jobID, "failed", "", nil, fmt.Sprintf("Failed to open CSV file: %v", err))
		return
	}
	defer f.Close()

	delimiter := ','
	if req.Delimiter != "" {
		switch req.Delimiter {
		case "tab", "\t":
			delimiter = '\t'
		case "semicolon", ";":
			delimiter = ';'
		case "pipe", "|":
			delimiter = '|'
		default:
			if len(req.Delimiter) == 1 {
				delimiter = rune(req.Delimiter[0])
			}
		}
	}

	reader := newCSVReaderSkippingBOM(f, delimiter)

	if req.HasHeader {
		if _, err := reader.Read(); err != nil {
			h.updateImportJobStatus(jobID, "failed", "", nil, "Failed to read CSV header")
			return
		}
	}

	defaultStatusID, _ := h.repo.GetDefaultStatus(setID)

	progress := &AssetImportProgress{
		Phase: "importing",
	}

	// Count and process together so total always equals imported plus failed.
	batchSize := 100
	rowNum := 0
	errorsTruncated := false

	appendErr := func(msg string) {
		if len(progress.Errors) < importErrorCap {
			progress.Errors = append(progress.Errors, msg)
		} else {
			errorsTruncated = true
		}
	}

	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		rowNum++
		progress.TotalRows = rowNum
		if readErr != nil {
			progress.FailedCount++
			appendErr(fmt.Sprintf("Row %d: %v", rowNum, readErr))
		} else if importErr := h.importCSVRow(record, setID, req, userID, defaultStatusID, jobID); importErr != nil {
			progress.FailedCount++
			appendErr(fmt.Sprintf("Row %d: %v", rowNum, importErr))
		} else {
			progress.ImportedCount++
		}

		if rowNum%batchSize == 0 {
			h.updateImportJobProgress(jobID, progress)
		}
	}

	if errorsTruncated {
		progress.Errors = append(progress.Errors,
			fmt.Sprintf("… additional errors omitted; only the first %d are shown.", importErrorCap))
	}

	progress.Phase = "completed"
	h.updateImportJobStatus(jobID, "completed", "completed", progress, "")

	importDir := filepath.Dir(filePath)
	if err := os.RemoveAll(importDir); err != nil { //nolint:gosec // filePath from trusted internal import job state
		slog.Error("Failed to clean up import temp files", "dir", importDir, "error", err)
	}
}

// ReconcileInterruptedImports rolls back and fails jobs orphaned by a restart.
func (h *AssetHandler) ReconcileInterruptedImports() (int, error) {
	jobIDs, err := h.repo.ListInterruptedImportJobIDs()
	if err != nil {
		return 0, err
	}
	if len(jobIDs) == 0 {
		return 0, nil
	}

	for _, id := range jobIDs {
		if err := h.repo.DeleteAssetsFromImportJob(id); err != nil {
			slog.Warn("failed to roll back partial import inserts", slog.String("job_id", id), slog.Any("error", err))
		}
	}

	return h.repo.MarkInterruptedImportsFailed(time.Now())
}

func (h *AssetHandler) importCSVRow(record []string, setID int, req StartAssetImportRequest, userID int, defaultStatusID *int, jobID string) error {
	getCol := func(idx int) string {
		if idx < 0 || idx >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[idx])
	}

	title := sanitize.PlainTextField.Sanitize(getCol(req.Mappings.Title))
	if title == "" {
		return fmt.Errorf("title is empty")
	}

	description := ""
	if req.Mappings.Description >= 0 {
		description = sanitize.RichText.Sanitize(getCol(req.Mappings.Description))
	}

	assetTag := ""
	if req.Mappings.AssetTag >= 0 {
		assetTag = sanitize.PlainTextField.Sanitize(getCol(req.Mappings.AssetTag))
	}

	var categoryID *int
	if req.Mappings.CategoryID >= 0 {
		catName := getCol(req.Mappings.CategoryID)
		if catName != "" && req.CategoryMap != nil {
			if id, ok := req.CategoryMap[catName]; ok {
				categoryID = &id
			}
		}
	}

	var statusID *int
	if req.Mappings.StatusID >= 0 {
		statusName := getCol(req.Mappings.StatusID)
		if statusName != "" && req.StatusMap != nil {
			if id, ok := req.StatusMap[statusName]; ok {
				statusID = &id
			}
		}
	}
	if statusID == nil {
		statusID = defaultStatusID
	}

	// Validate every row so required custom fields cannot be bypassed.
	cfValues := make(map[string]any)
	for fieldKey, colIdx := range req.Mappings.CustomFields {
		val := getCol(colIdx)
		if val != "" {
			sanitized := sanitize.PlainTextField.Sanitize(val)
			resolved := h.resolveImportFieldValue(fieldKey, sanitized)
			cfValues[fieldKey] = resolved
		}
	}
	coerced, err := h.assetService.CoerceAndValidateCustomFieldValues(req.AssetTypeID, cfValues)
	if err != nil {
		return err
	}

	var customFieldValuesJSON *string
	if len(coerced) > 0 {
		b, err := json.Marshal(coerced)
		if err != nil {
			return fmt.Errorf("encode custom field values: %w", err)
		}
		s := string(b)
		customFieldValuesJSON = &s
	}

	_, err = h.assetService.InsertImportedAsset(repository.ImportAssetRowInput{
		SetID:                 setID,
		AssetTypeID:           req.AssetTypeID,
		CategoryID:            categoryID,
		StatusID:              statusID,
		Title:                 title,
		Description:           description,
		AssetTag:              assetTag,
		CustomFieldValuesJSON: customFieldValuesJSON,
		ImportJobID:           jobID,
		CreatedBy:             userID,
		CreatedAt:             time.Now(),
	})
	return err
}

// resolveImportFieldValue maps select labels to option IDs.
func (h *AssetHandler) resolveImportFieldValue(fieldKey, textValue string) any {
	fieldID, err := strconv.Atoi(fieldKey)
	if err != nil {
		return textValue
	}

	fieldType, optionsJSON, err := h.repo.GetCustomFieldTypeAndOptions(fieldID)
	if err != nil || !optionsJSON.Valid {
		return textValue
	}

	if fieldType != "select" && fieldType != "multiselect" {
		return textValue
	}

	opts, parseErr := models.ParseSelectOptions(optionsJSON.String)
	if parseErr != nil {
		return textValue
	}

	labelToID := make(map[string]int, len(opts.Items))
	for _, item := range opts.Items {
		labelToID[item.Label] = item.ID
	}

	if fieldType == "select" {
		if optID, ok := labelToID[textValue]; ok {
			return optID
		}
		return textValue
	}

	parts := strings.Split(textValue, ",")
	var ids []int
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if optID, ok := labelToID[part]; ok {
			ids = append(ids, optID)
		}
	}
	if len(ids) > 0 {
		return ids
	}
	return textValue
}

func (h *AssetHandler) updateImportJobStatus(jobID, status, phase string, progress *AssetImportProgress, errorMessage string) {
	progressJSON := "{}"
	if progress != nil {
		if data, err := json.Marshal(progress); err == nil {
			progressJSON = string(data)
		}
	}

	var err error
	switch status {
	case "running":
		err = h.repo.StartImportJobRunning(jobID, phase, progressJSON)
	case "completed", "failed":
		err = h.repo.FinishImportJob(jobID, status, phase, progressJSON, errorMessage)
	default:
		err = h.repo.UpdateImportJobStatus(jobID, status, phase, progressJSON)
	}
	if err != nil {
		slog.Error("Failed to update import job status", "jobID", jobID, "error", err)
	}
}

func (h *AssetHandler) updateImportJobProgress(jobID string, progress *AssetImportProgress) {
	progressJSON := "{}"
	if progress != nil {
		if data, err := json.Marshal(progress); err == nil {
			progressJSON = string(data)
		}
	}
	if err := h.repo.UpdateImportJobProgress(jobID, progress.Phase, progressJSON); err != nil {
		slog.Error("Failed to update import job progress", "jobID", jobID, "error", err)
	}
}

// SuggestFieldsRequest is the request body for suggesting fields from CSV columns.
type SuggestFieldsRequest struct {
	UploadID  string `json:"upload_id"`
	HasHeader bool   `json:"has_header"`
	Delimiter string `json:"delimiter,omitempty"`
}

// SuggestedField represents a single suggested field from CSV analysis.
type SuggestedField struct {
	ColumnIndex   int      `json:"column_index"`
	HeaderName    string   `json:"header_name"`
	SuggestedName string   `json:"suggested_name"`
	SuggestedType string   `json:"suggested_type"`
	Options       []string `json:"options,omitempty"`
	SampleValues  []string `json:"sample_values"`
	IsStandard    bool     `json:"is_standard"`
}

// CreateTypeFromImportRequest is the request body for creating a type with fields during import.
type CreateTypeFromImportRequest struct {
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Icon        string                      `json:"icon"`
	Color       string                      `json:"color"`
	Fields      []CreateTypeFromImportField `json:"fields"`
}

// CreateTypeFromImportField represents a field to create/associate with the new type.
type CreateTypeFromImportField struct {
	Name         string   `json:"name"`
	FieldType    string   `json:"field_type"`
	Options      []string `json:"options,omitempty"`
	IsRequired   bool     `json:"is_required"`
	DisplayOrder int      `json:"display_order"`
}

// SuggestFieldsFromCSV handles POST /asset-sets/{setId}/import/suggest-fields
func (h *AssetHandler) SuggestFieldsFromCSV(w http.ResponseWriter, r *http.Request) {
	_, _, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	req, ok := decodeJSON[SuggestFieldsRequest](w, r)
	if !ok {
		return
	}

	if req.UploadID == "" {
		respondValidationError(w, r, "upload_id is required")
		return
	}

	importsBase := filepath.Join(h.attachmentPath, "imports")
	importDir, err := securejoin.SecureJoin(importsBase, req.UploadID)
	if err != nil {
		respondBadRequest(w, r, "Invalid upload ID")
		return
	}

	entries, err := os.ReadDir(importDir)
	if err != nil || len(entries) == 0 {
		respondBadRequest(w, r, "Upload not found - please re-upload the file")
		return
	}
	filePath := filepath.Join(importDir, entries[0].Name())

	delimiter := ','
	if req.Delimiter != "" {
		switch req.Delimiter {
		case "tab", "\t":
			delimiter = '\t'
		case "semicolon", ";":
			delimiter = ';'
		case "pipe", "|":
			delimiter = '|'
		default:
			if len(req.Delimiter) == 1 {
				delimiter = rune(req.Delimiter[0])
			}
		}
	}

	headers, previewRows, _, err := h.parseCSVPreview(filePath, delimiter, req.HasHeader, 20)
	if err != nil {
		respondBadRequest(w, r, fmt.Sprintf("Failed to parse CSV: %v", err))
		return
	}

	var suggestions []SuggestedField
	for colIdx, header := range headers {
		var samples []string
		seen := make(map[string]bool)
		for _, row := range previewRows {
			if colIdx < len(row) {
				val := strings.TrimSpace(row[colIdx])
				if val != "" && !seen[val] {
					seen[val] = true
					samples = append(samples, val)
				}
			}
		}

		isStd := isStandardField(header)
		suggestedType, options := inferFieldType(samples)
		suggestedName := cleanHeaderName(header)

		displaySamples := samples
		if len(displaySamples) > 5 {
			displaySamples = displaySamples[:5]
		}

		suggestions = append(suggestions, SuggestedField{
			ColumnIndex:   colIdx,
			HeaderName:    header,
			SuggestedName: suggestedName,
			SuggestedType: suggestedType,
			Options:       options,
			SampleValues:  displaySamples,
			IsStandard:    isStd,
		})
	}

	respondJSONOK(w, map[string]any{
		"suggested_fields": suggestions,
	})
}

// CreateTypeFromImport handles POST /asset-sets/{setId}/import/create-type
func (h *AssetHandler) CreateTypeFromImport(w http.ResponseWriter, r *http.Request) {
	currentUser, setID, ok := h.requireSetAdminAccess(w, r)
	if !ok {
		return
	}

	req, ok := decodeJSON[CreateTypeFromImportRequest](w, r)
	if !ok {
		return
	}

	if req.Name == "" {
		respondValidationError(w, r, "Name is required")
		return
	}

	if req.Icon == "" {
		req.Icon = "Box"
	}
	if req.Color == "" {
		req.Color = "#6b7280"
	}

	allowedTypes := map[string]bool{
		"text": true, "textarea": true, "number": true, "date": true, "select": true,
		models.CustomFieldTypeBoolean: true, models.CustomFieldTypeCheckbox: true,
	}
	for i := range req.Fields {
		f := &req.Fields[i]
		f.FieldType = models.CanonicalCustomFieldType(f.FieldType)
		if f.Name == "" {
			respondValidationError(w, r, "All fields must have a name")
			return
		}
		if !allowedTypes[f.FieldType] {
			respondValidationError(w, r, fmt.Sprintf("Invalid field type: %s", f.FieldType))
			return
		}
	}

	fieldInputs := make([]repository.ImportTypeFieldInput, 0, len(req.Fields))
	for _, f := range req.Fields {
		var optionsJSON *string
		if f.FieldType == "select" && len(f.Options) > 0 {
			if b, err := json.Marshal(f.Options); err == nil {
				s := string(b)
				optionsJSON = &s
			}
		}
		fieldInputs = append(fieldInputs, repository.ImportTypeFieldInput{
			Name:         f.Name,
			FieldType:    f.FieldType,
			OptionsJSON:  optionsJSON,
			IsRequired:   f.IsRequired,
			DisplayOrder: f.DisplayOrder,
		})
	}

	typeID, createdAt, results, err := h.repo.CreateAssetTypeWithFields(setID, models.AssetType{
		Name:        req.Name,
		Description: req.Description,
		Icon:        req.Icon,
		Color:       req.Color,
	}, fieldInputs)
	if errors.Is(err, repository.ErrDuplicateEntry) {
		respondValidationError(w, r, "An asset type with this name already exists")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	fields := make([]models.AssetTypeField, len(results))
	for i, res := range results {
		req := req.Fields[i]
		f := models.AssetTypeField{
			ID:            res.AssetTypeFieldID,
			AssetTypeID:   typeID,
			CustomFieldID: res.CustomFieldID,
			IsRequired:    req.IsRequired,
			DisplayOrder:  req.DisplayOrder,
			CreatedAt:     createdAt,
			FieldName:     req.Name,
			FieldType:     req.FieldType,
		}
		if fieldInputs[i].OptionsJSON != nil {
			f.Options = *fieldInputs[i].OptionsJSON
		}
		fields[i] = f
	}

	_ = logger.LogAudit(h.db, logger.AuditEvent{
		UserID:       currentUser.ID,
		Username:     currentUser.Username,
		IPAddress:    utils.GetClientIP(r),
		UserAgent:    r.UserAgent(),
		ActionType:   logger.ActionAssetTypeCreate,
		ResourceType: logger.ResourceAssetType,
		ResourceID:   &typeID,
		ResourceName: req.Name,
		Details: map[string]any{
			"source":      "import_wizard",
			"field_count": len(req.Fields),
		},
		Success: true,
	})

	assetType := models.AssetType{
		ID:          typeID,
		SetID:       setID,
		Name:        req.Name,
		Description: req.Description,
		Icon:        req.Icon,
		Color:       req.Color,
		IsActive:    true,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
		Fields:      fields,
	}

	respondJSONCreated(w, map[string]any{
		"asset_type": assetType,
		"fields":     fields,
	})
}

// inferFieldType analyzes sample values and returns a suggested field type and options.
func inferFieldType(values []string) (fieldType string, options []string) {
	if len(values) == 0 {
		return "text", nil
	}

	allNumeric := true
	allDate := true
	allBoolean := true
	hasLong := false
	uniqueValues := make(map[string]bool)

	dateRegex := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$|^\d{1,2}/\d{1,2}/\d{2,4}$|^\d{1,2}\.\d{1,2}\.\d{2,4}$`)
	numRegex := regexp.MustCompile(`^-?\d+([.,]\d+)?$`)

	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		uniqueValues[v] = true
		if !numRegex.MatchString(v) {
			allNumeric = false
		}
		if !dateRegex.MatchString(v) {
			allDate = false
		}
		if normalized := strings.ToLower(v); normalized != "true" && normalized != "false" {
			allBoolean = false
		}
		if len(v) > 200 {
			hasLong = true
		}
	}

	nonEmpty := len(uniqueValues)
	if nonEmpty == 0 {
		return "text", nil
	}

	if allBoolean {
		return models.CustomFieldTypeBoolean, nil
	}
	if allNumeric {
		return "number", nil
	}
	if allDate {
		return "date", nil
	}
	if nonEmpty <= 10 && len(values) >= 2 {
		opts := make([]string, 0, len(uniqueValues))
		for v := range uniqueValues {
			opts = append(opts, v)
		}
		return "select", opts
	}
	if hasLong {
		return "textarea", nil
	}
	return "text", nil
}

// isStandardField checks if a header name matches a standard asset field.
func isStandardField(header string) bool {
	h := strings.ToLower(strings.TrimSpace(header))
	standardFields := map[string]bool{
		"title": true, "name": true, "asset name": true, "asset_name": true,
		"description": true, "desc": true, "details": true,
		"tag": true, "asset tag": true, "asset_tag": true,
		"serial": true, "serial number": true, "serial_number": true,
		"category": true, "status": true, "state": true,
		"id": true, "asset id": true, "asset_id": true,
	}
	return standardFields[h]
}

// cleanHeaderName converts a raw CSV header into a display name.
func cleanHeaderName(header string) string {
	s := strings.TrimSpace(header)
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	words := strings.Fields(s)
	for i, w := range words {
		if w != "" {
			runes := []rune(w)
			runes[0] = unicode.ToUpper(runes[0])
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

func (h *AssetHandler) detectDelimiter(filePath string) rune {
	f, err := os.Open(filePath) //nolint:gosec // G304 — filePath sanitized via securejoin.SecureJoin
	if err != nil {
		return ','
	}
	defer f.Close()

	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return ','
	}
	sample := string(buf[:n])

	lines := strings.SplitN(sample, "\n", 5)
	if len(lines) == 0 {
		return ','
	}

	delimiters := []rune{',', '\t', ';', '|'}
	bestDelim := ','
	bestScore := 0

	for _, d := range delimiters {
		counts := make([]int, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			counts = append(counts, strings.Count(line, string(d)))
		}

		if len(counts) < 2 {
			continue
		}

		if counts[0] > 0 {
			consistent := true
			for i := 1; i < len(counts); i++ {
				if counts[i] != counts[0] {
					consistent = false
					break
				}
			}
			score := counts[0]
			if consistent {
				score *= 2
			}
			if score > bestScore {
				bestScore = score
				bestDelim = d
			}
		}
	}

	return bestDelim
}

func (h *AssetHandler) parseCSVPreview(filePath string, delimiter rune, hasHeader bool, maxPreviewRows int) (headers []string, rows [][]string, totalRows int, err error) {
	f, err := os.Open(filePath) //nolint:gosec // filePath from trusted internal import job state
	if err != nil {
		return nil, nil, 0, err
	}
	defer f.Close()

	reader := newCSVReaderSkippingBOM(f, delimiter)

	var previewRows [][]string

	if hasHeader {
		headers, err = reader.Read()
		if err != nil {
			return nil, nil, 0, fmt.Errorf("failed to read header row: %w", err)
		}
	}

	totalRows = 0
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			totalRows++
			continue
		}
		totalRows++

		if len(previewRows) < maxPreviewRows {
			previewRows = append(previewRows, record)
		}

		if !hasHeader && headers == nil {
			headers = make([]string, len(record))
			for i := range record {
				headers[i] = fmt.Sprintf("Column %d", i+1)
			}
		}
	}

	return headers, previewRows, totalRows, nil
}

var (
	numericPattern = regexp.MustCompile(`^\d+(\.\d+)?$`)
	datePattern    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$|^\d{1,2}/\d{1,2}/\d{2,4}$`)
	headerKeywords = map[string]bool{
		"name": true, "title": true, "description": true, "status": true,
		"category": true, "type": true, "tag": true, "serial": true,
		"id": true, "date": true, "notes": true, "location": true,
		"model": true, "brand": true, "manufacturer": true,
	}
)

// detectHeaderMismatch checks if the user's hasHeader setting likely doesn't match the CSV content.
func detectHeaderMismatch(headers []string, previewRows [][]string, hasHeader bool) string {
	if hasHeader {
		if len(headers) == 0 {
			return ""
		}
		dataLikeCount := 0
		for _, h := range headers {
			v := strings.TrimSpace(h)
			if numericPattern.MatchString(v) || datePattern.MatchString(v) {
				dataLikeCount++
			}
		}
		if float64(dataLikeCount)/float64(len(headers)) > 0.5 {
			return "The first row looks like it contains data, not column headers. You may want to uncheck 'First row contains column headers' and re-upload."
		}
		return ""
	}

	if len(previewRows) == 0 {
		return ""
	}
	firstRow := previewRows[0]
	if len(firstRow) == 0 {
		return ""
	}

	keywordMatches := 0
	shortNonNumeric := 0
	for _, val := range firstRow {
		v := strings.TrimSpace(val)
		if headerKeywords[strings.ToLower(v)] {
			keywordMatches++
		}
		if len(v) <= 30 && !numericPattern.MatchString(v) && v != "" {
			shortNonNumeric++
		}
	}

	if keywordMatches >= 2 {
		return "The first row looks like it contains column headers. You may want to check 'First row contains column headers' and re-upload."
	}

	if len(previewRows) >= 2 && float64(shortNonNumeric)/float64(len(firstRow)) > 0.5 {
		dataRowNumeric := 0
		dataRowTotal := 0
		for _, row := range previewRows[1:] {
			for _, val := range row {
				v := strings.TrimSpace(val)
				dataRowTotal++
				if numericPattern.MatchString(v) || datePattern.MatchString(v) || len(v) > 30 {
					dataRowNumeric++
				}
			}
		}
		if dataRowTotal > 0 && float64(dataRowNumeric)/float64(dataRowTotal) > 0.5 {
			return "The first row looks like it contains column headers. You may want to check 'First row contains column headers' and re-upload."
		}
	}

	return ""
}
