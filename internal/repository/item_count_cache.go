package repository

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"windshift/internal/database"

	"golang.org/x/sync/singleflight"
)

// Item list totals are shared by the many authenticated readers of the same
// workspace. A short TTL absorbs the thundering herd caused by a page refresh
// while mutation invalidation keeps local writes immediately observable.
const itemListCountCacheTTL = 2 * time.Second

type itemListCountCacheEntry struct {
	count     int
	expiresAt time.Time
}

var itemListCountCache = struct {
	sync.Mutex
	entries map[string]itemListCountCacheEntry
}{
	entries: make(map[string]itemListCountCacheEntry),
}

var itemListCountFlights singleflight.Group

func itemListCountCacheDBKey(db database.Database) string {
	value := reflect.ValueOf(db)
	if value.IsValid() && value.Kind() == reflect.Pointer {
		return fmt.Sprintf("%T:%x", db, value.Pointer())
	}
	return fmt.Sprintf("%T", db)
}

func itemListCountCacheKey(db database.Database, workspaceID int) string {
	return fmt.Sprintf("%s:workspace:%d", itemListCountCacheDBKey(db), workspaceID)
}

func cachedItemListCount(ctx context.Context, db database.Database, workspaceID int, query string, args ...any) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	key := itemListCountCacheKey(db, workspaceID)
	load := func() (any, error) {
		now := time.Now()
		itemListCountCache.Lock()
		entry, ok := itemListCountCache.entries[key]
		if ok && now.Before(entry.expiresAt) {
			itemListCountCache.Unlock()
			return entry.count, nil
		}
		itemListCountCache.Unlock()

		var count int
		if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			return 0, err
		}

		itemListCountCache.Lock()
		itemListCountCache.entries[key] = itemListCountCacheEntry{
			count:     count,
			expiresAt: time.Now().Add(itemListCountCacheTTL),
		}
		itemListCountCache.Unlock()
		return count, nil
	}

	result := itemListCountFlights.DoChan(key, load)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case response := <-result:
		if response.Err != nil {
			return 0, response.Err
		}
		count, ok := response.Val.(int)
		if !ok {
			return 0, fmt.Errorf("cached item list count has unexpected type %T", response.Val)
		}
		return count, nil
	}
}

// invalidateItemListCountCache drops totals for one database after a local
// item mutation. Other replicas are bounded by the short TTL above.
func invalidateItemListCountCache(db database.Database) {
	prefix := itemListCountCacheDBKey(db) + ":workspace:"
	itemListCountCache.Lock()
	for key := range itemListCountCache.entries {
		if strings.HasPrefix(key, prefix) {
			delete(itemListCountCache.entries, key)
		}
	}
	itemListCountCache.Unlock()
}

// InvalidateItemListCountCache makes a committed item mutation immediately
// visible to the unfiltered workspace-list total cache.
func InvalidateItemListCountCache(db database.Database) {
	invalidateItemListCountCache(db)
}
