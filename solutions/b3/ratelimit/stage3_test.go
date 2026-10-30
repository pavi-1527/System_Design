package ratelimit

import (
	"context"
	"errors"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newWaitLimiter wires the fake clock into both Now and the timer source.
func newWaitLimiter(t *testing.T, rate Rate, burst int, opts ...Option) (*Limiter, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	l, err := New(rate, burst, append([]Option{WithClock(clk), WithTimer(clk.NewTimer)}, opts...)...)
	if err != nil {
		t.Fatalf("New returned error %v", err)
	}
	if l == nil {
		t.Fatal("New returned a nil *Limiter with a nil error")
	}
	return l, clk
}

// waitResult runs Wait in a goroutine.
func waitAsync(l *Limiter, ctx context.Context, key string, n int) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- l.WaitN(ctx, key, n) }()
	return ch
}

func recv(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(testTimeout):
		t.Fatalf("%s: Wait did not return within %v", what, testTimeout)
		return nil
	}
}

func expectPending(t *testing.T, ch <-chan error, why string) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("Wait returned %v too early: %s", err, why)
	case <-time.After(10 * time.Millisecond):
	}
}

func TestStage3_NewKeyTriggersAmortisedReclamationOfIdleKeys(t *testing.T) {
	l, clk := newLimiter(t, Rate{1, time.Second}, 1, WithShards(1)) // one shard: sweeping budget is per shard
	for i := 0; i < 100; i++ {
		l.Allow("old-" + strconv.Itoa(i)) // each consumes its only token
	}
	if l.Len() != 100 {
		t.Fatalf("Len = %d, want 100", l.Len())
	}
	clk.Advance(time.Second) // every old key has fully recovered: idle
	for i := 0; i < 200; i++ {
		l.Allow("new-" + strconv.Itoa(i))
	}
	if got := l.Len(); got != 200 {
		t.Fatalf("Len = %d after 200 new keys arrived, want 200: the 100 idle keys should have been reclaimed by that traffic (no janitor goroutine is needed to bound memory)", got)
	}
}

func TestStage3_EvictionIsInvisibleBecauseOnlyIdleKeysAreDropped(t *testing.T) {
	l, clk := newLimiter(t, Rate{1, time.Second}, 5)
	for i := 0; i < 99; i++ {
		l.Allow("b" + strconv.Itoa(i)) // 99 keys with 4 of 5 tokens left: full again after 1s
	}
	drain(l, "a") // empty: needs 5s to be full again
	clk.Advance(2 * time.Second)
	if n := l.Sweep(); n != 99 || l.Len() != 1 {
		t.Fatalf("Sweep dropped %d keys leaving %d, want 99 dropped and a alone left (a is still 3 tokens short)", n, l.Len())
	}
	if st := l.Stats(); st.IdleEvicted != 99 || st.ForcedEvicted != 0 || st.Keys != 1 {
		t.Fatalf("Stats = %+v, want IdleEvicted 99, ForcedEvicted 0, Keys 1", st)
	}
	if got := drain(l, "a"); got != 2 {
		t.Fatalf("a had %d permits after 2s at 1/s, want exactly 2", got)
	}
	clk.Advance(time.Hour)
	if n := l.Sweep(); n != 1 {
		t.Fatalf("Sweep dropped %d keys, want 1 (a is full again, so it is indistinguishable from a new key)", n)
	}
	if got := drain(l, "a"); got != 5 {
		t.Fatalf("a re-created after eviction allowed %d, want burst 5", got)
	}
}

func TestStage3_ManyOneOffKeysStayBounded(t *testing.T) {
	// 200,000 distinct keys, one request each, 1ms apart (1000 new keys per
	// second), recovery time 1s: about a thousand keys are ever "live". A
	// limiter that remembers every key holds 200,000 (and 1M of them is a
	// quarter of a gigabyte once keys and state are counted).
	const keys = 200_000
	l, clk := newLimiter(t, Rate{1, time.Second}, 1)
	maxLen := 0
	within(t, "200k one-off keys", func() {
		for i := 0; i < keys; i++ {
			clk.Advance(time.Millisecond)
			if !l.Allow(strconv.Itoa(i)) {
				t.Errorf("first request of key %d denied", i)
				return
			}
			if i%1000 == 0 {
				if n := l.Len(); n > maxLen {
					maxLen = n
				}
			}
		}
	})
	if maxLen > 5000 {
		t.Fatalf("Len peaked at %d with ~1000 keys live at any time: idle keys are not being reclaimed, memory grows with key cardinality", maxLen)
	}
	if st := l.Stats(); st.IdleEvicted < keys-5000 {
		t.Fatalf("IdleEvicted = %d, want about %d", st.IdleEvicted, keys-1000)
	}
}

