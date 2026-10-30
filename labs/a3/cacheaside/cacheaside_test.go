package cacheaside

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

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

const tTimeout = 2 * time.Second

// tWait waits for wg, failing the test instead of hanging.
func tWait(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-tTimer(t):
		t.Fatalf("%s did not finish within %v (deadlock, or a waiter that is never released?)", what, tTimeout)
	}
}

// tTimer returns a channel that closes when the test timeout passes or as
// soon as the test has already failed (so a stub that returns errors does not
// make every test wait for its full timeout).
func tTimer(t *testing.T) <-chan struct{} {
	ch := make(chan struct{})
	stop := make(chan struct{})
	exited := make(chan struct{})
	t.Cleanup(func() { close(stop); <-exited })
	go func() {
		defer close(exited)
		defer close(ch)
		deadline := time.Now().Add(tTimeout)
		for time.Now().Before(deadline) && !t.Failed() {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	return ch
}

// tCleanupWait makes the test wait (briefly) for its goroutines before it is
// considered finished, so that an early t.FailNow never leaves a goroutine
// calling t.Errorf on a completed test.
func tCleanupWait(t *testing.T, wg *sync.WaitGroup) {
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(tTimeout):
		}
	})
}

// tEventually polls cond until it is true or the timeout passes.
func tEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(tTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		if t.Failed() { // a goroutine already reported a problem: don't wait for a timeout
			t.FailNow()
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// tLoader is a fake source of truth. Every call is counted. If gate is
// non-nil the loader blocks until it is closed (or ctx is done).
type tLoader struct {
	calls atomic.Int64
	gate  chan struct{}
	fn    func(ctx context.Context, key string, n int64) (string, error)
}

func (l *tLoader) Load(ctx context.Context, key string) (string, error) {
	n := l.calls.Add(1)
	if l.gate != nil {
		select {
		case <-l.gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if l.fn != nil {
		return l.fn(ctx, key, n)
	}
	return fmt.Sprintf("%s@%d", key, n), nil
}

func tWaitInFlight(t *testing.T, c *Cache[string], key string, n int) {
	t.Helper()
	tEventually(t, fmt.Sprintf("%d Gets waiting on %q", n, key), func() bool { return c.InFlight(key) == n })
}

func tGroupWaiters(t *testing.T, g *Group[string], key string, n int) {
	t.Helper()
	tEventually(t, fmt.Sprintf("%d callers waiting in Group.Do(%q)", n, key), func() bool { return g.Waiters(key) == n })
}

// ======================================================================
// Group
// ======================================================================

func TestGroup_ConcurrentCallsRunFnOnce(t *testing.T) {
	var g Group[string]
	var calls atomic.Int64
	release := make(chan struct{})
	fn := func(ctx context.Context) (string, error) {
		calls.Add(1)
		<-release
		return "v", nil
	}
	const n = 50
	var sharedCount atomic.Int64
	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	results := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, shared, err := g.Do(context.Background(), "k", fn)
			if err != nil {
				t.Errorf("Do: %v", err)
			}
			if shared {
				sharedCount.Add(1)
			}
			results[i] = v
		}(i)
	}
	tGroupWaiters(t, &g, "k", n)
	close(release)
	tWait(t, &wg, "50 concurrent Do calls")
	if calls.Load() != 1 {
		t.Fatalf("fn ran %d times for %d concurrent callers of one key, want 1: this is the stampede singleflight exists to stop", calls.Load(), n)
	}
	for i, v := range results {
		if v != "v" {
			t.Fatalf("caller %d got %q, want %q", i, v, "v")
		}
	}
	if sharedCount.Load() != n-1 {
		t.Fatalf("%d callers reported shared=true, want %d (everyone except the leader)", sharedCount.Load(), n-1)
	}
	if g.Waiters("k") != 0 {
		t.Fatalf("Waiters after completion = %d, want 0", g.Waiters("k"))
	}
}

func TestGroup_ResultsAreNotRememberedBetweenCalls(t *testing.T) {
	var g Group[string]
	var calls atomic.Int64
	fn := func(ctx context.Context) (string, error) {
		return fmt.Sprint("run", calls.Add(1)), nil
	}
	for i := 1; i <= 3; i++ {
		v, shared, err := g.Do(context.Background(), "k", fn)
		if err != nil || shared || v != fmt.Sprint("run", i) {
			t.Fatalf("call %d = (%q, shared=%v, %v), want (run%d, false, nil): Group collapses concurrent calls, it is not a cache", i, v, shared, err, i)
		}
	}
}

func TestGroup_DifferentKeysDoNotBlockEachOther(t *testing.T) {
	var g Group[string]
	started := make(chan string, 2)
	release := make(chan struct{})
	fn := func(key string) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			started <- key
			<-release
			return key, nil
		}
	}
	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	for _, k := range []string{"a", "b"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			if _, _, err := g.Do(context.Background(), k, fn(k)); err != nil {
				t.Errorf("Do(%s): %v", k, err)
			}
		}(k)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-tTimer(t):
			t.Fatalf("only %d of 2 loads started: a slow load for one key blocked another key (is a global lock held while fn runs?)", i)
		}
	}
	close(release)
	tWait(t, &wg, "two keys")
}

