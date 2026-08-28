package services

import (
	"context"
	"fmt"
	"time"

	"windshift/internal/database"
	"windshift/internal/itemevents"
	"windshift/internal/models"
	"windshift/internal/repository"
)

func itemMilestoneIDsInTx(tx database.Tx, itemID int) ([]int, error) {
	rows, err := tx.Query("SELECT milestone_id FROM item_milestones WHERE item_id = ? ORDER BY milestone_id", itemID)
	if err != nil {
		return nil, fmt.Errorf("load item milestones for event: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func recordRemovedItemLinks(ctx context.Context, db database.Database, tx database.Tx, itemIDs []int, metadata itemevents.Metadata) error {
	return itemevents.NewRecorder(db).RemovedLinks(ctx, tx, "item", itemIDs, metadata)
}

func itemEventMetadata(userID int, sourceKind string, actionContext *ActionContext) itemevents.Metadata {
	metadata := itemevents.System(sourceKind)
	if userID > 0 {
		metadata = itemevents.User(userID, sourceKind)
	}
	if actionContext == nil {
		return metadata
	}
	metadata.SourceKind = "automation"
	if actionContext.SourceApplication != "" {
		metadata.SourceRef = actionContext.SourceApplication
	}
	metadata.CorrelationID = actionContext.ExecutionChainID
	metadata.CausationEventKey = actionContext.CausationEventKey
	metadata.Automation = &itemevents.AutomationContext{
		TriggeredByAction: actionContext.TriggeredByAction,
		ExecutionChainID:  actionContext.ExecutionChainID,
		CascadeDepth:      actionContext.CascadeDepth,
		SourceApplication: actionContext.SourceApplication,
	}
	return metadata
}

func mergeItemEventMetadata(metadata, fallback itemevents.Metadata) itemevents.Metadata {
	if metadata.OccurredAt.IsZero() {
		metadata.OccurredAt = fallback.OccurredAt
	}
	if metadata.ActorKind == "" {
		metadata.ActorKind = fallback.ActorKind
	}
	if metadata.ActorRef == "" {
		metadata.ActorRef = fallback.ActorRef
	}
	if metadata.SourceKind == "" {
		metadata.SourceKind = fallback.SourceKind
	}
	if metadata.SourceRef == "" {
		metadata.SourceRef = fallback.SourceRef
	}
	if metadata.CorrelationID == "" {
		metadata.CorrelationID = fallback.CorrelationID
	}
	if metadata.CausationEventKey == "" {
		metadata.CausationEventKey = fallback.CausationEventKey
	}
	if metadata.Automation == nil {
		metadata.Automation = fallback.Automation
	}
	return metadata
}

func itemCreateEventMetadata(params ItemCreationParams, occurredAt time.Time) itemevents.Metadata {
	fallback := itemevents.System("application")
	switch {
	case params.CreatorPortalCustomerID != nil:
		fallback = itemevents.PortalCustomer(*params.CreatorPortalCustomerID, "portal")
	case params.ValidatingUserID > 0:
		fallback = itemevents.User(params.ValidatingUserID, "application")
	case params.CreatorID != nil && *params.CreatorID > 0:
		fallback = itemevents.User(*params.CreatorID, "application")
	}
	fallback.OccurredAt = occurredAt
	return mergeItemEventMetadata(params.EventMetadata, fallback)
}

func itemHistoryEventChanges(history []repository.HistoryEntry) []itemevents.FieldChange {
	changes := make([]itemevents.FieldChange, 0, len(history))
	for _, entry := range history {
		changes = append(changes, itemevents.FieldChange{
			Field: entry.FieldName, OldValue: entry.OldValue, NewValue: entry.NewValue,
		})
	}
	return changes
}

func actionContextFromExecution(ctx *models.ExecutionContext) *ActionContext {
	if ctx == nil || ctx.Event == nil {
		return nil
	}
	return &ActionContext{
		TriggeredByAction: true,
		ExecutionChainID:  ctx.ChainID,
		CascadeDepth:      ctx.Event.CascadeDepth + 1,
		SourceApplication: "workspace",
	}
}
