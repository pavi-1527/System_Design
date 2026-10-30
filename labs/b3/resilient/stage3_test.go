package resilient

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStage3_BudgetDepositsWithdrawsAndCaps(t *testing.T) {
	b := NewBudget(0.25, 2)
	if b.Tokens() != 2 {
		t.Fatalf("a new budget starts full: Tokens = %v, want 2", b.Tokens())
	}
	if !b.TryWithdraw() || !b.TryWithdraw() {
		t.Fatal("two withdrawals from a full budget of 2 should succeed")
	}
	if b.TryWithdraw() {
		t.Fatal("a third withdrawal succeeded from an empty budget")
	}
	for range 3 {
		b.Deposit()
	}
	if b.TryWithdraw() {
		t.Fatalf("Tokens = %v: three deposits of 0.25 are not yet one whole retry", b.Tokens())
	}
	b.Deposit()
	if !b.TryWithdraw() {
		t.Fatal("four deposits of 0.25 should pay for one retry")
	}
	for range 100 {
		b.Deposit()
	}
	if b.Tokens() != 2 {
		t.Fatalf("Tokens = %v after many deposits, want the burst cap 2", b.Tokens())
	}
}

func TestStage3_RetriesAreLimitedByTheBudget(t *testing.T) {
	// ratio 0.25, burst 1: credit for the first retry, then one more retry per 4 requests.
	f := newFake(status(503))
	r := NewRetrier(retryCfg(&fakeSleeper{}), WithBudget(NewBudget(0.25, 1)))
	rt := r.Wrap(f)
	for range 20 {
		resp, err := send(t, rt, newReq(t, "GET", ""))
		if err != nil || resp.StatusCode != 503 {
			t.Fatalf("RoundTrip = %v, %v; want the 503", resp, err)
		}
		closeResp(resp)
	}
	// Retries happen on requests 1, 5, 9, 13 and 17; every other request is denied its first retry.
	if f.count() != 25 {
		t.Fatalf("%d vendor calls, want 25 = 20 requests + 5 budgeted retries (without a budget it would be 60)", f.count())
	}
	st := r.Stats()
	want := RetryStats{Requests: 20, Attempts: 25, Retries: 5, BudgetDenied: 20, GaveUp: 0}
	if st != want {
		t.Fatalf("Stats = %+v, want %+v", st, want)
	}
}

func TestStage3_BudgetTokensAreOnlySpentOnRetriesThatHappen(t *testing.T) {
	// MaxAttempts 2: after the single allowed retry fails there is nothing left to pay for.
	b := NewBudget(0, 5)
	cfg := retryCfg(&fakeSleeper{})
	cfg.MaxAttempts = 2
	resp, _ := send(t, NewRetrier(cfg, WithBudget(b)).Wrap(newFake(status(503))), newReq(t, "GET", ""))
	closeResp(resp)
	if b.Tokens() != 4 {
		t.Fatalf("Tokens = %v, want 4: one retry happened, and exhausting attempts must not withdraw again", b.Tokens())
	}
	// A request that will never be retried (POST without a key) must not touch the budget either.
	resp, _ = send(t, NewRetrier(cfg, WithBudget(b)).Wrap(newFake(status(503))), newReq(t, "POST", "x"))
	closeResp(resp)
	if b.Tokens() != 4 {
		t.Fatalf("Tokens = %v, want 4: a non-retriable request was charged", b.Tokens())
	}
}

func TestStage3_AttemptTimeoutAppliesToEveryAttempt(t *testing.T) {
	f := newFake()
	var mu sync.Mutex
	var budgets []time.Duration
	f.handler = func(n int, req *http.Request) (*http.Response, error) {
		dl, has := req.Context().Deadline()
		if !has {
			t.Errorf("attempt %d has no deadline", n)
			return nil, errors.New("no deadline")
		}
		mu.Lock()
		budgets = append(budgets, time.Until(dl))
		mu.Unlock()
		if n <= 2 { // a hung vendor: nothing happens until the attempt's context expires
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		return f.respond(req, ok())
	}
	s := &fakeSleeper{}
	r := NewRetrier(retryCfg(s), WithAttemptTimeout(40*time.Millisecond, 0))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := send(t, r.Wrap(f), newReqCtx(t, ctx, "GET", ""))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("RoundTrip = %v, %v; want success on the 3rd attempt after two attempt timeouts", resp, err)
	}
	closeResp(resp)
	if f.count() != 3 || len(s.delays()) != 2 {
		t.Fatalf("%d calls, %d sleeps; want 3 and 2", f.count(), len(s.delays()))
	}
	for i, b := range budgets {
		if b <= 0 || b > 40*time.Millisecond {
			t.Fatalf("attempt %d saw %v left on its context, want within (0, 40ms]: each attempt gets its own timeout, not the 30s overall one", i+1, b)
		}
	}
}

