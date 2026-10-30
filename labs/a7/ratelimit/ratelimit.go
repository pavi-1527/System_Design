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
// SetHeaders and Middleware. Run:
//
//	go test -race ./labs/a7/ratelimit/
//
// Design hint: all three limiters need the same bounded per-key table (a map
// plus a container/list for LRU order, capped at MaxKeys, with a sweep that
// deletes idle entries). Write it once as a small unexported generic type and
// reuse it; the tests only see Len and Sweep.
package ratelimit

import (
	"net/http"
	"sync"
	"time"
)

// Decision is the outcome of one Allow/AllowN call. Provided.
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
// are safe for concurrent use. Provided.
type Limiter interface {
	// Allow is AllowN(key, 1).
	Allow(key string) Decision
	// AllowN asks for n units at once, all or nothing. n ≤ 0 or n > Limit
	// can never be satisfied: the result is denied, with RetryAfter 0, and
	// nothing is consumed or created.
	AllowN(key string, n int) Decision
}

const defaultMaxKeys = 10000

// ---------- token bucket ----------

// TokenBucketConfig configures a TokenBucket. Provided.
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

// TokenBucket allows bursts of up to Burst requests and a sustained rate of
// Rate per second. Tokens refill continuously (lazily, from the clock at each
// call); idle time never banks more than Burst.
//
//   - Allowed iff tokens ≥ n after refill; then n tokens are taken.
//   - RetryAfter when denied = (n − tokens) / Rate, rounded up to a
//     nanosecond. Refill progress made during a denied call is kept.
//   - If the clock moves backwards, no tokens are added or removed.
//   - Remaining = floor(tokens) after the call (beware float error: ten
//     refills of 0.1 must not read as 0.999…).
//   - Config defaults: Rate 1, Burst 1, MaxKeys 10,000, Now time.Now.
//
// A key that has been idle long enough to be full is indistinguishable from
// a new key, so Sweep may drop it. Evicting a key that is *not* full forgives
// its debt: that is the cost of a hard memory cap.
type TokenBucket struct {
	// TODO(lab): config, a mutex, and a bounded LRU table of
	// {tokens float64, last time.Time} per key.
}

// NewTokenBucket returns a TokenBucket with defaults applied.
func NewTokenBucket(cfg TokenBucketConfig) *TokenBucket {
	// TODO(lab)
	return &TokenBucket{}
}

// Allow implements Limiter.
func (tb *TokenBucket) Allow(key string) Decision { return tb.AllowN(key, 1) }

// AllowN implements Limiter.
func (tb *TokenBucket) AllowN(key string, n int) Decision {
	// TODO(lab): validate n (before touching the table), find or create the
	// key (full bucket), refill from the clock, then take n tokens or
	// compute RetryAfter.
	return Decision{}
}

// Len reports the number of tracked keys.
func (tb *TokenBucket) Len() int {
	// TODO(lab)
	return 0
}

// Sweep removes keys whose bucket would be full right now and returns how
// many were removed.
func (tb *TokenBucket) Sweep() int {
	// TODO(lab)
	return 0
}

// ---------- window limiters ----------

// WindowConfig configures the window limiters. Provided.
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

// SlidingWindowLog is exact: it remembers the timestamp of every accepted
// request in the last Window and allows a request iff fewer than Limit
// (counting n) are inside the window ending now. A timestamp t is inside
// while t > now − Window (so it expires exactly Window after it was made).
// Memory is O(Limit) per key, which is the price of exactness.
//
//   - Denied requests are never recorded (record them and a client that
//     keeps retrying is locked out forever).
//   - RetryAfter when denied = the time until enough of the oldest timestamps
//     expire: with c entries in the window, the (c + n − Limit)-th oldest
//     entry's time + Window − now.
//   - Remaining = Limit − entries in the window, after the call.
//   - Sweep drops keys with no timestamp inside the window.
type SlidingWindowLog struct {
	// TODO(lab): config, a mutex, and a bounded LRU table of []time.Time.
}

// NewSlidingWindowLog returns a SlidingWindowLog (cfg.withDefaults() gives
// the defaults).
func NewSlidingWindowLog(cfg WindowConfig) *SlidingWindowLog {
	// TODO(lab)
	return &SlidingWindowLog{}
}

