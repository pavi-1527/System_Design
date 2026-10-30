package ratelimit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tClock is a fake clock. It starts on a whole minute so that windows of 1 s
// (or 10 s) are aligned with the clock's origin.
type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func tNewClock() *tClock { return &tClock{t: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)} }
func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func tNear(a, b, tol time.Duration) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// tMaker builds a limiter that allows 10 requests per second per key, with
// the given key cap. maxKeys 0 means the default.
type tMaker struct {
	name string
	mk   func(c *tClock, maxKeys int) Limiter
}

func tMakers() []tMaker {
	return []tMaker{
		{"TokenBucket", func(c *tClock, k int) Limiter {
			return NewTokenBucket(TokenBucketConfig{Rate: 10, Burst: 10, MaxKeys: k, Now: c.Now})
		}},
		{"SlidingWindowLog", func(c *tClock, k int) Limiter {
			return NewSlidingWindowLog(WindowConfig{Limit: 10, Window: time.Second, MaxKeys: k, Now: c.Now})
		}},
		{"SlidingWindowCounter", func(c *tClock, k int) Limiter {
			return NewSlidingWindowCounter(WindowConfig{Limit: 10, Window: time.Second, MaxKeys: k, Now: c.Now})
		}},
	}
}

func forEachLimiter(t *testing.T, f func(t *testing.T, c *tClock, mk func(maxKeys int) Limiter)) {
	t.Helper()
	for _, m := range tMakers() {
		m := m
		t.Run(m.name, func(t *testing.T) {
			c := tNewClock()
			f(t, c, func(k int) Limiter { return m.mk(c, k) })
		})
	}
}

type tSizer interface {
	Len() int
	Sweep() int
}

func tWait(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish within 10 s (deadlock?)", what)
	}
}

// ---------- properties every limiter must have ----------

func TestLimiters_AllowUpToTheLimitThenDeny(t *testing.T) {
	forEachLimiter(t, func(t *testing.T, c *tClock, mk func(int) Limiter) {
		l := mk(0)
		for i := 1; i <= 10; i++ {
			d := l.Allow("alice")
			if !d.Allowed {
				t.Fatalf("request %d denied, want allowed (limit 10)", i)
			}
			if d.Limit != 10 || d.Remaining != 10-i || d.RetryAfter != 0 {
				t.Fatalf("request %d: %+v; want Limit 10, Remaining %d, RetryAfter 0", i, d, 10-i)
			}
		}
		d := l.Allow("alice")
		if d.Allowed || d.RetryAfter <= 0 || d.Remaining != 0 || d.Limit != 10 {
			t.Errorf("11th request: %+v; want denied, Remaining 0, RetryAfter > 0", d)
		}
	})
}

func TestLimiters_KeysAreIndependent(t *testing.T) {
	forEachLimiter(t, func(t *testing.T, c *tClock, mk func(int) Limiter) {
		l := mk(0)
		for i := 0; i < 10; i++ {
			l.Allow("noisy")
		}
		if l.Allow("noisy").Allowed {
			t.Fatalf("noisy should be limited")
		}
		if !l.Allow("quiet").Allowed {
			t.Errorf("quiet was denied because noisy used its budget: one tenant must not be able to starve another")
		}
	})
}

func TestLimiters_AllowNIsAllOrNothing(t *testing.T) {
	forEachLimiter(t, func(t *testing.T, c *tClock, mk func(int) Limiter) {
		l := mk(0)
		if d := l.AllowN("k", 7); !d.Allowed || d.Remaining != 3 {
			t.Fatalf("AllowN(7): %+v; want allowed, Remaining 3", d)
		}
		if d := l.AllowN("k", 5); d.Allowed || d.RetryAfter <= 0 {
			t.Fatalf("AllowN(5) with 3 left: %+v; want denied with a RetryAfter", d)
		}
		if d := l.AllowN("k", 3); !d.Allowed || d.Remaining != 0 {
			t.Errorf("AllowN(3): %+v; want allowed: the denied AllowN(5) must not have consumed anything", d)
		}
	})
}

