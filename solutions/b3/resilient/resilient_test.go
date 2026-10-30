package resilient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

var errBoom = errors.New("connection reset by peer")

// ------------------------------------------------------------ test helpers

// within fails the test if fn does not return in time, turning a hang in your
// implementation into a message.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("%s did not finish within %v (a blocked wait, a lock held across a call, or a missing ctx check)", what, testTimeout)
	}
}

// waitFor polls a condition that should become true (a positive wait: it only
// ever delays a passing test, never decides the verdict of a failing one).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for !cond() {
		if t.Failed() { // a goroutine already reported why the awaited event cannot happen
			t.FailNow()
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", testTimeout, what)
		}
		time.Sleep(time.Millisecond)
	}
}

// bodyTracker records every response body handed out, whether it was read to
// EOF, and whether it was closed.
type bodyTracker struct{ opened, closed, drained atomic.Int64 }

func (bt *bodyTracker) body(s string) io.ReadCloser {
	bt.opened.Add(1)
	return &trackedBody{Reader: strings.NewReader(s), bt: bt}
}

func (bt *bodyTracker) outstanding() int64 { return bt.opened.Load() - bt.closed.Load() }

type trackedBody struct {
	*strings.Reader
	bt        *bodyTracker
	eof, shut sync.Once
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		b.eof.Do(func() { b.bt.drained.Add(1) })
	}
	return n, err
}

func (b *trackedBody) Close() error {
	b.shut.Do(func() { b.bt.closed.Add(1) })
	return nil
}

// step is one scripted outcome of the fake vendor.
type step struct {
	status int
	header http.Header
	err    error
}

func ok() step { return step{status: 200} }

func status(code int, kv ...string) step {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return step{status: code, header: h}
}

func fail(err error) step { return step{err: err} }

type call struct {
	method string
	host   string
	header http.Header
	body   string
}

// fakeTransport is the vendor: a scripted (or programmable) http.RoundTripper.
// Like a real one it reads and closes the request body.
type fakeTransport struct {
	bt      *bodyTracker
	script  []step
	handler func(n int, req *http.Request) (*http.Response, error) // overrides script

	mu    sync.Mutex
	calls []call
}

func newFake(script ...step) *fakeTransport {
	return &fakeTransport{bt: &bodyTracker{}, script: script}
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		body = string(b)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{method: req.Method, host: req.URL.Host, header: req.Header.Clone(), body: body})
	n := len(f.calls)
	f.mu.Unlock()
	if f.handler != nil {
		return f.handler(n, req)
	}
	return f.respond(req, f.script[min(n-1, len(f.script)-1)])
}

func (f *fakeTransport) respond(req *http.Request, s step) (*http.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	h := s.header
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{
		StatusCode: s.status,
		Status:     fmt.Sprintf("%d %s", s.status, http.StatusText(s.status)),
		Header:     h,
		Body:       f.bt.body(fmt.Sprintf("body-%d", s.status)),
		Request:    req,
	}, nil
}

func (f *fakeTransport) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTransport) call(i int) call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

// fakeSleeper replaces time.Sleep: it records every requested delay and
// returns at once (or blocks until ctx is done when block is set).
type fakeSleeper struct {
	mu      sync.Mutex
	slept   []time.Duration
	block   bool
	entered chan struct{}
}

func (s *fakeSleeper) Sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.slept = append(s.slept, d)
	s.mu.Unlock()
	if s.block {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
	}
	return ctx.Err()
}

func (s *fakeSleeper) delays() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.slept...)
}

var fixedNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func constRand(v float64) func() float64 { return func() float64 { return v } }

// retryCfg is the baseline: 3 attempts, 100ms base, 10s cap, jitter always 0.5
// (so the first retry sleeps 50ms), a recording sleeper and a fixed "now".
func retryCfg(s *fakeSleeper) RetryConfig {
	return RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Sleep:       s.Sleep,
		Rand:        constRand(0.5),
		Now:         func() time.Time { return fixedNow },
	}
}

func newReq(t *testing.T, method, body string, kv ...string) *http.Request {
	t.Helper()
	return newReqCtx(t, context.Background(), method, body, kv...)
}

func newReqCtx(t *testing.T, ctx context.Context, method, body string, kv ...string) *http.Request {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://vendor.test/v1/thing", rd)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		req.Header.Set(kv[i], kv[i+1])
	}
	return req
}

func send(t *testing.T, rt http.RoundTripper, req *http.Request) (*http.Response, error) {
	t.Helper()
	var resp *http.Response
	var err error
	within(t, "RoundTrip", func() { resp, err = rt.RoundTrip(req) })
	return resp, err
}

