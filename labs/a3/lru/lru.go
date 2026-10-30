// Package lru is a bounded, concurrency-safe LRU cache with per-entry TTLs
// and optional cost (byte) weighting. Keys are strings; values are generic.
//
// It is the lab for A3 Unit 1 §3 (eviction). Suggested order: the list
// primitives (unlink, pushFront), then Get/Set/Delete with entry-count
// eviction, then TTL, then cost weighting, then the OnEvict callback and
// stats. Run: go test -race ./labs/a3/lru/
//
// Behaviour (the tests depend on every line):
//
//   - Get is a hit for a live entry: it returns the value, promotes the entry
//     to most-recently-used and counts a hit. Anything else is a miss (an
//     absent key, or an expired entry, which is also removed on the spot and
//     counted as an expiration). A hit does NOT extend the entry's TTL.
//   - Set inserts or overwrites. Either way the entry becomes
//     most-recently-used. Overwriting replaces value, cost and expiry, and is
//     not an eviction.
//   - Capacity has two independent limits, each disabled when zero:
//     MaxEntries (number of entries) and MaxCost (sum of entry costs). After
//     a Set, entries are evicted from the least-recently-used end until both
//     limits hold. The entry just written is never the victim.
//   - A cost <= 0 is treated as 1. An entry whose cost alone exceeds MaxCost
//     is not stored at all: Set returns false, and any older entry under the
//     same key is deleted (it would be stale if left in place).
//   - Expiry: an entry written at time t with TTL d is expired when
//     now >= t+d. ttl == 0 means "use Config.DefaultTTL"; a DefaultTTL of 0
//     means never expire; a negative ttl means never expire for this entry.
//     Expired entries are removed lazily (on Get) or by PurgeExpired.
//   - OnEvict (optional) is called for capacity evictions and expirations,
//     but not for Delete or for overwrites. It is called AFTER the cache's
//     lock is released, so it may call back into the cache.
//   - Peek returns a live value without promoting it and without touching
//     the statistics or removing anything.
//   - Safe for concurrent use. Get mutates (it promotes), so a plain Mutex is
//     required: a read lock around Get is a data race.
package lru

import (
	"sync"
	"time"
)

// Reason says why OnEvict was called.
type Reason int

// Eviction reasons.
const (
	ReasonCapacity Reason = iota + 1 // pushed out to make room
	ReasonExpired                    // TTL elapsed
)

func (r Reason) String() string {
	switch r {
	case ReasonCapacity:
		return "capacity"
	case ReasonExpired:
		return "expired"
	}
	return "unknown"
}

// Config configures a Cache. The zero value is an unbounded cache without TTL.
type Config[V any] struct {
	MaxEntries int           // 0 = no limit on entry count
	MaxCost    int64         // 0 = no limit on total cost
	DefaultTTL time.Duration // 0 = entries never expire unless Set with a TTL
	Now        func() time.Time
	OnEvict    func(key string, value V, reason Reason)
}

// Stats is a snapshot of the counters.
type Stats struct {
	Hits        uint64
	Misses      uint64
	Evictions   uint64 // capacity evictions only
	Expirations uint64 // entries removed because their TTL elapsed
}

// HitRatio returns Hits / (Hits + Misses), or 0 with no lookups. Provided.
func (s Stats) HitRatio() float64 {
	if s.Hits+s.Misses == 0 {
		return 0
	}
	return float64(s.Hits) / float64(s.Hits+s.Misses)
}

// entry is a node of the intrusive doubly linked list. Provided.
type entry[V any] struct {
	key        string
	value      V
	cost       int64
	expiresAt  time.Time // zero = never
	prev, next *entry[V]
}

// Cache is the LRU cache.
type Cache[V any] struct {
	cfg  Config[V]
	mu   sync.Mutex
	m    map[string]*entry[V]
	head entry[V] // sentinel: head.next is the most recently used
	tail entry[V] // sentinel: tail.prev is the least recently used
	cost int64
	st   Stats
}

