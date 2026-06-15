package scheduler

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"windshift/internal/database"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/sso"
)

// TodoistSyncScheduler periodically reconciles every user who has enabled
// Todoist personal-task sync. It mirrors the other schedulers' lifecycle
// (New/Start/Stop) and drives the same TodoistSyncService the manual "Sync now"
// endpoint uses, so polling and on-demand sync share one code path.
type TodoistSyncScheduler struct {
	syncRepo    *repository.TodoistSyncRepository
	syncService *services.TodoistSyncService

	ticker   *time.Ticker
	stopChan chan struct{}
	mu       sync.Mutex
	running  bool

	interval time.Duration
}

// NewTodoistSyncScheduler creates the scheduler with a 5-minute poll interval.
func NewTodoistSyncScheduler(db database.Database, encryption *sso.SecretEncryption) *TodoistSyncScheduler {
	return &TodoistSyncScheduler{
		syncRepo:    repository.NewTodoistSyncRepository(db),
		syncService: services.NewTodoistSyncService(db, encryption),
		interval:    5 * time.Minute,
	}
}

// Start begins the poll loop. Unlike some schedulers it does NOT run immediately
// on boot — that would fan out external Todoist calls for every connected user
// at startup. The first reconciliation happens one interval in.
func (s *TodoistSyncScheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.ticker = time.NewTicker(s.interval)
	s.stopChan = make(chan struct{})
	s.running = true
	slog.Info("Starting Todoist sync scheduler (5-minute interval)")
	go s.loop(s.ticker, s.stopChan)
}

// Stop halts the poll loop.
func (s *TodoistSyncScheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	if s.ticker != nil {
		s.ticker.Stop()
		s.ticker = nil
	}
	close(s.stopChan)
	slog.Info("Todoist sync scheduler stopped")
}

func (s *TodoistSyncScheduler) loop(ticker *time.Ticker, stopChan <-chan struct{}) {
	for {
		select {
		case <-ticker.C:
			s.syncAllEnabled()
		case <-stopChan:
			return
		}
	}
}

// syncAllEnabled reconciles every enabled config. A failure for one user is
// logged and does not stop the others.
func (s *TodoistSyncScheduler) syncAllEnabled() {
	configs, err := s.syncRepo.ListEnabledConfigs()
	if err != nil {
		slog.Error("Todoist sync: failed to list enabled configs", slog.String("component", "todoist-sync"), slog.Any("error", err))
		return
	}
	for _, cfg := range configs {
		_, err := s.syncService.SyncConfig(cfg)
		switch {
		case errors.Is(err, services.ErrTodoistSyncAlreadyRunning):
			// A manual "Sync now" holds the lock; skip this tick for the config.
			slog.Debug("Todoist sync: config already running, skipping",
				slog.String("component", "todoist-sync"),
				slog.String("user_id", cfg.UserID))
		case err != nil:
			// SyncConfig already records last_error on the config row; log for ops.
			slog.Warn("Todoist sync: user reconciliation failed",
				slog.String("component", "todoist-sync"),
				slog.String("user_id", cfg.UserID),
				slog.Any("error", err))
		}
	}
}
