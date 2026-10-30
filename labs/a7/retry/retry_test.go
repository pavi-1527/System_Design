package retry

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

var (
	errBoom = errors.New("boom")
	errTemp = errors.New("temporary")
	errFatl = errors.New("fatal")
)

// tEnv is a fake clock + fake sleep + scripted randomness. Sleep never waits:
// it records the request and moves the fake clock forward, so a test that
// "waits" 30 s of backoff takes microseconds.
type tEnv struct {
	mu     sync.Mutex
	base   time.Time
	now    time.Time
	sleeps []time.Duration
	rnd    float64
	script []float64 // if non-empty, Rand pops from here first
}

func tNewEnv() *tEnv {
	// Based on the real time so that contexts built with real deadlines
	// (base + 5 s) are in the future for the few milliseconds a test runs.
	b := time.Now()
	return &tEnv{base: b, now: b, rnd: 0.5}
}

func (e *tEnv) Now() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *tEnv) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sleeps = append(e.sleeps, d)
	e.now = e.now.Add(d)
	return nil
}

func (e *tEnv) Rand() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.script) > 0 {
		v := e.script[0]
		e.script = e.script[1:]
		return v
	}
	return e.rnd
}

func (e *tEnv) Sleeps() []time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]time.Duration(nil), e.sleeps...)
}

func (e *tEnv) Elapsed() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now.Sub(e.base)
}

// cfg wires the fakes into c.
func (e *tEnv) cfg(c Config) Config {
	c.Now, c.Sleep, c.Rand = e.Now, e.Sleep, e.Rand
	return c
}

func tDeadline(t *testing.T, e *tEnv, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithDeadline(context.Background(), e.base.Add(d))
}

// tFailing returns an op that fails n times with err and then succeeds,
// together with its call counter. n < 0 means "always fail".
func tFailing(n int, err error) (func(context.Context) error, *atomic.Int64) {
	var calls atomic.Int64
	return func(context.Context) error {
		c := calls.Add(1)
		if n < 0 || int(c) <= n {
			return err
		}
		return nil
	}, &calls
}

func tDurs(ms ...int) []time.Duration {
	out := make([]time.Duration, len(ms))
	for i, m := range ms {
		out[i] = time.Duration(m) * time.Millisecond
	}
	return out
}

