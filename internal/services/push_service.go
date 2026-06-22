package services

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"windshift/internal/config"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// system_settings keys under which an auto-provisioned VAPID keypair is
// persisted. The private key lives at the same trust boundary as the push
// subscription keys already in the database and is never returned by any
// settings API.
const (
	settingVAPIDPublicKey  = "push_vapid_public_key"
	settingVAPIDPrivateKey = "push_vapid_private_key"
)

// ResolveVAPIDConfig guarantees a usable VAPID keypair so Web Push works with
// zero operator configuration. Resolution precedence:
//
//  1. Explicit env (both VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY set) — returned
//     untouched so operators can manage and rotate keys out-of-band.
//  2. A keypair previously generated and persisted in system_settings.
//  3. A freshly generated keypair, persisted for every future boot.
//
// On any error it returns cfg unchanged (push stays disabled) rather than
// failing startup — push is a non-critical feature.
func ResolveVAPIDConfig(db database.Database, cfg config.PushConfig, log *slog.Logger) config.PushConfig {
	// (1) Explicit env override wins and is never persisted.
	if cfg.VAPIDPublicKey != "" && cfg.VAPIDPrivateKey != "" {
		return cfg
	}

	repo := repository.NewSystemSettingRepository(db)
	pub, pubOK, errPub := repo.GetValue(settingVAPIDPublicKey)
	priv, privOK, errPriv := repo.GetValue(settingVAPIDPrivateKey)
	if err := errors.Join(errPub, errPriv); err != nil {
		log.Error("reading persisted VAPID keys; Web Push disabled", "error", err)
		return cfg
	}

	// (2) Reuse a previously persisted pair.
	if pubOK && privOK && pub != "" && priv != "" {
		cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey = pub, priv
		return cfg
	}

	// (3) First run with no keys anywhere: generate and persist a pair.
	// GenerateVAPIDKeys returns (privateKey, publicKey, err) — order matters.
	newPriv, newPub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		log.Error("generating VAPID keypair; Web Push disabled", "error", err)
		return cfg
	}
	if err := repo.Upsert(settingVAPIDPublicKey, newPub, "string",
		"Auto-generated VAPID public key for Web Push", "push"); err != nil {
		log.Error("persisting VAPID public key; Web Push disabled this boot", "error", err)
		return cfg
	}
	if err := repo.Upsert(settingVAPIDPrivateKey, newPriv, "string",
		"Auto-generated VAPID private key for Web Push", "push"); err != nil {
		log.Error("persisting VAPID private key; Web Push disabled this boot", "error", err)
		return cfg
	}
	cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey = newPub, newPriv
	log.Info("auto-generated and persisted a VAPID keypair for Web Push")
	return cfg
}

// pushTTLSeconds is how long a push service should retain an undelivered
// message. One day matches the notification cache horizon.
const pushTTLSeconds = 86400

// maxPushBodyLen caps the notification snippet placed in the push payload. Push
// payloads intentionally carry only IDs + a short title/body + a target URL; the
// app fetches full content after opening (see plan security note).
const maxPushBodyLen = 140

// activeSubsWhere filters to a user's non-revoked subscriptions. Shared by
// List and deliver so the "active" invariant (revoked_at IS NULL) is defined
// once rather than duplicated per query.
const activeSubsWhere = "WHERE user_id = ? AND revoked_at IS NULL"

