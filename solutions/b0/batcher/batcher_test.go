package batcher

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"runtime/pprof"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// collector is a flush func that records batches and exposes them on a channel.
type collector struct {
	mu      sync.Mutex
	batches [][]int
	ch      chan []int
}

func newCollector() *collector { return &collector{ch: make(chan []int, 1000)} }

func (c *collector) flush(b []int) {
	c.mu.Lock()
	c.batches = append(c.batches, b)
	c.mu.Unlock()
	c.ch <- b
}

func (c *collector) all() [][]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]int(nil), c.batches...)
}

func (c *collector) next(t *testing.T, what string) []int {
	t.Helper()
	select {
	case b := <-c.ch:
		return b
	case <-time.After(testTimeout):
		t.Fatalf("timed out after %v waiting for %s", testTimeout, what)
		return nil
	}
}

func (c *collector) expectNoFlush(t *testing.T, why string) {
	t.Helper()
	select {
	case b := <-c.ch:
		t.Fatalf("unexpected flush of %v: %s", b, why)
	default:
	}
}

// fakeClock lets tests decide exactly when a linger timer fires.
type fakeTimer struct {
	d       time.Duration
	c       chan time.Time
	stopped atomic.Bool
}

type fakeClock struct{ created chan *fakeTimer }

func newFakeClock() *fakeClock { return &fakeClock{created: make(chan *fakeTimer, 100)} }

func (f *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	ft := &fakeTimer{d: d, c: make(chan time.Time, 1)}
	f.created <- ft
	return ft.c, func() bool { return !ft.stopped.Swap(true) }
}

func (f *fakeClock) nextTimer(t *testing.T) *fakeTimer {
	t.Helper()
	select {
	case ft := <-f.created:
		return ft
	case <-time.After(testTimeout):
		t.Fatal("no linger timer was created after the first item of a batch (did you use Config.NewTimer?)")
		return nil
	}
}

func (ft *fakeTimer) fire() { ft.c <- time.Now() }

func mustNew(t *testing.T, cfg Config, flush func([]int)) *Batcher[int] {
	t.Helper()
	b, err := New(cfg, flush)
	if err != nil {
		t.Fatalf("New(%+v) returned error %v", cfg, err)
	}
	if b == nil {
		t.Fatal("New returned a nil *Batcher with a nil error")
	}
	return b
}

func closeWithTimeout(t *testing.T, b *Batcher[int]) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- b.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close() = %v, want nil", err)
		}
	case <-time.After(testTimeout):
		t.Fatalf("Close did not return within %v (run loop stuck, or Close waits on something that never happens)", testTimeout)
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
			t.Fatalf("goroutine leak: %d goroutines still running, baseline was %d. Live stacks:\n%s", n, baseline, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNew_RejectsInvalidConfig(t *testing.T) {
	flush := func([]int) {}
	for _, cfg := range []Config{{MaxItems: 0, Linger: time.Second}, {MaxItems: 10, Linger: 0}, {MaxItems: -1, Linger: -1}} {
		b, err := New(cfg, flush)
		if err == nil {
			_ = b.Close()
			t.Errorf("New(%+v) returned nil error; a batcher that never flushes by size or time silently loses latency guarantees", cfg)
		}
	}
	if _, err := New[int](Config{MaxItems: 1, Linger: time.Second}, nil); err == nil {
		t.Error("New with a nil flush func returned nil error")
	}
}

func TestFlush_WhenFull_PreservesOrderAndOwnership(t *testing.T) {
	c := newCollector()
	b := mustNew(t, Config{MaxItems: 3, Linger: time.Hour}, c.flush)
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		if err := b.Add(ctx, i); err != nil {
			t.Fatalf("Add(%d) = %v", i, err)
		}
	}
	if got := c.next(t, "first full batch"); !equal(got, []int{0, 1, 2}) {
		t.Fatalf("first batch = %v, want [0 1 2]", got)
	}
	if got := c.next(t, "second full batch"); !equal(got, []int{3, 4, 5}) {
		t.Fatalf("second batch = %v, want [3 4 5]", got)
	}
	closeWithTimeout(t, b)
	if got := c.next(t, "remainder flushed by Close"); !equal(got, []int{6}) {
		t.Fatalf("remainder batch = %v, want [6]", got)
	}
	// Ownership: earlier batches must not have been overwritten by reuse of
	// the same backing array (batch = batch[:0] is the classic bug).
	all := c.all()
	want := [][]int{{0, 1, 2}, {3, 4, 5}, {6}}
	if len(all) != len(want) {
		t.Fatalf("got %d batches %v, want %v", len(all), all, want)
	}
	for i := range want {
		if !equal(all[i], want[i]) {
			t.Fatalf("batch %d is now %v, want %v: the batcher mutated a slice it had already handed to flush", i, all[i], want[i])
		}
	}
}

