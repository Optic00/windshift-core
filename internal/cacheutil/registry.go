package cacheutil

import (
	"sort"
	"sync"
	"sync/atomic"

	"github.com/allegro/bigcache/v3"
)

type cacheRegistration struct {
	name      string
	cache     *bigcache.BigCache
	maxBytes  int64
	evictions atomic.Uint64
}

// Snapshot describes one live process-local cache.
type Snapshot struct {
	Name                   string `json:"name"`
	Entries                int    `json:"entries"`
	AllocatedCapacityBytes int64  `json:"allocated_capacity_bytes"`
	MaximumCapacityBytes   int64  `json:"maximum_capacity_bytes"`
	Hits                   int64  `json:"hits"`
	Misses                 int64  `json:"misses"`
	NoSpaceEvictions       uint64 `json:"no_space_evictions"`
}

var cacheRegistry = struct {
	sync.RWMutex
	entries map[string]*cacheRegistration
}{entries: make(map[string]*cacheRegistration)}

func registerCache(registration *cacheRegistration) {
	cacheRegistry.Lock()
	cacheRegistry.entries[registration.name] = registration
	cacheRegistry.Unlock()
}

// Snapshots returns stable, name-sorted diagnostics for registered caches.
func Snapshots() []Snapshot {
	cacheRegistry.RLock()
	registrations := make([]*cacheRegistration, 0, len(cacheRegistry.entries))
	for _, registration := range cacheRegistry.entries {
		registrations = append(registrations, registration)
	}
	cacheRegistry.RUnlock()

	snapshots := make([]Snapshot, 0, len(registrations))
	for _, registration := range registrations {
		stats := registration.cache.Stats()
		snapshots = append(snapshots, Snapshot{
			Name:                   registration.name,
			Entries:                registration.cache.Len(),
			AllocatedCapacityBytes: int64(registration.cache.Capacity()),
			MaximumCapacityBytes:   registration.maxBytes,
			Hits:                   stats.Hits,
			Misses:                 stats.Misses,
			NoSpaceEvictions:       registration.evictions.Load(),
		})
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Name < snapshots[j].Name })
	return snapshots
}
