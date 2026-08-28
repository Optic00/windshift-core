// Package logbookevents defines canonical logbook document facts.
package logbookevents

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"windshift/internal/database"
	"windshift/internal/events"

	"uuid"
)

const (
	// Classified is emitted when ingestion atomically publishes a ready
	// document and its searchable chunks.
	Classified = "logbook.document.classified.v1"

	PayloadVersion = 1
)

// DocumentSnapshot is the immutable action-facing document state.
type DocumentSnapshot struct {
	ID          string `json:"id"`
	BucketID    string `json:"bucket_id"`
	Title       string `json:"title"`
	ContentType string `json:"content_type,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
	SourceType  string `json:"source_type,omitempty"`
	Author      string `json:"author,omitempty"`
	RawContent  string `json:"raw_content,omitempty"`
}

// AutomationContext preserves loop-prevention metadata across applications.
type AutomationContext struct {
	TriggeredByAction bool   `json:"triggered_by_action,omitempty"`
	ExecutionChainID  string `json:"execution_chain_id,omitempty"`
	CascadeDepth      int    `json:"cascade_depth,omitempty"`
	SourceApplication string `json:"source_application,omitempty"`
}

// ClassifiedV1 is the version-one document classification payload.
type ClassifiedV1 struct {
	Document   DocumentSnapshot   `json:"document"`
	Automation *AutomationContext `json:"automation,omitempty"`
}

// ClassifiedInput supplies canonical metadata at the source transaction.
type ClassifiedInput struct {
	Document          DocumentSnapshot
	WorkspaceID       *int
	ActorUserID       int
	OccurredAt        time.Time
	CorrelationID     string
	CausationEventKey string
	Automation        *AutomationContext
}

// Recorder appends canonical logbook facts through source transactions.
type Recorder struct {
	store *events.Store
}

func NewRecorder(db database.Database) *Recorder {
	return &Recorder{store: events.NewStore(db)}
}

func (r *Recorder) RecordClassified(ctx context.Context, tx database.Tx, input ClassifiedInput) (*events.Event, error) {
	payload, err := json.Marshal(ClassifiedV1{Document: input.Document, Automation: input.Automation})
	if err != nil {
		return nil, fmt.Errorf("encode logbook document classified event: %w", err)
	}
	occurredAt := input.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	actorKind := "system"
	actorRef := ""
	if input.ActorUserID > 0 {
		actorKind = "user"
		actorRef = strconv.Itoa(input.ActorUserID)
	}
	event, err := r.store.Append(ctx, tx, events.NewEvent{
		Key: uuid.New().String(), WorkspaceID: input.WorkspaceID,
		AggregateType: "logbook_document", AggregateID: input.Document.ID,
		Type: Classified, PayloadVersion: PayloadVersion, OccurredAt: occurredAt,
		ActorKind: actorKind, ActorRef: actorRef, SourceKind: "logbook",
		CorrelationID: input.CorrelationID, CausationEventKey: input.CausationEventKey,
		Payload: payload,
	})
	if err != nil {
		return nil, fmt.Errorf("record logbook document classified event: %w", err)
	}
	return event, nil
}
