package lru

import (
	"fmt"
	"math/rand"
	"reflect"
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

// tWait waits for wg, failing the test instead of hanging if it takes longer
// than d (e.g. a deadlock in the implementation under test).
func tWait(t *testing.T, wg *sync.WaitGroup, d time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %v (deadlock in the implementation?)", what, d)
	}
}

func tKeys(t *testing.T, c *Cache[int], want ...string) {
	t.Helper()
	got := c.Keys()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() (most- to least-recently used) = %v, want %v", got, want)
	}
}

// ---------- basics ----------

func TestGetSet_RoundTripAndMiss(t *testing.T) {
	c := New(Config[string]{MaxEntries: 10})
	if v, ok := c.Get("missing"); ok || v != "" {
		t.Fatalf(`Get of an absent key = (%q, %v), want ("", false)`, v, ok)
	}
	if !c.Set("a", "alpha") {
		t.Fatalf("Set returned false for an ordinary entry")
	}
	if v, ok := c.Get("a"); !ok || v != "alpha" {
		t.Fatalf(`Get("a") = (%q, %v), want ("alpha", true)`, v, ok)
	}
	if c.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", c.Len())
	}
}

func TestSet_OverwriteReplacesValueWithoutGrowing(t *testing.T) {
	c := New(Config[int]{MaxEntries: 3})
	c.Set("a", 1)
	c.Set("a", 2)
	c.Set("a", 3)
	if c.Len() != 1 {
		t.Fatalf("Len() = %d after overwriting one key three times, want 1 (a duplicate list node leaks capacity)", c.Len())
	}
	if v, _ := c.Get("a"); v != 3 {
		t.Fatalf("Get(a) = %d, want the last value 3", v)
	}
	if ev := c.Stats().Evictions; ev != 0 {
		t.Fatalf("Evictions = %d, want 0: an overwrite is not an eviction", ev)
	}
}

func TestKeys_OrderIsMostRecentFirst(t *testing.T) {
	c := New(Config[int]{MaxEntries: 5})
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)
	tKeys(t, c, "c", "b", "a")
	c.Get("a")
	tKeys(t, c, "a", "c", "b")
	c.Set("b", 20)
	tKeys(t, c, "b", "a", "c")
}

// ---------- eviction order: kills FIFO and "Get does not promote" ----------

func TestEviction_EvictsLeastRecentlyUsedNotOldestInserted(t *testing.T) {
	c := New(Config[int]{MaxEntries: 3})
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)
	c.Get("a") // a is now the most recently used; b is the least
	c.Set("d", 4)
	if _, ok := c.Peek("b"); ok {
		t.Fatalf("b should have been evicted (least recently used); keys now %v", c.Keys())
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := c.Peek(k); !ok {
			t.Fatalf("%s was evicted; with capacity 3 only b should go. A FIFO cache, or a Get that does not promote, evicts a here", k)
		}
	}
	if ev := c.Stats().Evictions; ev != 1 {
		t.Fatalf("Evictions = %d, want 1", ev)
	}
}

func TestEviction_OverwriteCountsAsUse(t *testing.T) {
	c := New(Config[int]{MaxEntries: 3})
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)
	c.Set("a", 10) // overwrite promotes
	c.Set("d", 4)
	if _, ok := c.Peek("a"); !ok {
		t.Fatalf("a was evicted although it was just overwritten; keys %v", c.Keys())
	}
	if _, ok := c.Peek("b"); ok {
		t.Fatalf("b should have been the victim; keys %v", c.Keys())
	}
}

func TestEviction_PeekDoesNotPromoteOrCount(t *testing.T) {
	c := New(Config[int]{MaxEntries: 2})
	c.Set("a", 1)
	c.Set("b", 2)
	if v, ok := c.Peek("a"); !ok || v != 1 {
		t.Fatalf("Peek(a) = (%d, %v), want (1, true)", v, ok)
	}
	c.Set("c", 3) // a is still the LRU entry, so it goes
	if _, ok := c.Peek("a"); ok {
		t.Fatalf("a survived: Peek must not promote; keys %v", c.Keys())
	}
	if s := c.Stats(); s.Hits != 0 || s.Misses != 0 {
		t.Fatalf("Peek changed stats: %+v, want no hits/misses", s)
	}
}

func TestEviction_CapacityOneKeepsTheNewestEntry(t *testing.T) {
	c := New(Config[int]{MaxEntries: 1})
	c.Set("a", 1)
	c.Set("b", 2)
	if _, ok := c.Peek("b"); !ok {
		t.Fatalf("the entry just written must never be the eviction victim")
	}
	if c.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", c.Len())
	}
}

