package distlimit

import (
	"context"
	"errors"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

var tT0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

var tErrBoom = errors.New("store unreachable")

// tClock is a fake clock that only moves when a test moves it.
type tClock struct {
	mu  sync.Mutex
	now time.Time
}

func tNewClock() *tClock { return &tClock{now: tT0} }

func (c *tClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *tClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *tClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// tOffsetClock is a gateway whose clock is off by a fixed amount.
type tOffsetClock struct {
	base *tClock
	off  time.Duration
}

func (c tOffsetClock) Now() time.Time { return c.base.Now().Add(c.off) }

// tWait waits for wg, failing the test instead of hanging.
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: still running after %v (deadlock, or a store call with no timeout?)", what, d)
	}
}

func tLimiter(t *testing.T, cfg LimiterConfig) *Limiter {
	t.Helper()
	l, err := NewLimiter(cfg)
	if err != nil || l == nil {
		t.Fatalf("NewLimiter(%+v) = %v, %v", cfg, l, err)
	}
	return l
}

func tGuard(t *testing.T, cfg GuardConfig) *Guard {
	t.Helper()
	g, err := NewGuard(cfg)
	if err != nil || g == nil {
		t.Fatalf("NewGuard(%+v) = %v, %v", cfg, g, err)
	}
	return g
}

func tLease(t *testing.T, cfg LeaseConfig) *LeaseClient {
	t.Helper()
	c, err := NewLeaseClient(cfg)
	if err != nil || c == nil {
		t.Fatalf("NewLeaseClient(%+v) = %v, %v", cfg, c, err)
	}
	return c
}

func tDecide(t *testing.T, d Decider, key string, limit Limit, n int) Decision {
	t.Helper()
	dec, err := d.Decide(context.Background(), key, limit, n)
	if err != nil {
		t.Fatalf("Decide(%q, %+v, %d): unexpected error %v", key, limit, n, err)
	}
	return dec
}

// tScriptedStore loses its first `lose` compare-and-swaps (as if another
// gateway had just written the key) and can fail CompareAndSwap with casErr.
type tScriptedStore struct {
	mu     sync.Mutex
	v      int64
	lose   int
	casErr error
	loads  int
	cas    int
}

func (s *tScriptedStore) Load(context.Context, string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	return s.v, nil
}

func (s *tScriptedStore) CompareAndSwap(_ context.Context, _ string, old, new int64, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cas++
	if s.casErr != nil {
		return false, s.casErr
	}
	if s.lose > 0 || s.v != old {
		s.lose--
		return false, nil
	}
	s.v = new
	return true, nil
}

// tBlockingStore never answers until the caller's context is done (or the
// test releases it), like a store behind a dead network path.
type tBlockingStore struct {
	release chan struct{}
	once    sync.Once
}

func tNewBlockingStore(t *testing.T) *tBlockingStore {
	s := &tBlockingStore{release: make(chan struct{})}
	t.Cleanup(func() { s.once.Do(func() { close(s.release) }) })
	return s
}

func (s *tBlockingStore) Load(ctx context.Context, _ string) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.release:
		return 0, tErrBoom
	}
}

func (s *tBlockingStore) CompareAndSwap(ctx context.Context, _ string, _, _ int64, _ time.Duration) (bool, error) {
	_, err := s.Load(ctx, "")
	return false, err
}

// tSimResult summarises one fleet simulation.
type tSimResult struct {
	offered, admitted int
	maxWindow         int // most requests admitted in any window of 1 s
	storeOps          int64
	held              int // tokens still held by the nodes at the end
}

// tSimulate runs `nodes` gateways against one shared Limiter in virtual time:
// Poisson arrivals at offeredPerSec for dur, each sent to a node chosen by
// pick (uniform when nil). batch > 0 puts a LeaseClient with that batch in
// every node; batch <= 0 has every node call the Limiter directly.
// Deterministic for a given seed.
func tSimulate(t testing.TB, nodes, batch int, limit Limit, leaseTTL time.Duration,
	offeredPerSec float64, dur time.Duration, seed uint64, pick func(r *rand.Rand) int) tSimResult {
	t.Helper()
	clk := tNewClock()
	store := NewMemoryStore(clk)
	lim, err := NewLimiter(LimiterConfig{Store: store, Clock: clk})
	if err != nil {
		t.Fatalf("NewLimiter: %v", err)
	}
	deciders := make([]Decider, nodes)
	leases := make([]*LeaseClient, nodes)
	for i := range deciders {
		if batch <= 0 {
			deciders[i] = lim
			continue
		}
		lc, err := NewLeaseClient(LeaseConfig{Pool: lim, Clock: clk, Batch: batch, LeaseTTL: leaseTTL})
		if err != nil {
			t.Fatalf("NewLeaseClient: %v", err)
		}
		deciders[i], leases[i] = lc, lc
	}
	r := rand.New(rand.NewPCG(seed, 7))
	if pick == nil {
		pick = func(r *rand.Rand) int { return r.IntN(nodes) }
	}
	var res tSimResult
	var admittedAt []time.Duration
	at := time.Duration(0)
	for {
		at += time.Duration(r.ExpFloat64() / offeredPerSec * float64(time.Second))
		if at >= dur {
			break
		}
		clk.Set(tT0.Add(at))
		res.offered++
		d, err := deciders[pick(r)].Decide(context.Background(), "tenant-42", limit, 1)
		if err != nil {
			t.Fatalf("simulation: Decide at %v: %v", at, err)
		}
		if d.Allowed {
			res.admitted++
			admittedAt = append(admittedAt, at)
		}
	}
	lo := 0
	for hi := range admittedAt {
		for admittedAt[hi]-admittedAt[lo] >= time.Second {
			lo++
		}
		res.maxWindow = max(res.maxWindow, hi-lo+1)
	}
	res.storeOps = store.Ops()
	for _, lc := range leases {
		if lc != nil {
			res.held += lc.Held()
		}
	}
	return res
}

