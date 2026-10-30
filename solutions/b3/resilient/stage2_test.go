package resilient

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock for cooldowns.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: fixedNow} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type transition struct{ from, to State }

// recorder collects OnStateChange calls (they arrive serialised by the breaker).
type recorder struct{ got []transition }

func (r *recorder) on(from, to State) { r.got = append(r.got, transition{from, to}) }

func breakerCfg(clk *fakeClock) BreakerConfig {
	return BreakerConfig{ConsecutiveFailures: 3, Cooldown: 30 * time.Second, HalfOpenProbes: 1, Now: clk.Now}
}

// get sends one GET through rt, closes any response, and returns status or error.
func get(t *testing.T, rt http.RoundTripper) (int, error) {
	t.Helper()
	resp, err := send(t, rt, newReq(t, "GET", ""))
	if err != nil {
		return 0, err
	}
	code := resp.StatusCode
	closeResp(resp)
	return code, nil
}

func mustPass(t *testing.T, rt http.RoundTripper, want int) {
	t.Helper()
	code, err := get(t, rt)
	if err != nil || code != want {
		t.Fatalf("request = %d, %v; want %d (breaker should let it through)", code, err, want)
	}
}

func mustReject(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	resp, err := send(t, rt, newReq(t, "GET", ""))
	closeResp(resp)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("request err = %v; want ErrCircuitOpen", err)
	}
}

func TestStage2_OpensAfterConsecutiveFailures(t *testing.T) {
	clk := newClock()
	f := newFake(status(503))
	b := NewBreaker(breakerCfg(clk))
	rt := b.Wrap(f)
	for range 3 {
		mustPass(t, rt, 503)
	}
	if b.State() != Open {
		t.Fatalf("state = %v after 3 consecutive failures, want open", b.State())
	}
	before := f.count()
	mustReject(t, rt)
	if f.count() != before {
		t.Fatal("the vendor was called while the circuit was open; rejecting fast is the whole point")
	}
	if st := b.Stats(); st.Rejected != 1 || st.Opened != 1 {
		t.Fatalf("Stats = %+v, want Rejected 1, Opened 1", st)
	}
}

func TestStage2_SuccessResetsTheConsecutiveCount(t *testing.T) {
	clk := newClock()
	f := newFake(status(503), status(503), ok(), status(503), status(503), ok())
	b := NewBreaker(breakerCfg(clk))
	rt := b.Wrap(f)
	for _, want := range []int{503, 503, 200, 503, 503, 200} {
		mustPass(t, rt, want)
	}
	if b.State() != Closed {
		t.Fatalf("state = %v; failures separated by successes are not 'consecutive'", b.State())
	}
}

func TestStage2_OnlyVendorTroubleCountsAsFailure(t *testing.T) {
	clk := newClock()
	// Client errors (including 429) say nothing about vendor health.
	for _, code := range []int{400, 401, 404, 409, 422, 429} {
		b := NewBreaker(breakerCfg(clk))
		rt := b.Wrap(newFake(status(code)))
		for range 10 {
			mustPass(t, rt, code)
		}
		if b.State() != Closed {
			t.Fatalf("status %d opened the breaker; only 5xx and transport errors count", code)
		}
	}
	// 5xx and transport errors (timeouts included) do.
	for name, s := range map[string]step{
		"500": status(500), "502": status(502), "504": status(504),
		"network error": fail(errBoom), "attempt timeout": fail(context.DeadlineExceeded),
	} {
		b := NewBreaker(breakerCfg(clk))
		rt := b.Wrap(newFake(s))
		for range 3 {
			resp, _ := send(t, rt, newReq(t, "GET", ""))
			closeResp(resp)
		}
		if b.State() != Open {
			t.Fatalf("%s did not count as a failure: state %v after 3, want open", name, b.State())
		}
	}
}

func TestStage2_CallerCancellationIsNotAVendorFailure(t *testing.T) {
	clk := newClock()
	b := NewBreaker(breakerCfg(clk))
	f := newFake()
	f.handler = func(n int, req *http.Request) (*http.Response, error) { return nil, req.Context().Err() }
	rt := b.Wrap(f)
	for range 10 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := send(t, rt, newReqCtx(t, ctx, "GET", ""))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled passed through", err)
		}
	}
	if b.State() != Closed {
		t.Fatal("members closing the app opened the breaker: cancellations by the caller must be ignored")
	}
}