func TestGroup_ErrorIsSharedAndNotRemembered(t *testing.T) {
	var g Group[string]
	var calls atomic.Int64
	boom := errors.New("db down")
	release := make(chan struct{})
	fn := func(ctx context.Context) (string, error) {
		calls.Add(1)
		<-release
		return "", boom
	}
	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := g.Do(context.Background(), "k", fn); !errors.Is(err, boom) {
				t.Errorf("err = %v, want the loader's error for every caller", err)
			}
		}()
	}
	tGroupWaiters(t, &g, "k", 10)
	close(release)
	tWait(t, &wg, "error fan-out")
	if calls.Load() != 1 {
		t.Fatalf("fn ran %d times, want 1", calls.Load())
	}
	v, _, err := g.Do(context.Background(), "k", func(context.Context) (string, error) { return "ok", nil })
	if err != nil || v != "ok" {
		t.Fatalf("after a failed call the key must be usable again, got (%q, %v)", v, err)
	}
}

func TestGroup_OneCallerCancelling_OthersAndLoadUnaffected(t *testing.T) {
	var g Group[string]
	release := make(chan struct{})
	var loadCtx context.Context
	var mu sync.Mutex
	fn := func(ctx context.Context) (string, error) {
		mu.Lock()
		loadCtx = ctx
		mu.Unlock()
		<-release
		return "v", nil
	}
	leaderCtx, cancelLeader := context.WithCancel(context.WithValue(context.Background(), tCtxKey{}, "trace-1"))
	defer cancelLeader()
	leaderErr := make(chan error, 1)
	go func() {
		_, _, err := g.Do(leaderCtx, "k", fn)
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("leader Do: %v", err)
		}
		leaderErr <- err
	}()
	tGroupWaiters(t, &g, "k", 1)

	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, shared, err := g.Do(context.Background(), "k", fn)
			if err != nil || v != "v" || !shared {
				t.Errorf("joiner got (%q, shared=%v, %v), want (v, true, nil)", v, shared, err)
			}
		}()
	}
	tGroupWaiters(t, &g, "k", 3)

	cancelLeader() // the caller that STARTED the load walks away
	select {
	case err := <-leaderErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller got %v, want context.Canceled", err)
		}
	case <-tTimer(t):
		t.Fatalf("a cancelled caller was not released promptly while the shared load is still running")
	}
	tGroupWaiters(t, &g, "k", 2)
	mu.Lock()
	lc := loadCtx
	mu.Unlock()
	if lc.Err() != nil {
		t.Fatalf("the shared load's context was cancelled (%v) although two callers still wait: one impatient client must not kill the load for everyone", lc.Err())
	}
	if lc.Value(tCtxKey{}) != "trace-1" {
		t.Fatalf("the load's context should keep the starting caller's values (trace ids, tenant) but not its cancellation")
	}
	close(release)
	tWait(t, &wg, "remaining callers")
}

type tCtxKey struct{}

