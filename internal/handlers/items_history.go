package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

// GetItemHistory returns the history of changes for a specific item
func (h *ItemHandler) GetItemHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// First, get the item to check workspace ownership and permissions
	workspaceID, err := repository.NewItemRepository(h.db).GetWorkspaceID(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, err)
		return
	}

	// Check if user has permission to view items in this workspace. Active
	// approvers without workspace item.view are allowed through so they can
	// inspect change history for context before deciding.
	canView, permErr := h.canViewItemAsActor(r.Context(), user.ID, id, workspaceID)
	if permErr != nil {
		respondInternalError(w, r, permErr)
		return
	}
	if !canView {
		respondNotFound(w, r, "Item")
		return
	}

	// Fetch history with user information. Approval-engine events live in
	// approval_decisions and are surfaced here too so the item's history tab
	// is the single chronological feed (approve/reject/comment text included).
	// Synthetic IDs for approval rows are negated to avoid colliding with
	// item_history IDs in the response.
	includeAgentOwner := canViewAgentOwnerAttribution(h.permissionService, user.ID)
	history, err := repository.NewItemRepository(h.db).GetHistoryWithApprovals(id, includeAgentOwner)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Determine which time projects the caller may see by name so project_id
	// history rows don't disclose restricted project names. nil = full access
	// (GetAccessibleProjects sentinel); otherwise only listed IDs resolve.
	var allowedProjects map[int]struct{}
	if accessible, accErr := services.NewTimePermissionService(h.db, h.permissionService).GetAccessibleProjects(user.ID); accErr != nil {
		slog.Warn("failed to load accessible projects for history masking", slog.Int("user_id", user.ID), slog.Any("error", accErr))
		allowedProjects = map[int]struct{}{} // fail closed: resolve no project names
	} else if accessible != nil {
		allowedProjects = make(map[int]struct{}, len(accessible))
		for _, pid := range accessible {
			allowedProjects[pid] = struct{}{}
		}
	}

	// Resolve ID values to human-readable names
	for i := range history {
		h.resolveHistoryValues(&history[i], allowedProjects)
	}

	respondJSONOK(w, history)
}

// resolveHistoryValues resolves ID values to human-readable names based on field name.
// allowedProjects gates project_id name resolution: nil means full access,
// otherwise only project IDs in the set resolve to a name.
func (h *ItemHandler) resolveHistoryValues(entry *models.ItemHistory, allowedProjects map[int]struct{}) {
	// Resolve old value if present
	if entry.OldValue != nil && *entry.OldValue != "" {
		if resolved := h.resolveValue(entry.FieldName, *entry.OldValue, allowedProjects); resolved != "" {
			entry.ResolvedOldValue = &resolved
		}
	}

	// Resolve new value if present
	if entry.NewValue != nil && *entry.NewValue != "" {
		if resolved := h.resolveValue(entry.FieldName, *entry.NewValue, allowedProjects); resolved != "" {
			entry.ResolvedNewValue = &resolved
		}
	}
}

// resolveValue resolves a single value based on field name
func (h *ItemHandler) resolveValue(fieldName, value string, allowedProjects map[int]struct{}) string {
	// Multi-milestone history rows store a comma-joined ID list in
	// old/new_value (e.g. "1,4,5"). Resolve each ID to a name before
	// the single-int Atoi path below.
	if fieldName == "milestones" {
		parts := strings.Split(value, ",")
		names := make([]string, 0, len(parts))
		for _, p := range parts {
			id, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				continue
			}
			if name := h.idResolver.ResolveMilestoneName(id); name != "" {
				names = append(names, name)
			}
		}
		return strings.Join(names, ", ")
	}

	id, err := strconv.Atoi(value)
	if err != nil {
		return ""
	}

	switch fieldName {
	case "assignee_id":
		return h.idResolver.ResolveUserName(id)
	case "priority_id":
		return h.idResolver.ResolvePriorityName(id)
	case "status_id":
		return h.idResolver.ResolveStatusName(id)
	case "parent_id":
		return h.idResolver.ResolveItemKey(id)
	case "project_id":
		// Leave the resolved value empty for projects the caller can't
		// access; the raw ID stays but the name is not disclosed.
		if allowedProjects != nil {
			if _, ok := allowedProjects[id]; !ok {
				return ""
			}
		}
		return h.idResolver.ResolveProjectName(id)
	case "milestone_id": // legacy field name kept for old history rows
		return h.idResolver.ResolveMilestoneName(id)
	case "item_type_id":
		return h.idResolver.ResolveItemTypeName(id)
	default:
		return ""
	}
}
