package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrWriteBatcherBackoff reports that a flush was skipped until the current
	// retry window expires. It is not counted as another flush failure.
	ErrWriteBatcherBackoff = errors.New("write batcher retry backoff active")
)

// WriteBatcherOverflowPolicy defines what happens when a new unique item would
// exceed MaxPending. Coalesced updates to an existing key remain accepted.
type WriteBatcherOverflowPolicy string

const (
	// WriteBatcherDropNewest is for best-effort telemetry. Rejected writes are
	// counted as dropped and Add returns false so callers can expose the loss.
	WriteBatcherDropNewest WriteBatcherOverflowPolicy = "drop_newest"
	// WriteBatcherRejectNewest is for callers that must propagate queue
	// saturation as an error. Rejected writes are counted separately.
	WriteBatcherRejectNewest WriteBatcherOverflowPolicy = "reject_newest"
)

// WriteBatcherConfig configures the write batcher behavior.
type WriteBatcherConfig struct {
	FlushInterval       time.Duration              // How often to flush (default: 30s)
	FlushTimeout        time.Duration              // Deadline for one drain cycle (default: 30s)
	ShutdownTimeout     time.Duration              // Deadline used by Stop (default: 5s)
	MaxBatchSize        int                        // Maximum items in one flush transaction (default: 100)
	MaxPending          int                        // Hard queue bound (default: 10x MaxBatchSize)
	MaxRetryAge         time.Duration              // Drop unchanged work older than this; zero retains forever
	RetryInitialBackoff time.Duration              // First retry delay after failure (default: 100ms)
	RetryMaxBackoff     time.Duration              // Retry delay ceiling (default: 30s)
	RetryJitter         float64                    // Fractional jitter in [0,1] (default: 0.2)
	OverflowPolicy      WriteBatcherOverflowPolicy // Drop best-effort or reject critical work
	Name                string                     // Name for logging (e.g., "audit_logs")
}

type queuedWrite[T any] struct {
	item          T
	key           string
	firstQueuedAt time.Time
	lastUpdatedAt time.Time
}

type writeBatcherCoalescer[T any] struct {
	key   func(T) string
	merge func(existing, incoming T) T
}

// WriteBatcher buffers writes and flushes them periodically or at the batch
// threshold. Flushes are serialized, bounded to MaxBatchSize, retried with
// backoff, and cancellable through the context passed to flushFn.
type WriteBatcher[T any] struct {
	config    WriteBatcherConfig
	flushFn   func(context.Context, []T) error
	coalescer *writeBatcherCoalescer[T]

	mu           sync.Mutex
	buffer       []queuedWrite[T]
	keyIndexes   map[string]int
	accepting    bool
	retryAt      time.Time
	failures     int
	retryTimer   *time.Timer
	workerCancel context.CancelFunc

	flushMu sync.Mutex

	startOnce  sync.Once
	stopOnce   sync.Once
	flushCh    chan struct{}
	stopCh     chan struct{}
	workerDone chan struct{}

	// Stats
	itemsBuffered        int64
	itemsFlushed         int64
	flushCount           int64
	flushErrors          int64
	itemsDropped         int64
	itemsRejected        int64
	itemsExpired         int64
	itemsCoalesced       int64
	highWaterMark        int64
	retryCount           int64
	lastFlushDurationNS  int64
	totalFlushDurationNS int64
	maxFlushDurationNS   int64
}

// NewWriteBatcher creates a FIFO batcher. Use NewCoalescingWriteBatcher for
// high-rate updates where multiple writes to a stable key can be merged.
func NewWriteBatcher[T any](config WriteBatcherConfig, flushFn func(context.Context, []T) error) *WriteBatcher[T] {
	return newWriteBatcher(config, nil, flushFn)
}

// NewCoalescingWriteBatcher creates a batcher that stores at most one pending
// entry per key. merge must preserve the workload semantics (for example,
// latest timestamp plus summed count for activity updates).
func NewCoalescingWriteBatcher[T any](
	config WriteBatcherConfig,
	keyFn func(T) string,
	mergeFn func(existing, incoming T) T,
	flushFn func(context.Context, []T) error,
) *WriteBatcher[T] {
	if keyFn == nil || mergeFn == nil {
		panic("write batcher coalescer requires key and merge functions")
	}
	return newWriteBatcher(config, &writeBatcherCoalescer[T]{key: keyFn, merge: mergeFn}, flushFn)
}

