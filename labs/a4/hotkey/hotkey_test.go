package hotkey

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"
)

// ---------- helpers (prefixed with t to avoid clashing with your code) ----------

// tZipf returns n keys drawn from a Zipf distribution over `universe` keys
// with a fixed seed: "key-0" is by far the most frequent.
func tZipf(n, universe int, s float64) []string {
	r := rand.New(rand.NewSource(42))
	z := rand.NewZipf(r, s, 1, uint64(universe-1))
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("key-%d", z.Uint64())
	}
	return out
}

func tCounts(stream []string) map[string]uint64 {
	m := map[string]uint64{}
	for _, k := range stream {
		m[k]++
	}
	return m
}

func tWithin(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("timed out after %v (deadlock?)", d)
	}
}

// ---------- Space-Saving ----------

func TestSpaceSaving_EvictsSmallestCounterAndInheritsItsCountAsError(t *testing.T) {
	s := NewSpaceSaving(2)
	for _, k := range []string{"a", "a", "b"} {
		s.Observe(k)
	}
	s.Observe("c") // evicts b (count 1); c inherits 1 and counts itself: 2, err 1
	if _, _, ok := s.Estimate("b"); ok {
		t.Error("b should have been evicted: it had the smallest count")
	}
	if c, e, ok := s.Estimate("c"); !ok || c != 2 || e != 1 {
		t.Errorf("c = (count %d, err %d, ok %v), want (2, 1, true): the newcomer inherits the evicted count as its error", c, e, ok)
	}
	// a and c are tied at 2: the smaller key (a) is evicted next.
	s.Observe("d")
	if _, _, ok := s.Estimate("a"); ok {
		t.Error("a should have been evicted (tie at count 2, smaller key goes first)")
	}
	if c, e, ok := s.Estimate("d"); !ok || c != 3 || e != 2 {
		t.Errorf("d = (%d, %d, %v), want (3, 2, true)", c, e, ok)
	}
	if s.Total() != 5 {
		t.Errorf("Total = %d, want 5", s.Total())
	}
}

func TestSpaceSaving_NeverExceedsCapacity(t *testing.T) {
	s := NewSpaceSaving(10)
	for i := 0; i < 5000; i++ {
		s.Observe(fmt.Sprintf("distinct-%d", i))
	}
	if n := len(s.Top(1000)); n != 10 {
		t.Errorf("%d counters monitored, want exactly the capacity 10 (memory must be bounded)", n)
	}
}

func TestSpaceSaving_GuaranteesOnAZipfStream(t *testing.T) {
	const capacity = 50
	stream := tZipf(50000, 5000, 1.1)
	truth := tCounts(stream)
	s := NewSpaceSaving(capacity)
	for _, k := range stream {
		s.Observe(k)
	}
	if s.Total() != uint64(len(stream)) {
		t.Fatalf("Total = %d, want %d", s.Total(), len(stream))
	}
	for _, it := range s.Top(capacity) {
		real := truth[it.Key]
		if it.Count < real {
			t.Errorf("%s: Count %d < true %d; Count must be an upper bound", it.Key, it.Count, real)
		}
		if it.Count-it.Err > real {
			t.Errorf("%s: Count-Err %d > true %d; it must be a lower bound", it.Key, it.Count-it.Err, real)
		}
	}
	// Any key with frequency > total/capacity must be monitored.
	for k, c := range truth {
		if c > uint64(len(stream))/capacity {
			if _, _, ok := s.Estimate(k); !ok {
				t.Errorf("%s occurs %d times (> total/capacity = %d) but is not monitored", k, c, len(stream)/capacity)
			}
		}
	}
	top := s.Top(3)
	if len(top) != 3 || top[0].Key != "key-0" || top[0].Count < top[1].Count || top[1].Count < top[2].Count {
		t.Errorf("Top(3) = %+v, want key-0 first and counts in descending order", top)
	}
	if got := s.Top(0); got != nil {
		t.Errorf("Top(0) = %v, want nil", got)
	}
}

func TestSpaceSaving_ObserveNAndReset(t *testing.T) {
	s := NewSpaceSaving(3)
	s.ObserveN("a", 10)
	s.ObserveN("a", 0)
	s.ObserveN("b", 4)
	if c, _, _ := s.Estimate("a"); c != 10 || s.Total() != 14 {
		t.Errorf("count(a)=%d total=%d, want 10 and 14", c, s.Total())
	}
	s.Reset()
	if s.Total() != 0 || len(s.Top(5)) != 0 {
		t.Error("Reset did not clear the summary")
	}
}

