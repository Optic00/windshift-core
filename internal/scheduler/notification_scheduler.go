package scheduler

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

// defaultBatchInterval is the production cadence: every 5 minutes the
// scheduler scans for unread notifications and emails one batch per user.
// The interval is supplied by the caller (resolved from config, which reads
// WINDSHIFT_NOTIFICATION_BATCH_INTERVAL); a non-positive value falls back here.
const defaultBatchInterval = 5 * time.Minute

// maxBatchSize caps how many notifications one email can carry. Without this,
// a user with a thousand unread notifications gets a single oversize email on
// every 5-minute tick (and many SMTP servers will reject it).
const maxBatchSize = 50

// maxConsecutiveFailures is the per-user circuit breaker. After K failed
// sends in a row, we stop trying and let the user's notifications accumulate
// until the cooldown expires, instead of re-flooding the SMTP server with
// a growing batch every tick.
const maxConsecutiveFailures = 5

// failureCooldown is how long to skip a user after hitting the failure cap.
const failureCooldown = 30 * time.Minute

// NotificationScheduler handles batching and sending of notifications every 5 minutes
type NotificationScheduler struct {
	db         database.Database
	ticker     *time.Ticker
	stopChan   chan struct{}
	mu            sync.RWMutex
	running       bool
	smtpSender    SMTPSender
	runRepo       *repository.SchedulerRunRepository
	notifSvc      *services.NotificationService
	batchInterval time.Duration

	// Per-user failure tracking for the circuit breaker. Keyed by user email
	// (matching how we fan-out batches today). Resets on restart — the goal
	// is to prevent hot-loop flooding within a process, not to persist state.
	failMu     sync.Mutex
	failCounts map[string]int
	skipUntil  map[string]time.Time
}

// SMTPSender interface for sending emails
type SMTPSender interface {
	SendBatchedNotifications(userEmail, userName string, notifications []models.Notification) error
	IsSMTPConfigured() bool
}

// NewNotificationScheduler creates a new notification scheduler. batchInterval
// is the tick cadence (resolved from config); a non-positive value falls back
// to defaultBatchInterval. notifSvc supplies the unread-notification batches so
// the scheduler never queries the notifications table directly.
func NewNotificationScheduler(db database.Database, smtpSender SMTPSender, batchInterval time.Duration, notifSvc *services.NotificationService) *NotificationScheduler {
	if batchInterval <= 0 {
		batchInterval = defaultBatchInterval
	}
	return &NotificationScheduler{
		db:            db,
		ticker:        time.NewTicker(batchInterval),
		stopChan:      make(chan struct{}),
		running:       false,
		smtpSender:    smtpSender,
		runRepo:       repository.NewSchedulerRunRepository(db),
		notifSvc:      notifSvc,
		batchInterval: batchInterval,
		failCounts:    make(map[string]int),
		skipUntil:     make(map[string]time.Time),
	}
}

// Start begins the notification batching scheduler
func (ns *NotificationScheduler) Start() {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	if ns.running {
		return
	}

	ns.running = true
	slog.Debug("Starting notification scheduler", slog.String("component", "scheduler"), slog.String("interval", ns.batchInterval.String()))

	go ns.schedulerLoop()
}

// Stop stops the notification scheduler
func (ns *NotificationScheduler) Stop() {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	if !ns.running {
		return
	}

	ns.running = false
	ns.ticker.Stop()
	close(ns.stopChan)
	slog.Debug("Notification scheduler stopped", slog.String("component", "scheduler"))
}

// schedulerLoop runs the main scheduler loop
func (ns *NotificationScheduler) schedulerLoop() {
	for {
		select {
		case <-ns.ticker.C:
			ns.processPendingNotifications()
		case <-ns.stopChan:
			return
		}
	}
}

