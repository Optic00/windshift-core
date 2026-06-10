package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"windshift/internal/llm"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/utils"
)

// DiagnosticsHandler exposes admin-only system diagnostics endpoints.
//
// Each endpoint reuses existing instrumentation (action_execution_logs,
// webhook_deliveries, scheduler_runs) and is read-only except for the manual
// purge endpoints, which delete old rows on demand.
type DiagnosticsHandler struct {
	actionRepo       *repository.ActionRepository
	deliveryRepo     *repository.WebhookDeliveryRepository
	schedulerRunRepo *repository.SchedulerRunRepository
	fracIndexRepo    *repository.FracIndexRepository
	aiRepo           *repository.AIRepository
	llmManager       *llm.ConnectionManager
	llmCache         *llm.ModelCache
	auditor          *logger.Auditor
}

// NewDiagnosticsHandler creates a new diagnostics handler.
func NewDiagnosticsHandler(
	actionRepo *repository.ActionRepository,
	deliveryRepo *repository.WebhookDeliveryRepository,
	schedulerRunRepo *repository.SchedulerRunRepository,
	fracIndexRepo *repository.FracIndexRepository,
	aiRepo *repository.AIRepository,
	llmManager *llm.ConnectionManager,
	llmCache *llm.ModelCache,
	auditor *logger.Auditor,
) *DiagnosticsHandler {
	return &DiagnosticsHandler{
		actionRepo:       actionRepo,
		deliveryRepo:     deliveryRepo,
		schedulerRunRepo: schedulerRunRepo,
		fracIndexRepo:    fracIndexRepo,
		aiRepo:           aiRepo,
		llmManager:       llmManager,
		llmCache:         llmCache,
		auditor:          auditor,
	}
}

// GetFracIndexState returns a snapshot of persisted items.frac_index state
// for the admin diagnostics panel. "healthy" is false when the column
// collation diverges from byte ordering or when the next key the generator
// would produce already exists in the table.
//
// GET /api/admin/diagnostics/frac-index
func (h *DiagnosticsHandler) GetFracIndexState(w http.ResponseWriter, r *http.Request) {
	dbState, err := h.fracIndexRepo.GetDBStats()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Compute what the next append would produce so the panel can flag
	// in-flight collisions even though the in-process generator no longer
	// caches a predicted key.
	predicted, perr := nextAppendKey(dbState.ByteMax)
	if perr != nil {
		respondInternalError(w, r, perr)
		return
	}
	if predicted != "" {
		p := predicted
		dbState.PredictedNext = &p
		collision, cerr := h.fracIndexRepo.ProbePredictedKey(predicted)
		if cerr != nil {
			respondInternalError(w, r, cerr)
			return
		}
		dbState.PredictedCollision = collision
	}

	healthy := !dbState.CollationMismatch && dbState.PredictedCollision == nil
	respondJSONOK(w, map[string]any{
		"db":      dbState,
		"healthy": healthy,
	})
}

// nextAppendKey mirrors GenerateFracIndexForNewItem's "append after current max"
// step without taking a row lock: it just runs KeyBetween over the supplied
// byte-wise max. Empty when there are no rows (the generator would seed with
// KeyBetween("", "") in that case, which is fine to surface).
func nextAppendKey(byteMax *string) (string, error) {
	last := ""
	if byteMax != nil {
		last = *byteMax
	}
	return repository.KeyBetween(last, "")
}

// GetActionLogs returns recent cross-workspace action execution logs.
//
// Query params:
//   - mode:  "failed" (default — recent failures) or "slowest" (longest-running completed runs)
//   - since: duration string like "24h", "1h", "15m" — defaults to 24h
//   - limit: max rows (default 25, capped at 200)
//
// GET /api/admin/diagnostics/action-logs
func (h *DiagnosticsHandler) GetActionLogs(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "failed"
	}

	sinceStr := r.URL.Query().Get("since")
	if sinceStr == "" {
		sinceStr = "24h"
	}
	dur, err := time.ParseDuration(sinceStr)
	if err != nil {
		respondValidationError(w, r, "invalid 'since' duration")
		return
	}

	limit := 25
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	opts := repository.RecentExecutionLogsOpts{
		Since: time.Now().Add(-dur),
		Limit: limit,
	}
	switch mode {
	case "failed":
		opts.Status = "failed"
	case "slowest":
		opts.SortBy = "duration"
	default:
		respondValidationError(w, r, "mode must be 'failed' or 'slowest'")
		return
	}

	logs, err := h.actionRepo.GetRecentExecutionLogs(opts)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if logs == nil {
		logs = []*models.ActionExecutionLog{}
	}
	respondJSONOK(w, logs)
}

