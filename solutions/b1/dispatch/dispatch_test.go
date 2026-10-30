package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// ---- helpers ---------------------------------------------------------------------

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

func ev(id, typ string) Event { return Event{ID: id, Type: typ, Tenant: "cu-1", Payload: []byte("{}")} }

// sleeper records requested delays and returns immediately (no real waiting).
type sleeper struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (s *sleeper) Sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.delays = append(s.delays, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *sleeper) got() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.delays...)
}

// counting sink: records calls, optionally fails the first n with errFor(call).
type countingSink struct {
	calls atomic.Int32
	fn    func(call int, ctx context.Context, e Event) error
}

func (c *countingSink) Deliver(ctx context.Context, e Event) error {
	n := int(c.calls.Add(1))
	if c.fn == nil {
		return nil
	}
	return c.fn(n, ctx, e)
}

func newD(t *testing.T, opts ...Option) *Dispatcher {
	t.Helper()
	d, err := New(opts...)
	if err != nil || d == nil {
		t.Fatalf("New() = %v, %v; want a Dispatcher (is New still returning errNotImplemented?)", d, err)
	}
	return d
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var errVendor503 = errors.New("vendor returned 503")

// ---- pure functions ----------------------------------------------------------------

func TestMatch_Table(t *testing.T) {
	cases := []struct {
		pattern, typ string
		want         bool
	}{
		{"*", "anything.at.all", true},
		{"payment.settled", "payment.settled", true},
		{"payment.settled", "payment.settled.late", false},
		{"payment.settled", "payment.failed", false},
		{"payment.*", "payment.settled", true},
		{"payment.*", "payment.settled.late", true},
		{"payment.*", "payment", false},
		{"payment.*", "payment.", false},
		{"payment.*", "payments.settled", false},
		{"payment.*", "refund.payment.settled", false},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.typ); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.typ, got, c.want)
		}
	}
}

type netLike struct{}

func (netLike) Error() string   { return "i/o hiccup" }
func (netLike) Temporary() bool { return true }

type notTemp struct{}

func (notTemp) Error() string   { return "temporary failure in name resolution (but not marked)" }
func (notTemp) Temporary() bool { return false }

func TestIsTemporary_UsesTypesNotText(t *testing.T) {
	if Temporary(nil) != nil {
		t.Error("Temporary(nil) must be nil")
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("timeout"), false}, // the word 'timeout' means nothing
		{"marked", Temporary(errVendor503), true},
		{"wrapped marked", fmt.Errorf("vendor x: %w", Temporary(errVendor503)), true},
		{"joined marked", errors.Join(errors.New("a"), Temporary(errVendor503)), true},
		{"custom Temporary() true", netLike{}, true},
		{"custom Temporary() false", notTemp{}, false},
		// The standard library's DeadlineExceeded implements Temporary() == true
		// (and Timeout()); Canceled does not. Dispatch still never retries once
		// the caller's own ctx is done.
		{"context deadline", context.DeadlineExceeded, true},
		{"context canceled", context.Canceled, false},
	}
	for _, c := range cases {
		if got := IsTemporary(c.err); got != c.want {
			t.Errorf("%s: IsTemporary = %v, want %v", c.name, got, c.want)
		}
	}
	if err := Temporary(errVendor503); !errors.Is(err, errVendor503) || err.Error() != errVendor503.Error() {
		t.Errorf("Temporary must keep the cause and its message; got %q", err)
	}
}

// ---- construction & registration -------------------------------------------------------

func TestNew_RejectsBadOptions(t *testing.T) {
	cases := map[string]Option{
		"WithMaxAttempts(0)":     WithMaxAttempts(0),
		"WithBackoff(nil)":       WithBackoff(nil),
		"WithSleep(nil)":         WithSleep(nil),
		"WithClock(nil)":         WithClock(nil),
		"WithAttemptTimeout(<0)": WithAttemptTimeout(-time.Second),
		"nil option":             nil,
	}
	for name, o := range cases {
		if _, err := New(o); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("New(%s) error = %v, want ErrInvalidConfig", name, err)
		}
	}
	if _, err := New(WithAttemptTimeout(0)); err != nil {
		t.Errorf("WithAttemptTimeout(0) means 'no extra bound' and must be accepted, got %v", err)
	}
}

