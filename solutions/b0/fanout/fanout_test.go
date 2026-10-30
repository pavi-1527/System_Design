package fanout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

var req = Request{From: "USD", To: "EUR", AmountMinor: 100_00}

// fakeVendor lets each test script a vendor's behaviour.
type fakeVendor struct {
	name  string
	calls atomic.Int32
	fn    func(ctx context.Context) (Quote, error)
}

func (f *fakeVendor) Name() string { return f.name }
func (f *fakeVendor) Quote(ctx context.Context, _ Request) (Quote, error) {
	f.calls.Add(1)
	return f.fn(ctx)
}

func fast(name string, rate int64) *fakeVendor {
	return &fakeVendor{name: name, fn: func(context.Context) (Quote, error) {
		return Quote{Vendor: name, RateMicros: rate}, nil
	}}
}

func failing(name string, err error) *fakeVendor {
	return &fakeVendor{name: name, fn: func(context.Context) (Quote, error) { return Quote{}, err }}
}

// slow takes d unless ctx ends first (a well-behaved adapter).
func slow(name string, d time.Duration, rate int64) *fakeVendor {
	return &fakeVendor{name: name, fn: func(ctx context.Context) (Quote, error) {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return Quote{Vendor: name, RateMicros: rate}, nil
		case <-ctx.Done():
			return Quote{}, ctx.Err()
		}
	}}
}

// stubborn ignores ctx entirely and returns only when release is closed
// (an adapter built on a client with no timeout, e.g. a raw net.Conn read).
func stubborn(name string, release <-chan struct{}) *fakeVendor {
	return &fakeVendor{name: name, fn: func(context.Context) (Quote, error) {
		<-release
		return Quote{Vendor: name, RateMicros: 1}, nil
	}}
}

