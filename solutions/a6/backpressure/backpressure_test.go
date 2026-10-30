package backpressure

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

func tOffer(t *testing.T, q *Queue[int], v int) {
	t.Helper()
	if err := q.Offer(context.Background(), v); err != nil {
		t.Fatalf("Offer(%d): %v", v, err)
	}
}

func tTake(t *testing.T, q *Queue[int]) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	v, err := q.Take(ctx)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	return v
}

func tDrain(q *Queue[int]) []int {
	var out []int
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		v, err := q.Take(ctx)
		cancel()
		if err != nil {
			return out
		}
		out = append(out, v)
	}
}

func tEq(a, b []int) bool {
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

func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock or lost wake-up in the implementation?)", what, d)
	}
}

// tBlocked reports whether fn is still blocked after a grace period. A correct
// implementation never fails this by being slow; a wrong one that returns
// immediately is caught.
func tBlocked(fn func()) (finished chan struct{}, stillBlocked bool) {
	finished = make(chan struct{})
	go func() { fn(); close(finished) }()
	select {
	case <-finished:
		return finished, false
	case <-time.After(40 * time.Millisecond):
		return finished, true
	}
}

// ---------- Queue ----------

func TestQueue_IsFIFO(t *testing.T) {
	q := NewQueue[int](5, Block)
	for i := 1; i <= 5; i++ {
		tOffer(t, q, i)
	}
	if q.Len() != 5 {
		t.Fatalf("Len = %d, want 5", q.Len())
	}
	var got []int
	for i := 0; i < 5; i++ {
		got = append(got, tTake(t, q))
	}
	if !tEq(got, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("took %v, want 1..5 in order", got)
	}
}

func TestQueue_BlockPolicyMakesTheProducerWaitUntilThereIsRoom(t *testing.T) {
	q := NewQueue[int](2, Block)
	tOffer(t, q, 1)
	tOffer(t, q, 2)
	finished, blocked := tBlocked(func() { _ = q.Offer(context.Background(), 3) })
	if !blocked {
		t.Fatalf("Offer on a full Block queue returned immediately; backpressure means the producer is slowed to the consumer's pace")
	}
	if got := tTake(t, q); got != 1 {
		t.Fatalf("Take = %d, want 1", got)
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatalf("the blocked Offer was not woken when space appeared")
	}
	if got := tDrain(q); !tEq(got, []int{2, 3}) {
		t.Fatalf("remaining = %v, want [2 3]", got)
	}
}

func TestQueue_BlockedOfferHonoursContextAndEnqueuesNothing(t *testing.T) {
	q := NewQueue[int](1, Block)
	tOffer(t, q, 1)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- q.Offer(ctx, 2) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Offer err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Offer ignored cancellation: a producer stuck on a full queue can never shut down")
	}
	if got := tDrain(q); !tEq(got, []int{1}) {
		t.Fatalf("queue = %v, want [1]: a cancelled Offer must not enqueue", got)
	}
	if st := q.Stats(); st.Accepted != 1 {
		t.Fatalf("Accepted = %d, want 1", st.Accepted)
	}
}

func TestQueue_DropNewestRejectsAndKeepsWhatIsAlreadyQueued(t *testing.T) {
	q := NewQueue[int](2, DropNewest)
	tOffer(t, q, 1)
	tOffer(t, q, 2)
	if err := q.Offer(context.Background(), 3); !errors.Is(err, ErrFull) {
		t.Fatalf("Offer on a full DropNewest queue: err = %v, want ErrFull (and it must not block)", err)
	}
	if got := tDrain(q); !tEq(got, []int{1, 2}) {
		t.Fatalf("queue = %v, want [1 2]", got)
	}
	if st := q.Stats(); st.Accepted != 2 || st.Rejected != 1 || st.Taken != 2 {
		t.Fatalf("stats = %+v, want Accepted 2, Rejected 1, Taken 2", st)
	}
}

