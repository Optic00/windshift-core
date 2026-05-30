package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"windshift/internal/database"
	"windshift/internal/llm"
	"windshift/internal/repository"
	"windshift/internal/services"
)

// BriefingScheduler generates daily briefings for all users in the background.
type BriefingScheduler struct {
	db              database.Database
	llmManager      *llm.ConnectionManager
	permService     *services.PermissionService
	timePermService *services.TimePermissionService
	userService     *services.UserReadService
	promptStore     *llm.PromptStore
	runRepo         *repository.SchedulerRunRepository
	ticker          *time.Ticker
	stopChan        chan struct{}
	mu              sync.RWMutex
	running         bool
}

// NewBriefingScheduler creates a new briefing scheduler.
func NewBriefingScheduler(db database.Database, llmManager *llm.ConnectionManager, permService *services.PermissionService, timePermService *services.TimePermissionService, userService *services.UserReadService, promptStore *llm.PromptStore) *BriefingScheduler {
	return &BriefingScheduler{
		db:              db,
		llmManager:      llmManager,
		permService:     permService,
		timePermService: timePermService,
		userService:     userService,
		promptStore:     promptStore,
		runRepo:         repository.NewSchedulerRunRepository(db),
		ticker:          time.NewTicker(6 * time.Hour),
		stopChan:        make(chan struct{}),
	}
}

// Start begins the briefing scheduler.
func (bs *BriefingScheduler) Start() {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	if bs.running {
		return
	}

	bs.running = true
	slog.Info("briefing scheduler started", slog.String("component", "scheduler"), slog.String("interval", "6h"))

	go bs.schedulerLoop()
}

// Stop stops the briefing scheduler.
func (bs *BriefingScheduler) Stop() {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	if !bs.running {
		return
	}

	bs.running = false
	bs.ticker.Stop()
	close(bs.stopChan)
	slog.Info("briefing scheduler stopped", slog.String("component", "scheduler"))
}

func (bs *BriefingScheduler) schedulerLoop() {
	bs.safeGenerateAllBriefings()

	for {
		select {
		case <-bs.ticker.C:
			bs.safeGenerateAllBriefings()
		case <-bs.stopChan:
			return
		}
	}
}

func (bs *BriefingScheduler) safeGenerateAllBriefings() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("briefing: panic in generateAllBriefings", slog.Any("panic", r))
		}
	}()
	bs.generateAllBriefings()
}

// last review: ser, 300526
func (bs *BriefingScheduler) generateAllBriefings() {
	start := time.Now()
	var usersProcessed int
	var runErr error
	defer recordSchedulerRun(bs.runRepo, "briefing", start, &usersProcessed, &runErr)

	// Check per-feature config for daily_briefing
	llmClient, err := bs.llmManager.ResolveForFeature("daily_briefing")
	if err != nil {
		slog.Info("briefing: generation skipped", slog.Any("reason", err))
		return
	}
	if llmClient == nil || !llmClient.Available() {
		slog.Info("briefing: generation skipped, AI not available")
		return
	}

	// Check schedule: "every_6h" allows regeneration on the same day
	regenerate := false
	if cfg, err := llm.LoadAIFeaturesConfig(bs.db); err == nil {
		regenerate = cfg["daily_briefing"].Schedule == "every_6h"
	}

	// Get active users – empty-context filtering happens in generateBriefingForUser
	users, err := bs.userService.ListAll()
	if err != nil {
		slog.Error("failed to list users for briefing generation", slog.Any("error", err))
		runErr = err
		return
	}
	usersProcessed = len(users)

	slog.Info("generating daily briefings",
		slog.String("component", "scheduler"),
		slog.Int("users", len(users)),
		slog.Int("delay_seconds", 3),
	)

	failures := 0
	for i, u := range users {
		ok := func() (succeeded bool) {
			succeeded = true
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic in briefing generation", slog.Int("user_id", u.ID), slog.Any("panic", r))
					succeeded = false
				}
			}()
			tz := u.Timezone
			if tz == "" {
				tz = "UTC"
			}
			return bs.generateBriefingForUser(llmClient, u.ID, u.FirstName, tz, regenerate)
		}()
		if !ok {
			failures++
		}
		if i < len(users)-1 {
			time.Sleep(3 * time.Second)
		}
	}

	// Surface aggregate failures to scheduler_runs. A panic-recovery path returns
	// false too, so the success metric stays honest even when individual users
	// hit LLM errors or DB hiccups.
	if failures > 0 {
		runErr = fmt.Errorf("%d of %d daily briefings failed", failures, len(users))
	}
}

