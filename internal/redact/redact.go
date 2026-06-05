// Package redact strips embedded credentials from strings before they are
// logged or persisted. It lives in its own leaf package so both the services
// layer and the lower-level repoprep package (which also backs a standalone
// triage binary) can share one scrubber without an import cycle.
package redact

import "regexp"

// urlCredentialPattern matches the `user:pass@` portion of HTTP(S) URLs. Both
// halves are non-empty and contain no @ / / or whitespace — just enough to
// identify embedded credentials in a git remote without chewing on unrelated
// `:` characters elsewhere in the string.
var urlCredentialPattern = regexp.MustCompile(`(https?://)[^@/\s:]+:[^@/\s]+@`)

// String strips embedded credentials from any URL-shaped substring. The
// orchestrator handles git error output, command echoes, and exec failures
// that may carry a token as `https://oauth2:<token>@host/...`; running them
// through this scrubber removes a whole class of accidental leaks regardless
// of origin. The username is dropped alongside the password because "oauth2"
// is itself a token-bearing signal that shouldn't survive into audit output.
func String(s string) string {
	if s == "" {
		return s
	}
	return urlCredentialPattern.ReplaceAllString(s, "${1}[REDACTED]@")
}
