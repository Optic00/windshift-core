package config

import "fmt"

const (
	DefaultMemoryLimitMB  = 2048
	MinimumMemoryLimitMB  = 512
	maximumCacheBudgetMB  = 512
	securityCacheBudgetMB = 20
)

// MemoryBudget is the resolved process, Go heap, and per-cache memory plan.
// All values are in MiB except GoLimitBytes, which is ready for
// runtime/debug.SetMemoryLimit.
type MemoryBudget struct {
	ProcessLimitMB      int   `json:"process_limit_mb"`
	GoLimitBytes        int64 `json:"go_limit_bytes"`
	CacheLimitMB        int   `json:"cache_limit_mb"`
	ItemCacheMB         int   `json:"item_cache_mb"`
	PermissionCacheMB   int   `json:"permission_cache_mb"`
	NotificationCacheMB int   `json:"notification_cache_mb"`
	ActivityCacheMB     int   `json:"activity_cache_mb"`
	APITokenCacheMB     int   `json:"api_token_cache_mb"`
	SessionCacheMB      int   `json:"session_cache_mb"`
	SCIMTokenCacheMB    int   `json:"scim_token_cache_mb"`
}

// SplitSSHCacheBudget divides one configured cache allocation between the
// primary HTTP server and the optional SSH server without increasing the
// process-wide maximum. The primary server receives the remainder for odd
// allocations.
func SplitSSHCacheBudget(totalMB int, sshEnabled bool) (primaryMB, sshMB int) {
	if !sshEnabled {
		return totalMB, 0
	}
	sshMB = totalMB / 2
	return totalMB - sshMB, sshMB
}

// ResolveMemoryBudget validates a process limit and derives bounded cache
// allocations. Zero selects the production default so manually constructed
// Config values and existing embedders remain backward compatible.
func ResolveMemoryBudget(limitMB int) (MemoryBudget, error) {
	if limitMB == 0 {
		limitMB = DefaultMemoryLimitMB
	}
	if limitMB < MinimumMemoryLimitMB {
		return MemoryBudget{}, fmt.Errorf("memory limit must be at least %d MiB", MinimumMemoryLimitMB)
	}

	cacheMB := min(limitMB/4, maximumCacheBudgetMB)
	remaining := cacheMB - securityCacheBudgetMB
	itemMB := remaining * 40 / 100
	permissionMB := remaining * 25 / 100
	notificationMB := remaining * 25 / 100
	activityMB := remaining - itemMB - permissionMB - notificationMB

	return MemoryBudget{
		ProcessLimitMB:      limitMB,
		GoLimitBytes:        int64(limitMB) * 1024 * 1024 * 4 / 5,
		CacheLimitMB:        cacheMB,
		ItemCacheMB:         itemMB,
		PermissionCacheMB:   permissionMB,
		NotificationCacheMB: notificationMB,
		ActivityCacheMB:     activityMB,
		APITokenCacheMB:     8,
		SessionCacheMB:      8,
		SCIMTokenCacheMB:    4,
	}, nil
}