func TestLimiters_InvalidNIsDeniedWithoutSideEffects(t *testing.T) {
	forEachLimiter(t, func(t *testing.T, c *tClock, mk func(int) Limiter) {
		l := mk(0)
		for _, n := range []int{0, -3, 11} {
			d := l.AllowN("k", n)
			if d.Allowed || d.RetryAfter != 0 || d.Limit != 10 {
				t.Errorf("AllowN(%d): %+v; want denied, RetryAfter 0 (waiting can never help), Limit 10", n, d)
			}
		}
		if s := l.(tSizer); s.Len() != 0 {
			t.Errorf("Len() = %d after only invalid requests; they must not create keys", s.Len())
		}
		if d := l.AllowN("k", 10); !d.Allowed {
			t.Errorf("a full allowance of 10 was denied after invalid requests: %+v", d)
		}
	})
}

func TestLimiters_AllowanceReturnsAfterAWindow(t *testing.T) {
	forEachLimiter(t, func(t *testing.T, c *tClock, mk func(int) Limiter) {
		l := mk(0)
		for i := 0; i < 10; i++ {
			l.Allow("k")
		}
		if l.Allow("k").Allowed {
			t.Fatalf("should be exhausted")
		}
		c.Advance(2 * time.Second)
		for i := 0; i < 10; i++ {
			if !l.Allow("k").Allowed {
				t.Fatalf("request %d after 2 s denied; the full allowance should be back", i+1)
			}
		}
	})
}

func TestLimiters_RetryAfterIsTheTrueWait(t *testing.T) {
	forEachLimiter(t, func(t *testing.T, c *tClock, mk func(int) Limiter) {
		l := mk(0)
		for i := 0; i < 10; i++ {
			l.Allow("k")
		}
		d := l.Allow("k")
		if d.Allowed || d.RetryAfter <= ms(1) {
			t.Fatalf("want a denial with RetryAfter > 1ms, got %+v", d)
		}
		c.Advance(d.RetryAfter - ms(1))
		if l.Allow("k").Allowed {
			t.Errorf("allowed 1ms before RetryAfter (%v): the hint was too long, clients wait for nothing", d.RetryAfter)
		}
		c.Advance(ms(1))
		if !l.Allow("k").Allowed {
			t.Errorf("denied after waiting RetryAfter (%v): the hint was too short, clients will retry into a second 429", d.RetryAfter)
		}
	})
}

func TestLimiters_DeniedRequestsAreNotCounted(t *testing.T) {
	forEachLimiter(t, func(t *testing.T, c *tClock, mk func(int) Limiter) {
		l := mk(0)
		for i := 0; i < 10; i++ {
			l.Allow("k")
		}
		// A client that keeps retrying through its 429s, every 50 ms. If
		// denied requests were recorded, each one would refresh the window
		// and the client would be locked out for as long as it keeps trying.
		var allowedAt time.Duration
		for step := 1; step <= 60; step++ {
			c.Advance(ms(50))
			if l.Allow("k").Allowed {
				allowedAt = time.Duration(step) * ms(50)
				break
			}
		}
		if allowedAt == 0 {
			t.Fatalf("still denied after 3 s of retrying every 50 ms: denied requests must not be recorded (a client that keeps retrying is locked out forever)")
		}
		if allowedAt > ms(1200) {
			t.Errorf("first allowed %v after exhaustion, want ≤ 1.2 s for a 10/s limit", allowedAt)
		}
	})
}

func TestLimiters_ConcurrentCallersNeverExceedTheLimit(t *testing.T) {
	forEachLimiter(t, func(t *testing.T, c *tClock, mk func(int) Limiter) {
		l := mk(0)
		const keys, perKey = 8, 40
		var allowed [keys]atomic.Int64
		var wg sync.WaitGroup
		for k := 0; k < keys; k++ {
			for g := 0; g < perKey; g++ {
				wg.Add(1)
				go func(k int) {
					defer wg.Done()
					if l.Allow(fmt.Sprintf("key-%d", k)).Allowed {
						allowed[k].Add(1)
					}
				}(k)
			}
		}
		tWait(t, &wg, "concurrent Allow calls")
		for k := range allowed {
			if got := allowed[k].Load(); got != 10 {
				t.Errorf("key-%d: %d of %d concurrent requests allowed, want exactly 10 (check-and-update must be atomic)", k, got, perKey)
			}
		}
	})
}

