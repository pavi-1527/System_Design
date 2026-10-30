package ratelimit

import (
	"errors"
	"hash/maphash"
	"time"
)

// Errors returned by Check (and by Wait in stage 3).
var (
	// ErrInvalidN is returned for n < 1.
	ErrInvalidN = errors.New("ratelimit: n must be >= 1")
	// ErrExceedsLimit is returned when n is larger than the burst (or the
	// window limit): the request can never succeed, however long you wait.
	ErrExceedsLimit = errors.New("ratelimit: n exceeds the limit and can never be allowed")
)

// Decision is the outcome of one Check.
type Decision struct {
	Allowed bool
	// Remaining is how many more single permits would be allowed right now,
	// after this decision (the number behind an X-RateLimit-Remaining header).
	Remaining int
	// RetryAfter is 0 when Allowed. Otherwise it is the shortest delay after
	// which the SAME request would be allowed, assuming no other traffic for
	// the key. It is exact: waiting RetryAfter-1ns is still too early.
	RetryAfter time.Duration
}

// Strategy is one key's algorithm. A Limiter creates one per key, lazily, and
// only ever calls it with the key's shard lock held, so implementations are
// single-threaded state machines: no locking, no blocking, no callbacks into
// the Limiter, no I/O.
type Strategy interface {
	// Take tries to consume n permits at time now (an offset from an
	// arbitrary epoch, never decreasing between calls). 1 <= n <= Burst().
	// On success the permits are consumed; on failure nothing is.
	Take(now time.Duration, n int) Decision
	// Burst is the largest n that can ever succeed. It must not change.
	Burst() int
	// Idle reports whether the strategy's state at now is indistinguishable
	// from a freshly created one, so dropping it changes no future decision.
	// It must not modify the strategy.
	Idle(now time.Duration) bool
}

// Factory creates the Strategy for a new key. now is the creation time.
// Every Strategy a Factory returns must have the same Burst.
type Factory func(now time.Duration) Strategy

const defaultShards = 32

// NewWithStrategy returns a limiter whose per-key algorithm comes from f.
// New(rate, burst) is NewWithStrategy(TokenBucket(rate, burst)).
func NewWithStrategy(f Factory, opts ...Option) (*Limiter, error) {
	if f == nil {
		return nil, ErrInvalidConfig
	}
	l := &Limiter{
		clock:    systemClock{},
		factory:  f,
		shardN:   defaultShards,
		newTimer: realTimer,
		seed:     maphash.MakeSeed(),
	}
	for _, opt := range opts {
		opt(l)
	}
	if l.cfgErr != nil {
		return nil, l.cfgErr
	}
	if l.clock == nil {
		return nil, ErrInvalidConfig
	}
	l.epoch = l.clock.Now()
	l.burst = f(0).Burst() // probe: Burst is constant per factory
	if l.burst < 1 {
		return nil, ErrInvalidConfig
	}
	perShard := 0
	if l.maxKeys > 0 {
		perShard = (l.maxKeys + l.shardN - 1) / l.shardN
	}
	l.shards = make([]*shard, l.shardN)
	for i := range l.shards {
		sh := &shard{m: make(map[string]*entry), cap: perShard}
		sh.root.next, sh.root.prev = &sh.root, &sh.root
		l.shards[i] = sh
	}
	return l, nil
}

// WithShards sets the number of lock shards (default 32). More shards mean
// less contention between unrelated keys and a little more fixed memory.
func WithShards(n int) Option {
	return func(l *Limiter) {
		if n < 1 {
			l.cfgErr = ErrInvalidConfig
			return
		}
		l.shardN = n
	}
}

// Check is AllowN with the details: the Decision carries Remaining and
// RetryAfter. It returns ErrInvalidN for n < 1 and ErrExceedsLimit for n above
// the burst; neither creates per-key state.
func (l *Limiter) Check(key string, n int) (Decision, error) { return l.take(key, n) }

// TokenBucket returns a Factory for per-key token buckets that refill at rate
// and hold at most burst tokens. New buckets start full. It returns
// ErrInvalidConfig under the same conditions as New.
func TokenBucket(rate Rate, burst int) (Factory, error) {
	p, err := newBucketParams(rate, burst)
	if err != nil {
		return nil, err
	}
	return p.factory(), nil
}

// ---- sliding window log -----------------------------------------------------

// SlidingWindow returns a Factory for an exact sliding-window-log algorithm:
// at most limit permits in any interval of length window. A permit taken at
// time t counts until t+window; at exactly t+window it no longer counts.
func SlidingWindow(limit int, window time.Duration) (Factory, error) {
	if limit < 1 || window < 1 {
		return nil, ErrInvalidConfig
	}
	return func(time.Duration) Strategy {
		return &windowLog{limit: limit, window: window}
	}, nil
}

type windowEvent struct {
	at time.Duration
	n  int
}

// windowLog stores one record per distinct instant that granted permits, so
// memory is O(min(limit, distinct instants in the window)), not O(requests).
type windowLog struct {
	limit  int
	window time.Duration
	events []windowEvent // events[head:] are the live ones, oldest first
	head   int
	count  int // sum of n over events[head:]
}

func (w *windowLog) Burst() int { return w.limit }

func (w *windowLog) expire(now time.Duration) {
	for w.head < len(w.events) && now-w.events[w.head].at >= w.window {
		w.count -= w.events[w.head].n
		w.head++
	}
	switch {
	case w.head == len(w.events):
		w.events, w.head = w.events[:0], 0
	case w.head >= 32 && w.head*2 >= len(w.events):
		w.events = append(w.events[:0], w.events[w.head:]...) // compact: amortised O(1)
		w.head = 0
	}
}

func (w *windowLog) Take(now time.Duration, n int) Decision {
	w.expire(now)
	if w.count+n <= w.limit {
		if last := len(w.events) - 1; last >= w.head && w.events[last].at == now {
			w.events[last].n += n
		} else {
			w.events = append(w.events, windowEvent{at: now, n: n})
		}
		w.count += n
		return Decision{Allowed: true, Remaining: w.limit - w.count}
	}
	// We need `need` of the oldest permits to age out; the last of those to
	// leave is the one that frees enough room.
	need := w.count + n - w.limit
	freed := 0
	for _, ev := range w.events[w.head:] {
		freed += ev.n
		if freed >= need {
			return Decision{Remaining: w.limit - w.count, RetryAfter: ev.at + w.window - now}
		}
	}
	// Unreachable: n <= limit means the live permits always cover `need`.
	// Fail safe (deny, retry later) rather than panic on a request path.
	return Decision{Remaining: w.limit - w.count, RetryAfter: w.window}
}

func (w *windowLog) Idle(now time.Duration) bool {
	if w.head == len(w.events) {
		return true
	}
	return now-w.events[len(w.events)-1].at >= w.window // newest event gone => all gone
}
