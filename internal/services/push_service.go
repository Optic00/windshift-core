package services

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"windshift/internal/config"
	"windshift/internal/database"
	"windshift/internal/models"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// pushTTLSeconds is how long a push service should retain an undelivered
// message. One day matches the notification cache horizon.
const pushTTLSeconds = 86400

// maxPushBodyLen caps the notification snippet placed in the push payload. Push
// payloads intentionally carry only IDs + a short title/body + a target URL; the
// app fetches full content after opening (see plan security note).
const maxPushBodyLen = 140

// PushSubscriptionInfo is the non-sensitive view of a stored subscription
// returned to the owning user (keys are never exposed back to the client).
type PushSubscriptionInfo struct {
	ID         int        `json:"id"`
	Endpoint   string     `json:"endpoint"`
	UserAgent  string     `json:"user_agent"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// pushPayload is the compact JSON delivered to the service worker's push handler.
type pushPayload struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Type  string `json:"type"`
	URL   string `json:"url"`
}

// PushService stores per-user Web Push subscriptions and dispatches compact
// push messages when notifications are created. It is a no-op when VAPID keys
// are not configured (Enabled() == false).
type PushService struct {
	db  database.Database
	cfg config.PushConfig
}

// NewPushService constructs a PushService. The returned service is safe to use
// even when push is disabled — every method degrades to a no-op / empty result.
func NewPushService(db database.Database, cfg config.PushConfig) *PushService {
	return &PushService{db: db, cfg: cfg}
}

// Enabled reports whether Web Push is configured.
func (s *PushService) Enabled() bool { return s.cfg.Enabled() }

// PublicKey returns the VAPID public key the browser needs to subscribe.
func (s *PushService) PublicKey() string { return s.cfg.VAPIDPublicKey }

// Subscribe upserts a subscription for the user, keyed by endpoint. Re-subscribing
// an existing endpoint (e.g. after key rotation) refreshes its keys, reattaches
// it to the current user, and clears any revoked marker.
func (s *PushService) Subscribe(userID int, endpoint, authKey, p256dhKey, userAgent string) error {
	_, err := s.db.Exec(`
		INSERT INTO push_subscriptions (user_id, endpoint, auth_key, p256dh_key, user_agent, created_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT (endpoint) DO UPDATE SET
			user_id = excluded.user_id,
			auth_key = excluded.auth_key,
			p256dh_key = excluded.p256dh_key,
			user_agent = excluded.user_agent,
			revoked_at = NULL,
			last_used_at = CURRENT_TIMESTAMP
	`, userID, endpoint, authKey, p256dhKey, userAgent)
	return err
}

// List returns the user's active (non-revoked) subscriptions, newest first.
func (s *PushService) List(userID int) ([]PushSubscriptionInfo, error) {
	rows, err := s.db.Query(`
		SELECT id, endpoint, user_agent, created_at, last_used_at
		FROM push_subscriptions
		WHERE user_id = ? AND revoked_at IS NULL
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PushSubscriptionInfo{}
	for rows.Next() {
		var info PushSubscriptionInfo
		var lastUsed sql.NullTime
		if err := rows.Scan(&info.ID, &info.Endpoint, &info.UserAgent, &info.CreatedAt, &lastUsed); err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			t := lastUsed.Time
			info.LastUsedAt = &t
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// Delete removes one of the user's subscriptions by id. The user_id predicate
// enforces ownership — a user can never delete another user's subscription.
func (s *PushService) Delete(userID, id int) error {
	_, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// SendTest sends a test push to every active subscription of the user.
func (s *PushService) SendTest(userID int) error {
	s.deliver(userID, pushPayload{
		Title: "Windshift",
		Body:  "Push notifications are working.",
		Type:  "info",
		URL:   "/m/notifications",
	})
	return nil
}

// Dispatch fans a created notification out to the recipient's subscriptions.
// Intended to be called in a goroutine; it recovers from panics so a transport
// failure can never take down the caller.
func (s *PushService) Dispatch(notification models.Notification) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("push dispatch panicked", slog.String("component", "push"), slog.Any("recover", r))
		}
	}()

	if !s.Enabled() {
		return
	}

	body := notification.Message
	if body == "" {
		body = notification.Title
	}
	if len(body) > maxPushBodyLen {
		body = body[:maxPushBodyLen]
	}
	url := notification.ActionURL
	if url == "" {
		url = "/m/notifications"
	}

	s.deliver(notification.UserID, pushPayload{
		ID:    notification.ID,
		Title: notification.Title,
		Body:  body,
		Type:  notification.Type,
		URL:   url,
	})
}

// deliver loads the user's active subscriptions and sends one push each,
// pruning subscriptions the push service reports as permanently gone.
func (s *PushService) deliver(userID int, payload pushPayload) {
	if !s.Enabled() {
		return
	}

	rows, err := s.db.Query(`
		SELECT id, endpoint, auth_key, p256dh_key
		FROM push_subscriptions
		WHERE user_id = ? AND revoked_at IS NULL
	`, userID)
	if err != nil {
		slog.Error("push: load subscriptions failed", slog.String("component", "push"), slog.Any("error", err))
		return
	}

	type sub struct {
		id       int
		endpoint string
		auth     string
		p256dh   string
	}
	var subs []sub
	for rows.Next() {
		var sb sub
		if err := rows.Scan(&sb.id, &sb.endpoint, &sb.auth, &sb.p256dh); err != nil {
			slog.Error("push: scan subscription failed", slog.String("component", "push"), slog.Any("error", err))
			continue
		}
		subs = append(subs, sb)
	}
	if err := rows.Err(); err != nil {
		slog.Error("push: iterate subscriptions failed", slog.String("component", "push"), slog.Any("error", err))
	}
	rows.Close()

	if len(subs) == 0 {
		return
	}

	message, err := json.Marshal(payload)
	if err != nil {
		slog.Error("push: marshal payload failed", slog.String("component", "push"), slog.Any("error", err))
		return
	}

	options := &webpush.Options{
		Subscriber:      s.cfg.VAPIDSubject,
		VAPIDPublicKey:  s.cfg.VAPIDPublicKey,
		VAPIDPrivateKey: s.cfg.VAPIDPrivateKey,
		TTL:             pushTTLSeconds,
	}

	for _, sb := range subs {
		resp, err := webpush.SendNotification(message, &webpush.Subscription{
			Endpoint: sb.endpoint,
			Keys:     webpush.Keys{Auth: sb.auth, P256dh: sb.p256dh},
		}, options)
		if err != nil {
			slog.Warn("push: send failed", slog.String("component", "push"), slog.Int("sub_id", sb.id), slog.Any("error", err))
			continue
		}
		status := resp.StatusCode
		resp.Body.Close()

		// 404/410 mean the endpoint is permanently gone — prune it. Other
		// non-2xx are transient; mark only last_used_at via successes below.
		if status == http.StatusNotFound || status == http.StatusGone {
			if _, derr := s.db.Exec(`UPDATE push_subscriptions SET revoked_at = CURRENT_TIMESTAMP WHERE id = ?`, sb.id); derr != nil {
				slog.Error("push: prune failed", slog.String("component", "push"), slog.Int("sub_id", sb.id), slog.Any("error", derr))
			}
			continue
		}
		if status >= 200 && status < 300 {
			_, _ = s.db.Exec(`UPDATE push_subscriptions SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, sb.id)
		}
	}
}
