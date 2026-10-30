package lfu

import (
	"container/list"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tLRU is a plain LRU used as the baseline the admission filter must beat.
type tLRU struct {
	cap int
	ll  *list.List
	m   map[string]*list.Element
}

func tNewLRU(cap int) *tLRU { return &tLRU{cap: cap, ll: list.New(), m: map[string]*list.Element{}} }
func (l *tLRU) Get(k string) bool {
	if el, ok := l.m[k]; ok {
		l.ll.MoveToFront(el)
		return true
	}
	return false
}
func (l *tLRU) Set(k string) {
	if l.ll.Len() >= l.cap {
		b := l.ll.Back()
		l.ll.Remove(b)
		delete(l.m, b.Value.(string))
	}
	l.m[k] = l.ll.PushFront(k)
}

// tAccess performs the cache-aside access pattern: Get, and Set on a miss.
func tAccess(c *Cache[int], k string) bool {
	if _, ok := c.Get(k); ok {
		return true
	}
	c.Set(k, 1)
	return false
}

func tAccessLRU(l *tLRU, k string) bool {
	if l.Get(k) {
		return true
	}
	l.Set(k)
	return false
}

func tHot(i int) string  { return fmt.Sprintf("hot-%02d", i) }
func tCold(i int) string { return fmt.Sprintf("cold-%05d", i) }

// ---------- sketch ----------

func TestSketch_NeverUnderestimatesBelowSaturation(t *testing.T) {
	s := NewSketch(4096, 1_000_000)
	rng := rand.New(rand.NewSource(1))
	truth := map[string]int{}
	for i := 0; i < 600; i++ {
		k := fmt.Sprintf("key-%d", i)
		n := 1 + rng.Intn(MaxCount)
		truth[k] = n
		for j := 0; j < n; j++ {
			s.Increment(k)
		}
	}
	over := 0
	for k, n := range truth {
		e := s.Estimate(k)
		if e < n {
			t.Fatalf("Estimate(%s) = %d < true count %d: a count-min sketch may only over-count", k, e, n)
		}
		over += e - n
	}
	if avg := float64(over) / float64(len(truth)); avg > 0.5 {
		t.Fatalf("average over-count %.2f per key with 4096 counters and 600 keys; the conservative update should keep it tiny", avg)
	}
}

func TestSketch_SaturatesAtMaxCount(t *testing.T) {
	s := NewSketch(1024, 1_000_000)
	for i := 0; i < 100; i++ {
		s.Increment("a")
	}
	if e := s.Estimate("a"); e != MaxCount {
		t.Fatalf("Estimate after 100 increments = %d, want the saturation value %d (counters are small on purpose)", e, MaxCount)
	}
}

func TestSketch_UnseenKeysEstimateNearZero(t *testing.T) {
	s := NewSketch(8192, 1_000_000)
	for i := 0; i < 500; i++ {
		for j := 0; j < 3; j++ {
			s.Increment(fmt.Sprintf("seen-%d", i))
		}
	}
	total := 0
	for i := 0; i < 500; i++ {
		total += s.Estimate(fmt.Sprintf("unseen-%d", i))
	}
	if avg := float64(total) / 500; avg > 0.2 {
		t.Fatalf("average estimate for never-seen keys = %.2f, want ≈ 0: the four rows must use independent hashes and the min must be taken", avg)
	}
}

func TestSketch_HalvesAfterSampleSize(t *testing.T) {
	s := NewSketch(1024, 10)
	for i := 0; i < 9; i++ {
		s.Increment("a")
	}
	if e := s.Estimate("a"); e != 9 {
		t.Fatalf("Estimate before the 10th increment = %d, want 9", e)
	}
	s.Increment("a") // 10th call: counters hit 10, then every counter is halved
	if e := s.Estimate("a"); e != 5 {
		t.Fatalf("Estimate right after the sample boundary = %d, want 5 (10 halved): aging is what lets old popularity fade", e)
	}
	for i := 0; i < 4; i++ {
		s.Increment("a")
	}
	if e := s.Estimate("a"); e != 9 {
		t.Fatalf("Estimate = %d, want 9 (5 + 4; the call counter must restart after a halving)", e)
	}
}

func TestSketch_HeavyKeyDoesNotInflateOthers(t *testing.T) {
	s := NewSketch(4096, 1_000_000)
	for i := 0; i < 1000; i++ {
		s.Increment("heavy")
	}
	s.Increment("light")
	if e := s.Estimate("light"); e > 2 {
		t.Fatalf("Estimate(light) = %d after one increment; a saturated neighbour must not leak into it", e)
	}
}

// ---------- cache ----------

func TestCache_BasicSetGetAndLRUAmongResidents(t *testing.T) {
	c := New[int](Config{Capacity: 3})
	for _, k := range []string{"a", "b", "c"} {
		if !c.Set(k, 1) {
			t.Fatalf("Set(%s) rejected although the cache has room", k)
		}
	}
	c.Get("a")
	if got := fmt.Sprint(c.Keys()); got != "[a c b]" {
		t.Fatalf("Keys() = %s, want [a c b]: a hit promotes", got)
	}
	if !c.Set("a", 2) {
		t.Fatalf("overwriting a resident key must succeed")
	}
	if v, _ := c.Get("a"); v != 2 {
		t.Fatalf("Get(a) = %d, want 2", v)
	}
	if c.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", c.Len())
	}
}