func TestGroup_AllCallersCancelling_CancelsTheLoadAndFreesTheKey(t *testing.T) {
	var g Group[string]
	var calls atomic.Int64
	hold := make(chan struct{})
	oldLoadCancelled := make(chan struct{})
	fn := func(ctx context.Context) (string, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			close(oldLoadCancelled)
			<-hold // the abandoned load is slow to wind down
			return "", ctx.Err()
		}
		return "fresh", nil
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	for _, ctx := range []context.Context{ctx1, ctx2} {
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()
			if _, _, err := g.Do(ctx, "k", fn); !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled", err)
			}
		}(ctx)
	}
	tGroupWaiters(t, &g, "k", 2)
	cancel1()
	tGroupWaiters(t, &g, "k", 1)
	select {
	case <-oldLoadCancelled:
		t.Fatalf("load cancelled while one caller was still waiting")
	case <-time.After(20 * time.Millisecond):
	}
	cancel2()
	select {
	case <-oldLoadCancelled:
	case <-tTimer(t):
		t.Fatalf("every caller left but the load's context was never cancelled: abandoned loads keep burning the database")
	}
	tWait(t, &wg, "cancelled callers")

	// The abandoned fn is still blocked on hold; a new caller must not wait for it.
	done := make(chan string, 1)
	go func() {
		v, _, _ := g.Do(context.Background(), "k", fn)
		done <- v
	}()
	select {
	case v := <-done:
		if v != "fresh" {
			t.Fatalf("new caller got %q, want a fresh load", v)
		}
	case <-tTimer(t):
		t.Fatalf("a new Do joined the abandoned, cancelled call instead of starting a fresh one")
	}
	close(hold)
}

func TestGroup_AlreadyCancelledContextDoesNotStartFn(t *testing.T) {
	var g Group[string]
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := g.Do(ctx, "k", func(context.Context) (string, error) { calls.Add(1); return "v", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("fn ran for a caller whose context was already cancelled")
	}
	if g.Waiters("k") != 0 {
		t.Fatalf("Waiters = %d, want 0", g.Waiters("k"))
	}
}

func TestGroup_PanicReleasesAllWaiters(t *testing.T) {
	var g Group[string]
	release := make(chan struct{})
	fn := func(ctx context.Context) (string, error) {
		<-release
		panic("boom")
	}
	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := g.Do(context.Background(), "k", fn)
			var pe *PanicError
			if !errors.As(err, &pe) {
				t.Errorf("err = %v, want a *PanicError", err)
				return
			}
			if pe.Value != "boom" || len(pe.Stack) == 0 {
				t.Errorf("PanicError = {%v, %d stack bytes}, want value boom and a stack", pe.Value, len(pe.Stack))
			}
		}()
	}
	tGroupWaiters(t, &g, "k", 5)
	close(release)
	tWait(t, &wg, "panicking load (waiters must be released, not hang)")
	v, _, err := g.Do(context.Background(), "k", func(context.Context) (string, error) { return "ok", nil })
	if err != nil || v != "ok" {
		t.Fatalf("key wedged after a panic: (%q, %v)", v, err)
	}
}

func TestGroup_ManyKeysUnderRace(t *testing.T) {
	var g Group[int]
	var calls [10]atomic.Int64
	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	for w := 0; w < 40; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 300; i++ {
				k := rng.Intn(10)
				v, _, err := g.Do(context.Background(), fmt.Sprint(k), func(context.Context) (int, error) {
					calls[k].Add(1)
					time.Sleep(50 * time.Microsecond)
					return k * 7, nil
				})
				if err != nil || v != k*7 {
					t.Errorf("key %d: got (%d, %v)", k, v, err)
					return
				}
			}
		}(w)
	}
	tWait(t, &wg, "many keys")
}

// ======================================================================
// Cache
// ======================================================================

func TestCache_ColdKeyConcurrentGetsLoadOnce(t *testing.T) {
	l := &tLoader{gate: make(chan struct{})}
	c := New(Config{TTL: time.Minute}, l.Load)
	const n = 50
	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	got := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := c.Get(context.Background(), "acct:42")
			if err != nil {
				t.Errorf("Get: %v", err)
			}
			got[i] = v
		}(i)
	}
	tEventually(t, "all 50 Gets to be waiting on (or each calling) the loader", func() bool {
		return c.InFlight("acct:42") == n || l.calls.Load() >= n
	})
	close(l.gate)
	tWait(t, &wg, "50 concurrent cold Gets")
	if l.calls.Load() != 1 {
		t.Fatalf("loader called %d times for %d concurrent Gets of one cold key, want 1: a stampede sends every one of them to the database", l.calls.Load(), n)
	}
	for i, v := range got {
		if v != got[0] {
			t.Fatalf("caller %d got %q but caller 0 got %q: everyone must see the one loaded value", i, v, got[0])
		}
	}
	s := c.Stats()
	if s.Loads != 1 || s.Misses != n || s.Coalesced != n-1 {
		t.Fatalf("stats %+v, want Loads 1, Misses %d, Coalesced %d", s, n, n-1)
	}
	if v, err := c.Get(context.Background(), "acct:42"); err != nil || v != got[0] || l.calls.Load() != 1 {
		t.Fatalf("a Get after the load = (%q, %v) with %d loader calls; it should be a plain hit", v, err, l.calls.Load())
	}
}