// PushSubscriptionInfo is the non-sensitive view of a stored subscription
// returned to the owning user (keys are never exposed back to the client).
type PushSubscriptionInfo struct {
	ID         int        `json:"id"`
	Endpoint   string     `json:"endpoint"`
	UserAgent  string     `json:"user_agent"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// PushTestResult reports the per-subscription outcome of a diagnostic test push.
// It lets the mobile UI distinguish "no subscription on the server" from "the
// push provider rejected the send" from "delivered fine, but iOS didn't show a
// banner" — the three failure modes of WI-472.
type PushTestResult struct {
	SubscriptionID int    `json:"subscription_id"`
	Endpoint       string `json:"endpoint"`
	StatusCode     int    `json:"status_code"`
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
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
	rows, err := s.db.Query(
		"SELECT id, endpoint, user_agent, created_at, last_used_at FROM push_subscriptions "+
			activeSubsWhere+" ORDER BY created_at DESC", userID)
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

// SendTest sends a test push to every active subscription of the user and
// returns the per-subscription delivery outcome for diagnostics. An empty slice
// means the server has no active subscription for the user (the most common
// cause of "enabled but no banner").
func (s *PushService) SendTest(userID int) []PushTestResult {
	return s.deliver(userID, pushPayload{
		Title: "Windshift",
		Body:  "Push notifications are working.",
		Type:  "info",
		URL:   "/m/notifications",
	})
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
// pruning subscriptions the push service reports as permanently gone. It returns
// the per-subscription outcome so the diagnostic test endpoint can surface it;
// the fire-and-forget Dispatch path simply discards the result.
func (s *PushService) deliver(userID int, payload pushPayload) []PushTestResult {
	if !s.Enabled() {
		return nil
	}

	rows, err := s.db.Query(
		"SELECT id, endpoint, auth_key, p256dh_key FROM push_subscriptions "+activeSubsWhere, userID)
	if err != nil {
		slog.Error("push: load subscriptions failed", slog.String("component", "push"), slog.Any("error", err))
		return nil
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
		return nil
	}

	message, err := json.Marshal(payload)
	if err != nil {
		slog.Error("push: marshal payload failed", slog.String("component", "push"), slog.Any("error", err))
		return nil
	}

	options := &webpush.Options{
		Subscriber:      s.cfg.VAPIDSubject,
		VAPIDPublicKey:  s.cfg.VAPIDPublicKey,
		VAPIDPrivateKey: s.cfg.VAPIDPrivateKey,
		TTL:             pushTTLSeconds,
	}

	results := make([]PushTestResult, 0, len(subs))
	for _, sb := range subs {
		res := PushTestResult{SubscriptionID: sb.id, Endpoint: sb.endpoint}
		resp, err := webpush.SendNotification(message, &webpush.Subscription{
			Endpoint: sb.endpoint,
			Keys:     webpush.Keys{Auth: sb.auth, P256dh: sb.p256dh},
		}, options)
		if err != nil {
			res.Error = err.Error()
			slog.Warn("push: send failed", slog.String("component", "push"), slog.Int("sub_id", sb.id), slog.Any("error", err))
			results = append(results, res)
			continue
		}
		status := resp.StatusCode
		res.StatusCode = status

		switch {
		case status == http.StatusNotFound || status == http.StatusGone:
			// The endpoint is permanently gone — prune it.
			resp.Body.Close()
			res.Error = "subscription expired; pruned"
			if _, derr := s.db.Exec(`UPDATE push_subscriptions SET revoked_at = CURRENT_TIMESTAMP WHERE id = ?`, sb.id); derr != nil {
				slog.Error("push: prune failed", slog.String("component", "push"), slog.Int("sub_id", sb.id), slog.Any("error", derr))
			}
		case status >= 200 && status < 300:
			resp.Body.Close()
			res.OK = true
			_, _ = s.db.Exec(`UPDATE push_subscriptions SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, sb.id)
		default:
			// Other non-2xx (e.g. APNs rejecting the VAPID JWT) are transient
			// from the subscription's POV, but the provider's response body is
			// the single most useful clue for diagnosing why nothing arrives —
			// so capture and surface it rather than dropping it on the floor.
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			res.Error = strings.TrimSpace(string(body))
			if res.Error == "" {
				res.Error = resp.Status
			}
			slog.Warn("push: send rejected",
				slog.String("component", "push"), slog.Int("sub_id", sb.id),
				slog.Int("status", status), slog.String("body", res.Error))
		}
		results = append(results, res)
	}
	return results
}