func tEqualDurs(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func tNear(a, b, tol time.Duration) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// ---------- Delay: the backoff arithmetic ----------

func TestDelay_NoJitterDoublesUntilTheCap(t *testing.T) {
	want := tDurs(100, 200, 400, 800, 1000, 1000)
	for attempt, w := range want {
		if got := Delay(NoJitter, 100*time.Millisecond, time.Second, attempt, 0, 0.7); got != w {
			t.Errorf("attempt %d: got %v, want %v (base × 2^attempt, capped at max)", attempt, got, w)
		}
	}
}

func TestDelay_FullJitterIsUniformBetweenZeroAndTheCap(t *testing.T) {
	base, max := 100*time.Millisecond, 10*time.Second
	// attempt 3 => cap 800 ms
	cases := []struct {
		rnd  float64
		want time.Duration
	}{{0, 0}, {0.25, 200 * time.Millisecond}, {0.5, 400 * time.Millisecond}, {0.75, 600 * time.Millisecond}}
	for _, c := range cases {
		if got := Delay(FullJitter, base, max, 3, 0, c.rnd); got != c.want {
			t.Errorf("rnd=%v: got %v, want %v (full jitter = rnd × cap; zero is allowed)", c.rnd, got, c.want)
		}
	}
	if got := Delay(FullJitter, base, max, 3, 0, 0.999999); got >= 800*time.Millisecond {
		t.Errorf("rnd just below 1 gave %v; full jitter must stay below the cap", got)
	}
}

func TestDelay_EqualJitterNeverDropsBelowHalfTheCap(t *testing.T) {
	base, max := 100*time.Millisecond, 10*time.Second
	for _, c := range []struct {
		rnd  float64
		want time.Duration
	}{{0, 400 * time.Millisecond}, {0.5, 600 * time.Millisecond}, {0.25, 500 * time.Millisecond}} {
		if got := Delay(EqualJitter, base, max, 3, 0, c.rnd); got != c.want {
			t.Errorf("rnd=%v: got %v, want %v (equal jitter = cap/2 + rnd × cap/2)", c.rnd, got, c.want)
		}
	}
}

func TestDelay_DecorrelatedJitterGrowsFromThePreviousDelay(t *testing.T) {
	base, max := 100*time.Millisecond, 10*time.Second
	cases := []struct {
		prev time.Duration
		rnd  float64
		want time.Duration
	}{
		{100 * time.Millisecond, 0, 100 * time.Millisecond},   // base + 0
		{100 * time.Millisecond, 0.5, 200 * time.Millisecond}, // 100 + 0.5 × (300 − 100)
		{time.Second, 0.5, 1550 * time.Millisecond},           // 100 + 0.5 × (3000 − 100)
		{5 * time.Second, 1, 10 * time.Second},                // capped
		{10 * time.Millisecond, 0.5, 200 * time.Millisecond},  // prev below base is treated as base
	}
	for _, c := range cases {
		if got := Delay(DecorrelatedJitter, base, max, 0, c.prev, c.rnd); got != c.want {
			t.Errorf("prev=%v rnd=%v: got %v, want %v (min(max, base + rnd × (3×prev − base)))", c.prev, c.rnd, got, c.want)
		}
	}
}

func TestDelay_HugeAttemptNumbersDoNotOverflow(t *testing.T) {
	for _, j := range []Jitter{NoJitter, FullJitter, EqualJitter, DecorrelatedJitter} {
		got := Delay(j, time.Millisecond, 30*time.Second, 1000, 20*time.Second, 0.5)
		if got < 0 || got > 30*time.Second {
			t.Errorf("%v: attempt 1000 gave %v; must stay within [0, max] (a shift/multiply overflowed?)", j, got)
		}
	}
	got := Delay(NoJitter, time.Second, time.Duration(math.MaxInt64), 200, 0, 0)
	if got <= 0 {
		t.Errorf("max = MaxInt64: got %v, want a positive capped value (overflow while doubling?)", got)
	}
}

func TestDelay_DegenerateInputs(t *testing.T) {
	if got := Delay(FullJitter, 0, time.Second, 3, 0, 0.5); got != 0 {
		t.Errorf("base 0: got %v, want 0", got)
	}
	if got := Delay(NoJitter, time.Second, time.Millisecond, 5, 0, 0); got != time.Second {
		t.Errorf("max < base: got %v, want base (1s)", got)
	}
}

func TestJitter_SpreadsASynchronisedHerd(t *testing.T) {
	// 1,000 clients fail at t=0 and retry 3 times with base 100 ms. Count
	// the worst 10 ms window of retry arrivals with and without jitter.
	worst := func(j Jitter) int {
		r := rand.New(rand.NewPCG(7, 11))
		buckets := map[int64]int{}
		for c := 0; c < 1000; c++ {
			at := time.Duration(0)
			prev := 100 * time.Millisecond
			for attempt := 0; attempt < 3; attempt++ {
				d := Delay(j, 100*time.Millisecond, 5*time.Second, attempt, prev, r.Float64())
				prev = d
				at += d
				buckets[int64(at/(10*time.Millisecond))]++
			}
		}
		w := 0
		for _, n := range buckets {
			w = max(w, n)
		}
		return w
	}
	none, full := worst(NoJitter), worst(FullJitter)
	if none != 1000 {
		t.Errorf("NoJitter worst window = %d, want 1000 (every client retries at the same instants)", none)
	}
	if full >= 300 {
		t.Errorf("FullJitter worst window = %d, want < 300 (jitter must spread a herd of 1000 across many windows)", full)
	}
}

// ---------- Do: attempts, classification, idempotency gate ----------

func TestDo_SuccessOnTheFirstAttemptNeverSleeps(t *testing.T) {
	e := tNewEnv()
	got, err := Do(context.Background(), e.cfg(Config{}), func(context.Context) (string, error) { return "ok", nil })
	if err != nil || got != "ok" {
		t.Fatalf("got (%q, %v), want (\"ok\", nil)", got, err)
	}
	if n := len(e.Sleeps()); n != 0 {
		t.Errorf("slept %d times on a first-try success; a healthy call must add no latency", n)
	}
}

func TestDo_RetriesTransientFailuresWithExponentialBackoff(t *testing.T) {
	e := tNewEnv()
	op, calls := tFailing(2, errBoom)
	cfg := e.cfg(Config{MaxAttempts: 5, BaseDelay: 100 * time.Millisecond, Jitter: NoJitter, Idempotent: true})
	if err := DoErr(context.Background(), cfg, op); err != nil {
		t.Fatalf("err = %v, want success on the third attempt", err)
	}
	if calls.Load() != 3 {
		t.Errorf("op called %d times, want 3", calls.Load())
	}
	if got, want := e.Sleeps(), tDurs(100, 200); !tEqualDurs(got, want) {
		t.Errorf("sleeps = %v, want %v (base × 2^n with no jitter)", got, want)
	}
}

func TestDo_ReturnsTheValueOfTheSuccessfulAttempt(t *testing.T) {
	e := tNewEnv()
	var n int
	got, err := Do(context.Background(), e.cfg(Config{Idempotent: true}), func(context.Context) (int, error) {
		n++
		if n < 3 {
			return -1, errBoom
		}
		return n * 10, nil
	})
	if err != nil || got != 30 {
		t.Fatalf("got (%d, %v), want (30, nil): the value must come from the attempt that succeeded", got, err)
	}
}

func TestDo_GivesUpAfterMaxAttemptsAndKeepsTheLastError(t *testing.T) {
	e := tNewEnv()
	op, calls := tFailing(-1, errBoom)
	err := DoErr(context.Background(), e.cfg(Config{MaxAttempts: 4, Idempotent: true}), op)
	if calls.Load() != 4 {
		t.Errorf("op called %d times, want exactly MaxAttempts = 4 (the first call counts)", calls.Load())
	}
	if len(e.Sleeps()) != 3 {
		t.Errorf("slept %d times, want 3 (between attempts, never after the last)", len(e.Sleeps()))
	}
	if !errors.Is(err, ErrMaxAttempts) || !errors.Is(err, errBoom) {
		t.Fatalf("err = %v; want it to wrap both ErrMaxAttempts and the last operation error", err)
	}
	var g *GiveUpError
	if !errors.As(err, &g) || g.Attempts != 4 {
		t.Errorf("want *GiveUpError with Attempts = 4, got %#v", g)
	}
}

func TestDo_DefaultsAreSane(t *testing.T) {
	e := tNewEnv()
	op, calls := tFailing(-1, errBoom)
	_ = DoErr(context.Background(), e.cfg(Config{Idempotent: true}), op)
	if calls.Load() != 3 {
		t.Errorf("default MaxAttempts: %d calls, want 3", calls.Load())
	}
	// default full jitter, base 100 ms, rnd 0.5 => 50 ms, 100 ms
	if got, want := e.Sleeps(), tDurs(50, 100); !tEqualDurs(got, want) {
		t.Errorf("default sleeps = %v, want %v (FullJitter, BaseDelay 100ms)", got, want)
	}
}

func TestDo_PermanentErrorsAreNotRetried(t *testing.T) {
	e := tNewEnv()
	op, calls := tFailing(-1, Permanent(errBoom))
	err := DoErr(context.Background(), e.cfg(Config{Idempotent: true}), op)
	if calls.Load() != 1 {
		t.Errorf("op called %d times, want 1: a 400-class failure will never succeed, retrying is pure load", calls.Load())
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want it to be (or wrap) the original error", err)
	}
	var g *GiveUpError
	if errors.As(err, &g) {
		t.Errorf("a non-retryable error should be returned as-is, not wrapped in GiveUpError")
	}
	if Permanent(nil) != nil {
		t.Errorf("Permanent(nil) must be nil")
	}
}

func TestDo_ClassifierDecidesWhatIsRetryable(t *testing.T) {
	e := tNewEnv()
	var calls int
	op := func(context.Context) error {
		calls++
		if calls == 1 {
			return errTemp
		}
		return errFatl
	}
	cfg := e.cfg(Config{Idempotent: true, MaxAttempts: 5, IsRetryable: func(err error) bool { return errors.Is(err, errTemp) }})
	err := DoErr(context.Background(), cfg, op)
	if calls != 2 {
		t.Errorf("op called %d times, want 2 (retry the temporary error, stop at the fatal one)", calls)
	}
	if !errors.Is(err, errFatl) {
		t.Errorf("err = %v, want the fatal error", err)
	}
}

func TestDo_PermanentWinsOverAClassifierThatSaysRetry(t *testing.T) {
	e := tNewEnv()
	op, calls := tFailing(-1, Permanent(errBoom))
	cfg := e.cfg(Config{Idempotent: true, IsRetryable: func(error) bool { return true }})
	_ = DoErr(context.Background(), cfg, op)
	if calls.Load() != 1 {
		t.Errorf("op called %d times, want 1: Permanent must override IsRetryable", calls.Load())
	}
}

func TestDo_NonIdempotentCallWithoutAKeyIsNotRetried(t *testing.T) {
	e := tNewEnv()
	op, calls := tFailing(-1, errBoom)
	err := DoErr(context.Background(), e.cfg(Config{MaxAttempts: 5}), op) // Idempotent false, no key
	if calls.Load() != 1 {
		t.Errorf("op called %d times, want 1: a retried POST without a key can move money twice", calls.Load())
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want the original error", err)
	}
}

func TestDo_NonIdempotentCallIsRetriedWhenTheRequestNeverReachedTheServer(t *testing.T) {
	e := tNewEnv()
	op, calls := tFailing(2, NotSent(errBoom)) // e.g. connection refused: provably not processed
	if err := DoErr(context.Background(), e.cfg(Config{MaxAttempts: 5}), op); err != nil {
		t.Fatalf("err = %v, want success", err)
	}
	if calls.Load() != 3 {
		t.Errorf("op called %d times, want 3: NotSent failures are safe to retry even without a key", calls.Load())
	}
	if NotSent(nil) != nil {
		t.Errorf("NotSent(nil) must be nil")
	}
}

func TestDo_AnIdempotencyKeyMakesAPostRetryable(t *testing.T) {
	e := tNewEnv()
	op, calls := tFailing(2, errBoom)
	if err := DoErr(context.Background(), e.cfg(Config{MaxAttempts: 5, IdempotencyKey: "k-123"}), op); err != nil {
		t.Fatalf("err = %v, want success", err)
	}
	if calls.Load() != 3 {
		t.Errorf("op called %d times, want 3: with a key the server deduplicates, so retrying is safe", calls.Load())
	}
}

func TestDo_ATimeoutOnANonIdempotentCallIsAmbiguousAndNotRetried(t *testing.T) {
	e := tNewEnv()
	var calls atomic.Int64
	op := func(ctx context.Context) error {
		calls.Add(1)
		<-ctx.Done() // the server may or may not have processed it
		return ctx.Err()
	}
	err := DoErr(context.Background(), e.cfg(Config{MaxAttempts: 5, AttemptTimeout: 5 * time.Millisecond}), op)
	if calls.Load() != 1 {
		t.Errorf("op called %d times, want 1: after a read timeout we cannot know the transfer did not happen", calls.Load())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the attempt's DeadlineExceeded", err)
	}
}

func TestDo_AnAttemptTimeoutOnAnIdempotentCallIsRetried(t *testing.T) {
	e := tNewEnv()
	var calls atomic.Int64
	op := func(ctx context.Context) error {
		if calls.Add(1) <= 2 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	cfg := e.cfg(Config{MaxAttempts: 5, AttemptTimeout: 5 * time.Millisecond, Idempotent: true})
	if err := DoErr(context.Background(), cfg, op); err != nil {
		t.Fatalf("err = %v, want success on the third attempt", err)
	}
	if calls.Load() != 3 {
		t.Errorf("op called %d times, want 3", calls.Load())
	}
}

func TestDo_OnRetryReportsEachWait(t *testing.T) {
	e := tNewEnv()
	type rec struct {
		attempt int
		err     error
		delay   time.Duration
	}
	var got []rec
	cfg := e.cfg(Config{MaxAttempts: 3, Idempotent: true, Jitter: NoJitter, BaseDelay: 50 * time.Millisecond,
		OnRetry: func(a int, err error, d time.Duration) { got = append(got, rec{a, err, d}) }})
	op, _ := tFailing(-1, errBoom)
	_ = DoErr(context.Background(), cfg, op)
	if len(got) != 2 || got[0].attempt != 1 || got[1].attempt != 2 ||
		got[0].delay != 50*time.Millisecond || got[1].delay != 100*time.Millisecond || !errors.Is(got[0].err, errBoom) {
		t.Errorf("OnRetry calls = %+v, want (1, boom, 50ms) then (2, boom, 100ms)", got)
	}
}

func TestDo_DecorrelatedJitterFeedsThePreviousDelayForward(t *testing.T) {
	e := tNewEnv()
	e.script = []float64{0.5, 0.5, 0.5}
	cfg := e.cfg(Config{MaxAttempts: 4, Idempotent: true, Jitter: DecorrelatedJitter, BaseDelay: 100 * time.Millisecond, MaxDelay: 10 * time.Second})
	op, _ := tFailing(-1, errBoom)
	_ = DoErr(context.Background(), cfg, op)
	// prev=100 => 200; prev=200 => 350; prev=350 => 575
	if got, want := e.Sleeps(), tDurs(200, 350, 575); !tEqualDurs(got, want) {
		t.Errorf("sleeps = %v, want %v: each decorrelated delay is derived from the one before it", got, want)
	}
}

// ---------- Do: deadlines, cancellation, Retry-After ----------

func TestDo_RetryAfterIsAFloorNotACeiling(t *testing.T) {
	e := tNewEnv()
	var n int
	op := func(context.Context) error {
		n++
		switch n {
		case 1:
			return &RetryAfterError{Err: errBoom, After: 3 * time.Second}
		case 2:
			return &RetryAfterError{Err: errBoom, After: 10 * time.Millisecond}
		}
		return nil
	}
	cfg := e.cfg(Config{MaxAttempts: 5, Idempotent: true, Jitter: NoJitter, BaseDelay: 100 * time.Millisecond})
	if err := DoErr(context.Background(), cfg, op); err != nil {
		t.Fatalf("err = %v", err)
	}
	// first wait: max(100ms backoff, 3s Retry-After) = 3s; second: max(200ms, 10ms) = 200ms
	if got, want := e.Sleeps(), []time.Duration{3 * time.Second, 200 * time.Millisecond}; !tEqualDurs(got, want) {
		t.Errorf("sleeps = %v, want %v: honour a longer Retry-After, never wait less than your own backoff", got, want)
	}
}

func TestDo_RetryAfterBeyondTheDeadlineFailsFastWithoutSleeping(t *testing.T) {
	e := tNewEnv()
	ctx, cancel := tDeadline(t, e, 2*time.Second)
	defer cancel()
	var calls int
	op := func(context.Context) error {
		calls++
		return &RetryAfterError{Err: errBoom, After: 5 * time.Second}
	}
	err := DoErr(ctx, e.cfg(Config{MaxAttempts: 5, Idempotent: true}), op)
	if calls != 1 || len(e.Sleeps()) != 0 {
		t.Errorf("calls=%d sleeps=%v; want 1 call and no sleep: waiting 5 s inside a 2 s budget only burns the caller's time", calls, e.Sleeps())
	}
	if !errors.Is(err, ErrDeadlineBudget) || !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want ErrDeadlineBudget wrapping the last error", err)
	}
}

func TestDo_ABackoffThatDoesNotFitInTheDeadlineIsNotSlept(t *testing.T) {
	e := tNewEnv()
	ctx, cancel := tDeadline(t, e, 250*time.Millisecond)
	defer cancel()
	op, calls := tFailing(-1, errBoom)
	cfg := e.cfg(Config{MaxAttempts: 10, Idempotent: true, Jitter: NoJitter, BaseDelay: 100 * time.Millisecond})
	err := DoErr(ctx, cfg, op)
	// attempt 1 fails -> wait 100 ms (150 ms left); attempt 2 fails -> wait 200 ms does not fit in 150 ms
	if calls.Load() != 2 {
		t.Errorf("op called %d times, want 2", calls.Load())
	}
	if got, want := e.Sleeps(), tDurs(100); !tEqualDurs(got, want) {
		t.Errorf("sleeps = %v, want %v: do not sleep when the wait would use up the whole remaining budget", got, want)
	}
	if !errors.Is(err, ErrDeadlineBudget) {
		t.Errorf("err = %v, want ErrDeadlineBudget", err)
	}
}

func TestDo_TotalWaitingNeverExceedsTheCallersDeadline(t *testing.T) {
	e := tNewEnv()
	ctx, cancel := tDeadline(t, e, 5*time.Second)
	defer cancel()
	op, _ := tFailing(-1, errBoom)
	cfg := e.cfg(Config{MaxAttempts: 100, Idempotent: true, Jitter: NoJitter, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second})
	err := DoErr(ctx, cfg, op)
	if e.Elapsed() > 5*time.Second {
		t.Errorf("slept %v in total, past the 5 s deadline", e.Elapsed())
	}
	if !errors.Is(err, ErrDeadlineBudget) {
		t.Errorf("err = %v, want ErrDeadlineBudget (the deadline, not MaxAttempts, ended the loop)", err)
	}
	// 100+200+400+800+1000+1000+1000 = 4500 ms; the next 1000 ms does not fit
	if got, want := e.Elapsed(), 4500*time.Millisecond; got != want {
		t.Errorf("elapsed = %v, want %v", got, want)
	}
}

func TestDo_AttemptTimeoutIsCappedByWhatIsLeftOfTheBudget(t *testing.T) {
	e := tNewEnv()
	ctx, cancel := tDeadline(t, e, 5*time.Second)
	defer cancel()
	var left []time.Duration
	op := func(ctx context.Context) error {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Errorf("attempt context has no deadline although AttemptTimeout is set")
			return errBoom
		}
		left = append(left, time.Until(dl))
		return errBoom
	}
	cfg := e.cfg(Config{MaxAttempts: 10, Idempotent: true, Jitter: NoJitter,
		BaseDelay: 1500 * time.Millisecond, MaxDelay: 1500 * time.Millisecond, AttemptTimeout: 2 * time.Second})
	_ = DoErr(ctx, cfg, op)
	// fake time of each attempt: 0 s, 1.5 s, 3.0 s, 4.5 s => budget left 5, 3.5, 2.0, 0.5 s
	if len(left) != 4 {
		t.Fatalf("attempts = %d, want 4 (%v)", len(left), left)
	}
	for i := 0; i < 3; i++ {
		if left[i] > 2*time.Second || left[i] < 1500*time.Millisecond {
			t.Errorf("attempt %d timeout ≈ %v, want ≈ 2s (AttemptTimeout)", i+1, left[i])
		}
	}
	if left[3] > 500*time.Millisecond || left[3] < 100*time.Millisecond {
		t.Errorf("last attempt timeout ≈ %v, want ≤ 500ms: an attempt must not outlive the caller's deadline", left[3])
	}
}

