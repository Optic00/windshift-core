package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// sanitizeRequestType applies the canonical sanitize policies to the
// free-form fields on a request type payload (Name, Description, Icon,
// Color, TitleTemplate). Returns labeled warnings the handler surfaces
// on the response.
func sanitizeRequestType(rt *models.RequestType) []string {
	return sanitize.ApplyAllWithWarnings(
		sanitize.Pair{Target: &rt.Name, Policy: sanitize.PlainTextField, Label: "Name"},
		sanitize.Pair{Target: &rt.Description, Policy: sanitize.RichText, Label: "Description"},
		sanitize.Pair{Target: &rt.Icon, Policy: sanitize.ShortIdentifier, Label: "Icon"},
		sanitize.Pair{Target: &rt.Color, Policy: sanitize.ShortIdentifier, Label: "Color"},
		sanitize.Pair{Target: &rt.TitleTemplate, Policy: sanitize.PlainTextField, Label: "Title template"},
	)
}

// sanitizeRequestTypeFields scrubs the per-row portal-customization
// fields the form picker exposes. DisplayName + Description override
// the field's stock label/help text in the portal; both render as
// plain copy near the form input. JSON pointers on RequestTypeField
// are *string, so missing values stay missing rather than getting
// silently created.
func sanitizeRequestTypeFields(fields []models.RequestTypeField) []string {
	var warnings []string
	for i := range fields {
		w := sanitize.ApplyAllWithWarnings(
			sanitize.Pair{Target: &fields[i].FieldIdentifier, Policy: sanitize.ShortIdentifier, Label: "Field identifier"},
			sanitize.Pair{Target: &fields[i].FieldType, Policy: sanitize.ShortIdentifier, Label: "Field type"},
			sanitize.Pair{Target: fields[i].DisplayName, Policy: sanitize.PlainTextField, Label: "Field display name"},
			sanitize.Pair{Target: fields[i].Description, Policy: sanitize.RichText, Label: "Field help text"},
			sanitize.Pair{Target: fields[i].VirtualFieldType, Policy: sanitize.ShortIdentifier, Label: "Virtual field type"},
		)
		warnings = append(warnings, w...)
	}
	return warnings
}

func requestTypeFieldSchemas(fields []models.RequestTypeField) []publicFormFieldSchema {
	schemas := make([]publicFormFieldSchema, 0, len(fields))
	for _, field := range fields {
		schemas = append(schemas, publicFormFieldSchema{
			Identifier:          field.FieldIdentifier,
			FieldType:           field.FieldType,
			DisplayOrder:        field.DisplayOrder,
			StepNumber:          field.StepNumber,
			VirtualFieldType:    field.VirtualFieldType,
			VirtualFieldOptions: field.VirtualFieldOptions,
		})
	}
	return schemas
}

type RequestTypeHandler struct {
	repo           *repository.RequestTypeRepository
	channelRepo    *repository.ChannelRepository
	screenRepo     *repository.ScreenRepository
	itemTypeRepo   *repository.ItemTypeRepository
	auditor        *logger.Auditor
	channelService *services.ChannelService
}

var errChannelDoesNotSupportRequestTypes = errors.New("channel does not support request types")

func NewRequestTypeHandler(
	repo *repository.RequestTypeRepository,
	channelRepo *repository.ChannelRepository,
	screenRepo *repository.ScreenRepository,
	itemTypeRepo *repository.ItemTypeRepository,
	auditor *logger.Auditor,
	channelService *services.ChannelService,
) *RequestTypeHandler {
	return &RequestTypeHandler{
		repo:           repo,
		channelRepo:    channelRepo,
		channelService: channelService,
		screenRepo:     screenRepo,
		itemTypeRepo:   itemTypeRepo,
		auditor:        auditor,
	}
}

func channelSupportsRequestTypes(channel *models.Channel) bool {
	return channel != nil && channel.Direction == "inbound" && (channel.Type == "portal" || channel.Type == "form")
}

