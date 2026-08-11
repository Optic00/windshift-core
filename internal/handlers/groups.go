package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
)

type GroupHandler struct {
	repo              *repository.GroupRepository
	permissionService *services.PermissionService
	auditor           *logger.Auditor
}

func NewGroupHandler(repo *repository.GroupRepository, permissionService *services.PermissionService, auditor *logger.Auditor) *GroupHandler {
	return &GroupHandler{
		repo:              repo,
		permissionService: permissionService,
		auditor:           auditor,
	}
}

// GetAll returns all groups with member counts
func (h *GroupHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	groups, err := h.repo.ListAll()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if groups == nil {
		groups = []models.TeamGroup{}
	}

	respondJSONOK(w, groups)
}

// Get returns a specific group with its members
func (h *GroupHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Get group details
	group, err := h.repo.GetByID(id)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "group")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Get group members
	members, err := h.repo.ListMembers(id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	group.Members = members
	group.MemberCount = len(members)

	respondJSONOK(w, group)
}

// Create creates a new group
func (h *GroupHandler) Create(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeJSON[models.TeamGroupCreateRequest](w, r)
	if !ok {
		return
	}
	sanitize.ApplyAll(
		sanitize.Pair{Target: &req.Name, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &req.Description, Policy: sanitize.RichText},
	)

	// Validate required fields
	if strings.TrimSpace(req.Name) == "" {
		respondValidationError(w, r, "Name is required")
		return
	}

	// Get current user ID from session/token
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	createdBy := &currentUser.ID

	// Check uniqueness before insert
	nameExists, err := h.repo.NameExists(req.Name, 0)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if nameExists {
		respondConflict(w, r, "Group name already exists")
		return
	}

	now := time.Now()
	id, err := h.repo.Create(req.Name, req.Description, createdBy, now)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			respondConflict(w, r, "Group name already exists")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	// Return the created group
	createdGroup := models.TeamGroup{
		ID:          int(id),
		Name:        req.Name,
		Description: req.Description,
		IsActive:    true,
		CreatedBy:   createdBy,
		CreatedAt:   now,
		UpdatedAt:   now,
		MemberCount: 0,
		Members:     []models.TeamGroupMember{},
	}

	// Log audit event
	auditUser := utils.GetCurrentUser(r)
	if auditUser != nil {
		groupID := int(id)
		h.auditor.LogWithDetails(r, auditUser, logger.ActionGroupCreate, logger.ResourceGroup, &groupID, req.Name, map[string]any{
			"description": req.Description,
		})
	}

	respondJSONCreated(w, createdGroup)
}

// Update updates an existing group
func (h *GroupHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Get the old group for audit logging
	oldGroup, err := h.repo.GetUpdateSnapshot(id)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "group")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Check if group is SCIM-managed
	if oldGroup.SCIMManaged {
		respondForbidden(w, r)
		return
	}

	req, ok := decodeJSON[models.TeamGroupUpdateRequest](w, r)
	if !ok {
		return
	}
	sanitize.ApplyAll(
		sanitize.Pair{Target: &req.Name, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &req.Description, Policy: sanitize.RichText},
	)

	// Validate required fields
	if strings.TrimSpace(req.Name) == "" {
		respondValidationError(w, r, "Name is required")
		return
	}

	// Check uniqueness before update
	nameExists, err := h.repo.NameExists(req.Name, id)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if nameExists {
		respondConflict(w, r, "Group name already exists")
		return
	}

	now := time.Now()
	if err := h.repo.Update(id, req.Name, req.Description, req.IsActive, now); err != nil {
		if errors.Is(err, repository.ErrDuplicateEntry) {
			respondConflict(w, r, "Group name already exists")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	// Log audit event with change tracking
	auditUser := utils.GetCurrentUser(r)
	if auditUser != nil {
		details := make(map[string]any)

		// Track what changed
		if oldGroup.Name != req.Name {
			details["name_changed"] = map[string]any{
				"old": oldGroup.Name,
				"new": req.Name,
			}
		}
		if oldGroup.Description != req.Description {
			details["description_changed"] = map[string]any{
				"old": oldGroup.Description,
				"new": req.Description,
			}
		}
		if oldGroup.IsActive != req.IsActive {
			details["is_active_changed"] = map[string]any{
				"old": oldGroup.IsActive,
				"new": req.IsActive,
			}
		}

		h.auditor.LogWithDetails(r, auditUser, logger.ActionGroupUpdate, logger.ResourceGroup, &id, req.Name, details)
	}

	// Return the updated group (call Get to get full details)
	h.Get(w, r)
}

// Delete deletes a group and all its memberships
func (h *GroupHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Ensure authenticated user context exists (required for auditing)
	auditUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Get the group details for audit logging before deletion
	snap, err := h.repo.GetDeleteSnapshot(id)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "group")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if snap.IsSystemGroup {
		respondForbidden(w, r)
		return
	}

	// Check if group is SCIM-managed
	if snap.SCIMManaged {
		respondForbidden(w, r)
		return
	}

	if err := h.repo.Delete(id); err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Log audit event
	h.auditor.LogWithDetails(r, auditUser, logger.ActionGroupDelete, logger.ResourceGroup, &id, snap.Name, map[string]any{
		"description": snap.Description,
		"is_active":   snap.IsActive,
	})

	w.WriteHeader(http.StatusNoContent)
}