func newWriteBatcher[T any](
	config WriteBatcherConfig,
	coalescer *writeBatcherCoalescer[T],
	flushFn func(context.Context, []T) error,
) *WriteBatcher[T] {
	if flushFn == nil {
		panic("write batcher requires a flush function")
	}
	applyWriteBatcherDefaults(&config)
	wb := &WriteBatcher[T]{
		config:     config,
		flushFn:    flushFn,
		coalescer:  coalescer,
		buffer:     make([]queuedWrite[T], 0, config.MaxBatchSize),
		accepting:  true,
		flushCh:    make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
		workerDone: make(chan struct{}),
	}
	if coalescer != nil {
		wb.keyIndexes = make(map[string]int, config.MaxPending)
	}
	return wb
}

func applyWriteBatcherDefaults(config *WriteBatcherConfig) {
	if config.FlushInterval <= 0 {
		config.FlushInterval = 30 * time.Second
	}
	if config.FlushTimeout <= 0 {
		config.FlushTimeout = 30 * time.Second
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 5 * time.Second
	}
	if config.MaxBatchSize <= 0 {
		config.MaxBatchSize = 100
	}
	if config.MaxPending <= 0 {
		config.MaxPending = config.MaxBatchSize * 10
	}
	if config.RetryInitialBackoff <= 0 {
		config.RetryInitialBackoff = 100 * time.Millisecond
	}
	if config.RetryMaxBackoff <= 0 {
		config.RetryMaxBackoff = 30 * time.Second
	}
	if config.RetryMaxBackoff < config.RetryInitialBackoff {
		config.RetryMaxBackoff = config.RetryInitialBackoff
	}
	if config.RetryJitter < 0 {
		config.RetryJitter = 0
	} else if config.RetryJitter == 0 {
		// Tests and callers that need deterministic zero jitter can set a tiny
		// negative value; defaults should avoid synchronized retry storms.
		config.RetryJitter = 0.2
	}
	if config.RetryJitter > 1 {
		config.RetryJitter = 1
	}
	if config.OverflowPolicy == "" {
		config.OverflowPolicy = WriteBatcherDropNewest
	}
}

// Start begins the periodic flush worker. It is safe to call more than once.
func (wb *WriteBatcher[T]) Start() {
	wb.startOnce.Do(func() {
		ticker := time.NewTicker(wb.config.FlushInterval)
		workerStarted := make(chan struct{})
		go func() {
			workerCtx, workerCancel := context.WithCancel(context.Background())
			defer workerCancel()
			defer close(wb.workerDone)
			defer ticker.Stop()
			wb.mu.Lock()
			wb.workerCancel = workerCancel
			wb.mu.Unlock()
			close(workerStarted)
			for {
				select {
				case <-ticker.C:
					wb.runDrainCycle(workerCtx)
				case <-wb.flushCh:
					wb.runDrainCycle(workerCtx)
				case <-wb.stopCh:
					return
				}
			}
		}()
		<-workerStarted

		slog.Info("write batcher started",
			"name", wb.config.Name,
			"flush_interval", wb.config.FlushInterval,
			"max_batch_size", wb.config.MaxBatchSize,
			"max_pending", wb.config.MaxPending,
			"max_retry_age", wb.config.MaxRetryAge,
			"overflow_policy", wb.config.OverflowPolicy,
		)
	})
}

func (wb *WriteBatcher[T]) runDrainCycle(workerCtx context.Context) {
	ctx, cancel := context.WithTimeout(workerCtx, wb.config.FlushTimeout)
	defer cancel()

	for {
		err := wb.FlushContext(ctx)
		if err != nil {
			if !errors.Is(err, ErrWriteBatcherBackoff) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Error("write batcher flush failed", "name", wb.config.Name, "error", err)
			}
			return
		}
		if wb.pendingCount() < wb.config.MaxBatchSize {
			return
		}
	}
}

// Stop gracefully stops the batcher using the configured shutdown deadline.
// Call StopContext when the caller already owns a shutdown context.
func (wb *WriteBatcher[T]) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), wb.config.ShutdownTimeout)
	defer cancel()
	if err := wb.StopContext(ctx); err != nil {
		slog.Error("write batcher stop failed", "name", wb.config.Name, "error", err)
	}
}

