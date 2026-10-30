package snowflake

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tEpoch is the epoch every test layout uses; tStart is where most fake clocks
// begin: exactly at the start of tick 10,000 of a 1 ms layout.
var (
	tEpoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	tStart = tEpoch.Add(10 * time.Second)
)

// tClock is a fake clock. Now returns the current fake time (then moves it on
// by step, a clock that advances on its own); Sleep moves it forward by d
// unless the clock is frozen. Nothing ever really waits.
type tClock struct {
	mu     sync.Mutex
	now    time.Time
	step   time.Duration
	frozen bool
	sleeps int
	slept  time.Duration
}

func tNewClock(at time.Time) *tClock { return &tClock{now: at} }

func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.now
	c.now = c.now.Add(c.step)
	return n
}

func (c *tClock) Sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps++
	c.slept += d
	if !c.frozen && d > 0 {
		c.now = c.now.Add(d)
	}
}

func (c *tClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *tClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *tClock) Peek() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *tClock) Stats() (sleeps int, slept time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sleeps, c.slept
}

// tSkewed is a second machine's view of a shared fake clock: off away from it.
// Sleeping advances the shared clock.
type tSkewed struct {
	base *tClock
	off  time.Duration
}

func (s tSkewed) Now() time.Time        { return s.base.Now().Add(s.off) }
func (s tSkewed) Sleep(d time.Duration) { s.base.Sleep(d) }

func tLayout(timeBits, nodeBits, seqBits uint) Layout {
	return Layout{TimeBits: timeBits, NodeBits: nodeBits, SeqBits: seqBits, Tick: time.Millisecond, Epoch: tEpoch}
}

func tGen(t *testing.T, cfg Config) *Generator {
	t.Helper()
	g, err := NewGenerator(cfg)
	if err != nil {
		t.Fatalf("NewGenerator(%+v): %v", cfg, err)
	}
	return g
}

func tNext(t *testing.T, g interface{ Next() (ID, error) }) ID {
	t.Helper()
	id, err := g.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return id
}

func tTick(t *testing.T, l Layout, at time.Time) uint64 {
	t.Helper()
	k, err := l.TickOf(at)
	if err != nil {
		t.Fatalf("TickOf(%v): %v", at, err)
	}
	return k
}

func tRegistry(t *testing.T, nodes uint64, ttl time.Duration, c Clock) *Registry {
	t.Helper()
	r, err := NewRegistry(nodes, ttl, c)
	if err != nil {
		t.Fatalf("NewRegistry(%d, %v): %v", nodes, ttl, err)
	}
	return r
}

func tWorker(t *testing.T, r *Registry, holder string, cfg WorkerConfig) *Worker {
	t.Helper()
	w, err := StartWorker(r, holder, cfg)
	if err != nil {
		t.Fatalf("StartWorker(%q): %v", holder, err)
	}
	return w
}

// tWait waits for wg, failing the test instead of hanging if it takes longer
// than d (a deadlock, or a Next that loops forever).
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: still running after %v (deadlock, or a Next that never returns?)", what, d)
	}
}

// tWithin runs f in a goroutine and fails the test if it has not returned
// within d.
func tWithin(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: still running after %v; a generator must return an error, not hang", what, d)
	}
}

// tCollect gathers ids from concurrent goroutines and reports duplicates.
type tCollect struct {
	mu   sync.Mutex
	seen map[ID]bool
	dups int
}

func (c *tCollect) add(ids ...ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = make(map[ID]bool)
	}
	for _, id := range ids {
		if c.seen[id] {
			c.dups++
		}
		c.seen[id] = true
	}
}

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

func TestLayout_DefaultIs41_10_12AndLastsAbout69Years(t *testing.T) {
	l := DefaultLayout()
	if l.TimeBits != 41 || l.NodeBits != 10 || l.SeqBits != 12 || l.Tick != time.Millisecond || !l.Epoch.Equal(DefaultEpoch) {
		t.Fatalf("DefaultLayout() = %+v, want 41/10/12 bits, 1 ms ticks, DefaultEpoch", l)
	}
	if err := l.Validate(); err != nil {
		t.Fatalf("DefaultLayout().Validate() = %v, want nil", err)
	}
	if l.Nodes() != 1024 || l.PerTick() != 4096 {
		t.Errorf("Nodes() = %d, PerTick() = %d; want 1024 and 4096", l.Nodes(), l.PerTick())
	}
	years := l.Lifetime().Hours() / 24 / 365.25
	if math.Abs(years-69.68) > 0.1 {
		t.Errorf("Lifetime() = %v ≈ %.2f years, want ≈ 69.7 (2^41 ms)", l.Lifetime(), years)
	}
	if got := l.NodeRate(); math.Abs(got-4.096e6) > 1 {
		t.Errorf("NodeRate() = %.0f ids/s, want 4,096,000 (4,096 per ms)", got)
	}
	sony := Layout{TimeBits: 39, NodeBits: 16, SeqBits: 8, Tick: 10 * time.Millisecond, Epoch: tEpoch}
	if y := sony.Lifetime().Hours() / 24 / 365.25; math.Abs(y-174.2) > 0.5 {
		t.Errorf("39-bit, 10 ms layout: Lifetime ≈ %.1f years, want ≈ 174", y)
	}
	if got := sony.NodeRate(); math.Abs(got-25600) > 1 {
		t.Errorf("39/16/8 at 10 ms: NodeRate() = %.0f, want 25,600 ids/s", got)
	}
	if l48 := tLayout(48, 7, 8); l48.Lifetime() <= 0 || l48.Lifetime() < 290*365*24*time.Hour {
		t.Errorf("48-bit ms layout: Lifetime() = %v; want it to saturate near the largest time.Duration (≈ 292 years), not overflow", l48.Lifetime())
	}
}