func TestEviction_LongRunMatchesReferenceModel(t *testing.T) {
	// Compare against a deliberately naive O(n) model on a random trace.
	const capN = 8
	c := New(Config[int]{MaxEntries: capN})
	var model []string // most recent first
	touch := func(k string) {
		for i, x := range model {
			if x == k {
				model = append(model[:i], model[i+1:]...)
				break
			}
		}
		model = append([]string{k}, model...)
		if len(model) > capN {
			model = model[:capN]
		}
	}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 5000; i++ {
		k := fmt.Sprintf("k%d", rng.Intn(20))
		if rng.Intn(3) == 0 {
			c.Set(k, i)
			touch(k)
		} else {
			_, ok := c.Get(k)
			inModel := false
			for _, x := range model {
				if x == k {
					inModel = true
				}
			}
			if ok != inModel {
				t.Fatalf("step %d: Get(%s) hit=%v but the reference LRU says %v; cache %v model %v", i, k, ok, inModel, c.Keys(), model)
			}
			if inModel {
				touch(k)
			}
		}
		if got := c.Keys(); len(got) != len(model) || (len(got) > 0 && !reflect.DeepEqual(got, model)) {
			t.Fatalf("step %d: order %v, reference %v", i, got, model)
		}
	}
}

func TestUnbounded_ZeroLimitsNeverEvict(t *testing.T) {
	c := New(Config[int]{})
	for i := 0; i < 5000; i++ {
		c.Set(fmt.Sprint(i), i)
	}
	if c.Len() != 5000 || c.Stats().Evictions != 0 {
		t.Fatalf("Len=%d evictions=%d; with both limits 0 nothing should be evicted", c.Len(), c.Stats().Evictions)
	}
}

func TestDelete(t *testing.T) {
	var called atomic.Int64
	c := New(Config[int]{MaxEntries: 2, OnEvict: func(string, int, Reason) { called.Add(1) }})
	c.Set("a", 1)
	c.Set("b", 2)
	if !c.Delete("a") {
		t.Fatalf("Delete(a) = false, want true")
	}
	if c.Delete("a") {
		t.Fatalf("second Delete(a) = true, want false")
	}
	c.Set("c", 3) // fits because a's slot was freed
	if c.Len() != 2 || c.Stats().Evictions != 0 {
		t.Fatalf("Len=%d evictions=%d; Delete must free capacity and not count as an eviction", c.Len(), c.Stats().Evictions)
	}
	if called.Load() != 0 {
		t.Fatalf("OnEvict was called %d times; it is for capacity and TTL removals, not Delete", called.Load())
	}
	tKeys(t, c, "c", "b")
}

// ---------- TTL ----------

func TestTTL_ExpiresExactlyAtDeadline(t *testing.T) {
	clk := tNewClock()
	c := New(Config[int]{Now: clk.Now})
	c.SetWithCost("a", 1, 1, 10*time.Second)
	clk.Advance(10*time.Second - time.Nanosecond)
	if _, ok := c.Get("a"); !ok {
		t.Fatalf("entry expired 1ns early")
	}
	clk.Advance(time.Nanosecond) // now == deadline
	if _, ok := c.Get("a"); ok {
		t.Fatalf("entry still served at now == expiry; the rule is expired when now >= written+ttl")
	}
	s := c.Stats()
	if s.Expirations != 1 || s.Misses != 1 || s.Hits != 1 {
		t.Fatalf("stats %+v, want 1 hit (before), 1 miss + 1 expiration (at deadline)", s)
	}
}

func TestTTL_HitDoesNotExtendLifetime(t *testing.T) {
	clk := tNewClock()
	c := New(Config[int]{Now: clk.Now})
	c.SetWithCost("a", 1, 1, 10*time.Second)
	for i := 0; i < 4; i++ {
		clk.Advance(2 * time.Second)
		if _, ok := c.Get("a"); !ok {
			t.Fatalf("expired too early at +%ds", (i+1)*2)
		}
	}
	clk.Advance(2 * time.Second) // +10s since the write
	if _, ok := c.Get("a"); ok {
		t.Fatalf("a was kept alive by reads (sliding expiration); TTL counts from the write only. A cache that slides can serve stale data forever")
	}
}

