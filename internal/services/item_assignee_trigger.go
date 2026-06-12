package services

import (
	"context"
	"log/slog"
)

// ItemAssigneeTrigger is the coding-agent harness's interest in item writes
// (WI-88): when an item gains a new assignee, the trigger checks for a
// workspace_agent_bindings row pointing at that assignee and, if found,
// starts a run. *BindingService implements it; the interface stays small so
// the item create/update services stay ignorant of the binding machinery
// (and so tests can stub it).
type ItemAssigneeTrigger interface {
	MaybeStartRunForAssignee(ctx context.Context, workspaceID, itemID int, oldAssignee, newAssignee *int, triggeredByUserID int) error
}

// itemAssigneeTrigger is the process-wide trigger registered once during
// server boot, before the HTTP server serves — deliberately unsynchronized.
// It lives at package level (rather than as a field on ItemUpdateService /
// an ItemCreationParams entry) because both are constructed ad hoc from a
// bare db handle at many call sites; per-site injection is exactly how the
// create path missed the trigger in the first place.
var itemAssigneeTrigger ItemAssigneeTrigger

// SetItemAssigneeTrigger registers the coding-agent assignee trigger fired
// by CreateItem and ItemUpdateService.UpdateItem. Nil disables the hook.
func SetItemAssigneeTrigger(t ItemAssigneeTrigger) {
	itemAssigneeTrigger = t
}

// maybeTriggerAssigneeRun fires the registered trigger for an assignee
// change. The trigger itself no-ops cheaply when the assignee did not
// actually change or no binding matches, so callers may invoke it eagerly.
// Errors are logged-and-swallowed: a failed trigger must never fail the
// item write that caused it.
func maybeTriggerAssigneeRun(workspaceID, itemID int, oldAssignee, newAssignee *int, triggeredByUserID int) {
	if itemAssigneeTrigger == nil || newAssignee == nil {
		return
	}
	// context.Background(): the run outlives the originating request; tying
	// it to a request context would cancel run dispatch on client disconnect.
	if err := itemAssigneeTrigger.MaybeStartRunForAssignee(context.Background(), workspaceID, itemID, oldAssignee, newAssignee, triggeredByUserID); err != nil {
		slog.Warn("coding-agent binding trigger failed",
			slog.Int("workspace_id", workspaceID),
			slog.Int("item_id", itemID),
			slog.Any("error", err),
		)
	}
}