func channelSupportsAssetReports(channel *models.Channel) bool {
	return channel != nil && channel.Direction == "inbound" && channel.Type == "portal"
}

// effectiveRequestTypeWorkspace resolves the runtime routing target. Legacy
// request types with a NULL workspace_id use the channel's first workspace;
// management validation must use that same target instead of skipping checks.
func effectiveRequestTypeWorkspace(served []int, pinned *int) (int, bool) {
	if pinned != nil {
		return *pinned, true
	}
	if len(served) == 0 {
		return 0, false
	}
	return served[0], true
}

// GetAllForChannel returns all request types for a specific channel
func (h *RequestTypeHandler) GetAllForChannel(w http.ResponseWriter, r *http.Request) {
	channelID, ok := requireIDParam(w, r, "channel_id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	canManage, err := h.channelService.UserCanManage(r.Context(), user.ID, channelID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canManage {
		respondNotFound(w, r, "channel")
		return
	}

	requestTypes, err := h.repo.ListByChannel(channelID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, requestTypes)
}

// Get returns a specific request type by ID
func (h *RequestTypeHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	rt, err := h.repo.GetByID(id)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "request_type")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Gate by manager scope on the owning channel. See bughunt2.md Run 6
	// finding #4.
	canManage, err := h.channelService.UserCanManage(r.Context(), user.ID, rt.ChannelID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canManage {
		respondNotFound(w, r, "request_type")
		return
	}

	respondJSONOK(w, rt)
}

// channelServedWorkspaceIDs returns the workspace list used by the owning
// channel's immutable type. Mixing portal/form lists lets an irrelevant JSON
// key satisfy validation even though the public runtime never reads it.
func (h *RequestTypeHandler) channelServedWorkspaceIDs(ctx context.Context, channelID int) ([]int, error) {
	channel, err := h.channelService.GetByID(ctx, channelID)
	if err != nil {
		return nil, err
	}
	if !channelSupportsRequestTypes(channel) {
		return nil, fmt.Errorf("%w: channel %d", errChannelDoesNotSupportRequestTypes, channelID)
	}
	cfgStr, err := h.channelRepo.GetConfig(ctx, channelID)
	if err != nil {
		return nil, err
	}
	var cfg models.ChannelConfig
	if strings.TrimSpace(cfgStr) != "" {
		if err := json.Unmarshal([]byte(cfgStr), &cfg); err != nil {
			return nil, fmt.Errorf("parse channel %d config: %w", channelID, err)
		}
	}
	switch channel.Type {
	case "portal":
		return append([]int(nil), cfg.PortalWorkspaceIDs...), nil
	case "form":
		return append([]int(nil), cfg.FormWorkspaceIDs...), nil
	default:
		return nil, fmt.Errorf("%w: channel %d", errChannelDoesNotSupportRequestTypes, channelID)
	}
}

func (h *RequestTypeHandler) availableFieldsForRequestType(ctx context.Context, rt *models.RequestType) ([]AvailableField, error) {
	workspaceID := rt.WorkspaceID
	if workspaceID == nil {
		served, err := h.channelServedWorkspaceIDs(ctx, rt.ChannelID)
		if err != nil {
			return nil, err
		}
		if effective, ok := effectiveRequestTypeWorkspace(served, nil); ok {
			workspaceID = &effective
		}
	}
	return availableCreateFields(h.screenRepo, workspaceID, rt.ItemTypeID)
}

