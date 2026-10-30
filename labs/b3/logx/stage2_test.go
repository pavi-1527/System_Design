package logx

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDst is a BatchSink for tests: it records batches, can block each call
// until released, and can inject failures.
type fakeDst struct {
	mu      sync.Mutex
	batches [][]Entry
	calls   int
	entered chan struct{} // one token per WriteBatch call that started
	release chan struct{} // nil: never blocks; otherwise closed by open()
	once    sync.Once
	fail    func(call int, batch []Entry) error
	panics  func(call int) any
	onBatch func(batch []Entry) // runs before the batch is recorded
}

func newDst() *fakeDst { return &fakeDst{entered: make(chan struct{}, 10000)} }

// newBlockedDst returns a destination whose WriteBatch blocks until open().
func newBlockedDst(t *testing.T) *fakeDst {
	t.Helper()
	d := newDst()
	d.release = make(chan struct{})
	t.Cleanup(d.open)
	return d
}

func (d *fakeDst) open() {
	if d.release == nil {
		return
	}
	d.once.Do(func() { close(d.release) })
}

func (d *fakeDst) WriteBatch(batch []Entry) error {
	d.mu.Lock()
	d.calls++
	n := d.calls
	d.mu.Unlock()
	select {
	case d.entered <- struct{}{}:
	default:
	}
	if d.release != nil {
		<-d.release
	}
	if d.panics != nil {
		if v := d.panics(n); v != nil {
			panic(v)
		}
	}
	if d.onBatch != nil {
		d.onBatch(batch)
	}
	if d.fail != nil {
		if err := d.fail(n, batch); err != nil {
			return err
		}
	}
	d.mu.Lock()
	d.batches = append(d.batches, batch)
	d.mu.Unlock()
	return nil
}

func (d *fakeDst) waitEntered(t *testing.T, what string) {
	t.Helper()
	select {
	case <-d.entered:
	case <-time.After(testTimeout):
		t.Fatalf("WriteBatch was not called within %v: %s", testTimeout, what)
	}
}

func (d *fakeDst) snapshot() [][]Entry {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]Entry(nil), d.batches...)
}

func (d *fakeDst) msgs() []string {
	var out []string
	for _, b := range d.snapshot() {
		for _, e := range b {
			out = append(out, e.Msg)
		}
	}
	return out
}

func (d *fakeDst) sizes() []int {
	var out []int
	for _, b := range d.snapshot() {
		out = append(out, len(b))
	}
	return out
}

func (d *fakeDst) waitBatches(t *testing.T, n int) [][]Entry {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		if bs := d.snapshot(); len(bs) >= n {
			return bs
		}
		if time.Now().After(deadline) {
			t.Fatalf("destination received %d batches (sizes %v), want at least %d within %v", len(d.snapshot()), d.sizes(), n, testTimeout)
		}
		time.Sleep(time.Millisecond)
	}
}

// expectNoMoreBatches asserts (after a short grace period) that nothing more arrived.
func (d *fakeDst) expectNoMoreBatches(t *testing.T, have int, why string) {
	t.Helper()
	time.Sleep(30 * time.Millisecond)
	if got := d.snapshot(); len(got) != have {
		t.Fatalf("destination has %d batches (sizes %v), want %d: %s", len(got), d.sizes(), have, why)
	}
}

// fakeTimers is an injectable linger clock: tests decide when a timer fires.
type fakeTimer struct {
	d       time.Duration
	c       chan time.Time
	stopped atomic.Bool
}

type fakeTimers struct{ created chan *fakeTimer }

func newFakeTimers() *fakeTimers { return &fakeTimers{created: make(chan *fakeTimer, 100)} }

func (f *fakeTimers) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	ft := &fakeTimer{d: d, c: make(chan time.Time, 1)}
	f.created <- ft
	return ft.c, func() bool { return !ft.stopped.Swap(true) }
}

func (f *fakeTimers) next(t *testing.T, what string) *fakeTimer {
	t.Helper()
	select {
	case ft := <-f.created:
		return ft
	case <-time.After(testTimeout):
		t.Fatalf("no linger timer was created: %s (did you use AsyncConfig.NewTimer?)", what)
		return nil
	}
}