func TestTTL_DefaultOverrideAndNever(t *testing.T) {
	clk := tNewClock()
	c := New(Config[int]{DefaultTTL: time.Minute, Now: clk.Now})
	c.Set("default", 1)
	c.SetWithCost("short", 2, 1, 5*time.Second)
	c.SetWithCost("never", 3, 1, -1)
	clk.Advance(10 * time.Second)
	if _, ok := c.Get("short"); ok {
		t.Fatalf("short (ttl 5s) still live after 10s")
	}
	if _, ok := c.Get("default"); !ok {
		t.Fatalf("default entry expired before DefaultTTL (1m)")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := c.Get("default"); ok {
		t.Fatalf("default entry still live after DefaultTTL")
	}
	if _, ok := c.Get("never"); !ok {
		t.Fatalf("entry written with ttl < 0 must never expire")
	}
}

func TestTTL_OverwriteResetsExpiry(t *testing.T) {
	clk := tNewClock()
	c := New(Config[int]{DefaultTTL: 10 * time.Second, Now: clk.Now})
	c.Set("a", 1)
	clk.Advance(8 * time.Second)
	c.Set("a", 2) // fresh write, fresh deadline
	clk.Advance(8 * time.Second)
	if v, ok := c.Get("a"); !ok || v != 2 {
		t.Fatalf("Get(a) = (%d, %v); an overwrite must restart the TTL", v, ok)
	}
}

func TestTTL_ExpiredEntryIsRemovedAndFreesCapacity(t *testing.T) {
	clk := tNewClock()
	c := New(Config[int]{MaxEntries: 2, Now: clk.Now})
	c.SetWithCost("a", 1, 1, time.Second)
	c.Set("b", 2)
	clk.Advance(time.Second)
	if _, ok := c.Get("a"); ok {
		t.Fatalf("a should have expired")
	}
	if c.Len() != 1 {
		t.Fatalf("Len() = %d after the expired entry was looked up, want 1: Get must remove expired entries", c.Len())
	}
	c.Set("c", 3)
	if _, ok := c.Peek("b"); !ok {
		t.Fatalf("b was evicted although freeing the expired slot left room")
	}
	if c.Stats().Evictions != 0 {
		t.Fatalf("Evictions = %d, want 0 (expiry is counted separately)", c.Stats().Evictions)
	}
}

func TestPeek_ExpiredIsAbsentButNotRemoved(t *testing.T) {
	clk := tNewClock()
	c := New(Config[int]{Now: clk.Now})
	c.SetWithCost("a", 1, 1, time.Second)
	clk.Advance(2 * time.Second)
	if _, ok := c.Peek("a"); ok {
		t.Fatalf("Peek returned an expired entry")
	}
	if c.Len() != 1 || c.Stats().Expirations != 0 {
		t.Fatalf("Peek must be read-only: Len=%d expirations=%d, want 1 and 0", c.Len(), c.Stats().Expirations)
	}
}

func TestPurgeExpired_RemovesOnlyExpiredAndReportsCount(t *testing.T) {
	clk := tNewClock()
	c := New(Config[int]{Now: clk.Now})
	for i := 0; i < 10; i++ {
		ttl := time.Duration(i+1) * time.Second
		c.SetWithCost(fmt.Sprint(i), i, 1, ttl)
	}
	c.SetWithCost("forever", 99, 1, -1)
	clk.Advance(4 * time.Second) // ttl 1..4 expire (deadline reached)
	if n := c.PurgeExpired(); n != 4 {
		t.Fatalf("PurgeExpired() = %d, want 4", n)
	}
	if c.Len() != 7 {
		t.Fatalf("Len() = %d, want 7", c.Len())
	}
	if c.Stats().Expirations != 4 {
		t.Fatalf("Expirations = %d, want 4", c.Stats().Expirations)
	}
	if n := c.PurgeExpired(); n != 0 {
		t.Fatalf("second PurgeExpired() = %d, want 0", n)
	}
}

// ---------- cost weighting ----------

func TestCost_EvictsUntilTheNewEntryFits(t *testing.T) {
	c := New(Config[string]{MaxCost: 100})
	c.SetWithCost("a", "a", 40, 0)
	c.SetWithCost("b", "b", 40, 0)
	c.SetWithCost("c", "c", 40, 0) // total 120 > 100 => evict a
	if _, ok := c.Peek("a"); ok {
		t.Fatalf("a should be evicted to bring cost under 100; keys %v", c.Keys())
	}
	if c.Cost() != 80 {
		t.Fatalf("Cost() = %d, want 80", c.Cost())
	}
	c.SetWithCost("big", "big", 90, 0) // needs b and c out too
	if c.Len() != 1 || c.Cost() != 90 {
		t.Fatalf("Len=%d Cost=%d, want a single entry of cost 90: one insert may evict several entries", c.Len(), c.Cost())
	}
	if ev := c.Stats().Evictions; ev != 3 {
		t.Fatalf("Evictions = %d, want 3", ev)
	}
}

func TestCost_OversizedEntryIsRejectedAndOldValueDropped(t *testing.T) {
	c := New(Config[string]{MaxCost: 100})
	c.SetWithCost("a", "old", 10, 0)
	c.SetWithCost("b", "b", 10, 0)
	if c.SetWithCost("a", "huge", 101, 0) {
		t.Fatalf("SetWithCost with cost > MaxCost returned true")
	}
	if _, ok := c.Peek("a"); ok {
		t.Fatalf("the old value of a is still cached after a rejected overwrite: readers would get stale data")
	}
	if _, ok := c.Peek("b"); !ok {
		t.Fatalf("an oversized insert must not evict unrelated entries")
	}
	if c.Cost() != 10 {
		t.Fatalf("Cost() = %d, want 10", c.Cost())
	}
}

func TestCost_OverwriteAdjustsAccountingAndZeroCostIsOne(t *testing.T) {
	c := New(Config[string]{MaxCost: 100})
	c.SetWithCost("a", "a", 60, 0)
	c.SetWithCost("a", "a2", 20, 0)
	if c.Cost() != 20 {
		t.Fatalf("Cost() = %d after shrinking a from 60 to 20, want 20", c.Cost())
	}
	c.SetWithCost("z", "z", 0, 0)
	c.SetWithCost("y", "y", -5, 0)
	if c.Cost() != 22 {
		t.Fatalf("Cost() = %d, want 22: a cost <= 0 counts as 1", c.Cost())
	}
	c.Delete("a")
	if c.Cost() != 2 {
		t.Fatalf("Cost() = %d after Delete, want 2", c.Cost())
	}
}

func TestCost_BothLimitsApply(t *testing.T) {
	c := New(Config[int]{MaxEntries: 3, MaxCost: 100})
	c.SetWithCost("a", 1, 10, 0)
	c.SetWithCost("b", 2, 10, 0)
	c.SetWithCost("c", 3, 10, 0)
	c.SetWithCost("d", 4, 10, 0) // entry limit
	tKeys(t, c, "d", "c", "b")
	c.SetWithCost("e", 5, 95, 0) // cost limit: everything else must go
	tKeys(t, c, "e")
}

// ---------- stats and callbacks ----------

func TestStats_HitsMissesEvictionsAndRatio(t *testing.T) {
	c := New(Config[int]{MaxEntries: 2})
	c.Set("a", 1)
	c.Set("b", 2)
	c.Get("a")
	c.Get("a")
	c.Get("zzz")
	c.Set("c", 3) // evicts b
	s := c.Stats()
	if s.Hits != 2 || s.Misses != 1 || s.Evictions != 1 {
		t.Fatalf("stats %+v, want hits 2 misses 1 evictions 1", s)
	}
	if r := s.HitRatio(); r < 0.66 || r > 0.67 {
		t.Fatalf("HitRatio() = %v, want ≈ 0.667", r)
	}
}

func TestOnEvict_ReasonsAndKeys(t *testing.T) {
	clk := tNewClock()
	type rec struct {
		key string
		val int
		why Reason
	}
	var mu sync.Mutex
	var got []rec
	c := New(Config[int]{MaxEntries: 2, Now: clk.Now, OnEvict: func(k string, v int, r Reason) {
		mu.Lock()
		got = append(got, rec{k, v, r})
		mu.Unlock()
	}})
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3) // evicts a (capacity)
	c.SetWithCost("d", 4, 1, time.Second)
	clk.Advance(time.Second)
	c.Get("d") // expired; removed lazily
	mu.Lock()
	defer mu.Unlock()
	want := []rec{{"a", 1, ReasonCapacity}, {"b", 2, ReasonCapacity}, {"d", 4, ReasonExpired}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OnEvict calls = %+v, want %+v", got, want)
	}
}

