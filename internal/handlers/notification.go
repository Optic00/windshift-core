package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"windshift/internal/cacheutil"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"

	"github.com/allegro/bigcache/v3"
)

// notificationWrite represents a read-state update queued for DB persistence.
// New notifications are inserted synchronously in AddNotification so the
// cache and the API response carry the real DB-assigned id; only read /
// updated_at changes flow through the WriteBatcher.
type notificationWrite struct {
	Notification models.Notification
}

// NotificationManagerConfig holds tuning parameters for the notification manager.
type NotificationManagerConfig struct {
	FlushInterval time.Duration // WriteBatcher flush interval (default: 30s)
	MaxBatchSize  int           // WriteBatcher max batch size (default: 50)
	SyncInterval  time.Duration // Periodic consistency check interval (default: 2min)
}

// DefaultNotificationManagerConfig returns a config with sensible defaults.
func DefaultNotificationManagerConfig() NotificationManagerConfig {
	return NotificationManagerConfig{
		FlushInterval: 30 * time.Second,
		MaxBatchSize:  50,
		SyncInterval:  2 * time.Minute,
	}
}

// NotificationManager handles notification caching and persistence
type NotificationManager struct {
	cache      *bigcache.BigCache
	db         database.Database
	batcher    *services.WriteBatcher[notificationWrite]
	syncTicker *time.Ticker
	stopChan   chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
	mu         sync.RWMutex

	// pushDispatcher, when set, receives every newly-created notification for
	// fan-out to Web Push. Read under mu; set once at startup via SetPushDispatcher.
	pushDispatcher PushDispatcher
}

// SetPushDispatcher wires the Web Push dispatcher. Safe to call once at startup
// before the manager handles traffic; nil leaves push disabled.
func (nm *NotificationManager) SetPushDispatcher(d PushDispatcher) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.pushDispatcher = d
}

// NotificationService interface for cache management
type NotificationService interface {
	ForceRefreshCache() error
}

// PushDispatcher delivers a freshly-created notification to the user's
// registered Web Push subscriptions. Implemented by services.PushService.
// Kept as an interface here so the notification manager stays decoupled from
// the push transport (and so push is a no-op when none is wired).
type PushDispatcher interface {
	Dispatch(notification models.Notification)
}

// NotificationHandler handles HTTP requests for notifications
type NotificationHandler struct {
	manager *NotificationManager
	service NotificationService
}

// NewNotificationManager creates a new notification manager with BigCache
func NewNotificationManager(db database.Database, nmCfg NotificationManagerConfig) (*NotificationManager, error) {
	cacheConfig := cacheutil.NewBigCacheConfig(cacheutil.BigCacheOptions{
		TTL:          24 * time.Hour,
		MaxCacheMB:   512,
		MaxEntrySize: 1024, // 1KB per entry
	})

	cache, err := bigcache.New(context.Background(), cacheConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create BigCache: %w", err)
	}

	manager := &NotificationManager{
		cache:      cache,
		db:         db,
		syncTicker: time.NewTicker(nmCfg.SyncInterval),
		stopChan:   make(chan struct{}),
	}

	batcherConfig := services.WriteBatcherConfig{
		FlushInterval: nmCfg.FlushInterval,
		MaxBatchSize:  nmCfg.MaxBatchSize,
		Name:          "notifications",
	}
	manager.batcher = services.NewWriteBatcher(batcherConfig, manager.flushNotificationBatch)
	manager.batcher.Start()

	// Start periodic sync goroutine (consistency check, reconciles temp IDs)
	manager.wg.Add(1)
	go manager.periodicSync()

	return manager, nil
}

// NewNotificationHandler creates a new notification handler
func NewNotificationHandler(manager *NotificationManager, service NotificationService) *NotificationHandler {
	return &NotificationHandler{
		manager: manager,
		service: service,
	}
}

// getCacheKey generates a cache key for a user's notifications
func (nm *NotificationManager) getCacheKey(userID int) string {
	return fmt.Sprintf("user:%d:notifications", userID)
}

