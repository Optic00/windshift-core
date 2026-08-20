package routes

import "net/http"

// RegisterItemRoutes registers item-related routes (items, recurrence, comments, attachments, diagrams).
func RegisterItemRoutes(deps *Deps) {
	api := deps.API
	auth := deps.AuthMiddleware.RequireAuth
	admin := deps.PermissionMiddleware.RequireSystemAdmin()

	// Item endpoints
	api.HandleH("GET /items", auth(http.HandlerFunc(deps.Items.Item.GetAll)))
	api.HandleH("POST /items", auth(http.HandlerFunc(deps.Items.Item.Create)))
	api.HandleH("GET /items/search", auth(deps.SearchLimiter.Limit(http.HandlerFunc(deps.Items.Item.Search))))
	api.HandleH("GET /items/changes", auth(http.HandlerFunc(deps.Items.Item.GetChanges)))
	api.HandleH("GET /workspaces/{id}/items/changes", auth(http.HandlerFunc(deps.Items.Item.GetChanges)))
	// Workspace-wide (item_type, status) transition matrix: replaces the board's
	// per-pair /items/{id}/available-status-transitions preload fan-out.
	api.HandleH("GET /workspaces/{id}/transition-matrix", auth(http.HandlerFunc(deps.Items.Item.GetWorkspaceTransitionMatrix)))
	api.HandleH("GET /workspaces/{id}/collections/{collectionId}/items/changes", auth(http.HandlerFunc(deps.Items.Item.GetChanges)))
	api.HandleH("GET /items/backlog", auth(http.HandlerFunc(deps.Items.Item.GetBacklogItems)))
	// Bulk fetch by id set: kills the api.items.getMany() per-id fan-out.
	// Registered before /items/{id} so the literal segment wins over the wildcard.
	api.HandleH("GET /items/batch", auth(http.HandlerFunc(deps.Items.Item.GetBatch)))
	api.HandleH("POST /items/bulk-update", auth(http.HandlerFunc(deps.Items.Item.BulkUpdate)))
	api.HandleH("POST /items/bulk-patch", auth(http.HandlerFunc(deps.Items.Item.BulkPatch)))
	api.HandleH("POST /items/roadmap-hierarchy-dates", auth(http.HandlerFunc(deps.Items.Item.GetRoadmapHierarchyDates)))
	api.HandleH("GET /items/cache-stats", auth(http.HandlerFunc(deps.Items.Item.GetCacheStats)))
	// Stable key lookup for SPA/CLI deep links: /workspaces/WI/items/123.
	api.HandleH("GET /workspaces/{key}/items/{number}/detail-summary", auth(http.HandlerFunc(deps.Items.Detail.GetByKeyAndNumber)))
	api.HandleH("GET /workspaces/{key}/items/{number}", auth(http.HandlerFunc(deps.Items.Item.GetByKeyAndNumber)))
	// Item live-update stream (WI-484). Item-view gated (404 on no view); the
	// path ends in /events so it is exempt from per-user concurrency slots.
	api.HandleH("GET /items/{id}/events", auth(http.HandlerFunc(deps.Items.Item.Events)))
	api.HandleH("GET /items/{id}/detail-summary", auth(http.HandlerFunc(deps.Items.Detail.Get)))
	api.HandleH("GET /items/{id}", auth(http.HandlerFunc(deps.Items.Item.Get)))
	api.HandleH("PUT /items/{id}", auth(http.HandlerFunc(deps.Items.Item.Update)))
	api.HandleH("DELETE /items/{id}", auth(http.HandlerFunc(deps.Items.Item.Delete)))
	api.HandleH("GET /items/{id}/delete-info", auth(http.HandlerFunc(deps.Items.Item.GetDeleteInfo)))
	api.HandleH("DELETE /items/{id}/cascade", auth(http.HandlerFunc(deps.Items.Item.DeleteCascade)))
	api.HandleH("POST /items/{id}/reparent-children", auth(http.HandlerFunc(deps.Items.Item.ReparentChildren)))
	api.HandleH("POST /items/{id}/copy", auth(http.HandlerFunc(deps.Items.Item.Copy)))
	api.HandleH("POST /items/{id}/move-workspace/preview", auth(http.HandlerFunc(deps.Items.Item.PreviewWorkspaceMove)))
	api.HandleH("POST /items/{id}/move-workspace", auth(http.HandlerFunc(deps.Items.Item.MoveWorkspace)))
	api.HandleH("GET /items/{id}/available-status-transitions", auth(http.HandlerFunc(deps.Items.Item.GetAvailableStatusTransitions)))
	api.HandleH("GET /items/{id}/type-change-analysis", auth(http.HandlerFunc(deps.Items.Item.AnalyzeTypeChange)))
	api.HandleH("POST /items/{id}/change-type", auth(http.HandlerFunc(deps.Items.Item.ChangeType)))
	api.HandleH("POST /items/{id}/transition", auth(http.HandlerFunc(deps.Items.Item.Transition)))
	api.HandleH("GET /items/{id}/history", auth(http.HandlerFunc(deps.Items.Item.GetItemHistory)))
	api.HandleH("GET /items/{id}/status-durations", auth(http.HandlerFunc(deps.Items.Item.GetStatusDurations)))

	// Item hierarchy endpoints
	api.HandleH("GET /items/{id}/children", auth(http.HandlerFunc(deps.Items.Item.GetChildrenNew)))
	api.HandleH("GET /items/{id}/ancestors", auth(http.HandlerFunc(deps.Items.Item.GetAncestors)))
	api.HandleH("GET /items/{id}/descendants", auth(http.HandlerFunc(deps.Items.Item.GetDescendantsNew)))
	api.HandleH("GET /items/{id}/tree", auth(http.HandlerFunc(deps.Items.Item.GetTree)))
	api.HandleH("GET /items/{id}/time-rollup", auth(http.HandlerFunc(deps.Items.Item.GetTimeRollup)))

	// Item watch endpoints
	api.HandleH("POST /items/{id}/watch", auth(http.HandlerFunc(deps.Items.Item.AddWatch)))
	api.HandleH("DELETE /items/{id}/watch", auth(http.HandlerFunc(deps.Items.Item.RemoveWatch)))
	api.HandleH("GET /items/{id}/watch", auth(http.HandlerFunc(deps.Items.Item.GetWatchStatus)))

	// Fractional indexing endpoint for manual ordering
	api.HandleH("PUT /items/{id}/frac-index", auth(http.HandlerFunc(deps.Items.Item.UpdateFracIndex)))

	// Calendar scheduling endpoints
	api.HandleH("POST /items/{id}/schedule", auth(http.HandlerFunc(deps.Items.Item.ScheduleItem)))
	api.HandleH("DELETE /items/{id}/unschedule", auth(http.HandlerFunc(deps.Items.Item.UnscheduleItem)))
	api.HandleH("GET /calendar/scheduled-items", auth(http.HandlerFunc(deps.Items.Item.GetScheduledItems)))

	// Personal task relationship endpoints
	api.HandleH("GET /items/{id}/personal-tasks", auth(http.HandlerFunc(deps.Items.Item.GetPersonalTasks)))
	api.HandleH("DELETE /items/{id}/related-work-item", auth(http.HandlerFunc(deps.Items.Item.RemoveRelatedWorkItem)))

	// Recurrence endpoints
	api.HandleH("GET /items/{id}/recurrence", auth(http.HandlerFunc(deps.Items.Recurrence.GetRecurrence)))
	api.HandleH("POST /items/{id}/recurrence", auth(http.HandlerFunc(deps.Items.Recurrence.CreateRecurrence)))
	api.HandleH("PUT /items/{id}/recurrence", auth(http.HandlerFunc(deps.Items.Recurrence.UpdateRecurrence)))
	api.HandleH("DELETE /items/{id}/recurrence", auth(http.HandlerFunc(deps.Items.Recurrence.DeleteRecurrence)))
	api.HandleH("GET /items/{id}/recurrence/instances", auth(http.HandlerFunc(deps.Items.Recurrence.ListInstances)))
	api.HandleH("POST /items/{id}/recurrence/generate", auth(http.HandlerFunc(deps.Items.Recurrence.ForceGenerate)))
	api.HandleH("POST /recurrence-rules/preview", auth(http.HandlerFunc(deps.Items.Recurrence.PreviewRRule)))

	// Comment endpoints
	api.HandleH("GET /items/{id}/comments", auth(http.HandlerFunc(deps.Items.Comment.GetComments)))
	api.HandleH("POST /items/{id}/comments", auth(http.HandlerFunc(deps.Items.Comment.CreateComment)))
	api.HandleH("PUT /comments/{id}", auth(http.HandlerFunc(deps.Items.Comment.UpdateComment)))
	api.HandleH("DELETE /comments/{id}", auth(http.HandlerFunc(deps.Items.Comment.DeleteComment)))

	// Attachment endpoints (only if enabled)
	if deps.Items.Attachment != nil {
		api.HandleH("POST /attachments/upload", auth(deps.UploadLimiter.Limit(http.HandlerFunc(deps.Items.Attachment.Upload))))
		api.HandleH("GET /attachments/{id}/download", auth(http.HandlerFunc(deps.Items.Attachment.Download)))
		api.HandleH("GET /attachments/{id}/thumbnail", auth(http.HandlerFunc(deps.Items.Attachment.Thumbnail)))
		api.HandleH("DELETE /attachments/{id}", auth(http.HandlerFunc(deps.Items.Attachment.Delete)))
		api.HandleH("GET /items/{itemId}/attachments", auth(http.HandlerFunc(deps.Items.Attachment.GetByItem)))
	}

	// Attachment settings endpoints
	if deps.Items.AttachmentSettings != nil {
		api.HandleH("GET /attachment-settings", auth(http.HandlerFunc(deps.Items.AttachmentSettings.Get)))
		api.HandleH("PUT /attachment-settings/{id}", admin(http.HandlerFunc(deps.Items.AttachmentSettings.Update)))
		api.HandleH("GET /attachment-settings/status", auth(http.HandlerFunc(deps.Items.AttachmentSettings.GetStatus)))
	}

	// Diagram endpoints
	api.HandleH("POST /items/{itemId}/diagrams", auth(http.HandlerFunc(deps.Items.Diagram.Create)))
	api.HandleH("GET /items/{itemId}/diagrams", auth(http.HandlerFunc(deps.Items.Diagram.GetByItem)))
	api.HandleH("GET /diagrams/{id}", auth(http.HandlerFunc(deps.Items.Diagram.Get)))
	api.HandleH("PUT /diagrams/{id}", auth(http.HandlerFunc(deps.Items.Diagram.Update)))
	api.HandleH("DELETE /diagrams/{id}", auth(http.HandlerFunc(deps.Items.Diagram.Delete)))

	// Item links
	api.HandleH("POST /links", auth(http.HandlerFunc(deps.Items.ItemLink.CreateLink)))
	api.HandleH("DELETE /links/{id}", auth(http.HandlerFunc(deps.Items.ItemLink.DeleteLink)))
	api.HandleH("GET /links/search", auth(http.HandlerFunc(deps.Items.ItemLink.SearchLinkableItems)))
	api.HandleH("GET /links/batch", auth(http.HandlerFunc(deps.Items.ItemLink.GetLinksForItemsBatch)))
	api.HandleH("GET /items/{id}/links", auth(http.HandlerFunc(deps.Items.ItemLink.GetLinksForItem)))
	api.HandleH("GET /pages/{id}/links", auth(http.HandlerFunc(deps.Items.ItemLink.GetLinksForItem)))
	api.HandleH("GET /items/{id}/field-links/{fieldId}", auth(http.HandlerFunc(deps.Items.ItemLink.GetFieldLinks)))
	api.HandleH("GET /items/{id}/linked-assets", auth(http.HandlerFunc(deps.Items.ItemLink.GetLinkedAssets)))

	// Get worklogs by item
	api.HandleH("GET /items/{id}/worklogs", auth(http.HandlerFunc(deps.TimeTracking.Worklog.GetByItem)))

	// Label definition CRUD
	api.HandleH("GET /labels", auth(http.HandlerFunc(deps.Items.Label.GetAll)))
	api.HandleH("POST /labels", auth(http.HandlerFunc(deps.Items.Label.Create)))
	api.HandleH("GET /labels/{id}", auth(http.HandlerFunc(deps.Items.Label.Get)))
	api.HandleH("PUT /labels/{id}", admin(http.HandlerFunc(deps.Items.Label.Update)))
	api.HandleH("DELETE /labels/{id}", admin(http.HandlerFunc(deps.Items.Label.Delete)))

	// Work item template CRUD (WI-438). Reads for any workspace viewer (the
	// create-modal picker needs them); catalog writes are gated in-handler on
	// workspace.admin — matching the admin settings page's canAdminWorkspace
	// visibility, so workspace admins (not only system admins) can manage them.
	api.HandleH("GET /item-templates", auth(http.HandlerFunc(deps.Items.ItemTemplate.GetAll)))
	api.HandleH("POST /item-templates", auth(http.HandlerFunc(deps.Items.ItemTemplate.Create)))
	api.HandleH("GET /item-templates/{id}", auth(http.HandlerFunc(deps.Items.ItemTemplate.Get)))
	api.HandleH("PUT /item-templates/{id}", auth(http.HandlerFunc(deps.Items.ItemTemplate.Update)))
	api.HandleH("DELETE /item-templates/{id}", auth(http.HandlerFunc(deps.Items.ItemTemplate.Delete)))

	// Item-label management
	api.HandleH("GET /items/{id}/labels", auth(http.HandlerFunc(deps.Items.Label.GetItemLabels)))
	api.HandleH("PUT /items/{id}/labels", auth(http.HandlerFunc(deps.Items.Label.SetItemLabels)))
	api.HandleH("POST /items/{id}/labels", auth(http.HandlerFunc(deps.Items.Label.AddItemLabel)))
	api.HandleH("DELETE /items/{id}/labels/{labelId}", auth(http.HandlerFunc(deps.Items.Label.RemoveItemLabel)))
}
