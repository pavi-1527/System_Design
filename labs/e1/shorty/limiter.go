package shorty

import (
	"container/list"
	"sync"
	"time"
)

// TokenBucket is a per-key token-bucket rate limiter with bounded memory, on an
// injected clock. Each key's bucket starts full (burst tokens), refills at rate
// tokens per second up to burst, and every allowed request takes one token.
// At most maxKeys buckets are kept; the least recently used is evicted (and
// starts full again if that key returns: memory is bounded at the price of
// forgetting idle clients). Safe for concurrent use. You may port your
// labs/a7/ratelimit TokenBucket.
//
// The fields are a suggestion.
type TokenBucket struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	maxKeys int
	now     func() time.Time
	ll      *list.List // front = most recently used; elements hold *bucket
	keys    map[string]*list.Element
}

type bucket struct {
	key    string
	tokens float64
	last   time.Time
}

// NewTokenBucket returns a limiter. rate > 0, burst ≥ 1 and maxKeys ≥ 1 are
// clamped to those minimums. now nil means time.Now.
func NewTokenBucket(rate float64, burst, maxKeys int, now func() time.Time) *TokenBucket {
	// TODO(lab)
	return &TokenBucket{}
}

// Allow takes one token from key's bucket. When it cannot, ok is false and
// retryAfter is exactly how long until one token will be there (round the
// duration up to the next nanosecond, never down).
func (b *TokenBucket) Allow(key string) (ok bool, retryAfter time.Duration) {
	// TODO(lab): refill lazily from the time since the bucket was last
	// touched, capped at burst; never let idle time bank more than burst.
	return false, 0
}

// Len is the number of buckets held.
func (b *TokenBucket) Len() int {
	// TODO(lab)
	return 0
}

// RetryAfterSeconds converts a wait into the integer seconds of a Retry-After
// header: rounded up (a client that comes back early is refused again) and at
// least 1.
func RetryAfterSeconds(d time.Duration) int {
	// TODO(lab)
	return 0
}
