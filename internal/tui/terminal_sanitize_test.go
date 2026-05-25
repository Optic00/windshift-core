package tui

import "testing"

func TestSanitizeTerminalTextStripsEscapeSequences(t *testing.T) {
	input := "ok \x1b[31mred\x1b[0m \x1b]52;c;SGVsbG8=\x07 done"
	got := sanitizeTerminalText(input)
	want := "ok red  done"
	if got != want {
		t.Fatalf("sanitizeTerminalText() = %q, want %q", got, want)
	}
}

func TestSanitizeTerminalTextPreservesSafeMultilineText(t *testing.T) {
	input := "line 1\nline 2\t✓\rrewritten\x07"
	got := sanitizeTerminalText(input)
	want := "line 1\nline 2\t✓rewritten"
	if got != want {
		t.Fatalf("sanitizeTerminalText() = %q, want %q", got, want)
	}
}

func TestSanitizeTerminalLineCollapsesRows(t *testing.T) {
	input := "title\n\x1b[2Jspoof\trow"
	got := sanitizeTerminalLine(input)
	want := "title spoof row"
	if got != want {
		t.Fatalf("sanitizeTerminalLine() = %q, want %q", got, want)
	}
}