func TestStage3_MaxKeysIsAHardCapAndEvictsLeastRecentlyUsed(t *testing.T) {
	if _, err := New(Rate{1, time.Second}, 5, WithMaxKeys(0)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("WithMaxKeys(0) error = %v, want ErrInvalidConfig", err)
	}
	l, _ := newLimiter(t, Rate{1, time.Hour}, 5, WithShards(1), WithMaxKeys(100)) // frozen clock: nothing is ever idle
	drain(l, "hot")
	for i := 0; i < 1000; i++ {
		l.Allow("flood-" + strconv.Itoa(i))
		if i%10 == 0 {
			if l.Allow("hot") {
				t.Fatalf("hot was allowed again at flood key %d: it was touched constantly so it must not have been evicted", i)
			}
		}
	}
	if got := l.Len(); got != 100 {
		t.Fatalf("Len = %d, want exactly the cap of 100", got)
	}
	st := l.Stats()
	if st.ForcedEvicted != 901 || st.IdleEvicted != 0 {
		t.Fatalf("Stats = %+v, want ForcedEvicted 901 (1001 keys seen, 100 kept) and IdleEvicted 0", st)
	}
	// The documented price: a key that was evicted while not idle starts over (fail-open).
	if !l.Allow("flood-0") {
		t.Fatal("flood-0 was evicted but its next request was denied: an evicted key must start with a full bucket")
	}
}

func TestStage3_StatsCountDecisions(t *testing.T) {
	l, _ := newLimiter(t, Rate{1, time.Hour}, 10) // frozen clock
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				l.Allow("k" + strconv.Itoa((w+i)%8))
				l.AllowN("k0", 0) // invalid: not a decision
			}
		}(w)
	}
	within(t, "counting workload", wg.Wait)
	st := l.Stats()
	if st.Allowed != 80 || st.Denied != 16*1000-80 || st.Keys != 8 || st.Keys != l.Len() {
		t.Fatalf("Stats = %+v, want Allowed 80 (8 keys x burst 10), Denied 15920, Keys 8 (and equal to Len)", st)
	}
}

func TestStage3_WaitBlocksForExactlyTheRetryAfterThenConsumesThePermit(t *testing.T) {
	l, clk := newWaitLimiter(t, Rate{10, time.Second}, 2)
	// With a permit available Wait returns at once and never touches a timer.
	if err := recv(t, waitAsync(l, context.Background(), "k", 1), "Wait with a permit available"); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
	if clk.timersCreated() != 0 {
		t.Fatalf("Wait created %d timers although a permit was available", clk.timersCreated())
	}
	l.Allow("k")
	ch := waitAsync(l, context.Background(), "k", 1)
	clk.waitForTimers(t, 1, "Wait should sleep on a timer from the injected source (WithTimer)")
	if ds := clk.activeDurations(); len(ds) != 1 || ds[0] != 100*time.Millisecond {
		t.Fatalf("Wait slept for %v, want exactly one timer of 100ms (the decision's RetryAfter)", ds)
	}
	clk.Advance(99 * time.Millisecond)
	expectPending(t, ch, "only 99ms of the 100ms have passed")
	clk.Advance(time.Millisecond)
	if err := recv(t, ch, "after the full RetryAfter"); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
	if l.Allow("k") {
		t.Fatal("the permit Wait obtained was not consumed")
	}
	if clk.active() != 0 {
		t.Fatalf("%d timers still active after Wait returned", clk.active())
	}
}