func TestSpaceSaving_ConcurrentObserve(t *testing.T) {
	s := NewSpaceSaving(20)
	tWithin(t, 20*time.Second, func() {
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 2000; i++ {
					s.Observe(fmt.Sprintf("k%d", (i*g)%50))
					if i%100 == 0 {
						_ = s.Top(5)
					}
				}
			}(g)
		}
		wg.Wait()
	})
	if s.Total() != 16000 {
		t.Errorf("Total = %d, want 16000", s.Total())
	}
}

// ---------- Count-Min ----------

func TestCountMin_NeverUnderestimatesAndIsAccurateForHeavyKeys(t *testing.T) {
	stream := tZipf(40000, 4000, 1.1)
	truth := tCounts(stream)
	cm := NewCountMin(512, 4)
	for _, k := range stream {
		cm.Add(k, 1)
	}
	if cm.Total() != 40000 {
		t.Fatalf("Total = %d, want 40000", cm.Total())
	}
	bound := uint64(212) // e/width × N = 2.718 × 40000 / 512 ≈ 212
	over := 0
	for k, c := range truth {
		est := cm.Estimate(k)
		if est < c {
			t.Fatalf("Estimate(%s) = %d < true count %d; a Count-Min sketch must never underestimate", k, est, c)
		}
		if est-c > bound {
			over++
		}
	}
	if frac := float64(over) / float64(len(truth)); frac > 0.02 {
		t.Errorf("%.1f%% of keys exceed the error bound e·N/width = %d; with depth 4 about e^-4 ≈ 2%% may (is every row using the same column?)", 100*frac, bound)
	}
	if est := cm.Estimate("key-0"); est < truth["key-0"] || est > truth["key-0"]+bound {
		t.Errorf("hottest key: estimate %d, true %d, allowed error %d", est, truth["key-0"], bound)
	}
}

func TestCountMin_DepthMattersAndAddAccumulates(t *testing.T) {
	stream := tZipf(40000, 4000, 1.1)
	truth := tCounts(stream)
	worstError := func(depth int) uint64 {
		cm := NewCountMin(256, depth)
		for _, k := range stream {
			cm.Add(k, 1)
		}
		var worst uint64
		for k, c := range truth {
			if d := cm.Estimate(k) - c; d > worst {
				worst = d
			}
		}
		return worst
	}
	d1, d5 := worstError(1), worstError(5)
	if d5*2 > d1 {
		t.Errorf("worst-case error with depth 5 (%d) should be far smaller than with depth 1 (%d): taking the minimum over independent rows is the point", d5, d1)
	}
	cm := NewCountMin(64, 3)
	cm.Add("x", 5)
	cm.Add("x", 7)
	if got := cm.Estimate("x"); got != 12 {
		t.Errorf("Estimate after Add 5 and 7 = %d, want 12 (sparse sketch, no collisions)", got)
	}
	if got := cm.Estimate("never-seen"); got > 12 {
		t.Errorf("Estimate of an unseen key = %d", got)
	}
}

func TestCountMin_ConcurrentAdd(t *testing.T) {
	cm := NewCountMin(128, 4)
	tWithin(t, 20*time.Second, func() {
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 1000; i++ {
					cm.Add("hot", 1)
					_ = cm.Estimate("hot")
				}
			}()
		}
		wg.Wait()
	})
	if got := cm.Estimate("hot"); got != 8000 {
		t.Errorf("Estimate(hot) = %d, want 8000", got)
	}
}

// ---------- Detector ----------

func TestDetector_FlagsHeavyKeysNotTheTail(t *testing.T) {
	d := NewDetector(DetectorConfig{Capacity: 32, Threshold: 0.05, MinCount: 20})
	for _, k := range tZipf(30000, 2000, 1.2) {
		d.Observe(k)
	}
	if !d.IsHot("key-0") {
		t.Error("key-0 carries a large share of the stream and should be hot")
	}
	if d.IsHot("key-1999") || d.IsHot("never-seen") {
		t.Error("tail keys must not be hot")
	}
	hot := d.HotKeys()
	if len(hot) == 0 || hot[0] != "key-0" {
		t.Errorf("HotKeys = %v, want key-0 first", hot)
	}
	for _, k := range hot {
		if !d.IsHot(k) {
			t.Errorf("%s listed in HotKeys but IsHot is false", k)
		}
	}
}