// Allow implements Limiter.
func (l *SlidingWindowLog) Allow(key string) Decision { return l.AllowN(key, 1) }

// AllowN implements Limiter.
func (l *SlidingWindowLog) AllowN(key string, n int) Decision {
	// TODO(lab): prune expired timestamps, then admit n or compute RetryAfter.
	return Decision{}
}

// Len reports the number of tracked keys.
func (l *SlidingWindowLog) Len() int {
	// TODO(lab)
	return 0
}

// Sweep removes keys with no timestamp inside the window.
func (l *SlidingWindowLog) Sweep() int {
	// TODO(lab)
	return 0
}

// ---------- sliding window counter ----------

// SlidingWindowCounter approximates the sliding window with two counters per
// key: the count in the current fixed window (aligned to multiples of Window
// since the epoch, i.e. index = now.UnixNano() / Window) and in the one
// before it. The estimated number of requests in the last Window is
//
//	estimate = prev × (1 − f) + curr,   f = fraction of the current window elapsed
//
// i.e. it assumes the previous window's requests were spread evenly. A
// request for n units is allowed iff estimate + n ≤ Limit (allow 1e-9 of
// float slack). Memory is O(1) per key. Bursty traffic can be over-admitted
// (the estimate assumes uniformity); steady traffic tracks the exact log.
//
//   - Denied requests are not counted.
//   - Moving to window w+1 makes curr the new prev; skipping two or more
//     windows zeroes both.
//   - RetryAfter when denied = the earliest time at which estimate + n ≤
//     Limit, assuming nothing else is accepted. Within the current window the
//     estimate falls linearly as prev's weight decays:
//     prev×(1−x) + curr ≤ Limit−n, solve for x = elapsed fraction. If that
//     point is past the window's end (curr alone is too big), carry on into
//     the next window, where prev' = curr and curr' = 0.
//   - Remaining = floor(Limit − estimate) after the call, never negative.
//   - Sweep drops keys whose estimate is zero now and forever.
type SlidingWindowCounter struct {
	// TODO(lab): config, a mutex, and a bounded LRU table of
	// {windowIndex, curr, prev} per key.
}

// NewSlidingWindowCounter returns a SlidingWindowCounter (cfg.withDefaults()
// gives the defaults).
func NewSlidingWindowCounter(cfg WindowConfig) *SlidingWindowCounter {
	// TODO(lab)
	return &SlidingWindowCounter{}
}

// Allow implements Limiter.
func (c *SlidingWindowCounter) Allow(key string) Decision { return c.AllowN(key, 1) }

// AllowN implements Limiter.
func (c *SlidingWindowCounter) AllowN(key string, n int) Decision {
	// TODO(lab): roll the window forward, compute the estimate, admit n or
	// compute RetryAfter.
	return Decision{}
}

// Len reports the number of tracked keys.
func (c *SlidingWindowCounter) Len() int {
	// TODO(lab)
	return 0
}

// Sweep removes keys whose estimate is already zero and will stay zero.
func (c *SlidingWindowCounter) Sweep() int {
	// TODO(lab)
	return 0
}

// ---------- HTTP glue ----------

// RetryAfterSeconds converts a wait into the whole seconds for a Retry-After
// header, rounding UP (telling a client to come back early only earns a
// second 429). Non-positive durations give 0.
func RetryAfterSeconds(d time.Duration) int {
	// TODO(lab)
	return 0
}

// SetHeaders writes the decision into h:
//
//	RateLimit-Limit      d.Limit
//	RateLimit-Remaining  d.Remaining (never negative)
//	Retry-After          only when denied: RetryAfterSeconds(d.RetryAfter),
//	                     at least 1 so a client never retries in a hot loop
func (d Decision) SetHeaders(h http.Header) {
	// TODO(lab)
}

// Middleware limits requests by key(r). The headers from SetHeaders are set
// on every response. A denied request gets 429 Too Many Requests with a small
// JSON error body (Content-Type: application/json) and next is not called.
// An empty key is a real key: all unidentified callers share one budget.
func Middleware(l Limiter, key func(*http.Request) string) func(http.Handler) http.Handler {
	// TODO(lab)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "ratelimit: not implemented", http.StatusNotImplemented)
		})
	}
}
