package ratelimit

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"runtime/pprof"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// ---- shared helpers ---------------------------------------------------------

// fakeClock is a manually driven clock and timer source, safe for concurrent
// use. Times it returns carry no monotonic reading, so Set can move it
// backwards to simulate a wall-clock step.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeTimer // active: neither fired nor stopped
	created int
}

type fakeTimer struct {
	at time.Time
	d  time.Duration
	c  chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	f.fireDue()
}

// Set jumps the clock to t, forwards or backwards.
func (f *fakeClock) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
	f.fireDue()
}

func (f *fakeClock) fireDue() {
	keep := f.timers[:0]
	for _, t := range f.timers {
		if t.at.After(f.now) {
			keep = append(keep, t)
			continue
		}
		t.c <- f.now // buffered(1) and fired at most once: never blocks
	}
	f.timers = keep
}

// NewTimer matches NewTimerFunc.
func (f *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{at: f.now.Add(d), d: d, c: make(chan time.Time, 1)}
	f.created++
	if d <= 0 {
		t.c <- f.now
		return t.c, func() bool { return false }
	}
	f.timers = append(f.timers, t)
	stop := func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, x := range f.timers {
			if x == t {
				f.timers = append(f.timers[:i], f.timers[i+1:]...)
				return true
			}
		}
		return false
	}
	return t.c, stop
}

func (f *fakeClock) active() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

func (f *fakeClock) timersCreated() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created
}

// activeDurations returns the requested durations of the active timers.
func (f *fakeClock) activeDurations() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ds []time.Duration
	for _, t := range f.timers {
		ds = append(ds, t.d)
	}
	return ds
}

// waitForTimers blocks until at least n timers are active.
func (f *fakeClock) waitForTimers(t *testing.T, n int, why string) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for f.active() < n {
		if time.Now().After(deadline) {
			t.Fatalf("expected %d active timer(s) but there are %d after %v: %s", n, f.active(), testTimeout, why)
		}
		time.Sleep(time.Millisecond)
	}
}

// newLimiter builds a token-bucket limiter on a fresh fake clock.
func newLimiter(t *testing.T, rate Rate, burst int, opts ...Option) (*Limiter, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	l, err := New(rate, burst, append([]Option{WithClock(clk)}, opts...)...)
	if err != nil {
		t.Fatalf("New(%+v, %d) returned error %v", rate, burst, err)
	}
	if l == nil {
		t.Fatal("New returned a nil *Limiter with a nil error")
	}
	return l, clk
}

// within fails the test if f does not return within testTimeout, so a deadlock
// or an accidental O(n^2) fails instead of hanging the run.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (deadlock, or a per-operation cost that grows with the number of keys)", what, testTimeout)
	}
}

