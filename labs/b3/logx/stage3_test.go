package logx

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

// eventLog collects ErrorEvents from the OnError hook.
type eventLog struct {
	mu sync.Mutex
	ev []ErrorEvent
}

func (l *eventLog) add(e ErrorEvent) {
	l.mu.Lock()
	l.ev = append(l.ev, e)
	l.mu.Unlock()
}

func (l *eventLog) all() []ErrorEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]ErrorEvent(nil), l.ev...)
}

// fakeSleeper records requested sleeps and returns immediately.
type fakeSleeper struct {
	mu sync.Mutex
	ds []time.Duration
}

func (s *fakeSleeper) Sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.ds = append(s.ds, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *fakeSleeper) durations() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.ds...)
}

func startAsyncF(t *testing.T, d *fakeDst, cfg AsyncConfig, fp FailurePolicy) *AsyncSink {
	t.Helper()
	a, err := NewAsyncWithFailure(d, cfg, fp)
	if err != nil || a == nil {
		t.Fatalf("NewAsyncWithFailure = %v, %v; want a sink and nil", a, err)
	}
	t.Cleanup(func() {
		d.open()
		ctx, cancel := ctxTimeout()
		defer cancel()
		_ = a.Close(ctx)
	})
	return a
}

// waitStats polls until cond holds; goroutine counts alone cannot say that the
// worker finished (other goroutines may exit meanwhile and mask it).
func waitStats(t *testing.T, a *AsyncSink, what string, cond func(Stats) bool) Stats {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		s := a.Stats()
		if cond(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; Stats = %+v", what, s)
		}
		time.Sleep(time.Millisecond)
	}
}

func batchCfg(maxBatch int) AsyncConfig {
	c := baseCfg()
	c.MaxBatch = maxBatch
	return c
}

// ---------------------------------------------------------------- Stage 3

func TestStage3_RetriesWithDoublingBackoffThenSucceeds(t *testing.T) {
	d := newDst()
	d.fail = func(call int, _ []Entry) error {
		if call <= 2 {
			return fmt.Errorf("attempt %d failed", call)
		}
		return nil
	}
	sl, ev := &fakeSleeper{}, &eventLog{}
	a := startAsyncF(t, d, batchCfg(2), FailurePolicy{MaxRetries: 3, Backoff: 10 * time.Millisecond, Sleep: sl.Sleep, OnError: ev.add})

	mustWrite(t, a, entry(0), entry(1))
	mustFlush(t, a)

	if got := d.msgs(); !reflect.DeepEqual(got, []string{"m0", "m1"}) {
		t.Fatalf("delivered %v, want [m0 m1] exactly once, from the third attempt", got)
	}
	if got, want := sl.durations(), []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}; !reflect.DeepEqual(got, want) {
		t.Fatalf("backoff sleeps = %v, want %v (the delay doubles on every retry)", got, want)
	}
	s := a.Stats()
	if s.Written != 2 || s.Failed != 0 || s.Retries != 2 || s.Batches != 1 {
		t.Fatalf("Stats = %+v, want Written 2, Failed 0, Retries 2, Batches 1", s)
	}
	evs := ev.all()
	if len(evs) != 2 || evs[0].Attempt != 1 || evs[1].Attempt != 2 || evs[0].Final || evs[1].Final || evs[0].Entries != 2 {
		t.Fatalf("OnError events = %+v, want two non-final events for attempts 1 and 2 (2 entries each)", evs)
	}
}