func TestOnEvict_RunsOutsideTheLock(t *testing.T) {
	var c *Cache[int]
	var calls atomic.Int64
	c = New(Config[int]{MaxEntries: 1, OnEvict: func(k string, v int, r Reason) {
		calls.Add(1)
		c.Len() // would deadlock if the cache still held its mutex
		c.Get("anything")
	}})
	done := make(chan struct{})
	go func() {
		c.Set("a", 1)
		c.Set("b", 2) // evicts a, callback re-enters the cache
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("deadlock: OnEvict was called while holding the cache lock. Collect victims under the lock, call the callback after unlocking")
	}
	if calls.Load() != 1 {
		t.Fatalf("OnEvict calls = %d, want 1", calls.Load())
	}
}

// ---------- generics ----------

func TestGeneric_StructValuesAndZeroOnMiss(t *testing.T) {
	type account struct {
		ID      string
		Balance int64
	}
	c := New(Config[*account]{MaxEntries: 2})
	c.Set("a", &account{"a", 100})
	if v, ok := c.Get("a"); !ok || v.Balance != 100 {
		t.Fatalf("Get(a) = (%v, %v)", v, ok)
	}
	if v, ok := c.Get("nope"); ok || v != nil {
		t.Fatalf("miss must return the zero value, got (%v, %v)", v, ok)
	}
}

