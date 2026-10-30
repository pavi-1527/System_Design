package decorator

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

var (
	errBoom = errors.New("vendor boom")
	errBad  = errors.New("vendor says 400")
)

// vendor is a scripted Client that counts calls. fn receives the 1-based call number.
type vendor struct {
	calls atomic.Int64
	fn    func(n int, ctx context.Context, req Request) (Response, error)
}

func (v *vendor) Do(ctx context.Context, req Request) (Response, error) {
	n := int(v.calls.Add(1))
	return v.fn(n, ctx, req)
}

func (v *vendor) count() int { return int(v.calls.Load()) }

func alwaysFail() *vendor {
	return &vendor{fn: func(int, context.Context, Request) (Response, error) { return Response{}, errBoom }}
}

func alwaysOK() *vendor {
	return &vendor{fn: func(_ int, _ context.Context, r Request) (Response, error) { return Response{ID: "ok-" + r.Key}, nil }}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)} }
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// sleeper records requested waits instead of sleeping.
type sleeper struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *sleeper) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	s.waits = append(s.waits, d)
	s.mu.Unlock()
	return nil
}

func (s *sleeper) got() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.waits...)
}

var req = Request{Key: "k1", AmountMinor: 100}

func linear(attempt int) time.Duration { return time.Duration(attempt) * 10 * time.Millisecond }

// ---- Chain ------------------------------------------------------------------------

func TestChain_FirstDecoratorIsOutermost(t *testing.T) {
	var trace []string
	tag := func(name string) Decorator {
		return func(next Client) Client {
			return ClientFunc(func(ctx context.Context, r Request) (Response, error) {
				trace = append(trace, name+">")
				resp, err := next.Do(ctx, r)
				trace = append(trace, "<"+name)
				return resp, err
			})
		}
	}
	base := ClientFunc(func(_ context.Context, r Request) (Response, error) {
		trace = append(trace, "vendor")
		return Response{ID: r.Key}, nil
	})
	resp, err := Chain(base, tag("A"), tag("B"), tag("C")).Do(context.Background(), req)
	if err != nil || resp.ID != "k1" {
		t.Fatalf("Do = %+v, %v", resp, err)
	}
	want := []string{"A>", "B>", "C>", "vendor", "<C", "<B", "<A"}
	if !reflect.DeepEqual(trace, want) {
		t.Errorf("call order = %v, want %v (Chain(c, a, b) must read left to right, outside in)", trace, want)
	}
}

func TestChain_NoDecoratorsIsTheClientItself(t *testing.T) {
	v := alwaysOK()
	if resp, err := Chain(v).Do(context.Background(), req); err != nil || resp.ID != "ok-k1" || v.count() != 1 {
		t.Errorf("Chain(v).Do = %+v, %v after %d calls", resp, err, v.count())
	}
}

// ---- Retry ------------------------------------------------------------------------

func TestRetry_SucceedsAfterTransientFailuresWithBackoffBetweenAttempts(t *testing.T) {
	v := &vendor{fn: func(n int, _ context.Context, r Request) (Response, error) {
		if n < 3 {
			return Response{}, errBoom
		}
		return Response{ID: "done"}, nil
	}}
	s := &sleeper{}
	c := Chain(v, WithRetry(RetryConfig{MaxAttempts: 5, Backoff: linear, Sleep: s.sleep}))
	resp, err := c.Do(context.Background(), req)
	if err != nil || resp.ID != "done" {
		t.Fatalf("Do = %+v, %v", resp, err)
	}
	if v.count() != 3 {
		t.Errorf("vendor called %d times, want 3", v.count())
	}
	if want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}; !reflect.DeepEqual(s.got(), want) {
		t.Errorf("waits = %v, want %v: Backoff(n) is the wait after the n-th failure, none before the first try or after success", s.got(), want)
	}
}