func TestRegister_Validation(t *testing.T) {
	d := newD(t)
	ok := SinkFunc(func(context.Context, Event) error { return nil })
	for _, c := range []struct {
		name string
		sink Sink
		pats []string
	}{
		{"", ok, nil},
		{"nil-sink", nil, nil},
		{"empty-pattern", ok, []string{""}},
		{"star-in-middle", ok, []string{"pay*ment"}},
		{"leading-star", ok, []string{"*.settled"}},
		{"star-mid-segment", ok, []string{"a.*.b"}},
		{"bare-dot-star", ok, []string{".*"}},
		{"space", ok, []string{"a b"}},
		{"good-and-bad", ok, []string{"a.b", "x*"}},
	} {
		if err := d.Register(c.name, c.sink, c.pats...); !errors.Is(err, ErrInvalidSink) {
			t.Errorf("Register(%q, sink=%v, %q) = %v, want ErrInvalidSink", c.name, c.sink != nil, c.pats, err)
		}
	}
	must(t, d.Register("audit", ok))
	if err := d.Register("audit", ok); !errors.Is(err, ErrDuplicateSink) {
		t.Errorf("second Register(audit) = %v, want ErrDuplicateSink", err)
	}
	if len(d.Stats()) != 1 {
		t.Errorf("rejected registrations must leave no trace; Stats() = %v", d.Stats())
	}
}

func TestZeroValueDispatcherIsUsable(t *testing.T) {
	var d Dispatcher // no New: like sync.Mutex or bytes.Buffer
	var got atomic.Int32
	must(t, d.Register("a", SinkFunc(func(context.Context, Event) error { got.Add(1); return nil })))
	if err := d.Dispatch(ctxT(t), ev("e1", "x.y")); err != nil {
		t.Fatalf("Dispatch on a zero Dispatcher: %v", err)
	}
	if got.Load() != 1 {
		t.Fatalf("sink ran %d times, want 1", got.Load())
	}
	if st := d.Stats()["a"]; st.Delivered != 1 || st.Attempts != 1 || st.LastSuccess.IsZero() {
		t.Fatalf("stats = %+v, want 1 delivered, 1 attempt and a LastSuccess from the default clock", st)
	}
}

func TestSinkFunc_ImplementsSink(t *testing.T) {
	var s Sink = SinkFunc(func(_ context.Context, e Event) error {
		if e.ID != "x" {
			return errors.New("wrong event")
		}
		return nil
	})
	if err := s.Deliver(context.Background(), ev("x", "t")); err != nil {
		t.Fatal(err)
	}
	if err := s.Deliver(context.Background(), ev("y", "t")); err == nil {
		t.Fatal("SinkFunc must pass the function's error through")
	}
}

// ---- routing ----------------------------------------------------------------------

func TestDispatch_RoutesByPattern(t *testing.T) {
	d := newD(t)
	sinks := map[string]*countingSink{"audit": {}, "payments": {}, "settled": {}, "users": {}}
	must(t, d.Register("audit", sinks["audit"]))                                // no patterns = everything
	must(t, d.Register("payments", sinks["payments"], "payment.*", "refund.*")) // several patterns
	must(t, d.Register("settled", sinks["settled"], "payment.settled"))         // exact
	must(t, d.Register("users", sinks["users"], "user.*"))
	must(t, d.Dispatch(ctxT(t), ev("e1", "payment.settled")))
	must(t, d.Dispatch(ctxT(t), ev("e2", "payment.failed")))
	must(t, d.Dispatch(ctxT(t), ev("e3", "user.login")))
	want := map[string]int32{"audit": 3, "payments": 2, "settled": 1, "users": 1}
	for name, w := range want {
		if got := sinks[name].calls.Load(); got != w {
			t.Errorf("sink %q received %d events, want %d", name, got, w)
		}
	}
}

