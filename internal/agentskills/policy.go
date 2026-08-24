// Package agentskills defines the trust and size boundaries for agent skills.
package agentskills

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"windshift/internal/models"
)

const (
	MaxBodyBytes        = 64 * 1024
	MaxActivationBytes  = 256 * 1024
	MaxActivationTokens = 64 * 1024
)

var ErrActivationTooLarge = errors.New("agent skill activation exceeds the aggregate context budget")

const referencedPagesPrefix = "\n\n---\n\n## Referenced pages\n\nSaved page snapshots attached to this skill.\n"

type Usage struct {
	Bytes           int `json:"bytes"`
	EstimatedTokens int `json:"estimated_tokens"`
	MaxBytes        int `json:"max_bytes"`
	MaxTokens       int `json:"max_tokens"`
}

// ValidateMetadata keeps prompt-index fields single-line and free of control
// characters. Skill instructions belong in the markdown body, not metadata.
func ValidateMetadata(name, description string) error {
	for label, value := range map[string]string{"name": name, "description": description} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must be a single line", label)
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return fmt.Errorf("%s must not contain control characters", label)
			}
		}
		if strings.ContainsAny(value, "<>") {
			return fmt.Errorf("%s must not contain structured tags", label)
		}
	}
	return nil
}

// RenderActivation creates the exact body a run may fetch. Page content comes
// only from the saved snapshots carried by the references.
func RenderActivation(body string, pages []models.SkillPageReference) (string, Usage, error) {
	var b strings.Builder
	b.WriteString(body)
	if len(pages) > 0 {
		b.WriteString(referencedPagesPrefix)
		for _, page := range pages {
			b.WriteString(renderPageSnapshot(page))
		}
	}
	rendered := b.String()
	usage := Usage{
		Bytes:           len(rendered),
		EstimatedTokens: max((len(rendered)+3)/4, utf8.RuneCountInString(rendered)),
		MaxBytes:        MaxActivationBytes,
		MaxTokens:       MaxActivationTokens,
	}
	if usage.Bytes > usage.MaxBytes || usage.EstimatedTokens > usage.MaxTokens {
		return "", usage, ErrActivationTooLarge
	}
	return rendered, usage, nil
}

// PageSnapshotUsage returns the contribution of one saved reference to the
// rendered activation. The prefix is reported separately because it appears
// only once when at least one page is attached.
func PageSnapshotUsage(page models.SkillPageReference) (bytes, runes, prefixBytes, prefixRunes int) {
	fragment := renderPageSnapshot(page)
	return len(fragment), utf8.RuneCountInString(fragment), len(referencedPagesPrefix), utf8.RuneCountInString(referencedPagesPrefix)
}

func renderPageSnapshot(page models.SkillPageReference) string {
	title := strings.TrimSpace(page.SnapshotTitle)
	if title == "" {
		title = "(untitled)"
	}
	return fmt.Sprintf("\n### %s\n\n%s\n", title, page.ContentSnapshot)
}