func TestCache_DifferentKeysLoadConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	l := &tLoader{fn: func(ctx context.Context, key string, n int64) (string, error) {
		started <- key
		<-release
		return key, nil
	}}
	c := New(Config{TTL: time.Minute}, l.Load)
	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	for _, k := range []string{"a", "b"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			if _, err := c.Get(context.Background(), k); err != nil {
				t.Errorf("Get(%s): %v", k, err)
			}
		}(k)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-tTimer(t):
			t.Fatalf("only %d of 2 loads started: a slow load for one key must not block loads of other keys (is the cache mutex held while the loader runs?)", i)
		}
	}
	close(release)
	tWait(t, &wg, "two keys")
}

func TestCache_HitsUntilTTLThenReloads(t *testing.T) {
	clk := tNewClock()
	l := &tLoader{}
	c := New(Config{TTL: 10 * time.Second, Now: clk.Now}, l.Load)
	ctx := context.Background()
	v1, _ := c.Get(ctx, "k")
	v2, _ := c.Get(ctx, "k")
	if v1 != v2 || l.calls.Load() != 1 {
		t.Fatalf("second Get = %q after %q with %d loads, want a hit", v2, v1, l.calls.Load())
	}
	clk.Advance(10*time.Second - time.Nanosecond)
	if _, _ = c.Get(ctx, "k"); l.calls.Load() != 1 {
		t.Fatalf("reloaded 1ns before the TTL")
	}
	clk.Advance(time.Nanosecond)
	v3, _ := c.Get(ctx, "k")
	if l.calls.Load() != 2 || v3 == v1 {
		t.Fatalf("at now == fresh-until the entry must be reloaded; loads=%d value=%q", l.calls.Load(), v3)
	}
	if s := c.Stats(); s.Hits != 2 || s.Misses != 2 {
		t.Fatalf("stats %+v, want Hits 2 Misses 2", s)
	}
}

func TestCache_KeysAreIndependent(t *testing.T) {
	l := &tLoader{}
	c := New(Config{TTL: time.Minute}, l.Load)
	a, _ := c.Get(context.Background(), "a")
	b, _ := c.Get(context.Background(), "b")
	if a == b || l.calls.Load() != 2 {
		t.Fatalf("a=%q b=%q loads=%d: each key needs its own entry", a, b, l.calls.Load())
	}
}

func TestCache_NegativeCaching(t *testing.T) {
	clk := tNewClock()
	exists := false
	l := &tLoader{fn: func(ctx context.Context, key string, n int64) (string, error) {
		if !exists {
			return "", fmt.Errorf("account %s: %w", key, ErrNotFound)
		}
		return "found", nil
	}}
	c := New(Config{TTL: time.Hour, NegativeTTL: 5 * time.Second, Now: clk.Now}, l.Load)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		_, err := c.Get(ctx, "ghost")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get %d: err = %v, want ErrNotFound", i, err)
		}
	}
	if l.calls.Load() != 1 {
		t.Fatalf("loader called %d times for a missing key within NegativeTTL, want 1: repeated lookups of nonexistent keys (cache penetration) go straight to the database", l.calls.Load())
	}
	if c.Stats().NegativeHits != 3 {
		t.Fatalf("NegativeHits = %d, want 3", c.Stats().NegativeHits)
	}
	exists = true
	clk.Advance(4 * time.Second)
	if _, err := c.Get(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("negative entry should still be served after 4s of a 5s NegativeTTL, err = %v", err)
	}
	clk.Advance(time.Second)
	v, err := c.Get(ctx, "ghost")
	if err != nil || v != "found" || l.calls.Load() != 2 {
		t.Fatalf("after NegativeTTL: (%q, %v) with %d loads, want the new value from a second load (NegativeTTL must be short so a created record shows up soon)", v, err, l.calls.Load())
	}
	clk.Advance(30 * time.Minute)
	if _, err := c.Get(ctx, "ghost"); err != nil || l.calls.Load() != 2 {
		t.Fatalf("a found value must use TTL (1h), not NegativeTTL")
	}
}