func TestDispatch_NoRouteAndInvalidEvent(t *testing.T) {
	d := newD(t)
	s := &countingSink{}
	must(t, d.Register("users", s, "user.*"))

	err := d.Dispatch(ctxT(t), ev("evt-9", "payment.settled"))
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("error = %v, want ErrNoRoute", err)
	}
	if !strings.Contains(err.Error(), "evt-9") || !strings.Contains(err.Error(), "payment.settled") {
		t.Errorf("error %q must name the event id and type, or on-call cannot find it", err)
	}
	for _, bad := range []Event{{Type: "a.b"}, {ID: "x"}, {}} {
		if err := d.Dispatch(ctxT(t), bad); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("Dispatch(%+v) = %v, want ErrInvalidEvent", bad, err)
		}
	}
	if s.calls.Load() != 0 {
		t.Error("a sink received an event that should have been rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	must(t, d.Register("all", s))
	if err := d.Dispatch(ctx, ev("e", "user.x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Dispatch with a cancelled ctx = %v, want context.Canceled", err)
	}
	if s.calls.Load() != 0 {
		t.Error("delivered an event although ctx was already cancelled")
	}
}

func TestDispatch_DeliversToMatchingSinksConcurrently(t *testing.T) {
	d := newD(t)
	const n = 3
	var arrived sync.WaitGroup
	arrived.Add(n)
	release := make(chan struct{})
	go func() { arrived.Wait(); close(release) }() // opens only when all n sinks are inside Deliver at once
	for i := 0; i < n; i++ {
		must(t, d.Register(fmt.Sprintf("s%d", i), SinkFunc(func(ctx context.Context, e Event) error {
			arrived.Done()
			select {
			case <-release:
				return nil
			case <-time.After(testTimeout):
				return errors.New("never saw the other sinks: they are being called one after another")
			}
		})))
	}
	if err := d.Dispatch(ctxT(t), ev("e", "t.x")); err != nil {
		t.Fatalf("Dispatch: %v (sinks must run concurrently so one slow sink does not delay the rest)", err)
	}
}

// ---- retries ------------------------------------------------------------------------

func TestDispatch_RetriesTemporaryFailuresWithDefaultBackoff(t *testing.T) {
	sl := &sleeper{}
	d := newD(t, WithSleep(sl.Sleep), WithClock(func() time.Time { return time.Unix(1_700_000_000, 0) }))
	s := &countingSink{fn: func(call int, _ context.Context, _ Event) error {
		if call < 3 {
			return Temporary(errVendor503)
		}
		return nil
	}}
	must(t, d.Register("webhook", s))
	if err := d.Dispatch(ctxT(t), ev("e1", "t.x")); err != nil {
		t.Fatalf("Dispatch = %v; the third attempt succeeds so the caller should see nil", err)
	}
	if got := sl.got(); len(got) != 2 || got[0] != 100*time.Millisecond || got[1] != 200*time.Millisecond {
		t.Errorf("backoff delays = %v, want [100ms 200ms] (the legacy schedule, now injectable)", got)
	}
	st := d.Stats()["webhook"]
	if st.Delivered != 1 || st.Failed != 0 || st.Attempts != 3 || !st.LastSuccess.Equal(time.Unix(1_700_000_000, 0)) {
		t.Errorf("stats = %+v, want Delivered 1, Failed 0, Attempts 3, LastSuccess from the injected clock", st)
	}
}

func TestDispatch_BackoffAndMaxAttemptsAreConfigurable(t *testing.T) {
	sl := &sleeper{}
	d := newD(t, WithSleep(sl.Sleep), WithMaxAttempts(4), WithBackoff(func(a int) time.Duration { return time.Duration(a) * time.Second }))
	s := &countingSink{fn: func(int, context.Context, Event) error { return Temporary(errVendor503) }}
	must(t, d.Register("webhook", s))

	err := d.Dispatch(ctxT(t), ev("evt-77", "t.x"))
	if got := s.calls.Load(); got != 4 {
		t.Errorf("sink called %d times, want 4 (WithMaxAttempts(4))", got)
	}
	if got := sl.got(); len(got) != 3 || got[0] != time.Second || got[2] != 3*time.Second {
		t.Errorf("delays = %v, want [1s 2s 3s]: no sleep after the final attempt", got)
	}
	var de *DeliveryError
	if !errors.As(err, &de) {
		t.Fatalf("error %v: errors.As(*DeliveryError) failed", err)
	}
	if de.Sink != "webhook" || de.EventID != "evt-77" || de.Attempts != 4 {
		t.Errorf("DeliveryError = %+v, want Sink webhook, EventID evt-77, Attempts 4", de)
	}
	if !errors.Is(err, errVendor503) {
		t.Errorf("errors.Is(err, errVendor503) = false: the sink's own error must stay reachable through the wrappers")
	}
	for _, part := range []string{`"webhook"`, `"evt-77"`, "4 attempt", "503"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("message %q lacks %s; an operator should not need the code to understand it", err, part)
		}
	}
	if st := d.Stats()["webhook"]; st.Failed != 1 || st.Delivered != 0 || st.Attempts != 4 || !st.LastSuccess.IsZero() {
		t.Errorf("stats = %+v, want Failed 1, Delivered 0, Attempts 4, no LastSuccess", st)
	}
}