// requireGroupMemberAccess parses the group ID from the URL and decodes +
// validates the member request body. It writes an HTTP error and returns
// ok=false when any step fails, so callers can simply return.
func requireGroupMemberAccess(w http.ResponseWriter, r *http.Request) (groupID int, req models.TeamGroupMemberRequest, ok bool) {
	groupID, ok = requireIDParam(w, r, "id")
	if !ok {
		return 0, req, false
	}

	req, ok = decodeJSON[models.TeamGroupMemberRequest](w, r)
	if !ok {
		return 0, req, false
	}

	if len(req.UserIDs) == 0 {
		respondValidationError(w, r, "At least one user ID is required")
		return 0, req, false
	}

	return groupID, req, true
}

// AddMembers adds users to a group
func (h *GroupHandler) AddMembers(w http.ResponseWriter, r *http.Request) {
	groupID, req, ok := requireGroupMemberAccess(w, r)
	if !ok {
		return
	}

	// Check if group exists
	exists, err := h.repo.Exists(groupID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !exists {
		respondNotFound(w, r, "group")
		return
	}

	// Get group name for audit logging
	groupName, err := h.repo.GetName(groupID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Get current user ID from session/token
	currentUser, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	addedBy := &currentUser.ID

	now := time.Now()
	addedMembers := []models.TeamGroupMember{}
	addedUsernames := []string{}

	for _, userID := range req.UserIDs {
		// Check if user exists
		userExists, err := h.repo.UserExists(userID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if !userExists {
			respondValidationError(w, r, "User ID "+strconv.Itoa(userID)+" not found")
			return
		}

		// Check if membership already exists
		membershipExists, err := h.repo.MembershipExists(groupID, userID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}
		if membershipExists {
			continue // Skip if already a member
		}

		// Add membership
		membershipID, err := h.repo.AddMember(groupID, userID, addedBy, now)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}

		// Invalidate permission cache for the user added to this group
		_ = h.permissionService.OnUserGroupMembershipChanged(userID, groupID)

		// Get user details for the response
		userEmail, userName, userUsername, err := h.repo.GetUserDisplay(userID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}

		addedMembers = append(addedMembers, models.TeamGroupMember{
			ID:           int(membershipID),
			GroupID:      groupID,
			UserID:       userID,
			AddedBy:      addedBy,
			AddedAt:      now,
			CreatedAt:    now,
			UpdatedAt:    now,
			UserEmail:    userEmail,
			UserName:     userName,
			UserUsername: userUsername,
		})
		addedUsernames = append(addedUsernames, userUsername)
	}

	// Log audit event
	auditUser := utils.GetCurrentUser(r)
	if auditUser != nil && len(addedMembers) > 0 {
		h.auditor.LogWithDetails(r, auditUser, logger.ActionGroupAddMember, logger.ResourceGroup, &groupID, groupName, map[string]any{
			"members_added": addedUsernames,
			"count":         len(addedMembers),
		})
	}

	respondJSONOK(w, map[string]any{
		"message":       "Members added successfully",
		"added_members": addedMembers,
		"members_added": len(addedMembers),
	})
}

// RemoveMembers removes users from a group
func (h *GroupHandler) RemoveMembers(w http.ResponseWriter, r *http.Request) {
	groupID, req, ok := requireGroupMemberAccess(w, r)
	if !ok {
		return
	}

	// Get group name for audit logging
	groupName, err := h.repo.GetName(groupID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "group")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	removedCount := 0
	removedUsernames := []string{}
	for _, userID := range req.UserIDs {
		// Get username before removing for audit logging
		if username, err := h.repo.GetUsername(userID); err == nil {
			removedUsernames = append(removedUsernames, username)
		}

		// Remove membership
		rowsAffected, err := h.repo.RemoveMember(groupID, userID)
		if err != nil {
			respondInternalError(w, r, err)
			return
		}

		if rowsAffected > 0 {
			_ = h.permissionService.OnUserGroupMembershipChanged(userID, groupID)
		}
		removedCount += int(rowsAffected)
	}

	// Log audit event
	auditUser := utils.GetCurrentUser(r)
	if auditUser != nil && removedCount > 0 {
		h.auditor.LogWithDetails(r, auditUser, logger.ActionGroupRemoveMember, logger.ResourceGroup, &groupID, groupName, map[string]any{
			"members_removed": removedUsernames,
			"count":           removedCount,
		})
	}

	respondJSONOK(w, map[string]any{
		"message":         "Members removed successfully",
		"members_removed": removedCount,
	})
}

// GetUserMemberships returns all groups a user belongs to
func (h *GroupHandler) GetUserMemberships(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireIDParam(w, r, "userId")
	if !ok {
		return
	}

	if AuthorizeUserRequest(w, r, userID, h.permissionService) == nil {
		return
	}

	// Check if user exists
	userExists, err := h.repo.UserExists(userID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if !userExists {
		respondNotFound(w, r, "user")
		return
	}

	groups, err := h.repo.ListUserMemberships(userID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	if groups == nil {
		groups = []models.TeamGroup{}
	}

	response := models.TeamGroupMembershipResponse{
		UserID: userID,
		Groups: groups,
	}

	respondJSONOK(w, response)
}