// GetUserNotifications retrieves notifications for a user (cache-first).
// Bughunt #10: this method used to hold nm.mu.RLock for the entire DB load
// path, so a slow read for one user stalled AddNotification / MarkAsRead
// for every user (those need the write lock). BigCache is internally
// thread-safe, so cache lookups don't need our mutex; we only acquire the
// manager lock briefly to write the cache after a DB load completes.
func (nm *NotificationManager) GetUserNotifications(userID, limit, offset int) ([]models.Notification, error) {
	cacheKey := nm.getCacheKey(userID)

	if entry, err := nm.cache.Get(cacheKey); err == nil {
		var cache models.NotificationCache
		if err := json.Unmarshal(entry, &cache); err == nil {
			start := offset
			end := offset + limit
			if start > len(cache.Notifications) {
				return []models.Notification{}, nil
			}
			if end > len(cache.Notifications) {
				end = len(cache.Notifications)
			}
			return cache.Notifications[start:end], nil
		}
	}

	return nm.loadNotificationsFromDB(userID, limit, offset)
}

// AddNotification persists a new notification synchronously and writes it to
// the per-user cache. Returns the notification with the real, DB-assigned id
// populated so callers (API handlers, mark-as-read flows) can reference it.
//
// INSERTs used to queue through the WriteBatcher with a temp id derived from
// UnixNano(), which produced two problems: (1) the cached row never learned
// its real id after the batch flushed, so MarkAsRead silently no-op'd
// against the temp id; and (2) POST /notifications responded with id: 0
// because AddNotification took the value by copy. Synchronous INSERT removes
// both. The batcher is still used for read-state UPDATEs.
func (nm *NotificationManager) AddNotification(notification models.Notification) (models.Notification, error) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	slog.Debug("adding notification", slog.String("component", "notifications"), slog.Int("user_id", notification.UserID), slog.String("type", notification.Type), slog.String("title", notification.Title))

	now := time.Now()
	notification.CreatedAt = now
	notification.UpdatedAt = now
	if notification.Timestamp.IsZero() {
		notification.Timestamp = now
	}

	err := nm.db.QueryRow(`
		INSERT INTO notifications (user_id, title, message, type, timestamp, read, avatar, action_url, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id
	`, notification.UserID, notification.Title, notification.Message, notification.Type, notification.Timestamp, notification.Read,
		nullableString(notification.Avatar), nullableString(notification.ActionURL),
		nullableString(notification.Metadata), notification.CreatedAt, notification.UpdatedAt).Scan(&notification.ID)
	if err != nil {
		return notification, fmt.Errorf("insert notification: %w", err)
	}

	cacheKey := nm.getCacheKey(notification.UserID)
	var cache models.NotificationCache
	if entry, cerr := nm.cache.Get(cacheKey); cerr == nil {
		_ = json.Unmarshal(entry, &cache)
	} else {
		cache = models.NotificationCache{
			Notifications: []models.Notification{},
			LastSynced:    now,
			IsDirty:       false,
		}
	}

	cache.Notifications = append([]models.Notification{notification}, cache.Notifications...)
	if len(cache.Notifications) > 1000 {
		cache.Notifications = cache.Notifications[:1000]
	}

	cacheData, _ := json.Marshal(cache)
	if cerr := nm.cache.Set(cacheKey, cacheData); cerr != nil {
		slog.Error("failed to set notification cache", slog.String("component", "notifications"), slog.Int("user_id", notification.UserID), slog.Any("error", cerr))
		return notification, cerr
	}

	slog.Debug("successfully added notification", slog.String("component", "notifications"), slog.Int("notification_id", notification.ID), slog.Int("user_id", notification.UserID), slog.Int("cache_size", len(cache.Notifications)))

	// Fan out to Web Push asynchronously — never block the notification write
	// on a remote push service. The dispatcher operates on its own copy.
	if nm.pushDispatcher != nil {
		go nm.pushDispatcher.Dispatch(notification)
	}
	return notification, nil
}

// MarkAsRead marks a notification as read
func (nm *NotificationManager) MarkAsRead(userID, notificationID int) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	cacheKey := nm.getCacheKey(userID)

	// Get existing cache
	var cache models.NotificationCache
	if entry, err := nm.cache.Get(cacheKey); err == nil {
		if err := json.Unmarshal(entry, &cache); err != nil {
			return err
		}
	} else {
		// Load from database if not in cache
		notifications, err := nm.loadNotificationsFromDB(userID, 1000, 0)
		if err != nil {
			return err
		}
		cache = models.NotificationCache{
			Notifications: notifications,
			LastSynced:    time.Now(),
			IsDirty:       false,
		}
	}

	// Find and update notification
	for i := range cache.Notifications {
		if cache.Notifications[i].ID != notificationID {
			continue
		}

		cache.Notifications[i].Read = true
		cache.Notifications[i].UpdatedAt = time.Now()
		cache.IsDirty = true

		// Queue read-status update for DB persistence. All cached rows now
		// carry their real DB id (sync INSERT in AddNotification), so the
		// temp-id guard that used to bracket this call is gone.
		nm.batcher.Add(notificationWrite{
			Notification: cache.Notifications[i],
		})
		break
	}

	// Update cache
	cacheData, _ := json.Marshal(cache)
	return nm.cache.Set(cacheKey, cacheData)
}

