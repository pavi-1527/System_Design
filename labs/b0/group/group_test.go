package group

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// waitWithTimeout calls g.Wait in a goroutine so a broken Wait fails the
// test instead of hanging it.
func waitWithTimeout(t *testing.T, g *Group) error {
	t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- g.Wait() }()
	select {
	case err := <-errc:
		return err
	case <-time.After(testTimeout):
		t.Fatalf("Wait did not return within %v", testTimeout)
		return nil
	}
}

func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			var buf bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
			t.Fatalf("goroutine leak: %d running, baseline %d. Live stacks:\n%s", runtime.NumGoroutine(), baseline, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// within runs f in a goroutine and fails the test if f has not returned
// within testTimeout. Used around calls to Go that may block on the limit, so
// an implementation that never frees slots fails instead of hanging.
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
		t.Fatalf("%s did not finish within %v: Go is blocked on the limit (are slots released when a function returns?)", what, testTimeout)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for: %s", testTimeout, what)
		}
		time.Sleep(time.Millisecond)
	}
}

// requireBasics fails fast (instead of after a 5 s timeout) when Go does not
// run its function or Wait does not wait for it, e.g. on the unimplemented stub.
func requireBasics(t *testing.T) {
	t.Helper()
	var g Group
	var ran atomic.Bool
	g.Go(func() error {
		time.Sleep(time.Millisecond)
		ran.Store(true)
		return nil
	})
	_ = waitWithTimeout(t, &g)
	if !ran.Load() {
		t.Fatal("precondition: Wait returned before the function passed to Go had run; implement Go and Wait first")
	}
}

func TestZeroValue_RunsAllAndWaits(t *testing.T) {
	var g Group
	var n atomic.Int32
	for i := 0; i < 100; i++ {
		g.Go(func() error {
			time.Sleep(time.Millisecond)
			n.Add(1)
			return nil
		})
	}
	if err := waitWithTimeout(t, &g); err != nil {
		t.Fatalf("Wait() = %v, want nil", err)
	}
	if got := n.Load(); got != 100 {
		t.Fatalf("after Wait, %d of 100 functions had finished: Wait must block until every Go'd function returns", got)
	}
}

func TestWait_ReturnsFirstError(t *testing.T) {
	requireBasics(t)
	g, ctx := WithContext(context.Background())
	errFirst := errors.New("vendor A: 503")
	g.Go(func() error { return errFirst })
	for i := 0; i < 5; i++ {
		g.Go(func() error {
			// These only fail AFTER the first error cancelled the context,
			// so errFirst is unambiguously first.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(testTimeout):
				return errors.New("context was never cancelled")
			}
		})
	}
	err := waitWithTimeout(t, g)
	if !errors.Is(err, errFirst) {
		t.Fatalf("Wait() = %v, want the first error %v", err, errFirst)
	}
}

func TestWithContext_CancelledOnFirstError_WithCause(t *testing.T) {
	requireBasics(t)
	g, ctx := WithContext(context.Background())
	boom := errors.New("boom")
	started := make(chan struct{})
	g.Go(func() error {
		<-started
		return boom
	})
	g.Go(func() error {
		close(started)
		<-ctx.Done() // a well-behaved sibling stops when the group fails
		return nil
	})
	select {
	case <-ctx.Done():
	case <-time.After(testTimeout):
		t.Fatal("group context was not cancelled after a function returned an error: siblings keep burning vendor quota")
	}
	if c := context.Cause(ctx); !errors.Is(c, boom) {
		t.Errorf("context.Cause(ctx) = %v, want %v (use context.WithCancelCause so callers can see WHY)", c, boom)
	}
	if err := waitWithTimeout(t, g); !errors.Is(err, boom) {
		t.Fatalf("Wait() = %v, want %v", err, boom)
	}
}

func TestWithContext_CancelledWhenWaitReturnsEvenWithoutError(t *testing.T) {
	g, ctx := WithContext(context.Background())
	g.Go(func() error { return nil })
	if err := waitWithTimeout(t, g); err != nil {
		t.Fatalf("Wait() = %v, want nil", err)
	}
	if ctx.Err() == nil {
		t.Fatal("group context still live after Wait returned: anything derived from it (timers, goroutines) leaks until the parent is cancelled")
	}
}

func TestWithContext_ParentCancellationPropagates(t *testing.T) {
	requireBasics(t)
	parent, cancel := context.WithCancel(context.Background())
	g, ctx := WithContext(parent)
	g.Go(func() error {
		<-ctx.Done()
		return ctx.Err()
	})
	cancel()
	if err := waitWithTimeout(t, g); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() = %v, want context.Canceled from the parent", err)
	}
}