func TestStage2_OpensOnFailureRatioInARollingWindow(t *testing.T) {
	clk := newClock()
	cfg := BreakerConfig{Window: 4, FailureRatio: 0.75, Cooldown: time.Minute, Now: clk.Now}

	// Not enough outcomes yet: two failures and a success do not open it (the window holds 4)...
	b := NewBreaker(cfg)
	rt := b.Wrap(newFake(status(503), status(503), ok(), status(503)))
	mustPass(t, rt, 503)
	mustPass(t, rt, 503)
	mustPass(t, rt, 200)
	if b.State() != Closed {
		t.Fatal("opened after only 3 outcomes; the window needs 4 before the ratio applies")
	}
	mustPass(t, rt, 503) // ...the 4th outcome makes it 3 failures of 4 = 0.75, which does.
	if b.State() != Open {
		t.Fatalf("state = %v with 3 failures out of the last 4, want open", b.State())
	}

	// Rolling, not tumbling: S S S S F F F -> the last 4 are S F F F = 0.75 at the 7th outcome.
	b = NewBreaker(cfg)
	rt = b.Wrap(newFake(ok(), ok(), ok(), ok(), status(503), status(503), status(503)))
	for i := range 7 {
		want := 200
		if i >= 4 {
			want = 503
		}
		mustPass(t, rt, want)
	}
	if b.State() != Open {
		t.Fatalf("state = %v; the window must slide over the last 4 outcomes, not reset every 4", b.State())
	}

	// Alternating success/failure never reaches 0.75.
	b = NewBreaker(cfg)
	alt := newFake()
	alt.handler = func(n int, req *http.Request) (*http.Response, error) {
		if n%2 == 1 {
			return alt.respond(req, ok())
		}
		return alt.respond(req, status(503))
	}
	rt = b.Wrap(alt)
	for i := range 20 {
		want := 200
		if i%2 == 1 {
			want = 503
		}
		mustPass(t, rt, want)
	}
	if b.State() != Closed {
		t.Fatal("a 50% failure rate opened a breaker configured for 75%")
	}
}

func TestStage2_RejectsUntilCooldownThenProbes(t *testing.T) {
	clk := newClock()
	f := newFake(status(503), status(503), status(503), ok())
	b := NewBreaker(breakerCfg(clk))
	rt := b.Wrap(f)
	for range 3 {
		mustPass(t, rt, 503)
	}
	clk.Advance(29 * time.Second)
	mustReject(t, rt)
	clk.Advance(time.Second) // exactly the 30s cooldown
	mustPass(t, rt, 200)     // the probe
	if b.State() != Closed {
		t.Fatalf("state = %v after a successful probe, want closed", b.State())
	}
	mustPass(t, rt, 200)
}

func TestStage2_FailedProbeReopensWithAFreshCooldown(t *testing.T) {
	clk := newClock()
	f := newFake(status(503))
	b := NewBreaker(breakerCfg(clk))
	rt := b.Wrap(f)
	for range 3 {
		mustPass(t, rt, 503)
	}
	clk.Advance(30 * time.Second)
	mustPass(t, rt, 503) // probe fails
	if b.State() != Open {
		t.Fatalf("state = %v after a failed probe, want open", b.State())
	}
	clk.Advance(29 * time.Second) // the cooldown restarted at the failed probe
	mustReject(t, rt)
	clk.Advance(time.Second)
	n := f.count()
	mustPass(t, rt, 503) // second probe
	if f.count() != n+1 {
		t.Fatal("the second probe did not reach the vendor")
	}
}

func TestStage2_HalfOpenAdmitsExactlyKProbesAndRejectsTheRest(t *testing.T) {
	clk := newClock()
	cfg := breakerCfg(clk)
	cfg.HalfOpenProbes = 2
	b := NewBreaker(cfg)

	gate := make(chan struct{})
	f := newFake()
	f.handler = func(n int, req *http.Request) (*http.Response, error) {
		if n <= 3 {
			return f.respond(req, status(503))
		}
		<-gate // the probes hang until released
		return f.respond(req, ok())
	}
	rt := b.Wrap(f)
	for range 3 {
		mustPass(t, rt, 503)
	}
	clk.Advance(30 * time.Second)

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, err := get(t, rt)
			if err != nil || code != 200 {
				t.Errorf("probe = %d, %v; want 200", code, err)
			}
		}()
	}
	waitFor(t, "both probes to reach the vendor", func() bool { return f.count() == 5 })
	if b.State() != HalfOpen {
		t.Fatalf("state = %v with probes in flight, want half-open", b.State())
	}
	for range 3 { // a stampede of other callers while the verdict is pending
		mustReject(t, rt)
	}
	if f.count() != 5 {
		t.Fatalf("%d vendor calls; exactly K=2 probes may be admitted, the rest fail fast", f.count())
	}
	close(gate)
	within(t, "probes", wg.Wait)
	if b.State() != Closed {
		t.Fatalf("state = %v after both probes succeeded, want closed", b.State())
	}
	mustPass(t, rt, 200)
}

