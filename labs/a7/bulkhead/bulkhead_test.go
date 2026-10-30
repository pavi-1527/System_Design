package bulkhead

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

type tClock struct {
	mu sync.Mutex
	t  time.Time
}

func tNewClock() *tClock { return &tClock{t: time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)} }
func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func tWait(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish within 10 s (deadlock or a leaked slot?)", what)
	}
}

func tEventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

// tHold acquires a slot and returns its release func, failing the test if
// the bulkhead refuses or does not grant a slot within 2 s (a leaked slot
// would otherwise make a queueing bulkhead wait forever).
func tHold(t *testing.T, b *Bulkhead) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rel, err := b.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v, want a free slot", err)
	}
	return rel
}

// tAcquire admits one call or fails the test (never returns a nil done).
func tAcquire(t *testing.T, a *AIMD) func(Outcome) {
	t.Helper()
	d, ok := a.TryAcquire()
	if !ok {
		t.Fatalf("TryAcquire refused (limit %v, in flight %d), want admitted", a.Limit(), a.InFlight())
	}
	return d
}

// ---------- Bulkhead ----------

func TestBulkhead_RejectsImmediatelyWhenFullAndNoQueue(t *testing.T) {
	b := New(Config{MaxConcurrent: 3})
	var rels []func()
	for i := 0; i < 3; i++ {
		rels = append(rels, tHold(t, b))
	}
	start := time.Now()
	_, err := b.Acquire(context.Background())
	if !errors.Is(err, ErrFull) {
		t.Fatalf("4th Acquire: err = %v, want ErrFull", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("rejection took %v: a full bulkhead must say no immediately, not make the caller wait", time.Since(start))
	}
	if s := b.Stats(); s.InFlight != 3 || s.Rejected != 1 || s.Admitted != 3 {
		t.Errorf("Stats = %+v; want InFlight 3, Admitted 3, Rejected 1", s)
	}
	rels[0]()
	rels[0]() // releasing twice must not free a second slot
	if s := b.Stats(); s.InFlight != 2 {
		t.Errorf("InFlight = %d after one release (called twice), want 2", s.InFlight)
	}
	if rel, err := b.Acquire(context.Background()); err != nil {
		t.Errorf("Acquire after release: %v", err)
	} else {
		rel()
	}
}

func TestBulkhead_DoRunsInsideASlotAndReleasesOnPanic(t *testing.T) {
	b := New(Config{MaxConcurrent: 1})
	sentinel := errors.New("fn error")
	if err := b.Do(context.Background(), func(context.Context) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("Do returned %v, want fn's own error", err)
	}
	func() {
		defer func() { _ = recover() }()
		_ = b.Do(context.Background(), func(context.Context) error { panic("adapter bug") })
	}()
	if s := b.Stats(); s.InFlight != 0 {
		t.Errorf("InFlight = %d after a panicking fn, want 0: a panic must not leak the slot", s.InFlight)
	}
	var ran bool
	if err := b.Do(context.Background(), func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("Do after the panic: err=%v ran=%v", err, ran)
	}
}

func TestBulkhead_NeverExceedsMaxConcurrentUnderLoad(t *testing.T) {
	b := New(Config{MaxConcurrent: 5, MaxQueue: 1000})
	var cur, peak atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Do(context.Background(), func(context.Context) error {
				n := cur.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(200 * time.Microsecond)
				cur.Add(-1)
				return nil
			})
		}()
	}
	tWait(t, &wg, "200 callers through a 5-slot bulkhead")
	if peak.Load() > 5 {
		t.Errorf("peak concurrency %d, want ≤ 5", peak.Load())
	}
	if peak.Load() < 2 {
		t.Errorf("peak concurrency %d: the bulkhead should allow real parallelism up to its limit", peak.Load())
	}
	if s := b.Stats(); s.InFlight != 0 || s.Waiting != 0 || s.Admitted != 200 {
		t.Errorf("Stats = %+v; want everything drained and 200 admitted", s)
	}
}