func TestDispatch_PermanentErrorsAreNeverRetried_EvenIfTheTextSaysTimeout(t *testing.T) {
	sl := &sleeper{}
	d := newD(t, WithSleep(sl.Sleep))
	s := &countingSink{fn: func(int, context.Context, Event) error { return errors.New("gateway timeout: 400 bad request") }}
	must(t, d.Register("webhook", s))
	err := d.Dispatch(ctxT(t), ev("e1", "t.x"))
	var de *DeliveryError
	if !errors.As(err, &de) || de.Attempts != 1 || s.calls.Load() != 1 || len(sl.got()) != 0 {
		t.Fatalf("err=%v calls=%d sleeps=%v; an unmarked error is permanent: one attempt, no sleeping (the legacy code matched the word 'timeout')", err, s.calls.Load(), sl.got())
	}
}

func TestDispatch_RetriesTemporaryErrorsWhateverTheirTextOrWrapping(t *testing.T) {
	sl := &sleeper{}
	d := newD(t, WithSleep(sl.Sleep))
	s := &countingSink{fn: func(call int, _ context.Context, _ Event) error {
		switch call {
		case 1:
			return fmt.Errorf("adapter acme: %w", fmt.Errorf("round trip: %w", Temporary(errors.New("EOF"))))
		case 2:
			return netLike{}
		}
		return nil
	}}
	must(t, d.Register("webhook", s))
	if err := d.Dispatch(ctxT(t), ev("e1", "t.x")); err != nil || s.calls.Load() != 3 {
		t.Fatalf("err=%v calls=%d; want nil after 3 calls (retry decisions must walk the error chain)", err, s.calls.Load())
	}
}

func TestDispatch_AttemptTimeoutCutsOffHungSinksAndIsRetried(t *testing.T) {
	sl := &sleeper{}
	d := newD(t, WithSleep(sl.Sleep), WithAttemptTimeout(20*time.Millisecond))
	var deadlines atomic.Int32
	s := &countingSink{fn: func(call int, ctx context.Context, _ Event) error {
		if _, ok := ctx.Deadline(); ok {
			deadlines.Add(1)
		}
		if call < 3 {
			<-ctx.Done() // a hung vendor: only the attempt timeout ends this call
			return ctx.Err()
		}
		return nil
	}}
	must(t, d.Register("webhook", s))
	start := time.Now()
	if err := d.Dispatch(ctxT(t), ev("e1", "t.x")); err != nil {
		t.Fatalf("Dispatch = %v, want nil on the third attempt", err)
	}
	if deadlines.Load() != 3 {
		t.Errorf("sink saw a deadline on %d of 3 calls; every attempt needs its own timeout", deadlines.Load())
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v for two 20ms timeouts", time.Since(start))
	}
}