func TestFlush_AfterLinger_MeasuredFromFirstItem(t *testing.T) {
	c := newCollector()
	clk := newFakeClock()
	b := mustNew(t, Config{MaxItems: 100, Linger: 250 * time.Millisecond, NewTimer: clk.NewTimer}, c.flush)
	defer closeWithTimeout(t, b)
	ctx := context.Background()

	if err := b.Add(ctx, 1); err != nil {
		t.Fatal(err)
	}
	ft := clk.nextTimer(t)
	if ft.d != 250*time.Millisecond {
		t.Fatalf("linger timer duration = %v, want 250ms (Config.Linger)", ft.d)
	}
	if err := b.Add(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(ctx, 3); err != nil {
		t.Fatal(err)
	}
	select {
	case extra := <-clk.created:
		t.Fatalf("a second timer (%v) was started for the same batch: linger must be measured from the FIRST item, not reset by every Add", extra.d)
	default:
	}
	c.expectNoFlush(t, "batch is not full and the linger timer has not fired")

	ft.fire()
	if got := c.next(t, "flush after linger timer fired"); !equal(got, []int{1, 2, 3}) {
		t.Fatalf("linger batch = %v, want [1 2 3]", got)
	}

	// The next batch starts a fresh timer on its first item.
	if err := b.Add(ctx, 4); err != nil {
		t.Fatal(err)
	}
	ft2 := clk.nextTimer(t)
	ft2.fire()
	if got := c.next(t, "second linger flush"); !equal(got, []int{4}) {
		t.Fatalf("second linger batch = %v, want [4]", got)
	}
}

func TestFlush_SizeFlushStopsTimer_StaleFireIgnored(t *testing.T) {
	c := newCollector()
	clk := newFakeClock()
	b := mustNew(t, Config{MaxItems: 2, Linger: time.Second, NewTimer: clk.NewTimer}, c.flush)
	defer closeWithTimeout(t, b)
	ctx := context.Background()
	_ = b.Add(ctx, 1)
	ft := clk.nextTimer(t)
	_ = b.Add(ctx, 2) // fills the batch: flush by size
	if got := c.next(t, "size flush"); !equal(got, []int{1, 2}) {
		t.Fatalf("size batch = %v, want [1 2]", got)
	}
	if !ft.stopped.Load() {
		t.Error("the linger timer of a batch flushed by size was not stopped (a leaked timer per batch adds up at 2k batches/s)")
	}
	_ = b.Add(ctx, 3)
	ft2 := clk.nextTimer(t)
	ft.fire() // a stale fire from the old batch's timer must not flush the new batch
	c.expectNoFlush(t, "a stale timer from an already-flushed batch fired")
	ft2.fire()
	if got := c.next(t, "flush by the new batch's own timer"); !equal(got, []int{3}) {
		t.Fatalf("batch = %v, want [3]", got)
	}
}

func TestFlush_RealClockDefault(t *testing.T) {
	c := newCollector()
	b := mustNew(t, Config{MaxItems: 100, Linger: 20 * time.Millisecond}, c.flush)
	defer closeWithTimeout(t, b)
	if err := b.Add(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if got := c.next(t, "linger flush with the real clock (NewTimer nil)"); !equal(got, []int{42}) {
		t.Fatalf("batch = %v, want [42]", got)
	}
}

func TestClose_FlushesRemainder_Idempotent_Concurrent(t *testing.T) {
	c := newCollector()
	b := mustNew(t, Config{MaxItems: 100, Linger: time.Hour}, c.flush)
	_ = b.Add(context.Background(), 1)
	_ = b.Add(context.Background(), 2)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// t.Fatal must not be called from a non-test goroutine, so report with Errorf.
			done := make(chan error, 1)
			go func() { done <- b.Close() }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("concurrent Close() = %v, want nil", err)
				}
			case <-time.After(testTimeout):
				t.Errorf("concurrent Close did not return within %v", testTimeout)
				return
			}
			// Every Close must return only after the final flush happened.
			if n := len(c.all()); n != 1 {
				t.Errorf("Close returned but %d batches were flushed, want 1 (Close must wait for the final flush)", n)
			}
		}()
	}
	wg.Wait()
	closeWithTimeout(t, b) // and once more, sequentially
	if all := c.all(); len(all) != 1 || !equal(all[0], []int{1, 2}) {
		t.Fatalf("batches after Close = %v, want [[1 2]] exactly once", all)
	}
}