// ---------- 1. MemoryStore ----------

func TestMemoryStore_CompareAndSwapIsAtomic(t *testing.T) {
	s := NewMemoryStore(tNewClock())
	ctx := context.Background()
	var wins atomic.Int64
	var winner atomic.Int64
	var wg sync.WaitGroup
	for i := 1; i <= 200; i++ {
		wg.Add(1)
		go func(v int64) {
			defer wg.Done()
			ok, err := s.CompareAndSwap(ctx, "k", 0, v, time.Minute)
			if err != nil {
				t.Errorf("CompareAndSwap: %v", err)
			}
			if ok {
				wins.Add(1)
				winner.Store(v)
			}
		}(int64(i))
	}
	tWait(t, &wg, 5*time.Second, "200 concurrent CompareAndSwap calls")
	if got := wins.Load(); got != 1 {
		t.Fatalf("%d of 200 CompareAndSwap(0 → v) calls succeeded, want exactly 1: two gateways that both "+
			"'won' the same old value would both admit a request against one token", got)
	}
	if v, _ := s.Load(ctx, "k"); v != winner.Load() {
		t.Fatalf("Load = %d, want the winner's value %d", v, winner.Load())
	}
	if ok, _ := s.CompareAndSwap(ctx, "k", 12345, 7, time.Minute); ok {
		t.Fatal("CompareAndSwap with the wrong old value succeeded")
	}
	if ok, _ := s.CompareAndSwap(ctx, "k", winner.Load(), 0, 0); !ok {
		t.Fatal("CompareAndSwap(current → 0) should delete the key and report true")
	}
	if v, _ := s.Load(ctx, "k"); v != 0 || s.Len() != 0 {
		t.Fatalf("after delete: Load = %d, Len = %d, want 0 and 0", v, s.Len())
	}
}

func TestMemoryStore_TTLRunsOnTheStoreClock(t *testing.T) {
	clk := tNewClock()
	s := NewMemoryStore(clk)
	ctx := context.Background()
	if ok, err := s.CompareAndSwap(ctx, "k", 0, 42, time.Second); !ok || err != nil {
		t.Fatalf("CompareAndSwap on an absent key = %v, %v; want true, nil", ok, err)
	}
	clk.Advance(999 * time.Millisecond)
	if v, _ := s.Load(ctx, "k"); v != 42 || s.Len() != 1 {
		t.Fatalf("1 ms before expiry: Load = %d, Len = %d; want 42, 1", v, s.Len())
	}
	clk.Advance(time.Millisecond)
	if v, _ := s.Load(ctx, "k"); v != 0 || s.Len() != 0 {
		t.Fatalf("at expiry: Load = %d, Len = %d; want 0, 0 (an expired key is absent)", v, s.Len())
	}
	if ok, _ := s.CompareAndSwap(ctx, "k", 42, 43, time.Second); ok {
		t.Fatal("CompareAndSwap(42 → 43) succeeded on an expired key; expired must compare as 0")
	}
	if ok, _ := s.CompareAndSwap(ctx, "k", 0, 43, 0); !ok {
		t.Fatal("CompareAndSwap(0 → 43) failed on an expired key")
	}
	clk.Advance(24 * time.Hour)
	if v, _ := s.Load(ctx, "k"); v != 43 {
		t.Fatalf("ttl 0 should never expire; Load = %d after a day", v)
	}
}

