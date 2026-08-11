package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"windshift/internal/logger"
	"windshift/internal/repository"
	"windshift/internal/utils"
)

// GetSetRoles returns all role assignments for a set (users, groups, and everyone default)
func (h *AssetHandler) GetSetRoles(w http.ResponseWriter, r *http.Request) {
	_, setID, ok := h.requireSetAdminByID(w, r)
	if !ok {
		return
	}

	userRoles, err := h.repo.FindSetUserRolesByGrantDate(setID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	groupRoles, err := h.repo.FindSetGroupRolesByGrantDate(setID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	everyoneRole, err := h.repo.GetEveryoneRoleDetailed(setID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, map[string]any{
		"user_roles":    userRoles,
		"group_roles":   groupRoles,
		"everyone_role": everyoneRole,
	})
}

// AssignRoleRequest represents the request body for assigning a role
type AssignRoleRequest struct {
	UserID  *int `json:"user_id,omitempty"`
	GroupID *int `json:"group_id,omitempty"`
	RoleID  int  `json:"role_id"`
}

// AssignSetRole assigns a role to a user or group for a set
func (h *AssetHandler) AssignSetRole(w http.ResponseWriter, r *http.Request) {
	currentUser, setID, ok := h.requireSetAdminByID(w, r)
	if !ok {
		return
	}

	req, ok := decodeJSON[AssignRoleRequest](w, r)
	if !ok {
		return
	}

	exists, err := h.repo.AssetRoleExists(req.RoleID)
	if err != nil || !exists {
		respondInvalidID(w, r, "role ID")
		return
	}

	if req.UserID == nil && req.GroupID == nil {
		respondValidationError(w, r, "Must specify user_id or group_id")
		return
	}

	// An upsert that demotes the set's sole Administrator is functionally a
	// revoke, so apply the same last-admin guard the revoke path uses.
	kind := "user"
	principalID := 0
	if req.UserID != nil {
		principalID = *req.UserID
	} else {
		kind = "group"
		principalID = *req.GroupID
	}
	if ok := h.ensureAssignWontOrphanAdmin(w, r, setID, req.RoleID, kind, principalID); !ok {
		return
	}

	if req.UserID != nil {
		err = h.repo.AssignUserRole(setID, *req.UserID, req.RoleID, currentUser.ID)
	} else {
		err = h.repo.AssignGroupRole(setID, *req.GroupID, req.RoleID, currentUser.ID)
	}

	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	_ = logger.LogAudit(h.db, logger.AuditEvent{
		UserID:       currentUser.ID,
		Username:     currentUser.Username,
		IPAddress:    utils.GetClientIP(r),
		UserAgent:    r.UserAgent(),
		ActionType:   logger.ActionAssetSetRoleAssign,
		ResourceType: logger.ResourceAssetSetRole,
		ResourceID:   &setID,
		Details:      map[string]any{"role_id": req.RoleID},
		Success:      true,
	})

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// RevokeSetRole revokes a role assignment from a user or group
func (h *AssetHandler) RevokeSetRole(w http.ResponseWriter, r *http.Request) {
	currentUser, setID, ok := h.requireSetAdminByID(w, r)
	if !ok {
		return
	}

	roleAssignmentID, ok := requireIDParam(w, r, "assignmentId")
	if !ok {
		return
	}

	assignmentType := r.URL.Query().Get("type")

	if ok := h.ensureNotLastAdmin(w, r, setID, roleAssignmentID, assignmentType); !ok {
		return
	}

	var err error
	if assignmentType == "group" {
		err = h.repo.DeleteGroupRoleAssignment(roleAssignmentID, setID)
	} else {
		err = h.repo.DeleteUserRoleAssignment(roleAssignmentID, setID)
	}

	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "Role assignment")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	_ = logger.LogAudit(h.db, logger.AuditEvent{
		UserID:       currentUser.ID,
		Username:     currentUser.Username,
		IPAddress:    utils.GetClientIP(r),
		UserAgent:    r.UserAgent(),
		ActionType:   logger.ActionAssetSetRoleRevoke,
		ResourceType: logger.ResourceAssetSetRole,
		ResourceID:   &setID,
		Details:      map[string]any{"role_assignment_id": roleAssignmentID},
		Success:      true,
	})

	w.WriteHeader(http.StatusNoContent)
}

// ensureNotLastAdmin blocks revocations that would leave an asset set with zero
// Administrator role assignments (and no everyone-role Administrator fallback).
// Returns true if the revocation is safe to proceed; writes an error response
// and returns false otherwise.
func (h *AssetHandler) ensureNotLastAdmin(w http.ResponseWriter, r *http.Request, setID, roleAssignmentID int, assignmentType string) bool {
	adminRoleID, err := h.repo.GetAssetRoleIDByName(AssetRoleAdministrator)
	if errors.Is(err, repository.ErrNotFound) {
		return true
	}
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}

	// Is the assignment being revoked an admin assignment?
	assignmentRoleID, err := h.repo.GetAssignmentRoleID(setID, roleAssignmentID, assignmentType)
	if errors.Is(err, repository.ErrNotFound) {
		// Missing row is handled by the subsequent DELETE path.
		return true
	}
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if assignmentRoleID != adminRoleID {
		return true
	}

	// Everyone-role Administrator fallback keeps the set reachable.
	everyoneRoleID, err := h.repo.GetEveryoneRoleIDValueForSet(setID)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if everyoneRoleID.Valid && int(everyoneRoleID.Int64) == adminRoleID {
		return true
	}

	kind := "user"
	if assignmentType == "group" {
		kind = "group"
	}
	remaining, err := h.repo.CountAdminAssignmentsExcluding(setID, adminRoleID, roleAssignmentID, kind)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}

	if remaining == 0 {
		respondConflict(w, r, "Cannot remove the last Administrator; grant Administrator to another user or group first.")
		return false
	}
	return true
}