// generateBriefingForUser returns true on success (or when nothing needs doing).
// It returns false only when the actual generation step (LLM call or storage)
// failed, so the caller can roll up failures into the scheduler_run record.
// last review: ser, 300526
func (bs *BriefingScheduler) generateBriefingForUser(llmClient llm.Client, userID int, firstName, timezone string, regenerate bool) bool {
	// Compute "today" / "yesterday" + their day boundaries in the *user's*
	// timezone, not the server's. The previous server-local calculation meant a
	// user in PT could get yesterday's briefing repeated after their local
	// midnight, or could miss their own Sunday-evening activity because the 24h
	// window was anchored at server midnight UTC.
	loc, err := time.LoadLocation(timezone)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	nowLocal := time.Now().In(loc)
	todayStart := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	today := todayStart.Format("2006-01-02")

	// Skip if today's briefing already exists (successful), unless regeneration is enabled
	if !regenerate {
		var exists int
		if err := bs.db.QueryRow("SELECT 1 FROM daily_briefings WHERE user_id = ? AND date = ? AND error IS NULL", userID, today).Scan(&exists); err == nil {
			slog.Debug("briefing: already generated today", slog.Int("user_id", userID))
			return true
		}
	}

	start := time.Now()

	// Get accessible workspace IDs (gated-aware item.view check, shared with the
	// HTTP and MCP surfaces via PermissionService).
	accessibleWSIDs, err := bs.permService.AccessibleWorkspaceIDs(userID)
	if err != nil || len(accessibleWSIDs) == 0 {
		slog.Info("briefing: no accessible workspaces",
			slog.Int("user_id", userID),
			slog.Int("workspaces", len(accessibleWSIDs)),
			slog.Any("error", err),
		)
		// "No accessible workspaces" isn't a generation failure — the user simply
		// has nothing to brief on. Don't penalize the run.
		return err == nil
	}

	itemRepo := repository.NewItemRepository(bs.db)
	lookups := repository.NewLookupRepository(bs.db).LoadNameMaps()

	// Gather context: recent activity
	var activityLines []string
	changes, err := itemRepo.RecentItemChanges(accessibleWSIDs, yesterdayStart, 50)
	if err != nil {
		slog.Warn("briefing: changes query failed", slog.Int("user_id", userID), slog.Any("error", err))
	}
	for _, c := range changes {
		displayField := strings.TrimSuffix(c.FieldName, "_id")
		displayOld := resolveLookup(lookups, c.FieldName, c.OldValue)
		displayNew := resolveLookup(lookups, c.FieldName, c.NewValue)
		line := fmt.Sprintf("- [%s] %s: %s changed '%s'", c.ItemKey, c.Title, c.ChangedBy, displayField)
		if displayOld != "" || displayNew != "" {
			line += fmt.Sprintf(" from '%s' to '%s'", displayOld, displayNew)
		}
		activityLines = append(activityLines, line)
	}

	// Gather context: recent comments
	var commentLines []string
	comments, err := itemRepo.RecentComments(accessibleWSIDs, yesterdayStart, 30)
	if err != nil {
		slog.Warn("briefing: comments query failed", slog.Int("user_id", userID), slog.Any("error", err))
	}
	for _, c := range comments {
		content := c.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		commentLines = append(commentLines, fmt.Sprintf("- [%s] %s commented on '%s': %s", c.ItemKey, c.Author, c.Title, content))
	}

	// Gather context: assigned open items, plus everything in the user's personal workspaces
	personalWSIDs, err := repository.NewWorkspaceRepository(bs.db).ListActivePersonalWorkspaceIDs(userID)
	if err != nil {
		slog.Warn("briefing: personal workspaces query failed", slog.Int("user_id", userID), slog.Any("error", err))
		personalWSIDs = nil
	}

	var itemLines []string
	openItems, err := itemRepo.OpenItemsForUser(accessibleWSIDs, personalWSIDs, userID, 50)
	if err != nil {
		slog.Warn("briefing: items query failed", slog.Int("user_id", userID), slog.Any("error", err))
	}
	for _, it := range openItems {
		line := fmt.Sprintf("- [%s-%d] %s", it.WorkspaceKey, it.ItemNumber, it.Title)
		if it.Priority != "" {
			line += fmt.Sprintf(" | Priority: %s", it.Priority)
		}
		if it.DueDate != "" {
			line += fmt.Sprintf(" | Due: %s", it.DueDate)
		} else {
			line += " | Due: none"
		}
		if it.Status != "" {
			line += fmt.Sprintf(" | Status: %s", it.Status)
		}
		if it.MilestoneName != "" {
			ms := fmt.Sprintf(" | Milestone: %s", it.MilestoneName)
			if it.MilestoneTargetDate != "" {
				ms += fmt.Sprintf(" (target: %s)", it.MilestoneTargetDate)
			}
			line += ms
		}
		if it.IterationName != "" {
			iter := fmt.Sprintf(" | Iteration: %s", it.IterationName)
			if it.IterationEndDate != "" {
				iter += fmt.Sprintf(" (ends: %s)", it.IterationEndDate)
			}
			line += iter
		}
		itemLines = append(itemLines, line)
	}

	// Gather context: yesterday's worklogs. time_worklogs.date is INTEGER (Unix
	// epoch), so we need actual instants, not date strings — and they must be
	// anchored at midnight in the *user's* tz, not midnight UTC, otherwise users
	// outside UTC see worklogs from the wrong window (or none at all).
	var worklogLines []string
	if bs.timePermService != nil {
		wlRows, err := bs.db.Query(`SELECT tw.description, tw.duration_minutes, tp.name
			FROM time_worklogs tw
			JOIN time_projects tp ON tw.project_id = tp.id
			WHERE tw.user_id = ? AND tw.date >= ? AND tw.date < ?
			ORDER BY tw.date DESC`,
			userID, yesterdayStart.Unix(), todayStart.Unix())
		if err != nil {
			slog.Warn("briefing: worklogs query failed", slog.Int("user_id", userID), slog.Any("error", err))
		} else {
			defer func() { _ = wlRows.Close() }()
			for wlRows.Next() {
				var desc, projectName string
				var durationMins int
				if err := wlRows.Scan(&desc, &durationMins, &projectName); err == nil {
					worklogLines = append(worklogLines, fmt.Sprintf("- %s (%s): %dm", desc, projectName, durationMins))
				}
			}
			if err := wlRows.Err(); err != nil {
				slog.Warn("briefing: worklogs iteration failed", slog.Int("user_id", userID), slog.Any("error", err))
			}
		}
	}

	// Build the data block
	var contextParts []string
	if len(activityLines) > 0 {
		contextParts = append(contextParts, "### Recent Changes (last 24h)\n"+strings.Join(activityLines, "\n"))
	}
	if len(commentLines) > 0 {
		contextParts = append(contextParts, "### Recent Comments (last 24h)\n"+strings.Join(commentLines, "\n"))
	}
	if len(itemLines) > 0 {
		contextParts = append(contextParts, "### Your Open Items\n"+strings.Join(itemLines, "\n"))
	}
	if len(worklogLines) > 0 {
		contextParts = append(contextParts, "### Yesterday's Worklogs\n"+strings.Join(worklogLines, "\n"))
	}

	slog.Info("briefing: context gathered",
		slog.Int("user_id", userID),
		slog.Int("changes", len(activityLines)),
		slog.Int("comments", len(commentLines)),
		slog.Int("items", len(itemLines)),
		slog.Int("worklogs", len(worklogLines)),
	)

	if len(contextParts) == 0 {
		slog.Info("briefing: no context found", slog.Int("user_id", userID))
		bs.storeBriefing(userID, today, "", time.Since(start).Milliseconds(), "")
		return true
	}

	systemPrompt := bs.promptStore.Get(llm.PromptDailyBriefing)

	userPrompt := fmt.Sprintf("Good morning %s! Today is %s (%s timezone).\n\nHere is your project data:\n\n%s",
		firstName, nowLocal.Format("Monday, January 2, 2006"), timezone, strings.Join(contextParts, "\n\n"))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	resp, err := llmClient.ChatCompletion(ctx, llm.ChatCompletionRequest{
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.3,
		MaxTokens:   2048,
	})

	durationMs := time.Since(start).Milliseconds()

	if err != nil || len(resp.Choices) == 0 {
		errMsg := "no response from LLM"
		if err != nil {
			errMsg = err.Error()
		}
		slog.Warn("briefing generation failed", slog.Int("user_id", userID), slog.String("error", errMsg))
		bs.storeBriefing(userID, today, "", durationMs, errMsg)
		return false
	}

	content := resp.Choices[0].Message.Content
	bs.storeBriefing(userID, today, content, durationMs, "")

	slog.Info("briefing: generated",
		slog.Int("user_id", userID),
		slog.Int("content_len", len(content)),
		slog.Int64("duration_ms", durationMs),
	)
	return true
}