func TestDetector_UsesGuaranteedCountSoCrowdedSummariesDoNotInventHotKeys(t *testing.T) {
	// 4 counters, thousands of distinct keys: every monitored counter has
	// inherited a large Count, but its guaranteed part (Count-Err) is tiny.
	d := NewDetector(DetectorConfig{Capacity: 4, Threshold: 0.05, MinCount: 20})
	for i := 0; i < 4000; i++ {
		d.Observe(fmt.Sprintf("distinct-%d", i))
	}
	if hot := d.HotKeys(); len(hot) != 0 {
		t.Errorf("HotKeys = %v on a stream of all-distinct keys; judge by Count-Err, not Count (inherited counts are not evidence)", hot)
	}
}

func TestDetector_NeedsMinCountAndThreshold(t *testing.T) {
	d := NewDetector(DetectorConfig{Capacity: 8, Threshold: 0.5, MinCount: 20})
	for i := 0; i < 10; i++ {
		d.Observe("a") // 100% of traffic but only 10 requests
	}
	if d.IsHot("a") {
		t.Error("a has 100% share but only 10 requests (< MinCount 20): not hot")
	}
	for i := 0; i < 20; i++ {
		d.Observe("a")
	}
	for i := 0; i < 40; i++ {
		d.Observe("b")
	}
	// a: 30 of 70 = 43% < 50% threshold; b: 40 of 70 = 57% ≥ 50%.
	if d.IsHot("a") || !d.IsHot("b") {
		t.Errorf("IsHot(a)=%v IsHot(b)=%v, want false and true (thresholds: 50%% share and 20 requests)", d.IsHot("a"), d.IsHot("b"))
	}
	if z := NewDetector(DetectorConfig{}); z.cfg.Capacity != 64 || z.cfg.Threshold != 0.05 || z.cfg.MinCount != 20 {
		t.Errorf("defaults = %+v, want capacity 64, threshold 0.05, min count 20", z.cfg)
	}
}

func TestDetector_RotateKeepsLastWindowsHotKeysForOneMoreWindow(t *testing.T) {
	d := NewDetector(DetectorConfig{Capacity: 8, Threshold: 0.2, MinCount: 10})
	for i := 0; i < 100; i++ {
		d.Observe("celebrity")
	}
	for i := 0; i < 20; i++ {
		d.Observe(fmt.Sprintf("other-%d", i))
	}
	if !d.IsHot("celebrity") {
		t.Fatal("celebrity should be hot in window 1")
	}
	d.Rotate()
	if !d.IsHot("celebrity") {
		t.Error("right after Rotate the previous window's hot key must stay hot (no flapping while the new window fills)")
	}
	for i := 0; i < 50; i++ {
		d.Observe(fmt.Sprintf("quiet-%d", i))
	}
	d.Rotate() // window 2 had no celebrity traffic
	if d.IsHot("celebrity") {
		t.Error("after a full window without traffic the key must cool down")
	}
}

func TestDetector_ConcurrentUse(t *testing.T) {
	d := NewDetector(DetectorConfig{Capacity: 16, Threshold: 0.1, MinCount: 10})
	tWithin(t, 20*time.Second, func() {
		var wg sync.WaitGroup
		for g := 0; g < 6; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 1500; i++ {
					if i%3 == 0 {
						d.Observe("hot")
					} else {
						d.Observe(fmt.Sprintf("k-%d-%d", g, i%40))
					}
					_ = d.IsHot("hot")
					if i%500 == 0 {
						d.Rotate()
					}
				}
			}(g)
		}
		wg.Wait()
	})
}

// ---------- Router ----------

func TestRouter_ColdKeysAreUntouched(t *testing.T) {
	d := NewDetector(DetectorConfig{})
	r := NewRouter(d, 4, nil)
	if got := r.WriteKey("acct-1"); got != "acct-1" {
		t.Errorf("WriteKey(cold) = %q, want the key itself", got)
	}
	if got := r.ReadKeys("acct-1"); len(got) != 1 || got[0] != "acct-1" {
		t.Errorf("ReadKeys(cold) = %v, want [acct-1]", got)
	}
}