func closeResp(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func wantDelays(t *testing.T, s *fakeSleeper, want ...time.Duration) {
	t.Helper()
	got := s.delays()
	if len(got) != len(want) {
		t.Fatalf("backoff sleeps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backoff sleeps = %v, want %v", got, want)
		}
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
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutine leak: %d goroutines, baseline %d\n%s", n, baseline, buf)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---------------------------------------------------------------- Stage 1

func TestStage1_SuccessMakesOneCall(t *testing.T) {
	s := &fakeSleeper{}
	f := newFake(ok())
	rt := NewRetrier(retryCfg(s)).Wrap(f)
	resp, err := send(t, rt, newReq(t, "GET", ""))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("RoundTrip = %v, %v; want 200, nil", resp, err)
	}
	b, _ := io.ReadAll(resp.Body)
	closeResp(resp)
	if string(b) != "body-200" || f.count() != 1 || len(s.delays()) != 0 {
		t.Fatalf("body %q, %d calls, sleeps %v; want body-200, 1 call, no sleeps", b, f.count(), s.delays())
	}
}

func TestStage1_RetriesTransientStatusesUntilSuccess(t *testing.T) {
	for _, code := range []int{429, 502, 503, 504} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			s := &fakeSleeper{}
			f := newFake(status(code), status(code), ok())
			resp, err := send(t, NewRetrier(retryCfg(s)).Wrap(f), newReq(t, "GET", ""))
			if err != nil || resp.StatusCode != 200 {
				t.Fatalf("RoundTrip = %v, %v; want 200 after retries", resp, err)
			}
			closeResp(resp)
			if f.count() != 3 || len(s.delays()) != 2 {
				t.Fatalf("%d calls, %d sleeps; want 3 calls and 2 sleeps", f.count(), len(s.delays()))
			}
		})
	}
}

func TestStage1_OtherStatusesAreReturnedWithoutRetry(t *testing.T) {
	// 500 is deliberately NOT retried: it usually means this very request
	// triggers a bug, and repeating it only adds load.
	for _, code := range []int{200, 201, 301, 400, 401, 404, 409, 422, 500, 501} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			s := &fakeSleeper{}
			f := newFake(status(code), ok())
			resp, err := send(t, NewRetrier(retryCfg(s)).Wrap(f), newReq(t, "GET", ""))
			if err != nil || resp.StatusCode != code {
				t.Fatalf("RoundTrip = %v, %v; want status %d untouched", resp, err, code)
			}
			closeResp(resp)
			if f.count() != 1 {
				t.Fatalf("status %d was attempted %d times, want exactly 1", code, f.count())
			}
		})
	}
}

func TestStage1_ExhaustedStatusReturnsLastResponse(t *testing.T) {
	s := &fakeSleeper{}
	f := newFake(status(503))
	resp, err := send(t, NewRetrier(retryCfg(s)).Wrap(f), newReq(t, "GET", ""))
	if err != nil {
		t.Fatalf("err = %v; a 503 is a response, not an error: return it with a nil error", err)
	}
	if resp == nil || resp.StatusCode != 503 {
		t.Fatalf("resp = %v, want the last 503", resp)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "body-503" {
		t.Fatalf("body = %q: the returned response must still be readable", b)
	}
	closeResp(resp)
	if f.count() != 3 {
		t.Fatalf("%d calls, want MaxAttempts = 3", f.count())
	}
	wantDelays(t, s, 50*time.Millisecond, 100*time.Millisecond)
}

func TestStage1_TransportErrorsAreRetriedThenReportedAsRetryError(t *testing.T) {
	s := &fakeSleeper{}
	f := newFake(fail(errBoom), ok())
	resp, err := send(t, NewRetrier(retryCfg(s)).Wrap(f), newReq(t, "GET", ""))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("RoundTrip = %v, %v; want success on the 2nd attempt", resp, err)
	}
	closeResp(resp)

	f = newFake(fail(errBoom))
	resp, err = send(t, NewRetrier(retryCfg(&fakeSleeper{})).Wrap(f), newReq(t, "GET", ""))
	if resp != nil {
		t.Fatalf("resp = %v, want nil with an error", resp)
	}
	var re *RetryError
	if !errors.As(err, &re) || re.Attempts != 3 {
		t.Fatalf("err = %v; want a *RetryError with Attempts = 3 (errors.As)", err)
	}
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v; RetryError must unwrap to the last transport error", err)
	}
	if f.count() != 3 {
		t.Fatalf("%d calls, want 3", f.count())
	}
}

