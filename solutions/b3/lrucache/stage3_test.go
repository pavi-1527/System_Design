package lrucache

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type evictRecord struct {
	key    string
	value  int
	reason Reason
}

type recorder struct {
	mu   sync.Mutex
	recs []evictRecord
}

func (r *recorder) add(k string, v int, why Reason) {
	r.mu.Lock()
	r.recs = append(r.recs, evictRecord{k, v, why})
	r.mu.Unlock()
}

func (r *recorder) all() []evictRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]evictRecord(nil), r.recs...)
}

func recordingCache(t *testing.T, capacity int, ttl time.Duration) (*Cache[string, int], *fakeClock, *recorder) {
	t.Helper()
	clk := newFakeClock()
	rec := &recorder{}
	c := mustNew(t, capacity,
		WithClock[string, int](clk),
		WithDefaultTTL[string, int](ttl),
		WithOnEvict[string, int](rec.add),
	)
	return c, clk, rec
}

func TestStage3_OnEvictReasons(t *testing.T) {
	c, clk, rec := recordingCache(t, 2, 10*time.Second)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3) // capacity: evicts a
	c.Delete("b") // explicit
	c.Put("b", 20)
	c.Put("b", 21) // replacing a value calls nothing
	clk.Advance(time.Minute)
	c.Get("c") // expired
	want := []evictRecord{
		{"a", 1, Capacity},
		{"b", 2, Deleted},
		{"c", 3, Expired},
	}
	if got := rec.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("eviction callbacks = %+v\nwant %+v", got, want)
	}
}

func TestStage3_RemoveExpiredReportsEachEntry(t *testing.T) {
	c, clk, rec := recordingCache(t, 5, time.Second)
	c.Put("a", 1)
	c.Put("b", 2)
	clk.Advance(time.Minute)
	if n := c.RemoveExpired(); n != 2 {
		t.Fatalf("RemoveExpired = %d, want 2", n)
	}
	got := rec.all()
	if len(got) != 2 || got[0].reason != Expired || got[1].reason != Expired {
		t.Fatalf("callbacks = %+v, want two Expired", got)
	}
}

func TestStage3_DeleteOfExpiredEntryCountsAsExpiration(t *testing.T) {
	c, clk, rec := recordingCache(t, 3, time.Second)
	c.Put("a", 1)
	clk.Advance(time.Minute)
	if c.Delete("a") {
		t.Fatal("Delete(a) = true, want false: a was already expired")
	}
	if got := rec.all(); !reflect.DeepEqual(got, []evictRecord{{"a", 1, Expired}}) {
		t.Fatalf("callbacks = %+v, want one Expired for a", got)
	}
	if c.Len() != 0 {
		t.Fatalf("Len = %d, want 0", c.Len())
	}
}

func TestStage3_CallbackMayReenterTheCache(t *testing.T) {
	var c *Cache[string, int]
	var calls int
	c = mustNew(t, 1,
		WithOnEvict[string, int](func(k string, v int, why Reason) {
			// Calling back into the cache deadlocks if the callback runs
			// while the cache's lock is held.
			calls++
			if calls == 1 {
				c.Len()
				c.Get("whatever")
				c.Put("from-callback", 99)
			}
		}),
	)
	within(t, "Put whose eviction callback calls back into the cache", func() {
		c.Put("a", 1)
		c.Put("b", 2) // evicts a -> callback -> Put(from-callback) evicts b -> callback
	})
	if calls != 2 {
		t.Fatalf("callback ran %d times, want 2 (a, then b)", calls)
	}
	if v, ok := c.Get("from-callback"); !ok || v != 99 || c.Len() != 1 {
		t.Fatalf("Get(from-callback) = (%v, %v), Len %d; want (99, true), 1", v, ok, c.Len())
	}
}

func TestStage3_FullCacheEvictsExpiredBeforeLiveLRU(t *testing.T) {
	c, clk, rec := recordingCache(t, 3, 0)
	c.Put("old-live", 1) // the LRU entry, but live
	c.PutWithTTL("doomed", 2, 5*time.Second)
	c.Put("newer-live", 3)
	clk.Advance(10 * time.Second)
	c.Put("fresh", 4)
	if _, ok := c.Get("old-live"); !ok {
		t.Fatal("old-live was evicted although an expired entry (doomed) was available: a full cache must reap expired entries before dropping live ones")
	}
	want := []evictRecord{{"doomed", 2, Expired}}
	if got := rec.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("callbacks = %+v, want %+v", got, want)
	}
}

