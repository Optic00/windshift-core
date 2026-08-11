package dto

import (
	"fmt"
	"time"

	"windshift/internal/models"
)

// AssetResponse is the public v1 representation of a single asset row.
// Fields mirror models.Asset minus internal-only columns; nested summaries
// keep type/category/status legible without forcing the client to fetch
// each separately.
type AssetResponse struct {
	ID          int       `json:"id"`
	SetID       int       `json:"set_id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	AssetTag    string    `json:"asset_tag,omitempty"`
	AssetTypeID int       `json:"asset_type_id"`
	CategoryID  *int      `json:"category_id,omitempty"`
	StatusID    *int      `json:"status_id,omitempty"`
	CreatedBy   *int      `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	CustomFieldValues map[string]any `json:"custom_field_values,omitempty"`

	// Nested summaries — populated from the row's joined columns when the
	// underlying query asks for them, otherwise omitted.
	Set       *AssetSetSummary      `json:"set,omitempty"`
	AssetType *AssetTypeSummary     `json:"asset_type,omitempty"`
	Category  *AssetCategorySummary `json:"category,omitempty"`
	Status    *AssetStatusSummary   `json:"status,omitempty"`
	Creator   *UserSummary          `json:"creator,omitempty"`

	// LinkedItemCount is the number of item↔asset links for this asset
	// (0 when the underlying query didn't ask for the rollup).
	LinkedItemCount int `json:"linked_item_count,omitempty"`

	// Warnings carries non-fatal anomalies surfaced by the query layer
	// (e.g. corrupt custom_field_values JSON). Always serialized as an
	// array (never null) so CLI consumers don't need to null-check.
	Warnings []string `json:"warnings"`

	Links *AssetLinks `json:"_links,omitempty"`
}

// AssetLinks groups HATEOAS endpoints for an asset.
type AssetLinks struct {
	Self string `json:"self"`
	Set  string `json:"set"`
}

// AssetCreateRequest is the v1 payload for creating an asset inside a set.
// The path-derived setID identifies the owning set; asset_type_id selects
// the schema. custom_field_values carries typed values keyed by custom
// field name — validation happens in the handler against the asset type's
// declared fields.
type AssetCreateRequest struct {
	Title             string         `json:"title"`
	Description       string         `json:"description,omitempty"`
	AssetTag          string         `json:"asset_tag,omitempty"`
	AssetTypeID       int            `json:"asset_type_id"`
	CategoryID        *int           `json:"category_id,omitempty"`
	StatusID          *int           `json:"status_id,omitempty"`
	CustomFieldValues map[string]any `json:"custom_field_values,omitempty"`
}

// AssetUpdateRequest is the v1 payload for partial-updating an asset.
// All fields are optional pointers so the handler can distinguish
// "not set" from "set to zero value". Pass a non-nil pointer to update;
// omit to leave unchanged.
type AssetUpdateRequest struct {
	Title             *string         `json:"title,omitempty"`
	Description       *string         `json:"description,omitempty"`
	AssetTag          *string         `json:"asset_tag,omitempty"`
	AssetTypeID       *int            `json:"asset_type_id,omitempty"`
	CategoryID        *int            `json:"category_id,omitempty"`
	StatusID          *int            `json:"status_id,omitempty"`
	CustomFieldValues *map[string]any `json:"custom_field_values,omitempty"`
}

