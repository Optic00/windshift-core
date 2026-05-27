package llm

import (
	"regexp"
	"strings"
)

// ErrorClass buckets LLM-call failures into a small set of admin-actionable
// categories. The classifier reads off the stringified error that the briefing
// scheduler already persists in daily_briefings.error, so no schema change is
// needed — just call ClassifyError on each row.
type ErrorClass string

const (
	// ErrorClassModelNotFound — HTTP 404 with a body that mentions the model.
	// Triggered when the configured model has been retired by the provider
	// (e.g. "gemini-3.1-flash-lite-preview is no longer available").
	ErrorClassModelNotFound ErrorClass = "model_not_found"

	// ErrorClassAuthFailed — HTTP 401/403 from the provider. Likely an
	// expired/revoked API key or wrong project.
	ErrorClassAuthFailed ErrorClass = "auth_failed"

	// ErrorClassRateLimited — HTTP 429 from the provider. Transient; usually
	// resolves on retry.
	ErrorClassRateLimited ErrorClass = "rate_limited"

	// ErrorClassServerError — HTTP 5xx (excluding 503 which already has a
	// dedicated ErrServiceNotReady code we don't surface separately here).
	ErrorClassServerError ErrorClass = "server_error"

	// ErrorClassConnectionFailed — no HTTP status; the request never landed.
	// Wraps ErrConnectionFailed in the LLM client.
	ErrorClassConnectionFailed ErrorClass = "connection_failed"

	// ErrorClassOther — anything we couldn't pattern-match. The raw message is
	// still surfaced in the diagnostics widget so admins aren't blind.
	ErrorClassOther ErrorClass = "other"
)

// statusRe extracts the HTTP status from messages like
//
//	"LLM API error: status 404 - {...}"
//	"failed to connect to LLM service: HTTP 502"
var statusRe = regexp.MustCompile(`(?:status|HTTP)\s*(\d{3})`)

// ClassifyError buckets a stringified error from the LLM client into a
// stable, admin-facing category. Pure function — safe to call from handlers
// without locking.
func ClassifyError(msg string) ErrorClass {
	lower := strings.ToLower(msg)

	if strings.Contains(lower, "failed to connect") || strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "no such host") || strings.Contains(lower, "timeout") {
		return ErrorClassConnectionFailed
	}

	match := statusRe.FindStringSubmatch(msg)
	if len(match) < 2 {
		return ErrorClassOther
	}
	switch match[1] {
	case "401", "403":
		return ErrorClassAuthFailed
	case "404":
		if strings.Contains(lower, "model") || strings.Contains(lower, "not_found") || strings.Contains(lower, "no longer available") {
			return ErrorClassModelNotFound
		}
		return ErrorClassOther
	case "429":
		return ErrorClassRateLimited
	}
	if len(match[1]) == 3 && match[1][0] == '5' {
		return ErrorClassServerError
	}
	return ErrorClassOther
}