func TestStage3_ExpiredFirstPicksEarliestExpiry(t *testing.T) {
	c, clk, rec := recordingCache(t, 3, 0)
	c.PutWithTTL("late", 1, 9*time.Second)
	c.PutWithTTL("early", 2, 3*time.Second)
	c.PutWithTTL("mid", 3, 6*time.Second)
	clk.Advance(time.Minute) // all three expired
	c.Put("x", 9)
	if got := rec.all(); len(got) != 1 || got[0].key != "early" || got[0].reason != Expired {
		t.Fatalf("callbacks = %+v, want exactly one: early/Expired (the earliest expiry)", got)
	}
}

func TestStage3_ExpiredFirstEvictionIsNotLinear(t *testing.T) {
	// 40k live no-TTL entries are the LRU end of the list; 40k short-TTL entries
	// are newer. After they expire, every Put must find an expired entry
	// without walking past 40k live ones (that is 1.6 billion steps).
	const live, doomed = 40_000, 40_000
	clk := newFakeClock()
	var expired int
	c := mustNew(t, live+doomed,
		WithClock[int, int](clk),
		WithOnEvict[int, int](func(k, v int, why Reason) {
			if why == Expired {
				expired++
			}
		}),
	)
	for i := 0; i < live; i++ {
		c.Put(i, i)
	}
	for i := 0; i < doomed; i++ {
		c.PutWithTTL(live+i, i, time.Second)
	}
	clk.Advance(time.Minute)
	within(t, fmt.Sprintf("%d Puts that must each reap an expired entry", doomed), func() {
		for i := 0; i < doomed; i++ {
			c.Put(live+doomed+i, i)
		}
	})
	if expired != doomed {
		t.Fatalf("%d Expired callbacks, want %d", expired, doomed)
	}
	for i := 0; i < live; i += 997 {
		if _, ok := c.Get(i); !ok {
			t.Fatalf("live entry %d was evicted while expired entries were available", i)
		}
	}
}

func TestStage3_Stats(t *testing.T) {
	c, clk, _ := recordingCache(t, 2, 10*time.Second)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Get("a")    // hit
	c.Get("x")    // miss
	c.Get("y")    // miss
	c.Put("c", 3) // evicts b (capacity)
	clk.Advance(time.Minute)
	c.Get("a") // expired: a miss AND an expiration
	want := Stats{Hits: 1, Misses: 3, Evictions: 1, Expirations: 1}
	if got := c.Stats(); got != want {
		t.Fatalf("Stats() = %+v, want %+v", got, want)
	}
}

func TestStage3_StatsAreConsistentUnderConcurrency(t *testing.T) {
	const workers, gets = 8, 2_000
	c := mustNew[int, int](t, 100)
	for i := 0; i < 50; i++ {
		c.Put(i, i)
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < gets; i++ {
				c.Get((w + i) % 100) // half hit, half miss
				_ = c.Stats()
			}
		}(w)
	}
	within(t, "concurrent Gets and Stats", wg.Wait)
	s := c.Stats()
	if s.Hits+s.Misses != workers*gets {
		t.Fatalf("hits %d + misses %d = %d, want %d: counters lost updates", s.Hits, s.Misses, s.Hits+s.Misses, workers*gets)
	}
}

func TestStage3_ConcurrentEvictionCallbacks(t *testing.T) {
	var mu sync.Mutex
	counts := map[Reason]int{}
	c := mustNew(t, 16,
		WithOnEvict[int, int](func(k, v int, why Reason) {
			mu.Lock()
			counts[why]++
			mu.Unlock()
		}),
	)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 2_000; i++ {
				c.Put(w*10_000+i, i)
			}
		}(w)
	}
	within(t, "concurrent Puts with callbacks", wg.Wait)
	mu.Lock()
	defer mu.Unlock()
	if want := 8*2_000 - 16; counts[Capacity] != want || c.Len() != 16 {
		t.Fatalf("capacity evictions = %d (want %d), Len = %d (want 16): every removed entry must produce exactly one callback", counts[Capacity], want, c.Len())
	}
}