func TestLayout_ValidateRejectsLayoutsThatDoNotFit(t *testing.T) {
	bad := []Layout{
		tLayout(42, 10, 12), // 64 bits: the sign bit would be used
		tLayout(0, 10, 12),  // no time
		{TimeBits: 41, NodeBits: 10, SeqBits: 12, Tick: 0, Epoch: tEpoch},
		{TimeBits: 41, NodeBits: 10, SeqBits: 12, Tick: -time.Millisecond, Epoch: tEpoch},
		{TimeBits: 41, NodeBits: 10, SeqBits: 12, Tick: time.Millisecond}, // zero epoch
		tLayout(1, 63, 0),
	}
	for _, l := range bad {
		if err := l.Validate(); !errors.Is(err, ErrBadLayout) {
			t.Errorf("Validate(%d/%d/%d, tick %v, epoch %v) = %v, want ErrBadLayout", l.TimeBits, l.NodeBits, l.SeqBits, l.Tick, l.Epoch, err)
		}
	}
	good := []Layout{tLayout(41, 10, 12), tLayout(41, 11, 11), tLayout(63, 0, 0), {TimeBits: 39, NodeBits: 16, SeqBits: 8, Tick: 10 * time.Millisecond, Epoch: tEpoch}}
	for _, l := range good {
		if err := l.Validate(); err != nil {
			t.Errorf("Validate(%d/%d/%d) = %v, want nil", l.TimeBits, l.NodeBits, l.SeqBits, err)
		}
	}
}

func TestLayout_ComposeAndDecomposeRoundTrip(t *testing.T) {
	l := DefaultLayout()
	cases := []Parts{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}, {0, 0, 1}, {123456789, 777, 4095}, {1<<41 - 1, 1023, 4095}}
	for _, p := range cases {
		id, err := l.Compose(p.Tick, p.Node, p.Seq)
		if err != nil {
			t.Fatalf("Compose(%+v): %v", p, err)
		}
		if got := l.Decompose(id); got != p {
			t.Errorf("Decompose(Compose(%+v)) = %+v", p, got)
		}
		if uint64(id) >= 1<<63 {
			t.Errorf("Compose(%+v) = %d sets the top bit; ids must fit a signed 64-bit column", p, id)
		}
	}
	if id, _ := l.Compose(1, 0, 0); id != 1<<22 {
		t.Errorf("Compose(tick 1, node 0, seq 0) = %d, want 1<<22 (the tick sits above 10 node + 12 seq bits)", id)
	}
	if id, _ := l.Compose(0, 1, 0); id != 1<<12 {
		t.Errorf("Compose(tick 0, node 1, seq 0) = %d, want 1<<12", id)
	}
	for _, p := range []Parts{{1 << 41, 0, 0}, {0, 1024, 0}, {0, 0, 4096}} {
		if _, err := l.Compose(p.Tick, p.Node, p.Seq); !errors.Is(err, ErrOverflow) {
			t.Errorf("Compose(%+v) = %v, want ErrOverflow: a field that spills into its neighbour mints someone else's id", p, err)
		}
	}
}

func TestLayout_NumericOrderIsTimeThenNodeThenSeq(t *testing.T) {
	l := tLayout(41, 10, 12)
	ordered := []Parts{{5, 0, 0}, {5, 0, 4095}, {5, 1, 0}, {5, 1023, 4095}, {6, 0, 0}, {6, 3, 2}, {1000, 0, 0}}
	var prev ID
	for i, p := range ordered {
		id, err := l.Compose(p.Tick, p.Node, p.Seq)
		if err != nil {
			t.Fatalf("Compose(%+v): %v", p, err)
		}
		if i > 0 && id <= prev {
			t.Errorf("Compose(%+v) = %d is not above the previous id %d: ids must sort by (tick, node, seq)", p, id, prev)
		}
		prev = id
	}
}

func TestLayout_TickOfAndTimeOf(t *testing.T) {
	l := tLayout(41, 10, 12)
	if k, err := l.TickOf(tEpoch.Add(1234*time.Millisecond + 700*time.Microsecond)); err != nil || k != 1234 {
		t.Errorf("TickOf(epoch + 1234.7 ms) = %d, %v; want tick 1234", k, err)
	}
	id, _ := l.Compose(1234, 9, 9)
	if got := l.TimeOf(id); !got.Equal(tEpoch.Add(1234 * time.Millisecond)) {
		t.Errorf("TimeOf(id at tick 1234) = %v, want epoch + 1234 ms", got)
	}
	if _, err := l.TickOf(tEpoch.Add(-time.Millisecond)); !errors.Is(err, ErrClockBackwards) {
		t.Errorf("TickOf(before epoch) = %v, want ErrClockBackwards", err)
	}
	short := tLayout(8, 2, 2) // 256 ms of lifetime
	if _, err := short.TickOf(tEpoch.Add(300 * time.Millisecond)); !errors.Is(err, ErrOverflow) {
		t.Errorf("8-bit layout, TickOf(epoch + 300 ms) = %v, want ErrOverflow (lifetime is 256 ms)", err)
	}
}

// ---------------------------------------------------------------------------
// Generator
// ---------------------------------------------------------------------------