// StopContext stops new writes, stops the worker, and drains pending work in
// bounded batches until completion, a flush error, or the context deadline.
func (wb *WriteBatcher[T]) StopContext(ctx context.Context) error {
	wb.stopOnce.Do(func() {
		wb.mu.Lock()
		wb.accepting = false
		if wb.retryTimer != nil {
			wb.retryTimer.Stop()
		}
		if wb.workerCancel != nil {
			wb.workerCancel()
		}
		wb.mu.Unlock()
		close(wb.stopCh)
	})

	// A batcher can be used without Start in tests and explicit-flush callers.
	started := false
	wb.startOnce.Do(func() {
		started = true
		close(wb.workerDone)
	})
	if !started {
		select {
		case <-wb.workerDone:
		case <-ctx.Done():
			return fmt.Errorf("wait for write batcher worker: %w", ctx.Err())
		}
	}

	for wb.pendingCount() > 0 {
		if err := wb.flushContext(ctx, true); err != nil {
			return fmt.Errorf("final write batcher flush (%d pending): %w", wb.pendingCount(), err)
		}
	}

	stats := wb.Stats()
	slog.Info("write batcher stopped",
		"name", wb.config.Name,
		"total_items_buffered", stats.ItemsBuffered,
		"total_items_flushed", stats.ItemsFlushed,
		"total_flushes", stats.FlushCount,
		"flush_errors", stats.FlushErrors,
		"items_dropped", stats.ItemsDropped,
		"items_rejected", stats.ItemsRejected,
		"items_expired", stats.ItemsExpired,
		"items_coalesced", stats.ItemsCoalesced,
		"high_water_mark", stats.HighWaterMark,
	)
	return nil
}

// Add queues an item for batched writing. It returns false when shutdown has
// started or a new unique item exceeds MaxPending. Coalesced updates to an
// already-pending key remain accepted at capacity.
func (wb *WriteBatcher[T]) Add(item T) bool {
	now := time.Now()
	wb.mu.Lock()
	if !wb.accepting {
		wb.mu.Unlock()
		atomic.AddInt64(&wb.itemsRejected, 1)
		return false
	}
	wb.dropExpiredLocked(now)

	key := ""
	if wb.coalescer != nil {
		key = wb.coalescer.key(item)
		if index, exists := wb.keyIndexes[key]; exists {
			entry := &wb.buffer[index]
			entry.item = wb.coalescer.merge(entry.item, item)
			entry.lastUpdatedAt = now
			wb.mu.Unlock()
			atomic.AddInt64(&wb.itemsBuffered, 1)
			atomic.AddInt64(&wb.itemsCoalesced, 1)
			return true
		}
	}

	if len(wb.buffer) >= wb.config.MaxPending {
		wb.mu.Unlock()
		if wb.config.OverflowPolicy == WriteBatcherRejectNewest {
			atomic.AddInt64(&wb.itemsRejected, 1)
		} else {
			atomic.AddInt64(&wb.itemsDropped, 1)
		}
		return false
	}

	wb.buffer = append(wb.buffer, queuedWrite[T]{
		item:          item,
		key:           key,
		firstQueuedAt: now,
		lastUpdatedAt: now,
	})
	if wb.coalescer != nil {
		wb.keyIndexes[key] = len(wb.buffer) - 1
	}
	bufferLen := len(wb.buffer)
	wb.mu.Unlock()

	atomic.AddInt64(&wb.itemsBuffered, 1)
	updateAtomicMaximum(&wb.highWaterMark, int64(bufferLen))
	if bufferLen >= wb.config.MaxBatchSize {
		wb.requestFlush()
	}
	return true
}

func (wb *WriteBatcher[T]) requestFlush() {
	select {
	case wb.flushCh <- struct{}{}:
	default:
	}
}

// Flush writes one bounded batch, respecting any active retry backoff.
func (wb *WriteBatcher[T]) Flush() error {
	ctx, cancel := context.WithTimeout(context.Background(), wb.config.FlushTimeout)
	defer cancel()
	return wb.FlushContext(ctx)
}

// FlushContext writes one bounded batch and passes ctx through to the workload.
func (wb *WriteBatcher[T]) FlushContext(ctx context.Context) error {
	return wb.flushContext(ctx, false)
}

