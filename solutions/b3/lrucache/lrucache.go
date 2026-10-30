// Package lrucache is a fixed-capacity, concurrency-safe LRU cache.
//
// Lab B3 Problem 1 (60-minute box). Stage 1 is this file; stage 2 (TTL) lives
// in stage2.go and stage 3 (eviction callbacks, stats, expired-first
// eviction) in stage3.go.
//
// Reference design, in one paragraph: a map for O(1) lookup, an intrusive
// circular doubly linked list for O(1) recency updates, a min-heap on expiry
// time for "find the entry that expired first" in O(log n), and ONE mutex
// that guards all three plus the counters. Every critical section is O(1) or
// O(log n) and never calls user code: eviction callbacks are collected under
// the lock and run after it is released.
package lrucache

import (
	"errors"
	"sync"
	"time"
)

// ErrInvalidCapacity is returned by New when capacity < 1.
var ErrInvalidCapacity = errors.New("lrucache: capacity must be >= 1")

// Option customises a Cache at construction time.
type Option[K comparable, V any] func(*Cache[K, V])

// entry is both a map value and a list node. heapIdx is the entry's position
// in the expiry heap, or -1 when it has no expiry.
type entry[K comparable, V any] struct {
	key        K
	value      V
	prev, next *entry[K, V]
	expiresAt  time.Time
	heapIdx    int
}

// Cache maps keys to values and evicts the least recently used entry when it
// is full. The zero value is not usable; call New.
type Cache[K comparable, V any] struct {
	mu       sync.Mutex // guards everything below
	capacity int
	items    map[K]*entry[K, V]
	root     entry[K, V] // sentinel: root.next is the MRU entry, root.prev the LRU
	expiry   expiryHeap[K, V]

	clock      Clock
	defaultTTL time.Duration
	onEvict    func(K, V, Reason)
	stats      Stats
}

// New returns a cache holding at most capacity entries. It returns
// ErrInvalidCapacity when capacity < 1. Options are applied in order.
func New[K comparable, V any](capacity int, opts ...Option[K, V]) (*Cache[K, V], error) {
	if capacity < 1 {
		return nil, ErrInvalidCapacity
	}
	c := &Cache[K, V]{
		capacity: capacity,
		items:    make(map[K]*entry[K, V], min(capacity, 1024)),
		clock:    systemClock{},
	}
	c.root.next, c.root.prev = &c.root, &c.root
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Get returns the value for key and marks it most recently used.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	var zero V
	var pending []removal[K, V]
	c.mu.Lock()
	e, ok := c.items[key]
	if ok && c.expired(e, c.clock.Now()) {
		pending = append(pending, c.remove(e, Expired))
		ok = false
	}
	if !ok {
		c.stats.Misses++
		fn := c.onEvict
		c.mu.Unlock()
		notify(fn, pending)
		return zero, false
	}
	c.stats.Hits++
	c.moveToFront(e)
	v := e.value
	c.mu.Unlock()
	return v, true
}

// Put stores value under key and marks it most recently used, evicting the
// least recently used entry if the cache is full and key is new.
func (c *Cache[K, V]) Put(key K, value V) { c.PutWithTTL(key, value, 0) }

// Delete removes key and reports whether it was present.
func (c *Cache[K, V]) Delete(key K) bool {
	var pending []removal[K, V]
	c.mu.Lock()
	e, ok := c.items[key]
	live := false
	if ok {
		if c.expired(e, c.clock.Now()) {
			pending = append(pending, c.remove(e, Expired))
		} else {
			pending = append(pending, c.remove(e, Deleted))
			live = true
		}
	}
	fn := c.onEvict
	c.mu.Unlock()
	notify(fn, pending)
	return live
}

// Len returns the number of stored entries.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Keys returns the stored keys from most to least recently used. It does not
// change recency. The caller owns the returned slice.
func (c *Cache[K, V]) Keys() []K {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]K, 0, len(c.items))
	for e := c.root.next; e != &c.root; e = e.next {
		keys = append(keys, e.key)
	}
	return keys
}

// ---- internals (all require c.mu) -----------------------------------------

func (c *Cache[K, V]) pushFront(e *entry[K, V]) {
	e.prev, e.next = &c.root, c.root.next
	c.root.next.prev = e
	c.root.next = e
}

func (c *Cache[K, V]) unlink(e *entry[K, V]) {
	e.prev.next, e.next.prev = e.next, e.prev
	e.prev, e.next = nil, nil
}

func (c *Cache[K, V]) moveToFront(e *entry[K, V]) {
	if c.root.next == e {
		return
	}
	c.unlink(e)
	c.pushFront(e)
}

// remove unlinks e from every index and returns the notification to send once
// the lock is released. Counters are updated here so they can never disagree
// with what the callback is told.
func (c *Cache[K, V]) remove(e *entry[K, V], why Reason) removal[K, V] {
	c.unlink(e)
	delete(c.items, e.key)
	if e.heapIdx >= 0 {
		c.expiry.remove(e)
	}
	switch why {
	case Capacity:
		c.stats.Evictions++
	case Expired:
		c.stats.Expirations++
	}
	return removal[K, V]{key: e.key, value: e.value, reason: why}
}