func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		n := runtime.NumGoroutine()
		if n <= baseline {
			return
		}
		if time.Now().After(deadline) {
			var buf bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
			t.Fatalf("goroutine leak: %d goroutines running, baseline was %d. Live stacks:\n%s", n, baseline, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// drain takes single permits until the key is denied and returns how many it got.
func drain(l *Limiter, key string) int {
	n := 0
	for l.Allow(key) {
		n++
		if n > 1_000_000 {
			return n
		}
	}
	return n
}

// ---- stage 1: per-key token bucket -----------------------------------------

func TestStage1_NewRejectsInvalidConfig(t *testing.T) {
	cases := []struct {
		name  string
		rate  Rate
		burst int
	}{
		{"zero events", Rate{0, time.Second}, 1},
		{"negative events", Rate{-1, time.Second}, 1},
		{"zero period", Rate{10, 0}, 1},
		{"negative period", Rate{10, -time.Second}, 1},
		{"zero burst", Rate{10, time.Second}, 0},
		{"negative burst", Rate{10, time.Second}, -5},
		{"burst*period overflows int64", Rate{1, 365 * 24 * time.Hour}, 1 << 30},
	}
	for _, c := range cases {
		l, err := New(c.rate, c.burst)
		if err == nil || !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: New(%+v, %d) error = %v, want ErrInvalidConfig (a limiter that cannot do the arithmetic must refuse to exist)", c.name, c.rate, c.burst, err)
		}
		if l != nil {
			t.Errorf("%s: New returned a non-nil limiter alongside an error", c.name)
		}
	}
	// A big but representable configuration is fine.
	if _, err := New(Rate{1 << 20, 24 * time.Hour}, 1000); err != nil {
		t.Errorf("New(1<<20 per day, burst 1000) = %v, want nil", err)
	}
}

func TestStage1_BucketStartsFullAndBurstIsAHardCeiling(t *testing.T) {
	l, clk := newLimiter(t, Rate{1, time.Second}, 5)
	if got := drain(l, "k"); got != 5 {
		t.Fatalf("a fresh key allowed %d requests, want exactly burst = 5", got)
	}
	clk.Advance(24 * time.Hour)
	if got := drain(l, "k"); got != 5 {
		t.Fatalf("after a day idle the key allowed %d, want burst = 5: idle time must not be banked beyond the bucket size", got)
	}
}

func TestStage1_RefillIsExactToTheNanosecond(t *testing.T) {
	// 3 events per second: one token every 333,333,333.33.. ns. A permit is
	// due at ceil(k * 1e9 / 3) ns after the drain and not one nanosecond sooner.
	// (Burst 3 so the bucket never sits at its cap while we wait: at the cap, leftover fractions are
	// legitimately discarded.)
	l, clk := newLimiter(t, Rate{3, time.Second}, 3)
	if got := drain(l, "k"); got != 3 {
		t.Fatalf("drained %d from a fresh bucket of 3", got)
	}
	var elapsed time.Duration
	for _, due := range []time.Duration{333_333_334, 666_666_667, 1_000_000_000} {
		clk.Advance(due - 1 - elapsed)
		elapsed = due - 1
		if l.Allow("k") {
			t.Fatalf("allowed at +%dns, one nanosecond before the permit is due at +%dns", elapsed, due)
		}
		clk.Advance(1)
		elapsed = due
		if !l.Allow("k") {
			t.Fatalf("denied at +%dns, when the permit is due: refill lost a fraction of a token", elapsed)
		}
		if l.Allow("k") {
			t.Fatalf("a second permit appeared at +%dns: refill minted too much", elapsed)
		}
	}
}

func TestStage1_NoFloatingPointDrift(t *testing.T) {
	// 10 events/s with burst 1, probed every 10ms for 100s must admit exactly
	// 1000 requests. Accumulating tokens as float64 (tokens += 0.01*10 ...)
	// lands on 0.9999999999999999 where it should be 1.0, misses a permit at
	// every boundary, and admits 999.
	l, clk := newLimiter(t, Rate{10, time.Second}, 1)
	l.Allow("k") // drain
	allowed := 0
	for i := 0; i < 10_000; i++ {
		clk.Advance(10 * time.Millisecond)
		if l.Allow("k") {
			allowed++
		}
	}
	if allowed != 1000 {
		t.Fatalf("admitted %d requests in 100s at 10/s, want exactly 1000 (token accounting drifted)", allowed)
	}
}

func TestStage1_HugeClockJumpDoesNotOverflow(t *testing.T) {
	// elapsed(ns) * events overflows int64 for a jump this big at 1000/s.
	l, clk := newLimiter(t, Rate{1000, time.Second}, 10)
	drain(l, "k")
	clk.Advance(250 * 365 * 24 * time.Hour)
	if got := drain(l, "k"); got != 10 {
		t.Fatalf("after a 250-year jump the key allowed %d, want exactly burst = 10 (integer overflow in the refill?)", got)
	}
}

func TestStage1_ClockGoingBackwardsMintsNothing(t *testing.T) {
	l, clk := newLimiter(t, Rate{1, time.Second}, 2)
	clk.Advance(5 * time.Minute)
	T := clk.Now()
	if got := drain(l, "k"); got != 2 {
		t.Fatalf("drained %d, want 2", got)
	}
	clk.Set(T.Add(-time.Hour)) // wall clock stepped back an hour
	if l.Allow("k") {
		t.Fatal("allowed while the clock was an hour in the past: a negative elapsed time minted tokens")
	}
	clk.Set(T) // and forward again to where we were
	if l.Allow("k") {
		t.Fatal("allowed after the clock returned to its old value: the rewind and the catch-up were both counted")
	}
	clk.Set(T.Add(500 * time.Millisecond))
	if l.Allow("k") {
		t.Fatal("allowed 500ms after the drain at 1/s")
	}
	clk.Set(T.Add(time.Second))
	if !l.Allow("k") {
		t.Fatal("denied one second after the drain: refill must resume once the clock passes its old high-water mark")
	}
	if l.Allow("k") {
		t.Fatal("second permit after 1s at 1/s")
	}

	// A clock that starts out before the limiter was built (a different
	// machine's wall clock, a restored VM) is harmless too.
	l2, clk2 := newLimiter(t, Rate{1, time.Second}, 3)
	clk2.Set(clk2.Now().Add(-48 * time.Hour))
	if got := drain(l2, "k"); got != 3 {
		t.Fatalf("allowed %d with the clock before construction time, want burst = 3", got)
	}
}

func TestStage1_AllowNIsAllOrNothing(t *testing.T) {
	l, _ := newLimiter(t, Rate{1, time.Second}, 5)
	if !l.AllowN("k", 3) {
		t.Fatal("AllowN(3) denied on a full bucket of 5")
	}
	if l.AllowN("k", 3) {
		t.Fatal("AllowN(3) allowed with only 2 tokens left")
	}
	if !l.AllowN("k", 2) {
		t.Fatal("AllowN(2) denied: the failed AllowN(3) must not have consumed the 2 remaining tokens")
	}
	if l.Allow("k") {
		t.Fatal("bucket should be empty")
	}
}

func TestStage1_InvalidAndHopelessNAreRejectedWithoutState(t *testing.T) {
	l, _ := newLimiter(t, Rate{1, time.Second}, 5)
	for i := 0; i < 1000; i++ {
		key := "flood-" + strconv.Itoa(i)
		for _, n := range []int{0, -1, -100, 6, 1 << 40} {
			if l.AllowN(key, n) {
				t.Fatalf("AllowN(%q, %d) = true, want false", key, n)
			}
		}
	}
	if l.Len() != 0 {
		t.Fatalf("Len = %d after 5000 rejected requests, want 0: requests that can never succeed must not allocate per-key state", l.Len())
	}
	// A negative n must not mint tokens.
	drain(l, "k")
	if l.AllowN("k", -5) {
		t.Fatal("AllowN(-5) = true")
	}
	if l.Allow("k") {
		t.Fatal("a negative AllowN refilled the bucket")
	}
}

func TestStage1_KeysAreIndependent(t *testing.T) {
	l, _ := newLimiter(t, Rate{1, time.Second}, 2)
	drain(l, "a")
	if l.Allow("a") {
		t.Fatal("a should be exhausted")
	}
	if !l.Allow("b") || !l.Allow("b") {
		t.Fatal("b was affected by a's traffic: buckets must be per key")
	}
	if l.Len() != 2 {
		t.Fatalf("Len = %d, want 2", l.Len())
	}
}

func TestStage1_ConcurrentCallersOnOneKeyAdmitExactlyBurst(t *testing.T) {
	const burst, workers, perWorker = 100, 64, 10
	l, _ := newLimiter(t, Rate{1, time.Hour}, burst) // clock frozen: no refill
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if l.Allow("hot") {
					allowed.Add(1)
				}
			}
		}()
	}
	within(t, "concurrent Allow", wg.Wait)
	if got := allowed.Load(); got != burst {
		t.Fatalf("%d requests admitted for one key under contention, want exactly %d (check-then-act race: the limit is a promise to a vendor)", got, burst)
	}
}