func TestRetry_GivesUpAfterMaxAttemptsWithoutAFinalSleep(t *testing.T) {
	v := alwaysFail()
	s := &sleeper{}
	c := Chain(v, WithRetry(RetryConfig{MaxAttempts: 3, Backoff: linear, Sleep: s.sleep}))
	_, err := c.Do(context.Background(), req)
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want it to wrap the vendor error", err)
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("err = %q, should say how many attempts were made", err)
	}
	if v.count() != 3 {
		t.Errorf("vendor called %d times, want exactly MaxAttempts = 3", v.count())
	}
	if len(s.got()) != 2 {
		t.Errorf("slept %d times, want 2 (between attempts only; a sleep after the last failure is wasted latency)", len(s.got()))
	}
}

func TestRetry_NonRetryableErrorsStopImmediately(t *testing.T) {
	v := &vendor{fn: func(int, context.Context, Request) (Response, error) { return Response{}, errBad }}
	s := &sleeper{}
	c := Chain(v, WithRetry(RetryConfig{
		MaxAttempts: 5, Sleep: s.sleep,
		Retryable: func(err error) bool { return !errors.Is(err, errBad) },
	}))
	_, err := c.Do(context.Background(), req)
	if !errors.Is(err, errBad) {
		t.Errorf("err = %v, want errBad", err)
	}
	if v.count() != 1 || len(s.got()) != 0 {
		t.Errorf("vendor calls = %d, sleeps = %d; a 4xx-style error must not be retried", v.count(), len(s.got()))
	}
}

func TestRetry_NeverRetriesAnOpenCircuit(t *testing.T) {
	v := &vendor{fn: func(int, context.Context, Request) (Response, error) { return Response{}, ErrOpen }}
	c := Chain(v, WithRetry(RetryConfig{MaxAttempts: 5, Sleep: (&sleeper{}).sleep, Retryable: func(error) bool { return true }}))
	_, err := c.Do(context.Background(), req)
	if !errors.Is(err, ErrOpen) || v.count() != 1 {
		t.Errorf("err = %v after %d calls; hammering an open breaker defeats it", err, v.count())
	}
}

func TestRetry_ZeroMaxAttemptsStillMakesOneCall(t *testing.T) {
	v := alwaysFail()
	_, err := Chain(v, WithRetry(RetryConfig{})).Do(context.Background(), req)
	if !errors.Is(err, errBoom) || v.count() != 1 {
		t.Errorf("err = %v after %d calls, want errBoom after 1", err, v.count())
	}
}

func TestRetry_StopsWaitingWhenTheCallerGivesUp(t *testing.T) {
	v := alwaysFail()
	c := Chain(v, WithRetry(RetryConfig{
		MaxAttempts: 5,
		Backoff:     func(int) time.Duration { return time.Hour }, // default Sleep must honour ctx
	}))
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := c.Do(ctx, req); errc <- err }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, errBoom) {
			t.Errorf("err = %v, want it to wrap both context.Canceled and the last vendor error", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Do kept sleeping after its context was cancelled: the backoff wait must select on ctx.Done()")
	}
	if v.count() != 1 {
		t.Errorf("vendor called %d times, want 1", v.count())
	}
}

func TestRetry_DoesNotCallTheVendorWithADeadContext(t *testing.T) {
	v := alwaysOK()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Chain(v, WithRetry(RetryConfig{MaxAttempts: 3})).Do(ctx, req)
	if !errors.Is(err, context.Canceled) || v.count() != 0 {
		t.Errorf("err = %v after %d calls, want context.Canceled after 0", err, v.count())
	}
}

