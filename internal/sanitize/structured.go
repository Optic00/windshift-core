package sanitize

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// QueryText — structured query text (CQL / QL saved queries).
// Comparison operators (<, >, <=, >=) are load-bearing syntax here, so
// the HTML-stripping policies would eat them (and a `<` followed by a
// letter swallows everything to the next `>`). This policy only
// enforces the unified long-form length cap — no HTML stripping, no
// entity decoding. The query engine tokenizes/parses the text itself
// and rejects anything malformed at evaluation time.
var QueryText Policy = PolicyFunc(queryText)

func queryText(s string) string {
	if len(s) > LongTextMaxBytes {
		s = truncateBytesRuneSafe(s, LongTextMaxBytes)
	}
	return s
}

// ValidateJSONPayload gates a user-supplied JSON blob: bound by the
// unified long-form size cap and require well-formed JSON. HTML
// stripping would corrupt valid payloads (it decodes entities and a
// `<` followed by a letter swallows text across quote boundaries), so
// JSON fields must be rejected when invalid rather than scrubbed —
// the same pattern as runner_control's payload_json and issue_sync's
// mapping blobs. Empty input is allowed; callers decide whether the
// field is required.
//
// The returned error message is phrased for a 400 validation response
// ("<field> exceeds the maximum size" / "<field> must be valid JSON").
// Returns nil when the payload is acceptable.
func ValidateJSONPayload(field, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > LongTextMaxBytes {
		return fmt.Errorf("%s exceeds the maximum size", field)
	}
	if !json.Valid([]byte(value)) {
		return fmt.Errorf("%s must be valid JSON", field)
	}
	return nil
}

// truncateBytesRuneSafe cuts s to at most maxBytes bytes without
// splitting a multi-byte rune.
func truncateBytesRuneSafe(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
