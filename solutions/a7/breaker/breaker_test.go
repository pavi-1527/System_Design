package breaker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

var errDown = errors.New("vendor down")

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

// tCfg is a small, readable configuration: ratio rule at 50% with at least 4
// calls in a 10 s window, open for 30 s.
func tCfg(c *tClock) Config {
	return Config{Window: 10 * time.Second, Buckets: 10, MinRequests: 4, FailureRatio: 0.5,
		OpenFor: 30 * time.Second, HalfOpenMax: 1, HalfOpenSuccesses: 1, Now: c.Now}
}

func tOK(context.Context) error   { return nil }
func tFail(context.Context) error { return errDown }

// tCall runs fn through b and returns its error.
func tCall(b *Breaker, fn func(context.Context) error) error {
	return b.Do(context.Background(), fn)
}

// tTrip drives a breaker open with 4 consecutive failures (tCfg).
func tTrip(t *testing.T, b *Breaker) {
	t.Helper()
	for i := 0; i < 4; i++ {
		if err := tCall(b, tFail); !errors.Is(err, errDown) {
			t.Fatalf("failure %d: got %v, want the dependency's error (breaker should still be closed)", i+1, err)
		}
	}
	if s := b.State(); s != Open {
		t.Fatalf("after 4/4 failures state = %v, want open", s)
	}
}

func tWait(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish within 10 s (deadlock or a probe that never returns?)", what)
	}
}

func tEventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

// ---------- closed state and tripping ----------

func TestBreaker_StartsClosedAndPassesCallsThrough(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	if b.State() != Closed {
		t.Fatalf("initial state = %v, want closed", b.State())
	}
	var ran int
	if err := tCall(b, func(context.Context) error { ran++; return nil }); err != nil || ran != 1 {
		t.Errorf("err=%v ran=%d; want nil and 1", err, ran)
	}
	if err := tCall(b, tFail); !errors.Is(err, errDown) {
		t.Errorf("a closed breaker must return the dependency's own error, got %v", err)
	}
}

func TestBreaker_ZeroConfigGetsUsableDefaults(t *testing.T) {
	b := New(Config{})
	for i := 0; i < 19; i++ {
		_ = tCall(b, tFail)
	}
	if b.State() != Closed {
		t.Errorf("state = %v after 19 failures, want closed (default MinRequests is 20)", b.State())
	}
	_ = tCall(b, tFail)
	if b.State() != Open {
		t.Errorf("state = %v after 20 failures, want open (default ratio 0.5, MinRequests 20)", b.State())
	}
}

func TestBreaker_DoesNotTripBelowMinRequestsEvenAtHundredPercentFailures(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	for i := 0; i < 3; i++ {
		_ = tCall(b, tFail)
	}
	if s := b.State(); s != Closed {
		t.Errorf("state = %v after 3 failures, want closed: with MinRequests = 4 one unlucky call at low traffic must not open the circuit", s)
	}
	_ = tCall(b, tFail)
	if s := b.State(); s != Open {
		t.Errorf("state = %v after the 4th failure, want open", s)
	}
}

func TestBreaker_StaysClosedBelowTheFailureRatio(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	for i := 0; i < 6; i++ { // 6 successes
		_ = tCall(b, tOK)
	}
	for i := 0; i < 4; i++ { // 4 failures => 40% < 50%
		_ = tCall(b, tFail)
	}
	if s := b.State(); s != Closed {
		t.Errorf("state = %v at 40%% failures, want closed (threshold 50%%)", s)
	}
	_ = tCall(b, tFail) // 5/11 = 45%
	_ = tCall(b, tFail) // 6/12 = 50% => trips
	if s := b.State(); s != Open {
		t.Errorf("state = %v at 50%% failures over 12 calls, want open (>= threshold trips)", s)
	}
}

