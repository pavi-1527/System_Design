package ratelimit

import (
	"errors"
	"math/rand"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- helpers ----------------------------------------------------------------

func mustFactory(t *testing.T, f Factory, err error) Factory {
	t.Helper()
	if err != nil {
		t.Fatalf("building a Factory returned error %v", err)
	}
	if f == nil {
		t.Fatal("a Factory constructor returned nil with a nil error")
	}
	return f
}

func slidingWindow(t *testing.T, limit int, window time.Duration) Factory {
	t.Helper()
	f, err := SlidingWindow(limit, window)
	return mustFactory(t, f, err)
}

func newWith(t *testing.T, f Factory, opts ...Option) (*Limiter, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	l, err := NewWithStrategy(f, append([]Option{WithClock(clk)}, opts...)...)
	if err != nil {
		t.Fatalf("NewWithStrategy returned error %v", err)
	}
	if l == nil {
		t.Fatal("NewWithStrategy returned a nil *Limiter with a nil error")
	}
	return l, clk
}

// algorithms are the two built-in strategies, each configured as "3 permits,
// 10 seconds": a bucket of 3 refilling 3 per 10s, and a window of 3 per 10s.
func algorithms(t *testing.T) map[string]Factory {
	t.Helper()
	tb, err := TokenBucket(Rate{3, 10 * time.Second}, 3)
	sw, err2 := SlidingWindow(3, 10*time.Second)
	return map[string]Factory{
		"token bucket":   mustFactory(t, tb, err),
		"sliding window": mustFactory(t, sw, err2),
	}
}

func mustCheck(t *testing.T, l *Limiter, key string, n int) Decision {
	t.Helper()
	d, err := l.Check(key, n)
	if err != nil {
		t.Fatalf("Check(%q, %d) error = %v", key, n, err)
	}
	return d
}

// ---- stage 2: strategies, retry-after, sharding -----------------------------

func TestStage2_FactoryConstructorsRejectInvalidConfig(t *testing.T) {
	if f, err := TokenBucket(Rate{0, time.Second}, 1); err == nil || f != nil {
		t.Errorf("TokenBucket with Events=0 = (%v, %v), want (nil, ErrInvalidConfig)", f != nil, err)
	}
	if f, err := TokenBucket(Rate{1, time.Second}, 0); err == nil || f != nil {
		t.Errorf("TokenBucket with burst=0 = (%v, %v), want (nil, ErrInvalidConfig)", f != nil, err)
	}
	for _, c := range []struct {
		limit  int
		window time.Duration
	}{{0, time.Second}, {-1, time.Second}, {5, 0}, {5, -time.Second}} {
		if f, err := SlidingWindow(c.limit, c.window); !errors.Is(err, ErrInvalidConfig) || f != nil {
			t.Errorf("SlidingWindow(%d, %v) = (%v, %v), want (nil, ErrInvalidConfig)", c.limit, c.window, f != nil, err)
		}
	}
	if l, err := NewWithStrategy(nil); err == nil || l != nil {
		t.Errorf("NewWithStrategy(nil) = (%v, %v), want an error", l != nil, err)
	}
	f := slidingWindow(t, 3, time.Second)
	for _, n := range []int{0, -1} {
		if l, err := NewWithStrategy(f, WithShards(n)); !errors.Is(err, ErrInvalidConfig) || l != nil {
			t.Errorf("WithShards(%d) = (%v, %v), want ErrInvalidConfig", n, l != nil, err)
		}
	}
}

func TestStage2_NewIsATokenBucketAndTheAlgorithmIsChosenByFactory(t *testing.T) {
	// Same numbers, different algorithm, different behaviour: 4s after
	// draining 3 permits the bucket has refilled one (3 per 10s = every 3.33s);
	// the sliding window frees nothing until 10s have passed.
	cases := []struct {
		name  string
		build func(*testing.T) (*Limiter, *fakeClock)
		want  bool // Allow 4s after draining?
	}{
		{"New(Rate{3, 10s}, 3)", func(t *testing.T) (*Limiter, *fakeClock) { return newLimiter(t, Rate{3, 10 * time.Second}, 3) }, true},
		{"TokenBucket factory", func(t *testing.T) (*Limiter, *fakeClock) { return newWith(t, algorithms(t)["token bucket"]) }, true},
		{"SlidingWindow factory", func(t *testing.T) (*Limiter, *fakeClock) { return newWith(t, algorithms(t)["sliding window"]) }, false},
	}
	for _, c := range cases {
		l, clk := c.build(t)
		drain(l, "k")
		clk.Advance(4 * time.Second)
		if got := l.Allow("k"); got != c.want {
			t.Errorf("%s: Allow 4s after draining = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestStage2_SlidingWindowBoundariesAreExact(t *testing.T) {
	f := slidingWindow(t, 3, 10*time.Second)
	l, clk := newWith(t, f)
	for i := 0; i < 3; i++ { // permits at +0s, +1s, +2s
		if !l.Allow("k") {
			t.Fatalf("request %d denied inside an empty window", i+1)
		}
		clk.Advance(time.Second)
	}
	clk.Advance(-time.Second) // now +2s
	d := mustCheck(t, l, "k", 1)
	if d.Allowed || d.RetryAfter != 8*time.Second {
		t.Fatalf("4th request at +2s = %+v, want denied with RetryAfter 8s (the +0s permit leaves at +10s)", d)
	}
	clk.Advance(8*time.Second - time.Nanosecond)
	if l.Allow("k") {
		t.Fatal("allowed 1ns before the oldest permit leaves the window")
	}
	clk.Advance(time.Nanosecond)
	if !l.Allow("k") {
		t.Fatal("denied at exactly t+window: a permit stops counting AT t+window (same convention as 'expired means now >= expiresAt')")
	}
	d = mustCheck(t, l, "k", 1) // window now holds +1s, +2s, +10s
	if d.Allowed || d.RetryAfter != time.Second {
		t.Fatalf("next request = %+v, want denied, RetryAfter 1s (the +1s permit leaves at +11s)", d)
	}
}

func TestStage2_SlidingWindowHasNoBoundaryBurst(t *testing.T) {
	// A fixed window of 10s would admit 3 requests at +9.9s and 3 more at
	// +10.1s: 6 in 200ms against a limit of 3 per 10s. A sliding window must not.
	f := slidingWindow(t, 3, 10*time.Second)
	l, clk := newWith(t, f)
	clk.Advance(9900 * time.Millisecond)
	if got := drain(l, "k"); got != 3 {
		t.Fatalf("first burst admitted %d, want 3", got)
	}
	clk.Advance(200 * time.Millisecond)
	d := mustCheck(t, l, "k", 1)
	if d.Allowed {
		t.Fatal("request at +10.1s allowed: 4 permits inside one 10s window (a fixed-window counter would do this)")
	}
	if want := 9800 * time.Millisecond; d.RetryAfter != want {
		t.Fatalf("RetryAfter = %v, want %v", d.RetryAfter, want)
	}
}

func TestStage2_SlidingWindowBatchesAreAccountedByPermitCount(t *testing.T) {
	f := slidingWindow(t, 5, time.Second)
	l, clk := newWith(t, f)
	if !l.AllowN("k", 3) {
		t.Fatal("AllowN(3) denied in an empty window of 5")
	}
	clk.Advance(400 * time.Millisecond)
	if !l.AllowN("k", 2) {
		t.Fatal("AllowN(2) denied with 3 of 5 used")
	}
	d := mustCheck(t, l, "k", 4)
	// Four permits must be free. The first batch (3 permits, taken at +0) leaves at +1s and
	// is not enough; the second batch (taken at +0.4s) leaves at +1.4s, which is 1s from now.
	if d.Allowed || d.RetryAfter != time.Second {
		t.Fatalf("Check(4) = %+v, want denied with RetryAfter 1s (a RetryAfter of 600ms would only wait for the first batch)", d)
	}
}

func TestStage2_CheckReportsRemainingAndRetryAfter(t *testing.T) {
	for name, f := range algorithms(t) {
		l, _ := newWith(t, f)
		d := mustCheck(t, l, "k", 2)
		if !d.Allowed || d.Remaining != 1 || d.RetryAfter != 0 {
			t.Errorf("%s: Check(2) on a fresh key = %+v, want allowed, Remaining 1, RetryAfter 0", name, d)
		}
		d = mustCheck(t, l, "k", 2)
		if d.Allowed || d.Remaining != 1 || d.RetryAfter <= 0 {
			t.Errorf("%s: Check(2) with 1 left = %+v, want denied, Remaining 1 (a denial consumes nothing), RetryAfter > 0", name, d)
		}
		d = mustCheck(t, l, "k", 1)
		if !d.Allowed || d.Remaining != 0 {
			t.Errorf("%s: Check(1) = %+v, want allowed, Remaining 0", name, d)
		}
	}
}

func TestStage2_RetryAfterIsExactUnderRandomTraffic(t *testing.T) {
	// Property: after ANY history, if Check(n) is denied with RetryAfter d,
	// then waiting d-1ns is still too early and waiting d is enough. Too small
	// and clients hammer you; too large and you waste the vendor's capacity.
	for name, f := range algorithms(t) {
		l, clk := newWith(t, f)
		rng := rand.New(rand.NewSource(42))
		denied := 0
		for round := 0; round < 400 && denied < 120; round++ {
			key := "k" + strconv.Itoa(rng.Intn(3))
			clk.Advance(time.Duration(rng.Int63n(int64(4 * time.Second))))
			n := 1 + rng.Intn(3)
			d := mustCheck(t, l, key, n)
			if d.Allowed {
				continue
			}
			denied++
			if d.RetryAfter <= 0 {
				t.Fatalf("%s: denied with RetryAfter %v, want > 0", name, d.RetryAfter)
			}
			clk.Advance(d.RetryAfter - 1)
			if d2 := mustCheck(t, l, key, n); d2.Allowed || d2.RetryAfter != 1 {
				t.Fatalf("%s: 1ns before RetryAfter (%v) the same request gave %+v, want denied with RetryAfter exactly 1ns", name, d.RetryAfter, d2)
			}
			clk.Advance(1)
			if d2 := mustCheck(t, l, key, n); !d2.Allowed {
				t.Fatalf("%s: still denied after waiting the advertised RetryAfter %v (n=%d)", name, d.RetryAfter, n)
			}
		}
		if denied < 20 {
			t.Fatalf("%s: only %d denials in the generated traffic: the property was barely exercised", name, denied)
		}
	}
}

func TestStage2_CheckRejectsImpossibleRequestsWithoutState(t *testing.T) {
	for name, f := range algorithms(t) {
		l, _ := newWith(t, f)
		for i := 0; i < 100; i++ {
			key := "k" + strconv.Itoa(i)
			for _, n := range []int{0, -3} {
				if _, err := l.Check(key, n); !errors.Is(err, ErrInvalidN) {
					t.Fatalf("%s: Check(n=%d) error = %v, want ErrInvalidN", name, n, err)
				}
			}
			if _, err := l.Check(key, 4); !errors.Is(err, ErrExceedsLimit) {
				t.Fatalf("%s: Check(4) with a limit of 3: error = %v, want ErrExceedsLimit (it can never succeed, so a RetryAfter would be a lie)", name, err)
			}
		}
		if l.Len() != 0 {
			t.Fatalf("%s: Len = %d after only impossible requests, want 0", name, l.Len())
		}
	}
}

func TestStage2_SlidingWindowSurvivesAClockRewind(t *testing.T) {
	f := slidingWindow(t, 2, 10*time.Second)
	l, clk := newWith(t, f)
	clk.Advance(time.Minute)
	T := clk.Now()
	drain(l, "k")
	clk.Set(T.Add(-time.Hour))
	if l.Allow("k") {
		t.Fatal("allowed with the clock an hour in the past")
	}
	clk.Set(T.Add(5 * time.Second))
	if l.Allow("k") {
		t.Fatal("allowed 5s after the permits were taken, window is 10s")
	}
	clk.Set(T.Add(10 * time.Second))
	if got := drain(l, "k"); got != 2 {
		t.Fatalf("after the window passed %d permits were available, want 2", got)
	}
}

func TestStage2_SlidingWindowLogIsTrimmed(t *testing.T) {
	// 400,000 admitted requests, but never more than 100 inside one second.
	// A log that is never trimmed keeps all 400,000 timestamps (~6 MB).
	f := slidingWindow(t, 100, time.Second)
	l, clk := newWith(t, f)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 400_000; i++ {
		clk.Advance(10 * time.Millisecond)
		if !l.Allow("k") {
			t.Fatalf("request %d denied: exactly 100 per second is within the limit", i)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(l)
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 3<<20 {
		t.Fatalf("heap grew by %d KiB serving one key: the window log must drop expired entries (memory per key is O(limit), not O(requests))", grown>>10)
	}
}

// countingStrategy is a custom Strategy that allows everything, and makes the
// Limiter's promises observable.
type countingStrategy struct {
	calls  int          // deliberately unsynchronised: -race flags a Limiter that lets two Takes overlap
	inside atomic.Int32 // overlap detector for builds without -race
	times  *[]time.Duration
}

func (s *countingStrategy) Take(now time.Duration, n int) Decision {
	if s.inside.Add(1) != 1 {
		panic("two Take calls overlapped on one key: per-key atomicity is broken")
	}
	s.calls++
	if s.times != nil {
		*s.times = append(*s.times, now)
	}
	runtime.Gosched()
	s.inside.Add(-1)
	return Decision{Allowed: true, Remaining: 1}
}
func (s *countingStrategy) Burst() int              { return 5 }
func (s *countingStrategy) Idle(time.Duration) bool { return false }

func TestStage2_CustomStrategiesPlugInAndAreCreatedLazilyPerKey(t *testing.T) {
	var made atomic.Int64
	f := func(time.Duration) Strategy { made.Add(1); return &countingStrategy{} }
	l, _ := newWith(t, f)
	if got := made.Load(); got > 1 {
		t.Fatalf("factory called %d times by the constructor, want at most 1 (a probe for Burst)", got)
	}
	base := made.Load()
	for i := 0; i < 1000; i++ {
		l.Allow("a")
		l.Allow("b")
		l.Allow("c")
	}
	if got := made.Load() - base; got != 3 {
		t.Fatalf("factory called %d times for 3 keys, want 3: exactly once per key, on first use", got)
	}
	if l.AllowN("a", 6) {
		t.Fatal("AllowN above the strategy's Burst() was allowed")
	}
}

func TestStage2_StrategiesAreOnlyEverCalledSeriallyPerKey(t *testing.T) {
	f := func(time.Duration) Strategy { return &countingStrategy{} }
	l, _ := newWith(t, f)
	var wg sync.WaitGroup
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				l.Allow("same-key")
			}
		}()
	}
	within(t, "concurrent Take on one key", wg.Wait)
}

func TestStage2_StrategyTimeNeverGoesBackwardsAndMatchesTheClock(t *testing.T) {
	var times []time.Duration // guarded by the Limiter's per-key serialisation
	f := func(time.Duration) Strategy { return &countingStrategy{times: &times} }
	l, clk := newWith(t, f)
	l.Allow("k")
	clk.Advance(7 * time.Second)
	l.Allow("k")
	clk.Set(clk.Now().Add(-time.Hour))
	l.Allow("k")
	clk.Set(clk.Now().Add(time.Hour + 3*time.Second))
	l.Allow("k")
	if len(times) != 4 {
		t.Fatalf("strategy saw %d calls, want 4", len(times))
	}
	if d := times[1] - times[0]; d != 7*time.Second {
		t.Errorf("a 7s clock advance reached the strategy as %v: offsets must track the clock exactly", d)
	}
	if times[2] != times[1] {
		t.Errorf("after a rewind the strategy saw %v, want it frozen at %v (time must never go backwards for a strategy)", times[2], times[1])
	}
	if d := times[3] - times[2]; d != 3*time.Second {
		t.Errorf("clock is 3s past the old high-water mark but the strategy advanced %v", d)
	}
}

// gateStrategy lets a test observe how many Take calls are inside the
// Limiter at the same moment.
type gateStrategy struct {
	inFlight, maxSeen *atomic.Int32
}

func (g gateStrategy) Take(time.Duration, int) Decision {
	cur := g.inFlight.Add(1)
	for {
		m := g.maxSeen.Load()
		if cur <= m || g.maxSeen.CompareAndSwap(m, cur) {
			break
		}
	}
	// Linger a little so a second goroutine can arrive, but give up quickly:
	// with a single global lock nobody can ever arrive.
	deadline := time.Now().Add(20 * time.Millisecond)
	for g.inFlight.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Microsecond)
	}
	g.inFlight.Add(-1)
	return Decision{Allowed: true}
}
func (gateStrategy) Burst() int              { return 1 }
func (gateStrategy) Idle(time.Duration) bool { return false }

func TestStage2_UnrelatedKeysDoNotContend(t *testing.T) {
	var inFlight, maxSeen atomic.Int32
	f := func(time.Duration) Strategy { return gateStrategy{&inFlight, &maxSeen} }
	l, _ := newWith(t, f, WithShards(16))
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l.Allow("tenant-" + strconv.Itoa(i))
		}(i)
	}
	within(t, "64 first requests on 64 different keys", wg.Wait)
	if maxSeen.Load() < 2 {
		t.Fatal("never saw two keys being decided at the same time with WithShards(16): one lock serialises every key. Shard the key space so unrelated keys do not wait for each other")
	}

	// And with one shard the same workload is serialised: WithShards must be honoured.
	inFlight.Store(0)
	maxSeen.Store(0)
	l1, _ := newWith(t, f, WithShards(1))
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l1.Allow("tenant-" + strconv.Itoa(i))
		}(i)
	}
	within(t, "8 first requests with one shard", wg.Wait)
	if got := maxSeen.Load(); got != 1 {
		t.Fatalf("WithShards(1) still had %d keys inside the limiter at once: the option is ignored or the shards share state", got)
	}
}

func TestStage2_ManyKeysManyGoroutinesBothAlgorithms(t *testing.T) {
	for name, f := range algorithms(t) {
		l, _ := newWith(t, f, WithShards(8))
		var allowed [20]atomic.Int64
		var wg sync.WaitGroup
		for w := 0; w < 32; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < 2000; i++ {
					k := (w*7 + i) % len(allowed)
					if l.Allow("k" + strconv.Itoa(k)) {
						allowed[k].Add(1)
					}
				}
			}(w)
		}
		within(t, name+" workload", wg.Wait)
		for k := range allowed {
			if got := allowed[k].Load(); got != 3 {
				t.Fatalf("%s: key %d admitted %d, want exactly 3 on a frozen clock", name, k, got)
			}
		}
	}
}