func TestQueue_DropOldestEvictsTheHeadToMakeRoom(t *testing.T) {
	q := NewQueue[int](3, DropOldest)
	for i := 1; i <= 5; i++ {
		tOffer(t, q, i)
	}
	if got := tDrain(q); !tEq(got, []int{3, 4, 5}) {
		t.Fatalf("queue = %v, want [3 4 5]: the newest items survive", got)
	}
	if st := q.Stats(); st.Accepted != 5 || st.Evicted != 2 {
		t.Fatalf("stats = %+v, want Accepted 5, Evicted 2", st)
	}
}

func TestQueue_TakeWaitsForAnItemAndHonoursContext(t *testing.T) {
	q := NewQueue[int](2, Block)
	got := make(chan int, 1)
	go func() {
		v, err := q.Take(context.Background())
		if err == nil {
			got <- v
		}
	}()
	time.Sleep(20 * time.Millisecond)
	tOffer(t, q, 42)
	select {
	case v := <-got:
		if v != 42 {
			t.Fatalf("Take = %d, want 42", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Take was not woken by an Offer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := q.Take(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Take on an empty queue: err = %v, want context.DeadlineExceeded", err)
	}
}

func TestQueue_CloseDrainsThenReportsClosedAndWakesBlockedProducers(t *testing.T) {
	q := NewQueue[int](1, Block)
	tOffer(t, q, 1)
	errc := make(chan error, 1)
	go func() { errc <- q.Offer(context.Background(), 2) }()
	time.Sleep(20 * time.Millisecond)
	q.Close()
	q.Close() // idempotent
	select {
	case err := <-errc:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("blocked Offer after Close: err = %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Close did not wake a producer blocked in Offer")
	}
	if err := q.Offer(context.Background(), 3); !errors.Is(err, ErrClosed) {
		t.Fatalf("Offer after Close: err = %v, want ErrClosed", err)
	}
	if got := tTake(t, q); got != 1 {
		t.Fatalf("Take after Close = %d, want the queued 1: closing must not lose accepted items", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := q.Take(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("Take on a closed empty queue: err = %v, want ErrClosed", err)
	}
}

func TestQueue_ConcurrentProducersAndConsumersLoseAndDuplicateNothing(t *testing.T) {
	const producers, perProducer, consumers = 8, 500, 4
	q := NewQueue[int](16, Block)
	var seen sync.Map
	var dup atomic.Int64
	var taken atomic.Int64
	var cwg, pwg sync.WaitGroup
	for c := 0; c < consumers; c++ {
		cwg.Add(1)
		go func() {
			defer cwg.Done()
			for {
				v, err := q.Take(context.Background())
				if err != nil {
					return
				}
				if _, loaded := seen.LoadOrStore(v, true); loaded {
					dup.Add(1)
				}
				taken.Add(1)
			}
		}()
	}
	for p := 0; p < producers; p++ {
		pwg.Add(1)
		go func(p int) {
			defer pwg.Done()
			for i := 0; i < perProducer; i++ {
				if err := q.Offer(context.Background(), p*perProducer+i); err != nil {
					t.Errorf("Offer: %v", err)
					return
				}
			}
		}(p)
	}
	tWait(t, &pwg, 15*time.Second, "producers")
	q.Close()
	tWait(t, &cwg, 15*time.Second, "consumers")
	if taken.Load() != producers*perProducer || dup.Load() != 0 {
		t.Fatalf("taken %d (want %d), duplicates %d", taken.Load(), producers*perProducer, dup.Load())
	}
}

func TestQueue_DropNewestUnderOverloadAccountsForEveryOffer(t *testing.T) {
	const producers, perProducer = 8, 1000
	q := NewQueue[int](10, DropNewest)
	var offered, rejected atomic.Int64
	var pwg, cwg sync.WaitGroup
	var taken atomic.Int64
	cwg.Add(1)
	go func() {
		defer cwg.Done()
		for {
			if _, err := q.Take(context.Background()); err != nil {
				return
			}
			taken.Add(1)
			time.Sleep(10 * time.Microsecond) // a consumer slower than the producers
		}
	}()
	for p := 0; p < producers; p++ {
		pwg.Add(1)
		go func() {
			defer pwg.Done()
			for i := 0; i < perProducer; i++ {
				offered.Add(1)
				if err := q.Offer(context.Background(), i); errors.Is(err, ErrFull) {
					rejected.Add(1)
				} else if err != nil {
					t.Errorf("Offer: %v", err)
					return
				}
			}
		}()
	}
	tWait(t, &pwg, 15*time.Second, "producers")
	q.Close()
	tWait(t, &cwg, 15*time.Second, "consumer")
	st := q.Stats()
	if st.Accepted+st.Rejected != offered.Load() || st.Rejected != rejected.Load() {
		t.Fatalf("stats %+v vs offered %d, rejected %d: every Offer must be either accepted or rejected, never silently lost", st, offered.Load(), rejected.Load())
	}
	if taken.Load() != st.Accepted {
		t.Fatalf("consumer took %d, queue accepted %d: accepted items must all be delivered", taken.Load(), st.Accepted)
	}
	if st.Rejected == 0 {
		t.Fatalf("nothing was rejected although the producers outran the consumer by far; was the queue really bounded?")
	}
}

// ---------- PriorityQueue ----------

func TestPriorityQueue_TakesMostImportantFirstAndFIFOWithinAPriority(t *testing.T) {
	q := NewPriorityQueue[string](100, nil)
	for _, in := range []struct {
		p Priority
		v string
	}{{Bulk, "b1"}, {Standard, "s1"}, {Critical, "c1"}, {Bulk, "b2"}, {Critical, "c2"}, {Standard, "s2"}} {
		if err := q.Offer(in.p, in.v); err != nil {
			t.Fatalf("Offer %s: %v", in.v, err)
		}
	}
	var got []string
	for {
		v, _, ok := q.Take()
		if !ok {
			break
		}
		got = append(got, v)
	}
	want := []string{"c1", "c2", "s1", "s2", "b1", "b2"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if _, _, ok := q.Take(); ok {
		t.Fatalf("Take on an empty queue returned ok")
	}
}

func TestPriorityQueue_LowerPrioritiesAreShedFirstAsTheQueueFills(t *testing.T) {
	q := NewPriorityQueue[int](10, nil) // defaults: Critical 100%, Standard 80%, Bulk 50%
	admit := func(p Priority) bool { return q.Offer(p, 0) == nil }
	for i := 0; i < 5; i++ {
		if !admit(Bulk) {
			t.Fatalf("bulk item %d refused while the queue holds only %d", i, q.Len())
		}
	}
	if admit(Bulk) {
		t.Fatalf("bulk admitted with 5 of 10 slots used; bulk work must be refused first")
	}
	for i := 0; i < 3; i++ {
		if !admit(Standard) {
			t.Fatalf("standard item refused with %d of 10 used", q.Len())
		}
	}
	if admit(Standard) {
		t.Fatalf("standard admitted with 8 of 10 slots used")
	}
	for i := 0; i < 2; i++ {
		if !admit(Critical) {
			t.Fatalf("critical item refused with %d of 10 used: critical may use the whole queue", q.Len())
		}
	}
	if err := q.Offer(Critical, 0); !errors.Is(err, ErrShed) {
		t.Fatalf("critical on a completely full queue: err = %v, want ErrShed", err)
	}
	shed := q.Shed()
	if shed[Bulk] != 1 || shed[Standard] != 1 || shed[Critical] != 1 {
		t.Fatalf("shed counters = %v, want one of each", shed)
	}
}

func TestPriorityQueue_CustomThresholdsOverrideDefaults(t *testing.T) {
	q := NewPriorityQueue[int](10, map[Priority]float64{Bulk: 0.2})
	n := 0
	for q.Offer(Bulk, 0) == nil && n < 20 {
		n++
	}
	if n != 2 {
		t.Fatalf("bulk admitted %d items with threshold 0.2 of 10, want 2", n)
	}
	// Standard keeps its default of 0.8.
	for q.Len() < 8 {
		if err := q.Offer(Standard, 0); err != nil {
			t.Fatalf("standard refused with Len %d: %v", q.Len(), err)
		}
	}
	if err := q.Offer(Standard, 0); !errors.Is(err, ErrShed) {
		t.Fatalf("standard at Len 8: err = %v, want ErrShed", err)
	}
}

func TestPriorityQueue_UnderOverloadCriticalWorkSurvivesAndBulkTakesTheLosses(t *testing.T) {
	// 3 arrivals per tick, 1 service per tick: 3x overload. Mix: 60% bulk,
	// 30% standard, 10% critical, in a fixed pseudo-random order.
	rng := rand.New(rand.NewSource(7))
	q := NewPriorityQueue[int](40, nil)
	offered := map[Priority]int{}
	servedByPrio := map[Priority]int{}
	for tick := 0; tick < 3000; tick++ {
		for a := 0; a < 3; a++ {
			var p Priority
			switch r := rng.Float64(); {
			case r < 0.6:
				p = Bulk
			case r < 0.9:
				p = Standard
			default:
				p = Critical
			}
			offered[p]++
			_ = q.Offer(p, tick)
		}
		if _, p, ok := q.Take(); ok {
			servedByPrio[p]++
		}
	}
	shed := q.Shed()
	frac := func(p Priority) float64 { return float64(shed[p]) / float64(offered[p]) }
	if shed[Critical] != 0 {
		t.Fatalf("%d critical requests were shed (%.0f%%) although critical traffic is only ≈ 0.3 per tick against a service rate of 1: shed low priorities first", shed[Critical], 100*frac(Critical))
	}
	if !(frac(Bulk) > frac(Standard) && frac(Standard) >= frac(Critical)) {
		t.Fatalf("shed fractions bulk %.2f, standard %.2f, critical %.2f: want bulk > standard >= critical", frac(Bulk), frac(Standard), frac(Critical))
	}
	if servedByPrio[Critical] != offered[Critical] {
		t.Fatalf("served %d of %d critical requests, want all of them", servedByPrio[Critical], offered[Critical])
	}
}

// ---------- CreditGate ----------

func TestCreditGate_AcquireBlocksUntilTheReceiverGrantsCredit(t *testing.T) {
	g := NewCreditGate(1)
	if err := g.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire with a credit available: %v", err)
	}
	if g.TryAcquire() {
		t.Fatalf("TryAcquire succeeded with zero credits")
	}
	finished, blocked := tBlocked(func() { _ = g.Acquire(context.Background()) })
	if !blocked {
		t.Fatalf("Acquire returned with no credit: a sender must stop when the receiver has granted nothing")
	}
	g.Grant(1)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatalf("Grant did not wake the waiting sender")
	}
	if g.Available() != 0 {
		t.Fatalf("Available = %d, want 0 (the woken sender consumed the credit)", g.Available())
	}
	g.Grant(0)
	g.Grant(-3)
	if g.Available() != 0 {
		t.Fatalf("Grant of a non-positive amount changed Available to %d", g.Available())
	}
}

func TestCreditGate_AcquireHonoursContextAndConsumesNothing(t *testing.T) {
	g := NewCreditGate(0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := g.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire err = %v, want context.DeadlineExceeded", err)
	}
	g.Grant(2)
	if g.Available() != 2 {
		t.Fatalf("Available = %d, want 2: the cancelled Acquire must not have consumed anything", g.Available())
	}
}

func TestCreditGate_ConcurrentSendersNeverUseMoreCreditThanWasGranted(t *testing.T) {
	const senders, total = 20, 1000
	g := NewCreditGate(0)
	var sent atomic.Int64
	var wg sync.WaitGroup
	for s := 0; s < senders; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for i := 0; i < total/senders; i++ {
				if err := g.Acquire(ctx); err != nil {
					t.Errorf("Acquire: %v (a credit was granted for every send)", err)
					return
				}
				sent.Add(1)
			}
		}()
	}
	granted := 0
	for granted < total {
		g.Grant(25)
		granted += 25
		if sent.Load() > int64(granted) {
			t.Fatalf("senders used %d credits but only %d were granted", sent.Load(), granted)
		}
		time.Sleep(100 * time.Microsecond)
	}
	tWait(t, &wg, 15*time.Second, "senders")
	if sent.Load() != total || g.Available() != 0 {
		t.Fatalf("sent %d (want %d), credits left %d (want 0)", sent.Load(), total, g.Available())
	}
}

// ---------- Simulate ----------

func tNear(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

func TestSimulate_UnderloadedSystemNeverQueues(t *testing.T) {
	r := Simulate(SimConfig{ArrivalRate: 50, ServiceTime: 10 * time.Millisecond, Servers: 1, Duration: 10 * time.Second})
	if r.Arrived != 500 || r.Served != 500 || r.Dropped != 0 {
		t.Fatalf("arrived/served/dropped = %d/%d/%d, want 500/500/0", r.Arrived, r.Served, r.Dropped)
	}
	if r.MaxWait != 0 || r.MaxQueue != 0 || r.MeanSojourn != 10*time.Millisecond {
		t.Fatalf("MaxWait %v, MaxQueue %d, MeanSojourn %v; at 50%% utilisation nothing should wait and sojourn equals service time", r.MaxWait, r.MaxQueue, r.MeanSojourn)
	}
	if !tNear(r.MeanInSystem, 0.5, 0.01) {
		t.Fatalf("MeanInSystem = %.3f, want ≈ 0.5 (utilisation)", r.MeanInSystem)
	}
}

func TestSimulate_OverloadWithoutALimitGrowsLatencyLinearlyForever(t *testing.T) {
	// 150 req/s arrive at a server that finishes 100 req/s, for 10 s.
	r := Simulate(SimConfig{ArrivalRate: 150, ServiceTime: 10 * time.Millisecond, Servers: 1, Duration: 10 * time.Second})
	if r.Arrived != 1500 || r.Served != 1500 || r.Dropped != 0 {
		t.Fatalf("arrived/served/dropped = %d/%d/%d, want 1500/1500/0 (an unbounded queue accepts everything)", r.Arrived, r.Served, r.Dropped)
	}
	if !tNear(float64(r.MaxQueue), 500, 5) {
		t.Fatalf("MaxQueue = %d, want ≈ 500: (150-100) req/s x 10 s of excess arrivals", r.MaxQueue)
	}
	if !tNear(r.MaxWait.Seconds(), 5.0, 0.1) {
		t.Fatalf("MaxWait = %v, want ≈ 5s: the last request waits behind ≈ 500 others at 10 ms each", r.MaxWait)
	}
	if !tNear(r.MeanWait.Seconds(), 2.5, 0.1) {
		t.Fatalf("MeanWait = %v, want ≈ 2.5s (wait grows linearly from 0 to 5 s)", r.MeanWait)
	}
	if !tNear(r.End.Seconds(), 15.0, 0.1) {
		t.Fatalf("End = %v, want ≈ 15s: it takes 5 s after the arrivals stop to drain the backlog", r.End)
	}
}

func TestSimulate_BoundedQueueTradesDroppedRequestsForBoundedLatency(t *testing.T) {
	r := Simulate(SimConfig{ArrivalRate: 150, ServiceTime: 10 * time.Millisecond, Servers: 1, QueueCap: 50, Duration: 10 * time.Second})
	if r.Dropped < 400 || r.Dropped > 500 {
		t.Fatalf("Dropped = %d, want ≈ 450 (the excess 50 req/s for 10 s, minus the queue's 50 slots)", r.Dropped)
	}
	if r.Served+r.Dropped != r.Arrived {
		t.Fatalf("served %d + dropped %d != arrived %d", r.Served, r.Dropped, r.Arrived)
	}
	if r.MaxWait > 520*time.Millisecond {
		t.Fatalf("MaxWait = %v, want ≤ ≈ 510ms (50 queued x 10 ms + the request in service): the bound on the queue is the bound on latency", r.MaxWait)
	}
	if r.MaxQueue != 50 {
		t.Fatalf("MaxQueue = %d, want 50", r.MaxQueue)
	}
	if r.End > 10*time.Second+600*time.Millisecond {
		t.Fatalf("End = %v: the system should drain within ≈ 0.5 s of the last arrival", r.End)
	}
}

func TestSimulate_MoreServersRemoveTheQueue(t *testing.T) {
	one := Simulate(SimConfig{ArrivalRate: 150, ServiceTime: 10 * time.Millisecond, Servers: 1, Duration: 5 * time.Second})
	two := Simulate(SimConfig{ArrivalRate: 150, ServiceTime: 10 * time.Millisecond, Servers: 2, Duration: 5 * time.Second})
	if one.MaxWait == 0 || two.MaxWait != 0 || two.MaxQueue != 0 {
		t.Fatalf("1 server MaxWait %v; 2 servers MaxWait %v MaxQueue %d: capacity 200 req/s covers 150 req/s, 100 does not", one.MaxWait, two.MaxWait, two.MaxQueue)
	}
	four := Simulate(SimConfig{ArrivalRate: 300, ServiceTime: 10 * time.Millisecond, Servers: 4, Duration: 5 * time.Second})
	if four.MaxWait != 0 {
		t.Fatalf("4 servers at 300 req/s (75%% utilisation, evenly spaced arrivals) waited %v", four.MaxWait)
	}
}

func TestSimulate_LittlesLawHoldsInEveryRegime(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  SimConfig
	}{
		{"underloaded", SimConfig{ArrivalRate: 50, ServiceTime: 10 * time.Millisecond, Servers: 1, Duration: 10 * time.Second}},
		{"overloaded, unbounded", SimConfig{ArrivalRate: 150, ServiceTime: 10 * time.Millisecond, Servers: 1, Duration: 10 * time.Second}},
		{"overloaded, bounded", SimConfig{ArrivalRate: 150, ServiceTime: 10 * time.Millisecond, Servers: 1, QueueCap: 50, Duration: 10 * time.Second}},
		{"four servers", SimConfig{ArrivalRate: 700, ServiceTime: 10 * time.Millisecond, Servers: 4, Duration: 10 * time.Second}},
	} {
		r := Simulate(c.cfg)
		if r.Served == 0 {
			t.Fatalf("%s: nothing served", c.name)
		}
		throughput := float64(r.Served) / r.End.Seconds()
		want := throughput * r.MeanSojourn.Seconds()
		if math.Abs(r.MeanInSystem-want) > 1e-3*math.Max(want, 1) {
			t.Fatalf("%s: MeanInSystem %.4f but throughput x mean sojourn = %.4f x %v = %.4f; L = lambda x W must hold (check how you integrate the number in system)",
				c.name, r.MeanInSystem, throughput, r.MeanSojourn, want)
		}
	}
}

func TestSimulate_DegenerateConfigsReturnZeroResults(t *testing.T) {
	if r := Simulate(SimConfig{}); r.Arrived != 0 || r.Served != 0 {
		t.Fatalf("Simulate(zero config) = %+v, want an empty result", r)
	}
	if r := Simulate(SimConfig{ArrivalRate: 10, ServiceTime: time.Millisecond, Duration: 0}); r.Arrived != 0 {
		t.Fatalf("zero duration produced %d arrivals", r.Arrived)
	}
}
