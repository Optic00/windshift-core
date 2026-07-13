package webhook

import (
	"context"
	"testing"
	"time"

	"windshift/internal/models"
)

func TestDispatchEventBoundsQueueAndWorkers(t *testing.T) {
	dispatchCtx, dispatchCancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	w := &WebhookSender{
		dispatchCtx:    dispatchCtx,
		dispatchCancel: dispatchCancel,
		dispatchQueue:  make(chan dispatchJob, 2),
		accepting:      true,
		dispatchJobFn: func(dispatchJob) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
		},
	}
	w.startDispatchWorkers(1)

	item := &models.Item{ID: 42}
	w.DispatchEvent("item.updated", item)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dispatch worker did not start")
	}

	w.DispatchEvent("item.updated", item)
	w.DispatchEvent("item.updated", item)
	w.DispatchEvent("item.updated", item)

	stats := w.Stats()
	if stats.ActiveWorkers != 1 {
		t.Fatalf("active workers = %d, want 1", stats.ActiveWorkers)
	}
	if stats.QueueDepth != 2 || stats.QueueCapacity != 2 {
		t.Fatalf("queue = %d/%d, want 2/2", stats.QueueDepth, stats.QueueCapacity)
	}
	if stats.Enqueued != 3 || stats.Rejected != 1 {
		t.Fatalf("admission counters = %d enqueued, %d rejected; want 3 and 1", stats.Enqueued, stats.Rejected)
	}

	close(release)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	w.DispatchEvent("item.updated", item)
	if rejected := w.Stats().Rejected; rejected != 2 {
		t.Fatalf("rejected after shutdown = %d, want 2", rejected)
	}
}
