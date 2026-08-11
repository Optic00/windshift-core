package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"windshift/internal/cql"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// validateResourceBelongsToSet checks that a resource (by table name) with resourceID belongs to setID.
// Returns true if valid; writes an error response and returns false otherwise.
func (h *AssetHandler) validateResourceBelongsToSet(w http.ResponseWriter, r *http.Request, table string, resourceID, setID int, resourceName string) bool {
	resSetID, err := h.repo.GetResourceSetID(table, resourceID)
	if errors.Is(err, repository.ErrNotFound) {
		respondValidationError(w, r, resourceName+" not found")
		return false
	}
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if resSetID != setID {
		respondValidationError(w, r, resourceName+" does not belong to this set")
		return false
	}
	return true
}

// serializeCustomFields normalizes user-type fields and marshals custom field values to JSON.
// Returns (serialized *string, ok bool). Writes error response on failure.
func (h *AssetHandler) serializeCustomFields(w http.ResponseWriter, r *http.Request, customFieldValues map[string]any, assetTypeID int) (*string, bool) {
	if customFieldValues == nil {
		return nil, true
	}
	if err := h.normalizeUserFieldValues(customFieldValues, assetTypeID); err != nil {
		respondInternalError(w, r, fmt.Errorf("failed to process custom field values: %w", err))
		return nil, false
	}
	b, err := json.Marshal(customFieldValues)
	if err != nil {
		respondValidationError(w, r, "Invalid custom field values")
		return nil, false
	}
	s := string(b)
	return &s, true
}

