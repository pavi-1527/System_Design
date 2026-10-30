package shorty

import (
	"container/list"
	"sync"
	"time"
)

// CacheState is what a cache lookup found.
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
// promotes an entry, so every method takes the one mutex for writing.
//
// Invalidation races: a fill that read the store before a Delete must not put
// the old value back after it. Callers take a Token before reading the source
// and store with SetIfToken; any Delete since the token makes the fill a no-op.
// (The token is cache-wide, which is conservative: a delete of one key also
// drops concurrent fills of other keys. Deletes are rare; a missed fill only
// costs one more store read.)
type Cache[V any] struct {
	mu    sync.Mutex
	cap   int
	now   func() time.Time
	ll    *list.List // front = most recently used
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
	if capacity < 1 {
		capacity = 1
	}
	if now == nil {
		now = time.Now
	}
	return &Cache[V]{cap: capacity, now: now, ll: list.New(), items: map[string]*list.Element{}}
}

// Get returns the entry for key and its state. A Fresh or Stale hit becomes
// the most recently used entry; an entry past its stale window is removed and
// reported as a Miss.
func (c *Cache[V]) Get(key string) (V, CacheState) {
	var zero V
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return zero, Miss
	}
	it := el.Value.(*cacheItem[V])
	now := c.now()
	if !now.Before(it.staleUntil) {
		c.ll.Remove(el)
		delete(c.items, key)
		return zero, Miss
	}
	c.ll.MoveToFront(el)
	if now.Before(it.freshUntil) {
		return it.val, Fresh
	}
	return it.val, Stale
}

// Set stores v as fresh for fresh, then servable-on-error for a further
// stale. It replaces any existing entry and evicts the least recently used
// entry when the cache is full. fresh ≤ 0 stores nothing.
func (c *Cache[V]) Set(key string, v V, fresh, stale time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(key, v, fresh, stale)
}

func (c *Cache[V]) setLocked(key string, v V, fresh, stale time.Duration) {
	if fresh <= 0 {
		return
	}
	if stale < 0 {
		stale = 0
	}
	now := c.now()
	it := &cacheItem[V]{key: key, val: v, freshUntil: now.Add(fresh), staleUntil: now.Add(fresh + stale)}
	if el, ok := c.items[key]; ok {
		el.Value = it
		c.ll.MoveToFront(el)
		return
	}
	c.items[key] = c.ll.PushFront(it)
	for c.ll.Len() > c.cap {
		old := c.ll.Back()
		c.ll.Remove(old)
		delete(c.items, old.Value.(*cacheItem[V]).key)
	}
}

// Token returns the current invalidation epoch, to pass to SetIfToken.
func (c *Cache[V]) Token() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
}

// SetIfToken is Set, unless any Delete happened since tok was issued; it
// reports whether it stored the entry.
func (c *Cache[V]) SetIfToken(key string, tok uint64, v V, fresh, stale time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch != tok {
		return false
	}
	c.setLocked(key, v, fresh, stale)
	return true
}

// Delete removes key and invalidates every outstanding Token.
func (c *Cache[V]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	if el, ok := c.items[key]; ok {
		c.ll.Remove(el)
		delete(c.items, key)
	}
}

// Len is the number of entries, including stale ones not yet removed.
func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Group coalesces concurrent calls for the same key into one execution
// (singleflight): the first caller runs fn, everyone who arrives while it runs
// waits and gets the same result. Nothing is remembered after fn returns. The
// zero value is ready to use.
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
// on another caller's execution, false for the caller that ran fn. If fn
// panics, waiters are released with the zero value and a nil error; don't
// panic in fn.
func (g *Group[V]) Do(key string, fn func() (V, error)) (v V, err error, shared bool) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*flight[V]{}
	}
	if f, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-f.done
		return f.val, f.err, true
	}
	f := &flight[V]{done: make(chan struct{})}
	g.calls[key] = f
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		close(f.done)
	}()
	f.val, f.err = fn()
	return f.val, f.err, false
}