func (ft *fakeTimer) fire() { ft.c <- time.Now() }

// neverTimers is a linger clock that never fires.
func neverTimers(time.Duration) (<-chan time.Time, func() bool) {
	return nil, func() bool { return true }
}

func entry(i int) Entry { return Entry{Level: Info, Msg: fmt.Sprintf("m%d", i)} }

func msgsOf(b []Entry) []string {
	var out []string
	for _, e := range b {
		out = append(out, e.Msg)
	}
	return out
}

func baseCfg() AsyncConfig {
	return AsyncConfig{QueueSize: 100, MaxBatch: 10, Linger: time.Hour, NewTimer: neverTimers}
}

// startAsync builds an AsyncSink and makes sure it is closed (and a blocked
// destination released) when the test ends.
func startAsync(t *testing.T, d *fakeDst, cfg AsyncConfig) *AsyncSink {
	t.Helper()
	a, err := NewAsync(d, cfg)
	if err != nil {
		t.Fatalf("NewAsync(%+v) = %v, want nil", cfg, err)
	}
	if a == nil {
		t.Fatal("NewAsync returned a nil *AsyncSink with a nil error")
	}
	t.Cleanup(func() {
		d.open()
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		_ = a.Close(ctx)
	})
	return a
}

func ctxTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), testTimeout)
}

func mustWrite(t *testing.T, a *AsyncSink, es ...Entry) {
	t.Helper()
	for _, e := range es {
		if err := a.Write(e); err != nil {
			t.Fatalf("Write(%q) = %v, want nil", e.Msg, err)
		}
	}
}

func mustFlush(t *testing.T, a *AsyncSink) {
	t.Helper()
	ctx, cancel := ctxTimeout()
	defer cancel()
	if err := a.Flush(ctx); err != nil {
		t.Fatalf("Flush = %v, want nil", err)
	}
}

func mustClose(t *testing.T, a *AsyncSink) {
	t.Helper()
	ctx, cancel := ctxTimeout()
	defer cancel()
	if err := a.Close(ctx); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
}

