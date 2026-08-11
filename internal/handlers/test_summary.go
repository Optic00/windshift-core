package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"windshift/internal/repository"
	"windshift/internal/testsummary"
)

type TestSummaryHandler struct {
	repo *repository.TestSummaryRepository
}

func NewTestSummaryHandlerWithPool(repo *repository.TestSummaryRepository) *TestSummaryHandler {
	return &TestSummaryHandler{repo: repo}
}

func (h *TestSummaryHandler) GetMarkdownSummary(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	runID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	header, err := h.repo.FindMarkdownRunHeader(runID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "test_run")
		return
	}
	if err != nil {
		respondNotFound(w, r, "test_run")
		return
	}

	results, err := h.repo.FindMarkdownResults(runID, workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, map[string]string{"markdown": testsummary.RenderMarkdown(header, results)})
}

// GetReportsSummary returns aggregate test reports for a workspace
// Supports optional milestone_id and days query parameters
func (h *TestSummaryHandler) GetReportsSummary(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	milestoneIDStr := r.URL.Query().Get("milestone_id")
	daysStr := r.URL.Query().Get("days")

	var milestoneID *int
	if milestoneIDStr != "" {
		mid, err := strconv.Atoi(milestoneIDStr)
		if err != nil {
			respondInvalidID(w, r, "milestone_id")
			return
		}
		milestoneID = &mid
	}

	days := 30
	if daysStr != "" {
		d, err := strconv.Atoi(daysStr)
		if err != nil || d < 1 || d > 365 {
			respondValidationError(w, r, "Invalid days parameter (must be 1-365)")
			return
		}
		days = d
	}

	filter := repository.ReportFilter{
		WorkspaceID: workspaceID,
		MilestoneID: milestoneID,
		StartDate:   time.Now().AddDate(0, 0, -days),
	}

	stats, err := h.repo.GetOverallStats(filter)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	trend, err := h.repo.GetTrend(filter)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	failures, err := h.repo.GetRecentFailures(filter, 20)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	blocked, err := h.repo.GetRecentBlocked(filter, 20)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, map[string]any{
		"overall": map[string]any{
			"total_runs":  stats.TotalRuns,
			"total_tests": stats.TotalTests,
			"passed":      stats.Passed,
			"failed":      stats.Failed,
			"blocked":     stats.Blocked,
			"skipped":     stats.Skipped,
			"not_run":     stats.NotRun,
			"pass_rate":   stats.PassRate(),
		},
		"trend":           trend,
		"recent_failures": failures,
		"recent_blocked":  blocked,
	})
}