func TestStage1_FullJitterBackoffSequence(t *testing.T) {
	// Ceilings: 100, 200, 300 (capped), 300 ms. Rand 0.5 picks half of each.
	s := &fakeSleeper{}
	cfg := retryCfg(s)
	cfg.MaxAttempts = 5
	cfg.MaxDelay = 300 * time.Millisecond
	f := newFake(status(503))
	resp, _ := send(t, NewRetrier(cfg).Wrap(f), newReq(t, "GET", ""))
	closeResp(resp)
	wantDelays(t, s, 50*time.Millisecond, 100*time.Millisecond, 150*time.Millisecond, 150*time.Millisecond)
}

func TestStage1_DelayIsRandTimesCeiling(t *testing.T) {
	// Full jitter: the delay is uniform in [0, ceiling), not ceiling +/- a bit.
	var i atomic.Int64
	seq := []float64{0, 0.25, 0.75}
	s := &fakeSleeper{}
	cfg := retryCfg(s)
	cfg.MaxAttempts = 4
	cfg.Rand = func() float64 { return seq[(i.Add(1)-1)%3] }
	resp, _ := send(t, NewRetrier(cfg).Wrap(newFake(status(503))), newReq(t, "GET", ""))
	closeResp(resp)
	wantDelays(t, s, 0, 50*time.Millisecond, 300*time.Millisecond) // ceilings 100, 200, 400
}

func TestStage1_BackoffNeverOverflowsOrExceedsMaxDelay(t *testing.T) {
	s := &fakeSleeper{}
	cfg := retryCfg(s)
	cfg.MaxAttempts = 90
	cfg.BaseDelay = time.Millisecond
	cfg.MaxDelay = 5 * time.Second
	cfg.Rand = constRand(0.9999)
	resp, _ := send(t, NewRetrier(cfg).Wrap(newFake(status(503))), newReq(t, "GET", ""))
	closeResp(resp)
	got := s.delays()
	if len(got) != 89 {
		t.Fatalf("%d sleeps, want 89", len(got))
	}
	for i, d := range got {
		if d < 0 || d > 5*time.Second {
			t.Fatalf("sleep %d = %v, want within [0, MaxDelay=5s] (doubling overflowed int64 nanoseconds?)", i, d)
		}
	}
	if last := got[len(got)-1]; last < 4*time.Second {
		t.Fatalf("last sleep = %v; after 89 doublings the ceiling must have been the 5s cap", last)
	}
}

func TestStage1_RetryAfterSeconds(t *testing.T) {
	s := &fakeSleeper{}
	f := newFake(status(429, "Retry-After", "2"), status(503, "Retry-After", "0"), ok())
	resp, err := send(t, NewRetrier(retryCfg(s)).Wrap(f), newReq(t, "GET", ""))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("RoundTrip = %v, %v", resp, err)
	}
	closeResp(resp)
	// 1st retry: the server asks for 2s, longer than our 50ms jittered backoff.
	// 2nd retry: the server says 0s, so our own backoff (ceiling 200ms * 0.5 = 100ms) applies.
	wantDelays(t, s, 2*time.Second, 100*time.Millisecond)
}

func TestStage1_RetryAfterHTTPDate(t *testing.T) {
	s := &fakeSleeper{}
	future := fixedNow.Add(5 * time.Second).UTC().Format(http.TimeFormat)
	past := fixedNow.Add(-time.Hour).UTC().Format(http.TimeFormat)
	f := newFake(status(503, "Retry-After", future), status(503, "Retry-After", past), ok())
	resp, err := send(t, NewRetrier(retryCfg(s)).Wrap(f), newReq(t, "GET", ""))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("RoundTrip = %v, %v", resp, err)
	}
	closeResp(resp)
	wantDelays(t, s, 5*time.Second, 100*time.Millisecond) // a date in the past means "now": our backoff applies
}

func TestStage1_RetryAfterBeyondMaxDelayGivesUp(t *testing.T) {
	s := &fakeSleeper{}
	f := newFake(status(429, "Retry-After", "3600"), ok())
	resp, err := send(t, NewRetrier(retryCfg(s)).Wrap(f), newReq(t, "GET", ""))
	if err != nil || resp == nil || resp.StatusCode != 429 {
		t.Fatalf("RoundTrip = %v, %v; want the 429 returned: the vendor asked for 1h, more than MaxDelay (10s)", resp, err)
	}
	closeResp(resp)
	if f.count() != 1 || len(s.delays()) != 0 {
		t.Fatalf("%d calls, sleeps %v; want 1 call and no sleep: retrying earlier than asked is rude, waiting 1h is pointless", f.count(), s.delays())
	}
}