// validateRequestTypeRouting first verifies the owning channel is an inbound
// portal/form channel, then, when the request type pins a workspace, enforces
// that the channel serves it and its configuration allows the item type. A nil
// workspace preserves the legacy fallback to the channel's first workspace.
func (h *RequestTypeHandler) validateRequestTypeRouting(w http.ResponseWriter, r *http.Request, channelID int, rt *models.RequestType) bool {
	served, err := h.channelServedWorkspaceIDs(r.Context(), channelID)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			respondValidationError(w, r, "Channel not found")
		case errors.Is(err, errChannelDoesNotSupportRequestTypes):
			respondValidationError(w, r, "Channel does not support request types")
		default:
			respondInternalError(w, r, err)
		}
		return false
	}
	effectiveWorkspaceID, routable := effectiveRequestTypeWorkspace(served, rt.WorkspaceID)
	if !routable {
		respondValidationError(w, r, "Channel has no workspace for this request type")
		return false
	}
	if !containsID(served, effectiveWorkspaceID) {
		respondValidationError(w, r, "Workspace is not served by this channel")
		return false
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return false
	}
	canConnect, err := h.channelService.UserCanConnectWorkspace(user.ID, effectiveWorkspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if !canConnect {
		respondForbidden(w, r)
		return false
	}
	allowed, err := h.repo.ItemTypeAllowedInWorkspace(effectiveWorkspaceID, rt.ItemTypeID)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if !allowed {
		respondValidationError(w, r, "Item type is not allowed in the selected workspace")
		return false
	}
	return true
}

// Create creates a new request type
func (h *RequestTypeHandler) Create(w http.ResponseWriter, r *http.Request) {
	channelID, ok := requireIDParam(w, r, "channel_id")
	if !ok {
		return
	}

	rt, ok := decodeChannelJSON[models.RequestType](w, r)
	if !ok {
		return
	}
	warnings := sanitizeRequestType(&rt)

	rt.ChannelID = channelID

	if strings.TrimSpace(rt.Name) == "" {
		respondValidationError(w, r, "Request type name is required")
		return
	}
	if rt.ItemTypeID == 0 {
		respondValidationError(w, r, "Item type ID is required")
		return
	}

	itemTypeExists, err := h.itemTypeRepo.Exists(rt.ItemTypeID)
	if err != nil || !itemTypeExists {
		respondValidationError(w, r, "Item type not found")
		return
	}
	if !h.validateRequestTypeRouting(w, r, rt.ChannelID, &rt) {
		return
	}

	if rt.Icon == "" {
		rt.Icon = "FileText"
	}
	if rt.Color == "" {
		rt.Color = "#3b82f6"
	}
	rt.TitleTemplate = strings.TrimSpace(rt.TitleTemplate)
	if rt.DisplayOrder == 0 {
		maxOrder, err := h.repo.MaxDisplayOrder(rt.ChannelID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		rt.DisplayOrder = maxOrder + 1
	}

	nameExists, err := h.repo.NameExistsInChannel(rt.ChannelID, rt.Name, 0)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if nameExists {
		respondConflict(w, r, "Request type with this name already exists for this channel")
		return
	}

	id, err := h.repo.Create(&rt)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			respondConflict(w, r, "Request type with this name already exists for this channel")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	created, err := h.repo.GetByID(int(id))
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	rt = *created

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		h.auditor.LogWithDetails(r, currentUser,
			"request_type_create", "request_type",
			&rt.ID, rt.Name,
			map[string]interface{}{
				"channel_id":     rt.ChannelID,
				"item_type_id":   rt.ItemTypeID,
				"icon":           rt.Icon,
				"color":          rt.Color,
				"title_template": rt.TitleTemplate,
			},
		)
	}

	respondJSONCreated(w, struct {
		models.RequestType
		Warnings []string `json:"warnings,omitempty"`
	}{rt, warnings})
}

