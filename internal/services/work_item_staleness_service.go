package services

import (
	"errors"
	"fmt"
	"strconv"

	"windshift/internal/database"
	"windshift/internal/repository"
)

const (
	workItemStalenessSettingKey = "items.stale_after_days"
	defaultStaleAfterDays       = 30
	minStaleAfterDays           = 1
	maxStaleAfterDays           = 365
)

// ErrInvalidWorkItemStalenessThreshold indicates a threshold outside the supported range.
var ErrInvalidWorkItemStalenessThreshold = errors.New("work item staleness threshold must be between 1 and 365 days")

// WorkItemStalenessSettings controls when unfinished work items are considered stale.
type WorkItemStalenessSettings struct {
	StaleAfterDays int `json:"stale_after_days"`
}

// WorkItemStalenessService owns the shared work item staleness setting.
type WorkItemStalenessService struct {
	settings *repository.SystemSettingRepository
}

// NewWorkItemStalenessService creates a work item staleness settings service.
func NewWorkItemStalenessService(db database.Database) *WorkItemStalenessService {
	return &WorkItemStalenessService{settings: repository.NewSystemSettingRepository(db)}
}

// DefaultWorkItemStalenessSettings returns the system default.
func DefaultWorkItemStalenessSettings() WorkItemStalenessSettings {
	return WorkItemStalenessSettings{StaleAfterDays: defaultStaleAfterDays}
}

// Get returns the configured threshold or the system default.
func (s *WorkItemStalenessService) Get() (WorkItemStalenessSettings, error) {
	settings := DefaultWorkItemStalenessSettings()
	value, ok, err := s.settings.GetValue(workItemStalenessSettingKey)
	if err != nil {
		return settings, fmt.Errorf("load work item staleness settings: %w", err)
	}
	if !ok {
		return settings, nil
	}

	days, valid := parseStaleAfterDays(value)
	if !valid {
		return settings, nil
	}
	settings.StaleAfterDays = days
	return settings, nil
}

func parseStaleAfterDays(value string) (int, bool) {
	days, err := strconv.Atoi(value)
	if err != nil || days < minStaleAfterDays || days > maxStaleAfterDays {
		return 0, false
	}
	return days, true
}

// Update validates and persists the shared threshold.
func (s *WorkItemStalenessService) Update(staleAfterDays int) (WorkItemStalenessSettings, error) {
	if staleAfterDays < minStaleAfterDays || staleAfterDays > maxStaleAfterDays {
		return WorkItemStalenessSettings{}, ErrInvalidWorkItemStalenessThreshold
	}

	err := s.settings.Upsert(
		workItemStalenessSettingKey,
		strconv.Itoa(staleAfterDays),
		"integer",
		"Days without work item activity before unfinished work is considered stale",
		"items",
	)
	if err != nil {
		return WorkItemStalenessSettings{}, fmt.Errorf("save work item staleness settings: %w", err)
	}
	return WorkItemStalenessSettings{StaleAfterDays: staleAfterDays}, nil
}
