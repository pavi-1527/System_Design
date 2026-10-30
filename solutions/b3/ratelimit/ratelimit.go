// Package ratelimit is a keyed, concurrency-safe rate limiter library.
//
// Lab B3 Problem 2 (60-minute box). Stage 1 is this file: a per-key token
// bucket. Stage 2 (stage2.go) puts the algorithm behind a small Strategy
// interface, adds a sliding-window-log algorithm, retry-after information and
// lock sharding. Stage 3 (stage3.go) bounds memory (idle-key eviction), adds a
// blocking Wait and Stats.
//
// Reference design, in one paragraph: keys hash (randomly seeded) onto shards;
// each shard has ONE mutex guarding its key map, its LRU list and a monotonic
// "high-water" clock; each key owns a small single-threaded Strategy value, so
// per-key atomicity is just "hold the shard lock for the duration of Take".
// Time is lazy: nothing refills in the background, every Take computes the
// elapsed time since the last Take. Token accounting is integer arithmetic in
// units of 1/Rate.Per tokens, so there is no float drift and no overflow.
package ratelimit

import (
	"errors"
	"hash/maphash"
	"math"
	"sync"
	"time"
)

// ErrInvalidConfig is returned by constructors for impossible parameters.
var ErrInvalidConfig = errors.New("ratelimit: invalid configuration")

// Clock is the only thing the limiter needs from time.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Rate is "Events per Per", for example Rate{Events: 100, Per: time.Minute}.
type Rate struct {
	Events int
	Per    time.Duration
}

// Option customises a Limiter at construction time. Options are applied in
// order, before any key exists.
type Option func(*Limiter)

// WithClock sets the clock. The default is the system clock, whose readings
// carry Go's monotonic component, so wall-clock steps (NTP, VM resume) do not
// affect the limiter.
func WithClock(c Clock) Option {
	return func(l *Limiter) { l.clock = c }
}

// Limiter rate-limits independent keys. The zero value is not usable; call
// New (or NewWithStrategy).
//
// Concurrency contract: every method is safe for concurrent use. Operations
// on one key are atomic and linearizable; operations on keys in different
// shards never contend. The Clock is called without any lock held; Strategy
// and Factory code runs under a shard lock and must not call back into the
// Limiter.
type Limiter struct {
	clock    Clock
	epoch    time.Time // the clock reading at construction; all internal times are offsets from it
	factory  Factory
	burst    int // largest n any request can ever be granted
	seed     maphash.Seed
	shards   []*shard
	shardN   int
	maxKeys  int
	newTimer NewTimerFunc
	cfgErr   error // first error recorded by an Option
}

// entry is a map value and an LRU list node.
type entry struct {
	key        string
	s          Strategy
	prev, next *entry
}

// shard owns a slice of the key space. Everything below mu is guarded by it.
type shard struct {
	mu   sync.Mutex
	m    map[string]*entry
	root entry         // LRU sentinel: root.next is the MRU entry, root.prev the LRU
	hwm  time.Duration // high-water mark of time seen by this shard; never decreases
	cap  int           // max keys in this shard, 0 = unbounded

	allowed, denied, evicted, forced uint64
}

// New returns a limiter that gives every key a token bucket: it refills at
// rate and holds at most burst tokens. A key's bucket starts full. It
// returns ErrInvalidConfig for Events < 1, Per < 1ns, burst < 1, or values
// whose product overflows the internal integer arithmetic.
func New(rate Rate, burst int, opts ...Option) (*Limiter, error) {
	p, err := newBucketParams(rate, burst)
	if err != nil {
		return nil, err
	}
	return NewWithStrategy(p.factory(), opts...)
}

// Allow reports whether one event for key may happen now, consuming one
// permit if so.
func (l *Limiter) Allow(key string) bool { return l.AllowN(key, 1) }

// AllowN reports whether n events for key may happen now. It is all or
// nothing: when it returns false nothing was consumed. n < 1 and n greater
// than the burst both return false (the latter can never succeed), and neither
// creates any per-key state.
func (l *Limiter) AllowN(key string, n int) bool {
	d, err := l.take(key, n)
	return err == nil && d.Allowed
}

// Len returns the number of keys currently tracked.
func (l *Limiter) Len() int {
	total := 0
	for _, sh := range l.shards {
		sh.mu.Lock()
		total += len(sh.m)
		sh.mu.Unlock()
	}
	return total
}

// ---- internals --------------------------------------------------------------

// since converts the clock reading to an offset from the limiter's epoch.
// Time.Sub uses the monotonic clock when both readings have one.
func (l *Limiter) since() time.Duration { return l.clock.Now().Sub(l.epoch) }