func TestGenerator_ValidatesConfig(t *testing.T) {
	c := tNewClock(tStart)
	l := tLayout(41, 4, 8)
	cases := []struct {
		name string
		cfg  Config
		want error
	}{
		{"node does not fit", Config{Layout: l, Node: 16, Clock: c}, ErrBadConfig},
		{"nil clock", Config{Layout: l, Node: 1}, ErrBadConfig},
		{"bad layout", Config{Layout: tLayout(50, 10, 10), Node: 1, Clock: c}, ErrBadLayout},
		{"negative MaxBackwards", Config{Layout: l, Clock: c, MaxBackwards: -time.Millisecond}, ErrBadConfig},
		{"negative MaxAhead", Config{Layout: l, Clock: c, MaxAhead: -time.Millisecond}, ErrBadConfig},
		{"limit not above floor", Config{Layout: l, Clock: c, Floor: 100, Limit: 100}, ErrBadConfig},
	}
	for _, tc := range cases {
		if _, err := NewGenerator(tc.cfg); !errors.Is(err, tc.want) {
			t.Errorf("%s: NewGenerator = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := NewGenerator(Config{Layout: l, Node: 15, Clock: c, Floor: 5, Limit: 6}); err != nil {
		t.Errorf("valid config: NewGenerator = %v, want nil", err)
	}
}

func TestGenerator_IDsCarryTheNodeAndTheClock(t *testing.T) {
	l := DefaultLayout()
	l.Epoch = tEpoch
	c := tNewClock(tEpoch.Add(5*time.Second + 300*time.Microsecond))
	g := tGen(t, Config{Layout: l, Node: 7, Clock: c})
	if g.Node() != 7 {
		t.Errorf("Node() = %d, want 7", g.Node())
	}
	a, b := tNext(t, g), tNext(t, g)
	if p := l.Decompose(a); p != (Parts{Tick: 5000, Node: 7, Seq: 0}) {
		t.Errorf("first id decomposes to %+v, want tick 5000 (5 s after the epoch), node 7, seq 0", p)
	}
	if p := l.Decompose(b); p != (Parts{Tick: 5000, Node: 7, Seq: 1}) {
		t.Errorf("second id in the same millisecond decomposes to %+v, want tick 5000, node 7, seq 1", p)
	}
	if g.Last() != 5000 {
		t.Errorf("Last() = %d, want 5000", g.Last())
	}
	if !l.TimeOf(a).Equal(tEpoch.Add(5 * time.Second)) {
		t.Errorf("TimeOf(first id) = %v, want epoch + 5 s: anyone can read the creation time out of an id", l.TimeOf(a))
	}
}

func TestGenerator_StrictlyIncreasingOnOneNode(t *testing.T) {
	l := tLayout(41, 10, 12)
	c := tNewClock(tStart)
	c.step = 10 * time.Microsecond // ≈ 100 calls per millisecond
	g := tGen(t, Config{Layout: l, Node: 3, Clock: c})
	var prev ID
	ticks := map[uint64]bool{}
	for i := 0; i < 50000; i++ {
		id := tNext(t, g)
		if i > 0 && id <= prev {
			t.Fatalf("id %d = %d is not above the previous %d; one node's ids must be strictly increasing", i, id, prev)
		}
		p := l.Decompose(id)
		if p.Node != 3 {
			t.Fatalf("id %d has node %d, want 3", i, p.Node)
		}
		if !ticks[p.Tick] && p.Seq != 0 {
			t.Fatalf("first id of tick %d has seq %d; the sequence restarts at 0 in a new tick", p.Tick, p.Seq)
		}
		ticks[p.Tick] = true
		prev = id
	}
	if len(ticks) < 400 {
		t.Errorf("50,000 ids at ≈ 100 per ms used %d ticks, want ≈ 500", len(ticks))
	}
}

func TestGenerator_SequenceOverflowWaitsForTheNextTick(t *testing.T) {
	// Default layout: 4,096 ids fit in one tick; the 4,097th must not wrap.
	l := tLayout(41, 10, 12)
	c := tNewClock(tStart) // time moves only when the generator sleeps
	g := tGen(t, Config{Layout: l, Node: 1, Clock: c})
	k := tTick(t, l, tStart)
	for i := 0; i < 4096; i++ {
		if p := l.Decompose(tNext(t, g)); p.Tick != k || p.Seq != uint64(i) {
			t.Fatalf("id %d = %+v, want tick %d seq %d", i, p, k, i)
		}
	}
	p := l.Decompose(tNext(t, g))
	if p.Tick != k+1 || p.Seq != 0 {
		t.Fatalf("id 4,097 = %+v, want tick %d seq 0: a wrapped sequence would repeat id (tick %d, seq 0)", p, k+1, k)
	}
	sleeps, slept := c.Stats()
	if sleeps != 1 || slept > time.Millisecond {
		t.Errorf("overflow: %d sleeps totalling %v, want one sleep of at most one tick", sleeps, slept)
	}

	// Small sequence: 4 per tick, so 40 ids span 10 ticks and need 9 waits.
	c2 := tNewClock(tStart)
	g2 := tGen(t, Config{Layout: tLayout(41, 10, 2), Node: 1, Clock: c2})
	seen := map[ID]bool{}
	for i := 0; i < 40; i++ {
		id := tNext(t, g2)
		if seen[id] {
			t.Fatalf("duplicate id %d after %d ids with 4 per tick", id, i)
		}
		seen[id] = true
	}
	if sleeps, _ := c2.Stats(); sleeps != 9 {
		t.Errorf("40 ids at 4 per tick slept %d times, want 9 (one per tick boundary)", sleeps)
	}
}

func TestGenerator_ConcurrentCallersNeverDuplicate(t *testing.T) {
	const goroutines, each = 16, 4000
	l := tLayout(41, 10, 4) // 16 per tick: overflow is constant, under contention
	c := tNewClock(tStart)
	c.step = time.Microsecond
	g := tGen(t, Config{Layout: l, Node: 9, Clock: c, MaxBackwards: 5 * time.Millisecond})
	var all tCollect
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids := make([]ID, 0, each)
			for i := 0; i < each; i++ {
				id, err := g.Next()
				if err != nil {
					errs <- err
					return
				}
				if len(ids) > 0 && id <= ids[len(ids)-1] {
					errs <- fmt.Errorf("a goroutine saw %d after %d; one node's ids must increase for every caller", id, ids[len(ids)-1])
					return
				}
				ids = append(ids, id)
			}
			all.add(ids...)
		}()
	}
	tWait(t, &wg, 20*time.Second, "16 goroutines × 4,000 ids")
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if all.dups != 0 || len(all.seen) != goroutines*each {
		t.Fatalf("%d duplicates, %d distinct of %d ids: concurrent Next must be serialised (run with -race)", all.dups, len(all.seen), goroutines*each)
	}
}

