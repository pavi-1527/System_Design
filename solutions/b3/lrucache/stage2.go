package lrucache

import (
	"container/heap"
	"time"
)

// Clock is the only thing the cache needs from time.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// WithClock sets the clock. The default is the system clock.
func WithClock[K comparable, V any](clock Clock) Option[K, V] {
	return func(c *Cache[K, V]) { c.clock = clock }
}

// WithDefaultTTL sets the TTL applied by Put (and by PutWithTTL when its ttl
// is <= 0). Zero means entries do not expire.
func WithDefaultTTL[K comparable, V any](ttl time.Duration) Option[K, V] {
	return func(c *Cache[K, V]) { c.defaultTTL = ttl }
}

// PutWithTTL is Put with an explicit TTL for this entry.
func (c *Cache[K, V]) PutWithTTL(key K, value V, ttl time.Duration) {
	if ttl <= 0 {
		ttl = c.defaultTTL // immutable after New: safe to read without the lock
	}
	var pending []removal[K, V]
	c.mu.Lock()
	now := c.clock.Now()
	e, ok := c.items[key]
	if ok {
		// Replacement: new value, new TTL, most recently used. Not an eviction.
		e.value = value
		c.setExpiry(e, now, ttl)
		c.moveToFront(e)
	} else {
		if len(c.items) >= c.capacity {
			pending = append(pending, c.evictOne(now))
		}
		e = &entry[K, V]{key: key, value: value, heapIdx: -1}
		c.items[key] = e
		c.setExpiry(e, now, ttl)
		c.pushFront(e)
	}
	fn := c.onEvict
	c.mu.Unlock()
	notify(fn, pending)
}

// RemoveExpired removes every expired entry and returns how many it removed.
func (c *Cache[K, V]) RemoveExpired() int {
	var pending []removal[K, V]
	c.mu.Lock()
	now := c.clock.Now()
	for len(c.expiry) > 0 && c.expired(c.expiry[0], now) {
		pending = append(pending, c.remove(c.expiry[0], Expired))
	}
	fn := c.onEvict
	c.mu.Unlock()
	notify(fn, pending)
	return len(pending)
}

// expired: now >= expiresAt. Entries without a TTL have heapIdx -1 and never expire.
func (c *Cache[K, V]) expired(e *entry[K, V], now time.Time) bool {
	return e.heapIdx >= 0 && !now.Before(e.expiresAt)
}

// setExpiry (re)schedules e. ttl == 0 means "never expires".
func (c *Cache[K, V]) setExpiry(e *entry[K, V], now time.Time, ttl time.Duration) {
	switch {
	case ttl <= 0 && e.heapIdx >= 0:
		c.expiry.remove(e)
	case ttl <= 0:
	case e.heapIdx >= 0:
		e.expiresAt = now.Add(ttl)
		heap.Fix(&c.expiry, e.heapIdx)
	default:
		e.expiresAt = now.Add(ttl)
		heap.Push(&c.expiry, e)
	}
}

// expiryHeap is a min-heap of entries ordered by expiresAt. Each entry knows
// its own index so it can be removed or re-keyed in O(log n).
type expiryHeap[K comparable, V any] []*entry[K, V]

func (h expiryHeap[K, V]) Len() int           { return len(h) }
func (h expiryHeap[K, V]) Less(i, j int) bool { return h[i].expiresAt.Before(h[j].expiresAt) }
func (h expiryHeap[K, V]) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIdx, h[j].heapIdx = i, j
}
func (h *expiryHeap[K, V]) Push(x any) {
	e := x.(*entry[K, V])
	e.heapIdx = len(*h)
	*h = append(*h, e)
}
func (h *expiryHeap[K, V]) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil // let the GC have it
	e.heapIdx = -1
	*h = old[:n-1]
	return e
}

func (h *expiryHeap[K, V]) remove(e *entry[K, V]) { heap.Remove(h, e.heapIdx) }
