package data

import (
	"strings"
	"unicode/utf8"
)

// SanitizeText strips terminal control sequences from untrusted text
// before it is rendered into the SSH TUI. HTML/Markdown sanitizers do not
// remove ANSI/OSC controls; rendering those verbatim can trigger terminal-side
// effects such as OSC 52 clipboard writes or UI spoofing.
func SanitizeText(input string) string {
	if input == "" {
		return ""
	}

	var out strings.Builder
	out.Grow(len(input))

	for i := 0; i < len(input); {
		if input[i] == 0x1b { // ESC
			i = skipEscSequence(input, i+1)
			continue
		}

		r, size := utf8.DecodeRuneInString(input[i:])
		if r == utf8.RuneError && size == 1 {
			// Drop invalid bytes rather than passing terminal garbage through.
			i++
			continue
		}

		switch r {
		case 0x009b: // 8-bit CSI
			i = skipCSI(input, i+size)
			continue
		case 0x009d: // 8-bit OSC
			i = skipStringTerminatedControl(input, i+size)
			continue
		}

		if isUnsafeTerminalControl(r) {
			i += size
			continue
		}

		out.WriteString(input[i : i+size])
		i += size
	}

	return out.String()
}

// SanitizeLine is for labels/titles/table cells. It strips terminal
// controls and also collapses line separators so untrusted labels cannot break
// out of their assigned row/heading.
func SanitizeLine(input string) string {
	clean := SanitizeText(input)
	clean = strings.ReplaceAll(clean, "\r", " ")
	clean = strings.ReplaceAll(clean, "\n", " ")
	clean = strings.ReplaceAll(clean, "\t", " ")
	return strings.Join(strings.Fields(clean), " ")
}

func SanitizeStringPtr(input *string, line bool) *string {
	if input == nil {
		return nil
	}
	clean := SanitizeText(*input)
	if line {
		clean = SanitizeLine(clean)
	}
	return &clean
}

func isUnsafeTerminalControl(r rune) bool {
	// Keep LF and TAB for multiline text. Drop CR to prevent carriage-return
	// rewrites and drop the rest of C0/C1 controls.
	if r == '\n' || r == '\t' {
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

func skipEscSequence(s string, i int) int {
	if i >= len(s) {
		return i
	}

	switch s[i] {
	case '[': // CSI
		return skipCSI(s, i+1)
	case ']': // OSC
		return skipStringTerminatedControl(s, i+1)
	case 'P', '^', '_', 'X': // DCS, PM, APC, SOS
		return skipStringTerminatedControl(s, i+1)
	default:
		// Most remaining ESC controls are two-byte sequences (ESC + final).
		_, size := utf8.DecodeRuneInString(s[i:])
		if size <= 0 {
			return i + 1
		}
		return i + size
	}
}

func skipCSI(s string, i int) int {
	for i < len(s) {
		b := s[i]
		i++
		if b >= 0x40 && b <= 0x7e {
			return i
		}
	}
	return i
}

func skipStringTerminatedControl(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case 0x07: // BEL terminator
			return i + 1
		case 0x1b: // ST terminator: ESC \
			if i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		i++
	}
	return i
}