func TestMemoryStore_FailInjectionAndOpCounts(t *testing.T) {
	s := NewMemoryStore(tNewClock())
	ctx := context.Background()
	s.Fail(tErrBoom)
	if _, err := s.Load(ctx, "k"); !errors.Is(err, tErrBoom) {
		t.Fatalf("Load during Fail: err = %v, want %v", err, tErrBoom)
	}
	if _, err := s.CompareAndSwap(ctx, "k", 0, 1, 0); !errors.Is(err, tErrBoom) {
		t.Fatalf("CompareAndSwap during Fail: err = %v, want %v", err, tErrBoom)
	}
	s.Fail(nil)
	if _, err := s.Load(ctx, "k"); err != nil {
		t.Fatalf("Load after Fail(nil): %v", err)
	}
	if got := s.Ops(); got != 3 {
		t.Fatalf("Ops = %d, want 3 (failed calls count: they cost the store a round trip too)", got)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Load(cctx, "k"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load with a cancelled context: err = %v, want context.Canceled", err)
	}
}

// ---------- 2. Limiter (GCRA over the store) ----------

func TestLimiter_BurstThenSustainedRate(t *testing.T) {
	clk := tNewClock()
	lim := tLimiter(t, LimiterConfig{Store: NewMemoryStore(clk), Clock: clk})
	limit := Limit{Rate: 10, Burst: 5} // T = 100 ms
	for i := 0; i < 5; i++ {
		d := tDecide(t, lim, "k", limit, 1)
		if !d.Allowed || d.Remaining != 4-i || d.Source != SourceStore {
			t.Fatalf("request %d of the burst: %+v; want allowed, Remaining %d, Source store", i+1, d, 4-i)
		}
		if want := time.Duration(i+1) * 100 * time.Millisecond; d.ResetAfter != want {
			t.Fatalf("request %d: ResetAfter = %v, want %v (n × T until the bucket is full again)", i+1, d.ResetAfter, want)
		}
	}
	d := tDecide(t, lim, "k", limit, 1)
	if d.Allowed || d.RetryAfter != 100*time.Millisecond || d.Remaining != 0 {
		t.Fatalf("6th request at the same instant: %+v; want denied, RetryAfter 100ms, Remaining 0", d)
	}
	clk.Advance(99 * time.Millisecond)
	if d := tDecide(t, lim, "k", limit, 1); d.Allowed || d.RetryAfter != time.Millisecond {
		t.Fatalf("99 ms later: %+v; want denied with RetryAfter 1ms", d)
	}
	clk.Advance(time.Millisecond)
	if d := tDecide(t, lim, "k", limit, 1); !d.Allowed || d.Remaining != 0 {
		t.Fatalf("100 ms later: %+v; want allowed with Remaining 0 (one token refilled)", d)
	}
	clk.Advance(10 * time.Second)
	allowed := 0
	for i := 0; i < 8; i++ {
		if tDecide(t, lim, "k", limit, 1).Allowed {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("after 10 s idle, %d of 8 instant requests admitted, want the burst of 5 (idle time must "+
			"not accumulate beyond the burst)", allowed)
	}
}

func TestLimiter_CostNChargesNTokens(t *testing.T) {
	clk := tNewClock()
	lim := tLimiter(t, LimiterConfig{Store: NewMemoryStore(clk), Clock: clk})
	limit := Limit{Rate: 10, Burst: 5}
	if d := tDecide(t, lim, "k", limit, 3); !d.Allowed || d.Remaining != 2 || d.ResetAfter != 300*time.Millisecond {
		t.Fatalf("cost 3 on a fresh key: %+v; want allowed, Remaining 2, ResetAfter 300ms", d)
	}
	if d := tDecide(t, lim, "k", limit, 3); d.Allowed || d.Remaining != 2 || d.RetryAfter != 100*time.Millisecond {
		t.Fatalf("cost 3 with 2 left: %+v; want denied, Remaining 2, RetryAfter 100ms", d)
	}
	if d := tDecide(t, lim, "k", limit, 2); !d.Allowed || d.Remaining != 0 {
		t.Fatalf("cost 2 with 2 left: %+v; want allowed, Remaining 0 (a denial must not consume)", d)
	}
	for _, bad := range []struct {
		limit Limit
		n     int
	}{
		{limit, 6}, {limit, 0}, {limit, -1}, {Limit{Rate: 0, Burst: 5}, 1}, {Limit{Rate: 10, Burst: 0}, 1},
	} {
		if _, err := lim.Decide(context.Background(), "k", bad.limit, bad.n); !errors.Is(err, ErrBadArgument) {
			t.Errorf("Decide(%+v, n=%d): err = %v, want ErrBadArgument", bad.limit, bad.n, err)
		}
	}
}

func TestLimiter_DenialsReadButNeverWrite(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk})
	limit := Limit{Rate: 10, Burst: 3}
	for i := 0; i < 3; i++ {
		tDecide(t, lim, "k", limit, 1)
	}
	if got := store.Ops(); got != 6 {
		t.Fatalf("3 admitted requests cost %d store ops, want 6 (one Load and one CompareAndSwap each)", got)
	}
	before, _ := store.Load(context.Background(), "k")
	for i := 0; i < 10; i++ {
		if tDecide(t, lim, "k", limit, 1).Allowed {
			t.Fatal("request admitted beyond the burst")
		}
	}
	if got := store.Ops() - 7; got != 10 {
		t.Fatalf("10 denials cost %d store ops, want 10 (one Load each, no write): a key over its limit "+
			"must not double the store's write load", got)
	}
	if after, _ := store.Load(context.Background(), "k"); after != before {
		t.Fatalf("denials changed the stored TAT from %d to %d", before, after)
	}
}