func waitGoroutines(t *testing.T, baseline int) {
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
			t.Fatalf("goroutine leak: %d running, baseline %d. Stacks:\n%s", n, baseline, buf)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---------------------------------------------------------------- Stage 2

func TestStage2_NewAsyncValidatesItsConfig(t *testing.T) {
	d := newDst()
	good := baseCfg()
	bad := map[string]func(*AsyncConfig){
		"QueueSize 0": func(c *AsyncConfig) { c.QueueSize = 0 },
		"MaxBatch 0":  func(c *AsyncConfig) { c.MaxBatch = 0 },
		"Linger 0":    func(c *AsyncConfig) { c.Linger = 0 },
		"bad Policy":  func(c *AsyncConfig) { c.Policy = Policy(42) },
	}
	for name, mutate := range bad {
		cfg := good
		mutate(&cfg)
		if a, err := NewAsync(d, cfg); err == nil {
			t.Errorf("NewAsync accepted a config with %s", name)
			_ = a.Close(context.Background())
		}
	}
	if _, err := NewAsync(nil, good); err == nil {
		t.Error("NewAsync accepted a nil destination")
	}
	a := startAsync(t, d, good)
	mustClose(t, a)
}

func TestStage2_FlushesWhenTheBatchIsFull(t *testing.T) {
	d := newDst()
	cfg := baseCfg()
	cfg.MaxBatch = 3
	a := startAsync(t, d, cfg)
	for i := range 7 {
		mustWrite(t, a, entry(i))
	}
	bs := d.waitBatches(t, 2)
	if !reflect.DeepEqual(msgsOf(bs[0]), []string{"m0", "m1", "m2"}) || !reflect.DeepEqual(msgsOf(bs[1]), []string{"m3", "m4", "m5"}) {
		t.Fatalf("batches = %v, want [m0 m1 m2] then [m3 m4 m5] in arrival order", [][]string{msgsOf(bs[0]), msgsOf(bs[1])})
	}
	d.expectNoMoreBatches(t, 2, "the 7th entry is a partial batch and must wait for the linger timer")
}

func TestStage2_FlushesAPartialBatchWhenLingerExpires(t *testing.T) {
	d := newDst()
	timers := newFakeTimers()
	cfg := baseCfg()
	cfg.NewTimer = timers.NewTimer
	cfg.Linger = 250 * time.Millisecond
	a := startAsync(t, d, cfg)

	mustWrite(t, a, entry(0), entry(1))
	ft := timers.next(t, "a partial batch is waiting")
	if ft.d != 250*time.Millisecond {
		t.Fatalf("linger timer duration = %v, want the configured Linger 250ms", ft.d)
	}
	d.expectNoMoreBatches(t, 0, "nothing may be flushed before the linger timer fires or the batch fills")
	ft.fire()
	bs := d.waitBatches(t, 1)
	if got := msgsOf(bs[0]); !reflect.DeepEqual(got, []string{"m0", "m1"}) {
		t.Fatalf("batch = %v, want [m0 m1]", got)
	}
}

func TestStage2_AStaleLingerTimerDoesNotFlushTheNextBatchEarly(t *testing.T) {
	d := newDst()
	timers := newFakeTimers()
	cfg := baseCfg()
	cfg.MaxBatch = 3
	cfg.NewTimer = timers.NewTimer
	a := startAsync(t, d, cfg)

	mustWrite(t, a, entry(0))
	first := timers.next(t, "the first entry of a batch starts a linger timer")
	mustWrite(t, a, entry(1), entry(2)) // fills the batch: flushed by size
	d.waitBatches(t, 1)
	first.fire() // that timer belongs to the batch that is already gone

	mustWrite(t, a, entry(3))
	second := timers.next(t, "a new batch needs a new linger timer")
	d.expectNoMoreBatches(t, 1, "a timer from the previous batch must not flush this one; stop it or ignore it")
	second.fire()
	bs := d.waitBatches(t, 2)
	if got := msgsOf(bs[1]); !reflect.DeepEqual(got, []string{"m3"}) {
		t.Fatalf("second batch = %v, want [m3]", got)
	}
}

func TestStage2_BatchesAreNeverReused(t *testing.T) {
	d := newDst() // keeps every batch slice it was given
	cfg := baseCfg()
	cfg.MaxBatch = 3
	a := startAsync(t, d, cfg)
	for i := range 9 {
		mustWrite(t, a, entry(i))
	}
	d.waitBatches(t, 3)
	want := [][]string{{"m0", "m1", "m2"}, {"m3", "m4", "m5"}, {"m6", "m7", "m8"}}
	for i, b := range d.snapshot() {
		if !reflect.DeepEqual(msgsOf(b), want[i]) {
			t.Fatalf("batch %d reads %v after later batches were written, want %v: the sink owns each slice, so the AsyncSink must not reuse a buffer", i, msgsOf(b), want[i])
		}
	}
}

// fillBehindBlockedWorker makes the worker pick up m0 and block in WriteBatch,
// then fills the queue (3 slots) and sends 6 more entries that do not fit.
func fillBehindBlockedWorker(t *testing.T, policy Policy) (*AsyncSink, *fakeDst) {
	t.Helper()
	d := newBlockedDst(t)
	cfg := baseCfg()
	cfg.QueueSize, cfg.MaxBatch, cfg.Policy = 3, 1, policy
	a := startAsync(t, d, cfg)
	mustWrite(t, a, entry(0))
	d.waitEntered(t, "the worker should take the first entry and call the (blocked) destination")
	return a, d
}

func TestStage2_DropNewestNeverBlocksAndKeepsTheOldest(t *testing.T) {
	a, d := fillBehindBlockedWorker(t, DropNewest)
	within(t, "9 Writes against a full queue and a stuck destination", func() {
		for i := 1; i <= 9; i++ {
			mustWrite(t, a, entry(i))
		}
	})
	if s := a.Stats(); s.Received != 10 || s.Dropped != 6 || s.Queued != 3 {
		t.Fatalf("Stats = %+v, want Received 10, Dropped 6, Queued 3 (1 in flight + 3 queued survive)", s)
	}
	d.open()
	mustFlush(t, a)
	if got, want := d.msgs(), []string{"m0", "m1", "m2", "m3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered %v, want %v: DropNewest keeps what is already queued", got, want)
	}
}