func (wb *WriteBatcher[T]) flushContext(ctx context.Context, ignoreBackoff bool) error {
	wb.flushMu.Lock()
	defer wb.flushMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	now := time.Now()
	wb.mu.Lock()
	wb.dropExpiredLocked(now)
	if len(wb.buffer) == 0 {
		wb.mu.Unlock()
		return nil
	}
	if !ignoreBackoff && now.Before(wb.retryAt) {
		retryAt := wb.retryAt
		wb.mu.Unlock()
		return fmt.Errorf("%w until %s", ErrWriteBatcherBackoff, retryAt.Format(time.RFC3339Nano))
	}

	batchSize := min(len(wb.buffer), wb.config.MaxBatchSize)
	queued := append([]queuedWrite[T](nil), wb.buffer[:batchSize]...)
	wb.buffer = append([]queuedWrite[T](nil), wb.buffer[batchSize:]...)
	wb.rebuildIndexesLocked()
	wb.mu.Unlock()

	items := make([]T, len(queued))
	for i := range queued {
		items[i] = queued[i].item
	}
	startedAt := time.Now()
	err := wb.flushFn(ctx, items)
	duration := time.Since(startedAt)
	recordFlushDuration(wb, duration)
	if err != nil {
		atomic.AddInt64(&wb.flushErrors, 1)
		atomic.AddInt64(&wb.retryCount, 1)
		wb.mu.Lock()
		wb.requeueFailedLocked(queued, time.Now())
		wb.failures++
		delay := wb.retryDelayLocked()
		wb.retryAt = time.Now().Add(delay)
		wb.scheduleRetryLocked(delay)
		wb.mu.Unlock()
		return err
	}

	atomic.AddInt64(&wb.itemsFlushed, int64(len(items)))
	atomic.AddInt64(&wb.flushCount, 1)
	wb.mu.Lock()
	wb.failures = 0
	wb.retryAt = time.Time{}
	if wb.retryTimer != nil {
		wb.retryTimer.Stop()
		wb.retryTimer = nil
	}
	wb.mu.Unlock()

	slog.Debug("write batcher flushed", "name", wb.config.Name, "items", len(items), "duration", duration)
	return nil
}

func (wb *WriteBatcher[T]) requeueFailedLocked(failed []queuedWrite[T], now time.Time) {
	combined := make([]queuedWrite[T], 0, min(wb.config.MaxPending, len(failed)+len(wb.buffer)))
	combined = append(combined, failed...)
	combined = append(combined, wb.buffer...)
	wb.buffer = nil
	if wb.coalescer == nil {
		if len(combined) > wb.config.MaxPending {
			overflow := len(combined) - wb.config.MaxPending
			combined = combined[:wb.config.MaxPending]
			wb.recordOverflow(int64(overflow))
		}
		wb.buffer = combined
		updateAtomicMaximum(&wb.highWaterMark, int64(len(wb.buffer)))
		return
	}

	wb.keyIndexes = make(map[string]int, wb.config.MaxPending)
	for _, entry := range combined {
		if index, exists := wb.keyIndexes[entry.key]; exists {
			existing := &wb.buffer[index]
			existing.item = wb.coalescer.merge(existing.item, entry.item)
			if entry.lastUpdatedAt.After(existing.lastUpdatedAt) {
				existing.lastUpdatedAt = entry.lastUpdatedAt
			}
			atomic.AddInt64(&wb.itemsCoalesced, 1)
			continue
		}
		if len(wb.buffer) >= wb.config.MaxPending {
			wb.recordOverflow(1)
			continue
		}
		wb.keyIndexes[entry.key] = len(wb.buffer)
		wb.buffer = append(wb.buffer, entry)
	}
	wb.dropExpiredLocked(now)
	updateAtomicMaximum(&wb.highWaterMark, int64(len(wb.buffer)))
}

func (wb *WriteBatcher[T]) recordOverflow(count int64) {
	if wb.config.OverflowPolicy == WriteBatcherRejectNewest {
		atomic.AddInt64(&wb.itemsRejected, count)
	} else {
		atomic.AddInt64(&wb.itemsDropped, count)
	}
}

func (wb *WriteBatcher[T]) dropExpiredLocked(now time.Time) {
	if wb.config.MaxRetryAge <= 0 || len(wb.buffer) == 0 {
		return
	}
	kept := wb.buffer[:0]
	for _, entry := range wb.buffer {
		if now.Sub(entry.lastUpdatedAt) > wb.config.MaxRetryAge {
			atomic.AddInt64(&wb.itemsExpired, 1)
			continue
		}
		kept = append(kept, entry)
	}
	wb.buffer = kept
	wb.rebuildIndexesLocked()
}

func (wb *WriteBatcher[T]) rebuildIndexesLocked() {
	if wb.coalescer == nil {
		return
	}
	clear(wb.keyIndexes)
	for index := range wb.buffer {
		wb.keyIndexes[wb.buffer[index].key] = index
	}
}