func TestLimiter_StateExpiresWhenTheBucketIsFull(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk})
	limit := Limit{Rate: 10, Burst: 5}
	for i := 0; i < 1000; i++ {
		tDecide(t, lim, "key-"+strconv.Itoa(i), limit, 1)
	}
	for i := 0; i < 2; i++ {
		tDecide(t, lim, "busy", limit, 1)
	}
	if got := store.Len(); got != 1001 {
		t.Fatalf("Len = %d, want 1001 keys", got)
	}
	clk.Advance(100 * time.Millisecond)
	if got := store.Len(); got != 1 {
		t.Fatalf("100 ms later Len = %d, want 1: a key whose bucket is full again must expire (TTL = "+
			"ResetAfter), or memory grows with every key ever seen", got)
	}
	clk.Advance(100 * time.Millisecond)
	if got := store.Len(); got != 0 {
		t.Fatalf("200 ms later Len = %d, want 0", got)
	}
}

func TestLimiter_StaleStateIsTreatedAsFull(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	// A TAT an hour in the past, stored without a TTL (a store that does not
	// expire keys, or a key written before a restart).
	if ok, _ := store.CompareAndSwap(context.Background(), "k", 0, tT0.Add(-time.Hour).UnixNano(), 0); !ok {
		t.Fatal("setup: CompareAndSwap failed")
	}
	lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk})
	allowed := 0
	for i := 0; i < 50; i++ {
		if tDecide(t, lim, "k", Limit{Rate: 10, Burst: 5}, 1).Allowed {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("%d of 50 instant requests admitted on a key idle for an hour, want 5: a TAT in the past "+
			"must be clamped to now (max(TAT, now)), or idle time becomes an unbounded burst", allowed)
	}
}

func TestLimiter_IsExactAcrossGatewaysUnderConcurrency(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	limit := Limit{Rate: 1, Burst: 100}
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk, MaxRetries: 1000})
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					d, err := lim.Decide(context.Background(), "tenant", limit, 1)
					if err != nil {
						t.Errorf("Decide: %v", err)
						return
					}
					if d.Allowed {
						admitted.Add(1)
					}
				}
			}()
		}
	}
	tWait(t, &wg, 10*time.Second, "4 gateways × 8 goroutines")
	if got := admitted.Load(); got != 100 {
		t.Fatalf("4 gateways admitted %d of 1,600 requests against a burst of 100 at one instant, want "+
			"exactly 100: the read-modify-write must be one compare-and-swap, retried on loss", got)
	}
}

func TestLimiter_RetriesLostSwapsThenGivesUp(t *testing.T) {
	clk := tNewClock()
	limit := Limit{Rate: 10, Burst: 5}
	s := &tScriptedStore{lose: 3}
	lim := tLimiter(t, LimiterConfig{Store: s, Clock: clk, MaxRetries: 5})
	if d := tDecide(t, lim, "k", limit, 1); !d.Allowed {
		t.Fatalf("after 3 lost swaps: %+v, want allowed on the 4th attempt", d)
	}
	if s.cas != 4 || s.loads != 4 {
		t.Fatalf("loads, swaps = %d, %d; want 4, 4 (reload before every retry: the other writer's value "+
			"is the new old)", s.loads, s.cas)
	}
	s2 := &tScriptedStore{lose: 1 << 30}
	lim2 := tLimiter(t, LimiterConfig{Store: s2, Clock: clk, MaxRetries: 2})
	if _, err := lim2.Decide(context.Background(), "k", limit, 1); !errors.Is(err, ErrContended) {
		t.Fatalf("always losing: err = %v, want ErrContended", err)
	}
	if s2.cas != 3 {
		t.Fatalf("MaxRetries 2 made %d attempts, want 3 (one try plus two retries)", s2.cas)
	}
}

func TestLimiter_StoreErrorsAndCancellation(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk})
	limit := Limit{Rate: 10, Burst: 5}
	store.Fail(tErrBoom)
	d, err := lim.Decide(context.Background(), "k", limit, 1)
	if !errors.Is(err, tErrBoom) || d.Allowed {
		t.Fatalf("store down: %+v, %v; want a wrapped store error and no admission (the policy belongs "+
			"to the Guard, not the Limiter)", d, err)
	}
	store.Fail(nil)
	s := &tScriptedStore{casErr: tErrBoom}
	if _, err := tLimiter(t, LimiterConfig{Store: s, Clock: clk}).Decide(context.Background(), "k", limit, 1); !errors.Is(err, tErrBoom) {
		t.Fatalf("CompareAndSwap error: err = %v, want it wrapped", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lim.Decide(ctx, "k", limit, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: err = %v, want context.Canceled", err)
	}
	if _, err := NewLimiter(LimiterConfig{}); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("NewLimiter without a Store: err = %v, want ErrBadArgument", err)
	}
	if _, err := NewLimiter(LimiterConfig{Store: store, MaxRetries: -1}); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("NewLimiter with MaxRetries -1: err = %v, want ErrBadArgument", err)
	}
}