func TestDo_ACancelledCallerIsNotRetriedAgainst(t *testing.T) {
	e := tNewEnv()
	ctx, cancel := context.WithCancel(context.Background())
	budget := NewBudget(0, 10, 1)
	var calls, onRetry int
	op := func(ctx context.Context) error {
		calls++
		cancel() // the member closed the app
		return ctx.Err()
	}
	cfg := e.cfg(Config{MaxAttempts: 5, Idempotent: true, Budget: budget,
		OnRetry: func(int, error, time.Duration) { onRetry++ }})
	err := DoErr(ctx, cfg, op)
	if calls != 1 || len(e.Sleeps()) != 0 || onRetry != 0 {
		t.Errorf("calls=%d sleeps=%v onRetry=%d; want 1 call and no retry machinery once the caller is gone", calls, e.Sleeps(), onRetry)
	}
	if got := budget.Tokens(); got != 1 {
		t.Errorf("Tokens() = %v, want 1: nobody is waiting for a retry, so no token may be spent on it", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to match context.Canceled", err)
	}
}

func TestDo_AlreadyCancelledContextCallsNothing(t *testing.T) {
	e := tNewEnv()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	op, calls := tFailing(0, nil)
	err := DoErr(ctx, e.cfg(Config{Idempotent: true}), op)
	if calls.Load() != 0 {
		t.Errorf("op called %d times with a cancelled context", calls.Load())
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestDo_CancellationWhileWaitingStopsTheLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	cfg := Config{MaxAttempts: 5, Idempotent: true,
		Sleep: func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }}
	err := DoErr(ctx, cfg, func(context.Context) error { calls++; return errBoom })
	if calls != 1 {
		t.Errorf("op called %d times, want 1: no attempt may start after the wait was cancelled", calls)
	}
	if !errors.Is(err, context.Canceled) || !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want to wrap context.Canceled and the last error", err)
	}
}

