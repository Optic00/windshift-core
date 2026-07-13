package services

import (
	"errors"
	"testing"
	"time"
)

func TestWriteBatcherBoundsPendingWorkAcrossFlushFailure(t *testing.T) {
	fail := true
	wb := NewWriteBatcher(WriteBatcherConfig{
		FlushInterval: time.Hour,
		MaxBatchSize:  100,
		MaxPending:    3,
		Name:          "test",
	}, func([]int) error {
		if fail {
			return errors.New("database unavailable")
		}
		return nil
	})

	for i := 0; i < 3; i++ {
		if !wb.Add(i) {
			t.Fatalf("item %d unexpectedly rejected", i)
		}
	}
	if wb.Add(4) {
		t.Fatal("item beyond MaxPending was accepted")
	}
	if err := wb.Flush(); err == nil {
		t.Fatal("failed flush returned nil")
	}

	stats := wb.Stats()
	if stats.Pending != 3 || stats.HighWaterMark != 3 || stats.ItemsDropped != 1 {
		t.Fatalf("stats after failure = %+v", stats)
	}

	fail = false
	if err := wb.Flush(); err != nil {
		t.Fatalf("recovery flush: %v", err)
	}
	if stats := wb.Stats(); stats.Pending != 0 || stats.ItemsFlushed != 3 {
		t.Fatalf("stats after recovery = %+v", stats)
	}
}