// Update updates an existing request type. The route is
// PUT /channels/{channel_id}/request-types/{id}; channelMgmt middleware gates
// access and the SQL UPDATE is constrained by channel_id so a request type
// belonging to another channel cannot be touched. Body-supplied channel_id is
// ignored (it comes from the URL). workspace_id IS mutable: a supplied value
// retargets the request type, and an omitted value preserves the existing
// workspace (so callers that don't manage routing can't accidentally clear
// it). A pinned workspace must be served by the channel and must allow the
// item type.
func (h *RequestTypeHandler) Update(w http.ResponseWriter, r *http.Request) {
	channelID, ok := requireIDParam(w, r, "channel_id")
	if !ok {
		return
	}
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	old, err := h.repo.GetBasicForChannel(id, channelID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "request_type")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	rt, ok := decodeChannelJSON[models.RequestType](w, r)
	if !ok {
		return
	}
	warnings := sanitizeRequestType(&rt)

	if strings.TrimSpace(rt.Name) == "" {
		respondValidationError(w, r, "Request type name is required")
		return
	}
	if rt.ItemTypeID == 0 {
		respondValidationError(w, r, "Item type ID is required")
		return
	}

	itemTypeExists, err := h.itemTypeRepo.Exists(rt.ItemTypeID)
	if err != nil || !itemTypeExists {
		respondValidationError(w, r, "Item type not found")
		return
	}

	// workspace_id is now mutable, but omitting it must not clear the existing
	// routing target. When the body carries no workspace_id, preserve the
	// stored value before validating/persisting.
	if rt.WorkspaceID == nil {
		_, existingWorkspaceID, err := h.repo.GetItemTypeAndWorkspace(id)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			respondInternalError(w, r, err)
			return
		}
		rt.WorkspaceID = existingWorkspaceID
	}
	if !h.validateRequestTypeRouting(w, r, channelID, &rt) {
		return
	}

	rt.TitleTemplate = strings.TrimSpace(rt.TitleTemplate)

	nameExists, err := h.repo.NameExistsInChannel(channelID, rt.Name, id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if nameExists {
		respondConflict(w, r, "Request type with this name already exists for this channel")
		return
	}

	if err := h.repo.Update(id, channelID, &rt); err != nil {
		switch {
		case errors.Is(err, repository.ErrDuplicateEntry):
			respondConflict(w, r, "Request type with this name already exists for this channel")
		case errors.Is(err, repository.ErrNotFound):
			respondNotFound(w, r, "request_type")
		default:
			respondInternalError(w, r, err)
		}
		return
	}

	updated, err := h.repo.GetByID(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	rt = *updated

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		details := make(map[string]interface{})
		if old.Name != rt.Name {
			details["name_changed"] = map[string]interface{}{"old": old.Name, "new": rt.Name}
		}
		if old.ItemTypeID != rt.ItemTypeID {
			details["item_type_changed"] = map[string]interface{}{"old": old.ItemTypeID, "new": rt.ItemTypeID}
		}
		if old.Icon != rt.Icon {
			details["icon_changed"] = map[string]interface{}{"old": old.Icon, "new": rt.Icon}
		}
		if old.Color != rt.Color {
			details["color_changed"] = map[string]interface{}{"old": old.Color, "new": rt.Color}
		}
		if old.TitleTemplate != rt.TitleTemplate {
			details["title_template_changed"] = map[string]interface{}{"old": old.TitleTemplate, "new": rt.TitleTemplate}
		}

		h.auditor.LogWithDetails(r, currentUser,
			"request_type_update", "request_type",
			&rt.ID, rt.Name, details,
		)
	}

	respondJSONOK(w, struct {
		models.RequestType
		Warnings []string `json:"warnings,omitempty"`
	}{rt, warnings})
}

