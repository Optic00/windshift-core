package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"windshift/internal/models"
)

type itemStatusTransition struct {
	oldStatusID *int
	newStatusID *int
	changedAt   time.Time
}

// GetStatusDurations computes accumulated wall-clock time per status from the
// item's creation time through its recorded transitions.
func (r *ItemRepository) GetStatusDurations(ctx context.Context, itemID int, calculatedAt time.Time) (*models.ItemStatusDurations, error) {
	var createdAt time.Time
	var currentStatusID sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `
		SELECT created_at, status_id
		FROM items
		WHERE id = ?
	`, itemID).Scan(&createdAt, &currentStatusID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get item status-duration baseline: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT old_value, new_value, changed_at
		FROM item_history
		WHERE item_id = ? AND field_name = 'status_id'
		ORDER BY changed_at ASC, id ASC
	`, itemID)
	if err != nil {
		return nil, fmt.Errorf("get item status transitions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	transitions := make([]itemStatusTransition, 0)
	statusIDs := make(map[int]struct{})
	if currentStatusID.Valid {
		statusIDs[int(currentStatusID.Int64)] = struct{}{}
	}
	for rows.Next() {
		var oldValue, newValue sql.NullString
		var changedAt time.Time
		if err := rows.Scan(&oldValue, &newValue, &changedAt); err != nil {
			return nil, fmt.Errorf("scan item status transition: %w", err)
		}
		transition := itemStatusTransition{
			oldStatusID: parseHistoryStatusID(oldValue),
			newStatusID: parseHistoryStatusID(newValue),
			changedAt:   changedAt,
		}
		if transition.oldStatusID != nil {
			statusIDs[*transition.oldStatusID] = struct{}{}
		}
		if transition.newStatusID != nil {
			statusIDs[*transition.newStatusID] = struct{}{}
		}
		transitions = append(transitions, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate item status transitions: %w", err)
	}

	statusNames, err := r.getStatusNames(ctx, statusIDs)
	if err != nil {
		return nil, err
	}

	var current *int
	if currentStatusID.Valid {
		id := int(currentStatusID.Int64)
		current = &id
	}
	return accumulateStatusDurations(createdAt, current, transitions, statusNames, calculatedAt), nil
}

func parseHistoryStatusID(value sql.NullString) *int {
	if !value.Valid {
		return nil
	}
	id, err := strconv.Atoi(strings.TrimSpace(value.String))
	if err != nil || id <= 0 {
		return nil
	}
	return &id
}

func (r *ItemRepository) getStatusNames(ctx context.Context, statusIDs map[int]struct{}) (map[int]string, error) {
	names := make(map[int]string, len(statusIDs))
	if len(statusIDs) == 0 {
		return names, nil
	}

	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM statuses`)
	if err != nil {
		return nil, fmt.Errorf("get status names for item durations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan status name for item durations: %w", err)
		}
		if _, wanted := statusIDs[id]; wanted {
			names[id] = name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate status names for item durations: %w", err)
	}
	return names, nil
}

func accumulateStatusDurations(createdAt time.Time, currentStatusID *int, transitions []itemStatusTransition, statusNames map[int]string, calculatedAt time.Time) *models.ItemStatusDurations {
	result := &models.ItemStatusDurations{
		Statuses:     make([]models.ItemStatusDuration, 0),
		CalculatedAt: calculatedAt,
	}
	if calculatedAt.Before(createdAt) {
		createdAt = calculatedAt
	}

	activeStatusID := currentStatusID
	if len(transitions) > 0 {
		if transitions[0].oldStatusID != nil {
			activeStatusID = transitions[0].oldStatusID
		} else if transitions[0].newStatusID != nil {
			activeStatusID = transitions[0].newStatusID
		}
	}

	indices := make(map[int]int)
	addInterval := func(statusID *int, startedAt, endedAt time.Time) {
		if statusID == nil {
			return
		}
		if endedAt.Before(startedAt) {
			endedAt = startedAt
		}
		index, exists := indices[*statusID]
		if !exists {
			index = len(result.Statuses)
			indices[*statusID] = index
			result.Statuses = append(result.Statuses, models.ItemStatusDuration{
				StatusID:       *statusID,
				StatusName:     statusNames[*statusID],
				FirstEnteredAt: startedAt,
				LastEnteredAt:  startedAt,
			})
		} else {
			result.Statuses[index].LastEnteredAt = startedAt
		}
		result.Statuses[index].DurationSeconds += int64(endedAt.Sub(startedAt) / time.Second)
	}

	cursor := createdAt
	for _, transition := range transitions {
		changedAt := transition.changedAt
		if changedAt.Before(cursor) {
			changedAt = cursor
		}
		if changedAt.After(calculatedAt) {
			changedAt = calculatedAt
		}
		addInterval(activeStatusID, cursor, changedAt)
		activeStatusID = transition.newStatusID
		cursor = changedAt
	}

	// The item row is authoritative if legacy or imported history ends in a
	// different status. The latest recorded transition is the best available
	// entry point for that current status.
	if currentStatusID != nil && (activeStatusID == nil || *activeStatusID != *currentStatusID) {
		activeStatusID = currentStatusID
	}
	addInterval(activeStatusID, cursor, calculatedAt)

	if currentStatusID != nil {
		if index, ok := indices[*currentStatusID]; ok {
			result.Statuses[index].IsCurrent = true
		}
	}
	return result
}