func TestLimiter_RefundGivesTokensBackButNeverOverfills(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk})
	limit := Limit{Rate: 10, Burst: 5}
	ctx := context.Background()
	count := func(tries int) int {
		n := 0
		for i := 0; i < tries; i++ {
			if tDecide(t, lim, "k", limit, 1).Allowed {
				n++
			}
		}
		return n
	}
	if got := count(6); got != 5 {
		t.Fatalf("burst: %d admitted, want 5", got)
	}
	if err := lim.Refund(ctx, "k", limit, 3); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if got := count(6); got != 3 {
		t.Fatalf("after refunding 3: %d admitted, want 3", got)
	}
	if err := lim.Refund(ctx, "k", limit, 100); err != nil {
		t.Fatalf("Refund 100: %v", err)
	}
	if store.Len() != 0 {
		t.Fatalf("refunding more than was taken should leave a full bucket, i.e. no key; Len = %d", store.Len())
	}
	if got := count(8); got != 5 {
		t.Fatalf("after an over-refund: %d admitted, want 5 (a refund may refill, never overfill)", got)
	}
	before := store.Ops()
	if err := lim.Refund(ctx, "absent", limit, 3); err != nil || store.Ops()-before != 1 {
		t.Fatalf("Refund on an absent key: err %v, %d ops; want nil and a single Load", err, store.Ops()-before)
	}
	if err := lim.Refund(ctx, "k", limit, 0); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("Refund 0: err = %v, want ErrBadArgument", err)
	}
}

// ---------- 3. Guard ----------

func TestGuard_PassesThroughAHealthyPrimary(t *testing.T) {
	clk := tNewClock()
	lim := tLimiter(t, LimiterConfig{Store: NewMemoryStore(clk), Clock: clk})
	g := tGuard(t, GuardConfig{Primary: lim, Mode: FailOpen})
	limit := Limit{Rate: 10, Burst: 2}
	for i := 0; i < 2; i++ {
		if d := tDecide(t, g, "k", limit, 1); !d.Allowed || d.Source != SourceStore {
			t.Fatalf("healthy store: %+v, want allowed from the store", d)
		}
	}
	if d := tDecide(t, g, "k", limit, 1); d.Allowed || d.Source != SourceStore {
		t.Fatalf("over the limit with a healthy store: %+v, want the store's denial passed through", d)
	}
	if g.Degraded() != 0 {
		t.Fatalf("Degraded = %d with a healthy store, want 0 (a denial is not an outage)", g.Degraded())
	}
}

func TestGuard_FailOpenAndFailClosed(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk})
	open := tGuard(t, GuardConfig{Primary: lim, Mode: FailOpen})
	closed := tGuard(t, GuardConfig{Primary: lim, Mode: FailClosed, ClosedRetryAfter: 2 * time.Second})
	limit := Limit{Rate: 10, Burst: 2}
	store.Fail(tErrBoom)
	for i := 0; i < 5; i++ {
		if d := tDecide(t, open, "k", limit, 1); !d.Allowed || d.Source != SourceFailOpen {
			t.Fatalf("fail-open during an outage: %+v, want allowed with Source fail-open", d)
		}
		if d := tDecide(t, closed, "k", limit, 1); d.Allowed || d.Source != SourceFailClosed || d.RetryAfter != 2*time.Second {
			t.Fatalf("fail-closed during an outage: %+v, want denied, Source fail-closed, RetryAfter 2s", d)
		}
	}
	if open.Degraded() != 5 || closed.Degraded() != 5 {
		t.Fatalf("Degraded = %d, %d; want 5, 5 (count every decision the store did not make)", open.Degraded(), closed.Degraded())
	}
	store.Fail(nil)
	if d := tDecide(t, closed, "k", limit, 1); !d.Allowed || d.Source != SourceStore {
		t.Fatalf("after recovery: %+v, want the store deciding again", d)
	}
}

func TestGuard_FailLocalEnforcesTheScaledLimit(t *testing.T) {
	clk := tNewClock()
	shared := NewMemoryStore(clk)
	lim := tLimiter(t, LimiterConfig{Store: shared, Clock: clk})
	local := tLimiter(t, LimiterConfig{Store: NewMemoryStore(clk), Clock: clk})
	g := tGuard(t, GuardConfig{Primary: lim, Fallback: local, Mode: FailLocal, FallbackScale: 0.2})
	shared.Fail(tErrBoom)
	allowed := 0
	for i := 0; i < 50; i++ {
		d := tDecide(t, g, "tenant", Limit{Rate: 100, Burst: 100}, 1)
		if d.Source != SourceFallback {
			t.Fatalf("decision %d during the outage came from %q, want fallback", i, d.Source)
		}
		if d.Allowed {
			allowed++
		}
	}
	if allowed != 20 {
		t.Fatalf("fallback admitted %d of 50 instant requests, want 20 (burst 100 × scale 0.2): the local "+
			"limit bounds the damage instead of admitting everything", allowed)
	}
	if g.Degraded() != 50 {
		t.Fatalf("Degraded = %d, want 50", g.Degraded())
	}
}