// Delete deletes a request type. Route is DELETE /channels/{channel_id}/request-types/{id};
// channelMgmt middleware gates and the DELETE is constrained by channel_id so a
// request type belonging to another channel cannot be deleted via this URL.
func (h *RequestTypeHandler) Delete(w http.ResponseWriter, r *http.Request) {
	channelID, ok := requireIDParam(w, r, "channel_id")
	if !ok {
		return
	}
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	requestTypeName, err := h.repo.GetNameForChannel(id, channelID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "request_type")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if err := h.repo.Delete(id, channelID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "request_type")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		h.auditor.LogWithDetails(r, currentUser,
			"request_type_delete", "request_type",
			&id, requestTypeName,
			map[string]interface{}{
				"channel_id": channelID,
			},
		)
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetFields returns all fields for a request type
func (h *RequestTypeHandler) GetFields(w http.ResponseWriter, r *http.Request) {
	requestTypeID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	rt, err := h.repo.GetByID(requestTypeID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "request_type")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	canManage, err := h.channelService.UserCanManage(r.Context(), user.ID, rt.ChannelID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canManage {
		respondNotFound(w, r, "request_type")
		return
	}

	fields, err := h.repo.ListFields(requestTypeID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, fields)
}

// UpdateFields rewrites the field schema for a request type. Route is
// PUT /channels/{channel_id}/request-types/{id}/fields; gated by channelMgmt
// and constrained to request types that belong to the URL-supplied channel.
func (h *RequestTypeHandler) UpdateFields(w http.ResponseWriter, r *http.Request) {
	channelID, ok := requireIDParam(w, r, "channel_id")
	if !ok {
		return
	}
	requestTypeID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Verify request type exists in this channel before mutating any fields.
	rt, err := h.repo.GetByID(requestTypeID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && rt.ChannelID != channelID) {
		respondNotFound(w, r, "request_type")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	fields, ok := decodeChannelJSON[[]models.RequestTypeField](w, r)
	if !ok {
		return
	}
	// Per-row Display name + Description override the field's stock label
	// and help text in the portal — both render as plain copy near the
	// form input. Warnings are stamped but the legacy response shape of
	// UpdateFields (delegating to GetFields) doesn't include them yet;
	// sanitize at decode is the primary guard, the surfaced warnings
	// will land when UpdateFields gets its own dedicated response in a
	// future slice.
	_ = sanitizeRequestTypeFields(fields)
	available, err := h.availableFieldsForRequestType(r.Context(), rt)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if err := validatePublicFormFieldSchema(requestTypeFieldSchemas(fields), available); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	if err := h.repo.ReplaceFields(requestTypeID, fields); err != nil {
		respondInternalError(w, r, err)
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		h.auditor.LogWithDetails(r, currentUser,
			"request_type_fields_update", "request_type",
			&requestTypeID, "",
			map[string]interface{}{
				"field_count": len(fields),
			},
		)
	}

	// Return the updated fields
	h.GetFields(w, r)
}

// GetAvailableFields returns all fields available for a request type based on its item type and workspace.
// Resolves fields via: workspace → workspace_configuration_sets → configuration_set_item_types → create_screen → screen_fields.
// Falls back to default fields (title, description) when workspace_id is not set or no screen is found.
func (h *RequestTypeHandler) GetAvailableFields(w http.ResponseWriter, r *http.Request) {
	requestTypeID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	rt, err := h.repo.GetByID(requestTypeID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "request_type")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	canManage, err := h.channelService.UserCanManage(r.Context(), user.ID, rt.ChannelID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !canManage {
		respondNotFound(w, r, "request_type")
		return
	}

	fields, err := h.availableFieldsForRequestType(r.Context(), rt)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, fields)
}

// UpdateVisibility updates only the visibility settings for a request type.
// Route is PUT /channels/{channel_id}/request-types/{id}/visibility — gated by
// channelMgmt and scoped by channel_id in the SQL.
func (h *RequestTypeHandler) UpdateVisibility(w http.ResponseWriter, r *http.Request) {
	channelID, ok := requireIDParam(w, r, "channel_id")
	if !ok {
		return
	}
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	var req visibilityInput
	if !decodeChannelRequest(w, r, &req, false) {
		return
	}

	if err := h.repo.UpdateVisibility(id, channelID, req.GroupIDs, req.OrgIDs); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "request_type")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	rt, err := h.repo.GetByID(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	currentUser := utils.GetCurrentUser(r)
	if currentUser != nil {
		h.auditor.LogWithDetails(r, currentUser,
			"request_type_visibility_update", "request_type",
			&rt.ID, rt.Name,
			map[string]interface{}{
				"visibility_group_ids": rt.VisibilityGroupIDs,
				"visibility_org_ids":   rt.VisibilityOrgIDs,
			},
		)
	}

	respondJSONOK(w, *rt)
}