// processPendingNotifications finds unread notifications and sends them in batches
func (ns *NotificationScheduler) processPendingNotifications() {
	start := time.Now()
	var batchesProcessed int
	var runErr error
	defer recordSchedulerRun(ns.runRepo, "notification", start, &batchesProcessed, &runErr)

	// Check if SMTP is configured first
	if !ns.smtpSender.IsSMTPConfigured() {
		slog.Debug("SMTP not configured, skipping notification batch processing", slog.String("component", "scheduler"))
		return
	}

	slog.Debug("Processing notification batches", slog.String("component", "scheduler"))

	// Get all users with unread notifications
	userBatches, err := ns.notifSvc.UnreadEmailBatches(maxBatchSize)
	if err != nil {
		slog.Error("Failed to get unread notifications", slog.String("component", "scheduler"), slog.Any("error", err))
		runErr = err
		return
	}

	if len(userBatches) == 0 {
		slog.Debug("No unread notifications to process", slog.String("component", "scheduler"))
		return
	}
	batchesProcessed = len(userBatches)

	// Send batches for each user. Track failures so the deferred recordSchedulerRun
	// can mark this tick failed when at least one batch couldn't be sent — otherwise
	// the admin Diagnostics page reports "100% success rate" while users miss email.
	failures := 0
	for userEmail, batch := range userBatches {
		if ns.inCooldown(userEmail) {
			slog.Debug("skipping user in failure cooldown",
				slog.String("component", "scheduler"),
				slog.String("user_email", userEmail),
			)
			continue
		}
		// Mark sent_at BEFORE the SMTP call. If the send succeeds, we're
		// done. If it fails, we roll back sent_at for the batch. Without
		// this pre-mark, a crash or mark-as-sent DB failure between send
		// and mark would re-send the same batch on the next tick.
		if err := ns.markNotificationsSent(batch.NotificationIDs); err != nil {
			slog.Error("failed to pre-mark notifications sent; skipping batch",
				slog.String("component", "scheduler"),
				slog.String("user_email", userEmail),
				slog.Any("error", err),
			)
			failures++
			continue
		}
		if err := ns.sendNotificationBatch(batch); err != nil {
			slog.Error("Failed to send notification batch", slog.String("component", "scheduler"), slog.String("user_email", userEmail), slog.Any("error", err))
			if rollbackErr := ns.rollbackSent(batch.NotificationIDs); rollbackErr != nil {
				slog.Error("failed to roll back sent_at after SMTP failure",
					slog.String("component", "scheduler"),
					slog.String("user_email", userEmail),
					slog.Any("error", rollbackErr),
				)
				// Surface the wedge: pre-mark left sent_at set, rollback couldn't
				// clear it, so the next tick's `WHERE sent_at IS NULL` would skip
				// these rows forever. Tagging last_send_failed makes them findable
				// (`SELECT * FROM notifications WHERE last_send_failed = true`) so
				// an operator can investigate; we deliberately don't auto-retry
				// here because we can't tell whether the SMTP send actually went
				// out before the rollback failed (would-be C3 territory).
				if markErr := ns.markSendFailed(batch.NotificationIDs); markErr != nil {
					slog.Error("failed to mark notifications as send-failed; rows are silently wedged",
						slog.String("component", "scheduler"),
						slog.String("user_email", userEmail),
						slog.Any("error", markErr),
					)
				}
			}
			ns.recordFailure(userEmail)
			failures++
			continue
		}
		ns.recordSuccess(userEmail)
	}

	if failures > 0 {
		runErr = fmt.Errorf("%d of %d notification batches failed", failures, len(userBatches))
	}

	slog.Debug("Processed notification batches", slog.String("component", "scheduler"), slog.Int("batch_count", len(userBatches)))
}

// inCooldown reports whether the user is currently being skipped because of
// prior consecutive SMTP failures.
func (ns *NotificationScheduler) inCooldown(userEmail string) bool {
	ns.failMu.Lock()
	defer ns.failMu.Unlock()
	until, ok := ns.skipUntil[userEmail]
	if !ok {
		return false
	}
	if time.Now().Before(until) {
		return true
	}
	// Cooldown expired — clear state so the user gets another shot.
	delete(ns.skipUntil, userEmail)
	delete(ns.failCounts, userEmail)
	return false
}