func TestStage2_OneFailedProbeReopensWithoutWaitingForTheOthers(t *testing.T) {
	clk := newClock()
	cfg := breakerCfg(clk)
	cfg.HalfOpenProbes = 3
	b := NewBreaker(cfg)
	f := newFake(status(503), status(503), status(503), ok(), status(503), ok())
	rt := b.Wrap(f)
	for range 3 {
		mustPass(t, rt, 503)
	}
	clk.Advance(30 * time.Second)
	mustPass(t, rt, 200) // probe 1 ok, 2 of 3 still needed
	if b.State() != HalfOpen {
		t.Fatalf("state = %v after 1 of 3 probes, want half-open", b.State())
	}
	mustPass(t, rt, 503) // probe 2 fails
	if b.State() != Open {
		t.Fatalf("state = %v; a single failed probe must reopen immediately", b.State())
	}
}

func TestStage2_ResultsFromBeforeTheStateChangedAreIgnored(t *testing.T) {
	clk := newClock()
	b := NewBreaker(breakerCfg(clk))
	gateOld, gateProbe := make(chan struct{}), make(chan struct{})
	f := newFake()
	f.handler = func(n int, req *http.Request) (*http.Response, error) {
		switch n {
		case 1:
			<-gateOld // a slow request admitted while the circuit was still closed
			return f.respond(req, ok())
		case 5:
			<-gateProbe // the half-open probe: its verdict is still pending
			return f.respond(req, ok())
		}
		return f.respond(req, status(503))
	}
	rt := b.Wrap(f)
	var wg sync.WaitGroup
	run := func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := get(t, rt); err != nil {
				t.Errorf("request err = %v", err)
			}
		}()
	}
	run() // call 1: the old, slow request
	waitFor(t, "the slow request to reach the vendor", func() bool { return f.count() == 1 })
	for range 3 { // calls 2-4: open the circuit
		mustPass(t, rt, 503)
	}
	clk.Advance(30 * time.Second)
	run() // call 5: the probe
	waitFor(t, "the probe to reach the vendor", func() bool { return f.count() == 5 })
	if b.State() != HalfOpen {
		t.Fatalf("state = %v with a probe in flight, want half-open", b.State())
	}
	close(gateOld) // the old request now succeeds: news from a previous era
	waitFor(t, "the old request to finish", func() bool { return f.bt.closed.Load() >= 4 })
	if b.State() != HalfOpen {
		t.Fatalf("state = %v: a success from a request admitted before the circuit opened was counted as the probe's verdict; results from earlier states must be dropped", b.State())
	}
	close(gateProbe)
	within(t, "requests", wg.Wait)
	if b.State() != Closed {
		t.Fatalf("state = %v after the real probe succeeded, want closed", b.State())
	}
}

func TestStage2_StateChangeCallbackSeesEveryTransitionInOrder(t *testing.T) {
	clk := newClock()
	rec := &recorder{}
	cfg := breakerCfg(clk)
	cfg.ConsecutiveFailures = 2
	cfg.OnStateChange = rec.on
	b := NewBreaker(cfg)
	rt := b.Wrap(newFake(status(503), status(503), status(503), ok()))
	mustPass(t, rt, 503)
	mustPass(t, rt, 503) // closed -> open
	clk.Advance(30 * time.Second)
	mustPass(t, rt, 503) // open -> half-open, probe fails: half-open -> open
	clk.Advance(30 * time.Second)
	mustPass(t, rt, 200) // open -> half-open, probe ok: half-open -> closed
	want := []transition{{Closed, Open}, {Open, HalfOpen}, {HalfOpen, Open}, {Open, HalfOpen}, {HalfOpen, Closed}}
	if !reflect.DeepEqual(rec.got, want) {
		t.Fatalf("transitions = %v, want %v", rec.got, want)
	}
}

func TestStage2_ClosingResetsCounters(t *testing.T) {
	clk := newClock()
	b := NewBreaker(breakerCfg(clk))
	rt := b.Wrap(newFake(status(503), status(503), status(503), ok(), status(503), status(503), ok()))
	for range 3 {
		mustPass(t, rt, 503)
	}
	clk.Advance(30 * time.Second)
	mustPass(t, rt, 200) // closed again
	mustPass(t, rt, 503)
	mustPass(t, rt, 503)
	if b.State() != Closed {
		t.Fatalf("state = %v; after recovery the consecutive count starts from zero (2 failures < 3)", b.State())
	}
}