func TestStage3_AttemptDeadlineNeverExceedsTheOverallDeadline(t *testing.T) {
	f := newFake()
	var left atomic.Int64
	f.handler = func(n int, req *http.Request) (*http.Response, error) {
		dl, _ := req.Context().Deadline()
		left.Store(int64(time.Until(dl)))
		return f.respond(req, ok())
	}
	r := NewRetrier(retryCfg(&fakeSleeper{}), WithAttemptTimeout(10*time.Second, 0))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	resp, err := send(t, r.Wrap(f), newReqCtx(t, ctx, "GET", ""))
	if err != nil {
		t.Fatal(err)
	}
	closeResp(resp)
	if got := time.Duration(left.Load()); got > 300*time.Millisecond {
		t.Fatalf("the attempt had %v, more than the caller's 300ms: a child deadline may only shorten the parent's", got)
	}
}

func TestStage3_AttemptContextOutlivesRoundTripUntilTheBodyIsClosed(t *testing.T) {
	var attemptCtx context.Context
	f := newFake()
	f.handler = func(n int, req *http.Request) (*http.Response, error) {
		attemptCtx = req.Context()
		resp, _ := f.respond(req, ok())
		resp.Body = &ctxBody{ReadCloser: resp.Body, ctx: req.Context()}
		return resp, nil
	}
	r := NewRetrier(retryCfg(&fakeSleeper{}), WithAttemptTimeout(5*time.Second, 0))
	resp, err := send(t, r.Wrap(f), newReq(t, "GET", ""))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil || string(b) != "body-200" {
		t.Fatalf("reading the body after RoundTrip returned: %q, %v; the attempt's context was cancelled too early (defer cancel() inside RoundTrip kills the body read)", b, err)
	}
	if attemptCtx.Err() != nil {
		t.Fatal("attempt context cancelled before the body was closed")
	}
	closeResp(resp)
	if attemptCtx.Err() == nil {
		t.Fatal("attempt context still live after the body was closed: its timer and registration leak")
	}
}

// ctxBody fails reads once the request context is done, like a real network body.
type ctxBody struct {
	io.ReadCloser
	ctx context.Context
}

func (b *ctxBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.ReadCloser.Read(p)
}

func TestStage3_NoRetryWhenTooLittleOfTheOverallBudgetRemains(t *testing.T) {
	s := &fakeSleeper{}
	f := newFake(status(503))
	r := NewRetrier(retryCfg(s), WithAttemptTimeout(time.Second, time.Second)) // want 1s left to start another attempt
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	resp, err := send(t, r.Wrap(f), newReqCtx(t, ctx, "GET", ""))
	if err != nil || resp == nil || resp.StatusCode != 503 {
		t.Fatalf("RoundTrip = %v, %v; want the 503 returned (a real answer beats 'deadline exceeded')", resp, err)
	}
	closeResp(resp)
	if f.count() != 1 || len(s.delays()) != 0 {
		t.Fatalf("%d calls, sleeps %v; want 1 call and no sleep: 500ms left is less than the 1s needed for another attempt", f.count(), s.delays())
	}
	if r.Stats().GaveUp != 1 {
		t.Fatalf("Stats = %+v, want GaveUp 1", r.Stats())
	}
}

func TestStage3_BackoffLongerThanTheRemainingTimeIsNotSlept(t *testing.T) {
	s := &fakeSleeper{}
	cfg := retryCfg(s)
	cfg.BaseDelay = 10 * time.Second
	cfg.MaxDelay = 30 * time.Second
	cfg.Rand = constRand(0.9) // backoff 9s
	f := newFake(status(503))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := send(t, NewRetrier(cfg).Wrap(f), newReqCtx(t, ctx, "GET", ""))
	if err != nil || resp == nil || resp.StatusCode != 503 {
		t.Fatalf("RoundTrip = %v, %v; want the 503: sleeping 9s into a 2s deadline only converts it into 'context deadline exceeded'", resp, err)
	}
	closeResp(resp)
	if len(s.delays()) != 0 || f.count() != 1 {
		t.Fatalf("%d calls, sleeps %v; want 1 call and no sleep", f.count(), s.delays())
	}
}

