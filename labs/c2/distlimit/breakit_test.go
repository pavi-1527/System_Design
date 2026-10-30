//go:build breakit

package distlimit

import (
	"context"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tBlindStore is what a shared counter looks like when the read-modify-write
// is written as GET, compute, SET: the "compare-and-swap" never compares. It
// yields first so that other gateways read the same old value, as they would
// across a network round trip. Run with:
//
//	go test -tags breakit -run BreakIt_NonAtomicStore -v ./labs/c2/distlimit/
type tBlindStore struct {
	mu sync.Mutex
	m  map[string]int64
}

func (s *tBlindStore) Load(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[key], nil
}

func (s *tBlindStore) CompareAndSwap(_ context.Context, key string, _, new int64, _ time.Duration) (bool, error) {
	runtime.Gosched()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = new
	return true, nil
}

func TestBreakIt_NonAtomicStore(t *testing.T) {
	clk := tNewClock()
	limit := Limit{Rate: 1, Burst: 100}
	for _, tc := range []struct {
		name  string
		store Store
	}{
		{"compare-and-swap (MemoryStore)", NewMemoryStore(clk)},
		{"GET then SET (blind write)", &tBlindStore{m: map[string]int64{}}},
	} {
		var admitted atomic.Int64
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			lim, err := NewLimiter(LimiterConfig{Store: tc.store, Clock: clk, MaxRetries: 1000})
			if err != nil {
				t.Fatal(err)
			}
			for w := 0; w < 8; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < 50; i++ {
						if d, err := lim.Decide(context.Background(), "tenant", limit, 1); err == nil && d.Allowed {
							admitted.Add(1)
						}
					}
				}()
			}
		}
		tWait(t, &wg, 30*time.Second, tc.name)
		t.Logf("%-32s 1,600 requests, burst 100, frozen clock: admitted %4d (%.1f× the limit)",
			tc.name, admitted.Load(), float64(admitted.Load())/100)
	}
}

// TestBreakIt_ClockSkew: two gateways share one key in one store, but each
// computes TAT from its own clock, and gateway B's clock runs ahead. Poisson
// traffic split evenly between them, limit 100 req/s with a burst of 20, in
// two regimes: 90 req/s offered (under the limit: every rejection is wrong)
// and 300 req/s (over it: admissions above 100/s are wrong).
//
//	go test -tags breakit -run BreakIt_ClockSkew -v ./labs/c2/distlimit/
func TestBreakIt_ClockSkew(t *testing.T) {
	limit := Limit{Rate: 100, Burst: 20}
	run := func(skew time.Duration, offered float64) (okA, okB, total int) {
		clk := tNewClock()
		store := NewMemoryStore(clk)
		a, _ := NewLimiter(LimiterConfig{Store: store, Clock: clk})
		b, _ := NewLimiter(LimiterConfig{Store: store, Clock: tOffsetClock{base: clk, off: skew}})
		r := rand.New(rand.NewPCG(4, 4))
		at := time.Duration(0)
		for {
			at += time.Duration(r.ExpFloat64() / offered * float64(time.Second))
			if at >= 10*time.Second {
				return okA, okB, total
			}
			clk.Set(tT0.Add(at))
			total++
			gw, ok := a, &okA
			if r.IntN(2) == 1 {
				gw, ok = b, &okB
			}
			if d, err := gw.Decide(context.Background(), "tenant", limit, 1); err == nil && d.Allowed {
				*ok++
			}
		}
	}
	t.Logf("%-10s | %-36s | %s", "", "90 req/s offered (under the limit)", "300 req/s offered (limit 100/s)")
	t.Logf("%-10s | %-12s %-23s | %s", "skew of B", "rejected", "admitted A / B", "admitted/s (A / B)")
	for _, skew := range []time.Duration{0, 10 * time.Millisecond, 50 * time.Millisecond,
		200 * time.Millisecond, time.Second} {
		ua, ub, ut := run(skew, 90)
		oa, ob, _ := run(skew, 300)
		t.Logf("%-10v | %5.1f%%       %4d / %-4d             | %5.1f  (%5.1f / %5.1f)", skew,
			100*float64(ut-ua-ub)/float64(ut), ua, ub, float64(oa+ob)/10, float64(oa)/10, float64(ob)/10)
	}
}