func TestStage2_PerHostBreakersAreIndependent(t *testing.T) {
	clk := newClock()
	f := newFake()
	f.handler = func(n int, req *http.Request) (*http.Response, error) {
		if req.URL.Host == "kyc.test" {
			return f.respond(req, status(503))
		}
		return f.respond(req, ok())
	}
	p := NewPerHost(breakerCfg(clk))
	rt := p.Wrap(f)
	kyc := func() *http.Request {
		r, _ := http.NewRequest("GET", "http://kyc.test/check", nil)
		return r
	}
	imaging := func() *http.Request {
		r, _ := http.NewRequest("GET", "http://imaging.test/scan", nil)
		return r
	}
	for range 3 {
		resp, err := send(t, rt, kyc())
		if err != nil {
			t.Fatal(err)
		}
		closeResp(resp)
	}
	if p.State("kyc.test") != Open {
		t.Fatalf("kyc breaker = %v, want open", p.State("kyc.test"))
	}
	_, err := send(t, rt, kyc())
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("kyc err = %v, want ErrCircuitOpen", err)
	}
	resp, err := send(t, rt, imaging())
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("imaging = %v, %v; one vendor's outage must not trip another's breaker", resp, err)
	}
	closeResp(resp)
	if p.State("imaging.test") != Closed {
		t.Fatalf("imaging breaker = %v, want closed", p.State("imaging.test"))
	}
}

func TestStage2_RetrierDoesNotRetryAnOpenCircuit(t *testing.T) {
	clk := newClock()
	s := &fakeSleeper{}
	f := newFake(status(503))
	cfg := breakerCfg(clk)
	cfg.ConsecutiveFailures = 2
	retry := retryCfg(s)
	retry.MaxAttempts = 5
	rt := Chain(f, NewRetrier(retry), NewBreaker(cfg)) // retry outside, breaker inside

	resp, err := send(t, rt, newReq(t, "GET", ""))
	closeResp(resp)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err = %v; once the breaker opens mid-retry the caller should see ErrCircuitOpen", err)
	}
	if f.count() != 2 {
		t.Fatalf("%d vendor calls, want 2 (the breaker opened after the 2nd)", f.count())
	}
	// Two sleeps: before attempt 2 and before attempt 3. The retrier cannot know
	// the breaker just opened; it learns it from the rejection and then stops.
	if n := len(s.delays()); n != 2 {
		t.Fatalf("%d backoff sleeps %v, want 2: a rejection must end the retry loop, not start another backoff", n, s.delays())
	}
	// A fresh request fails immediately: no vendor call, no backoff.
	_, err = send(t, rt, newReq(t, "GET", ""))
	if !errors.Is(err, ErrCircuitOpen) || f.count() != 2 || len(s.delays()) != 2 {
		t.Fatalf("second request: err %v, %d vendor calls, sleeps %v; want an instant ErrCircuitOpen", err, f.count(), s.delays())
	}
}

func TestStage2_ConcurrentCallsKeepTheBreakerConsistent(t *testing.T) {
	rec := &recorder{}
	cfg := BreakerConfig{
		ConsecutiveFailures: 5, Cooldown: time.Millisecond, HalfOpenProbes: 2,
		OnStateChange: rec.on, // Now defaults to the real clock
	}
	b := NewBreaker(cfg)
	f := newFake()
	var calls int
	var cmu sync.Mutex
	f.handler = func(n int, req *http.Request) (*http.Response, error) {
		cmu.Lock()
		calls++
		c := calls
		cmu.Unlock()
		if c%20 < 14 { // bursts of 14 failures, then 6 successes: enough in a row to trip
			return f.respond(req, status(503))
		}
		return f.respond(req, ok())
	}
	rt := b.Wrap(f)
	const workers, per = 16, 100
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range per {
				resp, err := rt.RoundTrip(newReqNoT())
				if err != nil && !errors.Is(err, ErrCircuitOpen) {
					t.Errorf("err = %v", err)
				}
				closeResp(resp)
			}
		}()
	}
	within(t, "concurrent requests", wg.Wait)
	legal := map[transition]bool{{Closed, Open}: true, {Open, HalfOpen}: true, {HalfOpen, Closed}: true, {HalfOpen, Open}: true}
	for i, tr := range rec.got {
		if !legal[tr] {
			t.Fatalf("transition #%d %v -> %v is not a legal breaker move", i, tr.from, tr.to)
		}
		if i > 0 && rec.got[i-1].to != tr.from {
			t.Fatalf("transitions %v then %v do not chain: the callback saw them out of order", rec.got[i-1], tr)
		}
	}
	if got, want := int64(f.count())+b.Stats().Rejected, int64(workers*per); got != want {
		t.Fatalf("vendor calls %d + rejected %d != %d requests: every request is either admitted or rejected", f.count(), b.Stats().Rejected, workers*per)
	}
	if len(rec.got) == 0 {
		t.Fatal("the breaker never opened under bursts of failures")
	}
}

func newReqNoT() *http.Request {
	r, _ := http.NewRequest("GET", "http://vendor.test/v1/thing", nil)
	return r
}