func TestCache_NegativeTTLZeroDoesNotCacheNotFound(t *testing.T) {
	l := &tLoader{fn: func(context.Context, string, int64) (string, error) { return "", ErrNotFound }}
	c := New(Config{TTL: time.Hour}, l.Load)
	for i := 0; i < 3; i++ {
		c.Get(context.Background(), "ghost")
	}
	if l.calls.Load() != 3 {
		t.Fatalf("loader calls = %d, want 3 when NegativeTTL is 0", l.calls.Load())
	}
}

func TestCache_ErrorsAreNotCachedByDefault(t *testing.T) {
	boom := errors.New("db timeout")
	l := &tLoader{fn: func(ctx context.Context, key string, n int64) (string, error) {
		if n == 1 {
			return "", boom
		}
		return "ok", nil
	}}
	c := New(Config{TTL: time.Minute, NegativeTTL: time.Minute}, l.Load) // a transient error is not "not found"
	if _, err := c.Get(context.Background(), "k"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the loader's error", err)
	}
	v, err := c.Get(context.Background(), "k")
	if err != nil || v != "ok" {
		t.Fatalf("second Get = (%q, %v): a transient error must not poison the key", v, err)
	}
	c.Get(context.Background(), "k")
	if l.calls.Load() != 2 {
		t.Fatalf("loads = %d, want 2 (error, success, then a hit)", l.calls.Load())
	}
}

func TestCache_ErrorTTLCachesFailuresBriefly(t *testing.T) {
	clk := tNewClock()
	boom := errors.New("vendor 503")
	l := &tLoader{fn: func(ctx context.Context, key string, n int64) (string, error) { return "", boom }}
	c := New(Config{TTL: time.Minute, ErrorTTL: 2 * time.Second, Now: clk.Now}, l.Load)
	for i := 0; i < 5; i++ {
		if _, err := c.Get(context.Background(), "k"); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	}
	if l.calls.Load() != 1 || c.Stats().ErrorHits != 4 {
		t.Fatalf("loads=%d errorHits=%d, want 1 and 4: a failing dependency should be shielded from a retry storm for ErrorTTL", l.calls.Load(), c.Stats().ErrorHits)
	}
	clk.Advance(2 * time.Second)
	c.Get(context.Background(), "k")
	if l.calls.Load() != 2 {
		t.Fatalf("loads = %d after ErrorTTL, want 2", l.calls.Load())
	}
}