func holdOpen(f *fakeTransport, gate chan struct{}) {
	f.handler = func(n int, req *http.Request) (*http.Response, error) {
		select {
		case <-gate:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		return f.respond(req, ok())
	}
}

func TestStage3_BulkheadRejectsBeyondCapacity(t *testing.T) {
	gate := make(chan struct{})
	f := newFake()
	holdOpen(f, gate)
	bh := NewBulkhead(BulkheadConfig{MaxInFlight: 2})
	rt := bh.Wrap(f)

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, err := get(t, rt); err != nil || code != 200 {
				t.Errorf("admitted request = %d, %v", code, err)
			}
		}()
	}
	waitFor(t, "two requests in flight", func() bool { return bh.InFlight() == 2 })
	resp, err := send(t, rt, newReq(t, "GET", ""))
	closeResp(resp)
	if !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("third request err = %v, want ErrBulkheadFull immediately (no queue configured)", err)
	}
	if f.count() != 2 {
		t.Fatalf("%d vendor calls, want 2", f.count())
	}
	close(gate)
	within(t, "admitted requests", wg.Wait)
	waitFor(t, "slots to be released", func() bool { return bh.InFlight() == 0 })
	if st := bh.Stats(); st.Rejected != 1 || st.Peak != 2 {
		t.Fatalf("Stats = %+v, want Rejected 1, Peak 2", st)
	}
}

func TestStage3_BulkheadSlotIsHeldUntilTheBodyIsClosed(t *testing.T) {
	bh := NewBulkhead(BulkheadConfig{MaxInFlight: 1})
	rt := bh.Wrap(newFake(ok()))
	resp, err := send(t, rt, newReq(t, "GET", ""))
	if err != nil {
		t.Fatal(err)
	}
	if bh.InFlight() != 1 {
		t.Fatalf("InFlight = %d with a response whose body is still open, want 1: the connection is busy until the body is read or closed", bh.InFlight())
	}
	_, err = send(t, rt, newReq(t, "GET", ""))
	if !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("second request err = %v, want ErrBulkheadFull", err)
	}
	within(t, "closing the body twice", func() {
		_ = resp.Body.Close()
		_ = resp.Body.Close() // closing twice must release once
	})
	if bh.InFlight() != 0 {
		t.Fatalf("InFlight = %d after Close, want 0", bh.InFlight())
	}
	resp, err = send(t, rt, newReq(t, "GET", ""))
	if err != nil {
		t.Fatalf("request after release: %v", err)
	}
	closeResp(resp)
	resp, err = send(t, rt, newReq(t, "GET", "")) // would fail if the double Close freed a phantom slot
	if err != nil {
		t.Fatal(err)
	}
	if _, err2 := send(t, rt, newReq(t, "GET", "")); !errors.Is(err2, ErrBulkheadFull) {
		t.Fatalf("err = %v; a double Close released a slot that was not held", err2)
	}
	closeResp(resp)
}

func TestStage3_BulkheadErrorsReleaseTheSlot(t *testing.T) {
	bh := NewBulkhead(BulkheadConfig{MaxInFlight: 1})
	rt := bh.Wrap(newFake(fail(errBoom)))
	for range 5 {
		if _, err := send(t, rt, newReq(t, "GET", "")); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want the transport error (a leaked slot would give ErrBulkheadFull)", err)
		}
	}
	if bh.InFlight() != 0 {
		t.Fatalf("InFlight = %d, want 0", bh.InFlight())
	}
}

func TestStage3_BulkheadQueueWaitsForASlot(t *testing.T) {
	bh := NewBulkhead(BulkheadConfig{MaxInFlight: 1, MaxQueue: 1})
	f := newFake(ok())
	rt := bh.Wrap(f)
	first, err := send(t, rt, newReq(t, "GET", ""))
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan error, 1)
	go func() {
		resp, err := rt.RoundTrip(newReqNoT())
		closeResp(resp)
		got <- err
	}()
	waitFor(t, "the second caller to queue", func() bool { return bh.Waiting() == 1 })
	if _, err := send(t, rt, newReq(t, "GET", "")); !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("third caller err = %v, want ErrBulkheadFull: the queue holds only 1", err)
	}
	select {
	case err := <-got:
		t.Fatalf("queued caller finished early with %v while the slot was still held", err)
	default:
	}
	closeResp(first) // frees the slot; the queued caller proceeds
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("queued caller err = %v, want success", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("queued caller never got the freed slot")
	}
	waitFor(t, "queue to drain", func() bool { return bh.Waiting() == 0 && bh.InFlight() == 0 })
}