// GetWebhookDeliveries returns recent outbound webhook delivery rows.
//
// Query params:
//   - status:     "" (any), "failed", or "success"
//   - channel_id: optional integer to scope to one channel
//   - since:      duration string (default "24h")
//   - limit:      max rows (default 25, capped at 200)
//
// GET /api/admin/diagnostics/webhook-deliveries
func (h *DiagnosticsHandler) GetWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	since, err := parseSinceDuration(q.Get("since"), 24*time.Hour)
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	limit := 25
	if v := q.Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	channelID := 0
	if v := q.Get("channel_id"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			channelID = parsed
		}
	}

	opts := repository.RecentDeliveriesOpts{
		Status:    q.Get("status"),
		ChannelID: channelID,
		Since:     time.Now().Add(-since),
		Limit:     limit,
	}
	if opts.Status != "" && opts.Status != "failed" && opts.Status != "success" {
		respondValidationError(w, r, "status must be 'failed' or 'success'")
		return
	}

	rows, err := h.deliveryRepo.GetRecent(opts)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if rows == nil {
		rows = []*models.WebhookDelivery{}
	}
	respondJSONOK(w, rows)
}

// GetWebhookStats returns per-channel delivery aggregates for a time window.
//
// Query params:
//   - since: duration string (default "24h")
//
// GET /api/admin/diagnostics/webhook-stats
func (h *DiagnosticsHandler) GetWebhookStats(w http.ResponseWriter, r *http.Request) {
	since, err := parseSinceDuration(r.URL.Query().Get("since"), 24*time.Hour)
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	stats, err := h.deliveryRepo.Stats(time.Now().Add(-since))
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if stats == nil {
		stats = []*repository.ChannelDeliveryStats{}
	}
	respondJSONOK(w, stats)
}

// PurgeWebhookDeliveriesRequest is the body for the manual purge endpoint.
type PurgeWebhookDeliveriesRequest struct {
	OlderThan string `json:"older_than"` // duration string, e.g. "30d", "168h"
}

// PurgeWebhookDeliveries deletes delivery rows older than the requested cutoff.
//
// Body: { "older_than": "30d" }  (or any Go-style duration; "d" = 24h here)
//
// POST /api/admin/diagnostics/webhook-deliveries/purge
func (h *DiagnosticsHandler) PurgeWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeJSON[PurgeWebhookDeliveriesRequest](w, r)
	if !ok {
		return
	}
	dur, err := parseExtendedDuration(req.OlderThan)
	if err != nil {
		respondValidationError(w, r, "invalid 'older_than' duration: "+err.Error())
		return
	}
	if dur < time.Hour {
		respondValidationError(w, r, "'older_than' must be at least 1h to avoid wiping live data")
		return
	}

	cutoff := time.Now().Add(-dur)
	rows, err := h.deliveryRepo.Purge(r.Context(), cutoff)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	h.auditPurge(r, logger.ActionDiagnosticsWebhookDeliveriesPurge, req.OlderThan, cutoff, rows)
	respondJSONOK(w, map[string]int64{"deleted": rows})
}

// GetSchedulerRuns returns recent in-process scheduler tick history.
//
// Query params:
//   - scheduler: "" (any), "briefing", "email", "recurrence", "notification"
//   - status:    "" (any), "success", or "failed"
//   - since:     duration string (default "24h")
//   - limit:     max rows (default 25, capped at 200)
//
// GET /api/admin/diagnostics/scheduler-runs
func (h *DiagnosticsHandler) GetSchedulerRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	since, err := parseSinceDuration(q.Get("since"), 24*time.Hour)
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	limit := 25
	if v := q.Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	opts := repository.RecentSchedulerRunsOpts{
		Scheduler: q.Get("scheduler"),
		Status:    q.Get("status"),
		Since:     time.Now().Add(-since),
		Limit:     limit,
	}
	if opts.Status != "" && opts.Status != "success" && opts.Status != "failed" {
		respondValidationError(w, r, "status must be 'success' or 'failed'")
		return
	}

	runs, err := h.schedulerRunRepo.GetRecent(opts)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if runs == nil {
		runs = []*models.SchedulerRun{}
	}
	respondJSONOK(w, runs)
}