func TestCache_ContextErrorsAreNeverCached(t *testing.T) {
	l := &tLoader{fn: func(ctx context.Context, key string, n int64) (string, error) {
		if n == 1 {
			return "", fmt.Errorf("query: %w", context.DeadlineExceeded)
		}
		return "ok", nil
	}}
	c := New(Config{TTL: time.Minute, ErrorTTL: time.Minute, NegativeTTL: time.Minute}, l.Load)
	if _, err := c.Get(context.Background(), "k"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if v, err := c.Get(context.Background(), "k"); err != nil || v != "ok" {
		t.Fatalf("a timeout says nothing about the key and must not be cached, even with ErrorTTL set; got (%q, %v)", v, err)
	}
}

func TestCache_JitterShortensTTL(t *testing.T) {
	clk := tNewClock()
	rs := []float64{0.5, 0, 1.0 - 1e-9}
	var i int
	var mu sync.Mutex
	l := &tLoader{}
	c := New(Config{
		TTL: 100 * time.Second, Jitter: 0.2, Now: clk.Now,
		Rand: func() float64 { mu.Lock(); defer mu.Unlock(); r := rs[i%len(rs)]; i++; return r },
	}, l.Load)
	ctx := context.Background()
	c.Get(ctx, "a") // r=0.5 => 100s * (1-0.1) = 90s
	c.Get(ctx, "b") // r=0   => 100s
	c.Get(ctx, "c") // r≈1   => 80s
	clk.Advance(79 * time.Second)
	c.Get(ctx, "a")
	c.Get(ctx, "b")
	c.Get(ctx, "c")
	if l.calls.Load() != 3 {
		t.Fatalf("loads = %d at +79s, want 3 (nothing expired yet)", l.calls.Load())
	}
	clk.Advance(2 * time.Second) // +81s: c (80s) expired
	c.Get(ctx, "a")
	c.Get(ctx, "b")
	c.Get(ctx, "c")
	if l.calls.Load() != 4 {
		t.Fatalf("loads = %d at +81s, want 4 (only the 80s entry expired); jitter must scale TTL by (1 - Jitter*Rand())", l.calls.Load())
	}
	clk.Advance(10 * time.Second) // +91s: a (90s) expired, b (100s) not
	c.Get(ctx, "b")
	if l.calls.Load() != 4 {
		t.Fatalf("b (jitter factor 1.0) expired early")
	}
	c.Get(ctx, "a")
	if l.calls.Load() != 5 {
		t.Fatalf("a (90s) should have expired by +91s")
	}
}

func TestCache_InvalidateForcesReload(t *testing.T) {
	l := &tLoader{}
	c := New(Config{TTL: time.Hour}, l.Load)
	v1, _ := c.Get(context.Background(), "k")
	c.Invalidate("k")
	v2, _ := c.Get(context.Background(), "k")
	if v1 == v2 || l.calls.Load() != 2 {
		t.Fatalf("after Invalidate: %q then %q with %d loads, want a reload", v1, v2, l.calls.Load())
	}
	c.Invalidate("never-cached") // must be a no-op
}

// The cache-aside stale-set race: a reader loads the old value, the writer
// updates the source and invalidates, then the reader's slow load finishes
// and stores the OLD value over the invalidation.
func TestCache_InvalidateDuringLoadPreventsStaleStore(t *testing.T) {
	source := "old"
	var smu sync.Mutex
	read := make(chan struct{}, 4) // loader signals "I have read the source"
	proceed := make(chan struct{}) // loader may return
	l := &tLoader{fn: func(ctx context.Context, key string, n int64) (string, error) {
		smu.Lock()
		v := source
		smu.Unlock()
		if n == 1 {
			read <- struct{}{}
			<-proceed // slow network between reading the DB and returning
		}
		return v, nil
	}}
	c := New(Config{TTL: time.Hour}, l.Load)

	got := make(chan string, 1)
	go func() {
		v, err := c.Get(context.Background(), "k")
		if err != nil {
			t.Errorf("in-flight reader: %v", err)
		}
		got <- v
	}()
	select {
	case <-read:
	case <-tTimer(t):
		t.Fatalf("loader never started")
	}
	// Writer: update the source of truth, then invalidate.
	smu.Lock()
	source = "new"
	smu.Unlock()
	c.Invalidate("k")
	close(proceed)

	select {
	case v := <-got:
		if v != "old" {
			t.Fatalf("the in-flight reader got %q; it read before the write, so %q is what it was promised", v, "old")
		}
	case <-tTimer(t):
		t.Fatalf("reader never returned")
	}
	v, err := c.Get(context.Background(), "k")
	if err != nil || v != "new" {
		t.Fatalf("Get after the race = (%q, %v), want %q: the slow load stored its stale result over the invalidation, so readers see old data until the TTL (1h) expires", v, err, "new")
	}
	if l.calls.Load() != 2 {
		t.Fatalf("loads = %d, want 2", l.calls.Load())
	}
}

func TestCache_WaiterCancellationDoesNotKillTheSharedLoad(t *testing.T) {
	l := &tLoader{gate: make(chan struct{})}
	c := New(Config{TTL: time.Minute}, l.Load)
	impatient, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := c.Get(impatient, "k")
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("impatient Get: %v", err)
		}
		errCh <- err
	}()
	tWaitInFlight(t, c, "k", 1)
	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, err := c.Get(context.Background(), "k"); err != nil || v == "" {
				t.Errorf("patient waiter got (%q, %v)", v, err)
			}
		}()
	}
	tWaitInFlight(t, c, "k", 3)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-tTimer(t):
		t.Fatalf("cancelled Get did not return")
	}
	close(l.gate)
	tWait(t, &wg, "patient waiters")
	if v, err := c.Get(context.Background(), "k"); err != nil || v == "" || l.calls.Load() != 1 {
		t.Fatalf("after the load: (%q, %v), %d loader calls; the load must complete and be cached", v, err, l.calls.Load())
	}
}

