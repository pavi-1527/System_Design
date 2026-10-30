// Package ratelimit implements per-key rate limiters behind one interface:
// a token bucket, an exact sliding-window log and an approximate sliding-window
// counter, plus a provided fixed-window baseline and the HTTP glue (429,
// Retry-After, RateLimit headers).
//
// It is the lab for A7 Unit 2 (§2–§3). Every limiter takes an injected clock,
// is safe for concurrent use, and keeps memory bounded: at most MaxKeys keys
// are tracked, the least recently used key is evicted first, and Sweep drops
// keys whose state is indistinguishable from a brand-new key.
//
// Suggested order: TokenBucket (math first, then the key table), the
// sliding-window log, the sliding-window counter, then RetryAfterSeconds,
// SetHeaders and Middleware. Run: go test -race ./labs/a7/ratelimit/
package ratelimit

import (
	"container/list"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Decision is the outcome of one Allow/AllowN call.
type Decision struct {
	// Allowed reports whether the request may proceed. A denied request
	// consumes nothing.
	Allowed bool
	// Limit is the configured capacity: the burst for a token bucket, the
	// per-window limit for the window limiters.
	Limit int
	// Remaining is how many more single requests would be allowed right
	// now (after this one, if it was allowed). Never negative.
	Remaining int
	// RetryAfter is zero when Allowed. Otherwise it is the shortest time
	// after which the same request would be allowed, assuming no other
	// request from this key is accepted in between. For an invalid n (see
	// AllowN) it is zero and Allowed is false.
	RetryAfter time.Duration
}

// Limiter decides whether a request from key may proceed. Implementations
// are safe for concurrent use.
type Limiter interface {
	// Allow is AllowN(key, 1).
	Allow(key string) Decision
	// AllowN asks for n units at once, all or nothing. n ≤ 0 or n > Limit
	// can never be satisfied: the result is denied, with RetryAfter 0, and
	// nothing is consumed or created.
	AllowN(key string, n int) Decision
}

// ---------- shared: bounded LRU key table ----------

type item[S any] struct {
	key string
	val *S
}

// table is a map with LRU order and a hard cap on the number of keys.
// It is not safe for concurrent use; limiters hold their own mutex.
type table[S any] struct {
	max int
	m   map[string]*list.Element
	l   *list.List
}

func newTable[S any](max int) *table[S] {
	return &table[S]{max: max, m: make(map[string]*list.Element), l: list.New()}
}

// get returns the state for key, creating it with create if needed. The key
// becomes the most recently used. If creating it would exceed the cap, the
// least recently used key is evicted first.
func (t *table[S]) get(key string, create func() *S) *S {
	if el, ok := t.m[key]; ok {
		t.l.MoveToFront(el)
		return el.Value.(*item[S]).val
	}
	if t.max > 0 && t.l.Len() >= t.max {
		if back := t.l.Back(); back != nil {
			delete(t.m, back.Value.(*item[S]).key)
			t.l.Remove(back)
		}
	}
	v := create()
	t.m[key] = t.l.PushFront(&item[S]{key: key, val: v})
	return v
}

// sweep deletes every key for which idle reports true and returns how many.
func (t *table[S]) sweep(idle func(*S) bool) int {
	n := 0
	for el := t.l.Back(); el != nil; {
		prev := el.Prev()
		it := el.Value.(*item[S])
		if idle(it.val) {
			delete(t.m, it.key)
			t.l.Remove(el)
			n++
		}
		el = prev
	}
	return n
}

func (t *table[S]) len() int { return t.l.Len() }

const defaultMaxKeys = 10000

// ---------- token bucket ----------

// TokenBucketConfig configures a TokenBucket.
type TokenBucketConfig struct {
	// Rate is the refill rate in tokens per second (default 1 if ≤ 0).
	Rate float64
	// Burst is the bucket capacity (default 1 if ≤ 0). A new key starts
	// with a full bucket.
	Burst int
	// MaxKeys caps the number of tracked keys (default 10,000).
	MaxKeys int
	// Now is the clock (default time.Now).
	Now func() time.Time
}

type bucketState struct {
	tokens float64
	last   time.Time
}

// TokenBucket allows bursts of up to Burst requests and a sustained rate of
// Rate per second. Tokens refill continuously (lazily, from the clock at each
// call); idle time never banks more than Burst.
//
//   - Allowed iff tokens ≥ n after refill; then n tokens are taken.
//   - RetryAfter when denied = (n − tokens) / Rate, rounded up to a
//     nanosecond. Refill progress made during a denied call is kept.
//   - If the clock moves backwards, no tokens are added or removed.
//   - Remaining = floor(tokens) after the call.
//
// A key that has been idle long enough to be full is indistinguishable from
// a new key, so Sweep may drop it. Evicting a key that is *not* full forgives
// its debt: that is the cost of a hard memory cap.
type TokenBucket struct {
	cfg TokenBucketConfig
	mu  sync.Mutex
	t   *table[bucketState]
}

// NewTokenBucket returns a TokenBucket with defaults applied.
func NewTokenBucket(cfg TokenBucketConfig) *TokenBucket {
	if cfg.Rate <= 0 {
		cfg.Rate = 1
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 1
	}
	if cfg.MaxKeys <= 0 {
		cfg.MaxKeys = defaultMaxKeys
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &TokenBucket{cfg: cfg, t: newTable[bucketState](cfg.MaxKeys)}
}

func (tb *TokenBucket) refill(s *bucketState, now time.Time) {
	if el := now.Sub(s.last); el > 0 {
		s.tokens = math.Min(float64(tb.cfg.Burst), s.tokens+tb.cfg.Rate*el.Seconds())
		s.last = now
	}
}

// Allow implements Limiter.
func (tb *TokenBucket) Allow(key string) Decision { return tb.AllowN(key, 1) }

// AllowN implements Limiter.
func (tb *TokenBucket) AllowN(key string, n int) Decision {
	d := Decision{Limit: tb.cfg.Burst}
	if n <= 0 || n > tb.cfg.Burst {
		return d
	}
	now := tb.cfg.Now()
	tb.mu.Lock()
	defer tb.mu.Unlock()
	s := tb.t.get(key, func() *bucketState { return &bucketState{tokens: float64(tb.cfg.Burst), last: now} })
	tb.refill(s, now)
	if s.tokens+1e-9 >= float64(n) {
		s.tokens = math.Max(0, s.tokens-float64(n))
		d.Allowed = true
		d.Remaining = int(s.tokens + 1e-9)
		return d
	}
	d.Remaining = int(s.tokens + 1e-9)
	d.RetryAfter = time.Duration(math.Ceil((float64(n) - s.tokens) / tb.cfg.Rate * float64(time.Second)))
	return d
}

// Len reports the number of tracked keys.
func (tb *TokenBucket) Len() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.t.len()
}

// Sweep removes keys whose bucket would be full right now and returns how
// many were removed.
func (tb *TokenBucket) Sweep() int {
	now := tb.cfg.Now()
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.t.sweep(func(s *bucketState) bool {
		tb.refill(s, now)
		return s.tokens+1e-9 >= float64(tb.cfg.Burst)
	})
}

// ---------- window limiters ----------

// WindowConfig configures the window limiters.
type WindowConfig struct {
	// Limit is the number of requests allowed per sliding Window.
	Limit int
	// Window is the window length (default 1s if ≤ 0).
	Window time.Duration
	// MaxKeys caps the number of tracked keys (default 10,000).
	MaxKeys int
	// Now is the clock (default time.Now).
	Now func() time.Time
}

func (c WindowConfig) withDefaults() WindowConfig {
	if c.Limit <= 0 {
		c.Limit = 1
	}
	if c.Window <= 0 {
		c.Window = time.Second
	}
	if c.MaxKeys <= 0 {
		c.MaxKeys = defaultMaxKeys
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// ---------- fixed window (provided baseline) ----------

type fixedState struct {
	idx   int64
	count int
}

// FixedWindow counts requests in windows aligned to multiples of Window since
// the Unix epoch and resets the count at each boundary. It is the simplest
// limiter, and the baseline for the boundary-burst test: a client can spend
// Limit just before a boundary and Limit again just after it, i.e. 2×Limit in
// a few milliseconds. Provided (not part of the exercise). It deliberately
// has no key cap and no cleanup: one entry per key ever seen, forever.
type FixedWindow struct {
	cfg  WindowConfig
	mu   sync.Mutex
	keys map[string]*fixedState
}

// NewFixedWindow returns a FixedWindow with defaults applied.
func NewFixedWindow(cfg WindowConfig) *FixedWindow {
	cfg = cfg.withDefaults()
	return &FixedWindow{cfg: cfg, keys: make(map[string]*fixedState)}
}

// Allow implements Limiter.
func (f *FixedWindow) Allow(key string) Decision { return f.AllowN(key, 1) }

// AllowN implements Limiter.
func (f *FixedWindow) AllowN(key string, n int) Decision {
	d := Decision{Limit: f.cfg.Limit}
	if n <= 0 || n > f.cfg.Limit {
		return d
	}
	now := f.cfg.Now()
	w := int64(f.cfg.Window)
	idx := now.UnixNano() / w
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.keys[key]
	if s == nil {
		s = &fixedState{idx: idx}
		f.keys[key] = s
	}
	if s.idx != idx {
		s.idx, s.count = idx, 0
	}
	if s.count+n <= f.cfg.Limit {
		s.count += n
		d.Allowed = true
		d.Remaining = f.cfg.Limit - s.count
		return d
	}
	d.Remaining = f.cfg.Limit - s.count
	d.RetryAfter = time.Duration((idx+1)*w - now.UnixNano())
	return d
}

// Len reports the number of tracked keys.
func (f *FixedWindow) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.keys)
}

// ---------- sliding window log ----------

type logState struct{ ts []time.Time } // ascending

// SlidingWindowLog is exact: it remembers the timestamp of every accepted
// request in the last Window and allows a request iff fewer than Limit
// (counting n) are inside the window ending now. A timestamp t is inside
// while t > now − Window (so it expires exactly Window after it was made).
// Memory is O(Limit) per key, which is the price of exactness.
//
//   - Denied requests are never recorded.
//   - RetryAfter when denied = the time until enough of the oldest timestamps
//     expire: with c entries in the window, the (c + n − Limit)-th oldest
//     entry's time + Window − now.
//   - Remaining = Limit − entries in the window, after the call.
//   - Sweep drops keys with no timestamp inside the window.
type SlidingWindowLog struct {
	cfg WindowConfig
	mu  sync.Mutex
	t   *table[logState]
}

// NewSlidingWindowLog returns a SlidingWindowLog with defaults applied.
func NewSlidingWindowLog(cfg WindowConfig) *SlidingWindowLog {
	cfg = cfg.withDefaults()
	return &SlidingWindowLog{cfg: cfg, t: newTable[logState](cfg.MaxKeys)}
}

func (l *SlidingWindowLog) prune(s *logState, now time.Time) {
	cutoff := now.Add(-l.cfg.Window)
	i := 0
	for i < len(s.ts) && !s.ts[i].After(cutoff) {
		i++
	}
	if i > 0 {
		s.ts = append(s.ts[:0], s.ts[i:]...)
	}
}

// Allow implements Limiter.
func (l *SlidingWindowLog) Allow(key string) Decision { return l.AllowN(key, 1) }

// AllowN implements Limiter.
func (l *SlidingWindowLog) AllowN(key string, n int) Decision {
	d := Decision{Limit: l.cfg.Limit}
	if n <= 0 || n > l.cfg.Limit {
		return d
	}
	now := l.cfg.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.t.get(key, func() *logState { return &logState{} })
	l.prune(s, now)
	if len(s.ts)+n <= l.cfg.Limit {
		for i := 0; i < n; i++ {
			s.ts = append(s.ts, now)
		}
		d.Allowed = true
		d.Remaining = l.cfg.Limit - len(s.ts)
		return d
	}
	d.Remaining = l.cfg.Limit - len(s.ts)
	need := len(s.ts) + n - l.cfg.Limit
	d.RetryAfter = s.ts[need-1].Add(l.cfg.Window).Sub(now)
	return d
}

// Len reports the number of tracked keys.
func (l *SlidingWindowLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.t.len()
}

// Sweep removes keys with no timestamp inside the window.
func (l *SlidingWindowLog) Sweep() int {
	now := l.cfg.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.t.sweep(func(s *logState) bool {
		l.prune(s, now)
		return len(s.ts) == 0
	})
}

// ---------- sliding window counter ----------

type counterState struct {
	idx  int64 // index of the window `curr` belongs to
	curr int
	prev int
}

// SlidingWindowCounter approximates the sliding window with two counters per
// key: the count in the current fixed window (aligned to multiples of Window
// since the epoch) and in the one before it. The estimated number of
// requests in the last Window is
//
//	estimate = prev × (1 − f) + curr,   f = fraction of the current window elapsed
//
// i.e. it assumes the previous window's requests were spread evenly. A
// request for n units is allowed iff estimate + n ≤ Limit. Memory is O(1)
// per key. Bursty traffic can be over-admitted (the estimate assumes
// uniformity); steady traffic tracks the exact log closely.
//
//   - Denied requests are not counted.
//   - Moving to window w+1 makes curr the new prev; skipping two or more
//     windows zeroes both.
//   - RetryAfter when denied = the earliest time at which estimate + n ≤
//     Limit, assuming nothing else is accepted: either later in the current
//     window (as prev's weight decays) or, if curr alone is too large, in a
//     later window.
//   - Remaining = floor(Limit − estimate) after the call, never negative.
//   - Sweep drops keys whose estimate is zero now and forever.
type SlidingWindowCounter struct {
	cfg WindowConfig
	mu  sync.Mutex
	t   *table[counterState]
}

// NewSlidingWindowCounter returns a SlidingWindowCounter with defaults applied.
func NewSlidingWindowCounter(cfg WindowConfig) *SlidingWindowCounter {
	cfg = cfg.withDefaults()
	return &SlidingWindowCounter{cfg: cfg, t: newTable[counterState](cfg.MaxKeys)}
}

func (c *SlidingWindowCounter) roll(s *counterState, w int64) {
	switch {
	case s.idx == w:
	case s.idx == w-1:
		s.prev, s.curr, s.idx = s.curr, 0, w
	default:
		s.prev, s.curr, s.idx = 0, 0, w
	}
}

// Allow implements Limiter.
func (c *SlidingWindowCounter) Allow(key string) Decision { return c.AllowN(key, 1) }

// AllowN implements Limiter.
func (c *SlidingWindowCounter) AllowN(key string, n int) Decision {
	d := Decision{Limit: c.cfg.Limit}
	if n <= 0 || n > c.cfg.Limit {
		return d
	}
	now := c.cfg.Now()
	W := int64(c.cfg.Window)
	nowNs := now.UnixNano()
	w := nowNs / W
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.t.get(key, func() *counterState { return &counterState{idx: w} })
	c.roll(s, w)
	f := float64(nowNs-w*W) / float64(W)
	est := float64(s.prev)*(1-f) + float64(s.curr)
	limit := float64(c.cfg.Limit)
	if est+float64(n) <= limit+1e-9 {
		s.curr += n
		d.Allowed = true
		d.Remaining = max(0, int(math.Floor(limit-est-float64(n)+1e-9)))
		return d
	}
	d.Remaining = max(0, int(math.Floor(limit-est+1e-9)))
	d.RetryAfter = c.retryAfter(s, w, nowNs, n)
	return d
}

func (c *SlidingWindowCounter) retryAfter(s *counterState, w, nowNs int64, n int) time.Duration {
	W := float64(c.cfg.Window)
	start := float64(w * int64(c.cfg.Window))
	need := float64(c.cfg.Limit - n) // largest estimate that still admits n
	curr, prev := float64(s.curr), float64(s.prev)
	var t float64
	if prev > 0 && curr <= need && need-curr > 0 {
		// Within this window: prev × (1 − x) + curr ≤ need  ⇔  x ≥ 1 − (need − curr)/prev
		x := 1 - (need-curr)/prev
		t = start + math.Ceil(x*W)
		if x < 1 && t > float64(nowNs) {
			return time.Duration(t - float64(nowNs))
		}
	}
	// Next window: prev' = curr, curr' = 0, estimate(x) = curr × (1 − x).
	x := 0.0
	if curr > 0 {
		x = math.Max(0, 1-need/curr)
	}
	t = start + W + math.Ceil(x*W)
	return time.Duration(t - float64(nowNs))
}

// Len reports the number of tracked keys.
func (c *SlidingWindowCounter) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t.len()
}