func TestGenerator_DistinctNodesNeverCollide(t *testing.T) {
	l := tLayout(41, 10, 6)
	c := tNewClock(tStart) // all four nodes read the same instant
	var all tCollect
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for node := uint64(0); node < 4; node++ {
		g := tGen(t, Config{Layout: l, Node: node, Clock: c})
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ids := make([]ID, 0, 1000)
				for i := 0; i < 1000; i++ {
					id, err := g.Next()
					if err != nil {
						errs <- err
						return
					}
					if p := l.Decompose(id); p.Node != g.Node() {
						errs <- fmt.Errorf("generator %d minted an id with node %d", g.Node(), p.Node)
						return
					}
					ids = append(ids, id)
				}
				all.add(ids...)
			}()
		}
	}
	tWait(t, &wg, 20*time.Second, "4 nodes × 4 goroutines")
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if all.dups != 0 || len(all.seen) != 16000 {
		t.Fatalf("%d duplicates from 4 nodes × 4 goroutines on one clock: one node's callers must be serialised, and the node field keeps nodes apart", all.dups)
	}
}

func TestGenerator_SmallBackwardStepIsWaitedOut(t *testing.T) {
	l := tLayout(41, 10, 12)
	c := tNewClock(tStart)
	g := tGen(t, Config{Layout: l, Node: 2, Clock: c, MaxBackwards: 10 * time.Millisecond})
	k := tTick(t, l, tStart)
	tNext(t, g)
	prev := tNext(t, g)
	c.Add(-3 * time.Millisecond) // an NTP step of 3 ms
	id := tNext(t, g)
	if id <= prev {
		t.Fatalf("after a 3 ms step back: id %d is not above %d", id, prev)
	}
	if p := l.Decompose(id); p.Tick != k || p.Seq != 2 {
		t.Errorf("after waiting out the step: %+v, want tick %d seq 2 (carry on where it left off)", p, k)
	}
	if sleeps, slept := c.Stats(); sleeps == 0 || slept < 3*time.Millisecond || slept > 4*time.Millisecond {
		t.Errorf("slept %v in %d sleeps, want ≈ 3 ms: a small regression is waited out", slept, sleeps)
	}
}

func TestGenerator_LargeBackwardStepIsRefusedWithoutAnID(t *testing.T) {
	l := tLayout(41, 10, 12)
	c := tNewClock(tStart)
	g := tGen(t, Config{Layout: l, Node: 2, Clock: c, MaxBackwards: 10 * time.Millisecond})
	prev := tNext(t, g)
	c.Add(-2 * time.Second)
	for i := 0; i < 3; i++ {
		id, err := g.Next()
		if !errors.Is(err, ErrClockBackwards) || id != 0 {
			t.Fatalf("2 s behind with MaxBackwards 10 ms: Next = %d, %v; want 0, ErrClockBackwards (never guess an id)", id, err)
		}
	}
	if sleeps, _ := c.Stats(); sleeps != 0 {
		t.Errorf("slept %d times before refusing; a regression beyond the bound is refused at once", sleeps)
	}
	c.Add(2 * time.Second)
	if id := tNext(t, g); id <= prev {
		t.Errorf("after the clock recovered: id %d is not above %d (state must be unchanged by the refusals)", id, prev)
	}
}

func TestGenerator_FrozenClockIsAnErrorNotAHang(t *testing.T) {
	l := tLayout(41, 10, 2)
	c := tNewClock(tStart)
	c.frozen = true // Sleep returns but time never moves
	g := tGen(t, Config{Layout: l, Clock: c})
	for i := 0; i < 4; i++ {
		tNext(t, g)
	}
	var err error
	tWithin(t, 2*time.Second, "Next on a frozen clock", func() { _, err = g.Next() })
	if !errors.Is(err, ErrClockStalled) {
		t.Fatalf("5th id with 4 per tick on a frozen clock: err = %v, want ErrClockStalled", err)
	}
	if _, slept := c.Stats(); slept > 3*time.Millisecond {
		t.Errorf("requested %v of sleep on a frozen clock, want at most MaxBackwards + MaxAhead + 2 ticks (2 ms here)", slept)
	}
}