func (wb *WriteBatcher[T]) retryDelayLocked() time.Duration {
	delay := wb.config.RetryInitialBackoff
	for i := 1; i < wb.failures && delay < wb.config.RetryMaxBackoff; i++ {
		if delay > wb.config.RetryMaxBackoff/2 {
			delay = wb.config.RetryMaxBackoff
			break
		}
		delay *= 2
	}
	if delay > wb.config.RetryMaxBackoff {
		delay = wb.config.RetryMaxBackoff
	}
	if wb.config.RetryJitter > 0 {
		factor := 1 + ((rand.Float64()*2 - 1) * wb.config.RetryJitter) //nolint:gosec // scheduling jitter, not security
		delay = time.Duration(float64(delay) * factor)
		if delay < 0 {
			delay = 0
		}
	}
	return delay
}

func (wb *WriteBatcher[T]) scheduleRetryLocked(delay time.Duration) {
	if wb.retryTimer != nil {
		wb.retryTimer.Stop()
	}
	wb.retryTimer = time.AfterFunc(delay, func() {
		select {
		case <-wb.stopCh:
			return
		default:
			wb.requestFlush()
		}
	})
}

func (wb *WriteBatcher[T]) pendingCount() int {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	return len(wb.buffer)
}

// Stats returns a consistent snapshot of queue and retry statistics.
func (wb *WriteBatcher[T]) Stats() WriteBatcherStats {
	now := time.Now()
	wb.mu.Lock()
	pending := len(wb.buffer)
	oldestAge := time.Duration(0)
	if pending > 0 {
		oldest := wb.buffer[0].firstQueuedAt
		for i := 1; i < pending; i++ {
			if wb.buffer[i].firstQueuedAt.Before(oldest) {
				oldest = wb.buffer[i].firstQueuedAt
			}
		}
		oldestAge = now.Sub(oldest)
	}
	retryAt := wb.retryAt
	wb.mu.Unlock()

	return WriteBatcherStats{
		Name:               wb.config.Name,
		Pending:            pending,
		ItemsBuffered:      atomic.LoadInt64(&wb.itemsBuffered),
		ItemsFlushed:       atomic.LoadInt64(&wb.itemsFlushed),
		FlushCount:         atomic.LoadInt64(&wb.flushCount),
		FlushErrors:        atomic.LoadInt64(&wb.flushErrors),
		ItemsDropped:       atomic.LoadInt64(&wb.itemsDropped),
		ItemsRejected:      atomic.LoadInt64(&wb.itemsRejected),
		ItemsExpired:       atomic.LoadInt64(&wb.itemsExpired),
		ItemsCoalesced:     atomic.LoadInt64(&wb.itemsCoalesced),
		HighWaterMark:      atomic.LoadInt64(&wb.highWaterMark),
		MaxPending:         wb.config.MaxPending,
		OldestAge:          oldestAge,
		RetryCount:         atomic.LoadInt64(&wb.retryCount),
		RetryAt:            retryAt,
		LastFlushDuration:  time.Duration(atomic.LoadInt64(&wb.lastFlushDurationNS)),
		TotalFlushDuration: time.Duration(atomic.LoadInt64(&wb.totalFlushDurationNS)),
		MaxFlushDuration:   time.Duration(atomic.LoadInt64(&wb.maxFlushDurationNS)),
	}
}

// WriteBatcherStats contains statistics about batcher performance.
type WriteBatcherStats struct {
	Name               string
	Pending            int
	ItemsBuffered      int64
	ItemsFlushed       int64
	FlushCount         int64
	FlushErrors        int64
	ItemsDropped       int64
	ItemsRejected      int64
	ItemsExpired       int64
	ItemsCoalesced     int64
	HighWaterMark      int64
	MaxPending         int
	OldestAge          time.Duration
	RetryCount         int64
	RetryAt            time.Time
	LastFlushDuration  time.Duration
	TotalFlushDuration time.Duration
	MaxFlushDuration   time.Duration
}

func updateAtomicMaximum(target *int64, value int64) {
	for {
		previous := atomic.LoadInt64(target)
		if value <= previous || atomic.CompareAndSwapInt64(target, previous, value) {
			return
		}
	}
}

func recordFlushDuration[T any](wb *WriteBatcher[T], duration time.Duration) {
	ns := int64(duration)
	atomic.StoreInt64(&wb.lastFlushDurationNS, ns)
	atomic.AddInt64(&wb.totalFlushDurationNS, ns)
	updateAtomicMaximum(&wb.maxFlushDurationNS, ns)
}
