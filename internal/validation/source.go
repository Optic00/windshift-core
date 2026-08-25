package validation

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	TitleMaxRunes    = 255
	MarkdownMaxBytes = 256 * 1024
)

// ValidateTitle validates a plain-text title without rewriting accepted source.
func ValidateTitle(title string) error {
	if title == "" || strings.TrimSpace(title) == "" {
		return &ValidationError{Field: "title", Message: "Title is required"}
	}
	if strings.TrimSpace(title) != title {
		return &ValidationError{Field: "title", Message: "Title must not have surrounding whitespace"}
	}
	if utf8.RuneCountInString(title) > TitleMaxRunes {
		return &ValidationError{Field: "title", Message: fmt.Sprintf("Title must be at most %d characters", TitleMaxRunes)}
	}
	for _, r := range title {
		if r == '\n' || r == '\r' || unicode.IsControl(r) {
			return &ValidationError{Field: "title", Message: "Title must be a single line without control characters"}
		}
	}
	return nil
}

// ValidateMarkdownSource validates Markdown without trimming, decoding,
// normalizing, or otherwise changing accepted source.
func ValidateMarkdownSource(field, source string, maxBytes int, required bool) error {
	if !utf8.ValidString(source) {
		return &ValidationError{Field: field, Message: fmt.Sprintf("%s must be valid UTF-8", field)}
	}
	if required && strings.TrimSpace(source) == "" {
		return &ValidationError{Field: field, Message: fmt.Sprintf("%s is required", field)}
	}
	if maxBytes > 0 && len(source) > maxBytes {
		return &ValidationError{Field: field, Message: fmt.Sprintf("%s must be at most %d bytes", field, maxBytes)}
	}
	return nil
}