func TestStage3_BulkheadWaiterHonoursItsContext(t *testing.T) {
	bh := NewBulkhead(BulkheadConfig{MaxInFlight: 1, MaxQueue: 1})
	rt := bh.Wrap(newFake(ok()))
	first, err := send(t, rt, newReq(t, "GET", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer closeResp(first)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		resp, err := rt.RoundTrip(newReqCtx(t, ctx, "GET", ""))
		closeResp(resp)
		got <- err
	}()
	waitFor(t, "the waiter to queue", func() bool { return bh.Waiting() == 1 })
	cancel()
	select {
	case err := <-got:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter err = %v, want context.Canceled", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("a queued caller ignored its cancelled context")
	}
	if bh.Waiting() != 0 {
		t.Fatalf("Waiting = %d after the waiter left, want 0", bh.Waiting())
	}
}

func TestStage3_ConcurrentChainHonoursEveryLimit(t *testing.T) {
	baseline := runtime.NumGoroutine()

	const (
		requests = 480
		workers  = 16
		capacity = 4
		ratio    = 0.2
		burst    = 10
	)
	var (
		mu       sync.Mutex
		rng      = rand.New(rand.NewSource(7))
		inflight atomic.Int64
		maxSeen  atomic.Int64
	)
	f := newFake()
	f.handler = func(n int, req *http.Request) (*http.Response, error) {
		cur := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			m := maxSeen.Load()
			if cur <= m || maxSeen.CompareAndSwap(m, cur) {
				break
			}
		}
		time.Sleep(200 * time.Microsecond) // vendor latency: makes callers overlap
		mu.Lock()
		x := rng.Float64()
		mu.Unlock()
		switch {
		case x < 0.20:
			return f.respond(req, status(503))
		case x < 0.28:
			return nil, errBoom
		}
		return f.respond(req, ok())
	}

	retrier := NewRetrier(retryCfg(&fakeSleeper{}), WithBudget(NewBudget(ratio, burst)))
	rec := &recorder{}
	breaker := NewBreaker(BreakerConfig{
		ConsecutiveFailures: 4, Cooldown: 2 * time.Millisecond, HalfOpenProbes: 2, OnStateChange: rec.on,
	})
	bulkhead := NewBulkhead(BulkheadConfig{MaxInFlight: capacity, MaxQueue: 4})
	rt := Chain(f, retrier, breaker, bulkhead) // retry outermost, then breaker, then bulkhead

	var next atomic.Int64
	var okCount, rejected, other atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for next.Add(1) <= requests {
				resp, err := rt.RoundTrip(newReqNoT())
				switch {
				case err == nil && resp.StatusCode == 200:
					okCount.Add(1)
				case errors.Is(err, ErrCircuitOpen), errors.Is(err, ErrBulkheadFull):
					rejected.Add(1)
				default:
					other.Add(1) // exhausted retries: a 503 response or a *RetryError
				}
				closeResp(resp)
			}
		}()
	}
	within(t, "concurrent chain", wg.Wait)

	if m := maxSeen.Load(); m > capacity {
		t.Fatalf("%d requests were in flight at the vendor at once, bulkhead capacity is %d", m, capacity)
	}
	if bulkhead.InFlight() != 0 {
		t.Fatalf("bulkhead InFlight = %d at the end: slots leaked", bulkhead.InFlight())
	}
	if p := bulkhead.Stats().Peak; p > capacity {
		t.Fatalf("bulkhead Peak = %d, want <= %d", p, capacity)
	}
	if out := f.bt.outstanding(); out != 0 {
		t.Fatalf("%d response bodies were never closed", out)
	}
	legal := map[transition]bool{{Closed, Open}: true, {Open, HalfOpen}: true, {HalfOpen, Closed}: true, {HalfOpen, Open}: true}
	for i, tr := range rec.got {
		if !legal[tr] {
			t.Fatalf("transition #%d %v -> %v is illegal", i, tr.from, tr.to)
		}
	}
	st := retrier.Stats()
	if st.Requests != requests || st.Attempts != st.Requests+st.Retries {
		t.Fatalf("Stats = %+v: want Requests = %d and Attempts = Requests + Retries", st, requests)
	}
	if maxRetries := int64(burst + ratio*requests); st.Retries > maxRetries {
		t.Fatalf("%d retries, but the budget (burst %d + %.1f per request) allows at most %d", st.Retries, burst, ratio, maxRetries)
	}
	if got := okCount.Load() + rejected.Load() + other.Load(); got != requests {
		t.Fatalf("%d outcomes for %d requests", got, requests)
	}
	if okCount.Load() == 0 {
		t.Fatal("no request succeeded: the chain is rejecting everything")
	}
	waitForGoroutines(t, baseline)
}