func TestGenerator_RunAheadBorrowsTicksWithinTheBound(t *testing.T) {
	l := tLayout(41, 10, 2) // 4 per tick
	c := tNewClock(tStart)
	g := tGen(t, Config{Layout: l, Clock: c, MaxAhead: 5 * time.Millisecond})
	k := tTick(t, l, tStart)
	var prev ID
	for i := 0; i < 24; i++ { // ticks k .. k+5, no sleeping
		id := tNext(t, g)
		if i > 0 && id <= prev {
			t.Fatalf("id %d not increasing", i)
		}
		prev = id
	}
	if sleeps, _ := c.Stats(); sleeps != 0 {
		t.Errorf("24 ids with MaxAhead 5 ms slept %d times, want 0 (borrow ticks k..k+5 instead)", sleeps)
	}
	if g.Last() != k+5 {
		t.Errorf("Last() = %d, want %d (5 ticks ahead of the clock)", g.Last(), k+5)
	}
	id := tNext(t, g)
	if p := l.Decompose(id); p.Tick != k+6 {
		t.Errorf("25th id at tick %d, want %d", p.Tick, k+6)
	}
	if sleeps, slept := c.Stats(); sleeps != 1 || slept != time.Millisecond {
		t.Errorf("25th id: %d sleeps, %v; want one 1 ms sleep (until the clock is within 5 ticks again)", sleeps, slept)
	}

	// A small step back is borrowed too: no sleep, carry on from the last tick.
	c2 := tNewClock(tStart)
	g2 := tGen(t, Config{Layout: l, Clock: c2, MaxAhead: 5 * time.Millisecond})
	a := tNext(t, g2)
	c2.Add(-3 * time.Millisecond)
	b := tNext(t, g2)
	if b <= a || l.Decompose(b).Tick != k {
		t.Errorf("3 ms step back with MaxAhead 5 ms: %+v after %+v, want the same tick, next seq", l.Decompose(b), l.Decompose(a))
	}
	if sleeps, _ := c2.Stats(); sleeps != 0 {
		t.Errorf("borrowing on a small regression slept %d times, want 0", sleeps)
	}
}

func TestGenerator_FloorIsNeverReusedAndLimitIsNeverPassed(t *testing.T) {
	l := tLayout(41, 10, 2)
	k := tTick(t, l, tStart)

	// A floor slightly ahead of the clock: wait, then start above it.
	c := tNewClock(tStart)
	g := tGen(t, Config{Layout: l, Clock: c, MaxBackwards: 10 * time.Millisecond, Floor: k + 5})
	if g.Last() != k+5 {
		t.Errorf("Last() before any id = %d, want the floor %d", g.Last(), k+5)
	}
	if p := l.Decompose(tNext(t, g)); p.Tick != k+6 || p.Seq != 0 {
		t.Errorf("first id with Floor %d = %+v, want tick %d seq 0 (ticks <= Floor belong to the previous holder)", k+5, p, k+6)
	}
	if _, slept := c.Stats(); slept != 6*time.Millisecond {
		t.Errorf("slept %v, want 6 ms (until tick floor+1)", slept)
	}

	// A floor far ahead: refuse.
	g2 := tGen(t, Config{Layout: l, Clock: tNewClock(tStart), MaxBackwards: 10 * time.Millisecond, Floor: k + 1000})
	if _, err := g2.Next(); !errors.Is(err, ErrClockBackwards) {
		t.Errorf("Floor 1 s ahead, MaxBackwards 10 ms: err = %v, want ErrClockBackwards", err)
	}

	// A limit: ticks k..k+2 hold 12 ids, the 13th is refused.
	c3 := tNewClock(tStart)
	g3 := tGen(t, Config{Layout: l, Clock: c3, Limit: k + 2})
	for i := 0; i < 12; i++ {
		tNext(t, g3)
	}
	if _, err := g3.Next(); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("13th id with Limit = k+2 and 4 per tick: err = %v, want ErrLeaseExpired", err)
	}
	g3.Extend(k + 10)
	if p := l.Decompose(tNext(t, g3)); p.Tick != k+3 {
		t.Errorf("after Extend: id at tick %d, want %d", p.Tick, k+3)
	}
	g3.Extend(k + 1) // never lowers
	c3.Add(5 * time.Millisecond)
	tNext(t, g3)
	c3.Add(time.Second)
	if _, err := g3.Next(); !errors.Is(err, ErrLeaseExpired) {
		t.Errorf("clock 1 s past the limit: err = %v, want ErrLeaseExpired", err)
	}
	g4 := tGen(t, Config{Layout: l, Clock: tNewClock(tStart)})
	g4.Extend(k + 1) // no limit stays no limit
	for i := 0; i < 12; i++ {
		tNext(t, g4)
	}
}

func TestGenerator_LifetimeOverIsAnError(t *testing.T) {
	l := tLayout(8, 2, 2) // 256 ms
	c := tNewClock(tEpoch.Add(100 * time.Millisecond))
	g := tGen(t, Config{Layout: l, Clock: c})
	tNext(t, g)
	c.Set(tEpoch.Add(300 * time.Millisecond))
	if _, err := g.Next(); !errors.Is(err, ErrOverflow) {
		t.Errorf("past the layout's lifetime: err = %v, want ErrOverflow (a wrapped time field restarts at the epoch)", err)
	}
	c.Set(tEpoch.Add(-time.Second))
	g2 := tGen(t, Config{Layout: l, Clock: c, MaxBackwards: time.Hour})
	if _, err := g2.Next(); !errors.Is(err, ErrClockBackwards) {
		t.Errorf("clock before the epoch: err = %v, want ErrClockBackwards", err)
	}
}

