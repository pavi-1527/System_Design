package lrucache

// ---- Stage 3: observability and smarter eviction (minute 40) ---------------
//
// Contract:
//   - When a Put of a new key finds the cache full, it evicts an EXPIRED
//     entry if one exists (any expired entry; the one that expired earliest
//     is the natural choice) before it evicts a live least-recently-used
//     entry. A full cache must never throw away live data while expired data
//     is lingering. Finding that entry must not cost O(n): a test fills the
//     cache with 80,000 entries and does 40,000 such Puts under a time cap.
//   - OnEvict callbacks run after the cache's lock is released, on the
//     goroutine that triggered the removal. A callback may call back into
//     the cache (Get, Len, Put...) without deadlocking.
//   - Reasons: Capacity (evicted to make room), Expired (found expired, by
//     Get, Put, Delete or RemoveExpired), Deleted (explicit Delete of a live
//     entry). Replacing the value of an existing key calls nothing.
//   - Delete on an entry that is already expired removes it as Expired and
//     returns false: logically it was already gone.
//   - Stats counts hits and misses seen by Get (an expired entry is a miss
//     AND an expiration), capacity evictions, and expirations.

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
	return func(c *Cache[K, V]) {
		// TODO(lab)
		_ = fn
	}
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
	// TODO(lab)
	return Stats{}
}