func TestCache_AllWaitersCancelStoresNothing(t *testing.T) {
	l := &tLoader{gate: make(chan struct{})}
	c := New(Config{TTL: time.Minute, ErrorTTL: time.Minute}, l.Load)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := c.Get(ctx, "k")
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Get: %v", err)
		}
		errCh <- err
	}()
	tWaitInFlight(t, c, "k", 1)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-tTimer(t):
		t.Fatalf("cancelled Get did not return")
	}
	tEventually(t, "the abandoned load to finish", func() bool { return c.InFlight("k") == 0 })
	close(l.gate)
	v, err := c.Get(context.Background(), "k")
	if err != nil || v == "" {
		t.Fatalf("Get after an abandoned load = (%q, %v): a cancelled load must not leave an error or value behind", v, err)
	}
	if l.calls.Load() != 2 {
		t.Fatalf("loads = %d, want 2", l.calls.Load())
	}
}

func TestCache_LoaderPanicReleasesWaitersAndIsNotCached(t *testing.T) {
	release := make(chan struct{})
	l := &tLoader{fn: func(ctx context.Context, key string, n int64) (string, error) {
		if n == 1 {
			<-release
			panic("nil map in vendor client")
		}
		return "ok", nil
	}}
	c := New(Config{TTL: time.Minute, ErrorTTL: time.Minute}, l.Load)
	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Get(context.Background(), "k")
			var pe *PanicError
			if !errors.As(err, &pe) {
				t.Errorf("err = %v, want *PanicError", err)
			}
		}()
	}
	tWaitInFlight(t, c, "k", 5)
	close(release)
	tWait(t, &wg, "waiters of a panicking loader")
	if v, err := c.Get(context.Background(), "k"); err != nil || v != "ok" {
		t.Fatalf("after a panic: (%q, %v); a panic is a bug to surface, not a state to cache", v, err)
	}
}

func TestCache_StaleWhileRevalidate(t *testing.T) {
	clk := tNewClock()
	l := &tLoader{}
	var gate atomic.Pointer[chan struct{}]
	l.fn = func(ctx context.Context, key string, n int64) (string, error) {
		if g := gate.Load(); g != nil && n > 1 {
			select {
			case <-*g:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return fmt.Sprintf("v%d", n), nil
	}
	c := New(Config{TTL: 10 * time.Second, StaleTTL: 20 * time.Second, Now: clk.Now}, l.Load)
	ctx := context.Background()
	if v, _ := c.Get(ctx, "rates"); v != "v1" {
		t.Fatalf("first Get = %q, want v1", v)
	}
	g := make(chan struct{})
	gate.Store(&g) // the refresh will block until we say so
	clk.Advance(15 * time.Second)

	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.Get(ctx, "rates")
			if err != nil || v != "v1" {
				t.Errorf("stale Get = (%q, %v), want (v1, nil) immediately", v, err)
			}
		}()
	}
	tWait(t, &wg, "stale Gets while the refresh is blocked (they must not wait for the loader)")
	tEventually(t, "the single background refresh to start", func() bool { return l.calls.Load() == 2 })
	if n := l.calls.Load(); n != 2 {
		t.Fatalf("loader calls = %d, want 2: 20 stale readers must trigger exactly one refresh", n)
	}
	if s := c.Stats(); s.StaleServed != 20 || s.Refreshes != 1 {
		t.Fatalf("stats %+v, want StaleServed 20 and Refreshes 1", s)
	}
	close(g)
	tEventually(t, "the refreshed value to be served", func() bool {
		v, _ := c.Get(ctx, "rates")
		return v == "v2"
	})
	if l.calls.Load() != 2 {
		t.Fatalf("loader calls = %d, want 2 (initial + one refresh)", l.calls.Load())
	}
	// Past TTL + StaleTTL the stale value is no longer acceptable: block on a load.
	gate.Store(nil)
	clk.Advance(31 * time.Second)
	if v, _ := c.Get(ctx, "rates"); v != "v3" {
		t.Fatalf("beyond the stale window Get = %q, want a blocking reload (v3)", v)
	}
}