func TestGenerator_SystemClockSmoke(t *testing.T) {
	l := DefaultLayout()
	g := tGen(t, Config{Layout: l, Node: 1, Clock: SystemClock{}, MaxBackwards: 10 * time.Millisecond})
	var prev ID
	var err error
	tWithin(t, 10*time.Second, "20,000 ids on the system clock", func() {
		for i := 0; i < 20000; i++ {
			var id ID
			id, err = g.Next()
			if err != nil {
				return
			}
			if i > 0 && id <= prev {
				err = fmt.Errorf("id %d = %d not above %d", i, id, prev)
				return
			}
			prev = id
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(l.TimeOf(prev)); d < -time.Second || d > 5*time.Second {
		t.Errorf("last id's time is %v away from now; ids should carry the wall-clock time", d)
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func TestRegistry_ValidatesConfig(t *testing.T) {
	c := tNewClock(tStart)
	if _, err := NewRegistry(0, time.Second, c); !errors.Is(err, ErrBadConfig) {
		t.Errorf("0 nodes: %v, want ErrBadConfig", err)
	}
	if _, err := NewRegistry(8, 0, c); !errors.Is(err, ErrBadConfig) {
		t.Errorf("ttl 0: %v, want ErrBadConfig", err)
	}
	if _, err := NewRegistry(8, time.Second, nil); !errors.Is(err, ErrBadConfig) {
		t.Errorf("nil clock: %v, want ErrBadConfig", err)
	}
	r := tRegistry(t, 8, 3*time.Second, c)
	if r.Nodes() != 8 || r.TTL() != 3*time.Second {
		t.Errorf("Nodes() = %d, TTL() = %v; want 8 and 3s", r.Nodes(), r.TTL())
	}
}

func TestRegistry_AcquireHandsOutDistinctNodesAndIncreasingTokens(t *testing.T) {
	c := tNewClock(tStart)
	r := tRegistry(t, 64, time.Minute, c)
	leases := make(chan Lease, 64)
	errs := make(chan error, 64)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := r.Acquire(fmt.Sprintf("pod-%d", i), 0)
			if err != nil {
				errs <- err
				return
			}
			leases <- l
		}()
	}
	tWait(t, &wg, 5*time.Second, "64 concurrent Acquire calls")
	close(leases)
	close(errs)
	for err := range errs {
		t.Fatalf("Acquire with free nodes left: %v", err)
	}
	nodes, tokens := map[uint64]bool{}, map[uint64]bool{}
	for l := range leases {
		if nodes[l.Node] || tokens[l.Token] {
			t.Fatalf("node %d or token %d handed out twice: two pods would mint the same ids", l.Node, l.Token)
		}
		nodes[l.Node], tokens[l.Token] = true, true
		if l.Node >= 64 || l.Token == 0 || !l.Expires.Equal(tStart.Add(time.Minute)) {
			t.Errorf("lease %+v: want node < 64, token > 0, expiry = now + TTL", l)
		}
	}
	if len(nodes) != 64 {
		t.Fatalf("%d distinct nodes, want 64", len(nodes))
	}
	if _, err := r.Acquire("pod-65", 0); !errors.Is(err, ErrNoFreeNode) {
		t.Errorf("65th Acquire on 64 nodes: %v, want ErrNoFreeNode", err)
	}
}

func TestRegistry_ExpiredLeaseIsReassignedWithItsHighWaterAsTheFloor(t *testing.T) {
	c := tNewClock(tStart)
	r := tRegistry(t, 2, 10*time.Second, c)
	a, err := r.Acquire("a", 500)
	if err != nil || a.Node != 0 || a.Floor != 0 {
		t.Fatalf("first Acquire = %+v, %v; want node 0, floor 0", a, err)
	}
	c.Add(10 * time.Second) // exactly at expiry: expired
	b, err := r.Acquire("b", 900)
	if err != nil {
		t.Fatalf("Acquire after a's lease expired: %v", err)
	}
	if b.Node != 0 || b.Token <= a.Token || b.Floor != 500 || b.Holder != "b" {
		t.Errorf("takeover lease = %+v; want node 0 (lowest free), a larger token than %d, floor 500 (a's high-water)", b, a.Token)
	}
	if _, err := r.Renew(a, 600); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("old holder's Renew after takeover: %v, want ErrLeaseLost", err)
	}
	if err := r.Release(a, 0); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("old holder's Release after takeover: %v, want ErrLeaseLost (it must not free b's node)", err)
	}
	if cl, err := r.Acquire("c", 0); err != nil || cl.Node != 1 || cl.Floor != 0 {
		t.Errorf("third Acquire = %+v, %v; want node 1 with floor 0", cl, err)
	}
}

func TestRegistry_RenewExtendsAndRaisesTheHighWater(t *testing.T) {
	c := tNewClock(tStart)
	r := tRegistry(t, 1, 10*time.Second, c)
	a, _ := r.Acquire("a", 100)
	c.Add(5 * time.Second)
	a2, err := r.Renew(a, 200)
	if err != nil || !a2.Expires.Equal(tStart.Add(15*time.Second)) || a2.Token != a.Token {
		t.Fatalf("Renew at +5 s = %+v, %v; want expiry +15 s, same token", a2, err)
	}
	if _, err := r.Renew(a2, 50); err != nil { // a lower limit never lowers the high-water
		t.Fatalf("Renew: %v", err)
	}
	stale := a2
	stale.Token++
	if _, err := r.Renew(stale, 300); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("Renew with a token that was never granted: %v, want ErrLeaseLost", err)
	}
	c.Add(20 * time.Second)
	if _, err := r.Renew(a2, 300); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("Renew after expiry (nobody took the node): %v, want ErrLeaseLost: an expired lease is gone", err)
	}
	b, err := r.Acquire("b", 0)
	if err != nil || b.Floor != 200 {
		t.Errorf("next holder's floor = %d (%v), want 200: the highest limit any renewal promised", b.Floor, err)
	}
}

