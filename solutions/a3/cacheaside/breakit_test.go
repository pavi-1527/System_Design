//go:build breakit

package cacheaside

// Break-it experiments for A3 Unit 2 §5–§6. These are measurements, not
// pass/fail tests (apart from sanity checks that the experiment itself
// reproduces): they print numbers so you can compare strategies.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a3/cacheaside/

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBreakIt_StampedeCount: a hot key expires; 400 requests arrive within
// the 30 ms the "database" needs to answer. Compare the naive cache-aside
// code path (check, load, set) with Group.Do. The database here can serve 20
// queries at once; the peak concurrency tells you whether it would have
// survived.
func TestBreakIt_StampedeCount(t *testing.T) {
	const (
		requests = 400
		dbTime   = 30 * time.Millisecond
		dbSlots  = 20
	)
	run := func(name string, get func(load func() string) string) {
		var calls, inflight, peak atomic.Int64
		load := func() string {
			calls.Add(1)
			n := inflight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(dbTime)
			inflight.Add(-1)
			return "balance=100"
		}
		var wg sync.WaitGroup
		for i := 0; i < requests; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); get(load) }()
		}
		wg.Wait()
		verdict := "database survives"
		if peak.Load() > dbSlots {
			verdict = fmt.Sprintf("database saturated (%d slots)", dbSlots)
		}
		t.Logf("%-22s db queries: %4d   peak concurrent: %4d   %s", name, calls.Load(), peak.Load(), verdict)
	}

	var mu sync.Mutex
	naive := map[string]string{}
	run("naive check-load-set", func(load func() string) string {
		mu.Lock()
		v, ok := naive["k"]
		mu.Unlock()
		if ok {
			return v
		}
		v = load()
		mu.Lock()
		naive["k"] = v
		mu.Unlock()
		return v
	})

	var g Group[string]
	var mu2 sync.Mutex
	guarded := map[string]string{}
	run("with Group.Do", func(load func() string) string {
		mu2.Lock()
		v, ok := guarded["k"]
		mu2.Unlock()
		if ok {
			return v
		}
		v, _, _ = g.Do(context.Background(), "k", func(context.Context) (string, error) {
			s := load()
			mu2.Lock()
			guarded["k"] = s
			mu2.Unlock()
			return s, nil
		})
		return v
	})
}

// TestBreakIt_SynchronizedExpiry: 2000 keys are warmed at the same moment
// (a deploy, a cold start, a bulk import) with TTL = 60 s. Simulate five
// minutes of one request per key per second on a fake clock and count
// database loads per second, with and without jitter.
func TestBreakIt_SynchronizedExpiry(t *testing.T) {
	const (
		keys    = 2000
		seconds = 300
	)
	simulate := func(jitter float64) (peak, spikes int) {
		clk := tNewClock()
		rng := rand.New(rand.NewSource(1))
		var loads atomic.Int64
		c := New(Config{TTL: 60 * time.Second, Jitter: jitter, Rand: rng.Float64, Now: clk.Now},
			func(ctx context.Context, key string) (string, error) { loads.Add(1); return key, nil })
		ctx := context.Background()
		ks := make([]string, keys)
		for i := range ks {
			ks[i] = fmt.Sprintf("acct:%d", i)
			c.Get(ctx, ks[i]) // warm-up at t = 0
		}
		for s := 1; s <= seconds; s++ {
			clk.Advance(time.Second)
			before := loads.Load()
			for _, k := range ks {
				c.Get(ctx, k)
			}
			n := int(loads.Load() - before)
			if n > peak {
				peak = n
			}
			if n >= keys/8 {
				spikes++ // a second in which >= 12.5% of the keys hit the database at once
			}
		}
		return peak, spikes
	}
	for _, j := range []float64{0, 0.1, 0.2, 0.5} {
		peak, spikes := simulate(j)
		t.Logf("jitter %.1f: peak loads in one second = %4d   seconds with a spike (>= %d loads): %d of %d", j, peak, keys/8, spikes, seconds)
		if j == 0 && peak < keys {
			t.Fatalf("expected all %d keys to expire in the same second without jitter, peak was %d", keys, peak)
		}
	}
}

// TestBreakIt_StaleSetRace: the classic cache-aside race with a hand-driven
// interleaving. Reader R misses and reads the database (value v1). Before R
// writes to the cache, writer W updates the database to v2 and deletes the
// cache key. R then stores v1. We check what the next reader sees.
func TestBreakIt_StaleSetRace(t *testing.T) {
	// --- naive cache-aside: Get; on miss read DB; Set. Writer: update DB; Delete.
	var mu sync.Mutex
	db, cache := "v1", map[string]string{}
	rRead := make(chan struct{})
	rGo := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // reader R
		defer wg.Done()
		mu.Lock()
		_, hit := cache["k"]
		v := db
		mu.Unlock()
		if hit {
			return
		}
		close(rRead) // R has read v1 ...
		<-rGo        // ... and is slow (GC pause, network) before the SET
		mu.Lock()
		cache["k"] = v
		mu.Unlock()
	}()
	<-rRead
	mu.Lock() // writer W
	db = "v2"
	delete(cache, "k")
	mu.Unlock()
	close(rGo)
	wg.Wait()
	mu.Lock()
	t.Logf("naive cache-aside:  database=%s  cache=%s   (stale until the TTL expires)", db, cache["k"])
	stale := cache["k"] != db
	mu.Unlock()
	if !stale {
		t.Fatalf("the harness failed to reproduce the race")
	}

	// --- Cache with an invalidation-aware load.
	source := "v1"
	var smu sync.Mutex
	read := make(chan struct{})
	proceed := make(chan struct{})
	var first atomic.Bool
	c := New(Config{TTL: time.Hour}, func(ctx context.Context, key string) (string, error) {
		smu.Lock()
		v := source
		smu.Unlock()
		if first.CompareAndSwap(false, true) {
			close(read)
			<-proceed
		}
		return v, nil
	})
	done := make(chan struct{})
	go func() { c.Get(context.Background(), "k"); close(done) }()
	<-read
	smu.Lock()
	source = "v2"
	smu.Unlock()
	c.Invalidate("k")
	close(proceed)
	<-done
	v, _ := c.Get(context.Background(), "k")
	smu.Lock()
	t.Logf("cacheaside.Cache:   database=%s  cache=%s", source, v)
	smu.Unlock()
}