func TestRetry_RetriesAPerAttemptTimeoutButNotTheCallersDeadline(t *testing.T) {
	v := &vendor{fn: func(n int, ctx context.Context, _ Request) (Response, error) {
		if n <= 2 {
			<-ctx.Done() // a hung attempt: only the per-attempt timeout frees it
			return Response{}, ctx.Err()
		}
		return Response{ID: "third-time-lucky"}, nil
	}}
	c := Chain(v, WithRetry(RetryConfig{MaxAttempts: 3, Sleep: (&sleeper{}).sleep}), WithTimeout(20*time.Millisecond))
	resp, err := c.Do(context.Background(), req)
	if err != nil || resp.ID != "third-time-lucky" {
		t.Fatalf("Do = %+v, %v; a DeadlineExceeded from the per-attempt timeout is retryable while the caller's ctx is alive", resp, err)
	}

	hung := &vendor{fn: func(_ int, ctx context.Context, _ Request) (Response, error) {
		<-ctx.Done()
		return Response{}, ctx.Err()
	}}
	c = Chain(hung, WithRetry(RetryConfig{MaxAttempts: 10, Sleep: (&sleeper{}).sleep}), WithTimeout(time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err = c.Do(ctx, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
	if hung.count() != 1 {
		t.Errorf("vendor called %d times after the caller's deadline passed, want 1: do not keep retrying for a caller that left", hung.count())
	}
}

// ---- Breaker ----------------------------------------------------------------------

func breakerOver(v Client, clk *clock, threshold int, cooldown time.Duration) Client {
	return Chain(v, WithBreaker(BreakerConfig{Threshold: threshold, Cooldown: cooldown, Now: clk.now}))
}

func TestBreaker_OpensAfterConsecutiveFailuresAndStopsCallingTheVendor(t *testing.T) {
	v := alwaysFail()
	c := breakerOver(v, newClock(), 3, time.Minute)
	for i := 1; i <= 3; i++ {
		if _, err := c.Do(context.Background(), req); !errors.Is(err, errBoom) {
			t.Fatalf("call %d: err = %v, want the vendor error", i, err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := c.Do(context.Background(), req); !errors.Is(err, ErrOpen) {
			t.Fatalf("call after threshold: err = %v, want ErrOpen", err)
		}
	}
	if v.count() != 3 {
		t.Errorf("vendor called %d times, want 3: an open circuit must not touch the vendor", v.count())
	}
}

func TestBreaker_SuccessResetsTheConsecutiveFailureCount(t *testing.T) {
	outcomes := []bool{false, false, true, false, false, false} // true = success
	v := &vendor{fn: func(n int, _ context.Context, _ Request) (Response, error) {
		if outcomes[n-1] {
			return Response{ID: "ok"}, nil
		}
		return Response{}, errBoom
	}}
	c := breakerOver(v, newClock(), 3, time.Minute)
	for i := range outcomes {
		_, err := c.Do(context.Background(), req)
		if errors.Is(err, ErrOpen) {
			t.Fatalf("call %d was rejected as open, but fewer than 3 CONSECUTIVE failures had happened", i+1)
		}
	}
	if _, err := c.Do(context.Background(), req); !errors.Is(err, ErrOpen) {
		t.Errorf("after 3 consecutive failures the next call = %v, want ErrOpen", err)
	}
}

func TestBreaker_HalfOpenProbeAfterCooldown(t *testing.T) {
	clk := newClock()
	healthy := atomic.Bool{}
	v := &vendor{fn: func(int, context.Context, Request) (Response, error) {
		if healthy.Load() {
			return Response{ID: "ok"}, nil
		}
		return Response{}, errBoom
	}}
	c := breakerOver(v, clk, 2, time.Minute)
	c.Do(context.Background(), req)
	c.Do(context.Background(), req) // open now
	opened := v.count()

	clk.advance(time.Minute - time.Nanosecond)
	if _, err := c.Do(context.Background(), req); !errors.Is(err, ErrOpen) {
		t.Fatalf("1ns before the cooldown ends: err = %v, want ErrOpen", err)
	}
	clk.advance(time.Nanosecond) // exactly Cooldown
	if _, err := c.Do(context.Background(), req); !errors.Is(err, errBoom) {
		t.Fatalf("at the cooldown boundary the probe should reach the vendor and fail with errBoom, got %v", err)
	}
	if v.count() != opened+1 {
		t.Fatalf("probe made %d vendor calls, want 1", v.count()-opened)
	}

	// A failed probe reopens the circuit for a FULL new cooldown.
	if _, err := c.Do(context.Background(), req); !errors.Is(err, ErrOpen) {
		t.Fatalf("right after a failed probe: err = %v, want ErrOpen", err)
	}
	clk.advance(time.Minute - time.Nanosecond)
	if _, err := c.Do(context.Background(), req); !errors.Is(err, ErrOpen) {
		t.Fatalf("cooldown must restart from the failed probe: err = %v, want ErrOpen", err)
	}

	healthy.Store(true)
	clk.advance(time.Nanosecond)
	if resp, err := c.Do(context.Background(), req); err != nil || resp.ID != "ok" {
		t.Fatalf("second probe = %+v, %v; want success", resp, err)
	}
	// A successful probe closes the circuit: traffic flows again.
	before := v.count()
	for i := 0; i < 3; i++ {
		if _, err := c.Do(context.Background(), req); err != nil {
			t.Fatalf("after recovery call %d: %v", i, err)
		}
	}
	if v.count() != before+3 {
		t.Errorf("recovered circuit forwarded %d of 3 calls", v.count()-before)
	}
}

func TestBreaker_HalfOpenLetsExactlyOneProbeThroughUnderConcurrency(t *testing.T) {
	clk := newClock()
	hold := make(chan struct{})
	entered := make(chan struct{}, 64)
	v := &vendor{fn: func(n int, _ context.Context, _ Request) (Response, error) {
		if n == 1 {
			return Response{}, errBoom // trips the breaker (threshold 1)
		}
		entered <- struct{}{}
		<-hold
		return Response{ID: "probe-ok"}, nil
	}}
	c := breakerOver(v, clk, 1, time.Minute)
	c.Do(context.Background(), req)
	clk.advance(time.Minute)

	const n = 32
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { _, err := c.Do(context.Background(), req); results <- err }()
	}
	rejected := 0
	for rejected < n-1 {
		select {
		case err := <-results:
			if !errors.Is(err, ErrOpen) {
				t.Fatalf("a non-probe call returned %v, want ErrOpen", err)
			}
			rejected++
		case <-time.After(testTimeout):
			t.Fatalf("only %d of %d callers were rejected while the probe was in flight: more than one probe got through", rejected, n-1)
		}
	}
	select {
	case <-entered:
	case <-time.After(testTimeout):
		t.Fatal("the probe never reached the vendor")
	}
	close(hold)
	select {
	case err := <-results:
		if err != nil {
			t.Errorf("probe result = %v, want success", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("probe never returned")
	}
	if got := v.count(); got != 2 {
		t.Errorf("vendor calls = %d, want 2 (the tripping call and one probe)", got)
	}
}

func TestBreaker_CallerCancellationIsNotAVendorFailure(t *testing.T) {
	v := &vendor{fn: func(n int, ctx context.Context, _ Request) (Response, error) {
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}
		return Response{}, errBoom
	}}
	c := breakerOver(v, newClock(), 2, time.Minute)
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c.Do(ctx, req)
	}
	if v.count() != 5 {
		t.Fatalf("vendor calls = %d, want 5: the circuit opened because callers hung up", v.count())
	}
	// Two genuine failures are still needed to open it.
	c.Do(context.Background(), req)
	if _, err := c.Do(context.Background(), req); !errors.Is(err, errBoom) {
		t.Fatalf("second genuine failure: err = %v, want errBoom", err)
	}
	if _, err := c.Do(context.Background(), req); !errors.Is(err, ErrOpen) {
		t.Errorf("after two genuine failures: err = %v, want ErrOpen", err)
	}
}

func TestBreaker_CancelledProbeDoesNotWedgeTheCircuit(t *testing.T) {
	clk := newClock()
	v := &vendor{fn: func(n int, ctx context.Context, _ Request) (Response, error) {
		switch {
		case n == 1:
			return Response{}, errBoom
		case ctx.Err() != nil:
			return Response{}, ctx.Err()
		}
		return Response{ID: "ok"}, nil
	}}
	c := breakerOver(v, clk, 1, time.Minute)
	c.Do(context.Background(), req)
	clk.advance(time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Do(ctx, req) // the probe, abandoned by its caller
	if resp, err := c.Do(context.Background(), req); err != nil || resp.ID != "ok" {
		t.Errorf("next call = %+v, %v; a cancelled probe must release the probe slot so a later call can probe", resp, err)
	}
}

func TestBreaker_LateSuccessFromBeforeTheTripDoesNotCloseTheCircuit(t *testing.T) {
	clk := newClock()
	hold := make(chan struct{})
	started := make(chan struct{})
	v := &vendor{fn: func(n int, _ context.Context, _ Request) (Response, error) {
		if n == 1 {
			close(started)
			<-hold // a slow call admitted while the circuit was still closed
			return Response{ID: "late-ok"}, nil
		}
		return Response{}, errBoom
	}}
	c := breakerOver(v, clk, 2, time.Minute)
	slow := make(chan error, 1)
	go func() { _, err := c.Do(context.Background(), req); slow <- err }()
	select {
	case <-started:
	case err := <-slow:
		t.Fatalf("slow call returned %v without reaching the vendor", err)
	case <-time.After(testTimeout):
		t.Fatal("slow call never reached the vendor")
	}
	c.Do(context.Background(), req)
	c.Do(context.Background(), req) // two failures: open
	close(hold)
	if err := <-slow; err != nil {
		t.Fatalf("slow call = %v", err)
	}
	if _, err := c.Do(context.Background(), req); !errors.Is(err, ErrOpen) {
		t.Errorf("err = %v, want ErrOpen: only a probe after the cooldown may close the circuit", err)
	}
}

func TestBreaker_ConcurrentCallsUnderRace(t *testing.T) {
	var n atomic.Int64
	v := &vendor{fn: func(int, context.Context, Request) (Response, error) {
		if n.Add(1)%3 == 0 {
			return Response{}, errBoom
		}
		return Response{ID: "ok"}, nil
	}}
	c := breakerOver(v, newClock(), 1_000_000, time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				c.Do(context.Background(), req)
			}
		}()
	}
	wg.Wait()
	if v.count() != 16*200 {
		t.Errorf("vendor calls = %d, want %d: a closed circuit forwards everything", v.count(), 16*200)
	}
}

// ---- Timeout and logging ---------------------------------------------------------

func TestTimeout_GivesEachCallItsOwnDeadlineAndReleasesIt(t *testing.T) {
	var seen context.Context
	v := &vendor{fn: func(_ int, ctx context.Context, _ Request) (Response, error) {
		seen = ctx
		return Response{ID: "x"}, nil
	}}
	if _, err := Chain(v, WithTimeout(50*time.Millisecond)).Do(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if seen == nil {
		t.Fatal("the wrapped client was never called")
	}
	dl, ok := seen.Deadline()
	if !ok || time.Until(dl) > 50*time.Millisecond {
		t.Errorf("inner ctx deadline = %v (ok=%v), want within 50ms", dl, ok)
	}
	if seen.Err() == nil {
		t.Error("the per-call context was not cancelled when Do returned: call cancel() (go vet's lostcancel catches the missing call)")
	}
}

func TestTimeout_NeverExtendsTheCallersDeadline(t *testing.T) {
	var seen context.Context
	v := &vendor{fn: func(_ int, ctx context.Context, _ Request) (Response, error) { seen = ctx; return Response{}, nil }}
	parent, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	Chain(v, WithTimeout(time.Hour)).Do(parent, req)
	if seen == nil {
		t.Fatal("the wrapped client was never called")
	}
	dl, _ := seen.Deadline()
	pdl, _ := parent.Deadline()
	if dl.After(pdl) {
		t.Errorf("inner deadline %v is later than the caller's %v", dl, pdl)
	}
}

func TestLogging_OneEntryPerCallWithDurationAndError(t *testing.T) {
	clk := newClock()
	v := &vendor{fn: func(n int, _ context.Context, _ Request) (Response, error) {
		clk.advance(30 * time.Millisecond)
		if n == 2 {
			return Response{}, errBoom
		}
		return Response{ID: "ok"}, nil
	}}
	var entries []Entry
	c := Chain(v, WithLogging(func(e Entry) { entries = append(entries, e) }, clk.now))
	c.Do(context.Background(), Request{Key: "a"})
	c.Do(context.Background(), Request{Key: "b"})
	if len(entries) != 2 {
		t.Fatalf("%d log entries, want 2", len(entries))
	}
	if entries[0].Key != "a" || entries[0].Duration != 30*time.Millisecond || entries[0].Err != nil {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].Key != "b" || !errors.Is(entries[1].Err, errBoom) {
		t.Errorf("entry 1 = %+v, want key b with errBoom", entries[1])
	}
	if _, err := Chain(alwaysOK(), WithLogging(nil, nil)).Do(context.Background(), req); err != nil {
		t.Errorf("a nil log func must be a harmless no-op, got %v", err)
	}
}

// ---- Order is a design decision --------------------------------------------------

func TestOrder_RetryOutsideBreaker_OneLogicalCallTripsTheBreakerAndLaterCallsAreShed(t *testing.T) {
	v := alwaysFail()
	retry := WithRetry(RetryConfig{MaxAttempts: 3, Sleep: (&sleeper{}).sleep})
	br := WithBreaker(BreakerConfig{Threshold: 3, Cooldown: time.Minute, Now: newClock().now})
	c := Chain(v, retry, br)

	if _, err := c.Do(context.Background(), req); !errors.Is(err, errBoom) {
		t.Fatalf("first call: %v", err)
	}
	if v.count() != 3 {
		t.Fatalf("first logical call made %d vendor calls, want 3 (each attempt passes through the breaker)", v.count())
	}
	if _, err := c.Do(context.Background(), req); !errors.Is(err, ErrOpen) {
		t.Errorf("second call: %v, want ErrOpen", err)
	}
	if v.count() != 3 {
		t.Errorf("vendor calls = %d after the breaker opened, want still 3: retry must not hammer an open circuit", v.count())
	}
}

func TestOrder_BreakerOutsideRetry_EachLogicalCallCostsAllAttemptsButCountsAsOneFailure(t *testing.T) {
	v := alwaysFail()
	retry := WithRetry(RetryConfig{MaxAttempts: 3, Sleep: (&sleeper{}).sleep})
	br := WithBreaker(BreakerConfig{Threshold: 3, Cooldown: time.Minute, Now: newClock().now})
	c := Chain(v, br, retry)

	for i := 1; i <= 3; i++ {
		if _, err := c.Do(context.Background(), req); !errors.Is(err, errBoom) {
			t.Fatalf("logical call %d: %v", i, err)
		}
	}
	if v.count() != 9 {
		t.Errorf("vendor calls = %d, want 9: 3 logical calls x 3 attempts before the breaker sees 3 failures", v.count())
	}
	if _, err := c.Do(context.Background(), req); !errors.Is(err, ErrOpen) {
		t.Errorf("4th call: %v, want ErrOpen", err)
	}
}

func TestOrder_LoggingOutsideRetryLogsOncePerLogicalCallInsideLogsEveryAttempt(t *testing.T) {
	count := func(order func(log Decorator, retry Decorator) []Decorator) int {
		n := 0
		log := WithLogging(func(Entry) { n++ }, nil)
		retry := WithRetry(RetryConfig{MaxAttempts: 3, Sleep: (&sleeper{}).sleep})
		Chain(alwaysFail(), order(log, retry)...).Do(context.Background(), req)
		return n
	}
	if got := count(func(l, r Decorator) []Decorator { return []Decorator{l, r} }); got != 1 {
		t.Errorf("logging outside retry wrote %d entries for one logical call, want 1", got)
	}
	if got := count(func(l, r Decorator) []Decorator { return []Decorator{r, l} }); got != 3 {
		t.Errorf("logging inside retry wrote %d entries, want 3 (one per attempt)", got)
	}
}