// Sweep removes keys whose estimate is already zero and will stay zero.
func (c *SlidingWindowCounter) Sweep() int {
	w := c.cfg.Now().UnixNano() / int64(c.cfg.Window)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t.sweep(func(s *counterState) bool {
		switch {
		case s.idx <= w-2:
			return true
		case s.idx == w-1:
			return s.curr == 0
		default:
			return s.curr == 0 && s.prev == 0
		}
	})
}

// ---------- HTTP glue ----------

// RetryAfterSeconds converts a wait into the whole seconds for a Retry-After
// header, rounding UP (telling a client to come back early only earns a
// second 429). Non-positive durations give 0.
func RetryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int(math.Ceil(d.Seconds()))
}

// SetHeaders writes the decision into h:
//
//	RateLimit-Limit      d.Limit
//	RateLimit-Remaining  d.Remaining (never negative)
//	Retry-After          only when denied: RetryAfterSeconds(d.RetryAfter),
//	                     at least 1 so a client never retries in a hot loop
func (d Decision) SetHeaders(h http.Header) {
	h.Set("RateLimit-Limit", strconv.Itoa(d.Limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(max(0, d.Remaining)))
	if !d.Allowed {
		h.Set("Retry-After", strconv.Itoa(max(1, RetryAfterSeconds(d.RetryAfter))))
	}
}

// Middleware limits requests by key(r). The headers from SetHeaders are set
// on every response. A denied request gets 429 Too Many Requests with a small
// JSON error body and next is not called. An empty key is a real key: all
// unidentified callers share one budget.
func Middleware(l Limiter, key func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d := l.Allow(key(r))
			d.SetHeaders(w.Header())
			if !d.Allowed {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"code":"rate_limited","message":"too many requests"}}`)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