// GetSchedulerStats returns per-scheduler aggregates for a time window.
//
// Query params:
//   - since: duration string (default "24h")
//
// GET /api/admin/diagnostics/scheduler-stats
func (h *DiagnosticsHandler) GetSchedulerStats(w http.ResponseWriter, r *http.Request) {
	since, err := parseSinceDuration(r.URL.Query().Get("since"), 24*time.Hour)
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}
	stats, err := h.schedulerRunRepo.Stats(time.Now().Add(-since))
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	if stats == nil {
		stats = []*repository.SchedulerStats{}
	}
	respondJSONOK(w, stats)
}

// PurgeSchedulerRuns deletes scheduler run rows older than the requested cutoff.
//
// Body: { "older_than": "30d" }
//
// POST /api/admin/diagnostics/scheduler-runs/purge
func (h *DiagnosticsHandler) PurgeSchedulerRuns(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeJSON[PurgeWebhookDeliveriesRequest](w, r)
	if !ok {
		return
	}
	dur, err := parseExtendedDuration(req.OlderThan)
	if err != nil {
		respondValidationError(w, r, "invalid 'older_than' duration: "+err.Error())
		return
	}
	if dur < time.Hour {
		respondValidationError(w, r, "'older_than' must be at least 1h to avoid wiping live data")
		return
	}

	cutoff := time.Now().Add(-dur)
	rows, err := h.schedulerRunRepo.Purge(r.Context(), cutoff)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	h.auditPurge(r, logger.ActionDiagnosticsSchedulerRunsPurge, req.OlderThan, cutoff, rows)
	respondJSONOK(w, map[string]int64{"deleted": rows})
}

func (h *DiagnosticsHandler) auditPurge(r *http.Request, action, olderThan string, cutoff time.Time, rows int64) {
	if h.auditor == nil {
		return
	}
	user := utils.GetCurrentUser(r)
	if user == nil {
		return
	}
	h.auditor.LogWithDetails(r, user, action, logger.ResourceDiagnostics, nil, "", map[string]interface{}{
		"older_than": olderThan,
		"cutoff":     cutoff.Format(time.RFC3339),
		"deleted":    rows,
	})
}

// LLMProviderConnectionStatus pairs an enabled connection with whether its
// configured model is still present in the provider's cached catalog.
//
// ModelStillInCatalog is nil when the catalog hasn't been refreshed yet
// (the UI then shows "unknown — refresh to check") and false when the
// model has dropped — the drift signal that surfaced the Gemini deprecation
// in the first place.
type LLMProviderConnectionStatus struct {
	ID                  int    `json:"id"`
	Name                string `json:"name"`
	Model               string `json:"model"`
	ModelStillInCatalog *bool  `json:"model_still_in_catalog,omitempty"`
}

// LLMProviderStatus is one row in the diagnostics widget: provider metadata,
// the cache state, and the list of enabled connections (with drift flags).
type LLMProviderStatus struct {
	Type              llm.ProviderType              `json:"type"`
	Name              string                        `json:"name"`
	HasDynamicModels  bool                          `json:"has_dynamic_models"`
	LastRefreshedAt   *time.Time                    `json:"last_refreshed_at,omitempty"`
	LastError         string                        `json:"last_error,omitempty"`
	ModelsCachedCount int                           `json:"models_cached_count"`
	Connections       []LLMProviderConnectionStatus `json:"connections"`
}

// GetLLMProviderStatus returns per-provider catalog cache state plus an
// enabled-connection drift check (configured model present in the cached
// catalog?). This is the System Diagnostics counterpart to the per-provider
// rows already shown in Settings → LLM Connections — same data, but explicitly
// surfaces drift instead of expecting admins to spot it.
//
// GET /api/admin/diagnostics/llm-providers
func (h *DiagnosticsHandler) GetLLMProviderStatus(w http.ResponseWriter, r *http.Request) {
	if h.llmManager == nil || h.llmCache == nil {
		respondInternalError(w, r, fmt.Errorf("llm dependencies not configured"))
		return
	}

	connections, err := h.llmManager.ListConnections()
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	byProvider := make(map[llm.ProviderType][]llm.ConnectionInfo, len(connections))
	for _, c := range connections {
		if !c.IsEnabled {
			continue
		}
		byProvider[c.ProviderType] = append(byProvider[c.ProviderType], c)
	}

	providers := llm.KnownProviders()
	out := make([]LLMProviderStatus, 0, len(providers))
	for _, p := range providers {
		entry := LLMProviderStatus{
			Type:             p.Type,
			Name:             p.Name,
			HasDynamicModels: p.HasDynamicModels(),
			Connections:      []LLMProviderConnectionStatus{},
		}
		var cachedIDs map[string]struct{}
		if p.HasDynamicModels() {
			cached, cerr := h.llmCache.Get(p.Type)
			if cerr != nil {
				slog.Warn("read model cache", slog.String("provider", string(p.Type)), slog.Any("error", cerr))
			} else {
				entry.LastRefreshedAt = cached.LastRefreshedAt
				entry.LastError = cached.LastError
				entry.ModelsCachedCount = len(cached.Models)
				if cached.LastRefreshedAt != nil {
					cachedIDs = make(map[string]struct{}, len(cached.Models))
					for _, m := range cached.Models {
						cachedIDs[m.ID] = struct{}{}
					}
				}
			}
		}
		for _, c := range byProvider[p.Type] {
			cs := LLMProviderConnectionStatus{ID: c.ID, Name: c.Name, Model: c.Model}
			if cachedIDs != nil {
				_, ok := cachedIDs[c.Model]
				cs.ModelStillInCatalog = &ok
			}
			entry.Connections = append(entry.Connections, cs)
		}
		out = append(out, entry)
	}
	respondJSONOK(w, out)
}