func TestDo_RealSleepRespectsContext(t *testing.T) {
	// Default Sleep: a 30 minute backoff fits inside the 1 hour deadline, so
	// Do really sleeps; cancelling the context must interrupt that sleep.
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	time.AfterFunc(50*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() {
		done <- DoErr(ctx, Config{Idempotent: true, Jitter: NoJitter, BaseDelay: 30 * time.Minute, MaxDelay: 30 * time.Minute},
			func(context.Context) error { return errBoom })
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want it to wrap context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Do did not return within 5 s: the default sleep must select on ctx.Done()")
	}
}

// ---------- ParseRetryAfter ----------

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"120", 120 * time.Second, true},
		{" 5 ", 5 * time.Second, true},
		{"0", 0, true},
		{"Sun, 01 Mar 2026 12:00:30 GMT", 30 * time.Second, true},
		{"Sun, 01 Mar 2026 11:00:00 GMT", 0, true}, // in the past: no wait
		{"", 0, false},
		{"soon", 0, false},
		{"-5", 0, false},
		{"1.5", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseRetryAfter(c.in, now)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseRetryAfter(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
	if d, ok := ParseRetryAfter("99999999999999999", now); !ok || d <= 0 {
		t.Errorf("huge value: got (%v, %v), want a positive clamped duration (no overflow)", d, ok)
	}
}

// ---------- Budget ----------

func TestBudget_EarnsARatioPerRequestAndSpendsWholeTokens(t *testing.T) {
	b := NewBudget(0.1, 5, 0)
	for i := 0; i < 9; i++ {
		b.Deposit()
	}
	if b.Withdraw() {
		t.Fatalf("withdraw succeeded after 9 deposits of 0.1; a retry costs a whole token")
	}
	b.Deposit() // 10 × 0.1 = exactly 1 token (no floating-point drift)
	if !b.Withdraw() {
		t.Fatalf("withdraw failed after 10 deposits of 0.1; ten first attempts should pay for one retry")
	}
	if b.Withdraw() {
		t.Errorf("a second withdraw succeeded; the token was already spent")
	}
}

func TestBudget_CapsSavingsAtMax(t *testing.T) {
	b := NewBudget(1, 3, 0)
	for i := 0; i < 50; i++ {
		b.Deposit()
	}
	n := 0
	for b.Withdraw() {
		n++
		if n > 10 {
			break
		}
	}
	if n != 3 {
		t.Errorf("withdrew %d tokens after 50 deposits, want exactly max = 3 (a long quiet period must not buy an unbounded retry burst)", n)
	}
}

func TestBudget_InitialTokensAllowAColdStart(t *testing.T) {
	b := NewBudget(0.1, 10, 2)
	if got := b.Tokens(); got != 2 {
		t.Errorf("Tokens() = %v, want 2", got)
	}
	if !b.Withdraw() || !b.Withdraw() || b.Withdraw() {
		t.Errorf("want exactly two withdrawals from an initial balance of 2")
	}
}

func TestBudget_ConcurrentWithdrawalsNeverOverspend(t *testing.T) {
	b := NewBudget(0, 10, 10)
	var got atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if b.Withdraw() {
				got.Add(1)
			}
		}()
	}
	close(start)
	tWait(t, &wg, "concurrent withdrawals")
	if got.Load() != 10 {
		t.Errorf("%d withdrawals succeeded, want exactly 10: check-and-take must be one atomic step", got.Load())
	}
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

