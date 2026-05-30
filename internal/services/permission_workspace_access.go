package services

import (
	"log/slog"

	"windshift/internal/models"
	"windshift/internal/repository"
)

// AccessibleWorkspaceIDs returns the IDs of active workspaces on which the user
// has item.view permission.
//
// This is the canonical "gated-aware" accessibility check: it enumerates active
// workspaces and re-checks HasWorkspacePermission per workspace, so a workspace
// flipped into gated mode (by any explicit role assignment) is hidden from
// non-members. It deliberately differs from repository.GetAccessibleWorkspaceIDs,
// which returns every active non-personal workspace unconditionally.
//
// A per-workspace permission error is logged and that workspace is skipped, so a
// transient failure can't widen access.
func (ps *PermissionService) AccessibleWorkspaceIDs(userID int) ([]int, error) {
	activeIDs, err := repository.NewWorkspaceRepository(ps.db).ListActiveIDs()
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(activeIDs))
	for _, id := range activeIDs {
		hasView, err := ps.HasWorkspacePermission(userID, id, models.PermissionItemView)
		if err != nil {
			slog.Error("error checking item.view permission",
				slog.String("component", "permissions"),
				slog.Int("workspace_id", id), slog.Int("user_id", userID), slog.Any("error", err))
			continue
		}
		if hasView {
			out = append(out, id)
		}
	}
	return out, nil
}