func TestDispatch_ParentDeadlineIsNotRetried(t *testing.T) {
	d := newD(t, WithSleep((&sleeper{}).Sleep), WithAttemptTimeout(time.Hour))
	s := &countingSink{fn: func(_ int, ctx context.Context, _ Event) error { <-ctx.Done(); return ctx.Err() }}
	must(t, d.Register("webhook", s))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := d.Dispatch(ctx, ev("e1", "t.x"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if s.calls.Load() != 1 {
		t.Errorf("sink called %d times; when the caller's own deadline has passed, retrying is pure load", s.calls.Load())
	}
}

func TestDispatch_CancellationDuringBackoffStopsRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := newD(t, WithSleep(func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }))
	s := &countingSink{fn: func(int, context.Context, Event) error { return Temporary(errVendor503) }}
	must(t, d.Register("webhook", s))
	err := d.Dispatch(ctx, ev("e1", "t.x"))
	if !errors.Is(err, context.Canceled) || !errors.Is(err, errVendor503) {
		t.Fatalf("error = %v; want BOTH context.Canceled and the sink's last error reachable with errors.Is", err)
	}
	var de *DeliveryError
	if !errors.As(err, &de) || de.Attempts != 1 || s.calls.Load() != 1 {
		t.Fatalf("de=%+v calls=%d; want one attempt then stop", de, s.calls.Load())
	}
}

// ---- failure isolation & error shape --------------------------------------------------

func TestDispatch_PanickingSinkIsContainedAndNotRetried(t *testing.T) {
	d := newD(t, WithSleep((&sleeper{}).Sleep))
	bad := &countingSink{fn: func(int, context.Context, Event) error { panic("boom: nil map in adapter") }}
	good := &countingSink{}
	must(t, d.Register("bad", bad))
	must(t, d.Register("good", good))
	err := d.Dispatch(ctxT(t), ev("e1", "t.x"))
	if !errors.Is(err, ErrSinkPanic) {
		t.Fatalf("error = %v, want ErrSinkPanic", err)
	}
	var de *DeliveryError
	if !errors.As(err, &de) || de.Sink != "bad" || de.Attempts != 1 {
		t.Fatalf("DeliveryError = %+v, want Sink bad with 1 attempt (panics are not retried)", de)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("message %q should include the panic value", err)
	}
	if good.calls.Load() != 1 || d.Stats()["good"].Delivered != 1 {
		t.Error("one sink's panic must not stop delivery to the others")
	}
}

func TestDispatch_ReportsEveryFailureAsAJoinedError(t *testing.T) {
	d := newD(t)
	errA, errB := errors.New("a is down"), errors.New("b rejects us")
	must(t, d.Register("zeta", SinkFunc(func(context.Context, Event) error { return errB })))
	must(t, d.Register("alpha", SinkFunc(func(context.Context, Event) error { return errA })))
	must(t, d.Register("fine", &countingSink{}))
	err := d.Dispatch(ctxT(t), ev("e1", "t.x"))
	j, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("error %T does not expose Unwrap() []error: return errors.Join(...) so callers can list every failed sink", err)
	}
	parts := j.Unwrap()
	if len(parts) != 2 {
		t.Fatalf("joined %d errors, want 2 (one per failed sink; the healthy sink contributes none)", len(parts))
	}
	var first, second *DeliveryError
	if !errors.As(parts[0], &first) || !errors.As(parts[1], &second) || first.Sink != "alpha" || second.Sink != "zeta" {
		t.Fatalf("parts = %v; want *DeliveryError for alpha then zeta (sorted by sink name, for deterministic output)", parts)
	}
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Error("errors.Is must reach both causes through the join")
	}
}