func TestBreaker_OnlyOutcomesInsideTheRollingWindowCount(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	for i := 0; i < 3; i++ {
		_ = tCall(b, tFail)
	}
	c.Advance(11 * time.Second) // the 3 failures slide out of the 10 s window
	if s, f := b.Counts(); s != 0 || f != 0 {
		t.Errorf("Counts() = (%d, %d) after the window passed, want (0, 0)", s, f)
	}
	_ = tCall(b, tFail) // alone: 1 call < MinRequests
	if s := b.State(); s != Closed {
		t.Errorf("state = %v: failures older than the window must not count toward tripping", s)
	}
}

func TestBreaker_OutcomesInsideTheWindowAccumulate(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	for i := 0; i < 3; i++ {
		_ = tCall(b, tFail)
	}
	c.Advance(5 * time.Second) // still inside the window
	_ = tCall(b, tFail)
	if s := b.State(); s != Open {
		t.Errorf("state = %v, want open: 4 failures within 10 s", s)
	}
}

func TestBreaker_ConsecutiveFailuresRuleTripsRegardlessOfVolume(t *testing.T) {
	c := tNewClock()
	cfg := tCfg(c)
	cfg.FailureRatio, cfg.MinRequests, cfg.ConsecutiveFailures = 0, 1000, 3
	b := New(cfg)
	_ = tCall(b, tFail)
	_ = tCall(b, tFail)
	_ = tCall(b, tOK) // a success resets the run
	_ = tCall(b, tFail)
	_ = tCall(b, tFail)
	if s := b.State(); s != Closed {
		t.Fatalf("state = %v, want closed: the run was broken by a success", s)
	}
	_ = tCall(b, tFail)
	if s := b.State(); s != Open {
		t.Errorf("state = %v after 3 consecutive failures, want open", s)
	}
}

// ---------- open and half-open ----------

func TestBreaker_OpenRejectsWithoutCallingTheDependency(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	tTrip(t, b)
	var ran int
	for i := 0; i < 10; i++ {
		err := tCall(b, func(context.Context) error { ran++; return nil })
		if !errors.Is(err, ErrOpen) {
			t.Fatalf("call %d: err = %v, want ErrOpen", i, err)
		}
	}
	if ran != 0 {
		t.Errorf("fn ran %d times while open; the whole point is to stop calling a failing dependency", ran)
	}
	c.Advance(29 * time.Second)
	if s := b.State(); s != Open {
		t.Errorf("state = %v after 29 s, want still open (OpenFor = 30 s)", s)
	}
}

func TestBreaker_HalfOpensAfterOpenForAndAProbeSuccessCloses(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	tTrip(t, b)
	c.Advance(30 * time.Second)
	if s := b.State(); s != HalfOpen {
		t.Fatalf("state = %v after OpenFor, want half-open", s)
	}
	if err := tCall(b, tOK); err != nil {
		t.Fatalf("probe: err = %v", err)
	}
	if s := b.State(); s != Closed {
		t.Errorf("state = %v after a successful probe, want closed", s)
	}
	if s, f := b.Counts(); s != 0 || f != 0 {
		t.Errorf("Counts() = (%d, %d) after closing, want a clean slate (0, 0): old failures must not instantly re-trip", s, f)
	}
	// A single failure after closing must not re-trip (needs MinRequests again).
	_ = tCall(b, tFail)
	if s := b.State(); s != Closed {
		t.Errorf("state = %v, want closed", s)
	}
}

func TestBreaker_AFailedProbeReopensForAFullOpenPeriod(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	tTrip(t, b)
	c.Advance(30 * time.Second)
	if err := tCall(b, tFail); !errors.Is(err, errDown) {
		t.Fatalf("probe err = %v, want the dependency's error", err)
	}
	if s := b.State(); s != Open {
		t.Fatalf("state = %v after a failed probe, want open", s)
	}
	c.Advance(29 * time.Second)
	if s := b.State(); s != Open {
		t.Errorf("state = %v 29 s after the failed probe, want open: the timer restarts", s)
	}
	c.Advance(time.Second)
	if s := b.State(); s != HalfOpen {
		t.Errorf("state = %v 30 s after the failed probe, want half-open", s)
	}
}