func TestLimiters_MemoryIsBoundedByMaxKeys(t *testing.T) {
	forEachLimiter(t, func(t *testing.T, c *tClock, mk func(int) Limiter) {
		l := mk(100)
		for i := 0; i < 10000; i++ {
			l.Allow(fmt.Sprintf("tenant-%d", i))
		}
		if n := l.(tSizer).Len(); n > 100 {
			t.Errorf("Len() = %d after 10,000 distinct keys with MaxKeys = 100: per-key state must not grow without bound", n)
		}
	})
}

func TestLimiters_LeastRecentlyUsedKeyIsEvictedFirst(t *testing.T) {
	forEachLimiter(t, func(t *testing.T, c *tClock, mk func(int) Limiter) {
		l := mk(2)
		l.AllowN("a", 10) // a exhausted
		l.AllowN("b", 10) // b exhausted
		l.Allow("a")      // touching a (denied) makes b the least recently used
		l.Allow("c")      // a third key: evicts b
		if l.Allow("a").Allowed {
			t.Errorf("a was evicted although it was the most recently used key")
		}
		if !l.Allow("b").Allowed {
			t.Errorf("b still limited: it should have been evicted (and so forgiven) when c arrived")
		}
		if n := l.(tSizer).Len(); n > 2 {
			t.Errorf("Len() = %d, want ≤ 2", n)
		}
	})
}

// ---------- the boundary burst ----------

// tSpendAtBoundary sends 10 requests just before a 1 s boundary and 10 just
// after it, 20 ms apart, and returns how many were allowed.
func tSpendAtBoundary(c *tClock, l Limiter) int {
	allowed := 0
	c.Advance(990 * time.Millisecond) // t = 0.99 s
	for i := 0; i < 10; i++ {
		if l.Allow("k").Allowed {
			allowed++
		}
	}
	c.Advance(20 * time.Millisecond) // t = 1.01 s: next fixed window
	for i := 0; i < 10; i++ {
		if l.Allow("k").Allowed {
			allowed++
		}
	}
	return allowed
}

func TestFixedWindow_AllowsTwiceTheLimitAcrossABoundary(t *testing.T) {
	c := tNewClock()
	l := NewFixedWindow(WindowConfig{Limit: 10, Window: time.Second, Now: c.Now})
	if got := tSpendAtBoundary(c, l); got != 20 {
		t.Errorf("fixed window allowed %d requests in 20 ms across a boundary, want 20 (2× the limit: the known flaw)", got)
	}
}

func TestSlidingWindowsAndTokenBucketFixTheBoundaryBurst(t *testing.T) {
	forEachLimiter(t, func(t *testing.T, c *tClock, mk func(int) Limiter) {
		if got := tSpendAtBoundary(c, mk(0)); got != 10 {
			t.Errorf("allowed %d requests in 20 ms across a window boundary, want 10: a limit of 10/s must hold for ANY 1 s span, not just aligned ones", got)
		}
	})
}

func TestFixedWindow_ResetsAtEachBoundary(t *testing.T) {
	c := tNewClock()
	l := NewFixedWindow(WindowConfig{Limit: 3, Window: time.Second, Now: c.Now})
	for i := 0; i < 3; i++ {
		l.Allow("k")
	}
	d := l.Allow("k")
	if d.Allowed || d.RetryAfter != time.Second {
		t.Fatalf("got %+v, want denied with RetryAfter = 1s (to the next boundary)", d)
	}
	c.Advance(ms(400))
	if d := l.Allow("k"); d.RetryAfter != ms(600) {
		t.Errorf("RetryAfter = %v, want 600ms", d.RetryAfter)
	}
	c.Advance(ms(600))
	if !l.Allow("k").Allowed {
		t.Errorf("denied at the boundary")
	}
}

// ---------- token bucket specifics ----------