// ---------- concurrency (run with -race) ----------

func TestConcurrent_MixedOperationsKeepInvariants(t *testing.T) {
	const (
		workers = 32
		ops     = 4000
		maxN    = 100
	)
	c := New(Config[int]{MaxEntries: maxN})
	var gets atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < ops; i++ {
				k := fmt.Sprint(rng.Intn(400))
				switch rng.Intn(10) {
				case 0, 1, 2:
					c.Set(k, i)
				case 3:
					c.Delete(k)
				case 4:
					c.Peek(k)
				case 5:
					c.Keys()
				default:
					gets.Add(1)
					c.Get(k)
				}
			}
		}(w)
	}
	tWait(t, &wg, 30*time.Second, "mixed workload")
	if c.Len() > maxN {
		t.Fatalf("Len() = %d > MaxEntries %d", c.Len(), maxN)
	}
	if keys := c.Keys(); len(keys) != c.Len() {
		t.Fatalf("list has %d nodes but the map has %d entries: they drifted apart under concurrency", len(keys), c.Len())
	}
	s := c.Stats()
	if int64(s.Hits+s.Misses) != gets.Load() {
		t.Fatalf("hits+misses = %d, but %d Gets were issued: counters lost updates", s.Hits+s.Misses, gets.Load())
	}
}

func TestConcurrent_SameKeyHammer(t *testing.T) {
	c := New(Config[int]{MaxEntries: 4})
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				c.Set("hot", i)
				c.Get("hot")
				if i%7 == 0 {
					c.Delete("hot")
				}
			}
		}(w)
	}
	tWait(t, &wg, 30*time.Second, "same-key hammer")
	if c.Len() > 1 {
		t.Fatalf("Len() = %d with a single key", c.Len())
	}
}

func TestConcurrent_TTLWithSharedClock(t *testing.T) {
	clk := tNewClock()
	c := New(Config[int]{MaxEntries: 50, DefaultTTL: time.Second, Now: clk.Now})
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				k := fmt.Sprint((w*31 + i) % 120)
				c.Set(k, i)
				c.Get(k)
				if i%100 == 0 {
					clk.Advance(300 * time.Millisecond)
					c.PurgeExpired()
				}
			}
		}(w)
	}
	tWait(t, &wg, 30*time.Second, "ttl workload")
	if keys := c.Keys(); len(keys) != c.Len() {
		t.Fatalf("list/map drift: %d vs %d", len(keys), c.Len())
	}
}

// ---------- O(1): kills list scans ----------

func TestScale_OperationsAreConstantTime(t *testing.T) {
	// 300k operations against a 100k-entry cache. With an O(n) step per
	// operation (scanning a slice or list to find the key or the LRU entry)
	// this is ~10^10 steps and takes minutes; an O(1) implementation takes
	// well under a second even with the race detector. The deadline is
	// generous (about 50x) so a slow CI machine does not flake.
	const (
		capN = 100_000
		ops  = 300_000
	)
	c := New(Config[int]{MaxEntries: capN})
	done := make(chan struct{})
	go func() {
		defer close(done)
		rng := rand.New(rand.NewSource(7))
		for i := 0; i < capN; i++ {
			c.Set(fmt.Sprint(i), i)
		}
		for i := 0; i < ops; i++ {
			k := fmt.Sprint(rng.Intn(2 * capN))
			if i%2 == 0 {
				c.Get(k)
			} else {
				c.Set(k, i)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("400k operations on a 100k-entry cache took > 20s: Get/Set/evict must be O(1) (map lookup + doubly linked list), not a scan")
	}
	if c.Len() != capN {
		t.Fatalf("Len() = %d, want %d", c.Len(), capN)
	}
	if keys := c.Keys(); len(keys) != capN {
		t.Fatalf("list length %d != %d", len(keys), capN)
	}
}