func TestRegistry_ReleaseLowersTheFloorToWhatWasUsed(t *testing.T) {
	c := tNewClock(tStart)
	r := tRegistry(t, 1, 10*time.Second, c)
	a, _ := r.Acquire("a", 900)
	if err := r.Release(a, 120); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := r.Release(a, 120); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("second Release: %v, want ErrLeaseLost", err)
	}
	b, err := r.Acquire("b", 0)
	if err != nil || b.Node != 0 || b.Floor != 120 {
		t.Errorf("Acquire after release = %+v, %v; want node 0 at once, floor 120 (what a used, not its limit 900)", b, err)
	}
}

// ---------------------------------------------------------------------------
// Worker
// ---------------------------------------------------------------------------

func TestWorker_ValidatesConfig(t *testing.T) {
	c := tNewClock(tStart)
	r := tRegistry(t, 8, 10*time.Second, c)
	l := tLayout(41, 10, 12)
	cases := []WorkerConfig{
		{Layout: l, Clock: c, Margin: -time.Second},
		{Layout: l, Clock: c, Margin: 10 * time.Second},
		{Layout: l, Margin: time.Second},
		{Layout: tLayout(41, 2, 12), Clock: c, Margin: time.Second}, // 4 node ids < 8 in the registry
	}
	for _, cfg := range cases {
		if _, err := StartWorker(r, "w", cfg); !errors.Is(err, ErrBadConfig) {
			t.Errorf("StartWorker(margin %v, clock %v, %d node bits) = %v, want ErrBadConfig", cfg.Margin, cfg.Clock != nil, cfg.Layout.NodeBits, err)
		}
	}
	if _, err := StartWorker(nil, "w", WorkerConfig{Layout: l, Clock: c}); !errors.Is(err, ErrBadConfig) {
		t.Errorf("nil registry: %v, want ErrBadConfig", err)
	}
}

func TestWorker_StopsMintingBeforeItsLeaseExpires(t *testing.T) {
	c := tNewClock(tStart)
	r := tRegistry(t, 4, 10*time.Second, c)
	w := tWorker(t, r, "a", WorkerConfig{Layout: tLayout(41, 10, 12), Clock: c, Margin: time.Second})
	if w.Node() != 0 || w.Lease().Holder != "a" {
		t.Errorf("worker on node %d, lease %+v; want node 0 held by a", w.Node(), w.Lease())
	}
	tNext(t, w)
	c.Add(8900 * time.Millisecond)
	tNext(t, w)
	c.Add(200 * time.Millisecond) // +9.1 s: past TTL − Margin
	if _, err := w.Next(); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("at +9.1 s with TTL 10 s and margin 1 s: err = %v, want ErrLeaseExpired (stop before the registry could hand the node to someone else)", err)
	}

	// Renewing in time moves the limit: renew at +5 s, mint until +14 s.
	c2 := tNewClock(tStart)
	r2 := tRegistry(t, 4, 10*time.Second, c2)
	w2 := tWorker(t, r2, "a", WorkerConfig{Layout: tLayout(41, 10, 12), Clock: c2, Margin: time.Second})
	c2.Add(5 * time.Second)
	if err := w2.Renew(); err != nil {
		t.Fatalf("Renew at +5 s: %v", err)
	}
	if !w2.Lease().Expires.Equal(tStart.Add(15 * time.Second)) {
		t.Errorf("lease after renew expires %v, want +15 s", w2.Lease().Expires.Sub(tStart))
	}
	c2.Add(4100 * time.Millisecond) // +9.1 s
	tNext(t, w2)
	c2.Add(5 * time.Second) // +14.1 s
	if _, err := w2.Next(); !errors.Is(err, ErrLeaseExpired) {
		t.Errorf("at +14.1 s after a renewal at +5 s: err = %v, want ErrLeaseExpired", err)
	}
}