func TestTokenBucket_BurstThenSteadyRate(t *testing.T) {
	c := tNewClock()
	tb := NewTokenBucket(TokenBucketConfig{Rate: 10, Burst: 5, Now: c.Now})
	for i := 0; i < 5; i++ {
		if !tb.Allow("k").Allowed {
			t.Fatalf("burst request %d denied; a full bucket of 5 allows 5 back-to-back", i+1)
		}
	}
	d := tb.Allow("k")
	if d.Allowed || !tNear(d.RetryAfter, ms(100), time.Microsecond) {
		t.Fatalf("6th: %+v; want denied with RetryAfter ≈ 100ms (one token at 10/s)", d)
	}
	c.Advance(ms(250)) // 2.5 tokens
	if !tb.Allow("k").Allowed || !tb.Allow("k").Allowed {
		t.Fatalf("two requests should be allowed after 250ms at 10/s")
	}
	d = tb.Allow("k")
	if d.Allowed || !tNear(d.RetryAfter, ms(50), time.Microsecond) {
		t.Errorf("after spending 2 of 2.5 tokens: %+v; want denied with RetryAfter ≈ 50ms (half a token left)", d)
	}
}

func TestTokenBucket_IdleTimeNeverBanksMoreThanBurst(t *testing.T) {
	c := tNewClock()
	tb := NewTokenBucket(TokenBucketConfig{Rate: 10, Burst: 5, Now: c.Now})
	tb.AllowN("k", 5)
	c.Advance(time.Hour)
	n := 0
	for tb.Allow("k").Allowed {
		n++
		if n > 100 {
			break
		}
	}
	if n != 5 {
		t.Errorf("%d requests allowed after an hour idle, want exactly Burst = 5", n)
	}
}

func TestTokenBucket_FractionalProgressSurvivesDeniedCalls(t *testing.T) {
	c := tNewClock()
	tb := NewTokenBucket(TokenBucketConfig{Rate: 1, Burst: 1, Now: c.Now})
	tb.Allow("k")
	c.Advance(ms(400))
	if d := tb.Allow("k"); d.Allowed || !tNear(d.RetryAfter, ms(600), time.Microsecond) {
		t.Fatalf("t=400ms: %+v; want denied, RetryAfter ≈ 600ms", d)
	}
	c.Advance(ms(400))
	if d := tb.Allow("k"); d.Allowed || !tNear(d.RetryAfter, ms(200), time.Microsecond) {
		t.Fatalf("t=800ms: %+v; want denied, RetryAfter ≈ 200ms: refill progress made before a denied call must be kept, not reset", d)
	}
	c.Advance(ms(200))
	if !tb.Allow("k").Allowed {
		t.Errorf("t=1s: denied, want allowed")
	}
}

func TestTokenBucket_ABackwardsClockNeitherMintsNorDestroysTokens(t *testing.T) {
	c := tNewClock()
	tb := NewTokenBucket(TokenBucketConfig{Rate: 10, Burst: 3, Now: c.Now})
	tb.AllowN("k", 2)     // one token left
	c.Advance(-time.Hour) // NTP step, VM restore, ...
	if !tb.Allow("k").Allowed {
		t.Fatalf("denied after the clock jumped back: the token that was left must survive (negative elapsed time must not drain the bucket)")
	}
	if tb.Allow("k").Allowed {
		t.Fatalf("a second request passed after the clock jumped back: negative elapsed time must not refill the bucket")
	}
	c.Advance(time.Hour + ms(100)) // back to the original time + 100ms
	if !tb.Allow("k").Allowed {
		t.Errorf("denied 100ms after the exhaustion: one token should have refilled")
	}
}

func TestTokenBucket_SweepDropsOnlyFullBuckets(t *testing.T) {
	c := tNewClock()
	tb := NewTokenBucket(TokenBucketConfig{Rate: 10, Burst: 10, Now: c.Now})
	tb.Allow("light")      // 9 tokens: full again after 100 ms
	tb.AllowN("heavy", 10) // 0 tokens: full again after 1 s
	c.Advance(ms(500))
	if n := tb.Sweep(); n != 1 {
		t.Fatalf("Sweep() removed %d keys, want 1 (only light is full again)", n)
	}
	if tb.Len() != 1 {
		t.Errorf("Len() = %d, want 1", tb.Len())
	}
	if d := tb.AllowN("heavy", 10); d.Allowed {
		t.Errorf("heavy had refilled only 5 tokens but AllowN(10) passed: Sweep must not forgive a key with debt")
	}
	c.Advance(time.Second)
	if n := tb.Sweep(); n != 1 || tb.Len() != 0 {
		t.Errorf("after a full refill Sweep() = %d, Len() = %d; want 1 and 0", n, tb.Len())
	}
}