// TestBreakIt_LeaseSize sweeps the lease batch for 10 nodes sharing one
// tenant's limit (1,000 req/s, burst 200), in two regimes: under the limit
// (800 req/s offered: every rejection is wrong) and over it (3,000 req/s: what
// matters is the overshoot and the store load).
//
//	go test -tags breakit -run BreakIt_LeaseSize -v ./labs/c2/distlimit/
func TestBreakIt_LeaseSize(t *testing.T) {
	limit := Limit{Rate: 1000, Burst: 200}
	const nodes = 10
	t.Logf("%-8s | %-34s | %s", "", "800 req/s offered (under the limit)", "3,000 req/s offered (over the limit)")
	t.Logf("%-8s | %-12s %-21s | %-15s %-18s %s", "batch", "rejected", "store ops/s", "busiest 1 s", "bound", "store ops/s")
	for _, batch := range []int{0, 1, 5, 20, 50, 100} {
		under := tSimulate(t, nodes, batch, limit, time.Second, 800, 10*time.Second, 3, nil)
		over := tSimulate(t, nodes, batch, limit, time.Second, 3000, 10*time.Second, 3, nil)
		name := fmt.Sprint(batch)
		if batch == 0 {
			name = "none"
		}
		bound := limit.Burst + int(limit.Rate) + nodes*max(batch, 0)
		t.Logf("%-8s | %5.2f%%       %-21.0f | %-15d ≤ %-16d %.0f", name,
			100*float64(under.offered-under.admitted)/float64(under.offered), float64(under.storeOps)/10,
			over.maxWindow, bound, float64(over.storeOps)/10)
	}
	t.Log("synchronized burst: every node leases once, 0.5 s of quiet (the pool refills), then 5,000")
	t.Log("requests at one instant across the nodes; a direct check admits exactly the burst:")
	for _, batch := range []int{0, 5, 20, 50, 100} {
		t.Logf("  batch %-4d admitted %4d at one instant (burst %d)", batch, tSyncBurst(t, nodes, batch, limit), limit.Burst)
	}
	t.Log("one hot node (60% of the traffic through node 0), 800 req/s offered:")
	hot := func(r *rand.Rand) int {
		if r.Float64() < 0.6 {
			return 0
		}
		return 1 + r.IntN(nodes-1)
	}
	for _, batch := range []int{5, 50, 100} {
		res := tSimulate(t, nodes, batch, limit, time.Second, 800, 10*time.Second, 3, hot)
		t.Logf("  batch %-4d rejected %5.2f%%", batch, 100*float64(res.offered-res.admitted)/float64(res.offered))
	}
}

// tSyncBurst: each node takes one lease (spaced 100 ms apart so the pool can
// refill), the fleet goes quiet for 0.5 s, then 5,000 requests arrive at one
// instant, spread round-robin over the nodes. Returns how many are admitted.
func tSyncBurst(t *testing.T, nodes, batch int, limit Limit) int {
	t.Helper()
	clk := tNewClock()
	lim, _ := NewLimiter(LimiterConfig{Store: NewMemoryStore(clk), Clock: clk})
	ds := make([]Decider, nodes)
	for i := range ds {
		ds[i] = lim
		if batch > 0 {
			ds[i], _ = NewLeaseClient(LeaseConfig{Pool: lim, Clock: clk, Batch: batch, LeaseTTL: 5 * time.Second})
		}
	}
	for i, d := range ds {
		clk.Set(tT0.Add(time.Duration(i) * 100 * time.Millisecond))
		_, _ = d.Decide(context.Background(), "tenant", limit, 1)
	}
	clk.Advance(500 * time.Millisecond)
	admitted := 0
	for i := 0; i < 5000; i++ {
		if d, err := ds[i%nodes].Decide(context.Background(), "tenant", limit, 1); err == nil && d.Allowed {
			admitted++
		}
	}
	return admitted
}

// TestBreakIt_StoreOutage: 10 gateways, one tenant limited to 1,000 req/s
// (burst 200). The tenant sends 3,000 req/s (a retry storm), and the store is
// down from 5 s to 10 s. What reaches the backend during the outage, for each
// failure mode? A second, well-behaved tenant sends 500 req/s throughout.
//
//	go test -tags breakit -run BreakIt_StoreOutage -v ./labs/c2/distlimit/
func TestBreakIt_StoreOutage(t *testing.T) {
	limit := Limit{Rate: 1000, Burst: 200}
	const nodes = 10
	modes := []struct {
		name string
		mode FailMode
	}{{"fail open", FailOpen}, {"fail closed", FailClosed}, {"fail local (scale 2/N)", FailLocal}}
	t.Logf("%-24s %-22s %-22s %s", "mode", "storm admitted/s", "good tenant admitted/s", "(healthy: storm 1000/s, good 500/s)")
	for _, m := range modes {
		clk := tNewClock()
		store := NewMemoryStore(clk)
		shared, _ := NewLimiter(LimiterConfig{Store: store, Clock: clk})
		guards := make([]*Guard, nodes)
		for i := range guards {
			local, _ := NewLimiter(LimiterConfig{Store: NewMemoryStore(clk), Clock: clk})
			g, err := NewGuard(GuardConfig{Primary: shared, Fallback: local, Mode: m.mode, FallbackScale: 2.0 / nodes})
			if err != nil {
				t.Fatal(err)
			}
			guards[i] = g
		}
		r := rand.New(rand.NewPCG(5, 9))
		var stormOK, goodOK int
		next := map[string]time.Duration{"storm": 0, "good": 0}
		rates := map[string]float64{"storm": 3000, "good": 500}
		for {
			key := "storm"
			if next["good"] < next["storm"] {
				key = "good"
			}
			at := next[key]
			if at >= 10*time.Second {
				break
			}
			next[key] += time.Duration(r.ExpFloat64() / rates[key] * float64(time.Second))
			clk.Set(tT0.Add(at))
			if at >= 5*time.Second {
				store.Fail(tErrBoom)
			}
			d, err := guards[r.IntN(nodes)].Decide(context.Background(), key, limit, 1)
			if err != nil || !d.Allowed || at < 5*time.Second {
				continue
			}
			if key == "storm" {
				stormOK++
			} else {
				goodOK++
			}
		}
		t.Logf("%-24s %-22.0f %-22.0f", m.name, float64(stormOK)/5, float64(goodOK)/5)
	}
	t.Log(strings.Repeat("-", 40))
	t.Log("fail local bounds the storm at ≈ 2× its limit without turning the outage into a 100% rejection")
}