func TestBreaker_HalfOpenAdmitsOnlyHalfOpenMaxProbes(t *testing.T) {
	c := tNewClock()
	cfg := tCfg(c)
	cfg.HalfOpenMax = 2
	b := New(cfg)
	tTrip(t, b)
	c.Advance(30 * time.Second)
	d1, err1 := b.Allow()
	d2, err2 := b.Allow()
	_, err3 := b.Allow()
	if err1 != nil || err2 != nil {
		t.Fatalf("first two probes: %v, %v; want both admitted", err1, err2)
	}
	if !errors.Is(err3, ErrOpen) {
		t.Errorf("third caller: err = %v, want ErrOpen (HalfOpenMax = 2)", err3)
	}
	d1(nil)
	d2(nil)
}

func TestBreaker_NeedsHalfOpenSuccessesProbesToClose(t *testing.T) {
	c := tNewClock()
	cfg := tCfg(c)
	cfg.HalfOpenSuccesses = 3
	b := New(cfg)
	tTrip(t, b)
	c.Advance(30 * time.Second)
	for i := 1; i <= 2; i++ {
		_ = tCall(b, tOK)
		if s := b.State(); s != HalfOpen {
			t.Fatalf("state = %v after %d probe successes, want half-open (needs 3)", s, i)
		}
	}
	_ = tCall(b, tOK)
	if s := b.State(); s != Closed {
		t.Errorf("state = %v after 3 probe successes, want closed", s)
	}
}

func TestBreaker_ThunderingHerdOnHalfOpenOnlyProbesGetThrough(t *testing.T) {
	c := tNewClock()
	cfg := tCfg(c)
	cfg.HalfOpenMax = 2
	b := New(cfg)
	tTrip(t, b)
	c.Advance(30 * time.Second) // 200 callers arrive at the instant the breaker half-opens

	const callers = 200
	var running, maxRunning, rejected, ran atomic.Int64
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := tCall(b, func(context.Context) error {
				ran.Add(1)
				n := running.Add(1)
				for {
					m := maxRunning.Load()
					if n <= m || maxRunning.CompareAndSwap(m, n) {
						break
					}
				}
				select {
				case <-release:
				case <-time.After(5 * time.Second):
				}
				running.Add(-1)
				return nil
			})
			if errors.Is(err, ErrOpen) {
				rejected.Add(1)
			}
		}()
	}
	tEventually(t, func() bool { return rejected.Load() == callers-2 },
		"expected 198 of 200 callers to be rejected quickly while the 2 probes are in flight")
	if ran.Load() != 2 || maxRunning.Load() != 2 {
		t.Errorf("%d calls reached the recovering dependency (max concurrent %d), want exactly HalfOpenMax = 2: "+
			"a herd of probes is how recovering services fall over again", ran.Load(), maxRunning.Load())
	}
	close(release)
	tWait(t, &wg, "herd")
	if s := b.State(); s != Closed {
		t.Errorf("state = %v after successful probes, want closed", s)
	}
}

// ---------- outcome classification ----------

func TestBreaker_CallerCancellationIsNotAFailure(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // the member closed the app
		err := b.Do(ctx, func(ctx context.Context) error { return ctx.Err() })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled passed through", err)
		}
	}
	if s := b.State(); s != Closed {
		t.Errorf("state = %v after 10 cancelled calls, want closed: callers hanging up say nothing about the vendor", s)
	}
	if s, f := b.Counts(); s != 0 || f != 0 {
		t.Errorf("Counts() = (%d, %d), want (0, 0): cancellations are not recorded at all", s, f)
	}
}

func TestBreaker_ACancelledErrorWithALiveCallerIsIgnoredByDefault(t *testing.T) {
	// e.g. a hedged request's losing copy, cancelled by its own sibling:
	// fn sees context.Canceled although the caller is still waiting.
	c := tNewClock()
	b := New(tCfg(c))
	for i := 0; i < 10; i++ {
		_ = tCall(b, func(context.Context) error { return context.Canceled })
	}
	if s, f := b.Counts(); s != 0 || f != 0 {
		t.Errorf("Counts() = (%d, %d), want (0, 0): the default classifier ignores context.Canceled", s, f)
	}
	if s := b.State(); s != Closed {
		t.Errorf("state = %v, want closed", s)
	}
}