// ---------- sliding window specifics ----------

func TestSlidingWindowLog_IsExactAtTheEdgeOfTheWindow(t *testing.T) {
	c := tNewClock()
	l := NewSlidingWindowLog(WindowConfig{Limit: 3, Window: time.Second, Now: c.Now})
	for i := 0; i < 3; i++ { // at t = 0, 0.4, 0.8
		if !l.Allow("k").Allowed {
			t.Fatalf("request %d denied", i+1)
		}
		c.Advance(ms(400))
	}
	c.Advance(-ms(300)) // t = 0.9
	d := l.Allow("k")
	if d.Allowed || d.RetryAfter != ms(100) {
		t.Fatalf("t=0.9: %+v; want denied with RetryAfter = 100ms (the oldest entry, t=0, expires at 1.0)", d)
	}
	c.Advance(ms(100)) // t = 1.0: the entry from t=0 is exactly one window old and has expired
	if !l.Allow("k").Allowed {
		t.Errorf("t=1.0: denied, want allowed: an entry expires exactly Window after it was made")
	}
	if l.Allow("k").Allowed {
		t.Errorf("t=1.0: a second request passed, but entries from 0.4 and 0.8 are still inside the window")
	}
}

func TestSlidingWindowLog_AllowNWaitsForEnoughEntriesToExpire(t *testing.T) {
	c := tNewClock()
	l := NewSlidingWindowLog(WindowConfig{Limit: 4, Window: time.Second, Now: c.Now})
	l.Allow("k") // t=0
	c.Advance(ms(200))
	l.Allow("k") // t=0.2
	c.Advance(ms(200))
	l.AllowN("k", 2) // t=0.4 (two entries)
	d := l.AllowN("k", 3)
	// 4 in window, want 3 more => the 3 oldest must expire => the third oldest entry (t=0.4) + 1s - now(0.4) = 1s
	if d.Allowed || d.RetryAfter != time.Second {
		t.Errorf("AllowN(3): %+v; want denied, RetryAfter = 1s (three entries must expire; the third oldest is the t=0.4 one)", d)
	}
	d = l.AllowN("k", 1)
	if d.Allowed || d.RetryAfter != ms(600) {
		t.Errorf("AllowN(1): %+v; want denied, RetryAfter = 600ms", d)
	}
}

func TestSlidingWindowLog_SweepDropsKeysWithNoLiveEntries(t *testing.T) {
	c := tNewClock()
	l := NewSlidingWindowLog(WindowConfig{Limit: 10, Window: time.Second, Now: c.Now})
	l.Allow("old")
	c.Advance(ms(600))
	l.Allow("recent")
	c.Advance(ms(600)) // old is 1.2 s old, recent is 0.6 s old
	if n := l.Sweep(); n != 1 || l.Len() != 1 {
		t.Errorf("Sweep() = %d, Len() = %d; want 1 and 1", n, l.Len())
	}
}

func TestSlidingWindowCounter_WeighsThePreviousWindowByHowMuchOfItIsStillInView(t *testing.T) {
	c := tNewClock()
	l := NewSlidingWindowCounter(WindowConfig{Limit: 10, Window: time.Second, Now: c.Now})
	c.Advance(ms(500))
	if d := l.AllowN("k", 10); !d.Allowed {
		t.Fatalf("filling window 0 failed: %+v", d)
	}
	c.Advance(ms(1000)) // t = 1.5 s: halfway into window 1; half of window 0 is still in view
	n := 0
	for l.Allow("k").Allowed {
		n++
		if n > 100 {
			break
		}
	}
	if n != 5 {
		t.Errorf("%d requests allowed halfway into the next window, want 5 (estimate = 10 × 0.5 + curr)", n)
	}
}