// MarkItemNotificationsAsRead marks cached unread notifications pointing at the
// given item as read. Notifications carry their item deep link in action_url
// (e.g. "/workspaces/<ws>/items/<itemID>"). This method intentionally follows
// the same cache-first/batched persistence path as MarkAsRead instead of doing a
// synchronous table-wide UPDATE on every item view; SQLite writes stay coalesced
// by the notification WriteBatcher / periodic sync.
func (nm *NotificationManager) MarkItemNotificationsAsRead(userID, itemID int) error {
	if itemID <= 0 {
		return nil
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()

	cacheKey := nm.getCacheKey(userID)
	var cache models.NotificationCache
	if entry, err := nm.cache.Get(cacheKey); err == nil {
		if err := json.Unmarshal(entry, &cache); err != nil {
			return err
		}
	} else {
		notifications, err := nm.loadNotificationsFromDB(userID, 1000, 0)
		if err != nil {
			return err
		}
		cache = models.NotificationCache{
			Notifications: notifications,
			LastSynced:    time.Now(),
			IsDirty:       false,
		}
	}

	now := time.Now()
	changed := false
	for i := range cache.Notifications {
		if cache.Notifications[i].Read || !actionURLPointsToItem(cache.Notifications[i].ActionURL, itemID) {
			continue
		}
		cache.Notifications[i].Read = true
		cache.Notifications[i].UpdatedAt = now
		cache.IsDirty = true
		changed = true

		nm.batcher.Add(notificationWrite{
			Notification: cache.Notifications[i],
		})
	}
	if !changed {
		return nil
	}

	cacheData, _ := json.Marshal(cache)
	return nm.cache.Set(cacheKey, cacheData)
}

// actionURLPointsToItem reports whether an action_url references the given
// item id via the "/items/<id>" route segment. Kept in lockstep with the
// actionUrl.js client-side matcher and the SQL LIKE above.
func actionURLPointsToItem(actionURL string, itemID int) bool {
	if actionURL == "" {
		return false
	}
	// "/items/<id>" followed by end-of-string, a separator, or a fragment.
	m := strings.Index(actionURL, "/items/")
	if m < 0 {
		return false
	}
	rest := actionURL[m+len("/items/"):]
	// Read leading digits; the segment must be exactly the numeric item id.
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return false
	}
	if end < len(rest) {
		switch rest[end] {
		case '/', '?', '#':
			// valid route boundary
		default:
			return false
		}
	}
	id, err := strconv.Atoi(rest[:end])
	if err != nil {
		return false
	}
	return id == itemID
}

// MarkAllAsSeen stamps seen_at on every unseen notification for the user.
// Distinct from MarkAllAsRead: "seen" reflects passive tray viewing and is
// safe to fire on an auto-timer, because the email batch scheduler keys off
// `read = false` and not seen_at. The DB write is synchronous (one UPDATE)
// because there is no batched "set seen_at" path through WriteBatcher.
func (nm *NotificationManager) MarkAllAsSeen(userID int) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	now := time.Now()
	if _, err := nm.db.ExecWrite(`
		UPDATE notifications
		SET seen_at = ?, updated_at = ?
		WHERE user_id = ? AND seen_at IS NULL
	`, now, now, userID); err != nil {
		return fmt.Errorf("mark notifications seen: %w", err)
	}

	cacheKey := nm.getCacheKey(userID)
	if entry, err := nm.cache.Get(cacheKey); err == nil {
		var cache models.NotificationCache
		if err := json.Unmarshal(entry, &cache); err == nil {
			seenAt := now
			for i := range cache.Notifications {
				if cache.Notifications[i].SeenAt == nil {
					cache.Notifications[i].SeenAt = &seenAt
					cache.Notifications[i].UpdatedAt = now
				}
			}
			cacheData, _ := json.Marshal(cache)
			_ = nm.cache.Set(cacheKey, cacheData)
		}
	}
	return nil
}