func TestRouter_HotKeysSplitOnWriteAndFanInOnRead(t *testing.T) {
	d := NewDetector(DetectorConfig{Capacity: 8, Threshold: 0.1, MinCount: 10})
	for i := 0; i < 200; i++ {
		d.Observe("celebrity")
	}
	script := []int{2, 0, 3, 1}
	i := 0
	r := NewRouter(d, 4, func(n int) int {
		if n != 4 {
			t.Errorf("rand called with n=%d, want the fanout 4", n)
		}
		v := script[i%len(script)]
		i++
		return v
	})
	want := []string{"celebrity#2", "celebrity#0", "celebrity#3", "celebrity#1"}
	for j, w := range want {
		if got := r.WriteKey("celebrity"); got != w {
			t.Errorf("write %d went to %q, want %q", j, got, w)
		}
	}
	reads := r.ReadKeys("celebrity")
	wantReads := []string{"celebrity", "celebrity#0", "celebrity#1", "celebrity#2", "celebrity#3"}
	if fmt.Sprint(reads) != fmt.Sprint(wantReads) {
		t.Errorf("ReadKeys = %v, want %v: the plain key is included because writes made before the key became hot live there", reads, wantReads)
	}
	if SplitKey("k", 7) != "k#7" {
		t.Errorf("SplitKey format changed: %q", SplitKey("k", 7))
	}
}

func TestRouter_DefaultSpreadsEvenlyAndFanoutOneIsANoOp(t *testing.T) {
	d := NewDetector(DetectorConfig{Capacity: 8, Threshold: 0.1, MinCount: 10})
	for i := 0; i < 100; i++ {
		d.Observe("hot")
	}
	r := NewRouter(d, 5, nil)
	seen := map[string]int{}
	for i := 0; i < 500; i++ {
		seen[r.WriteKey("hot")]++
	}
	if len(seen) != 5 {
		t.Fatalf("writes went to %d distinct keys, want 5: %v", len(seen), seen)
	}
	for k, n := range seen {
		if n != 100 {
			t.Errorf("%s got %d writes, want 100 (round robin)", k, n)
		}
	}
	one := NewRouter(d, 1, nil)
	if one.WriteKey("hot") != "hot" || len(one.ReadKeys("hot")) != 1 {
		t.Error("fanout 1 must leave keys alone")
	}
	if zero := NewRouter(d, 0, nil); zero.WriteKey("hot") != "hot" {
		t.Error("fanout 0 is treated as 1")
	}
}

func TestRouter_SplittingFlattensPartitionLoadUnderZipfSkew(t *testing.T) {
	const partitions = 8
	stream := tZipf(80000, 3000, 1.4)
	maxShare := func(route func(string) string, observe func(string)) float64 {
		load := make([]int, partitions)
		for _, k := range stream {
			observe(k)
			load[PartitionOf(route(k), partitions)]++
		}
		sort.Ints(load)
		return float64(load[partitions-1]) / float64(len(stream))
	}
	plain := maxShare(func(k string) string { return k }, func(string) {})
	d := NewDetector(DetectorConfig{Capacity: 64, Threshold: 0.02, MinCount: 50})
	router := NewRouter(d, 8, nil)
	split := maxShare(router.WriteKey, d.Observe)
	ideal := 1.0 / partitions
	if plain < 2*ideal {
		t.Fatalf("test setup: unsplit max partition share is %.2f; the stream should be skewed (ideal %.3f)", plain, ideal)
	}
	if split > plain*0.6 || split > 3*ideal {
		t.Errorf("busiest partition takes %.1f%% of writes with splitting vs %.1f%% without (ideal %.1f%%); expected a sharp drop once the hot keys are detected", 100*split, 100*plain, 100*ideal)
	}
}

func TestPartitionOf_IsDeterministicAndInRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		k := fmt.Sprintf("k%d", i)
		p := PartitionOf(k, 7)
		if p < 0 || p >= 7 || p != PartitionOf(k, 7) {
			t.Fatalf("PartitionOf(%s, 7) = %d", k, p)
		}
	}
	if PartitionOf("x", 0) != 0 {
		t.Error("PartitionOf with no partitions must return 0")
	}
}