// AssetSetResponse is the v1 representation of an asset management set.
type AssetSetResponse struct {
	ID             int            `json:"id"`
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	IsDefault      bool           `json:"is_default"`
	AssetTypeCount int            `json:"asset_type_count,omitempty"`
	AssetCount     int            `json:"asset_count,omitempty"`
	UserPermission string         `json:"user_permission,omitempty"`
	CreatedBy      *int           `json:"created_by,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	Links          *AssetSetLinks `json:"_links,omitempty"`
}

// AssetSetLinks groups HATEOAS endpoints for an asset set.
type AssetSetLinks struct {
	Self       string `json:"self"`
	Assets     string `json:"assets"`
	Types      string `json:"types"`
	Categories string `json:"categories"`
	Statuses   string `json:"statuses"`
}

// AssetSetSummary is the inline representation used inside AssetResponse.
type AssetSetSummary struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// AssetTypeResponse is the v1 representation of an asset type schema.
type AssetTypeResponse struct {
	ID           int                      `json:"id"`
	SetID        int                      `json:"set_id"`
	Name         string                   `json:"name"`
	Description  string                   `json:"description,omitempty"`
	Icon         string                   `json:"icon,omitempty"`
	Color        string                   `json:"color,omitempty"`
	DisplayOrder int                      `json:"display_order"`
	IsActive     bool                     `json:"is_active"`
	AssetCount   int                      `json:"asset_count,omitempty"`
	Fields       []AssetTypeFieldResponse `json:"fields"`
	CreatedAt    time.Time                `json:"created_at"`
	UpdatedAt    time.Time                `json:"updated_at"`
}

// AssetTypeFieldResponse describes a custom-field slot on an asset type.
type AssetTypeFieldResponse struct {
	ID               int    `json:"id"`
	CustomFieldID    int    `json:"custom_field_id"`
	FieldName        string `json:"field_name"`
	FieldType        string `json:"field_type"`
	FieldDescription string `json:"field_description,omitempty"`
	Options          string `json:"options,omitempty"`
	IsRequired       bool   `json:"is_required"`
	DisplayOrder     int    `json:"display_order"`
}

// AssetTypeSummary is the inline representation used inside AssetResponse.
type AssetTypeSummary struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Icon  string `json:"icon,omitempty"`
	Color string `json:"color,omitempty"`
}

// AssetCategoryResponse is the v1 representation of a category node.
type AssetCategoryResponse struct {
	ID               int       `json:"id"`
	SetID            int       `json:"set_id"`
	Name             string    `json:"name"`
	Description      string    `json:"description,omitempty"`
	ParentID         *int      `json:"parent_id,omitempty"`
	Path             string    `json:"path,omitempty"`
	HasChildren      bool      `json:"has_children"`
	ChildrenCount    int       `json:"children_count"`
	DescendantsCount int       `json:"descendants_count"`
	AssetCount       int       `json:"asset_count,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// AssetCategorySummary is the inline representation used inside AssetResponse.
type AssetCategorySummary struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

// AssetStatusResponse is the v1 representation of a status row.
type AssetStatusResponse struct {
	ID           int       `json:"id"`
	SetID        int       `json:"set_id"`
	Name         string    `json:"name"`
	Color        string    `json:"color,omitempty"`
	Description  string    `json:"description,omitempty"`
	IsDefault    bool      `json:"is_default"`
	DisplayOrder int       `json:"display_order"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AssetStatusSummary is the inline representation used inside AssetResponse.
type AssetStatusSummary struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}

// AssetImportJobResponse is the v1 representation of a CSV-import job.
type AssetImportJobResponse struct {
	ID            int        `json:"id"`
	SetID         int        `json:"set_id"`
	AssetTypeID   int        `json:"asset_type_id,omitempty"`
	Status        string     `json:"status"`
	TotalRows     int        `json:"total_rows"`
	ProcessedRows int        `json:"processed_rows"`
	CreatedRows   int        `json:"created_rows"`
	ErrorRows     int        `json:"error_rows"`
	ErrorMessage  string     `json:"error_message,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// MapAssetToResponse converts a models.Asset into the public v1 DTO.
// baseURL is the API host (e.g. https://app.example.com) used to render
// absolute HATEOAS links; passing "" omits the _links block.
func MapAssetToResponse(a *models.Asset, baseURL string) AssetResponse {
	resp := AssetResponse{
		ID:                a.ID,
		SetID:             a.SetID,
		Title:             a.Title,
		Description:       a.Description,
		AssetTag:          a.AssetTag,
		AssetTypeID:       a.AssetTypeID,
		CategoryID:        a.CategoryID,
		StatusID:          a.StatusID,
		CreatedBy:         a.CreatedBy,
		CreatedAt:         a.CreatedAt,
		UpdatedAt:         a.UpdatedAt,
		CustomFieldValues: a.CustomFieldValues,
		LinkedItemCount:   a.LinkedItemCount,
		Warnings:          a.Warnings,
	}
	if resp.Warnings == nil {
		resp.Warnings = []string{}
	}
	if a.SetName != "" {
		resp.Set = &AssetSetSummary{ID: a.SetID, Name: a.SetName}
	}
	if a.AssetTypeID != 0 && a.AssetTypeName != "" {
		resp.AssetType = &AssetTypeSummary{
			ID:    a.AssetTypeID,
			Name:  a.AssetTypeName,
			Icon:  a.AssetTypeIcon,
			Color: a.AssetTypeColor,
		}
	}
	if a.CategoryID != nil && a.CategoryName != "" {
		resp.Category = &AssetCategorySummary{
			ID:   *a.CategoryID,
			Name: a.CategoryName,
			Path: a.CategoryPath,
		}
	}
	if a.StatusID != nil && a.StatusName != "" {
		resp.Status = &AssetStatusSummary{
			ID:    *a.StatusID,
			Name:  a.StatusName,
			Color: a.StatusColor,
		}
	}
	// Creator email is deliberately omitted: assets:read alone shouldn't
	// expose user emails (that's what users:read gates). Callers that
	// need the email join a /users/{id} lookup using the creator id.
	// See docs/asset-api-v1-security-review-2026-06-03.md finding 2.
	if a.CreatedBy != nil {
		resp.Creator = &UserSummary{
			ID:       *a.CreatedBy,
			FullName: a.CreatorName,
		}
	}
	if baseURL != "" {
		resp.Links = &AssetLinks{
			Self: fmt.Sprintf("%s/rest/api/v1/assets/%d", baseURL, a.ID),
			Set:  fmt.Sprintf("%s/rest/api/v1/asset-sets/%d", baseURL, a.SetID),
		}
	}
	return resp
}

// MapAssetSetToResponse converts a models.AssetManagementSet into the v1 DTO.
func MapAssetSetToResponse(s *models.AssetManagementSet, baseURL string) AssetSetResponse {
	resp := AssetSetResponse{
		ID:             s.ID,
		Name:           s.Name,
		Description:    s.Description,
		IsDefault:      s.IsDefault,
		AssetTypeCount: s.AssetTypeCount,
		AssetCount:     s.AssetCount,
		UserPermission: s.UserPermission,
		CreatedBy:      s.CreatedBy,
		CreatedAt:      s.CreatedAt,
		UpdatedAt:      s.UpdatedAt,
	}
	if baseURL != "" {
		resp.Links = &AssetSetLinks{
			Self:       fmt.Sprintf("%s/rest/api/v1/asset-sets/%d", baseURL, s.ID),
			Assets:     fmt.Sprintf("%s/rest/api/v1/asset-sets/%d/assets", baseURL, s.ID),
			Types:      fmt.Sprintf("%s/rest/api/v1/asset-sets/%d/types", baseURL, s.ID),
			Categories: fmt.Sprintf("%s/rest/api/v1/asset-sets/%d/categories", baseURL, s.ID),
			Statuses:   fmt.Sprintf("%s/rest/api/v1/asset-sets/%d/statuses", baseURL, s.ID),
		}
	}
	return resp
}

// MapAssetTypeToResponse converts a models.AssetType into the v1 DTO.
func MapAssetTypeToResponse(t *models.AssetType) AssetTypeResponse {
	fields := make([]AssetTypeFieldResponse, 0, len(t.Fields))
	for _, f := range t.Fields {
		fields = append(fields, AssetTypeFieldResponse{
			ID:               f.ID,
			CustomFieldID:    f.CustomFieldID,
			FieldName:        f.FieldName,
			FieldType:        f.FieldType,
			FieldDescription: f.FieldDescription,
			Options:          f.Options,
			IsRequired:       f.IsRequired,
			DisplayOrder:     f.DisplayOrder,
		})
	}
	return AssetTypeResponse{
		ID:           t.ID,
		SetID:        t.SetID,
		Name:         t.Name,
		Description:  t.Description,
		Icon:         t.Icon,
		Color:        t.Color,
		DisplayOrder: t.DisplayOrder,
		IsActive:     t.IsActive,
		AssetCount:   t.AssetCount,
		Fields:       fields,
		CreatedAt:    t.CreatedAt,
		UpdatedAt:    t.UpdatedAt,
	}
}

// MapAssetCategoryToResponse converts a models.AssetCategory into the v1 DTO.
func MapAssetCategoryToResponse(c *models.AssetCategory) AssetCategoryResponse {
	return AssetCategoryResponse{
		ID:               c.ID,
		SetID:            c.SetID,
		Name:             c.Name,
		Description:      c.Description,
		ParentID:         c.ParentID,
		Path:             c.Path,
		HasChildren:      c.HasChildren,
		ChildrenCount:    c.ChildrenCount,
		DescendantsCount: c.DescendantsCount,
		AssetCount:       c.AssetCount,
		CreatedAt:        c.CreatedAt,
		UpdatedAt:        c.UpdatedAt,
	}
}

// MapAssetStatusToResponse converts a models.AssetStatus into the v1 DTO.
func MapAssetStatusToResponse(s *models.AssetStatus) AssetStatusResponse {
	return AssetStatusResponse{
		ID:           s.ID,
		SetID:        s.SetID,
		Name:         s.Name,
		Color:        s.Color,
		Description:  s.Description,
		IsDefault:    s.IsDefault,
		DisplayOrder: s.DisplayOrder,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
	}
}