func TestAdd_AfterClose_ReturnsErrClosed(t *testing.T) {
	c := newCollector()
	b := mustNew(t, Config{MaxItems: 10, Linger: time.Hour}, c.flush)
	closeWithTimeout(t, b)
	for i := 0; i < 100; i++ {
		if err := b.Add(context.Background(), i); !errors.Is(err, ErrClosed) {
			t.Fatalf("Add after Close = %v, want ErrClosed (callers must learn their event was NOT recorded)", err)
		}
	}
	if all := c.all(); len(all) != 0 {
		t.Fatalf("items added after Close were flushed: %v", all)
	}
}

func TestAdd_RespectsContextUnderBackpressure(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 10)
	flush := func([]int) {
		entered <- struct{}{}
		<-release // a slow sink: the database is having a bad day
	}
	b := mustNew(t, Config{MaxItems: 1, Linger: time.Hour}, flush)
	if err := b.Add(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(testTimeout):
		t.Fatal("flush was never called for a full batch")
	}
	// The run loop is stuck in flush. A request handler calling Add must be
	// able to give up instead of hanging forever.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- b.Add(ctx, 2) }()
	select {
	case err := <-errc:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Add while the sink is stuck = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(testTimeout):
		close(release)
		t.Fatal("Add ignored its context while the batcher was blocked: request handlers would hang behind a slow audit sink")
	}
	close(release)
	closeWithTimeout(t, b)
}

func TestFlush_SingleGoroutine_NoLossUnderConcurrentAdds(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	var mu sync.Mutex
	var got []int
	flush := func(batch []int) {
		n := inFlight.Add(1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		mu.Lock()
		got = append(got, batch...)
		mu.Unlock()
		inFlight.Add(-1)
	}
	b := mustNew(t, Config{MaxItems: 10, Linger: time.Millisecond}, flush)
	const adders, perAdder = 16, 200
	var wg sync.WaitGroup
	for a := 0; a < adders; a++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perAdder; i++ {
				if err := b.Add(context.Background(), a*perAdder+i); err != nil {
					t.Errorf("Add = %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	closeWithTimeout(t, b)
	if m := maxInFlight.Load(); m > 1 {
		t.Fatalf("flush ran on %d goroutines at once; it must be called from a single goroutine", m)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != adders*perAdder {
		t.Fatalf("flushed %d items, want %d", len(got), adders*perAdder)
	}
	sort.Ints(got)
	for i, v := range got {
		if v != i {
			t.Fatalf("item %d missing or duplicated (got %d at position %d)", i, v, i)
		}
	}
}

// Adds racing with Close: every Add that returned nil must be flushed.
func TestClose_RacingAdds_NoAcceptedItemIsLost(t *testing.T) {
	for round := 0; round < 20; round++ {
		var flushed atomic.Int64
		b := mustNew(t, Config{MaxItems: 7, Linger: time.Millisecond}, func(batch []int) { flushed.Add(int64(len(batch))) })
		var accepted atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for a := 0; a < 8; a++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for i := 0; ; i++ {
					err := b.Add(context.Background(), i)
					if errors.Is(err, ErrClosed) {
						return
					}
					if err != nil {
						t.Errorf("Add = %v", err)
						return
					}
					accepted.Add(1)
				}
			}()
		}
		close(start)
		time.Sleep(time.Millisecond)
		closeWithTimeout(t, b)
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(testTimeout):
			t.Fatal("Add calls kept succeeding (or hung) after Close returned")
		}
		if a, f := accepted.Load(), flushed.Load(); a != f {
			t.Fatalf("round %d: %d Adds returned nil but %d items were flushed: accepted audit events were dropped on shutdown", round, a, f)
		}
	}
}

func TestNoGoroutineLeak_AfterClose(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		b := mustNew(t, Config{MaxItems: 5, Linger: 10 * time.Millisecond}, func([]int) {})
		for j := 0; j < 12; j++ {
			_ = b.Add(context.Background(), j)
		}
		closeWithTimeout(t, b)
	}
	waitForGoroutines(t, baseline)
}

func equal(a, b []int) bool {
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
