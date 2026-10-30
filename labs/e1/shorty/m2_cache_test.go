package shorty

// Milestone 2, in-process: the redirect cache and singleflight (Unit 2 §2).

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tNewCache(t *testing.T, capacity int, clk *tClock) *Cache[string] {
	t.Helper()
	c := NewCache[string](capacity, clk.Now)
	if c == nil {
		t.Fatal("NewCache returned nil")
	}
	return c
}

func TestM2_CacheLRUEvictsLeastRecentlyUsed(t *testing.T) {
	clk := tNewClock()
	c := tNewCache(t, 3, clk)
	for _, k := range []string{"a", "b", "c"} {
		c.Set(k, "v"+k, time.Minute, 0)
	}
	if _, st := c.Get("a"); st != Fresh { // a is now most recently used
		t.Fatalf("Get(a) = %v, want fresh", st)
	}
	c.Set("d", "vd", time.Minute, 0) // evicts b, the least recently used
	if _, st := c.Get("b"); st != Miss {
		t.Errorf("b should have been evicted (LRU), got %v; a Get must promote", st)
	}
	for _, k := range []string{"a", "c", "d"} {
		if v, st := c.Get(k); st != Fresh || v != "v"+k {
			t.Errorf("Get(%s) = %q %v, want fresh", k, v, st)
		}
	}
	if c.Len() != 3 {
		t.Errorf("Len = %d, want 3 (capacity)", c.Len())
	}
	c.Set("a", "new", time.Minute, 0)
	if v, _ := c.Get("a"); v != "new" || c.Len() != 3 {
		t.Errorf("overwrite: Get(a) = %q, Len %d; want new, 3", v, c.Len())
	}
}

func TestM2_CacheFreshThenStaleThenGone(t *testing.T) {
	clk := tNewClock()
	c := tNewCache(t, 10, clk)
	c.Set("k", "v", 10*time.Second, 60*time.Second)
	if v, st := c.Get("k"); st != Fresh || v != "v" {
		t.Fatalf("at 0s: %q %v, want fresh", v, st)
	}
	clk.Advance(10 * time.Second)
	if v, st := c.Get("k"); st != Stale || v != "v" {
		t.Fatalf("at 10s: %q %v, want stale (fresh lifetime is [0s, 10s))", v, st)
	}
	clk.Advance(59 * time.Second)
	if _, st := c.Get("k"); st != Stale {
		t.Fatalf("at 69s: %v, want stale", st)
	}
	clk.Advance(time.Second)
	if _, st := c.Get("k"); st != Miss {
		t.Fatalf("at 70s: %v, want miss (past the stale window)", st)
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d; an entry past its stale window should be removed when seen", c.Len())
	}
	c.Set("z", "v", 0, time.Minute)
	if _, st := c.Get("z"); st != Miss {
		t.Errorf("Set with fresh = 0 stored an entry (%v); a zero TTL means don't cache", st)
	}
}

func TestM2_CacheDeleteBeatsAnInFlightFill(t *testing.T) {
	clk := tNewClock()
	c := tNewCache(t, 10, clk)
	c.Set("k", "old", time.Minute, 0)

	tok := c.Token() // a lookup starts and reads the old value from the store...
	c.Delete("k")    // ...while a delete lands and invalidates
	if c.SetIfToken("k", tok, "old", time.Minute, 0) {
		t.Fatal("SetIfToken stored a value read before a Delete; the deleted link would be served for a full TTL")
	}
	if _, st := c.Get("k"); st != Miss {
		t.Fatalf("Get after Delete + stale fill = %v, want miss", st)
	}
	tok = c.Token()
	if !c.SetIfToken("k", tok, "new", time.Minute, 0) {
		t.Fatal("SetIfToken with a current token refused to store")
	}
	if v, st := c.Get("k"); st != Fresh || v != "new" {
		t.Fatalf("Get = %q %v, want new/fresh", v, st)
	}
}

func TestM2_CacheIsSafeForConcurrentUse(t *testing.T) {
	c := NewCache[int](64, nil)
	if c == nil {
		t.Fatal("NewCache returned nil")
	}
	tParallel(t, 16, 20*time.Second, func(w int) {
		for i := 0; i < 2000; i++ {
			k := fmt.Sprintf("k%d", (w*7+i)%100)
			switch i % 5 {
			case 0:
				c.Delete(k)
			case 1:
				c.SetIfToken(k, c.Token(), i, time.Minute, time.Minute)
			default:
				if _, st := c.Get(k); st == Miss {
					c.Set(k, i, time.Minute, time.Minute)
				}
			}
		}
	})
	if n := c.Len(); n > 64 {
		t.Fatalf("Len = %d, above capacity 64", n)
	}
}

func TestM2_SingleflightCoalescesConcurrentCalls(t *testing.T) {
	var g Group[string]
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	fn := func() (string, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		return "value", nil
	}
	var shared atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the leader
		defer wg.Done()
		if v, err, sh := g.Do("k", fn); v != "value" || err != nil || sh {
			t.Errorf("leader Do = %q %v shared=%v", v, err, sh)
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Do never ran fn")
	}
	for i := 0; i < 49; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err, sh := g.Do("k", fn)
			if v != "value" || err != nil {
				t.Errorf("waiter Do = %q %v", v, err)
			}
			if sh {
				shared.Add(1)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond) // let the waiters queue up behind the leader
	close(release)
	tWait(t, &wg, 10*time.Second)
	if n := calls.Load(); n != 1 {
		t.Fatalf("fn ran %d times for 50 concurrent callers of one key; a stampede reaches the store %d times", n, n)
	}
	if shared.Load() != 49 {
		t.Errorf("%d waiters saw shared=true, want 49", shared.Load())
	}
	if v, err, sh := g.Do("k", func() (string, error) { return "again", nil }); v != "again" || err != nil || sh {
		t.Errorf("Do after completion = %q %v %v; Group must not remember results (that's the cache's job)", v, err, sh)
	}
}

func TestM2_SingleflightSharesErrorsAndKeepsKeysApart(t *testing.T) {
	var g Group[int]
	boom := errors.New("store down")
	if _, err, _ := g.Do("a", func() (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("Do error = %v, want the fn's error", err)
	}
	var calls atomic.Int32
	tParallel(t, 20, 10*time.Second, func(i int) {
		k := fmt.Sprintf("key-%d", i)
		v, err, _ := g.Do(k, func() (int, error) { calls.Add(1); return i, nil })
		if err != nil || v != i {
			t.Errorf("Do(%s) = %d %v, want %d", k, v, err, i)
		}
	})
	if calls.Load() != 20 {
		t.Errorf("fn ran %d times for 20 different keys, want 20: coalescing is per key", calls.Load())
	}
}

func tWait(t *testing.T, wg *sync.WaitGroup, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("goroutines did not finish within %v", timeout)
	}
}
