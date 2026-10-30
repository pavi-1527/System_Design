package lrucache

import "time"

// ---- Stage 2: TTL (announced at about minute 20) --------------------------
//
// Contract:
//   - An entry stored with a TTL expires at putTime+ttl. At exactly that
//     instant it is already expired (expired means now >= expiresAt).
//   - Get on an expired entry is a miss and REMOVES the entry. It does not
//     promote anything.
//   - Reading does not extend a TTL (no sliding expiry). Putting an existing
//     key replaces the value AND restarts its TTL.
//   - ttl <= 0 passed to PutWithTTL means "use the default TTL". A default
//     TTL of 0 means "never expires".
//   - Len and Keys describe STORED entries, including expired ones that have
//     not been reaped yet. RemoveExpired reaps them and reports how many.
//   - Time comes from the injected Clock so tests never sleep.

// Clock is the only thing the cache needs from time.
type Clock interface {
	Now() time.Time
}

// WithClock sets the clock. The default is the system clock.
func WithClock[K comparable, V any](clock Clock) Option[K, V] {
	return func(c *Cache[K, V]) {
		// TODO(lab)
		_ = clock
	}
}

// WithDefaultTTL sets the TTL applied by Put (and by PutWithTTL when its ttl
// is <= 0). Zero means entries do not expire.
func WithDefaultTTL[K comparable, V any](ttl time.Duration) Option[K, V] {
	return func(c *Cache[K, V]) {
		// TODO(lab)
		_ = ttl
	}
}

// PutWithTTL is Put with an explicit TTL for this entry.
func (c *Cache[K, V]) PutWithTTL(key K, value V, ttl time.Duration) {
	// TODO(lab)
	_, _, _ = key, value, ttl
}

// RemoveExpired removes every expired entry and returns how many it removed.
func (c *Cache[K, V]) RemoveExpired() int {
	// TODO(lab)
	return 0
}
