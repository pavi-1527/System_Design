//go:build breakit

package hotkey

// Break-it experiments for A4 Unit 3 (§8). Measurements, not pass/fail
// tests: they print tables. Predict the numbers before running.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a4/hotkey/

import (
	"sort"
	"testing"
)

// TestBreakIt_FanoutVersusReadCost: splitting a hot key over F physical keys
// flattens write load but every read of that key now touches F+1 keys. Where
// is the sweet spot for 8 partitions?
func TestBreakIt_FanoutVersusReadCost(t *testing.T) {
	const partitions = 8
	stream := tZipf(100000, 3000, 1.4)
	t.Logf("%7s %22s %18s", "fanout", "busiest partition", "keys per hot read")
	for _, f := range []int{1, 2, 4, 8, 16, 64} {
		d := NewDetector(DetectorConfig{Capacity: 64, Threshold: 0.02, MinCount: 50})
		r := NewRouter(d, f, nil)
		load := make([]int, partitions)
		for _, k := range stream {
			d.Observe(k)
			load[PartitionOf(r.WriteKey(k), partitions)]++
		}
		sort.Ints(load)
		t.Logf("%7d %21.1f%% %18d", f, 100*float64(load[partitions-1])/float64(len(stream)), len(r.ReadKeys("key-0")))
	}
	t.Logf("ideal share with perfect balance: %.1f%%", 100.0/partitions)
}

// TestBreakIt_SummarySizeVersusRecall: how many Space-Saving counters do you
// need before the true top-10 keys are all reported, in the right order?
func TestBreakIt_SummarySizeVersusRecall(t *testing.T) {
	stream := tZipf(200000, 20000, 1.05)
	truth := tCounts(stream)
	type kv struct {
		k string
		n uint64
	}
	var all []kv
	for k, n := range truth {
		all = append(all, kv{k, n})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].k < all[j].k
	})
	trueTop := map[string]bool{}
	for _, e := range all[:10] {
		trueTop[e.k] = true
	}
	t.Logf("%9s %16s %22s", "counters", "top-10 recalled", "max overcount on top-1")
	for _, capacity := range []int{10, 20, 50, 100, 500, 2000} {
		s := NewSpaceSaving(capacity)
		for _, k := range stream {
			s.Observe(k)
		}
		hit := 0
		for _, it := range s.Top(10) {
			if trueTop[it.Key] {
				hit++
			}
		}
		top := s.Top(1)[0]
		t.Logf("%9d %13d/10 %22d", capacity, hit, top.Count-truth[top.Key])
	}
}
