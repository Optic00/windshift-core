package wscli

import "strings"

// ParseCLIEscapes interprets the conventional `\n`, `\t`, `\r`, and `\\`
// escape sequences in a CLI flag value. Any other backslash stays
// literal (so users can still type `\d` in a regex pattern without it
// getting eaten). Equivalent to `printf %b` behavior — and the matching
// rationale: when a user types `ws page edit --content "a\nb"` they
// almost always mean a newline, not the two-character sequence `\n`.
//
// Pass `--file` when you need arbitrary bytes preserved verbatim
// (existing alternative on every free-form text flag that takes both).
func ParseCLIEscapes(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		default:
			// Unknown escape — leave the backslash in place so users
			// who type "\d" / "\s" / "\." in regex flags or markdown
			// don't get them silently dropped.
			b.WriteByte('\\')
			b.WriteByte(s[i+1])
		}
		i++
	}
	return b.String()
}