func (ns *NotificationScheduler) recordFailure(userEmail string) {
	ns.failMu.Lock()
	defer ns.failMu.Unlock()
	ns.failCounts[userEmail]++
	if ns.failCounts[userEmail] >= maxConsecutiveFailures {
		ns.skipUntil[userEmail] = time.Now().Add(failureCooldown)
		slog.Warn("tripping notification cooldown for user after repeated SMTP failures",
			slog.String("component", "scheduler"),
			slog.String("user_email", userEmail),
			slog.Duration("cooldown", failureCooldown),
		)
	}
}

func (ns *NotificationScheduler) recordSuccess(userEmail string) {
	ns.failMu.Lock()
	defer ns.failMu.Unlock()
	delete(ns.failCounts, userEmail)
	delete(ns.skipUntil, userEmail)
}

// sendNotificationBatch sends a batch of notifications to a user
func (ns *NotificationScheduler) sendNotificationBatch(batch *services.UserNotificationBatch) error {
	if len(batch.Notifications) == 0 {
		return nil
	}

	return ns.smtpSender.SendBatchedNotifications(batch.UserEmail, batch.UserName, batch.Notifications)
}

// markNotificationsSent stamps sent_at so the scheduler will not re-batch
// them. The function used to be named markNotificationsAsRead but it never
// touched the `read` flag — only sent_at. Callers: pre-send optimistic mark
// (with rollback on SMTP failure).
func (ns *NotificationScheduler) markNotificationsSent(notificationIDs []int) error {
	if len(notificationIDs) == 0 {
		return nil
	}

	placeholders := make([]string, len(notificationIDs))
	args := make([]interface{}, len(notificationIDs)+2)
	now := time.Now()
	args[0] = now // sent_at
	args[1] = now // updated_at

	for i, id := range notificationIDs {
		placeholders[i] = "?"
		args[i+2] = id
	}

	query := fmt.Sprintf(`
		UPDATE notifications
		SET sent_at = ?, updated_at = ?
		WHERE id IN (%s)
	`, strings.Join(placeholders, ","))

	_, err := ns.db.Exec(query, args...)
	return err
}

// markSendFailed flags a batch as wedged after a rollback failure so an operator
// can find it and decide whether to clear sent_at manually. Auto-retry isn't
// safe here because we can't tell whether the SMTP send actually went out
// before rollback failed.
func (ns *NotificationScheduler) markSendFailed(notificationIDs []int) error {
	if len(notificationIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(notificationIDs))
	args := make([]interface{}, len(notificationIDs)+1)
	args[0] = time.Now()
	for i, id := range notificationIDs {
		placeholders[i] = "?"
		args[i+1] = id
	}
	query := fmt.Sprintf(`
		UPDATE notifications
		SET last_send_failed = true, updated_at = ?
		WHERE id IN (%s)
	`, strings.Join(placeholders, ","))
	_, err := ns.db.Exec(query, args...)
	return err
}

// rollbackSent clears sent_at for a batch whose SMTP send ultimately failed,
// so the batch is eligible to retry on a future tick.
func (ns *NotificationScheduler) rollbackSent(notificationIDs []int) error {
	if len(notificationIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(notificationIDs))
	args := make([]interface{}, len(notificationIDs)+1)
	args[0] = time.Now()
	for i, id := range notificationIDs {
		placeholders[i] = "?"
		args[i+1] = id
	}
	query := fmt.Sprintf(`
		UPDATE notifications
		SET sent_at = NULL, updated_at = ?
		WHERE id IN (%s)
	`, strings.Join(placeholders, ","))
	_, err := ns.db.Exec(query, args...)
	return err
}