func TestStage1_OnlyIdempotentMethodsAreRetried(t *testing.T) {
	for method, want := range map[string]int{
		"GET": 3, "HEAD": 3, "PUT": 3, "DELETE": 3, "OPTIONS": 3,
		"POST": 1, "PATCH": 1,
	} {
		t.Run(method, func(t *testing.T) {
			f := newFake(status(503))
			resp, _ := send(t, NewRetrier(retryCfg(&fakeSleeper{})).Wrap(f), newReq(t, method, ""))
			closeResp(resp)
			if f.count() != want {
				t.Fatalf("%s was attempted %d times, want %d", method, f.count(), want)
			}
		})
	}
}

func TestStage1_PostWithoutKeyIsNeverResent(t *testing.T) {
	// The vendor books the payment, then the connection dies before the answer arrives.
	var processed atomic.Int64
	f := newFake()
	f.handler = func(n int, req *http.Request) (*http.Response, error) {
		processed.Add(1)
		return nil, errBoom
	}
	resp, err := send(t, NewRetrier(retryCfg(&fakeSleeper{})).Wrap(f), newReq(t, "POST", `{"amount":100}`))
	if resp != nil || !errors.Is(err, errBoom) {
		t.Fatalf("RoundTrip = %v, %v; want the transport error", resp, err)
	}
	if processed.Load() != 1 {
		t.Fatalf("the vendor processed the payment %d times; a retried POST without an idempotency key would double-charge", processed.Load())
	}
}

func TestStage1_PostWithIdempotencyKeyIsRetriedWithTheSameKey(t *testing.T) {
	f := newFake(fail(errBoom), status(503), ok())
	resp, err := send(t, NewRetrier(retryCfg(&fakeSleeper{})).Wrap(f),
		newReq(t, "POST", `{"amount":100}`, "Idempotency-Key", "pay-7f3a"))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("RoundTrip = %v, %v; want success after retries", resp, err)
	}
	closeResp(resp)
	if f.count() != 3 {
		t.Fatalf("%d calls, want 3", f.count())
	}
	for i := range 3 {
		c := f.call(i)
		if c.header.Get("Idempotency-Key") != "pay-7f3a" || c.body != `{"amount":100}` {
			t.Fatalf("attempt %d sent key %q body %q; every attempt must carry the same key and the full body", i+1, c.header.Get("Idempotency-Key"), c.body)
		}
	}
}

func TestStage1_RequestBodyIsReplayedOnEveryAttempt(t *testing.T) {
	f := newFake(status(503), status(503), ok())
	resp, err := send(t, NewRetrier(retryCfg(&fakeSleeper{})).Wrap(f), newReq(t, "PUT", "payload-bytes"))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("RoundTrip = %v, %v", resp, err)
	}
	closeResp(resp)
	for i := range 3 {
		if got := f.call(i).body; got != "payload-bytes" {
			t.Fatalf("attempt %d saw body %q, want payload-bytes (use req.GetBody to rewind)", i+1, got)
		}
	}
}

func TestStage1_UnreplayableBodyIsNotRetried(t *testing.T) {
	req, err := http.NewRequest("PUT", "http://vendor.test/x", io.NopCloser(strings.NewReader("stream")))
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("test setup: expected GetBody to be nil for an opaque reader")
	}
	f := newFake(status(503), ok())
	resp, err := send(t, NewRetrier(retryCfg(&fakeSleeper{})).Wrap(f), req)
	if err != nil || resp == nil || resp.StatusCode != 503 {
		t.Fatalf("RoundTrip = %v, %v; want the first 503 returned", resp, err)
	}
	closeResp(resp)
	if f.count() != 1 {
		t.Fatalf("%d calls; a request whose body cannot be rewound can only be sent once", f.count())
	}
}

func TestStage1_DiscardedResponsesAreDrainedAndClosed(t *testing.T) {
	f := newFake(status(503), status(503), ok())
	resp, err := send(t, NewRetrier(retryCfg(&fakeSleeper{})).Wrap(f), newReq(t, "GET", ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.bt.opened.Load(); got != 3 {
		t.Fatalf("%d bodies opened, want 3", got)
	}
	if out := f.bt.outstanding(); out != 1 {
		t.Fatalf("%d bodies still open before the caller closes the final one, want 1: every discarded response must be closed or its connection leaks", out)
	}
	if d := f.bt.drained.Load(); d < 2 {
		t.Fatalf("only %d discarded bodies were read to EOF, want 2: close without draining prevents connection reuse", d)
	}
	closeResp(resp)
	if out := f.bt.outstanding(); out != 0 {
		t.Fatalf("%d bodies open at the end", out)
	}
}

func TestStage1_CallerCancellationIsNeverRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := newFake()
	f.handler = func(n int, req *http.Request) (*http.Response, error) { return nil, req.Context().Err() }
	s := &fakeSleeper{}
	resp, err := send(t, NewRetrier(retryCfg(s)).Wrap(f), newReqCtx(t, ctx, "GET", ""))
	if resp != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("RoundTrip = %v, %v; want context.Canceled", resp, err)
	}
	var re *RetryError
	if errors.As(err, &re) {
		t.Fatalf("err = %v; a cancelled caller is not 'retries exhausted'", err)
	}
	if f.count() != 1 || len(s.delays()) != 0 {
		t.Fatalf("%d calls, sleeps %v; want 1 call and no backoff", f.count(), s.delays())
	}
}