// BriefingFailureBucket counts failed briefings under one error class.
type BriefingFailureBucket struct {
	Class         string `json:"class"`
	Count         int    `json:"count"`
	LatestMessage string `json:"latest_message,omitempty"`
}

// BriefingFailureRow is one row in the recent-failures table, paired with its
// classifier verdict so the frontend can render badges without re-classifying.
type BriefingFailureRow struct {
	ID           int    `json:"id"`
	UserID       int    `json:"user_id"`
	Date         string `json:"date"`
	Error        string `json:"error"`
	ClassifiedAs string `json:"classified_as"`
	CreatedAt    string `json:"created_at"`
}

// GetBriefingFailures returns recent failed daily_briefings rows bucketed by
// classifier verdict. The user reported a Gemini-deprecation 404 buried in
// scheduler logs — this surfaces those buckets in the admin UI.
//
// Query params:
//   - since: duration string (default "24h")
//
// GET /api/admin/diagnostics/briefing-failures
func (h *DiagnosticsHandler) GetBriefingFailures(w http.ResponseWriter, r *http.Request) {
	if h.aiRepo == nil {
		respondInternalError(w, r, fmt.Errorf("ai repository not configured"))
		return
	}
	since, err := parseSinceDuration(r.URL.Query().Get("since"), 24*time.Hour)
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	rows, err := h.aiRepo.ListFailedBriefings(time.Now().Add(-since), 100)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	// Stable bucket order so the frontend can render a fixed grid even when
	// some classes have zero hits.
	bucketOrder := []llm.ErrorClass{
		llm.ErrorClassModelNotFound,
		llm.ErrorClassAuthFailed,
		llm.ErrorClassRateLimited,
		llm.ErrorClassServerError,
		llm.ErrorClassConnectionFailed,
		llm.ErrorClassOther,
	}
	buckets := make(map[llm.ErrorClass]*BriefingFailureBucket, len(bucketOrder))
	for _, c := range bucketOrder {
		buckets[c] = &BriefingFailureBucket{Class: string(c)}
	}

	recent := make([]BriefingFailureRow, 0, len(rows))
	for i, row := range rows {
		cls := llm.ClassifyError(row.Error)
		b := buckets[cls]
		b.Count++
		if b.LatestMessage == "" {
			b.LatestMessage = row.Error
		}
		if i < 25 {
			recent = append(recent, BriefingFailureRow{
				ID:           row.ID,
				UserID:       row.UserID,
				Date:         row.Date,
				Error:        row.Error,
				ClassifiedAs: string(cls),
				CreatedAt:    row.CreatedAt,
			})
		}
	}

	bucketList := make([]BriefingFailureBucket, 0, len(bucketOrder))
	for _, c := range bucketOrder {
		bucketList = append(bucketList, *buckets[c])
	}

	respondJSONOK(w, map[string]interface{}{
		"since":   since.String(),
		"buckets": bucketList,
		"recent":  recent,
	})
}

// parseSinceDuration parses a duration string with a default fallback.
//
//nolint:unparam // def kept on signature for callers that may pass non-default windows in the future
func parseSinceDuration(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := parseExtendedDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid 'since' duration: %w", err)
	}
	return d, nil
}

// parseExtendedDuration parses Go duration strings, additionally treating a
// "d" suffix as days (e.g. "30d" → 30 * 24h). Standard time.ParseDuration does
// not accept "d", but humans expect it for retention windows.
func parseExtendedDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, err
		}
		const maxDays = int64(1<<63-1) / int64(24*time.Hour)
		if int64(n) > maxDays || int64(n) < -maxDays {
			return 0, fmt.Errorf("duration out of range: %s", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