// New returns an empty cache. A nil cfg.Now defaults to time.Now. Provided.
func New[V any](cfg Config[V]) *Cache[V] {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	c := &Cache[V]{cfg: cfg, m: make(map[string]*entry[V])}
	c.head.next = &c.tail
	c.tail.prev = &c.head
	return c
}

func (e *entry[V]) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && !now.Before(e.expiresAt)
}

type evicted[V any] struct {
	key    string
	value  V
	reason Reason
}

// unlink removes e from the list in O(1). The caller holds c.mu.
func (c *Cache[V]) unlink(e *entry[V]) {
	// TODO(lab): e.prev.next = e.next; e.next.prev = e.prev; clear e's links.
}

// pushFront links e right after the head sentinel. The caller holds c.mu.
func (c *Cache[V]) pushFront(e *entry[V]) {
	// TODO(lab): four pointer assignments.
}

// remove drops e from list, map and cost accounting. The caller holds c.mu.
func (c *Cache[V]) remove(e *entry[V]) {
	// TODO(lab): unlink, delete from the map, subtract e.cost.
}

// notify calls OnEvict for each removed entry. Call it WITHOUT holding c.mu.
// Provided.
func (c *Cache[V]) notify(ev []evicted[V]) {
	if c.cfg.OnEvict == nil {
		return
	}
	for _, e := range ev {
		c.cfg.OnEvict(e.key, e.value, e.reason)
	}
}

// Get returns the value for key if it is present and unexpired.
//
// Hints: under c.mu, look the key up; an expired entry is removed, counted
// as a miss AND an expiration, and reported to OnEvict (after unlocking); a
// live entry is moved to the front of the list and counted as a hit.
func (c *Cache[V]) Get(key string) (V, bool) {
	// TODO(lab)
	var zero V
	return zero, false
}

// Peek returns the value for key without promoting it, without counting a
// hit or miss and without removing an expired entry.
func (c *Cache[V]) Peek(key string) (V, bool) {
	// TODO(lab)
	var zero V
	return zero, false
}

// Set stores key with cost 1 and the default TTL. Provided.
func (c *Cache[V]) Set(key string, value V) bool {
	return c.SetWithCost(key, value, 1, 0)
}

// SetWithCost stores key with an explicit cost and TTL (see the package doc).
// It reports whether the entry was stored.
//
// Hints: normalise cost; reject oversized entries (and delete the old value
// under that key); compute the expiry; overwrite in place or insert; then
// loop "while over a limit, evict c.tail.prev", collecting victims into a
// slice and calling c.notify(victims) after unlocking.
func (c *Cache[V]) SetWithCost(key string, value V, cost int64, ttl time.Duration) bool {
	// TODO(lab)
	return false
}

// overLimit reports whether either capacity limit is exceeded. The caller
// holds c.mu. Provided.
func (c *Cache[V]) overLimit() bool {
	if c.cfg.MaxEntries > 0 && len(c.m) > c.cfg.MaxEntries {
		return true
	}
	return c.cfg.MaxCost > 0 && c.cost > c.cfg.MaxCost
}

// Delete removes key and reports whether it was present. OnEvict is not
// called and no statistic changes.
func (c *Cache[V]) Delete(key string) bool {
	// TODO(lab)
	return false
}

// PurgeExpired removes every expired entry (an O(n) scan, meant for a
// janitor goroutine, not the hot path) and returns how many it removed.
// Each removal counts as an expiration and is reported to OnEvict.
func (c *Cache[V]) PurgeExpired() int {
	// TODO(lab)
	return 0
}

// Len returns the number of stored entries, including expired entries that
// have not been removed yet.
func (c *Cache[V]) Len() int {
	// TODO(lab)
	return 0
}

// Cost returns the sum of the costs of the stored entries.
func (c *Cache[V]) Cost() int64 {
	// TODO(lab)
	return 0
}

// Keys returns the stored keys from most to least recently used.
func (c *Cache[V]) Keys() []string {
	// TODO(lab): walk from c.head.next to the tail sentinel.
	return nil
}

// Stats returns a snapshot of the counters.
func (c *Cache[V]) Stats() Stats {
	// TODO(lab)
	return Stats{}
}