func TestStage3_GivesUpAfterMaxRetriesAndKeepsGoing(t *testing.T) {
	d := newDst()
	poison := errors.New("constraint violation")
	d.fail = func(_ int, b []Entry) error {
		if b[0].Msg == "bad0" {
			return poison
		}
		return nil
	}
	ev := &eventLog{}
	a := startAsyncF(t, d, batchCfg(2), FailurePolicy{MaxRetries: 2, Sleep: (&fakeSleeper{}).Sleep, OnError: ev.add})

	mustWrite(t, a, Entry{Msg: "bad0"}, Entry{Msg: "bad1"}, entry(2), entry(3))
	mustFlush(t, a)

	s := a.Stats()
	if s.Failed != 2 || s.Written != 2 || s.Retries != 2 || s.Received != 4 {
		t.Fatalf("Stats = %+v, want Failed 2, Written 2, Retries 2, Received 4", s)
	}
	if got := d.msgs(); !reflect.DeepEqual(got, []string{"m2", "m3"}) {
		t.Fatalf("delivered %v, want [m2 m3]: a poisoned batch must not wedge the queue", got)
	}
	evs := ev.all()
	if len(evs) != 3 {
		t.Fatalf("got %d OnError events, want 3 (one per attempt)", len(evs))
	}
	finals := 0
	for i, e := range evs {
		if e.Attempt != i+1 || !errors.Is(e.Err, poison) {
			t.Fatalf("event %d = %+v, want attempt %d with the sink's error", i, e, i+1)
		}
		if e.Final {
			finals++
		}
	}
	if finals != 1 || !evs[2].Final {
		t.Fatalf("events %+v: exactly the last event of a lost batch must be Final", evs)
	}
}

func TestStage3_EachRetryGetsPristineEntries(t *testing.T) {
	var seen [][]string
	var mu sync.Mutex
	dst := batchFunc(func(b []Entry) error {
		mu.Lock()
		seen = append(seen, msgsOf(b))
		n := len(seen)
		mu.Unlock()
		for i := range b {
			b[i].Msg = "scribbled"
		}
		if n == 1 {
			return errors.New("first attempt fails after scribbling")
		}
		return nil
	})
	a, err := NewAsyncWithFailure(dst, batchCfg(2), FailurePolicy{MaxRetries: 1, Sleep: (&fakeSleeper{}).Sleep})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	mustWrite(t, a, entry(0), entry(1))
	mustFlush(t, a)
	mu.Lock()
	defer mu.Unlock()
	if want := [][]string{{"m0", "m1"}, {"m0", "m1"}}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("attempts saw %v, want %v: every attempt needs its own copy because the destination owns (and may modify) the slice", seen, want)
	}
}

type batchFunc func([]Entry) error

func (f batchFunc) WriteBatch(b []Entry) error { return f(b) }

func TestStage3_APanickingDestinationIsContained(t *testing.T) {
	d := newDst()
	d.panics = func(call int) any {
		if call == 1 {
			return "boom"
		}
		return nil
	}
	ev := &eventLog{}
	a := startAsyncF(t, d, batchCfg(1), FailurePolicy{OnError: ev.add})
	mustWrite(t, a, entry(0))
	mustFlush(t, a)
	mustWrite(t, a, entry(1)) // the worker must still be alive to take this
	mustFlush(t, a)

	if got := d.msgs(); !reflect.DeepEqual(got, []string{"m1"}) {
		t.Fatalf("delivered %v, want [m1]: the batch that panicked is lost, the next one must go through", got)
	}
	evs := ev.all()
	var pe *PanicError
	if len(evs) != 1 || !errors.As(evs[0].Err, &pe) || pe.Value != "boom" || !evs[0].Final {
		t.Fatalf("events = %+v, want one Final event wrapping a *PanicError{boom}", evs)
	}
	if s := a.Stats(); s.Failed != 1 || s.Written != 1 {
		t.Fatalf("Stats = %+v, want Failed 1, Written 1", s)
	}
}

func TestStage3_APanickingOnErrorHookDoesNotKillTheWorker(t *testing.T) {
	d := newDst()
	d.fail = func(call int, _ []Entry) error {
		if call == 1 {
			return errors.New("fails once")
		}
		return nil
	}
	a := startAsyncF(t, d, batchCfg(1), FailurePolicy{OnError: func(ErrorEvent) { panic("hook bug") }})
	mustWrite(t, a, entry(0))
	mustFlush(t, a)
	mustWrite(t, a, entry(1))
	mustFlush(t, a)
	if got := d.msgs(); !reflect.DeepEqual(got, []string{"m1"}) {
		t.Fatalf("delivered %v, want [m1]", got)
	}
}