func TestGuard_SlowStoreIsCutOffAtTheTimeout(t *testing.T) {
	clk := tNewClock()
	lim := tLimiter(t, LimiterConfig{Store: tNewBlockingStore(t), Clock: clk})
	g := tGuard(t, GuardConfig{Primary: lim, Mode: FailOpen, Timeout: 20 * time.Millisecond})
	var wg sync.WaitGroup
	var slowest atomic.Int64
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			d, err := g.Decide(context.Background(), "k", Limit{Rate: 10, Burst: 5}, 1)
			el := time.Since(start)
			for {
				cur := slowest.Load()
				if int64(el) <= cur || slowest.CompareAndSwap(cur, int64(el)) {
					break
				}
			}
			if err != nil || !d.Allowed || d.Source != SourceFailOpen {
				t.Errorf("hung store: %+v, %v; want a fail-open admission", d, err)
			}
		}()
	}
	tWait(t, &wg, 3*time.Second, "16 decisions against a store that never answers")
	if el := time.Duration(slowest.Load()); el > time.Second {
		t.Fatalf("slowest decision took %v with a 20ms Timeout: the limiter's store has become the outage", el)
	}
	if g.Degraded() != 16 {
		t.Fatalf("Degraded = %d, want 16", g.Degraded())
	}
}

func TestGuard_BadArgumentsAndCallerCancellationAreNotOutages(t *testing.T) {
	clk := tNewClock()
	lim := tLimiter(t, LimiterConfig{Store: NewMemoryStore(clk), Clock: clk})
	g := tGuard(t, GuardConfig{Primary: lim, Mode: FailOpen})
	if _, err := g.Decide(context.Background(), "k", Limit{Rate: 10, Burst: 5}, 6); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("cost above the burst: err = %v, want ErrBadArgument (not a fail-open admission)", err)
	}
	slow := tGuard(t, GuardConfig{
		Primary: tLimiter(t, LimiterConfig{Store: tNewBlockingStore(t), Clock: clk}),
		Mode:    FailOpen, Timeout: 5 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() {
		d, err := slow.Decide(ctx, "k", Limit{Rate: 10, Burst: 5}, 1)
		if d.Allowed {
			err = errors.New("admitted on behalf of a caller that went away")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancelled mid-call: err = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Decide ignored the caller's cancellation")
	}
	if g.Degraded()+slow.Degraded() != 0 {
		t.Fatalf("Degraded = %d, %d; want 0, 0: neither a bad request nor a departed caller is a store outage",
			g.Degraded(), slow.Degraded())
	}
}

func TestNewGuard_ValidatesConfig(t *testing.T) {
	clk := tNewClock()
	lim := tLimiter(t, LimiterConfig{Store: NewMemoryStore(clk), Clock: clk})
	for name, cfg := range map[string]GuardConfig{
		"nil primary":          {Mode: FailOpen},
		"local, no fallback":   {Primary: lim, Mode: FailLocal},
		"unknown mode":         {Primary: lim, Mode: FailMode(7)},
		"negative timeout":     {Primary: lim, Timeout: -time.Second},
		"negative scale":       {Primary: lim, Fallback: lim, Mode: FailLocal, FallbackScale: -1},
		"negative retry-after": {Primary: lim, Mode: FailClosed, ClosedRetryAfter: -time.Second},
	} {
		if _, err := NewGuard(cfg); !errors.Is(err, ErrBadArgument) {
			t.Errorf("%s: err = %v, want ErrBadArgument", name, err)
		}
	}
}

// ---------- 4. LeaseClient ----------

func TestLeaseClient_OneStoreRoundTripPerBatch(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk})
	lc := tLease(t, LeaseConfig{Pool: lim, Clock: clk, Batch: 10})
	limit := Limit{Rate: 1, Burst: 100} // T = 1 s
	fromStore := 0
	for i := 0; i < 100; i++ {
		d := tDecide(t, lc, "hot", limit, 1)
		if !d.Allowed {
			t.Fatalf("request %d denied with 100 tokens in the pool", i+1)
		}
		if d.Source == SourceStore {
			fromStore++
		} else if d.Source != SourceLease {
			t.Fatalf("request %d: Source %q", i+1, d.Source)
		}
	}
	if fromStore != 10 || store.Ops() != 20 {
		t.Fatalf("100 requests with Batch 10: %d leases, %d store ops; want 10 and 20: the store should see "+
			"one Load + CompareAndSwap per batch, not per request", fromStore, store.Ops())
	}
	d := tDecide(t, lc, "hot", limit, 1)
	if d.Allowed || d.Source != SourceStore || d.RetryAfter != 10*time.Second {
		t.Fatalf("101st request: %+v; want a store denial with RetryAfter 10s (the wait for a whole batch "+
			"of 10 at T = 1 s: a leasing node polls once per batch, not once per refilled token)", d)
	}
	if store.Ops() != 21 {
		t.Fatalf("the denied lease cost %d ops, want 1 (a Load, no write)", store.Ops()-20)
	}
	for i := 0; i < 50; i++ {
		clk.Advance(100 * time.Millisecond)
		want := 10*time.Second - time.Duration(i+1)*100*time.Millisecond
		if d := tDecide(t, lc, "hot", limit, 1); d.Allowed || d.Source != SourceDenyCache || d.RetryAfter != want {
			t.Fatalf("repeat request %d over the limit: %+v; want a deny-cache denial with RetryAfter %v", i, d, want)
		}
	}
	if store.Ops() != 21 {
		t.Fatalf("50 cached denials cost the store %d ops, want 0", store.Ops()-21)
	}
}