func TestStage3_WaitSleepsForTheDecisionsRetryAfterWithEitherAlgorithm(t *testing.T) {
	for name, f := range algorithms(t) {
		clk := newFakeClock()
		l, err := NewWithStrategy(f, WithClock(clk), WithTimer(clk.NewTimer))
		if err != nil {
			t.Fatal(err)
		}
		clk.Advance(3 * time.Second)
		drain(l, "k")
		clk.Advance(1234 * time.Millisecond)
		want := mustCheck(t, l, "k", 2).RetryAfter // a denial: consumes nothing
		if want <= 0 {
			t.Fatalf("%s: setup: expected a denial with RetryAfter > 0", name)
		}
		ch := waitAsync(l, context.Background(), "k", 2)
		clk.waitForTimers(t, 1, name+": Wait should sleep on a timer")
		if ds := clk.activeDurations(); len(ds) != 1 || ds[0] != want {
			t.Fatalf("%s: Wait slept %v, want %v (RetryAfter)", name, ds, want)
		}
		clk.Advance(want)
		if err := recv(t, ch, name); err != nil {
			t.Fatalf("%s: Wait = %v", name, err)
		}
	}
}

func TestStage3_WaitCancellationStopsTheTimerAndConsumesNothing(t *testing.T) {
	l, clk := newWaitLimiter(t, Rate{10, time.Second}, 1)
	l.Allow("k")
	ctx, cancel := context.WithCancel(context.Background())
	ch := waitAsync(l, ctx, "k", 1)
	clk.waitForTimers(t, 1, "Wait should be sleeping")
	cancel()
	if err := recv(t, ch, "after cancel"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %v, want context.Canceled", err)
	}
	if clk.active() != 0 {
		t.Fatalf("%d timers still active after a cancelled Wait: stop the timer on the cancel path (time.After in a select leaks it until it fires)", clk.active())
	}
	clk.Advance(100 * time.Millisecond)
	if !l.Allow("k") {
		t.Fatal("no permit after 100ms: the cancelled Wait must not have consumed one")
	}
	if l.Allow("k") {
		t.Fatal("more than one permit after 100ms at 10/s")
	}
}

func TestStage3_WaitWithADoneContextConsumesNothing(t *testing.T) {
	l, _ := newWaitLimiter(t, Rate{1, time.Hour}, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 50; i++ { // select picks randomly among ready cases: one run would pass by luck
		if err := recv(t, waitAsync(l, ctx, "k", 1), "Wait with a cancelled ctx"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait = %v, want context.Canceled even though permits were available", err)
		}
	}
	if got := drain(l, "k"); got != 3 {
		t.Fatalf("only %d permits left, want all 3: a caller that has given up must never take one", got)
	}
}

func TestStage3_WaitHonoursContextDeadlines(t *testing.T) {
	l, clk := newWaitLimiter(t, Rate{1, time.Hour}, 1)
	l.Allow("k")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond) // real time: ctx deadlines are
	defer cancel()
	if err := recv(t, waitAsync(l, ctx, "k", 1), "Wait with a 20ms deadline"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want context.DeadlineExceeded", err)
	}
	if clk.active() != 0 {
		t.Fatalf("%d timers active after the deadline", clk.active())
	}
}

func TestStage3_WaitNRejectsRequestsThatCanNeverSucceedImmediately(t *testing.T) {
	l, clk := newWaitLimiter(t, Rate{10, time.Second}, 3)
	if err := recv(t, waitAsync(l, context.Background(), "k", 4), "WaitN above burst"); !errors.Is(err, ErrExceedsLimit) {
		t.Fatalf("WaitN(4) with burst 3 = %v, want ErrExceedsLimit (waiting forever helps nobody)", err)
	}
	if err := recv(t, waitAsync(l, context.Background(), "k", 0), "WaitN(0)"); !errors.Is(err, ErrInvalidN) {
		t.Fatalf("WaitN(0) = %v, want ErrInvalidN", err)
	}
	if clk.timersCreated() != 0 || l.Len() != 0 {
		t.Fatalf("timers created = %d, keys = %d: rejected requests must neither sleep nor allocate state", clk.timersCreated(), l.Len())
	}
}

