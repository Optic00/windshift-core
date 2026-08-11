package services

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"windshift/internal/database"
	"windshift/internal/models"
)

// LegacyItemUpdatedEmitter preserves the original per-service event pipeline
// (notifications, automation actions, webhooks) for server embeddings that do
// not install an EventCoordinator. The HTTP transport installs it as the
// default item-update emitter; wiring a coordinator replaces it. The action
// and webhook sinks are attached through setters when the server wiring
// provides them.
type LegacyItemUpdatedEmitter struct {
	db      database.Database
	notify  func(*NotificationEvent)
	action  ActionEventEmitter
	webhook WebhookDispatcher
}

func NewLegacyItemUpdatedEmitter(db database.Database, notify func(*NotificationEvent), action ActionEventEmitter, webhook WebhookDispatcher) *LegacyItemUpdatedEmitter {
	return &LegacyItemUpdatedEmitter{db: db, notify: notify, action: action, webhook: webhook}
}

func (e *LegacyItemUpdatedEmitter) SetAction(action ActionEventEmitter) {
	e.action = action
}

func (e *LegacyItemUpdatedEmitter) SetWebhook(webhook WebhookDispatcher) {
	e.webhook = webhook
}

func (e *LegacyItemUpdatedEmitter) EmitItemUpdated(original, updated *models.Item, statusChanged, assigneeChanged bool, actorUserID int, fieldChanges []HistoryEntry, actorUsername ...string) {
	actorName := resolveActorName(actorUserID, actorUsername)

	if e.notify != nil {
		var statusName string
		if statusChanged && updated.StatusID != nil && e.db != nil {
			if err := e.db.QueryRow("SELECT name FROM statuses WHERE id = ?", *updated.StatusID).Scan(&statusName); err != nil && !errors.Is(err, sql.ErrNoRows) {
				slog.Warn("failed to load status name", slog.Int("status_id", *updated.StatusID), slog.Any("error", err))
			}
		}
		itemKey := fmt.Sprintf("%s-%d", updated.WorkspaceKey, updated.WorkspaceItemNumber)

		if statusChanged {
			e.notify(&NotificationEvent{
				EventType:   models.EventStatusChanged,
				WorkspaceID: updated.WorkspaceID,
				ActorUserID: actorUserID,
				ItemID:      updated.ID,
				AssigneeID:  updated.AssigneeID,
				CreatorID:   original.CreatorID,
				Title:       "Status Changed",
				TemplateData: map[string]any{
					"item.title":  updated.Title,
					"item.key":    itemKey,
					"item.id":     updated.ID,
					"status.name": statusName,
					"user.name":   actorName,
				},
			})
		}
		if assigneeChanged {
			e.notify(&NotificationEvent{
				EventType:   models.EventItemAssigned,
				WorkspaceID: updated.WorkspaceID,
				ActorUserID: actorUserID,
				ItemID:      updated.ID,
				AssigneeID:  updated.AssigneeID,
				CreatorID:   original.CreatorID,
				Title:       "Item Assigned",
				TemplateData: map[string]any{
					"item.title": updated.Title,
					"item.key":   itemKey,
					"item.id":    updated.ID,
					"user.name":  actorName,
				},
			})
		}
		if !statusChanged && !assigneeChanged {
			e.notify(&NotificationEvent{
				EventType:   models.EventItemUpdated,
				WorkspaceID: updated.WorkspaceID,
				ActorUserID: actorUserID,
				ItemID:      updated.ID,
				AssigneeID:  updated.AssigneeID,
				CreatorID:   original.CreatorID,
				Title:       "Item Updated",
				TemplateData: map[string]any{
					"item.title": updated.Title,
					"item.key":   itemKey,
					"item.id":    updated.ID,
					"user.name":  actorName,
				},
			})
		}
	}

	if e.action != nil {
		if statusChanged {
			e.action.EmitActionEvent(&models.ActionEvent{
				EventType:   models.ActionTriggerStatusTransition,
				WorkspaceID: updated.WorkspaceID,
				ItemID:      updated.ID,
				ActorUserID: actorUserID,
				OldValues:   map[string]any{"status_id": original.StatusID},
				NewValues: map[string]any{
					"status_id":   updated.StatusID,
					"title":       updated.Title,
					"assignee_id": updated.AssigneeID,
					"creator_id":  updated.CreatorID,
				},
			})
		} else {
			e.action.EmitActionEvent(&models.ActionEvent{
				EventType:   models.ActionTriggerItemUpdated,
				WorkspaceID: updated.WorkspaceID,
				ItemID:      updated.ID,
				ActorUserID: actorUserID,
				OldValues: map[string]any{
					"status_id":   original.StatusID,
					"assignee_id": original.AssigneeID,
					"title":       original.Title,
					"priority_id": original.PriorityID,
				},
				NewValues: map[string]any{
					"status_id":   updated.StatusID,
					"assignee_id": updated.AssigneeID,
					"title":       updated.Title,
					"priority_id": updated.PriorityID,
					"creator_id":  updated.CreatorID,
				},
			})
		}
	}

	if e.webhook != nil {
		if statusChanged {
			e.webhook.DispatchEvent("status.changed", updated)
		}
		if assigneeChanged {
			e.webhook.DispatchEvent("item.assigned", updated)
		}
		e.webhook.DispatchEvent("item.updated", updated)
	}
}