func TestDispatch_SinkMayRegisterAnotherSinkWhileDelivering(t *testing.T) {
	d := newD(t)
	late := &countingSink{}
	var once sync.Once
	must(t, d.Register("self-extending", SinkFunc(func(context.Context, Event) error {
		var err error
		once.Do(func() { err = d.Register("late", late) }) // would deadlock if Dispatch held the registry lock during Deliver
		return err
	})))
	done := make(chan error, 1)
	go func() { done <- d.Dispatch(context.Background(), ev("e1", "t.x")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Dispatch = %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Dispatch deadlocked: a lock is held while calling into a sink")
	}
	must(t, d.Dispatch(ctxT(t), ev("e2", "t.x")))
	if late.calls.Load() != 1 {
		t.Errorf("late sink calls = %d, want 1 (registered during e1, so it sees e2 only)", late.calls.Load())
	}
}

func TestStats_AreACopy(t *testing.T) {
	d := newD(t)
	must(t, d.Register("a", &countingSink{}))
	must(t, d.Dispatch(ctxT(t), ev("e1", "t.x")))
	m := d.Stats()
	m["a"] = SinkStats{Delivered: 999}
	delete(m, "a")
	if got := d.Stats()["a"].Delivered; got != 1 {
		t.Fatalf("Delivered = %d after the caller edited a returned map; Stats must return a copy", got)
	}
}

// ---- concurrency and leaks -----------------------------------------------------------------

func TestDispatcher_ConcurrentDispatchRegisterAndStats(t *testing.T) {
	d := newD(t, WithSleep((&sleeper{}).Sleep))
	counter := &countingSink{}
	must(t, d.Register("counter", counter, "load.*"))
	var seenMu sync.Mutex
	seen := map[string]int{}
	flaky := &countingSink{fn: func(_ int, _ context.Context, e Event) error {
		seenMu.Lock()
		seen[e.ID]++ // every event fails its first attempt, whatever the interleaving
		first := seen[e.ID] == 1
		seenMu.Unlock()
		if first {
			return Temporary(errVendor503)
		}
		return nil
	}}
	must(t, d.Register("flaky", flaky, "load.*"))

	const workers, per = 8, 40
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if err := d.Dispatch(ctxT(t), ev(fmt.Sprintf("w%d-%d", w, i), "load.test")); err != nil {
					t.Errorf("Dispatch: %v", err)
					return
				}
			}
		}()
	}
	wg.Add(2)
	go func() { // registrations racing with dispatches
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = d.Register(fmt.Sprintf("extra-%d", i), &countingSink{}, "other.*")
		}
	}()
	go func() { // readers racing with writers
		defer wg.Done()
		for i := 0; i < 200; i++ {
			for _, st := range d.Stats() {
				_ = st.Delivered
			}
		}
	}()
	wg.Wait()
	if got := counter.calls.Load(); got != workers*per {
		t.Errorf("counter saw %d events, want %d", got, workers*per)
	}
	if st := d.Stats()["counter"]; st.Delivered != workers*per || st.Attempts != workers*per {
		t.Errorf("counter stats = %+v, want %d delivered (lost updates mean the stats are unsynchronised)", st, workers*per)
	}
}

func TestDispatch_LeavesNoGoroutinesBehind(t *testing.T) {
	d := newD(t, WithSleep((&sleeper{}).Sleep))
	for i := 0; i < 5; i++ {
		must(t, d.Register(fmt.Sprintf("s%d", i), &countingSink{fn: func(call int, _ context.Context, _ Event) error {
			if call == 1 {
				return Temporary(errVendor503)
			}
			return nil
		}}))
	}
	baseline := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		_ = d.Dispatch(ctxT(t), ev(fmt.Sprintf("e%d", i), "t.x"))
	}
	deadline := time.Now().Add(testTimeout)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			var buf bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
			t.Fatalf("goroutine leak: %d running vs baseline %d.\n%s", runtime.NumGoroutine(), baseline, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- the "new requirement" test ----------------------------------------------------------------
//
// Product asks for (1) a chat sink with its own formatting and (2) "only deliver
// events for tenant cu-7, and never more than N per second" on the audit sink.
// Both are written below, in the TEST file, against the exported Sink
// interface only. If your Dispatcher needs an edit for either, its design has a
// hole where an extension point should be.

type chatSink struct {
	mu    sync.Mutex
	lines []string
}

func (c *chatSink) Deliver(_ context.Context, e Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf("[%s] %s (%d bytes)", e.Tenant, e.Type, len(e.Payload)))
	return nil
}

