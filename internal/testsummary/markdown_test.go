package testsummary

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"windshift/internal/repository"
)

func TestRenderMarkdown(t *testing.T) {
	start := time.Date(2026, 6, 17, 9, 30, 0, 0, time.UTC)
	end := start.Add(95 * time.Second)

	got := RenderMarkdown(&repository.MarkdownRunHeader{
		RunName:   "Release smoke",
		SetName:   "Core checks",
		StartedAt: sql.NullTime{Time: start, Valid: true},
		EndedAt:   sql.NullTime{Time: end, Valid: true},
	}, []repository.MarkdownResult{
		{Title: "Login | happy path", Status: "passed"},
		{Title: "Payment failure", Status: "failed", ActualResult: "500", Notes: "needs\ntriage"},
		{Title: "Email", Status: "blocked", Notes: "provider down"},
	})

	for _, want := range []string{
		"# Test Run Summary: Release smoke",
		"**Test Set:** Core checks",
		"**Duration:** 1m35s",
		"| ✅ Passed | 1 | 33.3% |",
		"### ❌ Payment failure",
		"### ⚠️ Email",
		"| Login \\| happy path | ✅ Passed | - |",
		"| Payment failure | ❌ Failed | needs triage |",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
}