func TestBulkhead_QueueIsBoundedAndServedInArrivalOrder(t *testing.T) {
	b := New(Config{MaxConcurrent: 1, MaxQueue: 2})
	holder := tHold(t, b)

	order := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 1; i <= 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := b.Acquire(context.Background())
			if err != nil {
				t.Errorf("waiter %d: %v", i, err)
				return
			}
			order <- i
			rel()
		}()
		tEventually(t, func() bool { return b.Stats().Waiting == i }, "waiter did not enter the queue")
	}
	if _, err := b.Acquire(context.Background()); !errors.Is(err, ErrFull) {
		t.Fatalf("3rd waiter: err = %v, want ErrFull (queue of 2 is full)", err)
	}
	holder()
	tWait(t, &wg, "queued waiters")
	if a, c := <-order, <-order; a != 1 || c != 2 {
		t.Errorf("waiters ran in order %d, %d; want FIFO 1, 2", a, c)
	}
	if s := b.Stats(); s.InFlight != 0 || s.Waiting != 0 {
		t.Errorf("Stats = %+v; want empty", s)
	}
}

func TestBulkhead_ACancelledWaiterLeavesWithoutTakingASlot(t *testing.T) {
	b := New(Config{MaxConcurrent: 1, MaxQueue: 5})
	holder := tHold(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := b.Acquire(ctx)
		errc <- err
	}()
	tEventually(t, func() bool { return b.Stats().Waiting == 1 }, "waiter did not queue")
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled waiter must return promptly")
	}
	if s := b.Stats(); s.Waiting != 0 {
		t.Errorf("Waiting = %d after the waiter left, want 0", s.Waiting)
	}
	holder()
	if s := b.Stats(); s.InFlight != 0 {
		t.Errorf("InFlight = %d after the holder released, want 0: the cancelled waiter must not have been handed the slot", s.InFlight)
	}
	tHold(t, b)()
}

func TestBulkhead_AlreadyCancelledContextIsRefusedWithoutSideEffects(t *testing.T) {
	b := New(Config{MaxConcurrent: 2})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if s := b.Stats(); s.InFlight != 0 || s.Admitted != 0 {
		t.Errorf("Stats = %+v; a refused call must not consume anything", s)
	}
}

func TestBulkhead_CancellationRacesNeverLeakSlots(t *testing.T) {
	b := New(Config{MaxConcurrent: 3, MaxQueue: 50})
	var wg sync.WaitGroup
	for i := 0; i < 300; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(i), 1))
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(r.IntN(400))*time.Microsecond)
			defer cancel()
			_ = b.Do(ctx, func(ctx context.Context) error {
				time.Sleep(time.Duration(r.IntN(200)) * time.Microsecond)
				return nil
			})
		}(i)
	}
	tWait(t, &wg, "racing callers")
	s := b.Stats()
	if s.InFlight != 0 || s.Waiting != 0 {
		t.Fatalf("Stats = %+v after all callers finished; want InFlight 0, Waiting 0 (a slot leaked in a cancel/grant race)", s)
	}
	var rels []func()
	for i := 0; i < 3; i++ { // all three slots must still be available
		rels = append(rels, tHold(t, b))
	}
	if got := b.Stats().InFlight; got != 3 {
		t.Errorf("InFlight = %d with three slots held, want 3", got)
	}
	for _, r := range rels {
		r()
	}
}

// ---------- Group ----------

