package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"windshift/internal/services"
)

type ZammadSyncScheduler struct {
	service  *services.ZammadService
	interval time.Duration
	ticker   *time.Ticker
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	running  bool
}

func NewZammadSyncScheduler(service *services.ZammadService) *ZammadSyncScheduler {
	return &ZammadSyncScheduler{service: service, interval: 2 * time.Minute}
}

func (s *ZammadSyncScheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.ticker = time.NewTicker(s.interval)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.running = true
	s.wg.Add(1)
	go s.loop(ctx, s.ticker)
}

func (s *ZammadSyncScheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.ticker.Stop()
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *ZammadSyncScheduler) loop(ctx context.Context, ticker *time.Ticker) {
	defer s.wg.Done()
	for {
		select {
		case <-ticker.C:
			if err := s.service.SyncDue(ctx, 50); err != nil && ctx.Err() == nil {
				slog.Warn("Zammad ticket synchronization failed", slog.String("component", "zammad-sync"), slog.Any("error", err))
			}
		case <-ctx.Done():
			return
		}
	}
}