func TestStage1_CancellationDuringBackoffReturnsPromptly(t *testing.T) {
	s := &fakeSleeper{block: true, entered: make(chan struct{}, 1)}
	f := newFake(status(503))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := NewRetrier(retryCfg(s)).Wrap(f).RoundTrip(newReqCtx(t, ctx, "GET", ""))
		done <- result{resp, err}
	}()
	select {
	case <-s.entered:
	case r := <-done:
		t.Fatalf("RoundTrip returned %v, %v before the retrier reached its backoff sleep (a 503 on a GET must be retried)", r.resp, r.err)
	case <-time.After(testTimeout):
		t.Fatal("the retrier never reached its backoff sleep")
	}
	cancel()
	select {
	case r := <-done:
		if r.resp != nil || !errors.Is(r.err, context.Canceled) {
			t.Fatalf("RoundTrip = %v, %v; want nil, context.Canceled", r.resp, r.err)
		}
	case <-time.After(testTimeout):
		t.Fatal("RoundTrip did not return after the context was cancelled during backoff (is the sleep ctx-aware?)")
	}
	if f.count() != 1 {
		t.Fatalf("%d calls, want 1: no attempt may start after cancellation", f.count())
	}
	if out := f.bt.outstanding(); out != 0 {
		t.Fatalf("%d response bodies left open", out)
	}
}

func TestStage1_ZeroConfigMeansASingleAttempt(t *testing.T) {
	f := newFake(status(503))
	resp, err := send(t, NewRetrier(RetryConfig{}).Wrap(f), newReq(t, "GET", ""))
	if err != nil || resp.StatusCode != 503 || f.count() != 1 {
		t.Fatalf("zero RetryConfig: %v, %v, %d calls; want one attempt and the 503 returned", resp, err, f.count())
	}
	closeResp(resp)
}

func TestStage1_ConcurrentRequestsShareOneRetrier(t *testing.T) {
	// Every request whose X-Seq is divisible by 3 fails once with 503, then succeeds.
	var mu sync.Mutex
	seen := map[string]int{}
	f := newFake()
	f.handler = func(n int, req *http.Request) (*http.Response, error) {
		seq := req.Header.Get("X-Seq")
		mu.Lock()
		seen[seq]++
		attempt := seen[seq]
		mu.Unlock()
		if attempt == 1 && req.Header.Get("X-Flaky") == "1" {
			return f.respond(req, status(503))
		}
		return f.respond(req, ok())
	}
	s := &fakeSleeper{}
	cfg := retryCfg(s)
	var r atomic.Int64
	cfg.Rand = func() float64 { return float64(r.Add(1)%100) / 100 }
	rt := NewRetrier(cfg).Wrap(f)

	const workers, per = 20, 25
	var wg sync.WaitGroup
	var bad atomic.Int64
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range per {
				seq := w*per + i
				flaky := "0"
				if seq%3 == 0 {
					flaky = "1"
				}
				req, _ := http.NewRequest("GET", "http://vendor.test/x", nil)
				req.Header.Set("X-Seq", fmt.Sprint(seq))
				req.Header.Set("X-Flaky", flaky)
				resp, err := rt.RoundTrip(req)
				if err != nil || resp.StatusCode != 200 {
					bad.Add(1)
				}
				closeResp(resp)
			}
		}()
	}
	within(t, "concurrent requests", wg.Wait)
	if bad.Load() != 0 {
		t.Fatalf("%d requests did not end in 200", bad.Load())
	}
	const total = workers * per
	flaky := (total + 2) / 3
	if f.count() != total+flaky {
		t.Fatalf("%d transport calls, want %d (%d requests + %d single retries)", f.count(), total+flaky, total, flaky)
	}
	if out := f.bt.outstanding(); out != 0 {
		t.Fatalf("%d bodies left open", out)
	}
}