func TestLeaseClient_TakesWhatIsLeftWhenABatchDoesNotFit(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk})
	lc := tLease(t, LeaseConfig{Pool: lim, Clock: clk, Batch: 10})
	limit := Limit{Rate: 1, Burst: 25}
	allowed := 0
	for i := 0; i < 25; i++ {
		if tDecide(t, lc, "k", limit, 1).Allowed {
			allowed++
		}
	}
	if allowed != 25 {
		t.Fatalf("%d of 25 admitted against a burst of 25 with Batch 10, want 25: when 10 do not fit, take "+
			"the 5 that do instead of denying with tokens left in the pool", allowed)
	}
	if store.Ops() != 7 {
		t.Fatalf("store ops = %d, want 7 (2 + 2 + a denied Load + 2 for the remainder)", store.Ops())
	}
}

func TestLeaseClient_HeldTokensExpireAfterTheLeaseTTL(t *testing.T) {
	clk := tNewClock()
	lim := tLimiter(t, LimiterConfig{Store: NewMemoryStore(clk), Clock: clk})
	lc := tLease(t, LeaseConfig{Pool: lim, Clock: clk, Batch: 10, LeaseTTL: 500 * time.Millisecond})
	limit := Limit{Rate: 1000, Burst: 1000}
	if d := tDecide(t, lc, "k", limit, 1); !d.Allowed || d.Remaining != 9 || lc.Held() != 9 {
		t.Fatalf("first request: %+v, Held %d; want allowed, Remaining 9, Held 9", d, lc.Held())
	}
	clk.Advance(499 * time.Millisecond)
	if lc.Held() != 9 {
		t.Fatalf("Held = %d before the lease TTL, want 9", lc.Held())
	}
	clk.Advance(time.Millisecond)
	if lc.Held() != 0 {
		t.Fatalf("Held = %d at the lease TTL, want 0: hoarded tokens spent much later turn into a burst "+
			"on top of the global limit", lc.Held())
	}
	if d := tDecide(t, lc, "k", limit, 1); !d.Allowed || d.Source != SourceStore {
		t.Fatalf("after expiry: %+v; want a fresh lease from the store", d)
	}
}

func TestLeaseClient_DenyCacheShieldsTheStoreFromAKeyOverItsLimit(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk})
	lc := tLease(t, LeaseConfig{Pool: lim, Clock: clk, Batch: 5})
	limit := Limit{Rate: 10, Burst: 10}
	allowed := 0
	for i := 0; i < 2000; i++ { // 1,000 req/s for 2 s against 10 req/s
		clk.Advance(time.Millisecond)
		if tDecide(t, lc, "abusive", limit, 1).Allowed {
			allowed++
		}
	}
	if allowed < 20 || allowed > 30 {
		t.Fatalf("admitted %d in 2 s, want at most burst + 2 s × rate = 30, and at least 20 (a node that "+
			"waits for whole batches admits a little less)", allowed)
	}
	if ops := store.Ops(); ops > 40 {
		t.Fatalf("2,000 requests over the limit cost the store %d ops, want ≤ 40: without a deny cache a "+
			"key over its limit costs one round trip per request", ops)
	}
}

func TestLeaseClient_FleetOvershootIsBoundedByNodesTimesBatch(t *testing.T) {
	limit := Limit{Rate: 100, Burst: 50}
	const nodes, batch = 5, 20
	res := tSimulate(t, nodes, batch, limit, time.Second, 300, 10*time.Second, 1, nil)
	bound := limit.Burst + int(limit.Rate) + nodes*batch // any 1 s window
	if res.maxWindow > bound {
		t.Fatalf("busiest 1 s window admitted %d, want ≤ burst + rate × 1 s + nodes × batch = %d", res.maxWindow, bound)
	}
	if total := limit.Burst + 10*int(limit.Rate); res.admitted > total {
		t.Fatalf("admitted %d in 10 s, want ≤ burst + 10 s × rate = %d: leases may move tokens in time, "+
			"never mint them", res.admitted, total)
	}
	if res.admitted < 950 {
		t.Fatalf("admitted only %d of ≈ 3,000 offered in 10 s, want ≥ 950 of the 1,050 allowed: leases are "+
			"stranding tokens", res.admitted)
	}
	direct := tSimulate(t, nodes, 0, limit, time.Second, 300, 10*time.Second, 1, nil)
	if res.storeOps*3 > direct.storeOps*2 {
		t.Fatalf("over the limit, leasing cost the store %d ops vs %d without leases, want at most two "+
			"thirds: denials must be cached until a batch could be available", res.storeOps, direct.storeOps)
	}
}

func TestLeaseClient_UnderTheLimitCostsAboutTwoOpsPerBatch(t *testing.T) {
	limit := Limit{Rate: 1000, Burst: 200}
	const nodes, batch = 10, 20
	res := tSimulate(t, nodes, batch, limit, time.Second, 800, 10*time.Second, 2, nil)
	if rejected := res.offered - res.admitted; rejected*1000 > res.offered {
		t.Fatalf("%d of %d rejected at 80%% of the limit, want ≤ 0.1%%: every one of them is wrong",
			rejected, res.offered)
	}
	if limit := int64(res.admitted)/batch*2*5/4 + 100; res.storeOps > limit {
		t.Fatalf("%d admitted with Batch %d cost the store %d ops, want ≤ %d (≈ 2 per batch): leasing is "+
			"not batching", res.admitted, batch, res.storeOps, limit)
	}
}