func TestSlidingWindowCounter_IsAnApproximationTheLogIsExact(t *testing.T) {
	// 10 requests arrive in the last 10 ms of window 0. At t = 1.5 s, all
	// of them are 0.5 s old: a true sliding window still holds 10. The
	// counter assumes they were spread evenly, so it believes only 5 remain.
	c := tNewClock()
	cfg := WindowConfig{Limit: 10, Window: time.Second, Now: c.Now}
	counter, log := NewSlidingWindowCounter(cfg), NewSlidingWindowLog(cfg)
	c.Advance(990 * time.Millisecond)
	counter.AllowN("k", 10)
	log.AllowN("k", 10)
	c.Advance(ms(510)) // t = 1.5 s
	count := func(l Limiter) int {
		n := 0
		for n < 100 && l.Allow("k").Allowed {
			n++
		}
		return n
	}
	gotCounter, gotLog := count(counter), count(log)
	if gotLog != 0 {
		t.Errorf("exact log allowed %d, want 0 (all 10 earlier requests are inside the last second)", gotLog)
	}
	if gotCounter != 5 {
		t.Errorf("counter allowed %d, want 5: its estimate is 10 × (1 − 0.5) = 5, which over-admits bursty traffic", gotCounter)
	}
}

func TestSlidingWindowCounter_TracksTheLogOnSteadyTraffic(t *testing.T) {
	// 40 req/s of perfectly steady traffic against a limit of 10 per second
	// for 20 s. Both limiters should admit close to 10 per second.
	run := func(mk func(*tClock) Limiter) int {
		c := tNewClock()
		l := mk(c)
		allowed := 0
		for i := 0; i < 800; i++ {
			c.Advance(ms(25))
			if l.Allow("k").Allowed {
				allowed++
			}
		}
		return allowed
	}
	cfg := func(c *tClock) WindowConfig { return WindowConfig{Limit: 10, Window: time.Second, Now: c.Now} }
	exact := run(func(c *tClock) Limiter { return NewSlidingWindowLog(cfg(c)) })
	approx := run(func(c *tClock) Limiter { return NewSlidingWindowCounter(cfg(c)) })
	t.Logf("20 s of 40 req/s against 10/s: exact log admitted %d, counter admitted %d", exact, approx)
	if exact < 195 || exact > 215 {
		t.Errorf("exact log admitted %d, want ≈ 200", exact)
	}
	if diff := approx - exact; diff < -20 || diff > 20 {
		t.Errorf("counter admitted %d vs exact %d; on steady traffic they should agree within 10%%", approx, exact)
	}
}

func TestSlidingWindowCounter_RetryAfterCrossesIntoTheNextWindowWhenNeeded(t *testing.T) {
	c := tNewClock()
	l := NewSlidingWindowCounter(WindowConfig{Limit: 10, Window: time.Second, Now: c.Now})
	l.AllowN("k", 10) // t = 0: window 0 is full
	d := l.Allow("k")
	// In window 1, estimate(x) = 10 × (1 − x); need ≤ 9 => x ≥ 0.1 => t = 1.1 s
	if d.Allowed || !tNear(d.RetryAfter, ms(1100), time.Microsecond) {
		t.Errorf("got %+v; want denied with RetryAfter ≈ 1.1s: waiting for the next window is not enough, its estimate starts at 10", d)
	}
}

func TestSlidingWindowCounter_SkippedWindowsForgetEverything(t *testing.T) {
	c := tNewClock()
	l := NewSlidingWindowCounter(WindowConfig{Limit: 10, Window: time.Second, Now: c.Now})
	l.AllowN("k", 10)
	c.Advance(2500 * time.Millisecond) // two windows later: prev must be 0, not the old curr
	if d := l.AllowN("k", 10); !d.Allowed {
		t.Errorf("got %+v; a key silent for 2+ windows must start fresh", d)
	}
}