func (l *Limiter) shardFor(key string) *shard {
	return l.shards[maphash.String(l.seed, key)%uint64(len(l.shards))]
}

// take is the single path every public method goes through.
func (l *Limiter) take(key string, n int) (Decision, error) {
	if n < 1 {
		return Decision{}, ErrInvalidN
	}
	if n > l.burst {
		return Decision{}, ErrExceedsLimit // hopeless: do not allocate state for it
	}
	now := l.since() // user code (Clock) runs before the lock is taken
	sh := l.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	// The clock may step backwards (a fake clock, a non-monotonic source, a
	// goroutine that read the clock before another one took the lock). Time
	// as the strategies see it never goes backwards: it just stands still
	// until the clock catches up, so no tokens are minted by a rewind.
	if now < sh.hwm {
		now = sh.hwm
	} else {
		sh.hwm = now
	}

	e := sh.m[key]
	if e == nil {
		sh.sweep(now, 2) // amortised idle-key reclamation, see stage3.go
		e = &entry{key: key, s: l.factory(now)}
		sh.m[key] = e
		sh.pushFront(e)
		sh.trim(now)
	} else {
		sh.moveToFront(e)
	}
	d := e.s.Take(now, n)
	if d.Allowed {
		sh.allowed++
	} else {
		sh.denied++
	}
	return d, nil
}

// ---- token bucket -----------------------------------------------------------

// bucketParams are shared by every bucket a factory creates.
//
// Exact arithmetic: one token is `per` units, the bucket holds burst*per
// units, and it gains `events` units per nanosecond (events tokens per `per`
// ns is events*per units per per ns). Everything is int64; a bucket's content
// at any instant is exactly min(capacity, start + elapsed*events).
type bucketParams struct {
	events   int64 // units gained per nanosecond
	per      int64 // units per token
	capacity int64 // burst * per
	fullIn   int64 // nanoseconds to refill an empty bucket, rounded up
}

type tokenBucket struct {
	p      *bucketParams
	tokens int64         // units, 0..capacity
	last   time.Duration // time of the last refill
}

func newBucketParams(rate Rate, burst int) (*bucketParams, error) {
	if rate.Events < 1 || rate.Per < 1 || burst < 1 {
		return nil, ErrInvalidConfig
	}
	events, per, b := int64(rate.Events), int64(rate.Per), int64(burst)
	// capacity + events must fit in int64: refill adds < capacity+events at once.
	if b > (math.MaxInt64-events)/per {
		return nil, ErrInvalidConfig
	}
	p := &bucketParams{events: events, per: per, capacity: b * per}
	p.fullIn = (p.capacity + events - 1) / events
	return p, nil
}

// factory returns a Factory whose buckets start full.
func (p *bucketParams) factory() Factory {
	return func(now time.Duration) Strategy {
		return &tokenBucket{p: p, tokens: p.capacity, last: now}
	}
}

// refill brings the bucket up to date. elapsed*events cannot overflow because
// elapsed < fullIn whenever the multiplication is reached.
func (b *tokenBucket) refill(now time.Duration) {
	if now <= b.last {
		return // a rewind (or no time passed) mints nothing and keeps last
	}
	el := int64(now - b.last)
	b.last = now
	if el >= b.p.fullIn {
		b.tokens = b.p.capacity
		return
	}
	add := el * b.p.events
	if add >= b.p.capacity-b.tokens {
		b.tokens = b.p.capacity
	} else {
		b.tokens += add
	}
}

func (b *tokenBucket) Burst() int { return int(b.p.capacity / b.p.per) }

func (b *tokenBucket) Take(now time.Duration, n int) Decision {
	b.refill(now)
	cost := int64(n) * b.p.per // n <= burst, so cost <= capacity
	if b.tokens >= cost {
		b.tokens -= cost
		return Decision{Allowed: true, Remaining: int(b.tokens / b.p.per)}
	}
	deficit := cost - b.tokens
	wait := (deficit + b.p.events - 1) / b.p.events // ceil: never tell a caller "too early"
	return Decision{Remaining: int(b.tokens / b.p.per), RetryAfter: time.Duration(wait)}
}

// Idle reports whether the bucket would be full at now, i.e. whether it is
// indistinguishable from a brand-new one.
func (b *tokenBucket) Idle(now time.Duration) bool {
	if now <= b.last {
		return b.tokens == b.p.capacity
	}
	el := int64(now - b.last)
	return el >= b.p.fullIn || el*b.p.events >= b.p.capacity-b.tokens
}
