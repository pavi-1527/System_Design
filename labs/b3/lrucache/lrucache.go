// Package lrucache is a fixed-capacity, concurrency-safe LRU cache.
//
// Lab B3 Problem 1 (60-minute box). Stage 1 is this file; stage 2 (TTL) lives
// in stage2.go and stage 3 (eviction callbacks, stats, expired-first
// eviction) in stage3.go. Treat the later files as sealed until the
// interviewer "announces" them (see unit-1-caches-and-limiters.md §1).
//
// Stage 1 contract:
//   - Get, Put, Delete, Len and Keys are safe for concurrent use.
//   - Get and Put are O(1) on average. A test caps total time, so an O(n)
//     scan to find the LRU entry will fail it.
//   - Get promotes the entry to most-recently-used; Put of an existing key
//     replaces the value and promotes it. When a Put of a NEW key would
//     exceed the capacity, the least-recently-used entry is evicted first.
//   - Keys returns keys from most- to least-recently-used and does not
//     change recency.
package lrucache

import "errors"

// ErrInvalidCapacity is returned by New when capacity < 1.
var ErrInvalidCapacity = errors.New("lrucache: capacity must be >= 1")

var errNotImplemented = errors.New("lrucache: not implemented")

// Option customises a Cache at construction time. Stage 2 and 3 add options
// (WithClock, WithDefaultTTL, WithOnEvict); your New must apply them.
type Option[K comparable, V any] func(*Cache[K, V])

// Cache maps keys to values and evicts the least recently used entry when it
// is full. The zero value is not usable; call New.
type Cache[K comparable, V any] struct {
	// TODO(lab): fields. Think about: the lock, the lookup structure, the
	// recency list, and (later stages) the clock, default TTL, expiry index,
	// stats and callback.
}

// New returns a cache holding at most capacity entries. It returns
// ErrInvalidCapacity when capacity < 1. Options are applied in order.
func New[K comparable, V any](capacity int, opts ...Option[K, V]) (*Cache[K, V], error) {
	// TODO(lab)
	_, _ = capacity, opts
	return nil, errNotImplemented
}

// Get returns the value for key and marks it most recently used.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	// TODO(lab)
	var zero V
	_ = key
	return zero, false
}

// Put stores value under key and marks it most recently used, evicting the
// least recently used entry if the cache is full and key is new.
func (c *Cache[K, V]) Put(key K, value V) {
	// TODO(lab)
	_, _ = key, value
}

// Delete removes key and reports whether it was present.
func (c *Cache[K, V]) Delete(key K) bool {
	// TODO(lab)
	_ = key
	return false
}

// Len returns the number of stored entries.
func (c *Cache[K, V]) Len() int {
	// TODO(lab)
	return 0
}

// Keys returns the stored keys from most to least recently used. It does not
// change recency. The caller owns the returned slice.
func (c *Cache[K, V]) Keys() []K {
	// TODO(lab)
	return nil
}
