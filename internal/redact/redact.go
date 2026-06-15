// Package redact strips embedded credentials from strings before they are
// logged or persisted. It lives in its own leaf package so services,
// repositories, brokers, and lower-level git helpers can share one scrubber
// without import cycles.
package redact

import "regexp"

var (
	// urlCredentialPattern matches the `user:pass@` portion of HTTP(S) URLs.
	urlCredentialPattern = regexp.MustCompile(`(https?://)[^@/\s:]+:[^@/\s]+@`)

	// Environment-style secret assignments that commonly appear in command
	// output or error strings.
	envSecretPattern = regexp.MustCompile(`(?i)\b(WS_TOKEN|LLM_API_KEY|AGENT_GIT_TOKEN)=([^\s"'` + "`" + `]+)`)

	// HTTP Authorization headers.
	authorizationBearerPattern = regexp.MustCompile(`(?i)(Authorization\s*:\s*Bearer\s+)([^\s"'` + "`" + `]+)`)

	// Windshift runner/control credentials and capability tokens.
	windshiftTokenPattern = regexp.MustCompile(`\b(?:wsrt|wsrc|crw)_[A-Za-z0-9._-]+\b`)

	// JSON string fields whose values are credentials. Keep this conservative:
	// only quoted string values are rewritten so arbitrary JSON remains valid.
	jsonSecretPattern = regexp.MustCompile(`(?i)("(?:api_key|apikey|token|access_token|refresh_token|password|client_secret|private_key|authorization)"\s*:\s*")[^"\\]*(?:\\.[^"\\]*)*(")`)
)

// String strips known credential forms from a string before it reaches logs or
// persistence. It intentionally preserves surrounding syntax (JSON quotes,
// header names, env var names, URL scheme) so redacted payloads remain useful
// for debugging without exposing token material.
func String(s string) string {
	if s == "" {
		return s
	}
	s = urlCredentialPattern.ReplaceAllString(s, "${1}[REDACTED]@")
	s = envSecretPattern.ReplaceAllString(s, "${1}=[REDACTED]")
	s = authorizationBearerPattern.ReplaceAllString(s, "${1}[REDACTED]")
	s = windshiftTokenPattern.ReplaceAllString(s, "[REDACTED]")
	s = jsonSecretPattern.ReplaceAllString(s, "${1}[REDACTED]${2}")
	return s
}
