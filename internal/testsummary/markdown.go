package testsummary

import (
	"fmt"
	"strings"
	"time"

	"windshift/internal/repository"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// RenderMarkdown renders the shared test-run markdown summary used by both the
// legacy cookie-auth surface and REST v1.
func RenderMarkdown(header *repository.MarkdownRunHeader, results []repository.MarkdownResult) string {
	stats := map[string]int{"total": 0, "passed": 0, "failed": 0, "blocked": 0, "skipped": 0, "not_run": 0}
	for _, res := range results {
		stats["total"]++
		stats[res.Status]++
	}

	var markdown strings.Builder
	fmt.Fprintf(&markdown, "# Test Run Summary: %s\n\n", escapeMarkdownInline(header.RunName)) //nolint:gosec // G705: written to strings.Builder, returned as JSON
	fmt.Fprintf(&markdown, "**Test Set:** %s\n\n", escapeMarkdownInline(header.SetName))       //nolint:gosec // G705: written to strings.Builder, returned as JSON
	if header.StartedAt.Valid {
		fmt.Fprintf(&markdown, "**Started:** %s\n\n", header.StartedAt.Time.Format("2006-01-02 15:04:05")) //nolint:gosec // G705: written to strings.Builder, returned as JSON
	}
	if header.EndedAt.Valid {
		fmt.Fprintf(&markdown, "**Ended:** %s\n\n", header.EndedAt.Time.Format("2006-01-02 15:04:05")) //nolint:gosec // G705: written to strings.Builder, returned as JSON
		if header.StartedAt.Valid {
			duration := header.EndedAt.Time.Sub(header.StartedAt.Time)
			fmt.Fprintf(&markdown, "**Duration:** %s\n\n", duration.Round(time.Second)) //nolint:gosec // G705: written to strings.Builder, returned as JSON
		}
	}
	markdown.WriteString("## Statistics\n\n")
	markdown.WriteString("| Status | Count | Percentage |\n")
	markdown.WriteString("|--------|-------|------------|\n")
	if stats["total"] > 0 {
		fmt.Fprintf(&markdown, "| ✅ Passed | %d | %.1f%% |\n", stats["passed"], float64(stats["passed"])/float64(stats["total"])*100)
		fmt.Fprintf(&markdown, "| ❌ Failed | %d | %.1f%% |\n", stats["failed"], float64(stats["failed"])/float64(stats["total"])*100)
		fmt.Fprintf(&markdown, "| ⚠️ Blocked | %d | %.1f%% |\n", stats["blocked"], float64(stats["blocked"])/float64(stats["total"])*100)
		fmt.Fprintf(&markdown, "| ⏭️ Skipped | %d | %.1f%% |\n", stats["skipped"], float64(stats["skipped"])/float64(stats["total"])*100)
		fmt.Fprintf(&markdown, "| ⏸️ Not Run | %d | %.1f%% |\n", stats["not_run"], float64(stats["not_run"])/float64(stats["total"])*100)
		fmt.Fprintf(&markdown, "| **Total** | **%d** | **100%%** |\n\n", stats["total"])
		passRate := float64(stats["passed"]) / float64(stats["total"]) * 100
		fmt.Fprintf(&markdown, "**Overall Pass Rate:** %.1f%%\n\n", passRate)
	}
	if stats["failed"] > 0 {
		markdown.WriteString("## Failed Tests\n\n")
		for _, result := range results {
			if result.Status == "failed" {
				fmt.Fprintf(&markdown, "### ❌ %s\n\n", escapeMarkdownInline(result.Title)) //nolint:gosec // G705: written to strings.Builder, returned as JSON
				if result.ActualResult != "" {
					fmt.Fprintf(&markdown, "**Actual Result:**\n%s\n\n", escapeMarkdownBlock(result.ActualResult)) //nolint:gosec // G705: written to strings.Builder, returned as JSON
				}
				if result.Notes != "" {
					fmt.Fprintf(&markdown, "**Notes:**\n%s\n\n", escapeMarkdownBlock(result.Notes)) //nolint:gosec // G705: written to strings.Builder, returned as JSON
				}
				markdown.WriteString("---\n\n")
			}
		}
	}
	if stats["blocked"] > 0 {
		markdown.WriteString("## Blocked Tests\n\n")
		for _, result := range results {
			if result.Status == "blocked" {
				fmt.Fprintf(&markdown, "### ⚠️ %s\n", escapeMarkdownInline(result.Title)) //nolint:gosec // G705: written to strings.Builder, returned as JSON
				if result.Notes != "" {
					fmt.Fprintf(&markdown, "**Reason:**\n%s\n", escapeMarkdownBlock(result.Notes)) //nolint:gosec // G705: written to strings.Builder, returned as JSON
				}
				markdown.WriteString("\n")
			}
		}
	}
	markdown.WriteString("## All Test Results\n\n")
	markdown.WriteString("| Test Case | Status | Notes |\n")
	markdown.WriteString("|-----------|--------|-------|\n")
	for _, result := range results {
		notes := "-"
		if result.Notes != "" {
			notes = escapeMarkdownTableCell(result.Notes)
		}
		fmt.Fprintf(&markdown, "| %s | %s %s | %s |\n", //nolint:gosec // G705: written to strings.Builder, returned as JSON
			escapeMarkdownTableCell(result.Title),
			statusIcon(result.Status),
			escapeMarkdownTableCell(cases.Title(language.English).String(result.Status)),
			notes)
	}
	return markdown.String()
}

func statusIcon(status string) string {
	switch status {
	case "passed":
		return "✅"
	case "failed":
		return "❌"
	case "blocked":
		return "⚠️"
	case "skipped":
		return "⏭️"
	default:
		return "⏸️"
	}
}

// escapeMarkdownTableCell makes a string safe to interpolate into a single
// markdown table cell. Pipes are escaped so they don't introduce new columns,
// and any newline is collapsed to a space so the cell can't break the row.
func escapeMarkdownTableCell(s string) string {
	return escapeMarkdownInline(s)
}

func escapeMarkdownInline(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return escapeMarkdownText(s)
}

func escapeMarkdownBlock(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = escapeMarkdownText(lines[i])
	}
	return strings.Join(lines, "  \n")
}

func escapeMarkdownText(s string) string {
	const punctuation = `!"#$%&'()*+,-./:;<=>?@[\]^_` + "`" + `{|}~`
	var escaped strings.Builder
	escaped.Grow(len(s))
	for _, char := range s {
		if strings.ContainsRune(punctuation, char) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(char)
	}
	result := escaped.String()
	result = strings.ReplaceAll(result, `\:\/\/`, "\\:\u200b\\/\\/")
	result = strings.ReplaceAll(result, `www\.`, "www\u200b\\.")
	result = strings.ReplaceAll(result, `WWW\.`, "WWW\u200b\\.")
	return strings.ReplaceAll(result, `\@`, "\\@\u200b")
}
