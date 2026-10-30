package shorty

import (
	"container/list"
	"math"
	"sync"
	"time"
)

// TokenBucket is a per-key token-bucket rate limiter with bounded memory, on an
// injected clock. Each key's bucket starts full (burst tokens), refills at rate
// tokens per second up to burst, and every allowed request takes one token.
// At most maxKeys buckets are kept; the least recently used is evicted (and
// starts full again if that key returns: memory is bounded at the price of
// forgetting idle clients). Safe for concurrent use.
type TokenBucket struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	maxKeys int
	now     func() time.Time
	ll      *list.List
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
	if rate <= 0 {
		rate = math.SmallestNonzeroFloat64
	}
	if burst < 1 {
		burst = 1
	}
	if maxKeys < 1 {
		maxKeys = 1
	}
	if now == nil {
		now = time.Now
	}
	return &TokenBucket{rate: rate, burst: float64(burst), maxKeys: maxKeys, now: now,
		ll: list.New(), keys: map[string]*list.Element{}}
}

// Allow takes one token from key's bucket. When it cannot, ok is false and
// retryAfter is exactly how long until one token will be there.
func (b *TokenBucket) Allow(key string) (ok bool, retryAfter time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	var bk *bucket
	if el, found := b.keys[key]; found {
		bk = el.Value.(*bucket)
		b.ll.MoveToFront(el)
		if el := now.Sub(bk.last).Seconds(); el > 0 {
			bk.tokens = math.Min(b.burst, bk.tokens+el*b.rate)
		}
		bk.last = now
	} else {
		bk = &bucket{key: key, tokens: b.burst, last: now}
		b.keys[key] = b.ll.PushFront(bk)
		for b.ll.Len() > b.maxKeys {
			old := b.ll.Back()
			b.ll.Remove(old)
			delete(b.keys, old.Value.(*bucket).key)
		}
	}
	if bk.tokens >= 1 {
		bk.tokens--
		return true, 0
	}
	wait := (1 - bk.tokens) / b.rate
	return false, time.Duration(math.Ceil(wait * float64(time.Second)))
}

// Len is the number of buckets held.
func (b *TokenBucket) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ll.Len()
}

// RetryAfterSeconds converts a wait into the integer seconds of a Retry-After
// header: rounded up (a client that comes back early is refused again) and at
// least 1.
func RetryAfterSeconds(d time.Duration) int {
	s := int(math.Ceil(d.Seconds()))
	if s < 1 {
		return 1
	}
	return s
}