func TestLeaseClient_ConcurrentCallersAcrossNodesConserveTokens(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk, MaxRetries: 1000})
	limit := Limit{Rate: 1, Burst: 1000}
	var admitted atomic.Int64
	var wg sync.WaitGroup
	clients := make([]*LeaseClient, 4)
	for c := range clients {
		clients[c] = tLease(t, LeaseConfig{Pool: lim, Clock: clk, Batch: 50})
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func(lc *LeaseClient) {
				defer wg.Done()
				for i := 0; i < 60; i++ {
					d, err := lc.Decide(context.Background(), "tenant", limit, 1)
					if err != nil {
						t.Errorf("Decide: %v", err)
						return
					}
					if d.Allowed {
						admitted.Add(1)
					}
				}
			}(clients[c])
		}
	}
	tWait(t, &wg, 10*time.Second, "4 nodes × 8 goroutines")
	held := 0
	for _, lc := range clients {
		held += lc.Held()
	}
	if got := int(admitted.Load()) + held; got != 1000 {
		t.Fatalf("admitted %d + held %d = %d, want exactly the burst of 1,000: a token is spent, held, or "+
			"still in the pool, never minted or lost", admitted.Load(), held, got)
	}
	if held >= 4*50 {
		t.Fatalf("nodes hold %d tokens, want < nodes × batch = 200", held)
	}
	if store.Ops() > 150 {
		t.Fatalf("1,920 requests cost %d store ops, want ≤ 150: concurrent callers on one node must share a "+
			"lease, not each take one", store.Ops())
	}
}

func TestLeaseClient_CloseRefundsUnspentTokens(t *testing.T) {
	clk := tNewClock()
	lim := tLimiter(t, LimiterConfig{Store: NewMemoryStore(clk), Clock: clk})
	limit := Limit{Rate: 1, Burst: 10}
	a := tLease(t, LeaseConfig{Pool: lim, Clock: clk, Batch: 10})
	if d := tDecide(t, a, "k", limit, 1); !d.Allowed || a.Held() != 9 {
		t.Fatalf("node a: %+v, Held %d; want allowed with 9 held", d, a.Held())
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := a.Decide(context.Background(), "k", limit, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("Decide after Close: err = %v, want ErrClosed", err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v, want nil", err)
	}
	b := tLease(t, LeaseConfig{Pool: lim, Clock: clk, Batch: 10})
	allowed := 0
	for i := 0; i < 12; i++ {
		if tDecide(t, b, "k", limit, 1).Allowed {
			allowed++
		}
	}
	if allowed != 9 {
		t.Fatalf("node b admitted %d after node a drained, want the 9 tokens a gave back (not 0: a "+
			"draining node must not strand its share)", allowed)
	}
}

func TestLeaseClient_PoolErrorsAndComposition(t *testing.T) {
	clk := tNewClock()
	store := NewMemoryStore(clk)
	lim := tLimiter(t, LimiterConfig{Store: store, Clock: clk})
	lc := tLease(t, LeaseConfig{Pool: lim, Clock: clk, Batch: 10})
	limit := Limit{Rate: 100, Burst: 100}
	store.Fail(tErrBoom)
	if _, err := lc.Decide(context.Background(), "k", limit, 1); !errors.Is(err, tErrBoom) {
		t.Fatalf("pool down: err = %v, want the store error", err)
	}
	if lc.Held() != 0 {
		t.Fatalf("Held = %d after a failed lease, want 0", lc.Held())
	}
	local := tLimiter(t, LimiterConfig{Store: NewMemoryStore(clk), Clock: clk})
	g := tGuard(t, GuardConfig{Primary: lc, Fallback: local, Mode: FailLocal, FallbackScale: 0.5})
	if d := tDecide(t, g, "k", limit, 1); !d.Allowed || d.Source != SourceFallback {
		t.Fatalf("Guard over a LeaseClient, store down: %+v; want a fallback admission", d)
	}
	store.Fail(nil)
	if d := tDecide(t, g, "k", limit, 1); !d.Allowed || d.Source != SourceStore {
		t.Fatalf("store back: %+v; want a fresh lease", d)
	}
	if d := tDecide(t, g, "k", limit, 1); !d.Allowed || d.Source != SourceLease {
		t.Fatalf("next request: %+v; want it spent from the lease", d)
	}
	if _, err := lc.Decide(context.Background(), "k", limit, 101); !errors.Is(err, ErrBadArgument) {
		t.Fatalf("cost above the burst: err = %v, want ErrBadArgument", err)
	}
	for name, cfg := range map[string]LeaseConfig{
		"nil pool":       {Batch: 10},
		"negative batch": {Pool: lim, Batch: -1},
		"negative ttl":   {Pool: lim, LeaseTTL: -time.Second},
	} {
		if _, err := NewLeaseClient(cfg); !errors.Is(err, ErrBadArgument) {
			t.Errorf("NewLeaseClient(%s): err = %v, want ErrBadArgument", name, err)
		}
	}
}