// resolveLookup returns a human-readable value for the given history field/raw
// value pair, using the centralized id→name maps. Non-*_id fields and
// unparseable values pass through unchanged.
func resolveLookup(maps *repository.NameMaps, field, raw string) string {
	if raw == "" {
		return raw
	}
	id, err := strconv.Atoi(raw)
	if err != nil {
		return raw
	}
	lookup := func(m map[int]string) string {
		if name, ok := m[id]; ok && name != "" {
			return name
		}
		return fmt.Sprintf("unknown (%d)", id)
	}
	switch field {
	case "status_id":
		return lookup(maps.Statuses)
	case "priority_id":
		return lookup(maps.Priorities)
	case "milestone_id":
		return lookup(maps.Milestones)
	case "iteration_id":
		return lookup(maps.Iterations)
	case "assignee_id", "creator_id":
		return lookup(maps.Users)
	}
	return raw
}

func (bs *BriefingScheduler) storeBriefing(userID int, date, content string, durationMs int64, errMsg string) {
	var errVal interface{}
	if errMsg != "" {
		errVal = errMsg
	}

	_, err := bs.db.Exec(`INSERT INTO daily_briefings (user_id, date, content, generation_duration_ms, error)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id, date) DO UPDATE SET
		content = excluded.content, generation_duration_ms = excluded.generation_duration_ms,
		error = excluded.error, updated_at = CURRENT_TIMESTAMP`,
		userID, date, content, durationMs, errVal)
	if err != nil {
		slog.Error("failed to store briefing", slog.Int("user_id", userID), slog.Any("error", err))
	}
}
