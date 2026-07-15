package services

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"windshift/internal/database"
)

// TokenTrackerConfig represents configuration for the token tracker
type TokenTrackerConfig struct {
	FlushInterval time.Duration `json:"flush_interval"` // Default: 5min
}

// DefaultTokenTrackerConfig returns default configuration
func DefaultTokenTrackerConfig() TokenTrackerConfig {
	return TokenTrackerConfig{
		FlushInterval: 5 * time.Minute,
	}
}

// tokenUpdateEntry represents a token usage update queued for DB persistence
type tokenUpdateEntry struct {
	TokenID    int
	LastUsedAt time.Time
}

// TokenTracker handles batched updates of API token last_used_at timestamps
// This prevents database write contention by buffering updates and flushing periodically
type TokenTracker struct {
	db      database.Database
	batcher *WriteBatcher[tokenUpdateEntry]

	// Statistics
	updates int64
}

// NewTokenTracker creates a new token tracker service
func NewTokenTracker(db database.Database, _ TokenTrackerConfig) *TokenTracker {
	tracker := &TokenTracker{
		db: db,
	}

	config := WriteBatcherConfig{
		FlushInterval:  30 * time.Second,
		MaxBatchSize:   100,
		MaxRetryAge:    time.Hour,
		OverflowPolicy: WriteBatcherDropNewest,
		Name:           "token_updates",
	}
	tracker.batcher = NewCoalescingWriteBatcher(config, tokenUpdateKey, mergeTokenUpdates, tracker.flushTokenBatch)
	tracker.batcher.Start()

	slog.Debug("TokenTracker initialized", slog.String("component", "tokens"), slog.Duration("flush_interval", config.FlushInterval))

	return tracker
}

func tokenUpdateKey(entry tokenUpdateEntry) string {
	return strconv.Itoa(entry.TokenID)
}

func mergeTokenUpdates(existing, incoming tokenUpdateEntry) tokenUpdateEntry {
	if incoming.LastUsedAt.After(existing.LastUsedAt) {
		return incoming
	}
	return existing
}

// RecordTokenUse marks a token as used (buffers for batch write)
// This method is safe to call concurrently from multiple goroutines
func (tt *TokenTracker) RecordTokenUse(tokenID int) {
	tt.batcher.Add(tokenUpdateEntry{
		TokenID:    tokenID,
		LastUsedAt: time.Now(),
	})
	atomic.AddInt64(&tt.updates, 1)
}

// FlushPendingUpdates flushes the write batcher
func (tt *TokenTracker) FlushPendingUpdates() error {
	return tt.batcher.Flush()
}

// flushTokenBatch persists a batch of token updates to the database.
// Called by WriteBatcher every 30s or when 100 items are queued.
func (tt *TokenTracker) flushTokenBatch(ctx context.Context, entries []tokenUpdateEntry) error {
	tx, err := tt.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin token update transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, entry := range entries {
		if _, err := tx.ExecContext(ctx, `
				UPDATE api_tokens
				SET last_used_at = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, entry.LastUsedAt, entry.TokenID); err != nil {
			return fmt.Errorf("update token last_used_at: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit token updates: %w", err)
	}
	return nil
}

// Close gracefully shuts down the tracker with final flush
func (tt *TokenTracker) Close() error {
	slog.Debug("Closing TokenTracker", slog.String("component", "tokens"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tt.batcher.StopContext(ctx); err != nil {
		return fmt.Errorf("stop token update batcher: %w", err)
	}
	slog.Debug("TokenTracker closed successfully", slog.String("component", "tokens"))
	return nil
}

// GetStats returns tracker statistics
func (tt *TokenTracker) GetStats() map[string]int64 {
	stats := tt.batcher.Stats()
	return map[string]int64{
		"updates":         atomic.LoadInt64(&tt.updates),
		"flushed":         stats.ItemsFlushed,
		"flushes":         stats.FlushCount,
		"errors":          stats.FlushErrors,
		"pending":         int64(stats.Pending),
		"dropped":         stats.ItemsDropped,
		"expired":         stats.ItemsExpired,
		"coalesced":       stats.ItemsCoalesced,
		"high_water_mark": stats.HighWaterMark,
		"max_pending":     int64(stats.MaxPending),
		"oldest_age_ms":   stats.OldestAge.Milliseconds(),
		"retries":         stats.RetryCount,
		"last_flush_ms":   stats.LastFlushDuration.Milliseconds(),
		"max_flush_ms":    stats.MaxFlushDuration.Milliseconds(),
	}
}
