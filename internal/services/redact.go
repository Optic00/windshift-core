package services

import "regexp"

// urlCredentialPattern matches the `user:pass@` portion of HTTP(S)
// URLs. Both halves are non-empty and contain no @ / / or whitespace —
// just enough to identify embedded credentials in a git remote without
// chewing on unrelated `:` characters elsewhere in the string.
var urlCredentialPattern = regexp.MustCompile(`(https?://)[^@/\s:]+:[^@/\s]+@`)

// RedactString strips embedded credentials from any URL-shaped substring
// before the result is logged or persisted. The orchestrator handles a
// number of strings — git error output, command echoes, exec failures —
// that may unexpectedly contain a token in the form
// `https://oauth2:<token>@host/...`. Always running them through this
// scrubber removes a whole class of accidental leaks regardless of
// where they originate.
//
// Replacement: `https://[REDACTED]@host/...`. The username is dropped
// alongside the password because "oauth2" is itself a token-bearing
// signal and shouldn't be retained in audit output.
func RedactString(s string) string {
	if s == "" {
		return s
	}
	return urlCredentialPattern.ReplaceAllString(s, "${1}[REDACTED]@")
}