// ---------- Do + Budget ----------

func TestDo_DepositsOncePerCallAndWithdrawsPerRetry(t *testing.T) {
	e := tNewEnv()
	b := NewBudget(0.5, 10, 0)
	op, calls := tFailing(-1, errBoom)
	err := DoErr(context.Background(), e.cfg(Config{MaxAttempts: 5, Idempotent: true, Budget: b}), op)
	if calls.Load() != 1 {
		t.Errorf("op called %d times, want 1: one call earns only 0.5 tokens, so no retry is affordable", calls.Load())
	}
	if !errors.Is(err, ErrBudgetExhausted) || !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want ErrBudgetExhausted wrapping the last error", err)
	}
	if got := b.Tokens(); got != 0.5 {
		t.Errorf("Tokens() = %v, want 0.5 (deposit once per Do, not once per attempt)", got)
	}

	b2 := NewBudget(0, 10, 1)
	op2, calls2 := tFailing(-1, errBoom)
	err = DoErr(context.Background(), e.cfg(Config{MaxAttempts: 5, Idempotent: true, Budget: b2}), op2)
	if calls2.Load() != 2 || !errors.Is(err, ErrBudgetExhausted) {
		t.Errorf("with one token: calls = %d, err = %v; want 2 calls then ErrBudgetExhausted", calls2.Load(), err)
	}
}