func TestStage3_ManyWaitersAreAdmittedNoFasterThanTheRate(t *testing.T) {
	const waiters = 20
	l, clk := newWaitLimiter(t, Rate{10, time.Second}, 1) // one permit per 100ms
	l.Allow("k")                                          // drain
	start := clk.Now()
	baseline := runtime.NumGoroutine()
	var mu sync.Mutex
	var finished []time.Duration // virtual time at which each Wait returned
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Wait(context.Background(), "k"); err != nil {
				t.Errorf("Wait = %v", err)
				return
			}
			mu.Lock()
			finished = append(finished, clk.Now().Sub(start))
			mu.Unlock()
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	deadline := time.Now().Add(testTimeout)
loop:
	for {
		select {
		case <-done:
			break loop
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiters still blocked after %v of real time (lost wake-up?)", testTimeout)
		}
		clk.Advance(10 * time.Millisecond)
		time.Sleep(100 * time.Microsecond)
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i] < finished[j] })
	if len(finished) != waiters {
		t.Fatalf("%d waiters finished, want %d", len(finished), waiters)
	}
	for k, at := range finished { // the (k+1)-th permit exists only from (k+1)*100ms
		if earliest := time.Duration(k+1) * 100 * time.Millisecond; at < earliest {
			t.Fatalf("waiter %d finished at +%v, before its permit existed (+%v): admitted faster than the configured rate", k+1, at, earliest)
		}
	}
	if clk.active() != 0 {
		t.Fatalf("%d timers still active after all waiters finished", clk.active())
	}
	waitForGoroutines(t, baseline)
}

func TestStage3_CancellingManyWaitersLeaksNothing(t *testing.T) {
	l, clk := newWaitLimiter(t, Rate{1, time.Hour}, 1)
	l.Allow("k")
	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	chans := make([]<-chan error, 50)
	for i := range chans {
		chans[i] = waitAsync(l, ctx, "k", 1)
	}
	clk.waitForTimers(t, 50, "50 waiters should each hold a timer")
	cancel()
	for i, ch := range chans {
		if err := recv(t, ch, "waiter "+strconv.Itoa(i)); !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter %d: Wait = %v, want context.Canceled", i, err)
		}
	}
	if clk.active() != 0 {
		t.Fatalf("%d timers leaked by cancelled waiters", clk.active())
	}
	waitForGoroutines(t, baseline)
}

func TestStage3_WaitWorksWithTheDefaultRealClockAndTimers(t *testing.T) {
	l, err := New(Rate{200, time.Second}, 1) // a permit every 5ms
	if err != nil {
		t.Fatal(err)
	}
	if !l.Allow("k") {
		t.Fatal("first request denied")
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	start := time.Now()
	if err := l.Wait(ctx, "k"); err != nil {
		t.Fatalf("Wait with real timers = %v", err)
	}
	if took := time.Since(start); took < 2*time.Millisecond {
		t.Fatalf("Wait returned after %v: it did not wait for the permit", took)
	}
}

func TestStage3_ConcurrentTrafficSweepStatsAndEvictionAreRaceFree(t *testing.T) {
	const workers, perWorker = 16, 5000
	l, clk := newLimiter(t, Rate{100, time.Second}, 3, WithMaxKeys(64), WithShards(4))
	var traffic, sweeper sync.WaitGroup
	var stop atomic.Bool
	for w := 0; w < workers; w++ {
		traffic.Add(1)
		go func(w int) {
			defer traffic.Done()
			for i := 0; i < perWorker; i++ {
				l.Allow("k" + strconv.Itoa((w*131+i*17)%500))
			}
		}(w)
	}
	sweeper.Add(1)
	go func() {
		defer sweeper.Done()
		for !stop.Load() {
			clk.Advance(time.Millisecond)
			l.Sweep()
			_ = l.Stats()
			_ = l.Len()
			runtime.Gosched()
		}
	}()
	within(t, "traffic phase", traffic.Wait)
	stop.Store(true)
	within(t, "sweeper shutdown", sweeper.Wait)
	if n := l.Len(); n > 64 {
		t.Fatalf("Len = %d, want at most the cap of 64 (4 shards x 16)", n)
	}
	if st := l.Stats(); st.Allowed+st.Denied != workers*perWorker {
		t.Fatalf("Stats counted %d decisions, want %d", st.Allowed+st.Denied, workers*perWorker)
	}
}