func TestSlidingWindowCounter_SweepDropsKeysWhoseEstimateIsZero(t *testing.T) {
	c := tNewClock()
	l := NewSlidingWindowCounter(WindowConfig{Limit: 10, Window: time.Second, Now: c.Now})
	l.Allow("stale")
	c.Advance(ms(1500))
	l.Allow("fresh")    // window 1
	c.Advance(ms(1000)) // window 2: stale (window 0) is 2 windows old; fresh (window 1) still weighs on the estimate
	if n := l.Sweep(); n != 1 {
		t.Errorf("Sweep() = %d, want 1 (stale only)", n)
	}
	if l.Len() != 1 {
		t.Errorf("Len() = %d, want 1", l.Len())
	}
}

// ---------- HTTP glue ----------

func TestRetryAfterSeconds_RoundsUp(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want int
	}{{0, 0}, {-time.Second, 0}, {time.Nanosecond, 1}, {ms(999), 1}, {time.Second, 1}, {ms(1001), 2}, {ms(2500), 3}, {60 * time.Second, 60}}
	for _, c := range cases {
		if got := RetryAfterSeconds(c.d); got != c.want {
			t.Errorf("RetryAfterSeconds(%v) = %d, want %d (round UP: an early retry just earns another 429)", c.d, got, c.want)
		}
	}
}

func TestDecision_SetHeaders(t *testing.T) {
	h := http.Header{}
	Decision{Allowed: true, Limit: 100, Remaining: 42}.SetHeaders(h)
	if h.Get("RateLimit-Limit") != "100" || h.Get("RateLimit-Remaining") != "42" {
		t.Errorf("headers = %v; want RateLimit-Limit 100 and RateLimit-Remaining 42", h)
	}
	if h.Get("Retry-After") != "" {
		t.Errorf("Retry-After set on an allowed request: %v", h)
	}
	h = http.Header{}
	Decision{Allowed: false, Limit: 100, Remaining: 0, RetryAfter: ms(1500)}.SetHeaders(h)
	if h.Get("Retry-After") != "2" || h.Get("RateLimit-Remaining") != "0" {
		t.Errorf("denied headers = %v; want Retry-After 2 (1.5 s rounded up) and Remaining 0", h)
	}
	h = http.Header{}
	Decision{Allowed: false, Limit: 5, Remaining: -3}.SetHeaders(h)
	if h.Get("Retry-After") != "1" || h.Get("RateLimit-Remaining") != "0" {
		t.Errorf("degenerate denial headers = %v; want Retry-After at least 1 and Remaining never negative", h)
	}
}

func TestMiddleware_Returns429WithRetryAfterAndSkipsTheHandler(t *testing.T) {
	c := tNewClock()
	l := NewTokenBucket(TokenBucketConfig{Rate: 1, Burst: 2, Now: c.Now})
	var calls atomic.Int64
	h := Middleware(l, func(r *http.Request) string { return r.Header.Get("X-Client") })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	do := func(client string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/accounts", nil)
		req.Header.Set("X-Client", client)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if r := do("a"); r.Code != 200 || r.Header().Get("RateLimit-Remaining") != "1" || r.Header().Get("RateLimit-Limit") != "2" {
		t.Fatalf("1st: code %d headers %v; want 200 with RateLimit-Limit 2, Remaining 1", r.Code, r.Header())
	}
	if r := do("a"); r.Code != 200 {
		t.Fatalf("2nd: code %d, want 200", r.Code)
	}
	r := do("a")
	if r.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd: code %d, want 429", r.Code)
	}
	if r.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After = %q, want \"1\" (one token at 1/s)", r.Header().Get("Retry-After"))
	}
	if r.Body.Len() == 0 || r.Header().Get("Content-Type") != "application/json" {
		t.Errorf("429 should carry a JSON error body; got type %q, %d bytes", r.Header().Get("Content-Type"), r.Body.Len())
	}
	if calls.Load() != 2 {
		t.Errorf("handler ran %d times, want 2: a rejected request must never reach the handler", calls.Load())
	}
	if r := do("b"); r.Code != 200 {
		t.Errorf("another client got %d, want 200: limits are per key", r.Code)
	}
	c.Advance(time.Second)
	if r := do("a"); r.Code != 200 {
		t.Errorf("after waiting Retry-After: code %d, want 200", r.Code)
	}
}