func TestSetLimit_BoundsConcurrency(t *testing.T) {
	requireBasics(t)
	var g Group
	g.SetLimit(3)
	var inFlight, maxInFlight atomic.Int32
	within(t, "starting 30 functions with SetLimit(3)", func() {
		for i := 0; i < 30; i++ {
			g.Go(func() error {
				n := inFlight.Add(1)
				for {
					m := maxInFlight.Load()
					if n <= m || maxInFlight.CompareAndSwap(m, n) {
						break
					}
				}
				time.Sleep(2 * time.Millisecond)
				inFlight.Add(-1)
				return nil
			})
		}
	})
	if err := waitWithTimeout(t, &g); err != nil {
		t.Fatal(err)
	}
	if m := maxInFlight.Load(); m > 3 {
		t.Fatalf("max concurrent functions = %d with SetLimit(3): the vendor's 3-connection limit was exceeded", m)
	}
	if m := maxInFlight.Load(); m < 1 {
		t.Fatalf("no function ever ran")
	}
}

func TestGo_BlocksAtLimitUntilSlotFrees(t *testing.T) {
	requireBasics(t)
	var g Group
	g.SetLimit(2)
	release := make(chan struct{})
	var running atomic.Int32
	for i := 0; i < 2; i++ {
		g.Go(func() error {
			running.Add(1)
			<-release
			return nil
		})
	}
	eventually(t, "both limited goroutines to start", func() bool { return running.Load() == 2 })

	thirdStarted := make(chan struct{})
	goReturned := make(chan struct{})
	go func() {
		g.Go(func() error { close(thirdStarted); return nil })
		close(goReturned)
	}()
	select {
	case <-goReturned:
		t.Fatal("Go returned while the group was at its limit; it must block until a slot frees")
	case <-thirdStarted:
		t.Fatal("a third goroutine started with SetLimit(2)")
	case <-time.After(50 * time.Millisecond):
		// still blocked: correct
	}
	close(release)
	select {
	case <-thirdStarted:
	case <-time.After(testTimeout):
		t.Fatal("third function never started after slots were released: slots are not given back when a function returns")
	}
	if err := waitWithTimeout(t, &g); err != nil {
		t.Fatal(err)
	}
}

func TestTryGo_RespectsLimit(t *testing.T) {
	requireBasics(t)
	var g Group
	g.SetLimit(1)
	release := make(chan struct{})
	if !g.TryGo(func() error { <-release; return nil }) {
		t.Fatal("TryGo returned false on an idle group with limit 1")
	}
	if g.TryGo(func() error { return nil }) {
		t.Fatal("TryGo returned true while the only slot was busy")
	}
	close(release)
	eventually(t, "TryGo to succeed once the slot is released", func() bool {
		return g.TryGo(func() error { return nil })
	})
	if err := waitWithTimeout(t, &g); err != nil {
		t.Fatal(err)
	}
}

func TestTryGo_NoLimitAlwaysStarts(t *testing.T) {
	var g Group
	var n atomic.Int32
	for i := 0; i < 50; i++ {
		if !g.TryGo(func() error { n.Add(1); return nil }) {
			t.Fatal("TryGo returned false with no limit set")
		}
	}
	if err := waitWithTimeout(t, &g); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 50 {
		t.Fatalf("%d of 50 TryGo functions ran", n.Load())
	}
}

func TestSetLimit_ZeroPreventsNewGoroutines_NegativeMeansUnlimited(t *testing.T) {
	var g Group
	g.SetLimit(0)
	if g.TryGo(func() error { return nil }) {
		t.Fatal("TryGo succeeded with SetLimit(0)")
	}
	g.SetLimit(-1)
	var n atomic.Int32
	for i := 0; i < 10; i++ {
		g.Go(func() error { n.Add(1); return nil })
	}
	if err := waitWithTimeout(t, &g); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 10 {
		t.Fatalf("%d of 10 ran with SetLimit(-1)", n.Load())
	}
}

func TestConcurrentGoCalls_AllCountedNoLeak(t *testing.T) {
	requireBasics(t)
	baseline := runtime.NumGoroutine()
	g, _ := WithContext(context.Background())
	g.SetLimit(4)
	var n atomic.Int32
	var callers sync.WaitGroup
	for c := 0; c < 8; c++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			for i := 0; i < 50; i++ {
				g.Go(func() error { n.Add(1); return nil })
			}
		}()
	}
	within(t, "8 callers each starting 50 functions with SetLimit(4)", callers.Wait)
	if err := waitWithTimeout(t, g); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 400 {
		t.Fatalf("%d of 400 functions ran", n.Load())
	}
	waitForGoroutines(t, baseline)
}