func TestStage3_CloseWithADeadlineReportsWhatItCouldNotFlush(t *testing.T) {
	baseline := runtime.NumGoroutine()
	d := newBlockedDst(t)
	cfg := baseCfg()
	cfg.MaxBatch = 2
	a := startAsyncF(t, d, cfg, FailurePolicy{})
	mustWrite(t, a, entry(0), entry(1)) // one batch of 2 is now stuck in the destination
	d.waitEntered(t, "the first full batch")
	mustWrite(t, a, entry(2), entry(3), entry(4)) // 3 more wait in the queue

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	var err error
	within(t, "Close with an expired context and a stuck destination", func() { err = a.Close(expired) })
	var ue *UnflushedError
	if !errors.As(err, &ue) || ue.Remaining != 5 || !errors.Is(err, context.Canceled) {
		t.Fatalf("Close = %v, want an *UnflushedError{Remaining: 5} wrapping context.Canceled (2 in flight + 3 queued)", err)
	}

	d.open() // the destination recovers after we gave up
	s := waitStats(t, a, "the worker to finish the in-flight batch and abandon the queue", func(s Stats) bool { return s.Written+s.Dropped == 5 })
	waitGoroutines(t, baseline)
	if s.Written != 2 || s.Dropped != 3 || s.Received != 5 {
		t.Fatalf("Stats = %+v, want the in-flight batch Written (2) and the queued entries Dropped (3)", s)
	}
	if got := d.msgs(); !reflect.DeepEqual(got, []string{"m0", "m1"}) {
		t.Fatalf("delivered %v, want only the in-flight batch [m0 m1]", got)
	}
	ctx, cancel2 := ctxTimeout()
	defer cancel2()
	err = a.Close(ctx) // a later Close reports the same loss
	if !errors.As(err, &ue) || ue.Remaining != 3 {
		t.Fatalf("second Close = %v, want *UnflushedError{Remaining: 3}", err)
	}
}

func TestStage3_CloseDoesNotSleepThroughARetryBackoff(t *testing.T) {
	baseline := runtime.NumGoroutine()
	d := newDst()
	d.fail = func(int, []Entry) error { return errors.New("always down") }
	entered := make(chan struct{}, 1)
	sleep := func(ctx context.Context, _ time.Duration) error { // a backoff of "an hour"
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	ev := &eventLog{}
	a := startAsyncF(t, d, batchCfg(1), FailurePolicy{MaxRetries: 5, Backoff: time.Hour, Sleep: sleep, OnError: ev.add})
	mustWrite(t, a, entry(0))
	select {
	case <-entered:
	case <-time.After(testTimeout):
		t.Fatal("the worker never reached its retry backoff")
	}

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	var err error
	within(t, "Close while the worker sleeps in a backoff", func() { err = a.Close(expired) })
	var ue *UnflushedError
	if !errors.As(err, &ue) || ue.Remaining != 1 {
		t.Fatalf("Close = %v, want *UnflushedError{Remaining: 1}", err)
	}
	s := waitStats(t, a, "the interrupted batch to be counted", func(s Stats) bool { return s.Failed == 1 })
	waitGoroutines(t, baseline) // the sleep's context must be cancelled so the worker exits
	if s.Received != 1 {
		t.Fatalf("Stats = %+v, want the interrupted batch counted as Failed (1)", s)
	}
	evs := ev.all()
	if len(evs) == 0 {
		t.Fatal("OnError was never called")
	}
	if last := evs[len(evs)-1]; !last.Final {
		t.Fatalf("last event %+v: a batch that is given up must produce a Final event", last)
	}
}

type seqVal struct{ g, n int }

func TestStage3_ManyLoggersConserveEveryEntry(t *testing.T) {
	baseline := runtime.NumGoroutine()
	var (
		mu  sync.Mutex
		got []seqVal
	)
	dst := batchFunc(func(b []Entry) error {
		runtime.Gosched() // a slightly slow destination, so the queue overflows and drops happen
		mu.Lock()
		defer mu.Unlock()
		for _, e := range b {
			var sv seqVal
			for _, f := range e.Fields {
				switch f.Key {
				case "g":
					sv.g = f.Value.(int)
				case "n":
					sv.n = f.Value.(int)
				}
			}
			got = append(got, sv)
		}
		return nil
	})
	cfg := AsyncConfig{QueueSize: 32, MaxBatch: 8, Linger: time.Millisecond, Policy: DropOldest}
	a, err := NewAsync(dst, cfg)
	if err != nil {
		t.Fatal(err)
	}
	l := New(Options{Level: Info}, a)

	const writers, per = 16, 500
	var wg sync.WaitGroup
	for g := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := l.With(F("g", g))
			for n := range per {
				child.Info("tick", F("n", n))
			}
		}()
	}
	stop := make(chan struct{})
	var fwg sync.WaitGroup
	fwg.Add(1)
	go func() { // Flush races the writers
		defer fwg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			ctx, cancel := ctxTimeout()
			_ = a.Flush(ctx)
			cancel()
		}
	}()
	within(t, "writers", wg.Wait)
	close(stop)
	within(t, "flusher", fwg.Wait)
	mustClose(t, a)

	s := a.Stats()
	if s.Received != writers*per {
		t.Fatalf("Received = %d, want %d", s.Received, writers*per)
	}
	if s.Written+s.Dropped+s.Failed != s.Received {
		t.Fatalf("Stats = %+v: Written + Dropped + Failed must equal Received (every accepted entry is accounted for)", s)
	}
	mu.Lock()
	defer mu.Unlock()
	if uint64(len(got)) != s.Written {
		t.Fatalf("destination received %d entries but Written = %d", len(got), s.Written)
	}
	last := map[int]int{}
	for _, sv := range got {
		if prev, ok := last[sv.g]; ok && sv.n <= prev {
			t.Fatalf("goroutine %d: entry %d delivered after %d: duplicates or reordering within one writer", sv.g, sv.n, prev)
		}
		last[sv.g] = sv.n
	}
	waitGoroutines(t, baseline)
}