func vendors(vs ...*fakeVendor) []Vendor {
	out := make([]Vendor, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

type quoteAllResult struct {
	results []Result
	err     error
	elapsed time.Duration
}

// quoteAll runs QuoteAll with a hard test-side timeout, so an implementation
// that waits on a stubborn vendor fails instead of hanging.
func quoteAll(t *testing.T, ctx context.Context, vs []Vendor, opts Options) quoteAllResult {
	t.Helper()
	ch := make(chan quoteAllResult, 1)
	start := time.Now()
	go func() {
		r, err := QuoteAll(ctx, vs, req, opts)
		ch <- quoteAllResult{r, err, time.Since(start)}
	}()
	select {
	case r := <-ch:
		if len(r.results) != len(vs) {
			t.Fatalf("QuoteAll returned %d results for %d vendors (err=%v); results must be index-aligned with vendors, always", len(r.results), len(vs), r.err)
		}
		for i, res := range r.results {
			if res.Vendor != vs[i].Name() {
				t.Fatalf("results[%d].Vendor = %q, want %q (index-aligned)", i, res.Vendor, vs[i].Name())
			}
		}
		return r
	case <-time.After(testTimeout):
		t.Fatalf("QuoteAll did not return within %v: it is waiting on a vendor it should have abandoned", testTimeout)
		return quoteAllResult{}
	}
}

func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			var buf bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
			t.Fatalf("goroutine leak: %d running vs baseline %d. Live stacks:\n%s", runtime.NumGoroutine(), baseline, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestQuoteAll_AllSucceed_ResultsIndexAligned(t *testing.T) {
	// Completion order is the reverse of vendor order.
	vs := vendors(slow("a", 30*time.Millisecond, 1), slow("b", 20*time.Millisecond, 2), slow("c", 10*time.Millisecond, 3), fast("d", 4))
	r := quoteAll(t, context.Background(), vs, Options{})
	if r.err != nil {
		t.Fatalf("QuoteAll error = %v, want nil", r.err)
	}
	for i, res := range r.results {
		if res.Err != nil || res.Quote.RateMicros != int64(i+1) {
			t.Fatalf("results[%d] = %+v, want rate %d and no error", i, res, i+1)
		}
	}
}

func TestQuoteAll_CallsVendorsConcurrently(t *testing.T) {
	// 10 vendors × 100 ms: sequential would take ≈ 1 s.
	var vs []*fakeVendor
	for i := 0; i < 10; i++ {
		vs = append(vs, slow(fmt.Sprintf("v%d", i), 100*time.Millisecond, 1))
	}
	r := quoteAll(t, context.Background(), vendors(vs...), Options{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.elapsed > 800*time.Millisecond {
		t.Fatalf("10 vendors × 100ms took %v: calls look sequential", r.elapsed)
	}
}

func TestQuoteAll_PerVendorTimeout_PartialResults(t *testing.T) {
	vs := vendors(fast("a", 1), slow("slow", time.Hour, 2), fast("c", 3))
	r := quoteAll(t, context.Background(), vs, Options{PerVendorTimeout: 50 * time.Millisecond})
	if r.err != nil {
		t.Fatalf("BestEffort with 2 of 3 quotes returned error %v, want nil (partial results are the point)", r.err)
	}
	if err := r.results[1].Err; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow vendor Err = %v, want context.DeadlineExceeded", err)
	}
	if r.results[0].Err != nil || r.results[2].Err != nil {
		t.Fatalf("fast vendors failed: %+v", r.results)
	}
}

func TestQuoteAll_VendorIgnoringContext_IsAbandoned_NoLeakAfterItReturns(t *testing.T) {
	baseline := runtime.NumGoroutine()
	release := make(chan struct{})
	vs := vendors(fast("a", 1), stubborn("stubborn", release))
	r := quoteAll(t, context.Background(), vs, Options{PerVendorTimeout: 50 * time.Millisecond})
	// We got here while the stubborn vendor is STILL blocked.
	if r.err != nil {
		t.Fatalf("err = %v, want nil (one good quote is enough)", r.err)
	}
	if err := r.results[1].Err; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stubborn vendor Err = %v, want context.DeadlineExceeded", err)
	}
	close(release) // the stubborn call finally returns...
	// ...and its goroutine must be able to exit (deliver into a buffered
	// channel, not block on an unbuffered one nobody reads any more).
	waitForGoroutines(t, baseline)
}

func TestQuoteAll_OverallDeadline_BoundsEverything(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	vs := vendors(fast("a", 1), slow("slow", time.Hour, 2), stubborn("stubborn", release))
	r := quoteAll(t, ctx, vs, Options{}) // no per-vendor timeout: only the overall deadline
	if r.err != nil {
		t.Fatalf("err = %v, want nil (one quote arrived before the deadline)", r.err)
	}
	for _, i := range []int{1, 2} {
		if !errors.Is(r.results[i].Err, context.DeadlineExceeded) {
			t.Errorf("results[%d].Err = %v, want context.DeadlineExceeded", i, r.results[i].Err)
		}
	}
}

func TestQuoteAll_FailFast_CancelsSiblingsAndReturnsFirstError(t *testing.T) {
	errDown := errors.New("vendor b: 503 Service Unavailable")
	var sawCancel atomic.Int32
	waiter := func(name string) *fakeVendor {
		return &fakeVendor{name: name, fn: func(ctx context.Context) (Quote, error) {
			select {
			case <-ctx.Done():
				sawCancel.Add(1)
				return Quote{}, ctx.Err()
			case <-time.After(time.Hour):
				return Quote{}, nil
			}
		}}
	}
	release := make(chan struct{})
	defer close(release)
	a, c := waiter("a"), waiter("c")
	vs := vendors(a, failing("b", errDown), c, stubborn("d", release))
	r := quoteAll(t, context.Background(), vs, Options{Policy: FailFast})
	if !errors.Is(r.err, errDown) {
		t.Fatalf("FailFast err = %v, want it to wrap %v", r.err, errDown)
	}
	for _, i := range []int{0, 2, 3} {
		if !errors.Is(r.results[i].Err, context.Canceled) {
			t.Errorf("results[%d].Err = %v, want context.Canceled (sibling failed)", i, r.results[i].Err)
		}
	}
	// A sibling may legitimately never be called (ctx already cancelled when
	// its turn came) or be abandoned a moment before its own select returns.
	// What must hold: every well-behaved vendor that WAS called eventually
	// sees its ctx cancelled, i.e. nobody keeps waiting for an hour.
	deadline := time.Now().Add(testTimeout)
	for sawCancel.Load() != a.calls.Load()+c.calls.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d called well-behaved vendors observed cancellation; their ctx must be cancelled on the first error",
				sawCancel.Load(), a.calls.Load()+c.calls.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestQuoteAll_BestEffort_InsufficientQuotes(t *testing.T) {
	e1, e2 := errors.New("timeout talking to a"), errors.New("bad auth at b")
	r := quoteAll(t, context.Background(), vendors(failing("a", e1), failing("b", e2)), Options{})
	if !errors.Is(r.err, ErrInsufficientQuotes) {
		t.Fatalf("err = %v, want ErrInsufficientQuotes", r.err)
	}
	if !errors.Is(r.err, e1) || !errors.Is(r.err, e2) {
		t.Errorf("err = %v; it should also wrap every vendor error (errors.Join) so on-call can see why", r.err)
	}
	r = quoteAll(t, context.Background(), vendors(fast("a", 1), failing("b", e2), fast("c", 3)), Options{MinSuccesses: 3})
	if !errors.Is(r.err, ErrInsufficientQuotes) {
		t.Fatalf("MinSuccesses=3 with 2 successes: err = %v, want ErrInsufficientQuotes", r.err)
	}
	r = quoteAll(t, context.Background(), nil, Options{})
	if !errors.Is(r.err, ErrInsufficientQuotes) {
		t.Fatalf("no vendors: err = %v, want ErrInsufficientQuotes", r.err)
	}
}

func TestQuoteAll_MaxConcurrency_Bounded(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	var vs []*fakeVendor
	for i := 0; i < 20; i++ {
		vs = append(vs, &fakeVendor{name: fmt.Sprintf("v%d", i), fn: func(ctx context.Context) (Quote, error) {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				m := maxInFlight.Load()
				if n <= m || maxInFlight.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			return Quote{RateMicros: 1}, nil
		}})
	}
	r := quoteAll(t, context.Background(), vendors(vs...), Options{MaxConcurrency: 3})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if m := maxInFlight.Load(); m > 3 {
		t.Fatalf("%d vendor calls in flight at once with MaxConcurrency 3", m)
	}
	for i, res := range r.results {
		if res.Err != nil {
			t.Fatalf("results[%d].Err = %v", i, res.Err)
		}
	}
}

func TestQuoteAll_PerVendorTimeout_StartsWhenTheCallStarts(t *testing.T) {
	// 12 vendors × 50 ms, one at a time ≈ 600 ms total. Each call is well
	// inside its 400 ms budget, but a timeout started when QuoteAll started
	// would kill every call that begins after 400 ms.
	var vs []*fakeVendor
	for i := 0; i < 12; i++ {
		vs = append(vs, slow(fmt.Sprintf("v%d", i), 50*time.Millisecond, 1))
	}
	r := quoteAll(t, context.Background(), vendors(vs...), Options{MaxConcurrency: 1, PerVendorTimeout: 400 * time.Millisecond, MinSuccesses: 12})
	if r.err != nil {
		for i, res := range r.results {
			if res.Err != nil {
				t.Logf("results[%d].Err = %v", i, res.Err)
			}
		}
		t.Fatalf("err = %v: queued calls timed out although each took 50ms of a 400ms per-call budget", r.err)
	}
}

func TestQuoteAll_QueuedVendorsNotCalledAfterDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	first := slow("first", time.Hour, 1)
	var queued []*fakeVendor
	for i := 0; i < 5; i++ {
		queued = append(queued, fast(fmt.Sprintf("q%d", i), 1))
	}
	r := quoteAll(t, ctx, vendors(append([]*fakeVendor{first}, queued...)...), Options{MaxConcurrency: 1})
	// Which vendor wins the single slot first is up to the scheduler, so
	// only check what must hold regardless: nobody is called after the
	// deadline, and every vendor that was not answered got the ctx error.
	_ = r
	for i, v := range queued {
		res := r.results[i+1]
		if res.Err == nil {
			continue // it got the slot before "first" did
		}
		if !errors.Is(res.Err, context.DeadlineExceeded) {
			t.Errorf("queued vendor %d Err = %v, want context.DeadlineExceeded", i, res.Err)
		}
		if v.calls.Load() != 0 {
			t.Errorf("queued vendor %d was called after the deadline (calls=%d): wasted vendor quota", i, v.calls.Load())
		}
	}
	if !errors.Is(r.results[0].Err, context.DeadlineExceeded) {
		t.Errorf("first vendor Err = %v, want context.DeadlineExceeded", r.results[0].Err)
	}
}

func TestQuoteAll_PreCancelledContext_CallsNobody(t *testing.T) {
	// A client that already disconnected: no vendor should see a request.
	// With MaxConcurrency, every waiter's select has BOTH a free slot and a
	// closed Done channel ready; select picks at random, so an implementation
	// that does not re-check ctx after acquiring calls some vendors.
	for _, limit := range []int{0, 1} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var vs []*fakeVendor
		for i := 0; i < 10; i++ {
			vs = append(vs, fast(fmt.Sprintf("v%d", i), 1))
		}
		r := quoteAll(t, ctx, vendors(vs...), Options{MaxConcurrency: limit})
		if !errors.Is(r.err, ErrInsufficientQuotes) {
			t.Errorf("MaxConcurrency=%d: err = %v, want ErrInsufficientQuotes", limit, r.err)
		}
		for i, v := range vs {
			if n := v.calls.Load(); n != 0 {
				t.Errorf("MaxConcurrency=%d: vendor %d called %d times on an already-cancelled context", limit, i, n)
			}
			if !errors.Is(r.results[i].Err, context.Canceled) {
				t.Errorf("MaxConcurrency=%d: results[%d].Err = %v, want context.Canceled", limit, i, r.results[i].Err)
			}
		}
	}
}

func TestQuoteAll_VendorPanic_IsContained(t *testing.T) {
	boom := &fakeVendor{name: "boom", fn: func(context.Context) (Quote, error) {
		var m map[string]int
		m["x"] = 1 // nil map write: a real adapter bug
		return Quote{}, nil
	}}
	r := quoteAll(t, context.Background(), vendors(fast("a", 1), boom), Options{})
	if r.err != nil {
		t.Fatalf("err = %v, want nil (one good quote)", r.err)
	}
	if !errors.Is(r.results[1].Err, ErrVendorPanic) {
		t.Fatalf("panicking vendor Err = %v, want ErrVendorPanic", r.results[1].Err)
	}
}

func TestQuoteAll_ConcurrentCallers_ShareVendors(t *testing.T) {
	vs := vendors(fast("a", 1), slow("b", 5*time.Millisecond, 2), failing("c", errors.New("nope")))
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := QuoteAll(context.Background(), vs, req, Options{MaxConcurrency: 2, PerVendorTimeout: time.Second})
			if err != nil {
				errs <- err
				return
			}
			if len(res) != 3 || res[0].Err != nil || res[1].Err != nil || res[2].Err == nil {
				errs <- fmt.Errorf("unexpected results %+v", res)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("concurrent QuoteAll callers did not finish")
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
