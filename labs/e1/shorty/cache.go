package shorty

import (
	"container/list"
	"sync"
	"time"
)

// CacheState is what a cache lookup found. Provided.
type CacheState int

const (
	// Miss: absent, or past its stale window (and now removed).
	Miss CacheState = iota
	// Fresh: inside its freshness lifetime; serve it.
	Fresh
	// Stale: past freshness but inside the stale window; revalidate, and
	// serve it only if revalidation fails (stale-if-error).
	Stale
)

func (s CacheState) String() string {
	return [...]string{"miss", "fresh", "stale"}[s]
}

// Cache is a fixed-capacity LRU map with per-entry freshness and a
// stale-if-error window, on an injected clock. Safe for concurrent use. Get
// promotes an entry, so every method takes the one mutex for writing. You may
// port your labs/a3/lru code.
//
// Invalidation races: a fill that read the store before a Delete must not put
// the old value back after it. Callers take a Token before reading the source
// and store with SetIfToken; any Delete since the token makes the fill a no-op.
// (A cache-wide token is fine: deletes are rare, and a dropped fill only costs
// one more store read.)
//
// The fields are a suggestion.
type Cache[V any] struct {
	mu    sync.Mutex
	cap   int
	now   func() time.Time
	ll    *list.List // front = most recently used; elements hold *cacheItem[V]
	items map[string]*list.Element
	epoch uint64 // bumped by every Delete
}

type cacheItem[V any] struct {
	key        string
	val        V
	freshUntil time.Time
	staleUntil time.Time
}

// NewCache returns an empty cache holding at most capacity entries (minimum 1).
// now is the clock; nil means time.Now.
func NewCache[V any](capacity int, now func() time.Time) *Cache[V] {
	// TODO(lab)
	return &Cache[V]{}
}

// Get returns the entry for key and its state. Fresh means now < freshUntil;
// Stale means freshUntil ≤ now < staleUntil. A Fresh or Stale hit becomes the
// most recently used entry; an entry at or past staleUntil is removed and
// reported as a Miss.
func (c *Cache[V]) Get(key string) (V, CacheState) {
	var zero V
	// TODO(lab)
	return zero, Miss
}

// Set stores v as fresh for fresh, then servable-on-error for a further
// stale. It replaces any existing entry and evicts the least recently used
// entry when the cache is full. fresh ≤ 0 stores nothing.
func (c *Cache[V]) Set(key string, v V, fresh, stale time.Duration) {
	// TODO(lab)
}

// Token returns the current invalidation epoch, to pass to SetIfToken.
func (c *Cache[V]) Token() uint64 {
	// TODO(lab)
	return 0
}

// SetIfToken is Set, unless any Delete happened since tok was issued; it
// reports whether it stored the entry.
func (c *Cache[V]) SetIfToken(key string, tok uint64, v V, fresh, stale time.Duration) bool {
	// TODO(lab)
	return false
}

// Delete removes key and invalidates every outstanding Token.
func (c *Cache[V]) Delete(key string) {
	// TODO(lab)
}

// Len is the number of entries, including stale ones not yet removed.
func (c *Cache[V]) Len() int {
	// TODO(lab)
	return 0
}

// Group coalesces concurrent calls for the same key into one execution
// (singleflight): the first caller runs fn, everyone who arrives while it runs
// waits and gets the same result. Nothing is remembered after fn returns
// (that's the cache's job). The zero value is ready to use. You may port your
// labs/a3/cacheaside Group.
type Group[V any] struct {
	mu    sync.Mutex
	calls map[string]*flight[V]
}

type flight[V any] struct {
	done chan struct{}
	val  V
	err  error
}

// Do runs fn once per key at a time. shared is true for a caller that waited
// on another caller's execution, false for the caller that ran fn.
func (g *Group[V]) Do(key string, fn func() (V, error)) (v V, err error, shared bool) {
	// TODO(lab): register a flight under the mutex, run fn outside it, then
	// remove the flight and close done so the waiters wake up.
	return v, errNotImplemented, false
}
