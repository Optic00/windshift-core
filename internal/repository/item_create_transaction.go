package repository

import (
	"context"
	"fmt"

	"windshift/internal/database"
	"windshift/internal/models"
)

// IsItemCreateRetryable reports whether item creation should restart on a
// fresh transaction.
func IsItemCreateRetryable(err error) bool {
	return IsFracIndexUniqueViolation(err) ||
		IsWorkspaceItemNumberUniqueViolation(err) ||
		IsSerializationAbort(err)
}

// WithItemCreateTransaction runs one complete item creation transaction and
// retries conflicts that require a fresh snapshot. The callback must allocate
// ranks and workspace item numbers inside the supplied transaction.
func WithItemCreateTransaction(
	ctx context.Context,
	db database.Database,
	create func(tx database.Tx) (int, error),
) (int, error) {
	if ctx == nil {
		return 0, fmt.Errorf("item creation requires a context")
	}
	if db == nil {
		return 0, fmt.Errorf("item creation requires a database")
	}
	if create == nil {
		return 0, fmt.Errorf("item creation requires a transaction callback")
	}

	var lastErr error
	for range FracIndexMaxRetries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return 0, fmt.Errorf("begin item creation transaction: %w", err)
		}

		itemID, err := create(tx)
		if err == nil {
			if commitErr := tx.Commit(); commitErr != nil {
				err = fmt.Errorf("commit item creation transaction: %w", commitErr)
			}
		}
		if err == nil {
			return itemID, nil
		}
		_ = tx.Rollback()

		if !IsItemCreateRetryable(err) {
			return 0, err
		}
		lastErr = err
	}

	return 0, fmt.Errorf("item creation failed after %d attempts: %w", FracIndexMaxRetries, lastErr)
}

// CreateWithRetry appends an item and any source-specific records in one
// retryable transaction.
func (r *ItemRepository) CreateWithRetry(
	ctx context.Context,
	item *models.Item,
	afterCreate func(tx database.Tx, itemID int) error,
) (int, error) {
	if item == nil {
		return 0, fmt.Errorf("item creation requires an item")
	}

	itemID, err := WithItemCreateTransaction(ctx, r.db, func(tx database.Tx) (int, error) {
		attempt := *item
		nextNumber, err := r.GetNextWorkspaceItemNumber(tx, attempt.WorkspaceID)
		if err != nil {
			return 0, fmt.Errorf("allocate workspace item number: %w", err)
		}
		attempt.WorkspaceItemNumber = nextNumber
		attempt.FracIndex = nil

		itemID, err := r.Create(tx, &attempt)
		if err != nil {
			return 0, err
		}
		if afterCreate != nil {
			if err := afterCreate(tx, itemID); err != nil {
				return 0, err
			}
		}
		return itemID, nil
	})
	if err != nil {
		return 0, err
	}
	InvalidateItemListCountCache(r.db, item.WorkspaceID)
	return itemID, nil
}
