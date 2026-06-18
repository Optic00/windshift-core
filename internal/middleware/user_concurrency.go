package middleware

import (
	"net/http"
	"strings"
	"sync"

	"windshift/internal/models"
)

// UserConcurrencyLimiter caps the number of simultaneously in-flight requests
// per authenticated user. It is a counting semaphore keyed by user id — unlike
// RateLimiter (requests/second), it bounds *concurrency*, which maps directly
// to how many shared DB-pool connections a single user can hold at once. With
// a pool of N connections and a per-user cap of C, no single user (a runaway
// frontend, a buggy retry loop, an automated client) can hold more than C of
// the N, so one user can no longer starve everyone else.
//
// Requests without an authenticated user (keyed by IP limiters elsewhere) and
// long-lived streaming endpoints pass through untouched.
type UserConcurrencyLimiter struct {
	mu       sync.Mutex
	inFlight map[int]int // userID -> current in-flight count
	limit    int
}

// NewUserConcurrencyLimiter returns a limiter capping each user to limit
// concurrent requests. A limit <= 0 disables limiting (Limit becomes a passthrough).
func NewUserConcurrencyLimiter(limit int) *UserConcurrencyLimiter {
	return &UserConcurrencyLimiter{inFlight: make(map[int]int), limit: limit}
}

// Limit wraps next, rejecting a user's request with 429 once they already have
// `limit` requests in flight. The slot is held only for the duration of next
// (acquired here, released on return), so it tracks actual handler/DB work.
func (l *UserConcurrencyLimiter) Limit(next http.Handler) http.Handler {
	if l == nil || l.limit <= 0 || e2eRateLimitsDisabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(ContextKeyUser).(*models.User)
		if !ok || user == nil || isStreamingPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !l.acquire(user.ID) {
			// Fail fast and release immediately rather than queueing (which
			// would itself hold the request open and add pool pressure).
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Too many concurrent requests. Please retry shortly.", http.StatusTooManyRequests)
			return
		}
		defer l.release(user.ID)
		next.ServeHTTP(w, r)
	})
}

func (l *UserConcurrencyLimiter) acquire(userID int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight[userID] >= l.limit {
		return false
	}
	l.inFlight[userID]++
	return true
}

func (l *UserConcurrencyLimiter) release(userID int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Drop the key at zero so the map can't grow unbounded with idle users.
	if l.inFlight[userID] <= 1 {
		delete(l.inFlight, userID)
		return
	}
	l.inFlight[userID]--
}

// isStreamingPath reports whether path is a long-lived SSE/streaming endpoint
// that must not consume a per-user concurrency slot for its whole lifetime — a
// held-open stream (agent-run events, AI chat) would otherwise eat a slot for
// minutes and throttle the user's normal interactive requests.
func isStreamingPath(path string) bool {
	return strings.HasSuffix(path, "/events") || strings.Contains(path, "/ai/")
}
