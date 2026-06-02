package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"windshift/internal/repository"
)

// RunnerLeaseReaper is the liveness backstop for remote agent runs
// (Initiative WI-141). A remote runner heartbeats on an interval; if it dies
// mid-run its heartbeat goes stale and its in-flight runs would otherwise
// hang in 'running' forever. On each tick this sweeper fails those runs and
// revokes the dead runner instances.
//
// It mirrors the other in-process schedulers' lifecycle: Start/Stop are wired
// into server.go alongside cfvCleanupScheduler et al.
type RunnerLeaseReaper struct {
	runs    *repository.AgentRunRepository
	runners *repository.RunnerRepository

	ticker   *time.Ticker
	stopChan chan struct{}
	mu       sync.RWMutex
	running  bool

	interval   time.Duration
	staleAfter time.Duration // a runner with no heartbeat for this long is dead
	now        func() time.Time
}

const (
	defaultReaperInterval   = 60 * time.Second
	defaultReaperStaleAfter = 90 * time.Second // ~3 missed 30s heartbeats
)

// NewRunnerLeaseReaper builds the reaper with sensible defaults. The caller
// wires Start/Stop into the server lifecycle.
func NewRunnerLeaseReaper(runs *repository.AgentRunRepository, runners *repository.RunnerRepository) *RunnerLeaseReaper {
	return &RunnerLeaseReaper{
		runs:       runs,
		runners:    runners,
		interval:   defaultReaperInterval,
		staleAfter: defaultReaperStaleAfter,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// Start begins the sweep loop. Idempotent.
func (s *RunnerLeaseReaper) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.ticker = time.NewTicker(s.interval)
	s.stopChan = make(chan struct{})
	s.running = true
	slog.Info("starting runner lease reaper", "interval", s.interval, "stale_after", s.staleAfter)
	go s.loop(s.ticker, s.stopChan)
}

// Stop halts the sweep loop. Idempotent.
func (s *RunnerLeaseReaper) Stop() {
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
	slog.Info("runner lease reaper stopped")
}

func (s *RunnerLeaseReaper) loop(ticker *time.Ticker, stop chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *RunnerLeaseReaper) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reaped, revoked, err := s.Sweep(ctx)
	if err != nil {
		slog.Error("runner lease reaper sweep", "error", err)
		return
	}
	if reaped > 0 || revoked > 0 {
		slog.Info("runner lease reaper swept", "reaped_runs", reaped, "revoked_instances", revoked)
	}
}

// Sweep runs one reap pass: fail runs of stale runners, then revoke those
// runners. Exported for testing. Returns the counts.
func (s *RunnerLeaseReaper) Sweep(ctx context.Context) (reapedRuns, revokedInstances int, err error) {
	now := s.now()
	staleBefore := now.Add(-s.staleAfter)
	reapedRuns, err = s.runs.ReapStaleRuns(ctx, staleBefore, now)
	if err != nil {
		return reapedRuns, 0, err
	}
	revokedInstances, err = s.runners.RevokeStaleInstances(ctx, staleBefore, now)
	return reapedRuns, revokedInstances, err
}