func TestStage2_DropOldestKeepsTheNewest(t *testing.T) {
	a, d := fillBehindBlockedWorker(t, DropOldest)
	within(t, "9 Writes against a full queue and a stuck destination", func() {
		for i := 1; i <= 9; i++ {
			mustWrite(t, a, entry(i))
		}
	})
	if s := a.Stats(); s.Received != 10 || s.Dropped != 6 || s.Queued != 3 {
		t.Fatalf("Stats = %+v, want Received 10, Dropped 6, Queued 3", s)
	}
	d.open()
	mustFlush(t, a)
	if got, want := d.msgs(), []string{"m0", "m7", "m8", "m9"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered %v, want %v: DropOldest evicts from the head of the queue and keeps arrival order", got, want)
	}
}

func TestStage2_BlockWaitsForSpaceAndLosesNothing(t *testing.T) {
	a, d := fillBehindBlockedWorker(t, Block)
	mustWrite(t, a, entry(1), entry(2), entry(3)) // the queue is now full
	wrote := make(chan error, 1)
	go func() { wrote <- a.Write(entry(4)) }()
	select {
	case err := <-wrote:
		t.Fatalf("Write returned %v while the queue was full and the destination stuck; the Block policy must wait", err)
	case <-time.After(100 * time.Millisecond):
	}
	d.open()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatalf("blocked Write returned %v after space appeared, want nil", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("blocked Write never returned after the queue drained")
	}
	mustFlush(t, a)
	if got, want := d.msgs(), []string{"m0", "m1", "m2", "m3", "m4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered %v, want %v", got, want)
	}
	if s := a.Stats(); s.Dropped != 0 {
		t.Fatalf("Block policy dropped %d entries", s.Dropped)
	}
}

func TestStage2_CloseReleasesWritersBlockedByTheBlockPolicy(t *testing.T) {
	a, d := fillBehindBlockedWorker(t, Block)
	mustWrite(t, a, entry(1), entry(2), entry(3))
	wrote := make(chan error, 1)
	go func() { wrote <- a.Write(entry(4)) }()
	time.Sleep(50 * time.Millisecond) // let the writer reach its wait; the assertion below does not depend on this

	closed := make(chan error, 1)
	go func() {
		ctx, cancel := ctxTimeout()
		defer cancel()
		closed <- a.Close(ctx)
	}()
	select {
	case err := <-wrote:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("blocked Write returned %v after Close, want ErrClosed", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Close did not release the writer blocked on a full queue (the destination is still stuck, so draining cannot be what freed it)")
	}
	d.open()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close = %v, want nil", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Close did not return after the destination was released")
	}
	if got, want := d.msgs(), []string{"m0", "m1", "m2", "m3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered %v, want %v: the rejected write must not appear, everything accepted must", got, want)
	}
}

func TestStage2_FlushWritesEverythingAcceptedSoFar(t *testing.T) {
	d := newDst()
	a := startAsync(t, d, baseCfg()) // MaxBatch 10, linger never fires
	mustWrite(t, a, entry(0), entry(1), entry(2), entry(3), entry(4))
	mustFlush(t, a)
	if got := d.msgs(); !reflect.DeepEqual(got, []string{"m0", "m1", "m2", "m3", "m4"}) {
		t.Fatalf("right after Flush returned the destination has %v, want all 5 entries in order", got)
	}
	mustFlush(t, a) // nothing queued: returns promptly
}

func TestStage2_FlushHonoursItsContext(t *testing.T) {
	d := newBlockedDst(t)
	a := startAsync(t, d, baseCfg())
	mustWrite(t, a, entry(0))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	within(t, "Flush with a cancelled context", func() {
		if err := a.Flush(cancelled); !errors.Is(err, context.Canceled) {
			t.Errorf("Flush(cancelled ctx) = %v, want context.Canceled", err)
		}
	})
	short, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	within(t, "Flush against a stuck destination with a 30ms deadline", func() {
		if err := a.Flush(short); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Flush with a stuck destination = %v, want context.DeadlineExceeded", err)
		}
	})
}