func TestStage1_ConcurrentAllowNNeitherLosesNorMintsPermits(t *testing.T) {
	const burst = 1000
	l, _ := newLimiter(t, Rate{1, time.Hour}, burst)
	var granted atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				n := 1 + (w+i)%7
				if l.AllowN("k", n) {
					granted.Add(int64(n))
				}
			}
		}(w)
	}
	within(t, "concurrent AllowN", wg.Wait)
	rest := int64(drain(l, "k"))
	if got := granted.Load() + rest; got != burst {
		t.Fatalf("granted %d permits in total (%d by AllowN + %d afterwards), want exactly the burst %d", got, granted.Load(), rest, burst)
	}
}

func TestStage1_ConcurrentTrafficWhileTheClockAdvancesRespectsTheRate(t *testing.T) {
	const keys, workers, perWorker, steps = 4, 16, 20_000, 200
	const burst = 10 // rate 1000/s, clock advances 200 x 1ms = 200ms => at most 10 + 200 per key
	l, clk := newLimiter(t, Rate{1000, time.Second}, burst)
	var allowed [keys]atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				k := (w + i) % keys
				if l.Allow("key-" + strconv.Itoa(k)) {
					allowed[k].Add(1)
				}
			}
		}(w)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < steps; i++ {
			clk.Advance(time.Millisecond)
			runtime.Gosched()
		}
	}()
	within(t, "traffic with a moving clock", wg.Wait)
	for k := 0; k < keys; k++ {
		if got := allowed[k].Load(); got > burst+steps {
			t.Fatalf("key %d admitted %d requests, more than burst %d + 200ms at 1000/s = %d", k, got, burst, burst+steps)
		}
	}
}

func TestStage1_NoBackgroundGoroutines(t *testing.T) {
	// There is no Close in stage 1's API. A refill ticker per limiter (or per
	// key) would leak with every limiter that goes out of scope: refill lazily.
	baseline := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		l, clk := newLimiter(t, Rate{10, time.Second}, 3)
		for k := 0; k < 20; k++ {
			l.Allow("k" + strconv.Itoa(k))
		}
		clk.Advance(time.Second)
	}
	waitForGoroutines(t, baseline)
}

func TestStage1_OperationsDoNotScaleWithTheNumberOfKeys(t *testing.T) {
	// A linear scan of the key set per call does ~1e10 steps here and misses
	// the time cap by a wide margin; a map lookup finishes in milliseconds.
	const keys, ops = 50_000, 200_000
	l, _ := newLimiter(t, Rate{1, time.Second}, 5) // frozen clock: no key becomes idle
	names := make([]string, keys)
	for i := range names {
		names[i] = fmt.Sprintf("member-%d", i)
	}
	within(t, fmt.Sprintf("%d Allow calls over %d keys", ops, keys), func() {
		for i := 0; i < ops; i++ {
			l.Allow(names[i%keys])
		}
	})
	if l.Len() != keys {
		t.Fatalf("Len = %d, want %d", l.Len(), keys)
	}
}
