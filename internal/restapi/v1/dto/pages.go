package dto

import (
	"encoding/json"
	"fmt"
	"time"

	"windshift/internal/models"
)

// PageResponse is the public v1 representation of a workspace knowledge
// page. Fields mirror models.Page minus internal-only columns and with
// HATEOAS-style links for the CLI to discover related endpoints.
type PageResponse struct {
	ID                 int            `json:"id"`
	WorkspaceID        int            `json:"workspace_id"`
	ParentID           *int           `json:"parent_id,omitempty"`
	Title              string         `json:"title"`
	Slug               string         `json:"slug"`
	Metadata           map[string]any `json:"metadata"`
	Content            string         `json:"content,omitempty"`
	Excerpt            string         `json:"excerpt,omitempty"`
	ContentHash        string         `json:"content_hash,omitempty"`
	Path               string         `json:"path"`
	Depth              int            `json:"depth"`
	IsHome             bool           `json:"is_home"`
	InheritPermissions bool           `json:"inherit_permissions"`
	CreatedBy          int            `json:"created_by"`
	UpdatedBy          *int           `json:"updated_by,omitempty"`
	ArchivedBy         *int           `json:"archived_by,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	ArchivedAt         *time.Time     `json:"archived_at,omitempty"`

	// Labels carries the workspace page-labels currently attached to the
	// page. Populated by the service layer when the page-label repository
	// is wired; always serialized as an array (never null) so CLI/JSON
	// consumers don't need to null-check.
	Labels []models.PageLabel `json:"labels"`

	Links *PageLinks `json:"_links,omitempty"`
}

// PageLinks groups HATEOAS endpoints for a page.
type PageLinks struct {
	Self        string `json:"self"`
	Workspace   string `json:"workspace"`
	History     string `json:"history"`
	Permissions string `json:"permissions"`
}

// PageRevisionResponse is the public v1 shape of a page revision.
type PageRevisionResponse struct {
	ID             int       `json:"id"`
	PageID         int       `json:"page_id"`
	RevisionNumber int       `json:"revision_number"`
	Title          string    `json:"title"`
	Slug           string    `json:"slug"`
	Content        string    `json:"content,omitempty"`
	Excerpt        string    `json:"excerpt,omitempty"`
	ParentID       *int      `json:"parent_id,omitempty"`
	Path           string    `json:"path"`
	Depth          int       `json:"depth"`
	ChangeSummary  string    `json:"change_summary,omitempty"`
	ChangeType     string    `json:"change_type"`
	CreatedBy      int       `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
}

// MapPageToResponse converts a models.Page into the public v1 DTO. baseURL
// is the API host (e.g. https://app.example.com) used to render absolute
// HATEOAS links. Omitting it keeps the link block out of the response.
func MapPageToResponse(p *models.Page, baseURL string) PageResponse {
	resp := PageResponse{
		ID:                 p.ID,
		WorkspaceID:        p.WorkspaceID,
		ParentID:           p.ParentID,
		Title:              p.Title,
		Slug:               p.Slug,
		Metadata:           map[string]any{},
		Content:            p.Content,
		Excerpt:            p.Excerpt,
		ContentHash:        p.ContentHash,
		Path:               p.Path,
		Depth:              p.Depth,
		IsHome:             p.IsHome,
		InheritPermissions: p.InheritPermissions,
		CreatedBy:          p.CreatedBy,
		UpdatedBy:          p.UpdatedBy,
		ArchivedBy:         p.ArchivedBy,
		CreatedAt:          p.CreatedAt,
		UpdatedAt:          p.UpdatedAt,
		ArchivedAt:         p.ArchivedAt,
		Labels:             p.Labels,
	}
	if len(p.Metadata) > 0 && json.Valid(p.Metadata) {
		_ = json.Unmarshal(p.Metadata, &resp.Metadata)
	}
	if resp.Metadata == nil {
		resp.Metadata = map[string]any{}
	}
	if resp.Labels == nil {
		resp.Labels = []models.PageLabel{}
	}
	if baseURL != "" {
		resp.Links = &PageLinks{
			Self:        fmt.Sprintf("%s/rest/api/v1/workspaces/%d/pages/%d", baseURL, p.WorkspaceID, p.ID),
			Workspace:   fmt.Sprintf("%s/rest/api/v1/workspaces/%d", baseURL, p.WorkspaceID),
			History:     fmt.Sprintf("%s/rest/api/v1/workspaces/%d/pages/%d/history", baseURL, p.WorkspaceID, p.ID),
			Permissions: fmt.Sprintf("%s/rest/api/v1/workspaces/%d/pages/%d/permissions", baseURL, p.WorkspaceID, p.ID),
		}
	}
	return resp
}

// MapPageRevisionToResponse converts a models.PageRevision into the public DTO.
func MapPageRevisionToResponse(r *models.PageRevision) PageRevisionResponse {
	return PageRevisionResponse{
		ID:             r.ID,
		PageID:         r.PageID,
		RevisionNumber: r.RevisionNumber,
		Title:          r.Title,
		Slug:           r.Slug,
		Content:        r.Content,
		Excerpt:        r.Excerpt,
		ParentID:       r.ParentID,
		Path:           r.Path,
		Depth:          r.Depth,
		ChangeSummary:  r.ChangeSummary,
		ChangeType:     r.ChangeType,
		CreatedBy:      r.CreatedBy,
		CreatedAt:      r.CreatedAt,
	}
}
