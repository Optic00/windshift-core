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
	fmt.Fprintf(&markdown, "# Test Run Summary: %s\n\n", header.RunName) //nolint:gosec // G705: written to strings.Builder, returned as JSON
	fmt.Fprintf(&markdown, "**Test Set:** %s\n\n", header.SetName)       //nolint:gosec // G705: written to strings.Builder, returned as JSON
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
				fmt.Fprintf(&markdown, "### ❌ %s\n\n", result.Title) //nolint:gosec // G705: written to strings.Builder, returned as JSON
				if result.ActualResult != "" {
					fmt.Fprintf(&markdown, "**Actual Result:**\n%s\n\n", result.ActualResult) //nolint:gosec // G705: written to strings.Builder, returned as JSON
				}
				if result.Notes != "" {
					fmt.Fprintf(&markdown, "**Notes:**\n%s\n\n", result.Notes) //nolint:gosec // G705: written to strings.Builder, returned as JSON
				}
				markdown.WriteString("---\n\n")
			}
		}
	}
	if stats["blocked"] > 0 {
		markdown.WriteString("## Blocked Tests\n\n")
		for _, result := range results {
			if result.Status == "blocked" {
				fmt.Fprintf(&markdown, "### ⚠️ %s\n", result.Title) //nolint:gosec // G705: written to strings.Builder, returned as JSON
				if result.Notes != "" {
					fmt.Fprintf(&markdown, "**Reason:** %s\n", result.Notes) //nolint:gosec // G705: written to strings.Builder, returned as JSON
				}
				markdown.WriteString("\n")
			}
		}
	}
	markdown.WriteString("## All Test Results\n\n")
	markdown.WriteString("| Test Case | Status | Notes |\n")
	markdown.WriteString("|-----------|--------|-------|\n")
	for _, result := range results {
		notes := result.Notes
		if notes == "" {
			notes = "-"
		}
		fmt.Fprintf(&markdown, "| %s | %s %s | %s |\n", //nolint:gosec // G705: written to strings.Builder, returned as JSON
			escapeMarkdownTableCell(result.Title),
			statusIcon(result.Status),
			escapeMarkdownTableCell(cases.Title(language.English).String(result.Status)),
			escapeMarkdownTableCell(notes))
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
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "|", `\|`)
	return s
}