type tenantOnly struct {
	tenant string
	next   Sink
}

func (f tenantOnly) Deliver(ctx context.Context, e Event) error {
	if e.Tenant != f.tenant {
		return nil // filtered: not an error
	}
	return f.next.Deliver(ctx, e)
}

type budget struct {
	left atomic.Int32
	next Sink
}

var errBudget = errors.New("per-second budget exhausted")

func (b *budget) Deliver(ctx context.Context, e Event) error {
	if b.left.Add(-1) < 0 {
		return Temporary(errBudget)
	}
	return b.next.Deliver(ctx, e)
}

func TestExtension_NewSinkAndDecoratorsNeedNoDispatcherChanges(t *testing.T) {
	sl := &sleeper{}
	d := newD(t, WithSleep(sl.Sleep), WithMaxAttempts(2))
	chat := &chatSink{}
	audit := &countingSink{}
	b := &budget{next: audit}
	b.left.Store(1)

	must(t, d.Register("chat", chat, "payment.*"))
	must(t, d.Register("audit-cu7", tenantOnly{tenant: "cu-7", next: b}))

	e1 := Event{ID: "1", Type: "payment.settled", Tenant: "cu-7", Payload: []byte("abc")}
	e2 := Event{ID: "2", Type: "payment.failed", Tenant: "cu-1"}
	e3 := Event{ID: "3", Type: "payment.settled", Tenant: "cu-7"}

	must(t, d.Dispatch(ctxT(t), e1))
	must(t, d.Dispatch(ctxT(t), e2)) // chat gets it, the tenant filter swallows it
	err := d.Dispatch(ctxT(t), e3)   // budget exhausted: retried once, then reported

	if want := []string{"[cu-7] payment.settled (3 bytes)", "[cu-1] payment.failed (0 bytes)", "[cu-7] payment.settled (0 bytes)"}; fmt.Sprint(chat.lines) != fmt.Sprint(want) {
		t.Errorf("chat lines = %q, want %q", chat.lines, want)
	}
	if audit.calls.Load() != 1 {
		t.Errorf("audit saw %d events, want 1 (only cu-7's first, before the budget ran out)", audit.calls.Load())
	}
	var de *DeliveryError
	if !errors.As(err, &de) || de.Sink != "audit-cu7" || de.Attempts != 2 || !errors.Is(err, errBudget) {
		t.Errorf("err = %v; want audit-cu7 to fail after 2 attempts with errBudget reachable", err)
	}
	if len(sl.got()) != 1 {
		t.Errorf("backoff sleeps = %v, want exactly one (between attempt 1 and 2)", sl.got())
	}
}

// ---- the legacy oracle (fast paths only; the retry path sleeps for real) ---------------------------

func TestLegacy_StringlyTypedErrorsAndGlobalState(t *testing.T) {
	oldSinks, oldStats := LegacySinks, LegacyStats
	defer func() { LegacySinks, LegacyStats = oldSinks, oldStats }()
	LegacySinks = map[string]func(string) string{
		"ok":  func(string) string { return "" },
		"bad": func(string) string { return "400 bad request" },
	}
	LegacyStats = map[string]int{}
	if got := LegacyDispatch("nope", "t", "{}"); got != "error: no such sink: nope" {
		t.Fatalf("got %q: a caller can only learn what failed by parsing this string", got)
	}
	if LegacyDispatch("ok", "t", "{}") != "ok" || LegacyDispatch("bad", "t", "{}") != "error: 400 bad request" {
		t.Fatal("the legacy oracle changed")
	}
	if LegacyStats["ok:ok"] != 1 || LegacyStats["bad:fail"] != 1 {
		t.Fatalf("stats = %v; legacy keeps counters in a global, unlocked map keyed by string concatenation", LegacyStats)
	}
}