func TestBreaker_ClosingStartsFromACleanWindow(t *testing.T) {
	// OpenFor (2 s) is shorter than the window (10 s), so the failures that
	// tripped the breaker are still inside the window when it closes.
	c := tNewClock()
	cfg := tCfg(c)
	cfg.OpenFor = 2 * time.Second
	b := New(cfg)
	tTrip(t, b)
	c.Advance(2 * time.Second)
	if err := tCall(b, tOK); err != nil || b.State() != Closed {
		t.Fatalf("probe err=%v state=%v; want success and closed", err, b.State())
	}
	if s, f := b.Counts(); s != 0 || f != 0 {
		t.Errorf("Counts() = (%d, %d) right after closing, want (0, 0)", s, f)
	}
	_ = tCall(b, tFail)
	if s := b.State(); s != Closed {
		t.Errorf("state = %v: one failure after recovery re-tripped the breaker because the old failures were still counted", s)
	}
}

func TestBreaker_CallersOwnDeadlineExpiryIsNotAFailure(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 0) // no budget left
		_ = b.Do(ctx, func(ctx context.Context) error { return ctx.Err() })
		cancel()
	}
	if s := b.State(); s != Closed {
		t.Errorf("state = %v, want closed: a caller who arrives with an exhausted budget is not evidence against the dependency", s)
	}
}

func TestBreaker_ADependencyTimeoutWithALiveCallerIsAFailure(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	for i := 0; i < 4; i++ {
		_ = tCall(b, func(context.Context) error { return context.DeadlineExceeded }) // the attempt timeout fired inside fn
	}
	if s := b.State(); s != Open {
		t.Errorf("state = %v, want open: slow responses are the most common way a vendor fails", s)
	}
}

func TestBreaker_ClassifyCanExcludeErrorsThatAreNotTheDependencysFault(t *testing.T) {
	c := tNewClock()
	errNotFound := errors.New("404")
	cfg := tCfg(c)
	cfg.Classify = func(err error) Outcome {
		switch {
		case err == nil, errors.Is(err, errNotFound):
			return Success // the vendor answered correctly
		case errors.Is(err, context.Canceled):
			return Ignore
		}
		return Failure
	}
	b := New(cfg)
	for i := 0; i < 20; i++ {
		_ = tCall(b, func(context.Context) error { return errNotFound })
	}
	if s := b.State(); s != Closed {
		t.Errorf("state = %v after 20 not-found answers, want closed", s)
	}
	if s, f := b.Counts(); s != 20 || f != 0 {
		t.Errorf("Counts() = (%d, %d), want (20, 0)", s, f)
	}
}