func TestCache_StaleRefreshFailureKeepsServingStaleAndRetries(t *testing.T) {
	clk := tNewClock()
	boom := errors.New("core banking 503")
	var fail atomic.Bool
	l := &tLoader{fn: func(ctx context.Context, key string, n int64) (string, error) {
		if n > 1 && fail.Load() {
			return "", boom
		}
		return fmt.Sprintf("v%d", n), nil
	}}
	c := New(Config{TTL: 10 * time.Second, StaleTTL: time.Minute, ErrorTTL: time.Minute, Now: clk.Now}, l.Load)
	ctx := context.Background()
	c.Get(ctx, "k")
	fail.Store(true)
	clk.Advance(11 * time.Second)
	if v, err := c.Get(ctx, "k"); err != nil || v != "v1" {
		t.Fatalf("stale Get = (%q, %v)", v, err)
	}
	tEventually(t, "first failed refresh", func() bool { return l.calls.Load() == 2 })
	// The failure must neither replace the stale value nor wedge refreshing.
	tEventually(t, "second refresh attempt after the first failed", func() bool {
		v, err := c.Get(ctx, "k")
		if err != nil || v != "v1" {
			t.Fatalf("Get during failing refreshes = (%q, %v), want the stale v1", v, err)
		}
		return l.calls.Load() >= 3
	})
	fail.Store(false)
	tEventually(t, "a successful refresh", func() bool {
		v, _ := c.Get(ctx, "k")
		return v != "v1"
	})
}

func TestCache_StaleRefreshNotFoundReplacesValue(t *testing.T) {
	clk := tNewClock()
	var gone atomic.Bool
	l := &tLoader{fn: func(ctx context.Context, key string, n int64) (string, error) {
		if gone.Load() {
			return "", ErrNotFound
		}
		return "v", nil
	}}
	c := New(Config{TTL: 10 * time.Second, StaleTTL: time.Minute, NegativeTTL: 30 * time.Second, Now: clk.Now}, l.Load)
	ctx := context.Background()
	if _, err := c.Get(ctx, "k"); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	gone.Store(true)
	clk.Advance(11 * time.Second)
	c.Get(ctx, "k") // stale, triggers refresh
	tEventually(t, "the value to be replaced by a negative entry", func() bool {
		_, err := c.Get(ctx, "k")
		return errors.Is(err, ErrNotFound)
	})
}

func TestCache_ConcurrentMixedWorkloadKeepsStatsConsistent(t *testing.T) {
	clk := tNewClock()
	l := &tLoader{fn: func(ctx context.Context, key string, n int64) (string, error) {
		if key == "missing" {
			return "", ErrNotFound
		}
		return key, nil
	}}
	c := New(Config{
		TTL: 3 * time.Second, StaleTTL: 2 * time.Second, NegativeTTL: time.Second,
		Jitter: 0.3, Rand: rand.New(rand.NewSource(1)).Float64, Now: clk.Now,
	}, l.Load)
	// rand.Rand is not goroutine-safe; Cache must serialise calls to cfg.Rand.
	const workers, ops = 32, 400
	var total atomic.Int64
	var wg sync.WaitGroup
	tCleanupWait(t, &wg)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < ops; i++ {
				key := []string{"a", "b", "c", "missing"}[rng.Intn(4)]
				switch rng.Intn(20) {
				case 0:
					c.Invalidate(key)
				case 1:
					clk.Advance(700 * time.Millisecond)
				default:
					total.Add(1)
					v, err := c.Get(context.Background(), key)
					if key == "missing" {
						if !errors.Is(err, ErrNotFound) {
							t.Errorf("missing: err=%v", err)
							return
						}
					} else if err != nil || v != key {
						t.Errorf("%s: (%q, %v)", key, v, err)
						return
					}
				}
			}
		}(w)
	}
	tWait(t, &wg, "mixed workload")
	// Background refreshes may still be running; let them settle.
	time.Sleep(20 * time.Millisecond)
	s := c.Stats()
	sum := int64(s.Hits + s.Misses + s.NegativeHits + s.ErrorHits + s.StaleServed)
	if sum != total.Load() {
		t.Fatalf("hits+misses+negative+error+stale = %d but %d Gets were issued (stats %+v): every Get must be counted exactly once", sum, total.Load(), s)
	}
}