func TestWorker_PausedHolderAndNewHolderNeverCollide(t *testing.T) {
	l := tLayout(41, 10, 12)
	base := tNewClock(tStart)
	r := tRegistry(t, 1, 10*time.Second, base)
	a := tWorker(t, r, "a", WorkerConfig{Layout: l, Clock: base, Margin: time.Second})
	var all tCollect
	var maxA uint64
	for base.Peek().Before(tStart.Add(8900 * time.Millisecond)) {
		id := tNext(t, a)
		all.add(id)
		maxA = l.Decompose(id).Tick
		base.Add(10 * time.Millisecond)
	}
	// a stops renewing (a 6.5 s GC pause, a frozen VM...). Its lease expires at +10 s.
	base.Set(tStart.Add(15500 * time.Millisecond))
	// b's machine is 7 s behind: it reads +8.5 s, a tick a has already used.
	bClock := tSkewed{base: base, off: -7 * time.Second}
	b := tWorker(t, r, "b", WorkerConfig{Layout: l, Clock: bClock, Margin: time.Second, MaxBackwards: time.Second})
	if b.Node() != 0 || b.Lease().Floor < maxA {
		t.Fatalf("b got node %d with floor %d; want node 0 and a floor at or above a's last tick %d", b.Node(), b.Lease().Floor, maxA)
	}
	minB := uint64(math.MaxUint64)
	for i := 0; i < 500; i++ {
		id := tNext(t, b)
		all.add(id)
		if k := l.Decompose(id).Tick; k < minB {
			minB = k
		}
	}
	if minB <= b.Lease().Floor {
		t.Errorf("b minted at tick %d, at or below its floor %d", minB, b.Lease().Floor)
	}
	// a wakes up.
	if id, err := a.Next(); !errors.Is(err, ErrLeaseExpired) {
		t.Errorf("woken old holder: Next = %d, %v; want ErrLeaseExpired", id, err)
	}
	if err := a.Renew(); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("woken old holder: Renew = %v, want ErrLeaseLost", err)
	}
	if _, err := a.Next(); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("after a failed Renew: Next = %v, want ErrLeaseLost (the worker stops for good)", err)
	}
	if all.dups != 0 {
		t.Fatalf("%d duplicate ids between the paused holder and its replacement", all.dups)
	}

	// Same story, but b may wait out only 10 ms: it refuses instead of colliding.
	base2 := tNewClock(tStart)
	r2 := tRegistry(t, 1, 10*time.Second, base2)
	a2 := tWorker(t, r2, "a", WorkerConfig{Layout: l, Clock: base2, Margin: time.Second})
	base2.Add(8500 * time.Millisecond)
	tNext(t, a2)
	base2.Set(tStart.Add(15500 * time.Millisecond))
	b2 := tWorker(t, r2, "b", WorkerConfig{Layout: l, Clock: tSkewed{base: base2, off: -7 * time.Second}, Margin: time.Second, MaxBackwards: 10 * time.Millisecond})
	if _, err := b2.Next(); !errors.Is(err, ErrClockBackwards) {
		t.Errorf("new holder 0.5 s behind its floor with MaxBackwards 10 ms: err = %v, want ErrClockBackwards", err)
	}
}

func TestWorker_StopHandsTheNodeOverWithoutWaitingForTheOldLimit(t *testing.T) {
	l := tLayout(41, 10, 12)
	c := tNewClock(tStart)
	r := tRegistry(t, 1, 10*time.Second, c)
	a := tWorker(t, r, "a", WorkerConfig{Layout: l, Clock: c, Margin: time.Second})
	var lastA ID
	for i := 0; i < 10; i++ {
		lastA = tNext(t, a)
	}
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := a.Next(); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("Next after Stop: %v, want ErrLeaseLost", err)
	}
	if err := a.Stop(); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("second Stop: %v, want ErrLeaseLost", err)
	}
	b := tWorker(t, r, "b", WorkerConfig{Layout: l, Clock: c, Margin: time.Second, MaxBackwards: 10 * time.Millisecond})
	if b.Node() != 0 || b.Lease().Floor != l.Decompose(lastA).Tick {
		t.Errorf("b: node %d floor %d; want node 0 with a's last used tick %d as floor (not a's limit, 9 s ahead)", b.Node(), b.Lease().Floor, l.Decompose(lastA).Tick)
	}
	id := tNext(t, b)
	if id <= lastA {
		t.Errorf("b's first id %d is not above a's last %d", id, lastA)
	}
	if _, slept := c.Stats(); slept > time.Millisecond {
		t.Errorf("b slept %v before its first id, want at most one tick", slept)
	}
}

func TestWorker_ChurningWorkersNeverDuplicate(t *testing.T) {
	l := tLayout(41, 10, 4)
	c := tNewClock(tStart)
	c.step = time.Microsecond
	r := tRegistry(t, 8, 50*time.Millisecond, c)
	cfg := WorkerConfig{Layout: l, Clock: c, Margin: 10 * time.Millisecond, MaxBackwards: 5 * time.Millisecond}
	var all tCollect
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 20; round++ {
				var w *Worker
				var err error
				for try := 0; try < 2000; try++ {
					if w, err = StartWorker(r, fmt.Sprintf("g%d-r%d", g, round), cfg); !errors.Is(err, ErrNoFreeNode) {
						break
					}
					c.Sleep(time.Millisecond)
				}
				if err != nil {
					errs <- fmt.Errorf("StartWorker: %v", err)
					return
				}
				ids := make([]ID, 0, 200)
				for i := 0; i < 200; i++ {
					id, err := w.Next()
					if errors.Is(err, ErrLeaseExpired) {
						if w.Renew() != nil {
							break
						}
						continue
					}
					if err != nil {
						break
					}
					ids = append(ids, id)
				}
				all.add(ids...)
				if round%4 != 3 { // every fourth round "crashes" without releasing
					_ = w.Stop()
				}
			}
		}()
	}
	tWait(t, &wg, 30*time.Second, "6 goroutines churning workers")
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if all.dups != 0 {
		t.Fatalf("%d duplicate ids across %d minted by churning workers", all.dups, len(all.seen))
	}
	if len(all.seen) < 6*20*100 {
		t.Errorf("only %d ids minted; workers should mint most of their 200 per round", len(all.seen))
	}
	ids := make([]ID, 0, len(all.seen))
	for id := range all.seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if l.Decompose(ids[0]).Tick < tTick(t, l, tStart) {
		t.Errorf("an id from before the test started: %+v", l.Decompose(ids[0]))
	}
}