func TestDo_ABudgetTokenIsNotSpentOnAWaitThatCannotHappen(t *testing.T) {
	e := tNewEnv()
	ctx, cancel := tDeadline(t, e, time.Second)
	defer cancel()
	b := NewBudget(0, 10, 1)
	op := func(context.Context) error { return &RetryAfterError{Err: errBoom, After: 30 * time.Second} }
	_ = DoErr(ctx, e.cfg(Config{MaxAttempts: 5, Idempotent: true, Budget: b}), op)
	if got := b.Tokens(); got != 1 {
		t.Errorf("Tokens() = %v, want 1: check the deadline before withdrawing", got)
	}
}

// tLayers builds k nested retrying layers around a bottom call that always
// fails. Each layer retries its inner layer, as in "client, gateway, adapter".
func tLayers(k int, cfg Config, bottom *atomic.Int64) func(context.Context) error {
	var layer func(depth int) func(context.Context) error
	layer = func(depth int) func(context.Context) error {
		if depth == k {
			return func(context.Context) error { bottom.Add(1); return errBoom }
		}
		inner := layer(depth + 1)
		return func(ctx context.Context) error { return DoErr(ctx, cfg, inner) }
	}
	return layer(0)
}

func TestAmplification_NestedRetriesMultiplyWithoutABudget(t *testing.T) {
	e := tNewEnv()
	var bottom atomic.Int64
	cfg := e.cfg(Config{MaxAttempts: 3, Idempotent: true}) // r = 2 retries per layer
	_ = tLayers(3, cfg, &bottom)(context.Background())
	if bottom.Load() != 27 {
		t.Errorf("the dependency saw %d calls for one user action, want (r+1)^k = 3^3 = 27", bottom.Load())
	}
}