func TestStage2_CloseDrainsEverythingAndIsIdempotent(t *testing.T) {
	d := newDst()
	cfg := baseCfg()
	cfg.MaxBatch = 4
	a := startAsync(t, d, cfg)
	for i := range 10 {
		mustWrite(t, a, entry(i))
	}
	mustClose(t, a) // the partial final batch must go out without waiting for the linger timer
	want := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9"}
	if got := d.msgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after Close the destination has %v, want all 10 in order", got)
	}
	mustClose(t, a)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); mustClose(t, a) }()
	}
	within(t, "concurrent Close calls", wg.Wait)
	if err := a.Write(entry(99)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Write after Close = %v, want ErrClosed", err)
	}
	ctx, cancel := ctxTimeout()
	defer cancel()
	if err := a.Flush(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("Flush after Close = %v, want ErrClosed", err)
	}
	if s := a.Stats(); s.Received != 10 || s.Written != 10 {
		t.Fatalf("Stats = %+v, want Received 10 and Written 10 (a rejected Write is not Received)", s)
	}
}

func TestStage2_AFailedBatchIsCountedNotRetriedByDefault(t *testing.T) {
	d := newDst()
	d.fail = func(call int, _ []Entry) error {
		if call == 1 {
			return errors.New("insert failed")
		}
		return nil
	}
	cfg := baseCfg()
	cfg.MaxBatch = 2
	a := startAsync(t, d, cfg)
	mustWrite(t, a, entry(0), entry(1), entry(2), entry(3))
	mustFlush(t, a)
	s := a.Stats()
	if s.Failed != 2 || s.Written != 2 || s.Received != 4 {
		t.Fatalf("Stats = %+v, want Failed 2, Written 2, Received 4 (the first batch is lost, the second still delivered)", s)
	}
	if got := d.msgs(); !reflect.DeepEqual(got, []string{"m2", "m3"}) {
		t.Fatalf("delivered %v, want [m2 m3]: one failing batch must not stop the worker", got)
	}
}

func TestStage2_ALoggerNeverBlocksOnASlowSink(t *testing.T) {
	d := newBlockedDst(t)
	cfg := baseCfg()
	cfg.QueueSize, cfg.MaxBatch, cfg.Policy = 8, 4, DropNewest
	a := startAsync(t, d, cfg)
	l := New(Options{}, LevelFilter(Info, a)).With(F("svc", "statements"))
	within(t, "10,000 log calls with the destination stuck", func() {
		for i := range 10_000 {
			l.Info("tick", F("i", i))
		}
	})
	s := a.Stats()
	if s.Received != 10_000 || s.Queued > 8 || s.Dropped < 10_000-8-4 {
		t.Fatalf("Stats = %+v: expected at most QueueSize+MaxBatch entries to be kept and the rest dropped", s)
	}
}

func TestStage2_StatsAddUpOnceQuiescent(t *testing.T) {
	d := newDst()
	cfg := baseCfg()
	cfg.QueueSize, cfg.MaxBatch, cfg.Policy = 5, 2, DropOldest
	a := startAsync(t, d, cfg)
	for i := range 200 {
		mustWrite(t, a, entry(i))
		if i%37 == 0 {
			mustFlush(t, a)
		}
	}
	mustFlush(t, a)
	s := a.Stats()
	if s.Received != 200 || s.Received != s.Written+s.Dropped+s.Failed || s.Queued != 0 {
		t.Fatalf("Stats = %+v, want Received 200 == Written + Dropped + Failed and an empty queue", s)
	}
	if uint64(len(d.msgs())) != s.Written {
		t.Fatalf("destination holds %d entries but Written = %d", len(d.msgs()), s.Written)
	}
}

func TestStage2_CloseStopsTheBackgroundGoroutine(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for range 5 {
		d := newDst()
		a, err := NewAsync(d, baseCfg())
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, a, entry(0))
		mustClose(t, a)
	}
	waitGoroutines(t, baseline)
}
