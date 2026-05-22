package handlers

import (
	"context"
	"net/http"
	"strconv"

	"windshift/internal/contextkeys"
	legacyhandlers "windshift/internal/handlers"
)

// PageAttachmentUploadHandler exposes page-attachment uploads to bearer-
// authenticated callers (the ws CLI, integrations).
//
// The legacy cookie-auth route /api/attachments/upload already implements
// the full upload pipeline (validation, MIME sniff, storage, thumbnail,
// DB insert, audit log) but its auth middleware explicitly rejects
// Authorization: Bearer crw_* tokens — see middleware/auth.go. Rather
// than duplicate the pipeline here (the legacy handler's Upload already
// carries a FIXME flagging that it's overloaded), this handler:
//
//  1. authenticates the request via the v1 bearer middleware,
//  2. projects the bearer-authenticated user into the request context
//     under the key the legacy handler reads (utils.GetCurrentUser →
//     contextkeys.User),
//  3. injects entity_type=page + entity_id=<pageID> via the URL query
//     so the legacy handler's r.FormValue lookups succeed without the
//     multipart body having to carry those fields,
//  4. delegates to the legacy handler.
//
// The legacy handler already enforces page-edit permission (PageOpEdit
// via PagePermissionService, with workspace page.edit fallback) for
// entity_type='page', so the permission gate stays identical to the
// web-UI upload path. Failures return 404 (matching the rest of the
// page surface — workspace permission failures must not leak existence).
type PageAttachmentUploadHandler struct {
	BaseHandler
	legacy *legacyhandlers.AttachmentHandler
}

// NewPageAttachmentUploadHandler constructs the v1 page-attachment upload
// handler. The legacy handler must have its PagePermissionService wired
// (the v1 router does this via SetPagePermissionService).
func NewPageAttachmentUploadHandler(base BaseHandler, legacy *legacyhandlers.AttachmentHandler) *PageAttachmentUploadHandler {
	return &PageAttachmentUploadHandler{BaseHandler: base, legacy: legacy}
}

// Upload handles POST /rest/api/v1/workspaces/{id}/pages/{pageId}/attachments.
//
// @Summary      Upload an attachment to a workspace knowledge page
// @Description  Uploads a file as an attachment on the given page. Requires
// @Description  the bearer token to carry the `pages:write` scope and the
// @Description  authenticated user to have `page.edit` on the page (Editor
// @Description  role on the workspace by default). The response includes
// @Description  the attachment id which can be embedded in markdown as
// @Description  `![alt](/api/attachments/{id}/download)`.
// @Tags         pages
// @Accept       multipart/form-data
// @Produce      application/json
// @Security     BearerAuth
// @Param        id      path      int    true  "Workspace ID"
// @Param        pageId  path      int    true  "Page ID"
// @Param        file    formData  file   true  "File to upload"
// @Success      200     {object}  map[string]interface{}
// @Failure      400     {object}  restapi.APIError
// @Failure      404     {object}  restapi.APIError  "page not found or you lack page.edit"
// @Router       /workspaces/{id}/pages/{pageId}/attachments [post]
func (h *PageAttachmentUploadHandler) Upload(w http.ResponseWriter, r *http.Request) {
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return
	}
	pageID, ok := h.ParsePathID(w, r, "pageId", "page ID")
	if !ok {
		return
	}

	// Stuff the entity discriminators into the URL query — r.FormValue
	// reads both query and parsed-form values, so the legacy handler will
	// see entity_type=page and entity_id=<pageID> even though the
	// multipart body only carries the `file` part.
	q := r.URL.Query()
	q.Set("entity_type", "page")
	q.Set("entity_id", strconv.Itoa(pageID))

	// Project the bearer-authenticated user into the request context under
	// contextkeys.User. The legacy handler reads the uploader id via
	// utils.GetCurrentUser, which looks up exactly this key. Without this
	// projection the attachment row would land with uploaded_by=NULL.
	ctx := context.WithValue(r.Context(), contextkeys.User, user)

	forwarded := r.Clone(ctx)
	forwarded.URL.RawQuery = q.Encode()

	h.legacy.Upload(w, forwarded)
}