// GetAssets returns all assets in a set with pagination and subcategory support
func (h *AssetHandler) GetAssets(w http.ResponseWriter, r *http.Request) {
	user, setID, ok := h.requireSetViewAccess(w, r)
	if !ok {
		return
	}

	limit, offset := parseOffsetPagination(r, 25, 10000)

	filter := repository.AssetListFilter{
		SetID:                setID,
		AssetTypeID:          r.URL.Query().Get("type_id"),
		CategoryID:           r.URL.Query().Get("category_id"),
		IncludeSubcategories: r.URL.Query().Get("include_subcategories") != "false",
		StatusID:             r.URL.Query().Get("status_id"),
		Search:               r.URL.Query().Get("search"),
		Limit:                limit,
		Offset:               offset,
	}

	if cqlQuery := r.URL.Query().Get("ql"); cqlQuery != "" {
		setMap, err := h.repo.GetCQLSetMap()
		if err != nil {
			respondInternalError(w, r, fmt.Errorf("failed to load set mapping: %w", err))
			return
		}
		workspaceMap, err := repository.NewWorkspaceRepository(h.db).ListNameKeyToIDMap()
		if err != nil {
			respondInternalError(w, r, fmt.Errorf("failed to load workspace mapping: %w", err))
			return
		}
		customFieldMap, err := h.repo.GetCQLCustomFieldMap(setID)
		if err != nil {
			respondInternalError(w, r, fmt.Errorf("failed to load custom field mapping: %w", err))
			return
		}
		itemCustomFieldMap, err := repository.NewItemRepository(h.db).GetCQLCustomFieldMap()
		if err != nil {
			respondInternalError(w, r, fmt.Errorf("failed to load item custom field mapping: %w", err))
			return
		}

		evaluator := cql.NewAssetEvaluator(setMap, workspaceMap, customFieldMap, itemCustomFieldMap, h.db.GetDriverName())
		resolvedQuery := cql.SubstituteFunctions(cqlQuery, cql.UserContext(user.ID))
		cqlSQL, cqlArgs, err := evaluator.EvaluateToSQL(resolvedQuery)
		if err != nil {
			respondValidationError(w, r, "CQL query error: "+err.Error())
			return
		}
		filter.CQLSQL = cqlSQL
		filter.CQLArgs = cqlArgs

		slog.Debug("asset query CQL",
			slog.String("cql", cqlQuery),
			slog.String("sql", cqlSQL),
			slog.Any("args", cqlArgs))
	}

	total, err := h.repo.CountAssets(filter)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	rows, err := h.repo.ListAssets(filter)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	assets := make([]models.Asset, 0, len(rows))
	for _, row := range rows {
		asset := repository.AssetRowToModel(row)
		if err := h.enrichUserCustomFields(&asset); err != nil {
			continue
		}
		assets = append(assets, asset)
	}

	respondJSONOK(w, map[string]any{
		"assets": assets,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

const maxAssetSummaryIDs = 500

// GetAssetSummaries returns compact display data for the requested IDs. Rows
// from inaccessible sets (and missing assets) are silently omitted so callers
// cannot use the batch surface as an existence oracle.
func (h *AssetHandler) GetAssetSummaries(w http.ResponseWriter, r *http.Request) {
	user := utils.GetCurrentUser(r)
	if user == nil {
		respondUnauthorized(w, r)
		return
	}

	rawIDs := parseIDListParam(r.URL.Query().Get("ids"))
	seen := make(map[int]struct{}, len(rawIDs))
	ids := make([]int, 0, len(rawIDs))
	for _, id := range rawIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		respondValidationError(w, r, "ids must contain at least one asset ID")
		return
	}
	if len(ids) > maxAssetSummaryIDs {
		respondBadRequest(w, r, fmt.Sprintf("too many ids (max %d per request)", maxAssetSummaryIDs))
		return
	}

	summaries, err := h.repo.FindAssetSummariesByIDs(ids)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	setAccess := make(map[int]bool)
	filtered := make([]models.AssetSummary, 0, len(summaries))
	for _, summary := range summaries {
		allowed, checked := setAccess[summary.SetID]
		if !checked {
			allowed, err = h.canViewSet(user.ID, summary.SetID)
			if err != nil {
				respondInternalError(w, r, err)
				return
			}
			setAccess[summary.SetID] = allowed
		}
		if allowed {
			filtered = append(filtered, summary)
		}
	}
	respondJSONOK(w, filtered)
}

// loadFullAsset fetches a single asset with all joined/enriched fields, matching
// the shape returned by GetAsset. Shared by GET and PUT so clients see a
// consistent payload after create/update.
func (h *AssetHandler) loadFullAsset(assetID int) (models.Asset, error) {
	row, err := h.repo.FindAssetFullByID(assetID)
	if err != nil {
		return models.Asset{}, err
	}
	asset := repository.AssetRowToModel(*row)
	if err := h.enrichUserCustomFields(&asset); err != nil {
		slog.Debug("failed to enrich user custom fields", slog.Any("error", err))
	}
	if err := h.enrichAssetRefCustomFields(&asset); err != nil {
		slog.Debug("failed to enrich asset-ref custom fields", slog.Any("error", err))
	}
	return asset, nil
}

func (h *AssetHandler) GetAsset(w http.ResponseWriter, r *http.Request) {
	_, assetID, ok := h.requireAssetViewAccess(w, r)
	if !ok {
		return
	}

	asset, err := h.loadFullAsset(assetID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "asset")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, asset)
}

// CreateAssetRequest represents the request body for creating an asset
type CreateAssetRequest struct {
	AssetTypeID       int            `json:"asset_type_id"`
	CategoryID        *int           `json:"category_id,omitempty"`
	StatusID          *int           `json:"status_id,omitempty"`
	Title             string         `json:"title"`
	Description       string         `json:"description"`
	AssetTag          string         `json:"asset_tag,omitempty"`
	CustomFieldValues map[string]any `json:"custom_field_values,omitempty"`
}

// CreateAsset creates a new asset
func (h *AssetHandler) CreateAsset(w http.ResponseWriter, r *http.Request) {
	currentUser, setID, ok := h.requireSetCreateAccess(w, r)
	if !ok {
		return
	}

	req, ok := decodeJSON[CreateAssetRequest](w, r)
	if !ok {
		return
	}

	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		respondValidationError(w, r, "Title is required")
		return
	}
	if req.AssetTypeID == 0 {
		respondValidationError(w, r, "Asset type is required")
		return
	}

	if !h.validateResourceBelongsToSet(w, r, "asset_types", req.AssetTypeID, setID, "Asset type") {
		return
	}

	// Input sanitization (XSS) happens in AssetService so the v1 (bearer)
	// and cookie-auth surfaces share one policy; see sanitizeAssetText
	// in internal/services/asset_service.go.

	if req.CategoryID != nil {
		if !h.validateResourceBelongsToSet(w, r, "asset_categories", *req.CategoryID, setID, "Category") {
			return
		}
	}

	// Handle status_id - get default if not provided
	var statusID *int
	if req.StatusID != nil {
		if !h.validateResourceBelongsToSet(w, r, "asset_statuses", *req.StatusID, setID, "Status") {
			return
		}
		statusID = req.StatusID
	} else {
		statusID, _ = h.repo.GetDefaultStatus(setID)
	}

	customFieldValuesJSON, ok := h.serializeCustomFields(w, r, req.CustomFieldValues, req.AssetTypeID)
	if !ok {
		return
	}

	asset, err := h.assetService.CreateAsset(
		services.NewAuditActorFromRequest(r, currentUser, nil, "cookie"),
		repository.CreateAssetInput{
			SetID:                 setID,
			AssetTypeID:           req.AssetTypeID,
			CategoryID:            req.CategoryID,
			StatusID:              statusID,
			Title:                 req.Title,
			Description:           req.Description,
			AssetTag:              req.AssetTag,
			CustomFieldValuesJSON: customFieldValuesJSON,
			CreatedBy:             currentUser.ID,
			CreatedAt:             time.Now(),
		},
		req.CustomFieldValues,
	)
	if ve, ok := services.IsAssetValidationError(err); ok {
		respondValidationError(w, r, ve.Msg)
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONCreated(w, asset)
}

// UpdateAssetRequest represents the request body for updating an asset
type UpdateAssetRequest struct {
	AssetTypeID       int            `json:"asset_type_id"`
	CategoryID        *int           `json:"category_id,omitempty"`
	StatusID          *int           `json:"status_id,omitempty"`
	Title             string         `json:"title"`
	Description       string         `json:"description"`
	AssetTag          string         `json:"asset_tag,omitempty"`
	CustomFieldValues map[string]any `json:"custom_field_values,omitempty"`
}

// UpdateAsset updates an existing asset
func (h *AssetHandler) UpdateAsset(w http.ResponseWriter, r *http.Request) {
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	assetID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	snap, err := h.repo.GetAssetUpdateSnapshot(assetID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "asset")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	canEdit, err := h.canEditSet(currentUser.ID, snap.SetID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canEdit {
		respondNotFound(w, r, "asset")
		return
	}

	req, ok := decodeJSON[UpdateAssetRequest](w, r)
	if !ok {
		return
	}

	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		respondValidationError(w, r, "Title is required")
		return
	}
	if req.AssetTypeID <= 0 {
		respondValidationError(w, r, "asset_type_id is required")
		return
	}

	// Input sanitization (XSS) happens in AssetService so the v1 (bearer)
	// and cookie-auth surfaces share one policy; see sanitizeAssetText
	// in internal/services/asset_service.go.

	if !h.validateResourceBelongsToSet(w, r, "asset_types", req.AssetTypeID, snap.SetID, "Asset type") {
		return
	}
	if req.CategoryID != nil {
		if !h.validateResourceBelongsToSet(w, r, "asset_categories", *req.CategoryID, snap.SetID, "Category") {
			return
		}
	}
	if req.StatusID != nil {
		if !h.validateResourceBelongsToSet(w, r, "asset_statuses", *req.StatusID, snap.SetID, "Status") {
			return
		}
	}

	customFieldValuesJSON, ok := h.serializeCustomFields(w, r, req.CustomFieldValues, req.AssetTypeID)
	if !ok {
		return
	}

	asset, err := h.assetService.UpdateAsset(
		services.NewAuditActorFromRequest(r, currentUser, nil, "cookie"),
		assetID,
		*snap,
		repository.UpdateAssetInput{
			AssetTypeID:           req.AssetTypeID,
			CategoryID:            req.CategoryID,
			StatusID:              req.StatusID,
			Title:                 req.Title,
			Description:           req.Description,
			AssetTag:              req.AssetTag,
			CustomFieldValuesJSON: customFieldValuesJSON,
		},
		req.CustomFieldValues,
	)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "asset")
		return
	}
	if ve, ok := services.IsAssetValidationError(err); ok {
		respondValidationError(w, r, ve.Msg)
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, asset)
}

// DeleteAsset deletes an asset
func (h *AssetHandler) DeleteAsset(w http.ResponseWriter, r *http.Request) {
	currentUser, assetID, ok := h.requireAssetDeleteAccess(w, r)
	if !ok {
		return
	}
	err := h.assetService.DeleteAsset(services.NewAuditActorFromRequest(r, currentUser, nil, "cookie"), assetID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "asset")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