func TestCache_GetRecordsFrequencyEvenOnMiss(t *testing.T) {
	c := New[int](Config{Capacity: 4})
	for i := 0; i < 5; i++ {
		c.Get("wanted")
	}
	if f := c.Frequency("wanted"); f != 5 {
		t.Fatalf("Frequency after 5 missing Gets = %d, want 5: misses are how a key earns admission", f)
	}
	if f := c.Frequency("other"); f != 0 {
		t.Fatalf("Frequency of an untouched key = %d, want 0", f)
	}
	if s := c.Stats(); s.Misses != 5 || s.Hits != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestCache_SetDoesNotRecordFrequency(t *testing.T) {
	c := New[int](Config{Capacity: 4})
	c.Set("a", 1)
	if f := c.Frequency("a"); f != 0 {
		t.Fatalf("Set changed the sketch (Frequency = %d); only Get records accesses", f)
	}
}

func TestCache_AdmitsOnlyWhenCandidateIsMoreFrequentThanVictim(t *testing.T) {
	c := New[int](Config{Capacity: 2})
	c.Set("a", 1)
	c.Set("b", 1)
	c.Get("a") // a: frequency 1
	for i := 0; i < 3; i++ {
		c.Get("b") // b: frequency 3, and the most recently used
	}
	if got := fmt.Sprint(c.Keys()); got != "[b a]" {
		t.Fatalf("setup: Keys() = %s, want [b a] so that a is the LRU victim with frequency 1", got)
	}
	if c.Set("newcomer", 1) {
		t.Fatalf("a never-requested key displaced a resident: admission requires Estimate(candidate) > Estimate(victim)")
	}
	c.Get("newcomer") // frequency 1 == the victim's 1
	if c.Set("newcomer", 1) {
		t.Fatalf("a tie in frequency must keep the incumbent")
	}
	c.Get("newcomer") // frequency 2 > 1
	if !c.Set("newcomer", 1) {
		t.Fatalf("a candidate more frequent than the victim must be admitted")
	}
	if got := fmt.Sprint(c.Keys()); got != "[newcomer b]" {
		t.Fatalf("Keys() = %s, want the victim (a) replaced: [newcomer b]", got)
	}
	if s := c.Stats(); s.Evictions != 1 || s.Rejections != 2 {
		t.Fatalf("stats %+v, want Evictions 1 and Rejections 2", s)
	}
}

func TestCache_ScanResistanceBeatsLRU(t *testing.T) {
	const capacity = 100
	run := func(access func(string) bool) (hotHitRatio float64) {
		// Phase 1: a hot set of 60 keys, each requested 20 times.
		for round := 0; round < 20; round++ {
			for i := 0; i < 60; i++ {
				access(tHot(i))
			}
		}
		// Phase 2: a scan of 800 distinct keys, each requested once
		// (think: a nightly export, a crawler, a statement-generation job).
		for i := 0; i < 800; i++ {
			access(tCold(i))
		}
		// Phase 3: the hot set comes back (one pass; a second pass would
		// let even a flushed LRU refill itself).
		hits, total := 0, 0
		for i := 0; i < 60; i++ {
			total++
			if access(tHot(i)) {
				hits++
			}
		}
		return float64(hits) / float64(total)
	}
	lru := tNewLRU(capacity)
	lruRatio := run(func(k string) bool { return tAccessLRU(lru, k) })
	tiny := New[int](Config{Capacity: capacity})
	tinyRatio := run(func(k string) bool { return tAccess(tiny, k) })
	t.Logf("hot-set hit ratio after the scan: LRU %.2f, admission-filtered %.2f", lruRatio, tinyRatio)
	if lruRatio > 0.1 {
		t.Fatalf("test bug: the baseline LRU kept %.2f of the hot set; the scan should flush it", lruRatio)
	}
	if tinyRatio < 0.9 {
		t.Fatalf("hot-set hit ratio after a scan = %.2f, want >= 0.90: one-hit-wonders must lose the admission contest against frequently used residents", tinyRatio)
	}
	if st := tiny.Stats(); st.Rejections < 700 {
		t.Fatalf("only %d rejections for an 800-key scan", st.Rejections)
	}
}

func TestCache_AgingLetsANewHotSetReplaceAnOldOne(t *testing.T) {
	const capacity = 20
	c := New[int](Config{Capacity: capacity}) // sample size = 200 accesses
	for round := 0; round < 40; round++ {
		for i := 0; i < capacity; i++ {
			tAccess(c, tHot(i)) // old hot set: saturates the counters
		}
	}
	// The workload changes: a different set of 20 keys becomes popular and the old set is never used again.
	hits, total := 0, 0
	for round := 0; round < 150; round++ {
		for i := 0; i < capacity; i++ {
			hit := tAccess(c, fmt.Sprintf("new-%02d", i))
			if round >= 100 {
				total++
				if hit {
					hits++
				}
			}
		}
	}
	if ratio := float64(hits) / float64(total); ratio < 0.9 {
		t.Fatalf("hit ratio on the new hot set = %.2f after 100 rounds, want >= 0.90: without halving, the old set's saturated counters keep newcomers out forever", ratio)
	}
}

func TestCache_Concurrent(t *testing.T) {
	c := New[int](Config{Capacity: 64})
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 3000; i++ {
				k := fmt.Sprint(rng.Intn(300))
				if rng.Intn(3) == 0 {
					c.Set(k, i)
				} else {
					c.Get(k)
				}
				if i%500 == 0 {
					c.Keys()
					c.Frequency(k)
				}
			}
		}(w)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("concurrent workload did not finish (deadlock?)")
	}
	if c.Len() > 64 {
		t.Fatalf("Len() = %d > capacity 64", c.Len())
	}
	if len(c.Keys()) != c.Len() {
		t.Fatalf("Keys/Len drift: %d vs %d", len(c.Keys()), c.Len())
	}
}
