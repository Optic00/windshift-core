package services

import "windshift/internal/redact"

// RedactString strips embedded credentials from any URL-shaped substring
// before the result is logged or persisted. It delegates to the shared
// internal/redact package so the services layer and repoprep scrub identically;
// the many existing services.RedactString call sites keep working unchanged.
func RedactString(s string) string {
	return redact.String(s)
}
