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

// NormalizeTitle trims and validates a plain-text title.
func NormalizeTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", &ValidationError{Field: "title", Message: "Title is required"}
	}
	if utf8.RuneCountInString(title) > TitleMaxRunes {
		return "", &ValidationError{Field: "title", Message: fmt.Sprintf("Title must be at most %d characters", TitleMaxRunes)}
	}
	for _, r := range title {
		if r == '\n' || r == '\r' || unicode.IsControl(r) {
			return "", &ValidationError{Field: "title", Message: "Title must be a single line without control characters"}
		}
	}
	return title, nil
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