func TestStage3_CloseRacingWritersNeverPanicsOrLosesTrack(t *testing.T) {
	for _, policy := range []Policy{Block, DropNewest, DropOldest} {
		t.Run(fmt.Sprintf("policy%d", policy), func(t *testing.T) {
			baseline := runtime.NumGoroutine()
			dst := batchFunc(func([]Entry) error { runtime.Gosched(); return nil })
			a, err := NewAsync(dst, AsyncConfig{QueueSize: 4, MaxBatch: 2, Linger: time.Millisecond, Policy: policy})
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						if err := a.Write(Entry{Msg: "x"}); err != nil {
							if !errors.Is(err, ErrClosed) {
								t.Errorf("Write = %v, want nil or ErrClosed", err)
							}
							return
						}
					}
				}()
			}
			deadline := time.Now().Add(testTimeout)
			for a.Stats().Received < 50 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			mustClose(t, a)
			within(t, "writers after Close", wg.Wait)
			s := a.Stats()
			if s.Received != s.Written+s.Dropped+s.Failed || s.Queued != 0 {
				t.Fatalf("Stats = %+v after Close, want Received == Written + Dropped + Failed and an empty queue", s)
			}
			waitGoroutines(t, baseline)
		})
	}
}

func TestStage3_RandomDestinationFailuresStillAddUp(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	var rmu sync.Mutex
	d := newDst()
	d.fail = func(int, []Entry) error {
		rmu.Lock()
		defer rmu.Unlock()
		if r.Intn(10) < 3 {
			return errors.New("flaky")
		}
		return nil
	}
	cfg := baseCfg()
	cfg.MaxBatch, cfg.QueueSize, cfg.Policy = 7, 64, Block
	a := startAsyncF(t, d, cfg, FailurePolicy{MaxRetries: 1})
	for i := range 2000 {
		mustWrite(t, a, entry(i))
	}
	mustFlush(t, a)
	s := a.Stats()
	if s.Received != 2000 || s.Dropped != 0 || s.Written+s.Failed != 2000 {
		t.Fatalf("Stats = %+v, want Received 2000, Dropped 0 (Block policy) and Written + Failed == 2000", s)
	}
	if s.Failed == 0 || s.Retries == 0 {
		t.Fatalf("Stats = %+v: the flaky destination should have forced some retries and some losses", s)
	}
	if uint64(len(d.msgs())) != s.Written {
		t.Fatalf("destination holds %d entries but Written = %d", len(d.msgs()), s.Written)
	}
}