func TestBreaker_IgnoredProbeFreesTheSlotWithoutAVerdict(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	tTrip(t, b)
	c.Advance(30 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = b.Do(ctx, func(ctx context.Context) error { return ctx.Err() })
	if s := b.State(); s != HalfOpen {
		t.Fatalf("state = %v, want half-open: a cancelled probe is neither success nor failure", s)
	}
	if err := tCall(b, tOK); err != nil {
		t.Errorf("next probe err = %v, want admitted: the cancelled probe's slot must be released", err)
	}
	if s := b.State(); s != Closed {
		t.Errorf("state = %v, want closed", s)
	}
}

func TestBreaker_ResultsFromBeforeAStateChangeAreIgnored(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	slowOK, err := b.Allow() // admitted while closed, still running
	if err != nil {
		t.Fatal(err)
	}
	slowBad, err := b.Allow() // another straggler from the same era
	if err != nil {
		t.Fatal(err)
	}
	tTrip(t, b)
	c.Advance(30 * time.Second) // half-open
	slowOK(nil)                 // the old call finally succeeds
	if s := b.State(); s != HalfOpen {
		t.Errorf("state = %v: a straggler that started before the trip must not close the breaker (it is not a probe)", s)
	}
	slowBad(errDown) // and a late failure must not re-open it either
	if s := b.State(); s != HalfOpen {
		t.Errorf("state = %v: a straggler's failure must not re-open the breaker", s)
	}
	probe, err := b.Allow() // this is the real probe
	if err != nil {
		t.Fatalf("probe not admitted: %v (stragglers must not occupy probe slots)", err)
	}
	if second, err := b.Allow(); err == nil {
		second(nil)
		t.Errorf("second probe admitted while the first is in flight (HalfOpenMax = 1)")
	}
	probe(nil)
	if s := b.State(); s != Closed {
		t.Errorf("state = %v, want closed after the real probe succeeded", s)
	}
}

func TestBreaker_DoneFunctionIsIdempotent(t *testing.T) {
	c := tNewClock()
	b := New(tCfg(c))
	done, err := b.Allow()
	if err != nil {
		t.Fatal(err)
	}
	done(errDown)
	done(errDown)
	done(errDown)
	if _, f := b.Counts(); f != 1 {
		t.Errorf("failures = %d after calling done three times, want 1", f)
	}
}

func TestBreaker_PanicCountsAsFailureAndPropagates(t *testing.T) {
	c := tNewClock()
	cfg := tCfg(c)
	cfg.MinRequests = 1
	b := New(cfg)
	func() {
		defer func() {
			if recover() == nil {
				t.Errorf("panic was swallowed; Do must let it continue")
			}
		}()
		_ = tCall(b, func(context.Context) error { panic("adapter bug") })
	}()
	if s := b.State(); s != Open {
		t.Errorf("state = %v after a panicking call with MinRequests=1, want open (a panic is a failure)", s)
	}
}

// ---------- callbacks, concurrency ----------

func TestBreaker_OnStateChangeSeesEveryTransitionInOrder(t *testing.T) {
	c := tNewClock()
	var mu sync.Mutex
	var got []string
	cfg := tCfg(c)
	cfg.OnStateChange = func(from, to State) {
		mu.Lock()
		got = append(got, from.String()+"->"+to.String())
		mu.Unlock()
	}
	b := New(cfg)
	tTrip(t, b)
	c.Advance(30 * time.Second)
	_ = tCall(b, tFail) // half-open -> open
	c.Advance(30 * time.Second)
	_ = tCall(b, tOK) // half-open -> closed
	want := []string{"closed->open", "open->half-open", "half-open->open", "open->half-open", "half-open->closed"}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("transitions = %v, want %v", got, want)
		}
	}
}

func TestBreaker_CallbackMayInspectTheBreakerWithoutDeadlock(t *testing.T) {
	c := tNewClock()
	cfg := tCfg(c)
	var b *Breaker
	seen := make(chan State, 8)
	cfg.OnStateChange = func(from, to State) { seen <- b.State() } // would deadlock if called under the lock
	b = New(cfg)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 4; i++ {
			_ = tCall(b, tFail)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: OnStateChange must run after the breaker's lock is released")
	}
	select {
	case s := <-seen:
		if s != Open {
			t.Errorf("State() inside the callback = %v, want open", s)
		}
	default:
		t.Fatal("OnStateChange was never called")
	}
}

func TestBreaker_ConcurrentUseIsRaceFree(t *testing.T) {
	c := tNewClock()
	cfg := tCfg(c)
	cfg.MinRequests = 10
	b := New(cfg)
	var wg sync.WaitGroup
	var ran atomic.Int64
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				err := tCall(b, func(context.Context) error {
					ran.Add(1)
					if (g+i)%3 == 0 {
						return errDown
					}
					return nil
				})
				if err != nil && !errors.Is(err, errDown) && !errors.Is(err, ErrOpen) {
					t.Errorf("unexpected error %v", err)
					return
				}
				if i%50 == 0 {
					c.Advance(5 * time.Second)
				}
				_ = b.State()
				_, _ = b.Counts()
			}
		}(g)
	}
	tWait(t, &wg, "concurrent callers")
	if ran.Load() == 0 {
		t.Errorf("no call ever ran")
	}
}