// DeleteUserNotifications removes all notification rows for a user and drops
// the user's tray cache. Offboarding uses this instead of deleting directly so
// cached entries cannot survive after the DB rows are gone.
func (nm *NotificationManager) DeleteUserNotifications(userID int) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if _, err := nm.db.ExecWrite(`DELETE FROM notifications WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete user notifications: %w", err)
	}
	_ = nm.cache.Delete(nm.getCacheKey(userID))
	return nil
}

// MarkNotificationsSent stamps sent_at so the email scheduler will not re-batch
// these rows after a successful or in-flight SMTP send.
func (nm *NotificationManager) MarkNotificationsSent(notificationIDs []int) error {
	if len(notificationIDs) == 0 {
		return nil
	}
	now := time.Now()
	return nm.updateNotificationsByID(notificationIDs, `sent_at = ?, updated_at = ?`, now, now)
}

// MarkNotificationsSendFailed flags rows whose SMTP-send rollback failed so an
// operator can find and repair the wedged notifications.
func (nm *NotificationManager) MarkNotificationsSendFailed(notificationIDs []int) error {
	if len(notificationIDs) == 0 {
		return nil
	}
	return nm.updateNotificationsByID(notificationIDs, `last_send_failed = true, updated_at = ?`, time.Now())
}

// RollbackNotificationsSent clears sent_at after an SMTP send failure so the
// rows become eligible for retry on a future scheduler tick.
func (nm *NotificationManager) RollbackNotificationsSent(notificationIDs []int) error {
	if len(notificationIDs) == 0 {
		return nil
	}
	return nm.updateNotificationsByID(notificationIDs, `sent_at = NULL, updated_at = ?`, time.Now())
}

func (nm *NotificationManager) updateNotificationsByID(notificationIDs []int, setClause string, args ...interface{}) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	placeholders := make([]string, len(notificationIDs))
	for i, id := range notificationIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := fmt.Sprintf(`UPDATE notifications SET %s WHERE id IN (%s)`, setClause, strings.Join(placeholders, ","))
	if _, err := nm.db.ExecWrite(query, args...); err != nil {
		return fmt.Errorf("update notifications: %w", err)
	}
	return nil
}

// notificationTrayRetention bounds how far back the notification tray scrolls.
// Older notifications stay in the DB (audit) but are hidden from the list view.
const notificationTrayRetention = 10 * 24 * time.Hour

// loadNotificationsFromDB loads notifications from database and updates cache
func (nm *NotificationManager) loadNotificationsFromDB(userID, limit, offset int) ([]models.Notification, error) {
	query := `
		SELECT id, user_id, title, message, type, timestamp, read, seen_at, avatar, action_url, metadata, created_at, updated_at
		FROM notifications
		WHERE user_id = ? AND timestamp >= ?
		ORDER BY timestamp DESC
		LIMIT ? OFFSET ?
	`

	cutoff := time.Now().Add(-notificationTrayRetention)
	rows, err := nm.db.Query(query, userID, cutoff, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var notifications []models.Notification
	for rows.Next() {
		var n models.Notification
		var avatar, actionURL, metadata *string

		err := rows.Scan(
			&n.ID, &n.UserID, &n.Title, &n.Message, &n.Type,
			&n.Timestamp, &n.Read, &n.SeenAt, &avatar, &actionURL, &metadata,
			&n.CreatedAt, &n.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if avatar != nil {
			n.Avatar = *avatar
		}
		if actionURL != nil {
			n.ActionURL = *actionURL
		}
		if metadata != nil {
			n.Metadata = *metadata
		}

		notifications = append(notifications, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Update cache with loaded data (only if we got the full first page)
	if offset == 0 && len(notifications) > 0 {
		cache := models.NotificationCache{
			Notifications: notifications,
			LastSynced:    time.Now(),
			IsDirty:       false,
		}
		cacheData, _ := json.Marshal(cache)
		cacheKey := nm.getCacheKey(userID)
		_ = nm.cache.Set(cacheKey, cacheData)
	}

	return notifications, nil
}

// periodicSync runs periodically to sync any remaining dirty cache entries to database (consistency check)
func (nm *NotificationManager) periodicSync() {
	defer nm.wg.Done()
	for {
		select {
		case <-nm.syncTicker.C:
			nm.syncCacheToDatabase()
		case <-nm.stopChan:
			return
		}
	}
}

// syncCacheToDatabase syncs all dirty cache entries to the database
func (nm *NotificationManager) syncCacheToDatabase() {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	slog.Debug("starting periodic notification sync to database", slog.String("component", "notifications"))

	// Get all cache keys (this is a simplified approach - in production you'd want to track dirty keys)
	iterator := nm.cache.Iterator()
	for iterator.SetNext() {
		info, err := iterator.Value()
		if err != nil {
			continue
		}

		var cache models.NotificationCache
		if err = json.Unmarshal(info.Value(), &cache); err != nil {
			continue
		}

		if !cache.IsDirty {
			continue
		}

		// Extract user ID from key
		key := info.Key()
		parts := strings.Split(key, ":")
		if len(parts) < 2 {
			continue
		}
		userID, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}

		// Sync notifications to database
		if err := nm.syncUserNotifications(userID, cache.Notifications); err != nil {
			slog.Error("failed to sync notifications", slog.String("component", "notifications"), slog.Int("user_id", userID), slog.Any("error", err))
			continue
		}

		// Mark cache as clean
		cache.IsDirty = false
		cache.LastSynced = time.Now()
		cacheData, _ := json.Marshal(cache)
		_ = nm.cache.Set(key, cacheData)
	}

	slog.Debug("completed periodic notification sync to database", slog.String("component", "notifications"))
}

// syncUserNotifications propagates cached read/updated_at changes to the
// database in a single transaction. Every cached row has a real DB id since
// AddNotification now inserts synchronously, so the old temp-id skip is
// gone; INSERTs are no longer this function's responsibility.
func (nm *NotificationManager) syncUserNotifications(_ int, notifications []models.Notification) error {
	tx, err := nm.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, notification := range notifications {
		if _, err := tx.Exec(`
			UPDATE notifications
			SET read = ?, updated_at = ?
			WHERE id = ? AND user_id = ?
		`, notification.Read, notification.UpdatedAt, notification.ID, notification.UserID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// flushNotificationBatch persists batched read-state updates to the database.
// Called by WriteBatcher every 30s or when 50 items are queued. INSERTs run
// synchronously in AddNotification so they aren't routed here anymore.
func (nm *NotificationManager) flushNotificationBatch(items []notificationWrite) error {
	tx, err := nm.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, item := range items {
		n := item.Notification
		if _, err := tx.Exec(`
			UPDATE notifications SET read = ?, updated_at = ? WHERE id = ? AND user_id = ?
		`, n.Read, n.UpdatedAt, n.ID, n.UserID); err != nil {
			return fmt.Errorf("update notification: %w", err)
		}
	}

	return tx.Commit()
}

// Stop stops the notification manager
func (nm *NotificationManager) Stop() {
	nm.stopOnce.Do(func() {
		nm.syncTicker.Stop()
		close(nm.stopChan)
		nm.wg.Wait()
		nm.batcher.Stop()        // Flush remaining batched writes
		nm.syncCacheToDatabase() // Final consistency sync
		_ = nm.cache.Close()
	})
}

// HTTP Handlers

// GetNotifications handles GET /api/notifications
func (nh *NotificationHandler) GetNotifications(w http.ResponseWriter, r *http.Request) {
	// Get authenticated user from context
	user := utils.GetCurrentUser(r)
	if user == nil {
		slog.Debug("no authenticated user in context", slog.String("component", "notifications"))
		respondUnauthorized(w, r)
		return
	}
	userID := user.ID

	// Parse pagination parameters
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50 // Default limit
	offset := 0 // Default offset

	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	notifications, err := nh.manager.GetUserNotifications(userID, limit, offset)
	if err != nil {
		slog.Error("failed to get notifications", slog.String("component", "notifications"), slog.Int("user_id", userID), slog.Any("error", err))
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, notifications)
}

// CreateNotification handles POST /api/notifications.
// The endpoint can only mint a notification for the authenticated user — the
// request's user_id is ignored. Server-side notifications go through
// NotificationService.EmitEvent; this handler exists only so a user can push
// their own ad-hoc reminders into the tray.
func (nh *NotificationHandler) CreateNotification(w http.ResponseWriter, r *http.Request) {
	user := utils.GetCurrentUser(r)
	if user == nil {
		respondUnauthorized(w, r)
		return
	}

	notification, ok := decodeJSON[models.Notification](w, r)
	if !ok {
		return
	}
	sanitize.ApplyAll(
		sanitize.Pair{Target: &notification.Title, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &notification.Message, Policy: sanitize.PlainTextField},
		sanitize.Pair{Target: &notification.Type, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &notification.Avatar, Policy: sanitize.ShortIdentifier},
		sanitize.Pair{Target: &notification.ActionURL, Policy: sanitize.PlainTextField},
	)
	// Metadata is a JSON blob that is never decoded server-side, so this
	// handler is the only bounding point — HTML stripping would corrupt
	// valid payloads, so it is size-capped and required to be well-formed
	// JSON instead, with invalid payloads rejected.
	if err := sanitize.ValidateJSONPayload("metadata", notification.Metadata); err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	// Force caller identity — never trust the request body's user_id.
	notification.UserID = user.ID

	if notification.Timestamp.IsZero() {
		notification.Timestamp = time.Now()
	}

	stored, err := nh.manager.AddNotification(notification)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONCreated(w, stored)
}

// MarkNotificationAsRead handles PATCH /api/notifications/{id}/read
func (nh *NotificationHandler) MarkNotificationAsRead(w http.ResponseWriter, r *http.Request) {
	// Get authenticated user from context
	user := utils.GetCurrentUser(r)
	if user == nil {
		slog.Debug("no authenticated user in context", slog.String("component", "notifications"))
		respondUnauthorized(w, r)
		return
	}
	userID := user.ID

	idStr := r.PathValue("id")

	id, err := strconv.Atoi(idStr)
	if err != nil {
		respondInvalidID(w, r, "notification ID")
		return
	}

	slog.Debug("marking notification as read", slog.String("component", "notifications"), slog.Int("user_id", userID), slog.String("username", user.Username), slog.Int("notification_id", id))

	if err := nh.manager.MarkAsRead(userID, id); err != nil {
		slog.Error("failed to mark notification as read", slog.String("component", "notifications"), slog.Int("notification_id", id), slog.Int("user_id", userID), slog.Any("error", err))
		respondInternalError(w, r, err)
		return
	}

	slog.Debug("successfully marked notification as read", slog.String("component", "notifications"), slog.Int("notification_id", id), slog.Int("user_id", userID))
	w.WriteHeader(http.StatusOK)
}

// MarkAllNotificationsAsSeen handles PATCH /api/notifications/seen-all.
// Bughunt #11: separate the tray's "I looked at it" signal from "I
// acknowledge this", so an auto-timer firing 5 s after the tray opens no
// longer suppresses email batches (the scheduler keys off read = false).
func (nh *NotificationHandler) MarkAllNotificationsAsSeen(w http.ResponseWriter, r *http.Request) {
	user := utils.GetCurrentUser(r)
	if user == nil {
		respondUnauthorized(w, r)
		return
	}

	if err := nh.manager.MarkAllAsSeen(user.ID); err != nil {
		slog.Error("failed to mark all notifications as seen", slog.String("component", "notifications"), slog.Int("user_id", user.ID), slog.Any("error", err))
		respondInternalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// MarkItemNotificationsAsRead handles POST /api/notifications/mark-item-read.
// Marks every unread notification pointing at the given item as read. Viewing
// an item should clear its notifications regardless of entry point (deep link,
// PWA, navigation) — not only when opened from the notification tray.
func (nh *NotificationHandler) MarkItemNotificationsAsRead(w http.ResponseWriter, r *http.Request) {
	user := utils.GetCurrentUser(r)
	if user == nil {
		respondUnauthorized(w, r)
		return
	}

	payload, ok := decodeJSON[struct {
		ItemID int `json:"item_id"`
	}](w, r)
	if !ok {
		return
	}
	if payload.ItemID <= 0 {
		respondValidationError(w, r, "item_id must be a positive integer")
		return
	}

	if err := nh.manager.MarkItemNotificationsAsRead(user.ID, payload.ItemID); err != nil {
		slog.Error("failed to mark item notifications as read", slog.String("component", "notifications"), slog.Int("user_id", user.ID), slog.Int("item_id", payload.ItemID), slog.Any("error", err))
		respondInternalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// RefreshCache handles POST /api/notifications/refresh-cache (admin only)
func (nh *NotificationHandler) RefreshCache(w http.ResponseWriter, r *http.Request) {
	slog.Debug("admin requested manual cache refresh", slog.String("component", "notifications"))

	if nh.service == nil {
		slog.Warn("notification service not available", slog.String("component", "notifications"))
		respondInternalError(w, r, fmt.Errorf("notification service not available"))
		return
	}

	if err := nh.service.ForceRefreshCache(); err != nil {
		slog.Error("failed to refresh cache", slog.String("component", "notifications"), slog.Any("error", err))
		respondInternalError(w, r, err)
		return
	}

	slog.Debug("cache refreshed successfully", slog.String("component", "notifications"))
	respondJSONOK(w, map[string]string{
		"message": "Notification cache refreshed successfully",
	})
}