func TestGroup_SaturatingOneDependencyDoesNotAffectAnother(t *testing.T) {
	g := NewGroup(map[string]Config{
		"imaging": {MaxConcurrent: 2},
		"fx":      {MaxConcurrent: 2},
	})
	block := make(chan struct{})
	started := make(chan struct{}, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := g.Do(context.Background(), "imaging", func(context.Context) error {
				started <- struct{}{}
				select {
				case <-block:
				case <-time.After(10 * time.Second):
				}
				return nil
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case err := <-errs:
			close(block)
			t.Fatalf("imaging call %d failed to start: %v", i+1, err)
		case <-time.After(5 * time.Second):
			close(block)
			t.Fatalf("imaging call %d did not start within 5 s", i+1)
		}
	}
	// the slow vendor now holds both of its slots
	if err := g.Do(context.Background(), "imaging", func(context.Context) error { return nil }); !errors.Is(err, ErrFull) {
		t.Errorf("imaging: err = %v, want ErrFull", err)
	}
	var ran bool
	if err := g.Do(context.Background(), "fx", func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("fx: err=%v ran=%v; a healthy dependency must not be starved by a slow one — that is the whole point of a bulkhead", err, ran)
	}
	st := g.Stats()
	if st["imaging"].InFlight != 2 || st["imaging"].Rejected != 1 || st["fx"].Admitted != 1 || st["fx"].Rejected != 0 {
		t.Errorf("Stats = %+v", st)
	}
	close(block)
	tWait(t, &wg, "blocked imaging calls")
}

func TestGroup_UnknownDependencyIsAnError(t *testing.T) {
	g := NewGroup(map[string]Config{"fx": {MaxConcurrent: 1}})
	var ran bool
	err := g.Do(context.Background(), "nope", func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, ErrUnknown) || ran {
		t.Errorf("err=%v ran=%v; want ErrUnknown and fn not run", err, ran)
	}
}

// ---------- AIMD ----------

func TestAIMD_AdmitsUpToTheLimitAndNoFurther(t *testing.T) {
	a := NewAIMD(AIMDConfig{Initial: 3, Min: 1, Max: 100})
	var dones []func(Outcome)
	for i := 0; i < 3; i++ {
		d, ok := a.TryAcquire()
		if !ok {
			t.Fatalf("TryAcquire %d refused, want admitted (limit 3)", i+1)
		}
		dones = append(dones, d)
	}
	if _, ok := a.TryAcquire(); ok {
		t.Errorf("4th TryAcquire admitted with limit 3: excess work must be shed, not queued")
	}
	if a.InFlight() != 3 {
		t.Errorf("InFlight = %d, want 3", a.InFlight())
	}
	dones[0](Ignore)
	dones[0](Dropped) // second call must do nothing
	if a.InFlight() != 2 || a.Limit() != 3 {
		t.Errorf("InFlight = %d, Limit = %v; want 2 and 3 (Ignore frees a slot and touches nothing else; done is idempotent)", a.InFlight(), a.Limit())
	}
}

func TestAIMD_SuccessesUnderLoadRaiseTheLimitAdditively(t *testing.T) {
	a := NewAIMD(AIMDConfig{Initial: 10, Min: 1, Max: 100})
	var dones []func(Outcome)
	for i := 0; i < 10; i++ {
		d, ok := a.TryAcquire()
		if !ok {
			t.Fatalf("TryAcquire %d refused", i+1)
		}
		dones = append(dones, d)
	}
	// The calls admitted with 0..4 others in flight were admitted at less
	// than 50% utilisation and do not count; the five admitted with 5..9
	// in flight each add 1/limit ≈ 0.1.
	for _, d := range dones {
		d(Success)
	}
	if l := a.Limit(); l < 10.4 || l > 10.6 {
		t.Errorf("Limit = %v, want ≈ 10.5: five loaded successes at limit ≈ 10 add 5 × 1/limit", l)
	}
	// Another full round with everything loaded adds about +1.
	before := a.Limit()
	dones = dones[:0]
	for i := 0; i < int(before); i++ {
		dones = append(dones, tAcquire(t, a))
	}
	for _, d := range dones {
		d(Success)
	}
	if grew := a.Limit() - before; grew < 0.3 || grew > 1.0 {
		t.Errorf("Limit grew by %v in a loaded round, want between 0.3 and 1.0 (≈ +1 per round trip at most)", grew)
	}
}

func TestAIMD_SuccessesWhileIdleDoNotInflateTheLimit(t *testing.T) {
	a := NewAIMD(AIMDConfig{Initial: 10, Min: 1, Max: 100})
	for i := 0; i < 200; i++ {
		d, ok := a.TryAcquire() // one request at a time: 10% utilisation
		if !ok {
			t.Fatal("refused")
		}
		d(Success)
	}
	if l := a.Limit(); l != 10 {
		t.Errorf("Limit = %v after 200 sequential successes, want 10: a limit that grows while the system is idle says nothing about capacity", l)
	}
}

func TestAIMD_OverloadHalvesTheLimitOncePerCooldown(t *testing.T) {
	c := tNewClock()
	a := NewAIMD(AIMDConfig{Initial: 20, Min: 1, Max: 100, Decrease: 0.5, Cooldown: time.Second, Now: c.Now})
	var dones []func(Outcome)
	for i := 0; i < 6; i++ {
		dones = append(dones, tAcquire(t, a))
	}
	for _, d := range dones { // six calls fail together: one overload event
		d(Dropped)
	}
	if l := a.Limit(); l != 10 {
		t.Fatalf("Limit = %v after a burst of 6 drops, want 10: one overload event must cause ONE decrease (20 → 10), not 20 × 0.5^6", l)
	}
	d := tAcquire(t, a)
	c.Advance(time.Second)
	d(Dropped)
	if l := a.Limit(); l != 5 {
		t.Errorf("Limit = %v after a drop one cooldown later, want 5", l)
	}
}

func TestAIMD_LimitStaysWithinMinAndMax(t *testing.T) {
	c := tNewClock()
	a := NewAIMD(AIMDConfig{Initial: 4, Min: 3, Max: 5, Cooldown: time.Millisecond, Now: c.Now})
	for i := 0; i < 10; i++ {
		d, ok := a.TryAcquire()
		if !ok {
			continue
		}
		c.Advance(time.Second)
		d(Dropped)
	}
	if l := a.Limit(); l != 3 {
		t.Errorf("Limit = %v after repeated drops, want Min = 3", l)
	}
	for i := 0; i < 2000; i++ {
		var ds []func(Outcome)
		for {
			d, ok := a.TryAcquire()
			if !ok {
				break
			}
			ds = append(ds, d)
		}
		for _, d := range ds {
			d(Success)
		}
	}
	if l := a.Limit(); l > 5 || l < 4.9 {
		t.Errorf("Limit = %v after many loaded successes, want capped at Max = 5", l)
	}
}

func TestAIMD_ConvergesToTheNeighbourhoodOfTheServersCapacity(t *testing.T) {
	// A server that can really handle 40 concurrent calls and drops the
	// rest. Each virtual second, 200 callers try; the limiter admits up to
	// its limit; the server answers the first 40 and drops the rest.
	const capacity = 40
	c := tNewClock()
	a := NewAIMD(AIMDConfig{Initial: 5, Min: 1, Max: 1000, Cooldown: time.Second, Now: c.Now})
	var history []float64
	for round := 0; round < 300; round++ {
		var ds []func(Outcome)
		for i := 0; i < 200; i++ {
			if d, ok := a.TryAcquire(); ok {
				ds = append(ds, d)
			}
		}
		for i, d := range ds {
			if i < capacity {
				d(Success)
			} else {
				d(Dropped)
			}
		}
		c.Advance(time.Second)
		if round >= 200 {
			history = append(history, a.Limit())
		}
	}
	lo, hi, sum := history[0], history[0], 0.0
	for _, l := range history {
		lo, hi, sum = min(lo, l), max(hi, l), sum+l
	}
	mean := sum / float64(len(history))
	t.Logf("limit over the last 100 rounds: min %.1f, max %.1f, mean %.1f (capacity %d)", lo, hi, mean, capacity)
	if lo < capacity/2-2 || hi > capacity+3 {
		t.Errorf("limit swung between %.1f and %.1f, want inside [%d, %d]: AIMD saw-tooths between ≈ ½ and ≈ 1× the capacity", lo, hi, capacity/2-2, capacity+3)
	}
	if mean < 0.6*capacity || mean > 1.0*capacity {
		t.Errorf("mean limit %.1f, want within [0.6, 1.0] × capacity", mean)
	}
}

func TestAIMD_ConcurrentUseIsRaceFree(t *testing.T) {
	a := NewAIMD(AIMDConfig{Initial: 8, Min: 1, Max: 64, Cooldown: time.Millisecond})
	var wg sync.WaitGroup
	var peak, cur atomic.Int64
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				d, ok := a.TryAcquire()
				if !ok {
					continue
				}
				n := cur.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				cur.Add(-1)
				switch (g + i) % 7 {
				case 0:
					d(Dropped)
				case 1:
					d(Ignore)
				default:
					d(Success)
				}
				_ = a.Limit()
			}
		}(g)
	}
	tWait(t, &wg, "concurrent AIMD callers")
	if a.InFlight() != 0 {
		t.Errorf("InFlight = %d after all calls finished, want 0", a.InFlight())
	}
	if l := a.Limit(); l < 1 || l > 64 {
		t.Errorf("Limit = %v outside [1, 64]", l)
	}
}
