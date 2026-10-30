package lrucache

import "time"

// Reason says why an entry left the cache.
type Reason int

// Eviction reasons passed to the OnEvict callback.
const (
	Capacity Reason = iota + 1
	Expired
	Deleted
)

// WithOnEvict registers fn to be called for every removed entry.
func WithOnEvict[K comparable, V any](fn func(key K, value V, reason Reason)) Option[K, V] {
	return func(c *Cache[K, V]) { c.onEvict = fn }
}

// Stats is a point-in-time copy of the cache's counters.
type Stats struct {
	Hits        uint64
	Misses      uint64
	Evictions   uint64 // removed for capacity
	Expirations uint64 // removed because they were expired
}

// Stats returns a snapshot of the counters.
func (c *Cache[K, V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// removal is a deferred OnEvict notification.
type removal[K comparable, V any] struct {
	key    K
	value  V
	reason Reason
}

// notify runs after the cache's lock is released, so fn may call back into
// the cache. fn was read under the lock, so no data race with construction.
func notify[K comparable, V any](fn func(K, V, Reason), rs []removal[K, V]) {
	if fn == nil {
		return
	}
	for _, r := range rs {
		fn(r.key, r.value, r.reason)
	}
}

// evictOne makes room for one new entry: an expired entry if any exists
// (the one that expired first, from the heap root), else the live LRU.
// Requires c.mu and a non-empty cache.
func (c *Cache[K, V]) evictOne(now time.Time) removal[K, V] {
	if len(c.expiry) > 0 && c.expired(c.expiry[0], now) {
		return c.remove(c.expiry[0], Expired)
	}
	return c.remove(c.root.prev, Capacity)
}