// ensureAssignWontOrphanAdmin blocks a role (re)assignment that would demote a
// set's sole Administrator to a non-admin role, leaving it with no
// Administrator and no everyone-role Administrator fallback. Mirrors
// ensureNotLastAdmin for the upsert/demote path. kind is "user" or "group".
func (h *AssetHandler) ensureAssignWontOrphanAdmin(w http.ResponseWriter, r *http.Request, setID, newRoleID int, kind string, principalID int) bool {
	adminRoleID, err := h.repo.GetAssetRoleIDByName(AssetRoleAdministrator)
	if errors.Is(err, repository.ErrNotFound) {
		return true
	}
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}

	// Granting (or keeping) Administrator can never orphan a set.
	if newRoleID == adminRoleID {
		return true
	}

	// Only a demotion of a current Administrator is dangerous.
	currentRoleID, err := h.repo.GetPrincipalDirectRoleID(setID, kind, principalID)
	if errors.Is(err, repository.ErrNotFound) {
		return true
	}
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if currentRoleID != adminRoleID {
		return true
	}

	// Everyone-role Administrator fallback keeps the set reachable.
	everyoneRoleID, err := h.repo.GetEveryoneRoleIDValueForSet(setID)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if everyoneRoleID.Valid && int(everyoneRoleID.Int64) == adminRoleID {
		return true
	}

	remaining, err := h.repo.CountAdminAssignmentsExcludingPrincipal(setID, adminRoleID, kind, principalID)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if remaining == 0 {
		respondConflict(w, r, "Cannot remove the last Administrator; grant Administrator to another user or group first.")
		return false
	}
	return true
}

// ensureEveryoneChangeWontOrphanAdmin blocks clearing or lowering the
// everyone-role when it is the set's only Administrator. newRoleID is the
// proposed everyone-role (nil to remove).
func (h *AssetHandler) ensureEveryoneChangeWontOrphanAdmin(w http.ResponseWriter, r *http.Request, setID int, newRoleID *int) bool {
	adminRoleID, err := h.repo.GetAssetRoleIDByName(AssetRoleAdministrator)
	if errors.Is(err, repository.ErrNotFound) {
		return true
	}
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}

	// Keeping the everyone-role at Administrator can never orphan the set.
	if newRoleID != nil && *newRoleID == adminRoleID {
		return true
	}

	// Only removing/lowering a current everyone-role Administrator is dangerous.
	currentRoleID, err := h.repo.GetEveryoneRoleIDValueForSet(setID)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if !currentRoleID.Valid || int(currentRoleID.Int64) != adminRoleID {
		return true
	}

	// An explicit user/group Administrator keeps the set reachable.
	remaining, err := h.repo.CountAdminAssignments(setID, adminRoleID)
	if err != nil {
		respondInternalError(w, r, err)
		return false
	}
	if remaining == 0 {
		respondConflict(w, r, "Cannot remove the last Administrator; grant Administrator to a user or group first.")
		return false
	}
	return true
}