func TestAmplification_ASharedBudgetBoundsTheStorm(t *testing.T) {
	const users = 200
	run := func(budget *Budget) (bottom int64, retries int64) {
		e := tNewEnv()
		var b atomic.Int64
		var r atomic.Int64
		cfg := e.cfg(Config{MaxAttempts: 3, Idempotent: true, Budget: budget,
			OnRetry: func(int, error, time.Duration) { r.Add(1) }})
		layers := tLayers(3, cfg, &b)
		for i := 0; i < users; i++ {
			_ = layers(context.Background())
		}
		return b.Load(), r.Load()
	}
	without, _ := run(nil)
	if without != users*27 {
		t.Fatalf("without a budget the dependency saw %d calls, want %d (27 per user action)", without, users*27)
	}
	with, retries := run(NewBudget(0.1, 10, 0))
	t.Logf("%d user actions: %d dependency calls without a budget, %d with a 10%% budget (%d retries)", users, without, with, retries)
	if with > users*3 {
		t.Errorf("with a 10%% shared budget the dependency saw %d calls for %d user actions, want ≤ %d (≈ 3x), not 27x", with, users, users*3)
	}
	if without < 8*with {
		t.Errorf("budget cut load only from %d to %d; expected at least 8x less", without, with)
	}
}

func TestDo_ManyCallersShareOneBudgetUnderRace(t *testing.T) {
	const goroutines, per = 50, 20
	budget := NewBudget(0.1, 5, 5)
	var retries, calls atomic.Int64
	cfg := Config{MaxAttempts: 5, Idempotent: true, Budget: budget, Jitter: NoJitter,
		Sleep:   func(context.Context, time.Duration) error { return nil },
		OnRetry: func(int, error, time.Duration) { retries.Add(1) }}
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				_ = DoErr(context.Background(), cfg, func(context.Context) error { calls.Add(1); return errBoom })
			}
		}()
	}
	tWait(t, &wg, "concurrent Do calls")
	// retries ≤ initial (5) + 0.1 × number of calls to Do (1,000)
	if limit := int64(5 + goroutines*per/10); retries.Load() > limit {
		t.Errorf("%d retries across %d calls; the budget allows at most %d: Withdraw must be atomic", retries.Load(), goroutines*per, limit)
	}
	if calls.Load() != int64(goroutines*per)+retries.Load() {
		t.Errorf("calls = %d, want first attempts + retries = %d", calls.Load(), int64(goroutines*per)+retries.Load())
	}
}
