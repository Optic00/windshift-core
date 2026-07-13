package services

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// WriteBatcherConfig configures the write batcher behavior
type WriteBatcherConfig struct {
	FlushInterval time.Duration // How often to flush (default: 30s)
	MaxBatchSize  int           // Max items before forced flush (default: 100)
	MaxPending    int           // Hard queue bound (default: 10x MaxBatchSize)
	Name          string        // Name for logging (e.g., "audit_logs")
}

// WriteBatcher buffers writes and flushes them periodically or when threshold is reached.
// This reduces write contention on SQLite by batching multiple writes into single transactions.
type WriteBatcher[T any] struct {
	config  WriteBatcherConfig
	flushFn func([]T) error // Function to flush items to database

	mu     sync.Mutex
	buffer []T

	// Lifecycle
	flushTicker           *time.Ticker
	stopCh                chan struct{}
	stopOnce              sync.Once
	wg                    sync.WaitGroup
	thresholdFlushRunning int32

	// Stats
	itemsBuffered int64
	itemsFlushed  int64
	flushCount    int64
	flushErrors   int64
	itemsDropped  int64
	highWaterMark int64
}

// NewWriteBatcher creates a new write batcher with the given flush function.
// The flushFn should perform a batch INSERT of all provided items.
func NewWriteBatcher[T any](config WriteBatcherConfig, flushFn func([]T) error) *WriteBatcher[T] {
	if config.MaxPending <= 0 {
		config.MaxPending = config.MaxBatchSize * 10
		if config.MaxPending <= 0 {
			config.MaxPending = 1000
		}
	}
	return &WriteBatcher[T]{
		config:  config,
		flushFn: flushFn,
		buffer:  make([]T, 0, config.MaxBatchSize),
		stopCh:  make(chan struct{}),
	}
}

// Start begins the periodic flush goroutine
func (wb *WriteBatcher[T]) Start() {
	wb.flushTicker = time.NewTicker(wb.config.FlushInterval)

	wb.wg.Add(1)
	go func() {
		defer wb.wg.Done()
		for {
			select {
			case <-wb.flushTicker.C:
				if err := wb.Flush(); err != nil {
					slog.Error("write batcher flush failed",
						"name", wb.config.Name,
						"error", err,
					)
				}
			case <-wb.stopCh:
				return
			}
		}
	}()

	slog.Info("write batcher started",
		"name", wb.config.Name,
		"flush_interval", wb.config.FlushInterval,
		"max_batch_size", wb.config.MaxBatchSize,
		"max_pending", wb.config.MaxPending,
	)
}

// Stop gracefully stops the batcher, flushing any remaining items
func (wb *WriteBatcher[T]) Stop() {
	wb.stopOnce.Do(func() {
		close(wb.stopCh)
		if wb.flushTicker != nil {
			wb.flushTicker.Stop()
		}
		wb.wg.Wait()

		// Final flush of remaining items
		if err := wb.Flush(); err != nil {
			slog.Error("write batcher final flush failed",
				"name", wb.config.Name,
				"error", err,
			)
		}

		slog.Info("write batcher stopped",
			"name", wb.config.Name,
			"total_items_buffered", atomic.LoadInt64(&wb.itemsBuffered),
			"total_items_flushed", atomic.LoadInt64(&wb.itemsFlushed),
			"total_flushes", atomic.LoadInt64(&wb.flushCount),
			"flush_errors", atomic.LoadInt64(&wb.flushErrors),
			"items_dropped", atomic.LoadInt64(&wb.itemsDropped),
			"high_water_mark", atomic.LoadInt64(&wb.highWaterMark),
		)
	})
}

// Add queues an item for batched writing.
// If the buffer reaches MaxBatchSize, it triggers an immediate flush.
func (wb *WriteBatcher[T]) Add(item T) bool {
	wb.mu.Lock()
	if len(wb.buffer) >= wb.config.MaxPending {
		wb.mu.Unlock()
		atomic.AddInt64(&wb.itemsDropped, 1)
		return false
	}
	wb.buffer = append(wb.buffer, item)
	bufferLen := len(wb.buffer)
	wb.mu.Unlock()

	atomic.AddInt64(&wb.itemsBuffered, 1)
	for {
		previous := atomic.LoadInt64(&wb.highWaterMark)
		if int64(bufferLen) <= previous || atomic.CompareAndSwapInt64(&wb.highWaterMark, previous, int64(bufferLen)) {
			break
		}
	}

	// Trigger immediate flush if buffer is full. Only one threshold-triggered
	// flush may be in flight at a time; periodic/final Flush calls still run
	// normally and are serialized by wb.mu when swapping the buffer.
	if wb.config.MaxBatchSize > 0 && bufferLen >= wb.config.MaxBatchSize && atomic.CompareAndSwapInt32(&wb.thresholdFlushRunning, 0, 1) {
		go func() {
			defer atomic.StoreInt32(&wb.thresholdFlushRunning, 0)
			if err := wb.Flush(); err != nil {
				slog.Error("write batcher threshold flush failed",
					"name", wb.config.Name,
					"error", err,
				)
			}
		}()
	}
	return true
}

// Flush writes all buffered items to the database
func (wb *WriteBatcher[T]) Flush() error {
	wb.mu.Lock()
	if len(wb.buffer) == 0 {
		wb.mu.Unlock()
		return nil
	}

	// Swap buffer to release lock quickly
	items := wb.buffer
	wb.buffer = make([]T, 0, wb.config.MaxBatchSize)
	wb.mu.Unlock()

	// Perform the actual flush
	err := wb.flushFn(items)
	if err != nil {
		atomic.AddInt64(&wb.flushErrors, 1)
		// Preserve the oldest failed work first, but never exceed MaxPending
		// while producers continue appending during the failed flush.
		wb.mu.Lock()
		combined := make([]T, 0, wb.config.MaxPending)
		combined = append(combined, items...)
		combined = append(combined, wb.buffer...)
		if overflow := len(combined) - wb.config.MaxPending; overflow > 0 {
			combined = combined[:wb.config.MaxPending]
			atomic.AddInt64(&wb.itemsDropped, int64(overflow))
		}
		wb.buffer = combined
		wb.mu.Unlock()
		return err
	}

	atomic.AddInt64(&wb.itemsFlushed, int64(len(items)))
	atomic.AddInt64(&wb.flushCount, 1)

	slog.Debug("write batcher flushed",
		"name", wb.config.Name,
		"items", len(items),
	)

	return nil
}

// Stats returns current batcher statistics
func (wb *WriteBatcher[T]) Stats() WriteBatcherStats {
	wb.mu.Lock()
	pending := len(wb.buffer)
	wb.mu.Unlock()

	return WriteBatcherStats{
		Name:          wb.config.Name,
		Pending:       pending,
		ItemsBuffered: atomic.LoadInt64(&wb.itemsBuffered),
		ItemsFlushed:  atomic.LoadInt64(&wb.itemsFlushed),
		FlushCount:    atomic.LoadInt64(&wb.flushCount),
		FlushErrors:   atomic.LoadInt64(&wb.flushErrors),
		ItemsDropped:  atomic.LoadInt64(&wb.itemsDropped),
		HighWaterMark: atomic.LoadInt64(&wb.highWaterMark),
		MaxPending:    wb.config.MaxPending,
	}
}

// WriteBatcherStats contains statistics about batcher performance
type WriteBatcherStats struct {
	Name          string
	Pending       int
	ItemsBuffered int64
	ItemsFlushed  int64
	FlushCount    int64
	FlushErrors   int64
	ItemsDropped  int64
	HighWaterMark int64
	MaxPending    int
}
